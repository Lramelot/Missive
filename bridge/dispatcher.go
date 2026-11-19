package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"slices"
	"sync"
	"time"

	"github.com/rs/zerolog"
)

type Emitter interface {
	Emit(event string, data any)
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

const (
	maxListLimit          = 500
	maxConcurrentRequests = 64
)

type Dispatcher struct {
	backend Backend
	out     *Output
	log     zerolog.Logger
	ctx     context.Context
	cancel  context.CancelFunc
	wg      sync.WaitGroup
	lanesMu sync.Mutex
	lanes   map[string]chan struct{}
	slots   chan struct{}
}

func NewDispatcher(backend Backend, out *Output, log zerolog.Logger) *Dispatcher {
	ctx, cancel := context.WithCancel(context.Background())
	return &Dispatcher{backend: backend, out: out, log: log, ctx: ctx, cancel: cancel, slots: make(chan struct{}, maxConcurrentRequests)}
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
	d.cancel()
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
			select {
			case <-wait:
			case <-d.ctx.Done():
				d.out.respondErr(req.ID, newError(CodeInternal, "bridge is shutting down"))
				return
			}
		}
		select {
		case d.slots <- struct{}{}:
		case <-d.ctx.Done():
			d.out.respondErr(req.ID, newError(CodeInternal, "bridge is shutting down"))
			return
		}
		defer func() { <-d.slots }()
		defer func() {
			if p := recover(); p != nil {
				logRecovered(d.log, "request "+req.Cmd, p)
				d.out.respondErr(req.ID, newError(CodeInternal, "internal error: %s", panicSummary(p)))
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
