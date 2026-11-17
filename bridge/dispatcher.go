package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/rs/zerolog"
)

type Emitter interface {
	Emit(event string, data any)
}

type Backend interface {
	Status(ctx context.Context) (StatusResult, error)
	Login(ctx context.Context, cookies map[string]string) (User, error)
	Logout(ctx context.Context) error
	Connect(ctx context.Context) error
	Threads(ctx context.Context, limit int) ([]Thread, error)
	Messages(ctx context.Context, threadID string, beforeMS int64, limit int, fetchMissing bool) ([]Message, error)
	Send(ctx context.Context, threadID, text, replyToID, clientID string, mentions []Mention) (string, error)
	MarkRead(ctx context.Context, threadID string) error
	Media(ctx context.Context, threadID, messageID, attachmentID, variant string) (MediaResult, error)
	Avatar(ctx context.Context, url string) (string, error)
	SendMedia(ctx context.Context, threadID string, files []sendFile, caption, replyToID string, clientIDs []string) ([]string, error)
	React(ctx context.Context, threadID, messageID, emoji string) error
	SetTyping(ctx context.Context, threadID string, typing bool) error
	SetActive(ctx context.Context, active bool) error
	Search(ctx context.Context, query, threadID string, limit int) ([]SearchHit, error)
	MediaHistory(ctx context.Context, threadID string, beforeMS int64, limit int, kinds []string, maxPages int) (MediaHistoryResult, error)
	HistoryStatus(ctx context.Context) (HistoryStatusResult, error)
	RestoreHistory(ctx context.Context, pin string) error
	Close()
}

type Output struct {
	mu  sync.Mutex
	w   *bufio.Writer
	log zerolog.Logger
}

func NewOutput(w io.Writer, log zerolog.Logger) *Output {
	return &Output{w: bufio.NewWriter(w), log: log}
}

