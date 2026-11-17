package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"go.mau.fi/whatsmeow/proto/waConsumerApplication"
	"go.mau.fi/whatsmeow/proto/waMediaTransport"
	waTypes "go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
	"google.golang.org/protobuf/proto"

	"go.mau.fi/mautrix-meta/pkg/messagix/socket"
	"go.mau.fi/mautrix-meta/pkg/messagix/table"
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

func TestLSThreadV11Fields(t *testing.T) {
	b, buf := groupBackend(t)
	now := time.Now().UnixMilli()
	b.handleTable(&table.LSTable{
		LSDeleteThenInsertThread: []*table.LSDeleteThenInsertThread{
			{ThreadKey: 55, ThreadType: table.GROUP_THREAD, ThreadName: "Team", FolderName: "inbox", LastActivityTimestampMs: now, CustomEmoji: "🍕", OutgoingBubbleColor: 0xFF7B61FF, ThemeFbid: 42, MuteExpireTimeMs: -1},
			{ThreadKey: 66, ThreadType: table.ONE_TO_ONE, FolderName: "pending", LastActivityTimestampMs: now},
			{ThreadKey: 67, ThreadType: table.ONE_TO_ONE, FolderName: "spam", LastActivityTimestampMs: now, MuteExpireTimeMs: now + 3_600_000},
		},
		LSUpsertGradientColor: []*table.LSUpsertGradientColor{{ThemeFbid: 42, GradientIndex: 1, Color: 0xFF00FF00}, {ThemeFbid: 42, GradientIndex: 0, Color: 0xFFFF0000}},
		LSSetPinnedMessage: []*table.LSSetPinnedMessage{
			{ThreadKey: 55, MessageId: "mid.$old", PinnedTimestampMs: 100},
			{ThreadKey: 55, MessageId: "mid.$new", PinnedTimestampMs: 200},
		},
		LSUpdateThreadParticipantAdminStatus: []*table.LSUpdateThreadParticipantAdminStatus{{ThreadKey: 55, ContactId: 999, IsAdmin: true}, {ThreadKey: 55, ContactId: 7, IsAdmin: true}},
		LSUpdateDeliveryReceipt:              []*table.LSUpdateDeliveryReceipt{{ThreadKey: 55, ContactId: 8, DeliveredWatermarkTimestampMs: now - 1000}},
		LSChangeViewerStatus:                 []*table.LSChangeViewerStatus{{ThreadKey: 66, CannotReplyReason: "blocked"}},
	}, modeLive)
	group, _ := b.store.ThreadView("55")
	if group.QuickEmoji != "🍕" || group.Theme == nil || group.Theme.Accent != "#7B61FF" || !slices.Equal(group.Theme.Gradient, []string{"#FF0000", "#00FF00"}) {
		t.Fatalf("theme not mapped: %+v %+v", group.QuickEmoji, group.Theme)
	}
	if group.MutedUntilMS == nil || *group.MutedUntilMS != -1 || !group.Muted {
		t.Fatalf("permanent mute must be -1: %+v", group.MutedUntilMS)
	}
	if !slices.Equal(group.PinnedMessageIDs, []string{"mid.$new", "mid.$old"}) {
		t.Fatalf("pins must be most recent first: %v", group.PinnedMessageIDs)
	}
	if !group.IsAdmin || !group.CanManage || group.Folder != FolderInbox {
		t.Fatalf("admin/can_manage/folder wrong: %+v", group)
	}
	admins := map[string]bool{}
	for _, p := range group.Participants {
		admins[p.UserID] = p.IsAdmin
	}
	if !admins["7"] || admins["8"] {
		t.Fatalf("participant admin flags wrong: %v", admins)
	}
	var delivered int64
	for _, r := range group.ReadReceipts {
		if r.UserID == "8" {
			delivered = r.DeliveredMS
		}
	}
	if delivered != now-1000 {
		t.Fatalf("delivery receipt missing: %+v", group.ReadReceipts)
	}
	request, _ := b.store.ThreadView("66")
	spam, _ := b.store.ThreadView("67")
	if request.Folder != FolderRequests || !request.Blocked || request.CanManage || spam.Folder != FolderSpam || spam.MutedUntilMS == nil || *spam.MutedUntilMS != now+3_600_000 {
		t.Fatalf("folders/block/mute wrong: %+v / %+v", request, spam)
	}
	raw, _ := json.Marshal(request.normalized())
	for _, field := range []string{`"folder":"requests"`, `"pinned_message_ids":[]`, `"can_manage":false`, `"is_admin":false`, `"blocked":true`} {
		if !strings.Contains(string(raw), field) {
			t.Fatalf("missing %s in %s", field, raw)
		}
	}
	if strings.Contains(string(raw), "muted_until_ms") || strings.Contains(string(raw), "theme") || strings.Contains(string(raw), "quick_emoji") {
		t.Fatalf("unset optional fields must be absent: %s", raw)
	}
	b.handleTable(&table.LSTable{
		LSClearPinnedMessages:            []*table.LSClearPinnedMessages{{ThreadKey: 55}},
		LSMoveThreadToArchivedFolder:     []*table.LSMoveThreadToArchivedFolder{{ThreadKey: 55}},
		LSDeleteThenInsertMessageRequest: []*table.LSDeleteThenInsertMessageRequest{{ThreadKey: 67, MessageRequestStatus: 1}, {ThreadKey: 66, MessageRequestStatus: 0}},
		LSUpdateThreadMuteSetting:        []*table.LSUpdateThreadMuteSetting{{ThreadKey: 55, MuteExpireTimeMS: 0}},
	}, modeLive)
	group, _ = b.store.ThreadView("55")
	spam, _ = b.store.ThreadView("67")
	request, _ = b.store.ThreadView("66")
	if len(group.PinnedMessageIDs) != 0 || group.Folder != FolderArchived || group.MutedUntilMS != nil {
		t.Fatalf("clear/archive/unmute not applied: %+v", group)
	}
	if spam.Folder != FolderSpam || request.Folder != FolderInbox {
		t.Fatalf("message request status must follow mautrix (1 = request, else not; spam untouched): %q %q", spam.Folder, request.Folder)
	}
	b.handleTable(&table.LSTable{LSDeleteThenInsertMessageRequest: []*table.LSDeleteThenInsertMessageRequest{{ThreadKey: 55, MessageRequestStatus: 0}, {ThreadKey: 66, MessageRequestStatus: 1}}}, modeLive)
	group, _ = b.store.ThreadView("55")
	request, _ = b.store.ThreadView("66")
	if group.Folder != FolderArchived || request.Folder != FolderRequests {
		t.Fatalf("status 0 must not move a thread that is not a request, status 1 must: %q %q", group.Folder, request.Folder)
	}
	if len(eventsOf(t, buf, EventThreads)) == 0 {
		t.Fatal("thread changes must be upserted")
	}
}

