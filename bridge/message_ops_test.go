package main

import (
	"context"
	"errors"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"go.mau.fi/mautrix-meta/pkg/messagix/socket"
	"go.mau.fi/mautrix-meta/pkg/messagix/table"
	"go.mau.fi/whatsmeow/proto/waConsumerApplication"
	waTypes "go.mau.fi/whatsmeow/types"
)

type taskRecorder struct {
	mu    sync.Mutex
	tasks []socket.Task
	resp  func(tasks []socket.Task) *table.LSTable
	err   error
}

func (r *taskRecorder) run(ctx context.Context, tasks ...socket.Task) (*table.LSTable, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.tasks = append(r.tasks, tasks...)
	if r.err != nil {
		return nil, r.err
	}
	if r.resp != nil {
		return r.resp(tasks), nil
	}
	return &table.LSTable{}, nil
}

func (r *taskRecorder) last() socket.Task {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.tasks) == 0 {
		return nil
	}
	return r.tasks[len(r.tasks)-1]
}

func recordingBackend(t *testing.T) (*MetaBackend, *syncBuffer, *taskRecorder) {
	t.Helper()
	b, buf := groupBackend(t)
	rec := &taskRecorder{}
	b.taskFn = rec.run
	b.store.UpdateThread("5", func(r *threadRec) {
		r.PeerID = "7"
		r.Members = map[string]bool{testSelf: true, "7": true}
	})
	b.store.UpdateThread("55", func(r *threadRec) { setAdmin(r, testSelf, true) })
	return b, buf, rec
}