func (o *Output) write(v any) {
	data, err := json.Marshal(v)
	if err != nil {
		o.log.Err(err).Msg("Failed to marshal protocol line")
		return
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	o.w.Write(data)
	o.w.WriteByte('\n')
	if err = o.w.Flush(); err != nil {
		o.log.Err(err).Msg("Failed to write protocol line")
	}
}

func (o *Output) Emit(event string, data any) {
	if data == nil {
		data = Empty{}
	}
	o.write(eventEnvelope{Event: event, Data: data})
}

func (o *Output) respondOK(id int64, result any) {
	if result == nil {
		result = Empty{}
	}
	o.write(okResponse{ID: id, OK: true, Result: result})
}

func (o *Output) respondErr(id int64, err error) {
	o.write(errResponse{ID: id, OK: false, Error: toErrorBody(err)})
}

type Dispatcher struct {
	backend Backend
	out     *Output
	log     zerolog.Logger
	ctx     context.Context
	cancel  context.CancelFunc
	wg      sync.WaitGroup
	lanesMu sync.Mutex
	lanes   map[string]chan struct{}
}

func NewDispatcher(backend Backend, out *Output, log zerolog.Logger) *Dispatcher {
	ctx, cancel := context.WithCancel(context.Background())
	return &Dispatcher{backend: backend, out: out, log: log, ctx: ctx, cancel: cancel}
}

func (d *Dispatcher) Run(r io.Reader) {
	reader := bufio.NewReaderSize(r, 64*1024)
	for {
		line, err := reader.ReadBytes('\n')
		if len(bytes.TrimSpace(line)) > 0 {
			d.HandleLine(line)
		}
		if err != nil {
			if err != io.EOF {
				d.log.Err(err).Msg("Failed to read stdin")
			}
			return
		}
	}
}

func (d *Dispatcher) Shutdown(timeout time.Duration) {
	done := make(chan struct{})
	go func() {
		d.wg.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(timeout):
		d.log.Warn().Msg("Timed out waiting for in-flight requests")
	}
	d.cancel()
	d.backend.Close()
}

func (d *Dispatcher) Wait() {
	d.wg.Wait()
}

func (d *Dispatcher) HandleLine(line []byte) {
	line = bytes.TrimSpace(line)
	var req Request
	if err := json.Unmarshal(line, &req); err != nil {
		var partial struct {
			ID *int64 `json:"id"`
		}
		if json.Unmarshal(line, &partial) == nil && partial.ID != nil {
			d.out.respondErr(*partial.ID, newError(CodeBadRequest, "invalid request: %v", err))
			return
		}
		d.log.Warn().Err(err).Int("line_length", len(line)).Msg("Ignoring unparseable request line")
		return
	}
	if req.ID <= 0 {
		d.out.respondErr(req.ID, newError(CodeBadRequest, "id must be a positive integer"))
		return
	}
	wait, release := d.enterLane(&req)
	d.wg.Add(1)
	go func() {
		defer d.wg.Done()
		defer release()
		if wait != nil {
			<-wait
		}
		defer func() {
			if p := recover(); p != nil {
				d.log.Error().Any("panic", p).Str("cmd", req.Cmd).Msg("Panic while handling request")
				d.out.respondErr(req.ID, newError(CodeInternal, "panic: %v", p))
			}
		}()
		result, err := d.dispatch(d.ctx, &req)
		if err != nil {
			level := zerolog.DebugLevel
			if toErrorBody(err).Code == CodeInternal {
				level = zerolog.WarnLevel
			}
			d.log.WithLevel(level).Err(err).Int64("id", req.ID).Str("cmd", req.Cmd).Msg("Request failed")
			d.out.respondErr(req.ID, err)
			return
		}
		d.out.respondOK(req.ID, result)
	}()
}

var serializedCommands = map[string]bool{"send": true, "send_media": true, "react": true, "send_voice": true, "forward": true}

func laneKeys(req *Request) []string {
	var p struct {
		ThreadID    string   `json:"thread_id"`
		ToThreadIDs []string `json:"to_thread_ids"`
	}
	if json.Unmarshal(req.Params, &p) != nil {
		return nil
	}
	var keys []string
	for _, k := range append([]string{p.ThreadID}, p.ToThreadIDs...) {
		if k != "" && !slices.Contains(keys, k) {
			keys = append(keys, k)
		}
	}
	return keys
}

func (d *Dispatcher) enterLane(req *Request) (<-chan struct{}, func()) {
	if !serializedCommands[req.Cmd] {
		return nil, func() {}
	}
	keys := laneKeys(req)
	if len(keys) == 0 {
		return nil, func() {}
	}
	done := make(chan struct{})
	var prevs []chan struct{}
	d.lanesMu.Lock()
	if d.lanes == nil {
		d.lanes = make(map[string]chan struct{})
	}
	for _, k := range keys {
		if prev := d.lanes[k]; prev != nil {
			prevs = append(prevs, prev)
		}
		d.lanes[k] = done
	}
	d.lanesMu.Unlock()
	release := func() {
		close(done)
		d.lanesMu.Lock()
		for _, k := range keys {
			if d.lanes[k] == done {
				delete(d.lanes, k)
			}
		}
		d.lanesMu.Unlock()
	}
	switch len(prevs) {
	case 0:
		return nil, release
	case 1:
		return prevs[0], release
	}
	all := make(chan struct{})
	go func() {
		for _, p := range prevs {
			<-p
		}
		close(all)
	}()
	return all, release
}

func decodeParams(raw json.RawMessage, into any) error {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) {
		return nil
	}
	if trimmed[0] != '{' {
		return newError(CodeBadRequest, "params must be an object")
	}
	if err := json.Unmarshal(trimmed, into); err != nil {
		return newError(CodeBadRequest, "invalid params: %v", err)
	}
	return nil
}

