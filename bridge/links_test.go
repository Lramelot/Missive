package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"go.mau.fi/mautrix-meta/pkg/messagix/table"
)

func TestLinkTargetIsNeverFetched(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		w.Write([]byte("<html>router admin</html>"))
	}))
	defer srv.Close()
	allowTestServer(t, srv)
	b, _, _ := loggedInBackend(t)
	b.handleTable(&table.LSTable{
		LSInsertMessage:       []*table.LSInsertMessage{{ThreadKey: 5, MessageId: "mid.$l", TimestampMs: 10, SenderId: 7}},
		LSInsertXmaAttachment: []*table.LSInsertXmaAttachment{{MessageId: "mid.$l", AttachmentFbid: "1", AttachmentType: table.AttachmentTypeXMA, TitleText: "click", ActionUrl: srv.URL + "/admin?reboot=1"}},
	}, modeLive)
	msg, _ := b.store.MessageInThread("5", "mid.$l")
	if len(msg.Attachments) != 1 || msg.Attachments[0].Kind != "link" {
		t.Fatalf("setup: %+v", msg)
	}
	if _, err := b.Media(context.Background(), "5", "mid.$l", "1", VariantFull); err == nil || toErrorBody(err).Code != CodeNotFound {
		t.Fatalf("link without preview must be not_found, got %v", err)
	}
	if hits.Load() != 0 {
		t.Fatal("the link target was fetched")
	}
}
