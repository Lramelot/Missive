package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/rs/zerolog"
	"go.mau.fi/mautrix-meta/pkg/messagix/table"
	"go.mau.fi/whatsmeow/proto/waCommon"
	waTypes "go.mau.fi/whatsmeow/types"
	"google.golang.org/protobuf/proto"
)

func waKey(id string, fromMe bool, participant, remote string) *waCommon.MessageKey {
	k := &waCommon.MessageKey{ID: proto.String(id)}
	if fromMe {
		k.FromMe = proto.Bool(true)
	}
	if participant != "" {
		k.Participant = proto.String(participant)
	}
	if remote != "" {
		k.RemoteJID = proto.String(remote)
	}
	return k
}

func lastUpdate(t *testing.T, buf *syncBuffer, id string) (Message, bool) {
	t.Helper()
	var found Message
	ok := false
	for _, raw := range eventsOf(t, buf, EventMessageUpdate) {
		var m Message
		if err := json.Unmarshal(raw, &m); err != nil {
			t.Fatal(err)
		}
		if m.ID == id {
			found, ok = m, true
		}
	}
	return found, ok
}

func TestLSUnsendKeepsKnownContent(t *testing.T) {
	b, buf, _ := loggedInBackend(t)
	now := time.Now().UnixMilli()
	b.store.PutMessage(Message{
		ID: "mid.$u1", ThreadID: "5", SenderID: "7", Text: "rendez-vous à 8h", TimestampMS: now,
		Attachments: []Attachment{{ID: "a0", Kind: "image"}},
		Mentions:    []Mention{{UserID: "8", Offset: 0, Length: 4}},
	})
	b.handleTable(&table.LSTable{LSDeleteThenInsertMessage: []*table.LSDeleteThenInsertMessage{
		{ThreadKey: 5, MessageId: "mid.$u1", TimestampMs: now, SenderId: 7, IsUnsent: true},
	}}, modeLive)
	m, _ := b.store.FindMessage("mid.$u1")
	if !m.Deleted || m.Text != "rendez-vous à 8h" || len(m.Attachments) != 1 || len(m.Mentions) != 1 {
		t.Fatalf("an unsent message must be flagged deleted and keep its content: %+v", m)
	}
	update, ok := lastUpdate(t, buf, "mid.$u1")
	if !ok || !update.Deleted || update.Text != "rendez-vous à 8h" || len(update.Attachments) != 1 {
		t.Fatalf("the update sent to the app must carry the content: %+v", update)
	}
}

func TestLSUnsentUpsertDoesNotEraseText(t *testing.T) {
	b, buf, _ := loggedInBackend(t)
	now := time.Now().UnixMilli()
	b.store.PutMessage(Message{ID: "mid.$u2", ThreadID: "5", SenderID: "7", Text: "bonjour", TimestampMS: now,
		Mentions:    []Mention{{UserID: "8", Offset: 0, Length: 3}},
		Attachments: []Attachment{{ID: "a0", Kind: "file", FileName: "plan.pdf"}}})
	b.handleTable(&table.LSTable{
		LSInsertNewMessageRange: []*table.LSInsertNewMessageRange{{ThreadKey: 5, MinTimestampMs: now, MaxTimestampMs: now}},
		LSUpsertMessage:         []*table.LSUpsertMessage{{ThreadKey: 5, MessageId: "mid.$u2", TimestampMs: now, SenderId: 7, IsUnsent: true}},
	}, modeLive)
	m, _ := b.store.FindMessage("mid.$u2")
	if !m.Deleted || m.Text != "bonjour" || len(m.Attachments) != 1 || len(m.Mentions) != 1 {
		t.Fatalf("an unsent upsert with empty text must not erase the stored content: %+v", m)
	}
	if update, ok := lastUpdate(t, buf, "mid.$u2"); !ok || update.Text != "bonjour" || !update.Deleted || len(update.Mentions) != 1 {
		t.Fatalf("the app must receive the deleted flag with the content: %+v", update)
	}
	b.handleTable(&table.LSTable{
		LSInsertMessage: []*table.LSInsertMessage{{ThreadKey: 5, MessageId: "mid.$u2", TimestampMs: now, SenderId: 7, Text: "bonjour"}},
	}, modeLive)
	if m, _ := b.store.FindMessage("mid.$u2"); !m.Deleted {
		t.Fatal("a deleted message must stay deleted when Meta resends it")
	}
}

