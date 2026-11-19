package main

import (
	"encoding/json"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"go.mau.fi/mautrix-meta/pkg/messagix/table"
	waTypes "go.mau.fi/whatsmeow/types"
)

func TestLSThreadSettingsFields(t *testing.T) {
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

func TestLSMessageConversion(t *testing.T) {
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

func TestLSFolderMapping(t *testing.T) {
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