func TestLSMessageV11Conversion(t *testing.T) {
	b, buf := groupBackend(t)
	now := time.Now().UnixMilli()
	b.handleTable(&table.LSTable{
		LSInsertMessage: []*table.LSInsertMessage{
			{ThreadKey: 55, MessageId: "mid.$sys", TimestampMs: now - 5000, SenderId: 7, IsAdminMessage: true, Text: "Camille Martin named the group Coloc."},
			{ThreadKey: 55, MessageId: "mid.$fwd", TimestampMs: now, SenderId: 7, IsForwarded: true, Text: "hello", EphemeralDurationInSec: 86400},
			{ThreadKey: 55, MessageId: "mid.$voice", TimestampMs: now - 3000, SenderId: 8},
			{ThreadKey: 55, MessageId: "mid.$xma", TimestampMs: now - 2000, SenderId: 8},
			{ThreadKey: 55, MessageId: "mid.$call", TimestampMs: now - 1000, SenderId: 8},
		},
		LSInsertBlobAttachment: []*table.LSInsertBlobAttachment{
			{MessageId: "mid.$voice", AttachmentFbid: "v1", AttachmentType: table.AttachmentTypeAudio, Filename: "audioclip-1.mp4", PlayableUrl: "https://scontent.xx.fbcdn.net/a.mp4", PlayableDurationMs: 4000, WaveformData: `{"amplitudes":[0.1,0.5,0.25],"sampling_frequency":10}`},
		},
		LSInsertXmaAttachment: []*table.LSInsertXmaAttachment{
			{MessageId: "mid.$xma", AttachmentFbid: "loc", AttachmentType: table.AttachmentTypeXMA, TitleText: "Gare", SubtitleText: "Place de la Gare"},
			{MessageId: "mid.$xma", AttachmentFbid: "story", AttachmentType: table.AttachmentTypeXMA, TitleText: "Replied to your story", PreviewUrl: "https://scontent.xx.fbcdn.net/s.jpg"},
			{MessageId: "mid.$xma", AttachmentFbid: "contact", AttachmentType: table.AttachmentTypeXMA, TitleText: "Léa", SubtitleText: "+33 6"},
			{MessageId: "mid.$call", AttachmentFbid: "call", AttachmentType: table.AttachmentTypeXMA, TitleText: "Video call", SubtitleText: "12 mins"},
		},
		LSInsertAttachmentCta: []*table.LSInsertAttachmentCta{
			{AttachmentFbid: "loc", MessageId: "mid.$xma", Type_: "xma_live_location_sharing", NativeUrl: "45.5,6.25"},
			{AttachmentFbid: "story", MessageId: "mid.$xma", Type_: "xma_story_reply", ActionUrl: "https://www.facebook.com/stories/1"},
			{AttachmentFbid: "contact", MessageId: "mid.$xma", Type_: "xma_contact_share"},
			{AttachmentFbid: "call", MessageId: "mid.$call", Type_: "xma_rtc_video"},
		},
	}, modeLive)
	sys, _ := b.store.FindMessage("mid.$sys")
	if !sys.System || sys.Text != "Camille Martin a renommé le groupe « Coloc »" {
		t.Fatalf("admin message not translated: %+v", sys)
	}
	fwd, _ := b.store.FindMessage("mid.$fwd")
	if !fwd.Forwarded || fwd.System {
		t.Fatalf("forwarded flag missing: %+v", fwd)
	}
	if th, _ := b.store.ThreadView("55"); th.EphemeralTTLMS != 86_400_000 {
		t.Fatalf("ephemeral ttl not derived: %d", th.EphemeralTTLMS)
	}
	voice, _ := b.store.FindMessage("mid.$voice")
	if a := voice.Attachments[0]; a.Kind != "audio" || !a.Voice || !slices.Equal(a.Waveform, []float64{0.1, 0.5, 0.25}) || a.DurationMS != 4000 {
		t.Fatalf("voice clip not mapped: %+v", a)
	}
	xma, _ := b.store.FindMessage("mid.$xma")
	if len(xma.Attachments) != 3 {
		t.Fatalf("unexpected xma attachments %+v", xma.Attachments)
	}
	loc, story, contact := xma.Attachments[0], xma.Attachments[1], xma.Attachments[2]
	if loc.Kind != "location" || loc.Latitude == nil || *loc.Latitude != 45.5 || *loc.Longitude != 6.25 || !strings.Contains(loc.URL, "45.5") || loc.Title != "Gare" {
		t.Fatalf("bad location %+v", loc)
	}
	if story.Kind != "story" || story.URL != "https://www.facebook.com/stories/1" || story.ThumbnailURL == "" {
		t.Fatalf("bad story %+v", story)
	}
	if contact.Kind != "contact" || contact.Title != "Léa" || contact.Description != "+33 6" {
		t.Fatalf("bad contact %+v", contact)
	}
	call, _ := b.store.FindMessage("mid.$call")
	if !call.System || call.Text != "Appel vidéo · 12 min" || len(call.Attachments) != 0 {
		t.Fatalf("bad call line %+v", call)
	}
	for _, raw := range eventsOf(t, buf, EventMessage) {
		if strings.Contains(string(raw), `"mid.$voice"`) && !strings.Contains(string(raw), `"voice":true`) {
			t.Fatalf("voice flag missing on the wire: %s", raw)
		}
		if strings.Contains(string(raw), `"mid.$fwd"`) && !strings.Contains(string(raw), `"waveform":[]`) && strings.Contains(string(raw), `"attachments":[{`) {
			t.Fatalf("waveform must default to []: %s", raw)
		}
	}
}

