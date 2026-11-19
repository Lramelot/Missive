package main

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"go.mau.fi/whatsmeow/proto/waConsumerApplication"

	"go.mau.fi/mautrix-meta/pkg/messagix/socket"
	"go.mau.fi/mautrix-meta/pkg/messagix/table"
	waTypes "go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
)

const readBaseMS = int64(1_763_000_000_000)

type waReadCall struct {
	chat   waTypes.JID
	sender waTypes.JID
	ids    []string
}

type waReadRecorder struct {
	mu    sync.Mutex
	calls []waReadCall
	fail  func(sender waTypes.JID) error
	block chan struct{}
}

func recordWAReads(b *MetaBackend) *waReadRecorder {
	r := &waReadRecorder{}
	b.waReadFn = func(ctx context.Context, chat, sender waTypes.JID, ids []waTypes.MessageID) error {
		r.mu.Lock()
		block := r.block
		r.mu.Unlock()
		if block != nil {
			select {
			case <-block:
			case <-time.After(2 * time.Second):
			}
		}
		r.mu.Lock()
		defer r.mu.Unlock()
		r.calls = append(r.calls, waReadCall{chat: chat, sender: sender, ids: slices.Clone(ids)})
		if r.fail != nil {
			return r.fail(sender)
		}
		return nil
	}
	return r
}

func (r *waReadRecorder) snapshot() []waReadCall {
	r.mu.Lock()
	defer r.mu.Unlock()
	return slices.Clone(r.calls)
}

func markReadTasks(rec *taskRecorder) []socket.ThreadMarkReadTask {
	rec.mu.Lock()
	defer rec.mu.Unlock()
	var out []socket.ThreadMarkReadTask
	for _, task := range rec.tasks {
		if mr, ok := task.(*socket.ThreadMarkReadTask); ok {
			out = append(out, *mr)
		}
	}
	return out
}

func putIncoming(b *MetaBackend, threadID, id, sender string, ts int64) {
	b.store.PutMessage(Message{ID: id, ThreadID: threadID, SenderID: sender, Text: "x", TimestampMS: ts})
}

func TestMarkReadE2EEOneToOneWithIdentityMapping(t *testing.T) {
	b, _, rec := recordingBackend(t)
	reads := recordWAReads(b)
	ctx := context.Background()
	key := mustThreadKey(testPeerID)
	b.ensureE2EEThread(waTypes.NewJID(testPeerID, waTypes.MessengerServer))
	b.store.SetHybrid(key, key)
	putIncoming(b, testPeerID, "W0", testPeerID, readBaseMS-120_000)
	putIncoming(b, testPeerID, "W1", testPeerID, readBaseMS)
	putIncoming(b, testPeerID, "W2", testPeerID, readBaseMS+1000)
	b.store.PutMessage(Message{ID: "W3", ThreadID: testPeerID, SenderID: testSelf, IsOutgoing: true, Text: "x", TimestampMS: readBaseMS + 2000})
	b.store.UpdateThread(testPeerID, func(r *threadRec) {
		r.LastActivityMS = readBaseMS + 2000
		r.LastReadMS = readBaseMS + 2000
		r.MetaReadMS = readBaseMS - 60_000
	})
	if err := b.MarkRead(ctx, testPeerID); err != nil {
		t.Fatal(err)
	}
	calls := reads.snapshot()
	if len(calls) != 1 || !calls[0].sender.IsEmpty() || calls[0].chat != waTypes.NewJID(testPeerID, waTypes.MessengerServer) || !slices.Equal(calls[0].ids, []string{"W1", "W2"}) {
		t.Fatalf("read receipts must cover what Meta still sees unread, despite the local watermark: %+v", calls)
	}
	want := socket.ThreadMarkReadTask{ThreadId: key, LastReadWatermarkTs: readBaseMS + 2000 + 999, SyncGroup: 95}
	if tasks := markReadTasks(rec); len(tasks) != 1 || tasks[0] != want {
		t.Fatalf("a hybrid mapping where the Facebook key equals the JID must still mark read in sync group 95: %+v", tasks)
	}
	if err := b.MarkRead(ctx, testPeerID); err != nil {
		t.Fatal(err)
	}
	if calls := reads.snapshot(); len(calls) != 1 {
		t.Fatalf("receipts must not be sent twice for the same messages: %+v", calls)
	}
	if tasks := markReadTasks(rec); len(tasks) != 1 {
		t.Fatalf("mark_read without anything new must not resend the watermark: %+v", tasks)
	}
	putIncoming(b, testPeerID, "W4", testPeerID, readBaseMS+3500)
	if err := b.MarkRead(ctx, testPeerID); err != nil {
		t.Fatal(err)
	}
	want = socket.ThreadMarkReadTask{ThreadId: key, LastReadWatermarkTs: readBaseMS + 3500, SyncGroup: 95}
	if tasks := markReadTasks(rec); len(tasks) != 2 || tasks[1] != want {
		t.Fatalf("new activity must move the watermark again: %+v", tasks)
	}
	if calls := reads.snapshot(); len(calls) != 2 || !slices.Equal(calls[1].ids, []string{"W4"}) {
		t.Fatalf("only the new message is receipted: %+v", calls)
	}
}

