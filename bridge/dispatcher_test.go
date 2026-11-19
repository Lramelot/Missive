package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/rs/zerolog"
)

type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.buf.Write(p)
}

func (s *syncBuffer) Lines() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	text := strings.TrimRight(s.buf.String(), "\n")
	if text == "" {
		return nil
	}
	return strings.Split(text, "\n")
}

type harness struct {
	t    *testing.T
	out  *syncBuffer
	disp *Dispatcher
	demo *DemoBackend
}

func newDemoHarness(t *testing.T) *harness {
	t.Helper()
	buf := &syncBuffer{}
	log := zerolog.Nop()
	out := NewOutput(buf, log)
	demo := NewDemoBackend(t.TempDir(), "", out, log)
	disp := NewDispatcher(demo, out, log)
	t.Cleanup(func() { disp.Shutdown(5 * time.Second) })
	return &harness{t: t, out: buf, disp: disp, demo: demo}
}

func (h *harness) send(lines ...string) {
	h.disp.Run(strings.NewReader(strings.Join(lines, "\n") + "\n"))
	h.disp.Wait()
}

func (h *harness) decoded() []map[string]json.RawMessage {
	h.t.Helper()
	var out []map[string]json.RawMessage
	for _, line := range h.out.Lines() {
		var obj map[string]json.RawMessage
		if err := json.Unmarshal([]byte(line), &obj); err != nil {
			h.t.Fatalf("stdout line is not valid JSON: %q: %v", line, err)
		}
		out = append(out, obj)
	}
	return out
}

func (h *harness) response(id int64) map[string]json.RawMessage {
	h.t.Helper()
	var found map[string]json.RawMessage
	for _, obj := range h.decoded() {
		raw, ok := obj["id"]
		if !ok {
			continue
		}
		var got int64
		if err := json.Unmarshal(raw, &got); err != nil {
			h.t.Fatalf("invalid id %s", raw)
		}
		if got == id {
			if found != nil {
				h.t.Fatalf("more than one response for id %d", id)
			}
			found = obj
		}
	}
	if found == nil {
		h.t.Fatalf("no response for id %d", id)
	}
	return found
}

func (h *harness) okResult(id int64, into any) {
	h.t.Helper()
	resp := h.response(id)
	if string(resp["ok"]) != "true" {
		h.t.Fatalf("expected ok response for id %d, got %s", id, mustJSON(resp))
	}
	if _, ok := resp["result"]; !ok {
		h.t.Fatalf("ok response %d has no result", id)
	}
	if _, ok := resp["error"]; ok {
		h.t.Fatalf("ok response %d has an error field", id)
	}
	if into != nil {
		if err := json.Unmarshal(resp["result"], into); err != nil {
			h.t.Fatalf("failed to decode result %d: %v", id, err)
		}
	}
}

func (h *harness) errCode(id int64) string {
	h.t.Helper()
	resp := h.response(id)
	if string(resp["ok"]) != "false" {
		h.t.Fatalf("expected error response for id %d, got %s", id, mustJSON(resp))
	}
	if _, ok := resp["result"]; ok {
		h.t.Fatalf("error response %d has a result field", id)
	}
	var body ErrorBody
	if err := json.Unmarshal(resp["error"], &body); err != nil {
		h.t.Fatalf("invalid error body: %v", err)
	}
	if body.Message == "" {
		h.t.Fatalf("error response %d has empty message", id)
	}
	return body.Code
}

func (h *harness) events(name string) []json.RawMessage {
	var out []json.RawMessage
	for _, obj := range h.decoded() {
		raw, ok := obj["event"]
		if !ok {
			continue
		}
		if _, hasID := obj["id"]; hasID {
			h.t.Fatalf("event line carries an id: %s", mustJSON(obj))
		}
		var evt string
		_ = json.Unmarshal(raw, &evt)
		if evt == name {
			out = append(out, obj["data"])
		}
	}
	return out
}