func (d *Dispatcher) dispatch(ctx context.Context, req *Request) (any, error) {
	ctx = sendReadContext(ctx, req)
	switch req.Cmd {
	case "status":
		if err := decodeParams(req.Params, &struct{}{}); err != nil {
			return nil, err
		}
		return d.backend.Status(ctx)
	case "login":
		var p LoginParams
		if err := decodeParams(req.Params, &p); err != nil {
			return nil, err
		}
		if len(p.Cookies) == 0 {
			return nil, newError(CodeBadRequest, "cookies is required")
		}
		user, err := d.backend.Login(ctx, p.Cookies)
		if err != nil {
			return nil, err
		}
		return LoginResult{User: user}, nil
	case "logout":
		if err := decodeParams(req.Params, &struct{}{}); err != nil {
			return nil, err
		}
		return Empty{}, d.backend.Logout(ctx)
	case "connect":
		if err := decodeParams(req.Params, &struct{}{}); err != nil {
			return nil, err
		}
		return Empty{}, d.backend.Connect(ctx)
	case "threads":
		var p ThreadsParams
		if err := decodeParams(req.Params, &p); err != nil {
			return nil, err
		}
		limit := 100
		if p.Limit != nil {
			if *p.Limit <= 0 {
				return nil, newError(CodeBadRequest, "limit must be positive")
			}
			limit = *p.Limit
		}
		threads, err := d.backend.Threads(ctx, limit)
		if err != nil {
			return nil, err
		}
		if threads == nil {
			threads = []Thread{}
		}
		return threads, nil
	case "messages":
		var p MessagesParams
		if err := decodeParams(req.Params, &p); err != nil {
			return nil, err
		}
		if strings.TrimSpace(p.ThreadID) == "" {
			return nil, newError(CodeBadRequest, "thread_id is required")
		}
		limit := 50
		if p.Limit != nil {
			if *p.Limit <= 0 {
				return nil, newError(CodeBadRequest, "limit must be positive")
			}
			limit = *p.Limit
		}
		var before int64
		if p.BeforeMS != nil {
			if *p.BeforeMS <= 0 {
				return nil, newError(CodeBadRequest, "before_ms must be positive")
			}
			before = *p.BeforeMS
		}
		fetchMissing := p.FetchMissing == nil || *p.FetchMissing
		msgs, err := d.backend.Messages(ctx, p.ThreadID, before, limit, fetchMissing)
		if err != nil {
			return nil, err
		}
		out := make([]Message, len(msgs))
		for i, msg := range msgs {
			out[i] = msg.normalized()
		}
		return out, nil
	case "send":
		var p SendParams
		if err := decodeParams(req.Params, &p); err != nil {
			return nil, err
		}
		if strings.TrimSpace(p.ThreadID) == "" {
			return nil, newError(CodeBadRequest, "thread_id is required")
		}
		if p.Text == "" {
			return nil, newError(CodeBadRequest, "text is required")
		}
		id, err := d.backend.Send(ctx, p.ThreadID, p.Text, p.ReplyToID, p.ClientID, p.Mentions)
		if err != nil {
			return nil, err
		}
		return SendResult{MessageID: id}, nil
	case "mark_read":
		var p MarkReadParams
		if err := decodeParams(req.Params, &p); err != nil {
			return nil, err
		}
		if strings.TrimSpace(p.ThreadID) == "" {
			return nil, newError(CodeBadRequest, "thread_id is required")
		}
		return Empty{}, d.backend.MarkRead(ctx, p.ThreadID)
	case "media":
		var p MediaParams
		if err := decodeParams(req.Params, &p); err != nil {
			return nil, err
		}
		if strings.TrimSpace(p.ThreadID) == "" || strings.TrimSpace(p.MessageID) == "" || strings.TrimSpace(p.AttachmentID) == "" {
			return nil, newError(CodeBadRequest, "thread_id, message_id and attachment_id are required")
		}
		switch p.Variant {
		case "":
			p.Variant = VariantFull
		case VariantFull, VariantThumb:
		default:
			return nil, newError(CodeBadRequest, "unknown variant %q", p.Variant)
		}
		return d.backend.Media(ctx, p.ThreadID, p.MessageID, p.AttachmentID, p.Variant)
	case "avatar":
		var p AvatarParams
		if err := decodeParams(req.Params, &p); err != nil {
			return nil, err
		}
		if strings.TrimSpace(p.URL) == "" {
			return nil, newError(CodeBadRequest, "url is required")
		}
		path, err := d.backend.Avatar(ctx, p.URL)
		if err != nil {
			return nil, err
		}
		return AvatarResult{Path: path}, nil
	case "send_media":
		var p SendMediaParams
		if err := decodeParams(req.Params, &p); err != nil {
			return nil, err
		}
		if strings.TrimSpace(p.ThreadID) == "" || len(p.Paths) == 0 {
			return nil, newError(CodeBadRequest, "thread_id and paths are required")
		}
		files, err := loadSendFiles(p.Paths)
		if err != nil {
			return nil, err
		}
		if len(p.ClientIDs) > 0 && len(p.ClientIDs) != len(p.Paths) {
			return nil, newError(CodeBadRequest, "client_ids must have one entry per path")
		}
		ids, err := d.backend.SendMedia(ctx, p.ThreadID, files, p.Caption, p.ReplyToID, p.ClientIDs)
		if err != nil {
			return nil, err
		}
		if ids == nil {
			ids = []string{}
		}
		return SendMediaResult{MessageIDs: ids}, nil
	case "react":
		var p ReactParams
		if err := decodeParams(req.Params, &p); err != nil {
			return nil, err
		}
		if strings.TrimSpace(p.ThreadID) == "" || strings.TrimSpace(p.MessageID) == "" || p.Emoji == nil {
			return nil, newError(CodeBadRequest, "thread_id, message_id and emoji are required")
		}
		return Empty{}, d.backend.React(ctx, p.ThreadID, p.MessageID, *p.Emoji)
	case "set_typing":
		var p TypingParams
		if err := decodeParams(req.Params, &p); err != nil {
			return nil, err
		}
		if strings.TrimSpace(p.ThreadID) == "" {
			return nil, newError(CodeBadRequest, "thread_id is required")
		}
		return Empty{}, d.backend.SetTyping(ctx, p.ThreadID, p.Typing)
	case "set_active":
		var p struct {
			Active *bool `json:"active"`
		}
		if err := decodeParams(req.Params, &p); err != nil {
			return nil, err
		}
		if p.Active == nil {
			return nil, newError(CodeBadRequest, "active is required")
		}
		return Empty{}, d.backend.SetActive(ctx, *p.Active)
	case "search":
		var p SearchParams
		if err := decodeParams(req.Params, &p); err != nil {
			return nil, err
		}
		return d.search(ctx, p)
	case "media_history":
		var p MediaHistoryParams
		if err := decodeParams(req.Params, &p); err != nil {
			return nil, err
		}
		if strings.TrimSpace(p.ThreadID) == "" {
			return nil, newError(CodeBadRequest, "thread_id is required")
		}
		limit := 60
		if p.Limit != nil {
			if *p.Limit <= 0 {
				return nil, newError(CodeBadRequest, "limit must be positive")
			}
			limit = *p.Limit
		}
		var before int64
		if p.BeforeMS != nil {
			if *p.BeforeMS <= 0 {
				return nil, newError(CodeBadRequest, "before_ms must be positive")
			}
			before = *p.BeforeMS
		}
		kinds := p.Kinds
		if len(kinds) == 0 {
			kinds = []string{"image", "video"}
		}
		for _, kind := range kinds {
			switch kind {
			case "image", "video", "file", "link", "audio", "sticker", "other":
			default:
				return nil, newError(CodeBadRequest, "unknown kind %q", kind)
			}
		}
		maxPages := maxMediaHistoryPages
		if p.MaxPages != nil {
			if *p.MaxPages < 0 {
				return nil, newError(CodeBadRequest, "max_pages must not be negative")
			}
			maxPages = min(*p.MaxPages, maxMediaHistoryPages)
		}
		res, err := d.backend.MediaHistory(ctx, p.ThreadID, before, limit, kinds, maxPages)
		if err != nil {
			return nil, err
		}
		if res.Items == nil {
			res.Items = []MediaItem{}
		}
		return res, nil
	case "history_status":
		if err := decodeParams(req.Params, &struct{}{}); err != nil {
			return nil, err
		}
		return d.backend.HistoryStatus(ctx)
	case "restore_history":
		var p RestoreHistoryParams
		if err := decodeParams(req.Params, &p); err != nil {
			return nil, newError(CodeBadRequest, "invalid params")
		}
		if !validPIN(p.PIN) {
			return nil, newError(CodeBadRequest, "pin must be 6 digits")
		}
		return Empty{}, d.backend.RestoreHistory(ctx, p.PIN)
	case "diagnostics":
		if err := decodeParams(req.Params, &struct{}{}); err != nil {
			return nil, err
		}
		return d.diagnostics(ctx)
	case "":
		return nil, newError(CodeBadRequest, "cmd is required")
	default:
		if result, handled, err := d.dispatchV12(ctx, req); handled {
			return result, err
		}
		if result, handled, err := d.dispatchV11(ctx, req); handled {
			return result, err
		}
		return nil, newError(CodeBadRequest, "unknown command %q", req.Cmd)
	}
}

func validPIN(pin string) bool {
	if len(pin) != 6 {
		return false
	}
	for _, c := range pin {
		if c < '0' || c > '9' {
			return false
		}
	}
	return true
}
