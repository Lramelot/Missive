package main

import (
	"context"
	"errors"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/rs/zerolog"
	"go.mau.fi/whatsmeow/proto/waCommon"
	"go.mau.fi/whatsmeow/proto/waConsumerApplication"
	waTypes "go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
	"google.golang.org/protobuf/proto"

	"go.mau.fi/mautrix-meta/pkg/messagix/socket"
	"go.mau.fi/mautrix-meta/pkg/messagix/table"
)

func TestReviewFolderMapping(t *testing.T) {
	cases := map[string]string{
		"": "", "inbox": FolderInbox, "e2ee_cutover": FolderInbox, "interop": FolderInbox,
		"pending": FolderRequests, "other": FolderRequests, "e2ee_cutover_pending": FolderRequests, "e2ee_cutover_other": FolderRequests,
		"restricted": FolderRequests, "salsa_restricted": FolderRequests, "messenger_marketing_message": FolderRequests,
		"spam": FolderSpam, "blocked": FolderSpam,
		"archived": FolderArchived, "e2ee_cutover_archived": FolderArchived, "hidden": FolderArchived,
	}
	for in, want := range cases {
		if got := lsFolder(in); got != want {
			t.Errorf("%q: got %q, want %q", in, got, want)
		}
	}
	b, _ := groupBackend(t)
	chat := waTypes.NewJID(testPeerID, waTypes.MessengerServer)
	b.ensureE2EEThread(chat)
	b.store.SetHybrid(500, mustThreadKey(testPeerID))
	now := time.Now().UnixMilli()
	b.handleTable(&table.LSTable{LSDeleteThenInsertThread: []*table.LSDeleteThenInsertThread{{ThreadKey: 500, ThreadType: table.ONE_TO_ONE, FolderName: "pending", LastActivityTimestampMs: now}}}, modeLive)
	if v, _ := b.store.ThreadView(testPeerID); v.Folder != FolderRequests {
		t.Fatalf("a pending hybrid row must put the encrypted thread in requests, got %q", v.Folder)
	}
	b.handleTable(&table.LSTable{LSDeleteThenInsertMessageRequest: []*table.LSDeleteThenInsertMessageRequest{{ThreadKey: 500, MessageRequestStatus: 0}}}, modeLive)
	if v, _ := b.store.ThreadView(testPeerID); v.Folder != FolderInbox {
		t.Fatalf("an accepted hybrid request must go back to inbox, got %q", v.Folder)
	}
}

func TestReviewLeaveKeepsThread(t *testing.T) {
	b, buf, rec := recordingBackend(t)
	b.store.PutMessage(Message{ID: "mid.$keep", ThreadID: "55", SenderID: "7", Text: "hello", TimestampMS: time.Now().UnixMilli()})
	self := mustThreadKey(b.store.SelfID())
	rec.resp = func([]socket.Task) *table.LSTable {
		return &table.LSTable{LSRemoveParticipantFromThread: []*table.LSRemoveParticipantFromThread{{ThreadKey: 55, ParticipantId: self}}}
	}
	if err := b.LeaveThread(context.Background(), "55"); err != nil {
		t.Fatal(err)
	}
	if task := rec.last().(*socket.RemoveParticipantTask); task.ContactID != self || task.ThreadID != 55 {
		t.Fatalf("unexpected leave task %+v", task)
	}
	v, ok := b.store.ThreadView("55")
	if _, kept := b.store.MessageInThread("55", "mid.$keep"); !ok || !kept || !v.IsGroup || v.Name != "Team" || v.CanManage || v.IsAdmin {
		t.Fatalf("leaving must keep the group and its history with can_manage false: %+v kept=%v", v, kept)
	}
	if len(eventsOf(t, buf, EventThreadRemoved)) != 0 {
		t.Fatal("leaving must not emit thread_removed")
	}
	if canManage(&threadRec{IsGroup: true, Left: true}, testSelf) {
		t.Fatal("a left group with no known members must not be manageable")
	}
	b.handleTable(&table.LSTable{LSRemoveParticipantFromThread: []*table.LSRemoveParticipantFromThread{{ThreadKey: 55, ParticipantId: self}}}, modeLive)
	if !b.store.HasThread("55") {
		t.Fatal("a later self-removal row must not delete a group we left")
	}
	if err := b.RenameThread(context.Background(), "55", "x"); toErrorBody(err).Code != CodeUnsupported {
		t.Fatalf("a left group cannot be managed, got %v", err)
	}
	b.handleTable(&table.LSTable{LSAddParticipantIdToGroupThread: []*table.LSAddParticipantIdToGroupThread{{ThreadKey: 55, ContactId: self}}}, modeLive)
	if v, _ := b.store.ThreadView("55"); !v.CanManage {
		t.Fatal("being added back must make the group manageable again")
	}
}