func TestLSPolls(t *testing.T) {
	b, buf := groupBackend(t)
	now := time.Now().UnixMilli()
	b.handleTable(&table.LSTable{
		LSInsertMessage: []*table.LSInsertMessage{{ThreadKey: 55, MessageId: "mid.$poll", TimestampMs: now - 5000, SenderId: 7, IsAdminMessage: true, Text: "Camille created a poll: Lunch?"}},
		LSAddPollOptionV2: []*table.LSAddPollOptionV2{
			{OptionID: 11, PollID: 900, OptionText: "Pizza", ThreadKey: 55, MessageID: "mid.$poll", MessageEventType: table.PollEventCreate},
			{OptionID: 12, PollID: 900, OptionText: "Sushi", ThreadKey: 55, MessageID: "mid.$poll", MessageEventType: table.PollEventCreate},
		},
	}, modeLive)
	msg, _ := decorated(b.store, "55", "mid.$poll")
	if msg.Poll == nil || msg.System || msg.Text != "" || len(msg.Poll.Options) != 2 || msg.Poll.ID != "900" || msg.Poll.Options[0].Text != "Pizza" {
		t.Fatalf("poll not attached: %+v %+v", msg, msg.Poll)
	}
	b.handleTable(&table.LSTable{
		LSInsertMessage: []*table.LSInsertMessage{
			{ThreadKey: 55, MessageId: "mid.$v1", TimestampMs: now - 4000, SenderId: 8},
			{ThreadKey: 55, MessageId: "mid.$v2", TimestampMs: now - 3000, SenderId: 999, IsAdminMessage: true, Text: "You voted for \"Sushi\" in the poll."},
		},
		LSAddPollVoteV2: []*table.LSAddPollVote{
			{OptionID: 11, PollID: 900, ContactID: 8, TimestampMS: now - 4000, ThreadKey: 55, MessageID: "mid.$v1", MessageEventType: table.PollEventVote},
			{OptionID: 12, PollID: 900, ContactID: 999, TimestampMS: now - 3000, ThreadKey: 55, MessageID: "mid.$v2", MessageEventType: table.PollEventVote},
		},
	}, modeLive)
	msg, _ = decorated(b.store, "55", "mid.$poll")
	if msg.Poll.TotalVotes != 2 || msg.Poll.Options[0].VoteCount != 1 || msg.Poll.Options[0].Voters[0].UserID != "8" || !msg.Poll.Options[1].Mine || msg.Poll.Options[0].Mine {
		t.Fatalf("votes not applied: %+v", msg.Poll)
	}
	if _, ok := b.store.FindMessage("mid.$v1"); ok {
		t.Fatal("a vote without text must not become a message")
	}
	if line, ok := b.store.FindMessage("mid.$v2"); !ok || !line.System || line.Text != "Vous avez voté pour « Sushi »" {
		t.Fatalf("a vote with text becomes a system line: %+v", line)
	}
	updates := 0
	for _, raw := range eventsOf(t, buf, EventMessageUpdate) {
		if strings.Contains(string(raw), `"mid.$poll"`) {
			updates++
		}
	}
	if updates < 2 {
		t.Fatalf("each vote must emit message_updated on the poll, got %d", updates)
	}
	b.handleTable(&table.LSTable{
		LSHandlePlaceholderPollData: []*table.LSHandlePlaceholderPollData{{PollID: 900}},
		LSAddPollOption:             []*table.LSAddPollOption{{OptionID: 11, PollID: 900, OptionText: "Pizza"}, {OptionID: 12, PollID: 900, OptionText: "Sushi"}, {OptionID: 13, PollID: 900, OptionText: "Tacos"}},
		LSAddPollVote:               []*table.LSAddPollVote{{OptionID: 13, PollID: 900, ContactID: 8, TimestampMS: now}},
	}, modeLive)
	msg, _ = decorated(b.store, "55", "mid.$poll")
	if len(msg.Poll.Options) != 3 || msg.Poll.Options[2].VoteCount != 1 || msg.Poll.Options[0].VoteCount != 0 {
		t.Fatalf("snapshot not applied: %+v", msg.Poll)
	}
	path := filepath.Join(t.TempDir(), "c.json")
	b.store.Save(path)
	reloaded := NewStore()
	reloaded.Load(path)
	if again, _ := decorated(reloaded, "55", "mid.$poll"); again.Poll == nil || len(again.Poll.Options) != 3 {
		t.Fatal("polls must survive a reload")
	}
}