func TestLSInsertOfKnownUnsentMessageEmitsContent(t *testing.T) {
	b, buf, _ := loggedInBackend(t)
	now := time.Now().UnixMilli()
	b.store.PutMessage(Message{ID: "mid.$u3", ThreadID: "5", SenderID: "7", Text: "bonjour", TimestampMS: now})
	b.handleTable(&table.LSTable{
		LSInsertMessage: []*table.LSInsertMessage{{ThreadKey: 5, MessageId: "mid.$u3", TimestampMs: now, SenderId: 7, IsUnsent: true}},
	}, modeLive)
	if update, ok := lastUpdate(t, buf, "mid.$u3"); !ok || !update.Deleted || update.Text != "bonjour" {
		t.Fatalf("an unsent insert of a known message must emit the deleted flag with the content: %+v", update)
	}
	b.handleTable(&table.LSTable{
		LSInsertMessage: []*table.LSInsertMessage{{ThreadKey: 5, MessageId: "mid.$u3", TimestampMs: now, SenderId: 7, Text: "bonjour 2"}},
	}, modeLive)
	if update, ok := lastUpdate(t, buf, "mid.$u3"); !ok || !update.Deleted {
		t.Fatalf("a resent deleted message must still be emitted as deleted: %+v", update)
	}
}

func TestMessageArrivingDeletedStaysEmpty(t *testing.T) {
	b, _, _ := loggedInBackend(t)
	now := time.Now().UnixMilli()
	b.handleTable(&table.LSTable{
		LSInsertMessage: []*table.LSInsertMessage{{ThreadKey: 5, MessageId: "mid.$gone", TimestampMs: now - 3_600_000, SenderId: 7, Text: "leaked", IsUnsent: true}},
	}, modeHistory)
	m, ok := b.store.FindMessage("mid.$gone")
	if !ok || !m.Deleted || m.Text != "" {
		t.Fatalf("a message already deleted when it arrives has no content: %+v", m)
	}
}

func TestOwnUnsendKeepsContent(t *testing.T) {
	b, buf, _ := recordingBackend(t)
	b.store.PutMessage(Message{ID: "mid.$own", ThreadID: "55", SenderID: testSelf, IsOutgoing: true, Text: "oups", TimestampMS: time.Now().UnixMilli()})
	if err := b.Unsend(context.Background(), "55", "mid.$own"); err != nil {
		t.Fatal(err)
	}
	if m, _ := b.store.FindMessage("mid.$own"); !m.Deleted || m.Text != "oups" {
		t.Fatalf("unsending my own message must keep its content: %+v", m)
	}
	if update, ok := lastUpdate(t, buf, "mid.$own"); !ok || update.Text != "oups" {
		t.Fatalf("the update must carry the content: %+v", update)
	}
	if err := b.Unsend(context.Background(), "55", "mid.$own"); err == nil {
		t.Fatal("a message already unsent cannot be unsent again")
	}
}

func TestE2EEOneToOneRevokeByPeer(t *testing.T) {
	b, buf, _ := loggedInBackend(t)
	peer := waTypes.NewJID(testPeerID, waTypes.MessengerServer)
	self := waTypes.NewJID(testSelf, waTypes.MessengerServer)
	b.ensureE2EEThread(peer)
	now := time.Now().UnixMilli()
	b.store.PutMessage(Message{ID: "P1", ThreadID: testPeerID, SenderID: testPeerID, Text: "à effacer", TimestampMS: now,
		Attachments: []Attachment{{ID: "media_0", Kind: "image"}}})
	b.handleFBMessage(e2eeRevoke(peer, peer, waKey("P1", true, "", self.String())))
	m, _ := b.store.FindMessage("P1")
	if !m.Deleted || m.Text != "à effacer" || len(m.Attachments) != 1 {
		t.Fatalf("a peer revoking their own message (key seen from their side) must apply and keep the content: %+v", m)
	}
	if update, ok := lastUpdate(t, buf, "P1"); !ok || !update.Deleted || update.Text != "à effacer" {
		t.Fatalf("the revoke must reach the app with the content: %+v", update)
	}
}

func TestE2EEOneToOneEditByPeerWithOwnRemoteJID(t *testing.T) {
	b, _, _ := loggedInBackend(t)
	peer := waTypes.NewJID(testPeerID, waTypes.MessengerServer)
	self := waTypes.NewJID(testSelf, waTypes.MessengerServer)
	b.ensureE2EEThread(peer)
	b.store.PutMessage(Message{ID: "P2", ThreadID: testPeerID, SenderID: testPeerID, Text: "avant", TimestampMS: time.Now().UnixMilli()})
	b.handleFBMessage(e2eeEdit(peer, peer, waKey("P2", true, "", self.String()), "après"))
	if m, _ := b.store.FindMessage("P2"); m.Text != "après" || !m.Edited {
		t.Fatalf("a peer editing their own message must apply: %+v", m)
	}
}

