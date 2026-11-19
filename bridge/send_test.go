package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"go.mau.fi/mautrix-meta/pkg/messagix/table"
)

func TestReplyMustBelongToThread(t *testing.T) {
	b, _, _ := loggedInBackend(t)
	b.store.UpdateThread("5", func(rec *threadRec) {})
	b.store.UpdateThread("6", func(rec *threadRec) {})
	b.store.PutMessage(Message{ID: "mid.$other", ThreadID: "6", TimestampMS: 1})
	if _, err := b.Send(context.Background(), "5", "hi", "mid.$other", "", nil); toErrorBody(err).Code != CodeBadRequest {
		t.Fatalf("reply to another thread must be bad_request, got %v", err)
	}
	if _, err := b.SendMedia(context.Background(), "5", nil, "", "mid.$other", nil); toErrorBody(err).Code != CodeBadRequest {
		t.Fatalf("send_media reply to another thread must be bad_request, got %v", err)
	}
	if lsReplyMeta(&Message{ID: "3EB0ABC"}) != nil || lsReplyMeta(&Message{ID: "mid.$x", SenderID: "7"}) == nil {
		t.Fatal("LS reply metadata must only quote mid.$ ids")
	}
	if waQuotedMeta(&Message{ID: "mid.$x"}).QuotedMessage != nil || waQuotedMeta(&Message{ID: "3EB0ABC", SenderID: "7"}).QuotedMessage == nil {
		t.Fatal("E2EE quotes must only quote WhatsApp ids")
	}
	b.store.SetHybrid(testLegacyFB, testPeer)
	if key, _ := b.lsThreadKey(threadRec{ID: testPeerID, E2EE: true}); key != testLegacyFB {
		t.Fatalf("LS actions on a hybrid thread must use the FB key, got %d", key)
	}
	if key, _ := b.lsThreadKey(threadRec{ID: "5"}); key != 5 {
		t.Fatalf("plain LS thread key changed: %d", key)
	}
}

func TestSendMediaErrorData(t *testing.T) {
	err := sendMediaError(uncertainError(newError(CodeInternal, "Meta did not confirm the message")), []string{"m1"}, 1)
	body := toErrorBody(err)
	raw, _ := json.Marshal(body)
	if body.Code != CodeInternal || !strings.Contains(string(raw), `"data":{"failed_index":1,"message_ids":["m1"],"uncertain":true}`) {
		t.Fatalf("unexpected error body %s", raw)
	}
	b, _, _ := loggedInBackend(t)
	b.store.UpdateThread("5", func(rec *threadRec) { rec.E2EE = true })
	dir := t.TempDir()
	path := filepath.Join(dir, "a.txt")
	os.WriteFile(path, []byte("x"), 0o600)
	files, _ := loadSendFiles([]string{path})
	_, err = b.SendMedia(context.Background(), "5", files, "", "", nil)
	body = toErrorBody(err)
	if body.Code != CodeNotConnected || body.Data["failed_index"] != 0 || body.Data["uncertain"] != false || len(body.Data["message_ids"].([]string)) != 0 {
		t.Fatalf("unexpected first-item failure %+v", body)
	}

	h := newDemoHarness(t)
	h.send(`{"id":1,"cmd":"connect"}`)
	h.waitEvent(EventReady, 3*time.Second)
	keep := filepath.Join(dir, "keep.txt")
	gone := filepath.Join(dir, "gone.txt")
	os.WriteFile(keep, []byte("k"), 0o600)
	os.WriteFile(gone, []byte("g"), 0o600)
	files, _ = loadSendFiles([]string{keep, gone})
	os.Remove(gone)
	ids, err := h.demo.SendMedia(context.Background(), demoPeople[0].ID, files, "", "", nil)
	body = toErrorBody(err)
	if len(ids) != 1 || body.Code != CodeBadRequest || body.Data["failed_index"] != 1 || len(body.Data["message_ids"].([]string)) != 1 {
		t.Fatalf("partial failure must report what was sent: %v %+v", ids, body)
	}
}

func TestClientIDOnLSEcho(t *testing.T) {
	b, buf, _ := loggedInBackend(t)
	now := time.Now().UnixMilli()
	b.pendingClientIDs.Store("777", "tmp-ls")
	b.handleTable(&table.LSTable{LSInsertMessage: []*table.LSInsertMessage{
		{ThreadKey: 5, MessageId: "mid.$mine", TimestampMs: now, SenderId: 999, OfflineThreadingId: "777", Text: "hello"},
		{ThreadKey: 5, MessageId: "mid.$theirs", TimestampMs: now + 1, SenderId: 7, OfflineThreadingId: "777", Text: "yo"},
	}}, modeLive)
	got := map[string]string{}
	for _, raw := range eventsOf(t, buf, EventMessage) {
		var m Message
		json.Unmarshal(raw, &m)
		got[m.ID] = m.ClientID
	}
	if got["mid.$mine"] != "tmp-ls" || got["mid.$theirs"] != "" {
		t.Fatalf("client_id must be echoed on our own message only: %v", got)
	}
	if stored, _ := b.store.MessageInThread("5", "mid.$mine"); stored.ClientID != "" {
		t.Fatal("client_id must not be persisted")
	}
	for _, raw := range eventsOf(t, buf, EventMessage) {
		if strings.Contains(string(raw), `"client_id":""`) {
			t.Fatal("empty client_id must be omitted")
		}
	}
}