func TestMarkReadLateOlderMessageStillReceipted(t *testing.T) {
	b, _, rec := recordingBackend(t)
	reads := recordWAReads(b)
	ctx := context.Background()
	key := mustThreadKey(testPeerID)
	b.ensureE2EEThread(waTypes.NewJID(testPeerID, waTypes.MessengerServer))
	b.store.SetHybrid(key, key)
	putIncoming(b, testPeerID, "W1", testPeerID, readBaseMS)
	putIncoming(b, testPeerID, "W3", testPeerID, readBaseMS+8000)
	b.store.UpdateThread(testPeerID, func(r *threadRec) {
		r.LastActivityMS = readBaseMS + 60_000
		r.MetaReadMS = readBaseMS - 60_000
	})
	if err := b.MarkRead(ctx, testPeerID); err != nil {
		t.Fatal(err)
	}
	want := socket.ThreadMarkReadTask{ThreadId: key, LastReadWatermarkTs: readBaseMS + 8000 + 999, SyncGroup: 95}
	if tasks := markReadTasks(rec); len(tasks) != 1 || tasks[0] != want {
		t.Fatalf("the watermark is the newest stored message, never the thread activity: %+v", tasks)
	}
	if r, _ := b.store.Thread(testPeerID); r.MetaReadMS != readBaseMS-60_000 || r.MarkedReadMS != readBaseMS+8000 {
		t.Fatalf("Meta's own watermark must not be faked: meta=%d marked=%d", r.MetaReadMS, r.MarkedReadMS)
	}
	putIncoming(b, testPeerID, "W2late", testPeerID, readBaseMS+5000)
	if err := b.MarkRead(ctx, testPeerID); err != nil {
		t.Fatal(err)
	}
	calls := reads.snapshot()
	if len(calls) != 2 || !slices.Equal(calls[1].ids, []string{"W2late"}) {
		t.Fatalf("a message delivered late, older than the newest one, still gets its receipt: %+v", calls)
	}
}

func TestMarkReadHybridGroupWithDistinctFacebookKey(t *testing.T) {
	b, _, rec := recordingBackend(t)
	reads := recordWAReads(b)
	ctx := context.Background()
	group := "120000000000555"
	fbKey := int64(8_800_000_000_001)
	b.ensureE2EEThread(waTypes.NewJID(group, waTypes.GroupServer))
	b.store.SetHybrid(fbKey, mustThreadKey(group))
	putIncoming(b, group, "W0", "7", readBaseMS-120_000)
	putIncoming(b, group, "mid.$legacy", "7", readBaseMS-5000)
	putIncoming(b, group, "W1", "7", readBaseMS)
	putIncoming(b, group, "W2", "8", readBaseMS+1500)
	b.store.UpdateThread(group, func(r *threadRec) {
		r.LastActivityMS = readBaseMS + 1500
		r.LastReadMS = readBaseMS + 10_000
		r.MetaReadMS = readBaseMS - 60_000
	})
	failing := true
	reads.fail = func(sender waTypes.JID) error {
		if failing && sender.User == "8" {
			return errors.New("receipt refused")
		}
		return nil
	}
	if err := b.MarkRead(ctx, group); err == nil {
		t.Fatal("a refused receipt must surface as an error")
	}
	calls := reads.snapshot()
	if len(calls) != 2 || calls[0].sender != waTypes.NewJID("7", waTypes.MessengerServer) || !slices.Equal(calls[0].ids, []string{"W1"}) ||
		calls[1].sender != waTypes.NewJID("8", waTypes.MessengerServer) || !slices.Equal(calls[1].ids, []string{"W2"}) {
		t.Fatalf("group receipts are grouped by sender and skip LS ids and what Meta already read: %+v", calls)
	}
	want := socket.ThreadMarkReadTask{ThreadId: fbKey, LastReadWatermarkTs: readBaseMS + 1500, SyncGroup: 95}
	if tasks := markReadTasks(rec); len(tasks) != 1 || tasks[0] != want {
		t.Fatalf("a hybrid thread is marked read on its Facebook key in sync group 95: %+v", tasks)
	}
	failing = false
	if err := b.MarkRead(ctx, group); err != nil {
		t.Fatal(err)
	}
	calls = reads.snapshot()[2:]
	if len(calls) != 1 || !slices.Equal(calls[0].ids, []string{"W2"}) {
		t.Fatalf("only the refused receipt is retried: %+v", calls)
	}
}