func TestMetaMessageAndThreadCommands(t *testing.T) {
	b, buf, rec := recordingBackend(t)
	ctx := context.Background()
	now := time.Now().UnixMilli()
	b.store.PutMessage(Message{ID: "mid.$mine", ThreadID: "55", SenderID: testSelf, IsOutgoing: true, Text: "hi", TimestampMS: now})
	b.store.PutMessage(Message{ID: "mid.$old", ThreadID: "55", SenderID: testSelf, IsOutgoing: true, Text: "old", TimestampMS: now - 20*60_000})
	b.store.PutMessage(Message{ID: "mid.$theirs", ThreadID: "55", SenderID: "7", Text: "yo", TimestampMS: now})

	if err := b.EditMessage(ctx, "55", "mid.$mine", "hello"); err != nil {
		t.Fatal(err)
	}
	if task, ok := rec.last().(*socket.EditMessageTask); !ok || task.MessageID != "mid.$mine" || task.Text != "hello" {
		t.Fatalf("unexpected edit task %#v", rec.last())
	}
	if m, _ := b.store.FindMessage("mid.$mine"); !m.Edited || m.Text != "hello" {
		t.Fatalf("edit not applied locally: %+v", m)
	}
	if err := b.EditMessage(ctx, "55", "mid.$old", "x"); toErrorBody(err).Code != CodeBadRequest {
		t.Fatalf("edits after 15 min must be refused, got %v", err)
	}
	if err := b.EditMessage(ctx, "55", "mid.$theirs", "x"); toErrorBody(err).Code != CodeBadRequest {
		t.Fatalf("editing someone else's message must be refused, got %v", err)
	}
	if err := b.Unsend(ctx, "55", "mid.$mine"); err != nil {
		t.Fatal(err)
	}
	if task, ok := rec.last().(*socket.DeleteMessageTask); !ok || task.MessageId != "mid.$mine" {
		t.Fatalf("unexpected unsend task %#v", rec.last())
	}
	if m, _ := b.store.FindMessage("mid.$mine"); !m.Deleted || m.Text != "hello" {
		t.Fatalf("unsend must mark the message deleted and keep its content: %+v", m)
	}
	if err := b.DeleteForMe(ctx, "55", "mid.$theirs"); err != nil {
		t.Fatal(err)
	}
	if task, ok := rec.last().(*socket.DeleteMessageMeOnlyTask); !ok || task.ThreadKey != 55 || task.MessageId != "mid.$theirs" {
		t.Fatalf("unexpected delete-for-me task %#v", rec.last())
	}
	if _, ok := b.store.FindMessage("mid.$theirs"); ok || len(eventsOf(t, buf, EventMessageRemoved)) != 1 {
		t.Fatal("delete for me must remove the message and emit message_removed")
	}

	future := now + 3_600_000
	if err := b.Mute(ctx, "55", future); err != nil {
		t.Fatal(err)
	}
	if task := rec.last().(*socket.MuteThreadTask); task.ThreadKey != 55 || task.MuteExpireTimeMS != future || task.SyncGroup != 1 {
		t.Fatalf("unexpected mute task %+v", task)
	}
	if v, _ := b.store.ThreadView("55"); v.MutedUntilMS == nil || *v.MutedUntilMS != future {
		t.Fatalf("mute not stored: %+v", v.MutedUntilMS)
	}
	if err := b.RenameThread(ctx, "55", "Équipe"); err != nil {
		t.Fatal(err)
	}
	if task := rec.last().(*socket.RenameThreadTask); task.ThreadName != "Équipe" || task.ThreadKey != 55 {
		t.Fatalf("unexpected rename task %+v", task)
	}
	if err := b.RenameThread(ctx, "5", "x"); toErrorBody(err).Code != CodeUnsupported {
		t.Fatalf("renaming a 1:1 must be unsupported, got %v", err)
	}
	if err := b.AddParticipants(ctx, "55", []string{"9"}); err != nil {
		t.Fatal(err)
	}
	if task := rec.last().(*socket.AddParticipantsTask); !slices.Equal(task.ContactIDs, []int64{9}) {
		t.Fatalf("unexpected add task %+v", task)
	}
	if err := b.SetAdmin(ctx, "55", "9", true); err != nil {
		t.Fatal(err)
	}
	if task := rec.last().(*socket.UpdateAdminTask); task.ContactID != 9 || task.IsAdmin != 1 {
		t.Fatalf("unexpected admin task %+v", task)
	}
	if err := b.RemoveParticipant(ctx, "55", "9"); err != nil {
		t.Fatal(err)
	}
	if task := rec.last().(*socket.RemoveParticipantTask); task.ContactID != 9 || task.ThreadID != 55 {
		t.Fatalf("unexpected remove task %+v", task)
	}
	if err := b.CreatePoll(ctx, "55", "Où ?", []string{"Ici", "Là"}); err != nil {
		t.Fatal(err)
	}
	if task := rec.last().(*socket.CreatePollTask); task.ThreadKey != 55 || task.QuestionText != "Où ?" || !slices.Equal(task.Options, []string{"Ici", "Là"}) {
		t.Fatalf("unexpected poll task %+v", task)
	}
	if err := b.CreatePoll(ctx, "5", "Q", []string{"a", "b"}); toErrorBody(err).Code != CodeUnsupported {
		t.Fatalf("polls in 1:1 must be unsupported, got %v", err)
	}
	b.store.PutMessage(Message{ID: "mid.$p", ThreadID: "55", SenderID: "7", TimestampMS: now})
	b.store.UpsertPoll("77", "55", "mid.$p", "Q", []pollOptionRec{{ID: "1", Text: "a"}, {ID: "2", Text: "b"}}, true)
	if err := b.VotePoll(ctx, "55", "77", []string{"2", "1"}); err != nil {
		t.Fatal(err)
	}
	if task := rec.last().(*socket.UpdatePollTask); task.PollID != 77 || !slices.Equal(task.SelectedOptions, []int64{1, 2}) {
		t.Fatalf("unexpected vote task %+v", task)
	}
	if err := b.VotePoll(ctx, "55", "77", []string{"9"}); toErrorBody(err).Code != CodeBadRequest {
		t.Fatalf("unknown option must be refused, got %v", err)
	}
	if err := b.BlockUser(ctx, "7", true); err != nil {
		t.Fatal(err)
	}
	if task := rec.last().(*socket.SetUserBlockStatusTask); task.BlockeeID != 7 || task.UserBlockAction != 1 || task.RequestID == "" {
		t.Fatalf("unexpected block task %+v", task)
	}
	if v, _ := b.store.ThreadView("5"); !v.Blocked {
		t.Fatal("1:1 must be flagged blocked")
	}
	if err := b.AcceptRequest(ctx, "5"); err != nil {
		t.Fatal(err)
	}
	if task := rec.last().(*socket.AcceptMessageRequestTask); task.ThreadKey != 5 || task.SyncGroup != 1 {
		t.Fatalf("unexpected accept task %+v", task)
	}

	rec.resp = func(tasks []socket.Task) *table.LSTable {
		if g, ok := tasks[0].(*socket.CreateGroupTask); ok {
			return &table.LSTable{LSReplaceOptimisticThread: []*table.LSReplaceOptimisticThread{{ThreadKey1: g.SendPayload.ThreadID, ThreadKey2: 4242}}}
		}
		return &table.LSTable{}
	}
	group, err := b.CreateGroup(ctx, []string{"7", "8"}, "Nouveau")
	if err != nil || group.ID != "4242" || !group.IsGroup || !group.IsAdmin || group.Name != "Nouveau" {
		t.Fatalf("unexpected group %+v %v", group, err)
	}
	if task := rec.last().(*socket.RenameThreadTask); task.ThreadKey != 4242 {
		t.Fatalf("group name must be set after creation: %+v", task)
	}
	existing, err := b.OpenThread(ctx, "7")
	if err != nil || existing.ID != "5" {
		t.Fatalf("open_thread must reuse the existing 1:1: %+v %v", existing, err)
	}
	fresh, err := b.OpenThread(ctx, "8")
	if err != nil || fresh.ID != "8" || fresh.PeerID != "8" {
		t.Fatalf("open_thread must create the 1:1: %+v %v", fresh, err)
	}
	if task := rec.last().(*socket.CreateThreadTask); task.ThreadFBID != 8 {
		t.Fatalf("unexpected create thread task %+v", task)
	}
	rec.resp = func(tasks []socket.Task) *table.LSTable {
		return &table.LSTable{LSInsertSearchResult: []*table.LSInsertSearchResult{
			{ResultId: "7", OtherUserId: 7, DisplayName: "Camille Martin", ThreadType: table.ONE_TO_ONE, CanViewerMessage: true, Type_: table.SearchTypeContact},
			{ResultId: "31", OtherUserId: 31, DisplayName: "Camille Dupont", ThreadType: table.ONE_TO_ONE, CanViewerMessage: true, Type_: table.SearchTypeNonContact},
			{ResultId: "55", DisplayName: "Team", ThreadType: table.GROUP_THREAD, CanViewerMessage: true},
		}}
	}
	users, err := b.SearchUsers(ctx, "camille", 10)
	if err != nil || len(users) != 2 || users[0].ID != "7" || users[0].ThreadID != "5" || users[1].ID != "31" || users[1].IsFriend {
		t.Fatalf("unexpected search results %+v %v", users, err)
	}
	if err := b.DeleteThread(ctx, "55"); err != nil {
		t.Fatal(err)
	}
	if task := rec.last().(*socket.DeleteThreadTask); task.ThreadKey != 55 {
		t.Fatalf("unexpected delete thread task %+v", task)
	}
	if b.store.HasThread("55") || len(eventsOf(t, buf, EventThreadRemoved)) != 1 {
		t.Fatal("delete_thread must remove the thread and emit thread_removed")
	}
	rec.resp = func([]socket.Task) *table.LSTable {
		return &table.LSTable{LSHandleFailedTask: []*table.LSHandleFailedTask{{Message: "nope"}}}
	}
	if err := b.Mute(ctx, "5", -1); err == nil {
		t.Fatal("a failed task must surface as an error")
	}
	if v, _ := b.store.ThreadView("5"); v.MutedUntilMS != nil {
		t.Fatal("a refused mute must not change the local state")
	}
}