func (h *harness) waitEvent(name string, timeout time.Duration) json.RawMessage {
	h.t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if evts := h.events(name); len(evts) > 0 {
			return evts[len(evts)-1]
		}
		time.Sleep(20 * time.Millisecond)
	}
	h.t.Fatalf("event %q not emitted within %s", name, timeout)
	return nil
}

func mustJSON(v any) string {
	data, _ := json.Marshal(v)
	return string(data)
}

func TestEnvelopeEncoding(t *testing.T) {
	buf := &syncBuffer{}
	out := NewOutput(buf, zerolog.Nop())
	out.respondOK(1, Empty{})
	out.respondOK(2, nil)
	out.respondOK(3, []Thread{})
	out.respondErr(4, newError(CodeNotLoggedIn, "human readable"))
	out.respondErr(5, fmt.Errorf("boom"))
	out.Emit(EventTyping, TypingEvent{ThreadID: "12345678901234567", UserID: "9", Typing: true})
	out.Emit(EventConnection, ConnectionEvent{State: StateConnected})
	out.Emit(EventMessage, Message{ID: "m", ThreadID: "t"}.normalized())
	out.respondOK(6, StatusResult{LoggedIn: false})
	want := []string{
		`{"id":1,"ok":true,"result":{}}`,
		`{"id":2,"ok":true,"result":{}}`,
		`{"id":3,"ok":true,"result":[]}`,
		`{"id":4,"ok":false,"error":{"code":"not_logged_in","message":"human readable"}}`,
		`{"id":5,"ok":false,"error":{"code":"internal","message":"boom"}}`,
		`{"event":"typing","data":{"thread_id":"12345678901234567","user_id":"9","typing":true}}`,
		`{"event":"connection","data":{"state":"connected"}}`,
		`{"event":"message","data":{"id":"m","thread_id":"t","sender_id":"","sender_name":"","reactions":[],"text":"","timestamp_ms":0,"is_outgoing":false,"attachments":[],"mentions":[],"edited":false,"deleted":false,"system":false,"forwarded":false}}`,
		`{"id":6,"ok":true,"result":{"logged_in":false,"connected":false}}`,
	}
	got := buf.Lines()
	if len(got) != len(want) {
		t.Fatalf("got %d lines, want %d:\n%s", len(got), len(want), strings.Join(got, "\n"))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("line %d:\n got  %s\n want %s", i, got[i], want[i])
		}
	}
}