func TestMarkReadLSThread(t *testing.T) {
	b, _, rec := recordingBackend(t)
	reads := recordWAReads(b)
	b.store.PutMessage(Message{ID: "mid.$a", ThreadID: "5", SenderID: "7", Text: "x", TimestampMS: readBaseMS})
	b.store.UpdateThread("5", func(r *threadRec) { r.LastActivityMS = readBaseMS })
	if err := b.MarkRead(context.Background(), "5"); err != nil {
		t.Fatal(err)
	}
	want := socket.ThreadMarkReadTask{ThreadId: 5, LastReadWatermarkTs: readBaseMS, SyncGroup: 1}
	if tasks := markReadTasks(rec); len(tasks) != 1 || tasks[0] != want {
		t.Fatalf("LS threads are marked read on their key in sync group 1: %+v", tasks)
	}
	if calls := reads.snapshot(); len(calls) != 0 {
		t.Fatalf("LS threads never send WA receipts: %+v", calls)
	}
	if err := b.MarkRead(context.Background(), "5"); err != nil {
		t.Fatal(err)
	}
	if tasks := markReadTasks(rec); len(tasks) != 1 {
		t.Fatalf("nothing new: no second task: %+v", tasks)
	}
	b.handleTable(&table.LSTable{LSMarkThreadRead: []*table.LSMarkThreadRead{{ThreadKey: 5, LastReadWatermarkTimestampMs: readBaseMS - 1000}}}, modeLive)
	b.store.UpdateThread("5", func(r *threadRec) { r.UnreadCount = 1 })
	if err := b.MarkRead(context.Background(), "5"); err != nil {
		t.Fatal(err)
	}
	if tasks := markReadTasks(rec); len(tasks) != 2 || tasks[1] != want {
		t.Fatalf("a thread Meta reports unread again must be re-marked: %+v", tasks)
	}
}

func TestHybridKeyPrefersMostRecentMapping(t *testing.T) {
	s := NewStore()
	s.SetHybrid(111, 900)
	s.SetHybrid(222, 900)
	s.SetHybrid(900, 900)
	for range 20 {
		if fb, ok := s.HybridFBKey(900); !ok || fb != 900 {
			t.Fatalf("most recent mapping must win: %d", fb)
		}
		if fb, ok := s.FBKeyFor(900); !ok || fb != 222 {
			t.Fatalf("most recent distinct mapping must win: %d", fb)
		}
	}
	s.SetHybrid(111, 900)
	if fb, _ := s.HybridFBKey(900); fb != 111 {
		t.Fatalf("seeing a mapping again makes it the most recent: %d", fb)
	}
}

func TestE2EESendMarksThreadRead(t *testing.T) {
	b, _, rec := recordingBackend(t)
	reads := recordWAReads(b)
	key := mustThreadKey(testPeerID)
	b.ensureE2EEThread(waTypes.NewJID(testPeerID, waTypes.MessengerServer))
	var sent int
	b.waSendFn = func(ctx context.Context, rec threadRec, consumer *waConsumerApplication.ConsumerApplication) error {
		sent++
		return nil
	}
	putIncoming(b, testPeerID, "W1", testPeerID, readBaseMS)
	b.store.UpdateThread(testPeerID, func(r *threadRec) {
		r.LastActivityMS = readBaseMS
		r.LastReadMS = readBaseMS
	})
	release := make(chan struct{})
	reads.mu.Lock()
	reads.block = release
	reads.mu.Unlock()
	before := time.Now().UnixMilli()
	id, err := b.Send(context.Background(), testPeerID, "salut", "", "", nil)
	if err != nil || id == "" || sent != 1 {
		t.Fatalf("send failed: %q %v (%d)", id, err, sent)
	}
	if elapsed := time.Now().UnixMilli() - before; elapsed > 1000 {
		t.Fatalf("the read mark after a send must not delay the send result (%d ms)", elapsed)
	}
	close(release)
	waitFor(t, 3*time.Second, func() bool { return len(markReadTasks(rec)) > 0 })
	calls := reads.snapshot()
	if len(calls) != 1 || !slices.Equal(calls[0].ids, []string{"W1"}) {
		t.Fatalf("sending in an encrypted chat reads it like the official clients: %+v", calls)
	}
	tasks := markReadTasks(rec)
	if len(tasks) != 1 || tasks[0].ThreadId != key || tasks[0].SyncGroup != 95 || tasks[0].LastReadWatermarkTs < before {
		t.Fatalf("sending must also move the Facebook read watermark past the sent message: %+v", tasks)
	}
}

