package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/rs/zerolog"
	"go.mau.fi/mautrix-meta/pkg/messagix/table"
)

func newTestMetaBackend(t *testing.T) (*MetaBackend, *syncBuffer, string) {
	t.Helper()
	dir := t.TempDir()
	buf := &syncBuffer{}
	out := NewOutput(buf, zerolog.Nop())
	b, err := NewMetaBackend(dir, "", out, zerolog.Nop())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(b.Close)
	return b, buf, dir
}

func eventsOf(t *testing.T, buf *syncBuffer, name string) []json.RawMessage {
	t.Helper()
	var out []json.RawMessage
	for _, line := range buf.Lines() {
		var env struct {
			Event string          `json:"event"`
			Data  json.RawMessage `json:"data"`
		}
		if err := json.Unmarshal([]byte(line), &env); err != nil {
			t.Fatalf("invalid line %q: %v", line, err)
		}
		if env.Event == name {
			out = append(out, env.Data)
		}
	}
	return out
}

func TestMetaBackendWithoutSession(t *testing.T) {
	b, _, _ := newTestMetaBackend(t)
	ctx := context.Background()
	status, err := b.Status(ctx)
	if err != nil || status.LoggedIn || status.Connected || status.User != nil {
		t.Fatalf("unexpected status %+v %v", status, err)
	}
	for name, call := range map[string]func() error{
		"connect":   func() error { return b.Connect(ctx) },
		"threads":   func() error { _, err := b.Threads(ctx, 10); return err },
		"messages":  func() error { _, err := b.Messages(ctx, "1", 0, 10, true); return err },
		"send":      func() error { _, err := b.Send(ctx, "1", "x", "", "", nil); return err },
		"mark_read": func() error { return b.MarkRead(ctx, "1") },
	} {
		if body := toErrorBody(call()); body.Code != CodeNotLoggedIn {
			t.Errorf("%s: got %+v, want not_logged_in", name, body)
		}
	}
	if _, err := b.Login(ctx, map[string]string{"xs": "x"}); toErrorBody(err).Code != CodeBadRequest {
		t.Fatalf("expected bad_request for missing cookies, got %v", err)
	}
	if err := b.Logout(ctx); err != nil {
		t.Fatal(err)
	}
}