func TestE2EEV11Conversion(t *testing.T) {
	b, buf := groupBackend(t)
	chat := waTypes.NewJID(testPeerID, waTypes.MessengerServer)
	sender := chat
	b.ensureE2EEThread(chat)
	b.store.PutMessage(Message{ID: "MINE", ThreadID: testPeerID, SenderID: testSelf, IsOutgoing: true, TimestampMS: 5000})
	b.handleWAReceipt(&events.Receipt{MessageSource: waTypes.MessageSource{Chat: chat, Sender: sender}, MessageIDs: []string{"MINE"}, Timestamp: time.UnixMilli(4000), Type: waTypes.ReceiptTypeDelivered})
	view, _ := b.store.ThreadView(testPeerID)
	if len(view.ReadReceipts) != 1 || view.ReadReceipts[0].DeliveredMS != 5000 || view.ReadReceipts[0].ReadMS != 0 {
		t.Fatalf("E2EE delivery receipt not stored: %+v", view.ReadReceipts)
	}
	if len(eventsOf(t, buf, EventRead)) != 0 {
		t.Fatal("a delivery receipt is not a read event")
	}
	audio := &waConsumerApplication.ConsumerApplication_AudioMessage{}
	if err := audio.Set(&waMediaTransport.AudioTransport{
		Integral:  &waMediaTransport.AudioTransport_Integral{Transport: &waMediaTransport.WAMediaTransport{Integral: &waMediaTransport.WAMediaTransport_Integral{DirectPath: proto.String("/v/x")}}},
		Ancillary: &waMediaTransport.AudioTransport_Ancillary{Seconds: proto.Uint32(3), Waveform: []byte{0, 50, 100}},
	}); err != nil {
		t.Fatal(err)
	}
	lat, long := 48.85, 2.35
	contents := map[string]*waConsumerApplication.ConsumerApplication_Content{
		"A1": {Content: &waConsumerApplication.ConsumerApplication_Content_AudioMessage{AudioMessage: audio}},
		"L1": {Content: &waConsumerApplication.ConsumerApplication_Content_LocationMessage{LocationMessage: &waConsumerApplication.ConsumerApplication_LocationMessage{
			Location: &waConsumerApplication.ConsumerApplication_Location{DegreesLatitude: &lat, DegreesLongitude: &long, Name: proto.String("Paris")},
			Address:  proto.String("Rue de Rivoli"),
		}}},
	}
	for id, content := range contents {
		b.handleFBMessage(&events.FBMessage{
			Info:    waTypes.MessageInfo{MessageSource: waTypes.MessageSource{Chat: chat, Sender: sender}, ID: id, Timestamp: time.Now()},
			Message: wrapConsumerContent(content),
		})
	}
	a, _ := b.store.FindMessage("A1")
	if att := a.Attachments[0]; !att.Voice || att.DurationMS != 3000 || !slices.Equal(att.Waveform, []float64{0, 0.5, 1}) {
		t.Fatalf("E2EE voice not mapped: %+v", att)
	}
	l, _ := b.store.FindMessage("L1")
	if att := l.Attachments[0]; att.Kind != "location" || *att.Latitude != lat || att.Title != "Paris" || att.Description != "Rue de Rivoli" || l.Text != "" {
		t.Fatalf("E2EE location not mapped: %+v %q", att, l.Text)
	}
}