func TestSendWithoutMarkRead(t *testing.T) {
	b, _, rec := recordingBackend(t)
	reads := recordWAReads(b)
	rec.resp = func(tasks []socket.Task) *table.LSTable {
		for _, task := range tasks {
			if send, ok := task.(*socket.SendMessageTask); ok {
				return &table.LSTable{LSReplaceOptimsiticMessage: []*table.LSReplaceOptimsiticMessage{{OfflineThreadingId: strconv.FormatInt(send.Otid, 10), MessageId: "mid.$sent" + strconv.Itoa(len(tasks))}}}
			}
		}
		return &table.LSTable{}
	}
	b.ensureE2EEThread(waTypes.NewJID(testPeerID, waTypes.MessengerServer))
	b.waSendFn = func(ctx context.Context, rec threadRec, consumer *waConsumerApplication.ConsumerApplication) error {
		return nil
	}
	putIncoming(b, testPeerID, "W1", testPeerID, readBaseMS)
	discreet := sendReadContext(context.Background(), &Request{Cmd: "send", Params: json.RawMessage(`{"thread_id":"5","text":"x","mark_read":false}`)})
	if _, err := b.Send(discreet, "5", "x", "", "", nil); err != nil {
		t.Fatal(err)
	}
	rec.mu.Lock()
	lsTasks := slices.Clone(rec.tasks)
	rec.mu.Unlock()
	if len(lsTasks) != 1 {
		t.Fatalf("mark_read:false must send the LS message without ThreadMarkReadTask: %+v", lsTasks)
	}
	if _, err := b.Send(discreet, testPeerID, "x", "", "", nil); err != nil {
		t.Fatal(err)
	}
	time.Sleep(100 * time.Millisecond)
	if tasks, calls := markReadTasks(rec), reads.snapshot(); len(tasks) != 0 || len(calls) != 0 {
		t.Fatalf("mark_read:false must not mark the encrypted chat read: %+v %+v", tasks, calls)
	}
	if _, err := b.Send(context.Background(), "5", "y", "", "", nil); err != nil {
		t.Fatal(err)
	}
	if tasks := markReadTasks(rec); len(tasks) != 1 || tasks[0].ThreadId != 5 || tasks[0].SyncGroup != 1 {
		t.Fatalf("by default the LS send bundles the mark-read task like mautrix: %+v", tasks)
	}
}

func TestReadReceiptsOnlyFromMarkReadAndSend(t *testing.T) {
	b, _, rec := recordingBackend(t)
	reads := recordWAReads(b)
	ctx := context.Background()
	now := time.Now().UnixMilli()
	b.ensureE2EEThread(waTypes.NewJID(testPeerID, waTypes.MessengerServer))
	b.store.SetHybrid(mustThreadKey(testPeerID), mustThreadKey(testPeerID))
	putIncoming(b, testPeerID, "W1", testPeerID, now-1000)
	b.store.SetHasMoreBefore(testPeerID, false)
	b.store.SetHasMoreBefore("5", false)
	b.handleTable(&table.LSTable{LSInsertMessage: []*table.LSInsertMessage{{ThreadKey: 5, MessageId: "mid.$in", TimestampMs: now, SenderId: 7, Text: "coucou"}}}, modeLive)
	b.handleFBMessage(consumerEvent("W2", &waConsumerApplication.ConsumerApplication_Content{
		Content: &waConsumerApplication.ConsumerApplication_Content_MessageText{MessageText: waOutgoingText("salut", nil)},
	}))
	if _, ok := b.store.FindMessage("W2"); !ok {
		t.Fatal("the incoming encrypted message was not stored")
	}
	b.handleWAReceipt(&events.Receipt{
		MessageSource: waTypes.MessageSource{Chat: waTypes.NewJID(testPeerID, waTypes.MessengerServer), Sender: waTypes.NewJID(testPeerID, waTypes.MessengerServer)},
		MessageIDs:    []string{"W1"},
		Timestamp:     time.Now(),
		Type:          waTypes.ReceiptTypeRead,
	})
	for _, id := range []string{"5", testPeerID} {
		if _, err := b.Messages(ctx, id, 0, 50, false); err != nil {
			t.Fatal(err)
		}
	}
	if err := b.SetActive(ctx, true); err != nil {
		t.Fatal(err)
	}
	if _, err := b.SearchFiltered(ctx, SearchFilter{ThreadID: "5"}, 10); err != nil {
		t.Fatal(err)
	}
	if _, err := b.ExportThread(ctx, ExportThreadParams{ThreadID: testPeerID, Dir: t.TempDir(), Format: ExportJSON}); err != nil {
		t.Fatal(err)
	}
	if _, err := b.SaveMedia(ctx, SaveMediaParams{ThreadID: "5", Dir: t.TempDir()}); err != nil {
		t.Fatal(err)
	}
	if tasks, calls := markReadTasks(rec), reads.snapshot(); len(tasks) != 0 || len(calls) != 0 {
		t.Fatalf("only mark_read and sending may send read receipts: %+v %+v", tasks, calls)
	}
	for _, id := range []string{"5", testPeerID} {
		if err := b.MarkRead(ctx, id); err != nil {
			t.Fatal(err)
		}
	}
	if tasks, calls := markReadTasks(rec), reads.snapshot(); len(tasks) != 2 || len(calls) != 1 {
		t.Fatalf("mark_read must send LS and E2EE receipts: %+v %+v", tasks, calls)
	}
}