func TestE2EEOneToOneRevokeFromMyOtherDevice(t *testing.T) {
	b, _, _ := loggedInBackend(t)
	peer := waTypes.NewJID(testPeerID, waTypes.MessengerServer)
	self := waTypes.NewJID(testSelf, waTypes.MessengerServer)
	b.ensureE2EEThread(peer)
	b.store.PutMessage(Message{ID: "M1", ThreadID: testPeerID, SenderID: testSelf, IsOutgoing: true, Text: "le mien", TimestampMS: time.Now().UnixMilli()})
	evt := e2eeRevoke(peer, self, waKey("M1", true, "", peer.String()))
	evt.Info.IsFromMe = true
	b.handleFBMessage(evt)
	if m, _ := b.store.FindMessage("M1"); !m.Deleted || m.Text != "le mien" {
		t.Fatalf("my revoke from another device must apply and keep the content: %+v", m)
	}
}

func TestE2EEOneToOneRevokeOfSomeoneElsesMessageRejected(t *testing.T) {
	b, _, _ := loggedInBackend(t)
	peer := waTypes.NewJID(testPeerID, waTypes.MessengerServer)
	self := waTypes.NewJID(testSelf, waTypes.MessengerServer)
	group := waTypes.NewJID("66", waTypes.GroupServer)
	b.ensureE2EEThread(peer)
	b.store.PutMessage(Message{ID: "M2", ThreadID: testPeerID, SenderID: testSelf, IsOutgoing: true, Text: "à moi", TimestampMS: time.Now().UnixMilli()})
	b.store.PutMessage(Message{ID: "P3", ThreadID: testPeerID, SenderID: testPeerID, Text: "à lui", TimestampMS: time.Now().UnixMilli()})
	b.handleFBMessage(e2eeRevoke(peer, peer, waKey("M2", true, "", self.String())))
	b.handleFBMessage(e2eeRevoke(peer, peer, waKey("M2", false, "", self.String())))
	b.handleFBMessage(e2eeRevoke(peer, peer, waKey("P3", true, "", group.String())))
	if m, _ := b.store.FindMessage("M2"); m.Deleted {
		t.Fatal("the peer must not revoke my message")
	}
	if m, _ := b.store.FindMessage("P3"); m.Deleted {
		t.Fatal("a key pointing at a group must not apply to a one-to-one chat")
	}
}

func TestE2EEGroupRevokeAuthorization(t *testing.T) {
	b, _ := groupBackend(t)
	group := waTypes.NewJID("66", waTypes.GroupServer)
	other := waTypes.NewJID("67", waTypes.GroupServer)
	seven := waTypes.NewJID("7", waTypes.MessengerServer)
	eight := waTypes.NewJID("8", waTypes.MessengerServer)
	b.ensureE2EEThread(group)
	b.ensureE2EEThread(other)
	now := time.Now().UnixMilli()
	b.store.PutMessage(Message{ID: "G1", ThreadID: "66", SenderID: "7", Text: "un", TimestampMS: now})
	b.store.PutMessage(Message{ID: "G2", ThreadID: "66", SenderID: "7", Text: "deux", TimestampMS: now})
	b.handleFBMessage(e2eeRevoke(group, eight, waKey("G1", false, seven.String(), group.String())))
	if m, _ := b.store.FindMessage("G1"); m.Deleted {
		t.Fatal("a non-admin must not revoke someone else's message")
	}
	b.handleFBMessage(e2eeRevoke(group, seven, waKey("G1", true, "", other.String())))
	if m, _ := b.store.FindMessage("G1"); m.Deleted {
		t.Fatal("a key pointing at another conversation must be rejected")
	}
	b.handleFBMessage(e2eeRevoke(group, seven, waKey("G1", true, "", group.String())))
	if m, _ := b.store.FindMessage("G1"); !m.Deleted || m.Text != "un" {
		t.Fatalf("the author may revoke their own group message, content kept: %+v", m)
	}
	b.store.UpdateThread("66", func(rec *threadRec) { setAdmin(rec, "8", true) })
	b.handleFBMessage(e2eeRevoke(group, eight, waKey("G2", false, seven.String(), group.String())))
	if m, _ := b.store.FindMessage("G2"); !m.Deleted || m.Text != "deux" {
		t.Fatalf("an admin may revoke a member's message, content kept: %+v", m)
	}
}