func TestDemoSessionFlow(t *testing.T) {
	h := newDemoHarness(t)
	h.send(`{"id":1,"cmd":"status"}`, `{"id":2,"cmd":"connect","params":{}}`)
	var status StatusResult
	h.okResult(1, &status)
	if !status.LoggedIn || status.User == nil || status.User.ID == "" {
		t.Fatalf("unexpected status %+v", status)
	}
	h.okResult(2, nil)
	h.waitEvent(EventReady, 3*time.Second)
	conn := h.events(EventConnection)
	if len(conn) < 2 || !strings.Contains(string(conn[0]), `"connecting"`) || !strings.Contains(string(conn[1]), `"connected"`) {
		t.Fatalf("unexpected connection events %s", conn)
	}

	h.send(`{"id":3,"cmd":"status"}`, `{"id":4,"cmd":"threads"}`, `{"id":5,"cmd":"threads","params":{"limit":3}}`, `{"id":6,"cmd":"connect"}`)
	h.okResult(3, &status)
	if !status.Connected {
		t.Fatalf("expected connected after ready, got %+v", status)
	}
	h.okResult(6, nil)
	var threads []Thread
	h.okResult(4, &threads)
	if len(threads) < 12 || len(threads) > 100 {
		t.Fatalf("unexpected thread count %d", len(threads))
	}
	var groups, e2ee, unread int
	for i, th := range threads {
		if th.ID == "" || th.Name == "" {
			t.Fatalf("thread %d missing id or name: %+v", i, th)
		}
		if i > 0 && threads[i-1].LastActivityMS < th.LastActivityMS {
			t.Fatalf("threads not sorted by newest activity at %d", i)
		}
		if th.IsGroup {
			groups++
		}
		if th.E2EE {
			e2ee++
		}
		if th.UnreadCount > 0 {
			unread++
		}
	}
	if groups == 0 || e2ee == 0 || unread == 0 {
		t.Fatalf("demo data lacks variety: groups=%d e2ee=%d unread=%d", groups, e2ee, unread)
	}
	var limited []Thread
	h.okResult(5, &limited)
	if len(limited) != 3 || limited[0].ID != threads[0].ID {
		t.Fatalf("limit not applied: %+v", limited)
	}

	threadID := threads[0].ID
	h.send(fmt.Sprintf(`{"id":7,"cmd":"messages","params":{"thread_id":%q}}`, threadID))
	var msgs []Message
	h.okResult(7, &msgs)
	if len(msgs) == 0 || len(msgs) > 50 {
		t.Fatalf("unexpected message count %d", len(msgs))
	}
	for i := 1; i < len(msgs); i++ {
		if msgs[i-1].TimestampMS > msgs[i].TimestampMS {
			t.Fatalf("messages not oldest first at %d", i)
		}
	}
	oldest := msgs[0].TimestampMS
	started := time.Now()
	h.send(fmt.Sprintf(`{"id":8,"cmd":"messages","params":{"thread_id":%q,"before_ms":%d,"limit":20}}`, threadID, oldest))
	if elapsed := time.Since(started); elapsed > demoBackfillDelay/2 {
		t.Fatalf("cache-first messages took %v", elapsed)
	}
	var older []Message
	h.okResult(8, &older)
	if len(older) >= 20 {
		t.Fatalf("expected the cache to be short before backfill, got %d", len(older))
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		var olderSeen int
		for _, raw := range h.events(EventBackfilled) {
			var ev MessagesBackfilledEvent
			if err := json.Unmarshal(raw, &ev); err != nil {
				t.Fatal(err)
			}
			for i, m := range ev.Messages {
				if ev.ThreadID != threadID || m.ThreadID != threadID || m.Reactions == nil || m.Attachments == nil {
					t.Fatalf("bad backfilled message %+v in %s", m, ev.ThreadID)
				}
				if i > 0 && ev.Messages[i-1].TimestampMS > m.TimestampMS {
					t.Fatalf("backfilled messages not oldest first")
				}
				if m.TimestampMS < oldest {
					olderSeen++
				}
			}
		}
		if olderSeen >= 20 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("no messages_backfilled with 20 older messages, saw %d", olderSeen)
		}
		time.Sleep(50 * time.Millisecond)
	}
	h.send(fmt.Sprintf(`{"id":80,"cmd":"messages","params":{"thread_id":%q,"before_ms":%d,"limit":20}}`, threadID, oldest))
	h.okResult(80, &older)
	if len(older) != 20 {
		t.Fatalf("expected 20 older messages after backfill, got %d", len(older))
	}
	for _, m := range older {
		if m.TimestampMS >= oldest {
			t.Fatalf("message %s not strictly older than before_ms", m.ID)
		}
		if m.ThreadID != threadID || m.Attachments == nil {
			t.Fatalf("bad message %+v", m)
		}
	}

	replyTo := msgs[len(msgs)-1].ID
	h.send(fmt.Sprintf(`{"id":9,"cmd":"send","params":{"thread_id":%q,"text":"salut","reply_to_id":%q,"client_id":"tmp-1"}}`, threadID, replyTo))
	var sent SendResult
	h.okResult(9, &sent)
	if sent.MessageID == "" {
		t.Fatal("send returned no message id")
	}
	var echoed bool
	for _, raw := range h.events(EventMessage) {
		var m Message
		if err := json.Unmarshal(raw, &m); err != nil {
			t.Fatal(err)
		}
		if m.ID == sent.MessageID {
			echoed = true
			if !m.IsOutgoing || m.Text != "salut" || m.ReplyToID != replyTo || m.ThreadID != threadID || m.ClientID != "tmp-1" {
				t.Fatalf("bad echo %+v", m)
			}
		}
	}
	if !echoed {
		t.Fatal("send did not echo a message event")
	}

	h.send(fmt.Sprintf(`{"id":10,"cmd":"mark_read","params":{"thread_id":%q}}`, threadID))
	h.send(`{"id":11,"cmd":"threads","params":{"limit":1}}`)
	h.okResult(10, nil)
	var after []Thread
	h.okResult(11, &after)
	if after[0].ID != threadID || after[0].UnreadCount != 0 || after[0].Snippet != "salut" {
		t.Fatalf("thread not updated after send+mark_read: %+v", after[0])
	}

	h.send(`{"id":12,"cmd":"logout"}`)
	h.send(`{"id":13,"cmd":"threads"}`, `{"id":14,"cmd":"status"}`)
	h.okResult(12, nil)
	if code := h.errCode(13); code != CodeNotLoggedIn {
		t.Fatalf("expected not_logged_in after logout, got %s", code)
	}
	var loggedOut StatusResult
	h.okResult(14, &loggedOut)
	if loggedOut.LoggedIn || loggedOut.User != nil {
		t.Fatalf("unexpected status after logout %+v", loggedOut)
	}
	h.send(`{"id":15,"cmd":"login","params":{"cookies":{"xs":"a","c_user":"1","datr":"d"}}}`)
	var login LoginResult
	h.okResult(15, &login)
	if login.User.ID == "" {
		t.Fatal("login returned no user")
	}
}