func TestMetaV11Commands(t *testing.T) {
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
	if m, _ := b.store.FindMessage("mid.$mine"); !m.Deleted || m.Text != "" {
		t.Fatalf("unsend not applied: %+v", m)
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

func TestMetaV11E2EECommands(t *testing.T) {
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

func TestDispatcherV11Validation(t *testing.T) {
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

func TestDemoV11Data(t *testing.T) {
	h := newDemoHarness(t)
	threads := h.demo.store.ListThreads(0)
	seen := map[string]bool{}
	for _, th := range threads {
		switch {
		case th.Folder == FolderRequests:
			seen["request"] = true
		case th.Folder == FolderArchived:
			seen["archived"] = true
		}
		if th.MutedUntilMS != nil && *th.MutedUntilMS > 0 {
			seen["muted_until"] = true
		}
		if th.Theme != nil && len(th.Theme.Gradient) > 1 && th.QuickEmoji != "" {
			seen["theme"] = true
		}
		if th.EphemeralTTLMS > 0 {
			seen["ephemeral"] = true
		}
		if len(th.PinnedMessageIDs) > 0 {
			seen["pinned"] = true
		}
		if th.IsAdmin && th.CanManage {
			seen["admin"] = true
		}
		if th.Blocked {
			seen["blocked"] = true
		}
		for _, r := range th.ReadReceipts {
			if r.DeliveredMS > 0 {
				seen["delivered"] = true
			}
		}
		for _, p := range th.Participants {
			if p.IsAdmin {
				seen["participant_admin"] = true
			}
		}
		for _, m := range h.demo.store.Messages(th.ID, 0, 0) {
			if m.System {
				seen["system"] = true
			}
			if m.Forwarded {
				seen["forwarded"] = true
			}
			if m.Poll != nil && m.Poll.TotalVotes > 0 {
				seen["poll"] = true
			}
			for _, a := range m.Attachments {
				seen[a.Kind] = true
				if a.Voice && len(a.Waveform) > 0 {
					res, err := h.demo.Media(context.Background(), th.ID, m.ID, a.ID, VariantFull)
					if info, statErr := os.Stat(res.Path); err != nil || statErr != nil || info.Size() < 1000 {
						t.Fatalf("demo voice must be playable: %+v %v", res, err)
					}
					seen["voice"] = true
				}
			}
		}
	}
	for _, want := range []string{"request", "archived", "muted_until", "theme", "ephemeral", "pinned", "admin", "blocked", "delivered", "participant_admin", "system", "forwarded", "poll", "location", "story", "contact", "voice"} {
		if !seen[want] {
			t.Errorf("demo data lacks %s", want)
		}
	}
}

func TestDemoV11Commands(t *testing.T) {
	h := newDemoHarness(t)
	h.send(`{"id":1,"cmd":"connect"}`)
	h.waitEvent(EventReady, 3*time.Second)
	coloc, peer := demoGroupID(1), demoPeople[0].ID
	h.send(fmt.Sprintf(`{"id":2,"cmd":"send","params":{"thread_id":%q,"text":"brouillon"}}`, coloc))
	var sent SendResult
	h.okResult(2, &sent)
	dir := t.TempDir()
	wav := filepath.Join(dir, "v.wav")
	os.WriteFile(wav, demoToneWAV(1, 16000), 0o600)
	img := filepath.Join(dir, "i.png")
	png, _ := demoPNG("x", 32, 32)
	os.WriteFile(img, png, 0o600)
	steps := []string{
		fmt.Sprintf(`{"id":3,"cmd":"edit_message","params":{"thread_id":%q,"message_id":%q,"text":"corrigé"}}`, coloc, sent.MessageID),
		fmt.Sprintf(`{"id":4,"cmd":"forward","params":{"from_thread_id":%q,"message_id":%q,"to_thread_ids":[%q]}}`, coloc, sent.MessageID, peer),
		fmt.Sprintf(`{"id":5,"cmd":"send_voice","params":{"thread_id":%q,"path":%q,"duration_ms":1000,"client_id":"v1"}}`, peer, wav),
		fmt.Sprintf(`{"id":6,"cmd":"create_poll","params":{"thread_id":%q,"question":"Ciné ?","options":["Oui","Non"]}}`, coloc),
		fmt.Sprintf(`{"id":7,"cmd":"mute","params":{"thread_id":%q,"until_ms":-1}}`, coloc),
		fmt.Sprintf(`{"id":8,"cmd":"rename_thread","params":{"thread_id":%q,"name":"Coloc 2"}}`, coloc),
		fmt.Sprintf(`{"id":9,"cmd":"set_thread_image","params":{"thread_id":%q,"path":%q}}`, coloc, img),
		fmt.Sprintf(`{"id":10,"cmd":"add_participants","params":{"thread_id":%q,"user_ids":[%q]}}`, coloc, demoPeople[9].ID),
		fmt.Sprintf(`{"id":11,"cmd":"set_admin","params":{"thread_id":%q,"user_id":%q,"admin":true}}`, coloc, demoPeople[9].ID),
		fmt.Sprintf(`{"id":12,"cmd":"remove_participant","params":{"thread_id":%q,"user_id":%q}}`, coloc, demoPeople[9].ID),
		fmt.Sprintf(`{"id":13,"cmd":"block_user","params":{"user_id":%q,"blocked":true}}`, peer),
		`{"id":14,"cmd":"search_users","params":{"query":"zo"}}`,
		fmt.Sprintf(`{"id":15,"cmd":"open_thread","params":{"user_id":%q}}`, demoStrangers[1].ID),
		fmt.Sprintf(`{"id":16,"cmd":"create_group","params":{"user_ids":[%q,%q],"name":"Projet"}}`, demoPeople[2].ID, demoPeople[3].ID),
		fmt.Sprintf(`{"id":17,"cmd":"accept_request","params":{"thread_id":%q}}`, demoStrangers[0].ID),
		fmt.Sprintf(`{"id":18,"cmd":"unsend","params":{"thread_id":%q,"message_id":%q}}`, coloc, sent.MessageID),
		fmt.Sprintf(`{"id":19,"cmd":"delete_for_me","params":{"thread_id":%q,"message_id":%q}}`, coloc, sent.MessageID),
		fmt.Sprintf(`{"id":20,"cmd":"leave_thread","params":{"thread_id":%q}}`, coloc),
		fmt.Sprintf(`{"id":21,"cmd":"delete_thread","params":{"thread_id":%q}}`, demoStrangers[1].ID),
		fmt.Sprintf(`{"id":22,"cmd":"create_poll","params":{"thread_id":%q,"question":"Q","options":["a","b"]}}`, peer),
	}
	for _, step := range steps {
		h.send(step)
	}
	for id := int64(3); id <= 21; id++ {
		h.okResult(id, nil)
	}
	if h.errCode(22) != CodeUnsupported {
		t.Fatal("polls in 1:1 must be unsupported")
	}
	var fwd ForwardResult
	h.okResult(4, &fwd)
	var users []UserResult
	h.okResult(14, &users)
	var opened, created Thread
	h.okResult(15, &opened)
	h.okResult(16, &created)
	if len(fwd.MessageIDs) != 1 || fwd.MessageIDs[0] == "" || len(users) == 0 || users[0].Name != "Zoé Fontaine" || opened.ID != demoStrangers[1].ID || !created.IsGroup || created.Name != "Projet" || !created.IsAdmin {
		t.Fatalf("unexpected results %v %+v %+v %+v", fwd, users, opened, created)
	}
	var voiceEcho, forwardEcho, pollMsg bool
	for _, raw := range h.events(EventMessage) {
		var m Message
		json.Unmarshal(raw, &m)
		voiceEcho = voiceEcho || m.ClientID == "v1" && len(m.Attachments) == 1 && m.Attachments[0].Voice
		forwardEcho = forwardEcho || m.ID == fwd.MessageIDs[0] && m.Forwarded
		pollMsg = pollMsg || m.Poll != nil && m.Poll.Question == "Ciné ?"
	}
	if !voiceEcho || !forwardEcho || !pollMsg {
		t.Fatalf("missing echoes voice=%v forward=%v poll=%v", voiceEcho, forwardEcho, pollMsg)
	}
	removed := h.events(EventMessageRemoved)
	if len(removed) != 1 || !strings.Contains(string(removed[0]), sent.MessageID) {
		t.Fatalf("delete_for_me must emit message_removed: %s", removed)
	}
	if len(h.events(EventThreadRemoved)) != 1 {
		t.Fatal("delete_thread must emit thread_removed")
	}
	view, _ := h.demo.store.ThreadView(coloc)
	if view.Name != "Coloc 2" || view.MutedUntilMS == nil || *view.MutedUntilMS != -1 || view.CanManage {
		t.Fatalf("group changes not applied: %+v", view)
	}
	if v, _ := h.demo.store.ThreadView(peer); !v.Blocked {
		t.Fatal("block not applied")
	}
	if v, _ := h.demo.store.ThreadView(demoStrangers[0].ID); v.Folder != FolderInbox {
		t.Fatal("accept_request not applied")
	}
	h.send(fmt.Sprintf(`{"id":30,"cmd":"rename_thread","params":{"thread_id":%q,"name":"x"}}`, coloc))
	if h.errCode(30) != CodeUnsupported {
		t.Fatal("after leaving, group management must be unsupported")
	}
	var updated bool
	for _, raw := range h.events(EventMessageUpdate) {
		updated = updated || strings.Contains(string(raw), `"edited":true`) && strings.Contains(string(raw), "corrigé")
	}
	if !updated {
		t.Fatal("edit_message must emit message_updated")
	}
}