func TestMetaBackendRestoresPersistedSession(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, sessionFile), []byte(`{"user":{"id":"4242","name":"Me"}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, cookiesFile), []byte(`{"c_user":"4242","xs":"x","datr":"d"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	buf := &syncBuffer{}
	b, err := NewMetaBackend(dir, "", NewOutput(buf, zerolog.Nop()), zerolog.Nop())
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	status, _ := b.Status(context.Background())
	if !status.LoggedIn || status.Connected || status.User == nil || status.User.ID != "4242" {
		t.Fatalf("unexpected status %+v", status)
	}
	if len(buf.Lines()) != 0 {
		t.Fatalf("bridge emitted output before connect: %v", buf.Lines())
	}
	threads, err := b.Threads(context.Background(), 10)
	if err != nil || len(threads) != 0 {
		t.Fatalf("unexpected threads %v %v", threads, err)
	}
}

func TestLSTableMapping(t *testing.T) {
	b, buf, _ := newTestMetaBackend(t)
	b.store.SetSelfID("999")
	tb := time.Now().Add(-time.Minute).UnixMilli()
	b.handleTable(&table.LSTable{
		LSDeleteThenInsertThread: []*table.LSDeleteThenInsertThread{{
			ThreadKey:                    111,
			ThreadType:                   table.ONE_TO_ONE,
			Snippet:                      "hello",
			LastActivityTimestampMs:      tb + 3000,
			LastReadWatermarkTimestampMs: tb + 1000,
			UnreadMessageCount:           2,
		}, {
			ThreadKey:               555,
			ThreadType:              table.GROUP_THREAD,
			ThreadName:              "Team",
			LastActivityTimestampMs: tb + 500,
		}},
		LSVerifyContactRowExists: []*table.LSVerifyContactRowExists{{ContactId: 111, Name: "Bob Smith", FirstName: "Bob"}},
		LSInsertMessage: []*table.LSInsertMessage{{
			ThreadKey:   111,
			MessageId:   "mid.$a",
			Text:        "hello",
			TimestampMs: tb + 3000,
			SenderId:    111,
		}},
		LSUpdateTypingIndicator: []*table.LSUpdateTypingIndicator{{ThreadKey: 111, SenderId: 111, IsTyping: true}},
		LSUpdateReadReceipt:     []*table.LSUpdateReadReceipt{{ThreadKey: 555, ContactId: 7, ReadWatermarkTimestampMs: 400}},
	}, modeLive)

	threads := b.store.ListThreads(0)
	if len(threads) != 2 || threads[0].ID != "111" || threads[0].Name != "Bob Smith" || threads[0].UnreadCount != 2 || threads[0].IsGroup || threads[0].E2EE {
		t.Fatalf("unexpected threads %+v", threads)
	}
	if threads[1].ID != "555" || !threads[1].IsGroup || threads[1].Name != "Team" || threads[1].UnreadCount != 1 {
		t.Fatalf("unexpected group %+v", threads[1])
	}
	if n := len(eventsOf(t, buf, EventMessage)); n != 1 {
		t.Fatalf("expected 1 message event, got %d", n)
	}
	if n := len(eventsOf(t, buf, EventTyping)); n != 1 {
		t.Fatalf("expected 1 typing event, got %d", n)
	}
	if n := len(eventsOf(t, buf, EventRead)); n != 1 {
		t.Fatalf("expected 1 read event, got %d", n)
	}
	if n := len(eventsOf(t, buf, EventThreads)); n != 2 {
		t.Fatalf("expected the ordering upsert plus the batched threads event, got %d", n)
	}
	lines := buf.Lines()
	firstThreads, firstMessage := -1, -1
	for i, line := range lines {
		if firstThreads < 0 && strings.Contains(line, `"event":"threads"`) && strings.Contains(line, `"id":"111"`) {
			firstThreads = i
		}
		if firstMessage < 0 && strings.Contains(line, `"event":"message"`) {
			firstMessage = i
		}
	}
	if firstThreads < 0 || firstThreads > firstMessage {
		t.Fatalf("threads upsert must precede the first message of an unknown thread (%d, %d)", firstThreads, firstMessage)
	}

	b.handleTable(&table.LSTable{
		LSInsertMessage: []*table.LSInsertMessage{
			{ThreadKey: 111, MessageId: "mid.$a", Text: "hello", TimestampMs: tb + 3000, SenderId: 111},
			{ThreadKey: 111, MessageId: "mid.$b", Text: "again", TimestampMs: tb + 4000, SenderId: 111},
		},
		LSEditMessage: []*table.LSEditMessage{{MessageID: "mid.$a", Text: "hello!", EditCount: 1}},
	}, modeLive)
	if n := len(eventsOf(t, buf, EventMessage)); n != 2 {
		t.Fatalf("duplicate insert must not re-emit; got %d message events", n)
	}
	th, _ := b.store.ThreadView("111")
	if th.UnreadCount != 3 || th.Snippet != "again" || th.LastActivityMS != tb+4000 {
		t.Fatalf("thread not updated by new message: %+v", th)
	}
	edited, _ := b.store.FindMessage("mid.$a")
	if !edited.Edited || edited.Text != "hello!" {
		t.Fatalf("edit not applied: %+v", edited)
	}

	b.handleTable(&table.LSTable{
		LSDeleteMessage:    []*table.LSDeleteMessage{{ThreadKey: 111, MessageId: "mid.$b"}},
		LSMarkThreadReadV2: []*table.LSMarkThreadReadV2{{ThreadKey: 111, LastReadWatermarkTimestampMs: tb + 4000}},
	}, modeLive)
	if _, still := b.store.FindMessage("mid.$b"); still {
		t.Fatal("LSDeleteMessage must remove the message for me")
	}
	if th, _ := b.store.ThreadView("111"); th.UnreadCount != 0 {
		t.Fatalf("mark read not applied: %+v", th)
	}
	if n := len(eventsOf(t, buf, EventMessageUpdate)); n != 1 {
		t.Fatalf("expected 1 message_updated event, got %d", n)
	}
	if removed := eventsOf(t, buf, EventMessageRemoved); len(removed) != 1 || !strings.Contains(string(removed[0]), `"message_id":"mid.$b"`) {
		t.Fatalf("expected one message_removed event, got %s", removed)
	}

	before := len(eventsOf(t, buf, EventMessage))
	b.handleTable(&table.LSTable{
		LSInsertNewMessageRange: []*table.LSInsertNewMessageRange{{ThreadKey: 111, MinTimestampMs: 100, MaxTimestampMs: 200, HasMoreBefore: false}},
		LSUpsertMessage:         []*table.LSUpsertMessage{{ThreadKey: 111, MessageId: "mid.$old", Text: "old", TimestampMs: 100, SenderId: 999}},
	}, modeHistory)
	if n := len(eventsOf(t, buf, EventMessage)); n != before {
		t.Fatal("history upsert must not emit message events")
	}
	msgs := b.store.Messages("111", tb+3000, 10)
	if len(msgs) != 1 || msgs[0].ID != "mid.$old" || !msgs[0].IsOutgoing {
		t.Fatalf("unexpected history %+v", msgs)
	}
	if more, known := b.store.HasMoreBefore("111"); !known || more {
		t.Fatal("has_more_before not recorded")
	}

	b.handleTable(&table.LSTable{
		LSVerifyHybridThreadExists: []*table.LSVerifyHybridThreadExists{{ThreadKey: 555, ThreadJID: 777, ThreadType: table.ENCRYPTED_OVER_WA_GROUP, LastActivityTimestampMS: 600}},
	}, modeLive)
	removed := eventsOf(t, buf, EventThreadRemoved)
	if len(removed) != 1 || string(removed[0]) != `{"thread_id":"555"}` {
		t.Fatalf("unexpected thread_removed events %s", removed)
	}
	hybrid, ok := b.store.ThreadView("777")
	if !ok || !hybrid.E2EE || !hybrid.IsGroup {
		t.Fatalf("hybrid thread not created: %+v", hybrid)
	}
	b.handleTable(&table.LSTable{
		LSSyncUpdateThreadName: []*table.LSSyncUpdateThreadName{{ThreadKey: 555, ThreadName: "Renamed"}},
	}, modeLive)
	if th, _ := b.store.ThreadView("777"); th.Name != "Renamed" {
		t.Fatalf("hybrid key mapping not applied: %+v", th)
	}
}

func TestStorePersistence(t *testing.T) {
	path := filepath.Join(t.TempDir(), "cache.json")
	s := NewStore()
	s.SetSelfID("1")
	s.SetContact("2", "Two", "T", "")
	s.UpdateThread("2", func(rec *threadRec) { rec.PeerID = "2"; rec.LastActivityMS = 10 })
	s.PutMessage(Message{ID: "a", ThreadID: "2", SenderID: "2", TimestampMS: 5, Text: "x"})
	s.PutMessage(Message{ID: "b", ThreadID: "2", SenderID: "1", TimestampMS: 9, Text: "y", IsOutgoing: true})
	if err := s.Save(path); err != nil {
		t.Fatal(err)
	}
	loaded := NewStore()
	if err := loaded.Load(path); err != nil {
		t.Fatal(err)
	}
	msgs := loaded.Messages("2", 0, 10)
	if len(msgs) != 2 || msgs[0].ID != "a" || msgs[1].ID != "b" || msgs[0].SenderName != "Two" {
		t.Fatalf("unexpected messages after reload %+v", msgs)
	}
	if th, ok := loaded.ThreadView("2"); !ok || th.Name != "Two" {
		t.Fatalf("unexpected thread after reload %+v", th)
	}
	if got := loaded.Messages("2", 9, 10); len(got) != 1 || got[0].ID != "a" {
		t.Fatalf("before_ms must be strict: %+v", got)
	}
}

func TestWhatsmeowStoreOpensWithPureGoSQLite(t *testing.T) {
	b, _, dir := newTestMetaBackend(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	b.e2eeMu.Lock()
	container, err := b.containerLocked(ctx)
	b.e2eeMu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	devices, err := container.GetAllDevices(ctx)
	if err != nil || len(devices) != 0 {
		t.Fatalf("unexpected devices %v %v", devices, err)
	}
	if dev := container.NewDevice(); dev == nil || dev.IdentityKey == nil {
		t.Fatal("NewDevice did not generate keys")
	}
	if _, err := os.Stat(filepath.Join(dir, waDBFile)); err != nil {
		t.Fatal(err)
	}
}