func TestBadRequests(t *testing.T) {
	h := newDemoHarness(t)
	h.send(
		`{"id":1,"cmd":"nope"}`,
		`{"id":2,"cmd":"messages","params":{}}`,
		`{"id":3,"cmd":"threads","params":[1]}`,
		`{"id":4,"cmd":"threads","params":{"limit":0}}`,
		`{"id":5,"cmd":"send","params":{"thread_id":"x","text":""}}`,
		`{"id":6,"cmd":"messages","params":{"thread_id":"does-not-exist"}}`,
		`{"id":7,"cmd":"login","params":{"cookies":{"xs":"a"}}}`,
		`{"id":8,"cmd":"send","params":{"thread_id":"100004781234501","text":"hi"}}`,
		`{"id":9,"cmd":"mark_read","params":{"thread_id":"nope"}}`,
		`{"id":10,"cmd":"threads","params":{"limit":"many"}}`,
		`{"id":11,"cmd":"status","params":null}`,
		`{"id":12 "cmd":"status"}`,
		`{"id":0,"cmd":"status"}`,
		`garbage`,
		`{"id":13}`,
	)
	cases := map[int64]string{
		1:  CodeBadRequest,
		2:  CodeBadRequest,
		3:  CodeBadRequest,
		4:  CodeBadRequest,
		5:  CodeBadRequest,
		6:  CodeNotFound,
		7:  CodeBadRequest,
		8:  CodeNotConnected,
		9:  CodeNotFound,
		10: CodeBadRequest,
		13: CodeBadRequest,
		0:  CodeBadRequest,
	}
	for id, want := range cases {
		if got := h.errCode(id); got != want {
			t.Errorf("id %d: got %s, want %s", id, got, want)
		}
	}
	h.okResult(11, nil)
	if lines := h.out.Lines(); len(lines) != 13 {
		t.Fatalf("expected 13 protocol lines, got %d:\n%s", len(lines), strings.Join(lines, "\n"))
	}
}