func TestReviewNoZombieThreads(t *testing.T) {
	for name, run := range map[string]func(b *MetaBackend) error{
		"accept": func(b *MetaBackend) error { return b.AcceptRequest(context.Background(), "5") },
		"mute":   func(b *MetaBackend) error { return b.Mute(context.Background(), "5", -1) },
		"block":  func(b *MetaBackend) error { return b.BlockUser(context.Background(), "7", true) },
	} {
		b, buf, rec := recordingBackend(t)
		rec.resp = func([]socket.Task) *table.LSTable {
			return &table.LSTable{LSDeleteThread: []*table.LSDeleteThread{{ThreadKey: 5}}}
		}
		if err := run(b); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if b.store.HasThread("5") {
			t.Fatalf("%s: a thread deleted by the response must not be re-created", name)
		}
		removedAt := -1
		for i, line := range buf.Lines() {
			if strings.Contains(line, `"thread_removed"`) {
				removedAt = i
			} else if removedAt >= 0 && strings.Contains(line, `"threads"`) && strings.Contains(line, `"id":"5"`) {
				t.Fatalf("%s: thread 5 upserted after its removal", name)
			}
		}
		if removedAt < 0 {
			t.Fatalf("%s: missing thread_removed", name)
		}
	}
}

func e2eeEdit(chat, sender waTypes.JID, key *waCommon.MessageKey, text string) *events.FBMessage {
	return &events.FBMessage{
		Info: waTypes.MessageInfo{MessageSource: waTypes.MessageSource{Chat: chat, Sender: sender}, ID: "E" + text, Timestamp: time.Now()},
		Message: wrapConsumerContent(&waConsumerApplication.ConsumerApplication_Content{
			Content: &waConsumerApplication.ConsumerApplication_Content_EditMessage{EditMessage: &waConsumerApplication.ConsumerApplication_EditMessage{
				Key: key, Message: &waCommon.MessageText{Text: proto.String(text)},
			}},
		}),
	}
}

func e2eeRevoke(chat, sender waTypes.JID, key *waCommon.MessageKey) *events.FBMessage {
	return &events.FBMessage{
		Info: waTypes.MessageInfo{MessageSource: waTypes.MessageSource{Chat: chat, Sender: sender}, ID: "R" + key.GetID(), Timestamp: time.Now()},
		Message: &waConsumerApplication.ConsumerApplication{Payload: &waConsumerApplication.ConsumerApplication_Payload{
			Payload: &waConsumerApplication.ConsumerApplication_Payload_ApplicationData{ApplicationData: &waConsumerApplication.ConsumerApplication_ApplicationData{
				ApplicationContent: &waConsumerApplication.ConsumerApplication_ApplicationData_Revoke{Revoke: &waConsumerApplication.ConsumerApplication_RevokeMessage{Key: key}},
			}},
		}},
	}
}