func TestMetaE2EEMessageCommands(t *testing.T) {
	b, _, rec := recordingBackend(t)
	ctx := context.Background()
	chat := waTypes.NewJID(testPeerID, waTypes.MessengerServer)
	b.ensureE2EEThread(chat)
	now := time.Now().UnixMilli()
	b.store.PutMessage(Message{ID: "W1", ThreadID: testPeerID, SenderID: testSelf, IsOutgoing: true, Text: "salut", TimestampMS: now})
	var sent []*waConsumerApplication.ConsumerApplication
	b.waSendFn = func(ctx context.Context, rec threadRec, consumer *waConsumerApplication.ConsumerApplication) error {
		sent = append(sent, consumer)
		return nil
	}
	if err := b.EditMessage(ctx, testPeerID, "W1", "bonjour"); err != nil {
		t.Fatal(err)
	}
	edit := sent[0].GetPayload().GetContent().GetEditMessage()
	if edit.GetKey().GetID() != "W1" || !edit.GetKey().GetFromMe() || edit.GetMessage().GetText() != "bonjour" {
		t.Fatalf("unexpected E2EE edit %+v", edit)
	}
	if err := b.Unsend(ctx, testPeerID, "W1"); err != nil {
		t.Fatal(err)
	}
	if revoke := sent[1].GetPayload().GetApplicationData().GetRevoke(); revoke.GetKey().GetID() != "W1" {
		t.Fatalf("unexpected E2EE revoke %+v", revoke)
	}
	before := len(rec.tasks)
	if err := b.DeleteForMe(ctx, testPeerID, "W1"); err != nil {
		t.Fatal(err)
	}
	if len(rec.tasks) != before {
		t.Fatal("E2EE delete for me is local only")
	}
	if err := b.CreatePoll(ctx, testPeerID, "Q", []string{"a", "b"}); toErrorBody(err).Code != CodeUnsupported {
		t.Fatalf("polls are LS only, got %v", err)
	}
	if err := b.Mute(ctx, testPeerID, -1); err != nil {
		t.Fatal(err)
	}
	if task := rec.last().(*socket.MuteThreadTask); task.ThreadKey != mustThreadKey(testPeerID) || task.SyncGroup != 1 {
		t.Fatalf("without a Facebook key, the WA JID is used with sync group 1: %+v", task)
	}
	if err := b.DeleteThread(ctx, testPeerID); err != nil {
		t.Fatal(err)
	}
	if task := rec.last().(*socket.DeleteThreadTask); task.ThreadKey != mustThreadKey(testPeerID) || task.SyncGroup != 1 || b.store.HasThread(testPeerID) {
		t.Fatalf("E2EE-only delete must send DeleteThreadTask(JID, 1) like mautrix: %+v", task)
	}
	b.store.SetHybrid(88, mustThreadKey(testPeerID))
	b.ensureE2EEThread(chat)
	if err := b.Mute(ctx, testPeerID, -1); err != nil {
		t.Fatal(err)
	}
	if task := rec.last().(*socket.MuteThreadTask); task.ThreadKey != 88 || task.SyncGroup != 95 || task.MuteExpireTimeMS != -1 {
		t.Fatalf("hybrid mute must use the FB thread key in sync group 95: %+v", task)
	}
}

func TestLSEditRejected(t *testing.T) {
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

func TestRevokeUploadedToHistory(t *testing.T) {
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

func TestForwardPartialAndLookups(t *testing.T) {
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