func TestDemoIncomingTraffic(t *testing.T) {
	if testing.Short() {
		t.Skip("waits for simulated traffic")
	}
	h := newDemoHarness(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	h.demo.mu.Lock()
	h.demo.connected = true
	h.demo.mu.Unlock()
	h.demo.simulateIncoming(ctx)
	typing := h.events(EventTyping)
	if len(typing) != 2 || !strings.Contains(string(typing[0]), `"typing":true`) || !strings.Contains(string(typing[1]), `"typing":false`) {
		t.Fatalf("unexpected typing events %s", typing)
	}
	msgs := h.events(EventMessage)
	if len(msgs) != 1 {
		t.Fatalf("expected one incoming message, got %d", len(msgs))
	}
	var m Message
	if err := json.Unmarshal(msgs[0], &m); err != nil {
		t.Fatal(err)
	}
	if m.IsOutgoing || m.SenderID == "" || (m.Text == "" && len(m.Attachments) == 0) {
		t.Fatalf("bad incoming message %+v", m)
	}
	if len(h.events(EventThreads)) != 1 {
		t.Fatal("incoming message did not update its thread")
	}
}

func TestDispatcherValidatesCommandParams(t *testing.T) {
	h := newDemoHarness(t)
	group := demoGroupID(1)
	cases := []string{
		`{"cmd":"edit_message","params":{"thread_id":"1","message_id":"m","text":"  "}}`,
		`{"cmd":"unsend","params":{"thread_id":"1"}}`,
		`{"cmd":"delete_for_me","params":{"message_id":"m"}}`,
		`{"cmd":"forward","params":{"from_thread_id":"1","message_id":"m","to_thread_ids":[]}}`,
		`{"cmd":"forward","params":{"from_thread_id":"1","message_id":"m","to_thread_ids":["1","2","3","4","5","6","7","8","9","10","11"]}}`,
		`{"cmd":"send_voice","params":{"thread_id":"1"}}`,
		`{"cmd":"send_voice","params":{"thread_id":"1","path":"x.wav","duration_ms":-1}}`,
		fmt.Sprintf(`{"cmd":"create_poll","params":{"thread_id":%q,"question":"Q","options":["a"]}}`, group),
		fmt.Sprintf(`{"cmd":"create_poll","params":{"thread_id":%q,"question":"Q","options":["a",""]}}`, group),
		fmt.Sprintf(`{"cmd":"create_poll","params":{"thread_id":%q,"question":" ","options":["a","b"]}}`, group),
		`{"cmd":"vote_poll","params":{"thread_id":"1"}}`,
		`{"cmd":"search_users","params":{"query":""}}`,
		`{"cmd":"open_thread","params":{}}`,
		`{"cmd":"create_group","params":{"user_ids":["1"]}}`,
		`{"cmd":"create_group","params":{"user_ids":["1","1"]}}`,
		`{"cmd":"accept_request","params":{}}`,
		`{"cmd":"mute","params":{"thread_id":"1"}}`,
		`{"cmd":"mute","params":{"thread_id":"1","until_ms":5}}`,
		`{"cmd":"mute","params":{"thread_id":"1","until_ms":-2}}`,
		`{"cmd":"rename_thread","params":{"thread_id":"1"}}`,
		`{"cmd":"set_thread_image","params":{"thread_id":"1"}}`,
		`{"cmd":"add_participants","params":{"thread_id":"1","user_ids":[]}}`,
		`{"cmd":"remove_participant","params":{"thread_id":"1"}}`,
		`{"cmd":"leave_thread","params":{}}`,
		`{"cmd":"set_admin","params":{"thread_id":"1","user_id":"2"}}`,
		`{"cmd":"block_user","params":{"user_id":"2"}}`,
	}
	lines := make([]string, len(cases))
	for i, c := range cases {
		lines[i] = fmt.Sprintf(`{"id":%d,%s`, i+1, c[1:])
	}
	h.send(lines...)
	for i := range cases {
		if code := h.errCode(int64(i + 1)); code != CodeBadRequest {
			t.Errorf("case %d (%s): expected bad_request, got %s", i, cases[i], code)
		}
	}
	h.send(`{"id":100,"cmd":"edit_message","params":{"thread_id":"nope","message_id":"m","text":"x"}}`, `{"id":101,"cmd":"definitely_unknown"}`)
	if h.errCode(100) != CodeNotFound || h.errCode(101) != CodeBadRequest {
		t.Fatal("unknown thread must be not_found and unknown commands bad_request")
	}
}

type laneBackend struct {
	*DemoBackend
	mu         sync.Mutex
	order      []string
	otherDone  chan struct{}
	otherClose sync.Once
}

func (l *laneBackend) Send(ctx context.Context, threadID, text, replyToID, clientID string, mentions []Mention) (string, error) {
	if text == "first" {
		select {
		case <-l.otherDone:
		case <-ctx.Done():
		}
	}
	l.mu.Lock()
	l.order = append(l.order, threadID+":"+text)
	l.mu.Unlock()
	if text == "other" {
		l.otherClose.Do(func() { close(l.otherDone) })
	}
	return text, nil
}

func TestSendsAreSerializedPerThread(t *testing.T) {
	buf := &syncBuffer{}
	out := NewOutput(buf, zerolog.Nop())
	lb := &laneBackend{DemoBackend: NewDemoBackend(t.TempDir(), "", out, zerolog.Nop()), otherDone: make(chan struct{})}
	disp := NewDispatcher(lb, out, zerolog.Nop())
	disp.Run(strings.NewReader(strings.Join([]string{
		`{"id":1,"cmd":"send","params":{"thread_id":"A","text":"first"}}`,
		`{"id":2,"cmd":"send","params":{"thread_id":"A","text":"second"}}`,
		`{"id":3,"cmd":"send","params":{"thread_id":"B","text":"other"}}`,
		`{"id":4,"cmd":"send","params":{"thread_id":"A","text":"third"}}`,
	}, "\n") + "\n"))
	disp.Wait()
	disp.Shutdown(time.Second)
	got := strings.Join(lb.order, ",")
	if !strings.HasPrefix(got, "B:other,") || !strings.HasSuffix(got, "A:first,A:second,A:third") {
		t.Fatalf("unexpected order %s", got)
	}
}

type forwardLaneBackend struct {
	*DemoBackend
	mu    sync.Mutex
	order []string
}

func (l *forwardLaneBackend) Send(ctx context.Context, threadID, text, replyToID, clientID string, mentions []Mention) (string, error) {
	time.Sleep(150 * time.Millisecond)
	l.mu.Lock()
	l.order = append(l.order, "send:"+threadID)
	l.mu.Unlock()
	return "s", nil
}

func (l *forwardLaneBackend) Forward(ctx context.Context, from, messageID string, to []string) ([]string, error) {
	l.mu.Lock()
	l.order = append(l.order, "forward:"+strings.Join(to, ","))
	l.mu.Unlock()
	return make([]string, len(to)), nil
}

func TestForwardIsSerializedWithSends(t *testing.T) {
	buf := &syncBuffer{}
	out := NewOutput(buf, zerolog.Nop())
	lb := &forwardLaneBackend{DemoBackend: NewDemoBackend(t.TempDir(), "", out, zerolog.Nop())}
	disp := NewDispatcher(lb, out, zerolog.Nop())
	disp.Run(strings.NewReader(`{"id":1,"cmd":"send","params":{"thread_id":"B","text":"x"}}` + "\n" +
		`{"id":2,"cmd":"forward","params":{"from_thread_id":"A","message_id":"m","to_thread_ids":["C","B"]}}` + "\n"))
	disp.Wait()
	if strings.Join(lb.order, " ") != "send:B forward:C,B" {
		t.Fatalf("forward to B must wait for the pending send to B: %v", lb.order)
	}
}