func TestReviewE2EEEditAuthorization(t *testing.T) {
	b, _ := groupBackend(t)
	group := waTypes.NewJID("66", waTypes.GroupServer)
	other := waTypes.NewJID("67", waTypes.GroupServer)
	seven := waTypes.NewJID("7", waTypes.MessengerServer)
	eight := waTypes.NewJID("8", waTypes.MessengerServer)
	b.ensureE2EEThread(group)
	b.ensureE2EEThread(other)
	now := time.Now().UnixMilli()
	b.store.PutMessage(Message{ID: "V1", ThreadID: "66", SenderID: "7", Text: "original", TimestampMS: now})
	b.store.PutMessage(Message{ID: "V2", ThreadID: "66", SenderID: "7", Text: "second", TimestampMS: now})
	text := func(id string) string { m, _ := b.store.FindMessage(id); return m.Text }
	key := func(id string, fromMe bool, participant string, remote string) *waCommon.MessageKey {
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
	b.handleFBMessage(e2eeEdit(group, eight, key("V1", true, "", ""), "forged1"))
	b.handleFBMessage(e2eeEdit(group, eight, key("V1", false, seven.String(), ""), "forged2"))
	b.handleFBMessage(e2eeEdit(other, seven, key("V1", true, "", ""), "forged3"))
	b.handleFBMessage(e2eeEdit(group, seven, key("V1", true, "", other.String()), "forged4"))
	if got := text("V1"); got != "original" {
		t.Fatalf("foreign or cross-chat edits must be ignored, text is %q", got)
	}
	b.handleFBMessage(e2eeEdit(group, seven, key("V1", true, "", group.String()), "fixed"))
	if got := text("V1"); got != "fixed" {
		t.Fatalf("the author's own edit must apply, text is %q", got)
	}
	b.handleFBMessage(e2eeRevoke(group, eight, key("V2", false, seven.String(), "")))
	if m, _ := b.store.FindMessage("V2"); m.Deleted {
		t.Fatal("a non-admin must not revoke someone else's message")
	}
	b.store.UpdateThread("66", func(rec *threadRec) { setAdmin(rec, "8", true) })
	b.handleFBMessage(e2eeRevoke(group, eight, key("V2", false, seven.String(), "")))
	if m, _ := b.store.FindMessage("V2"); !m.Deleted {
		t.Fatal("a group admin may revoke a member's message")
	}
	peer := waTypes.NewJID(testPeerID, waTypes.MessengerServer)
	b.ensureE2EEThread(peer)
	b.store.PutMessage(Message{ID: "D1", ThreadID: testPeerID, SenderID: testSelf, IsOutgoing: true, Text: "mine", TimestampMS: now})
	b.store.PutMessage(Message{ID: "D2", ThreadID: testPeerID, SenderID: testPeerID, Text: "theirs", TimestampMS: now})
	b.handleFBMessage(e2eeEdit(peer, peer, key("D1", false, "", ""), "forged"))
	b.handleFBMessage(e2eeEdit(peer, peer, key("D2", true, "", ""), "edited"))
	if text("D1") != "mine" || text("D2") != "edited" {
		t.Fatalf("1:1 edits must only touch the sender's own messages: %q %q", text("D1"), text("D2"))
	}
}

func TestReviewLSEditRejected(t *testing.T) {
	b, _, rec := recordingBackend(t)
	editConfirmWait = 200 * time.Millisecond
	t.Cleanup(func() { editConfirmWait = 5 * time.Second })
	b.store.PutMessage(Message{ID: "mid.$e", ThreadID: "55", SenderID: testSelf, IsOutgoing: true, Text: "before", TimestampMS: time.Now().UnixMilli()})
	rec.resp = func([]socket.Task) *table.LSTable {
		return &table.LSTable{LSEditMessage: []*table.LSEditMessage{{MessageID: "mid.$e", Text: "before", EditCount: 0}}}
	}
	err := b.EditMessage(context.Background(), "55", "mid.$e", "after")
	if body := toErrorBody(err); body.Code != CodeEditRejected {
		t.Fatalf("a rejected edit must be reported, got %v", err)
	}
	if m, _ := b.store.FindMessage("mid.$e"); m.Text != "before" {
		t.Fatalf("Meta's version must be kept, got %q", m.Text)
	}
	rec.resp = func([]socket.Task) *table.LSTable {
		return &table.LSTable{LSEditMessage: []*table.LSEditMessage{{MessageID: "mid.$e", Text: "after", EditCount: 1}}}
	}
	if err := b.EditMessage(context.Background(), "55", "mid.$e", "after"); err != nil {
		t.Fatal(err)
	}
	if m, _ := b.store.FindMessage("mid.$e"); m.Text != "after" || !m.Edited {
		t.Fatalf("an accepted edit must apply: %+v", m)
	}
}

func TestReviewRevokeUploadedToHistory(t *testing.T) {
	b, _, _ := recordingBackend(t)
	chat := waTypes.NewJID(testPeerID, waTypes.MessengerServer)
	b.ensureE2EEThread(chat)
	b.store.PutMessage(Message{ID: "W9", ThreadID: testPeerID, SenderID: testSelf, IsOutgoing: true, Text: "x", TimestampMS: time.Now().UnixMilli()})
	b.waSendFn = func(context.Context, threadRec, *waConsumerApplication.ConsumerApplication) error { return nil }
	var uploads [][3]string
	b.historyUploadFn = func(chat, id, deleted string) { uploads = append(uploads, [3]string{chat, id, deleted}) }
	if err := b.EditMessage(context.Background(), testPeerID, "W9", "y"); err != nil {
		t.Fatal(err)
	}
	if len(uploads) != 0 {
		t.Fatal("edits are not uploaded to secure history (mautrix does not)")
	}
	if err := b.Unsend(context.Background(), testPeerID, "W9"); err != nil {
		t.Fatal(err)
	}
	if len(uploads) != 1 || uploads[0][0] != chat.String() || uploads[0][2] != "W9" || uploads[0][1] == "" {
		t.Fatalf("the revoke must be uploaded with the deleted id: %v", uploads)
	}
}

func TestReviewForwardPartialAndLookups(t *testing.T) {
	b, _, rec := recordingBackend(t)
	ctx := context.Background()
	b.store.PutMessage(Message{ID: "mid.$f", ThreadID: "55", SenderID: "7", Text: "x", TimestampMS: time.Now().UnixMilli()})
	b.forwardFn = func(ctx context.Context, msg Message, target threadRec) (string, error) {
		if target.ID == "5" {
			return "text-id", errors.New("attachment failed")
		}
		return "", errors.New("down")
	}
	ids, err := b.Forward(ctx, "55", "mid.$f", []string{"5", "55"})
	if err != nil || len(ids) != 2 || ids[0] != "text-id" || ids[1] != "" {
		t.Fatalf("a partially forwarded target must report its text id: %v %v", ids, err)
	}
	if _, err := b.Forward(ctx, "55", "mid.$f", []string{"55"}); err == nil {
		t.Fatal("when every target fails the command fails")
	}
	if _, err := b.OpenThread(ctx, "55"); toErrorBody(err).Code != CodeBadRequest {
		t.Fatalf("open_thread on a group key must be bad_request, got %v", err)
	}
	groupConfirmWait = 300 * time.Millisecond
	t.Cleanup(func() { groupConfirmWait = 5 * time.Second })
	now := time.Now().UnixMilli()
	rec.resp = func(tasks []socket.Task) *table.LSTable {
		if _, ok := tasks[0].(*socket.CreateGroupTask); !ok {
			return &table.LSTable{}
		}
		return &table.LSTable{
			LSDeleteThenInsertThread:        []*table.LSDeleteThenInsertThread{{ThreadKey: 4343, ThreadType: table.GROUP_THREAD, LastActivityTimestampMs: now}},
			LSAddParticipantIdToGroupThread: []*table.LSAddParticipantIdToGroupThread{{ThreadKey: 4343, ContactId: 7}, {ThreadKey: 4343, ContactId: 8}},
		}
	}
	group, err := b.CreateGroup(ctx, []string{"7", "8"}, "")
	if err != nil || group.ID != "4343" {
		t.Fatalf("a group found in the following sync must be returned: %+v %v", group, err)
	}
	rec.resp = nil
	_, err = b.CreateGroup(ctx, []string{"7", "8"}, "")
	if body := toErrorBody(err); body.Code != CodeInternal || body.Data["uncertain"] != true || !strings.Contains(body.Message, "may have been created") {
		t.Fatalf("an unconfirmed group must say it may exist: %+v", body)
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

func TestReviewForwardIsSerializedWithSends(t *testing.T) {
	buf := &syncBuffer{}
	out := NewOutput(buf, zerolog.Nop())
	lb := &forwardLaneBackend{DemoBackend: NewDemoBackend(t.TempDir(), out, zerolog.Nop())}
	disp := NewDispatcher(lb, out, zerolog.Nop())
	disp.Run(strings.NewReader(`{"id":1,"cmd":"send","params":{"thread_id":"B","text":"x"}}` + "\n" +
		`{"id":2,"cmd":"forward","params":{"from_thread_id":"A","message_id":"m","to_thread_ids":["C","B"]}}` + "\n"))
	disp.Wait()
	if strings.Join(lb.order, " ") != "send:B forward:C,B" {
		t.Fatalf("forward to B must wait for the pending send to B: %v", lb.order)
	}
}

func TestReviewDemoRequestAndVoice(t *testing.T) {
	h := newDemoHarness(t)
	stranger := demoStrangers[0].ID
	h.demo.Messages(context.Background(), stranger, 0, 50, true)
	time.Sleep(demoBackfillDelay + 300*time.Millisecond)
	msgs := h.demo.store.Messages(stranger, 0, 0)
	if len(msgs) != 1 {
		t.Fatalf("the request thread must not grow fake history, got %d messages", len(msgs))
	}
	for _, m := range msgs {
		if m.IsOutgoing || m.SenderID != stranger {
			t.Fatalf("a message request must only contain the stranger's messages: %+v", m)
		}
	}
	path, _, duration, err := demoVoiceFile(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(path)
	frames, rate, samples, err := walkMP3Frames(data)
	if err != nil || rate != 24000 || int64(frames*samples)*1000/int64(rate) < duration {
		t.Fatalf("demo voice file must be a well-formed 24 kHz mp3: %v frames=%d rate=%d", err, frames, rate)
	}
}