func TestReadReceipts(t *testing.T) {
	b, buf, _ := newTestMetaBackend(t)
	b.store.SetSelfID(testSelf)
	ts := time.Now().UnixMilli()
	b.handleTable(&table.LSTable{
		LSDeleteThenInsertThread: []*table.LSDeleteThenInsertThread{{ThreadKey: 77, ThreadType: table.GROUP_THREAD, ThreadName: "Team", LastActivityTimestampMs: ts}},
		LSVerifyContactRowExists: []*table.LSVerifyContactRowExists{{ContactId: 8, Name: "Eight", ProfilePictureUrl: "https://cdn/8.jpg"}, {ContactId: 9, Name: "Nine"}},
		LSAddParticipantIdToGroupThread: []*table.LSAddParticipantIdToGroupThread{
			{ThreadKey: 77, ContactId: 8, Nickname: "Huit", ReadWatermarkTimestampMs: ts - 5000},
			{ThreadKey: 77, ContactId: 999, ReadWatermarkTimestampMs: ts},
		},
	}, modeHistory)
	th, _ := b.store.ThreadView("77")
	if len(th.ReadReceipts) != 1 || th.ReadReceipts[0] != (ReadReceipt{UserID: "8", Name: "Huit", AvatarURL: "https://cdn/8.jpg", ReadMS: ts - 5000}) {
		t.Fatalf("unexpected receipts %+v", th.ReadReceipts)
	}
	before := len(eventsOf(t, buf, EventThreads))
	b.handleTable(&table.LSTable{
		LSUpdateReadReceipt: []*table.LSUpdateReadReceipt{{ThreadKey: 77, ContactId: 9, ReadWatermarkTimestampMs: ts - 1000}, {ThreadKey: 77, ContactId: 8, ReadWatermarkTimestampMs: ts - 9000}},
	}, modeLive)
	th, _ = b.store.ThreadView("77")
	if len(th.ReadReceipts) != 2 || th.ReadReceipts[0].UserID != "9" || th.ReadReceipts[1].ReadMS != ts-5000 {
		t.Fatalf("receipts must be max-merged and sorted newest first: %+v", th.ReadReceipts)
	}
	if len(eventsOf(t, buf, EventThreads)) != before+1 || len(eventsOf(t, buf, EventRead)) != 2 {
		t.Fatal("live receipt change must emit a threads upsert and read events")
	}

	chat := waTypes.NewJID(testPeerID, waTypes.MessengerServer)
	b.ensureE2EEThread(chat)
	b.handleWAReceipt(&events.Receipt{MessageSource: waTypes.MessageSource{Chat: chat, Sender: chat}, Type: waTypes.ReceiptTypeRead, Timestamp: time.UnixMilli(ts)})
	if th, _ := b.store.ThreadView(testPeerID); len(th.ReadReceipts) != 1 || th.ReadReceipts[0].ReadMS != ts {
		t.Fatalf("e2ee receipt not stored: %+v", th.ReadReceipts)
	}
	b.store.UpdateThread("1234", func(rec *threadRec) { rec.IsGroup = true })
	empty, _ := b.store.ThreadView("1234")
	if raw, _ := json.Marshal(empty); !strings.Contains(string(raw), `"read_receipts":[]`) {
		t.Fatalf("empty read_receipts must serialize as []: %s", raw)
	}
}