func TestDemoUnsendKeepsContent(t *testing.T) {
	buf := &syncBuffer{}
	d := NewDemoBackend(t.TempDir(), "", NewOutput(buf, zerolog.Nop()), zerolog.Nop())
	var target Message
	for _, th := range d.store.ListThreads(0) {
		for _, m := range d.store.ThreadMessages(th.ID) {
			if m.IsOutgoing && !m.Deleted && !m.System && m.Text != "" && d.store.MessagePollID(m.ID) == "" {
				target = m
				break
			}
		}
		if target.ID != "" {
			break
		}
	}
	if target.ID == "" {
		t.Fatal("the demo must contain an outgoing text message")
	}
	if err := d.Unsend(context.Background(), target.ThreadID, target.ID); err != nil {
		t.Fatal(err)
	}
	if m, _ := d.store.FindMessage(target.ID); !m.Deleted || m.Text != target.Text {
		t.Fatalf("demo unsend must keep the content: %+v", m)
	}
}

func TestExportShowsDeletedLabelWithContent(t *testing.T) {
	base := time.Date(2024, 3, 4, 9, 5, 0, 0, time.Local).UnixMilli()
	doc := exportDoc{
		Title:      "Conv",
		ExportedAt: time.Date(2024, 8, 15, 14, 7, 0, 0, time.Local),
		Messages: []Message{
			{ID: "1", SenderID: "a", SenderName: "Alice", Text: "trop tard", TimestampMS: base, Deleted: true},
			{ID: "2", SenderID: "a", SenderName: "Alice", TimestampMS: base + 1000, Deleted: true},
		},
	}
	for _, locale := range []string{"fr", "en"} {
		doc.Locale = locale
		label, removed := "Ce message a été supprimé", "Message retiré"
		if locale == "en" {
			label, removed = "This message was deleted", "Message removed"
		}
		var md, page strings.Builder
		if err := renderExportMarkdown(&md, doc); err != nil {
			t.Fatal(err)
		}
		if err := renderExportHTML(&page, doc); err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(md.String(), "_"+label+"_\ntrop tard\n") || !strings.Contains(md.String(), "_"+removed+"_\n") {
			t.Fatalf("%s markdown must label the deleted message and keep its text:\n%s", locale, md.String())
		}
		if !strings.Contains(page.String(), `<div class="tag deleted">`+label+`</div><p class="text">trop tard</p>`) ||
			!strings.Contains(page.String(), `<p class="text deleted">`+removed+`</p>`) {
			t.Fatalf("%s HTML must label the deleted message and keep its text:\n%s", locale, page.String())
		}
	}
}

func TestExportIncludesMediaOfDeletedMessages(t *testing.T) {
	b, _, _ := loggedInBackend(t)
	b.store.UpdateThread("5", func(rec *threadRec) { rec.Name = "Photos" })
	b.store.SetHasMoreBefore("5", false)
	local := filepath.Join(t.TempDir(), "photo.png")
	if err := os.WriteFile(local, encodedImage(t, 4, 4, false), 0o600); err != nil {
		t.Fatal(err)
	}
	put := func(i int, withFile, deleted bool) {
		id := "mid.$p" + strconv.Itoa(i)
		b.store.PutMessage(Message{ID: id, ThreadID: "5", SenderID: "7", TimestampMS: int64(1000 + i),
			Attachments: []Attachment{{ID: "img", Kind: "image"}}})
		if withFile {
			b.store.PutMedia(id, "img", mediaRec{Kind: "image", MimeType: "image/png", Path: local})
		}
		if deleted {
			b.store.MarkUnsent(id)
		}
	}
	put(0, true, true)
	for i := 1; i <= maxConsecutiveMediaFailures; i++ {
		put(i, false, true)
	}
	put(maxConsecutiveMediaFailures+1, true, false)
	res, err := b.ExportThread(context.Background(), ExportThreadParams{ThreadID: "5", Dir: t.TempDir(), Format: ExportJSON, IncludeMedia: true})
	if err != nil {
		t.Fatal(err)
	}
	if res.Media != 2 || res.MediaFailed != maxConsecutiveMediaFailures {
		t.Fatalf("deleted media must be exported without tripping the failure breaker: %+v", res)
	}
	entries, err := os.ReadDir(filepath.Join(res.Path, exportMediaDirName))
	if err != nil || len(entries) != 2 {
		t.Fatalf("both media files must be copied into the export: %d %v", len(entries), err)
	}
}
