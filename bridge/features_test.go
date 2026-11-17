package main

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"go.mau.fi/whatsmeow/proto/waArmadilloApplication"
	"go.mau.fi/whatsmeow/proto/waArmadilloXMA"
	"go.mau.fi/whatsmeow/proto/waCommon"
	"go.mau.fi/whatsmeow/proto/waConsumerApplication"
	waTypes "go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
	"google.golang.org/protobuf/proto"

	"go.mau.fi/mautrix-meta/pkg/messagix/table"
)

func TestLSXMALinkMapping(t *testing.T) {
	b, _, _ := newTestMetaBackend(t)
	b.store.SetSelfID(testSelf)
	ts := time.Now().UnixMilli()
	b.handleTable(&table.LSTable{
		LSInsertMessage: []*table.LSInsertMessage{{ThreadKey: 5, MessageId: "mid.$x", TimestampMs: ts, SenderId: 7}},
		LSInsertXmaAttachment: []*table.LSInsertXmaAttachment{
			{MessageId: "mid.$x", AttachmentFbid: "1", AttachmentType: table.AttachmentTypeXMA, TitleText: "Video title", SubtitleText: "Channel", PreviewUrl: "https://scontent.xx.fbcdn.net/p.jpg"},
			{MessageId: "mid.$x", AttachmentFbid: "2", AttachmentType: table.AttachmentTypeXMA, ActionUrl: "https://www.instagram.com/reel/abc/", PreviewUrl: "https://scontent.cdninstagram.com/r.jpg"},
			{MessageId: "mid.$x", AttachmentFbid: "3", AttachmentType: table.AttachmentTypeXMA, PreviewUrl: "https://scontent.xx.fbcdn.net/post.jpg"},
			{MessageId: "mid.$x", AttachmentFbid: "4", AttachmentType: table.AttachmentTypeXMA, ActionUrl: "https://scontent.xx.fbcdn.net/only-image.jpg", PreviewUrl: "https://scontent.xx.fbcdn.net/only-image.jpg"},
			{MessageId: "mid.$x", AttachmentFbid: "5", AttachmentType: table.AttachmentTypeXMA, TitleText: ""},
		},
		LSInsertAttachmentCta: []*table.LSInsertAttachmentCta{
			{AttachmentFbid: "1", MessageId: "mid.$x", Type_: "xma_web_url", ActionUrl: "https://l.facebook.com/l.php?u=https%3A%2F%2Fwww.youtube.com%2Fwatch%3Fv%3Dabc&h=x"},
			{AttachmentFbid: "5", MessageId: "mid.$x", Type_: "xma_rtc_missed_audio"},
		},
	}, modeLive)
	msg, ok := b.store.FindMessage("mid.$x")
	if !ok || len(msg.Attachments) != 4 {
		t.Fatalf("unexpected message %+v", msg)
	}
	yt, ig, post, img := msg.Attachments[0], msg.Attachments[1], msg.Attachments[2], msg.Attachments[3]
	if yt.Kind != "link" || yt.URL != "https://www.youtube.com/watch?v=abc" || yt.Source != "youtube.com" || yt.Title != "Video title" || yt.Description != "Channel" || yt.ThumbnailURL != "https://scontent.xx.fbcdn.net/p.jpg" {
		t.Fatalf("bad youtube link %+v", yt)
	}
	if ig.Kind != "link" || ig.Source != "Instagram" || ig.URL != "https://www.instagram.com/reel/abc/" || ig.Title == "" {
		t.Fatalf("bad instagram link %+v", ig)
	}
	if post.Kind != "other" || post.URL != "" || post.ThumbnailURL == "" || post.Title != "Publication partagée" {
		t.Fatalf("bad unknown share %+v", post)
	}
	if img.Kind == "link" || img.URL == "https://scontent.xx.fbcdn.net/only-image.jpg" && img.Kind == "other" {
		t.Fatalf("preview image must never be a link target %+v", img)
	}
	if !msg.System || msg.Text != "Appel manqué" {
		t.Fatalf("a call log must become a system line: %+v", msg)
	}
	if rec, ok := b.store.Media("mid.$x", "1"); !ok || rec.URL != "https://scontent.xx.fbcdn.net/p.jpg" {
		t.Fatalf("link preview not downloadable through media: %+v", rec)
	}
}

func armadilloEvent(id string, content *waArmadilloXMA.ExtendedContentMessage) *events.FBMessage {
	chat := waTypes.NewJID(testPeerID, waTypes.MessengerServer)
	return &events.FBMessage{
		Info: waTypes.MessageInfo{MessageSource: waTypes.MessageSource{Chat: chat, Sender: chat}, ID: id, Timestamp: time.Now()},
		Message: &waArmadilloApplication.Armadillo{Payload: &waArmadilloApplication.Armadillo_Payload{
			Payload: &waArmadilloApplication.Armadillo_Payload_Content{Content: &waArmadilloApplication.Armadillo_Content{
				Content: &waArmadilloApplication.Armadillo_Content_ExtendedContentMessage{ExtendedContentMessage: content},
			}},
		}},
	}
}

func consumerEvent(id string, content *waConsumerApplication.ConsumerApplication_Content) *events.FBMessage {
	chat := waTypes.NewJID(testPeerID, waTypes.MessengerServer)
	return &events.FBMessage{
		Info: waTypes.MessageInfo{MessageSource: waTypes.MessageSource{Chat: chat, Sender: chat}, ID: id, Timestamp: time.Now()},
		Message: &waConsumerApplication.ConsumerApplication{Payload: &waConsumerApplication.ConsumerApplication_Payload{
			Payload: &waConsumerApplication.ConsumerApplication_Payload_Content{Content: content},
		}},
	}
}

func TestE2EELinkMapping(t *testing.T) {
	b, _, _ := newTestMetaBackend(t)
	b.store.SetSelfID(testSelf)
	b.handleFBMessage(armadilloEvent("A1", &waArmadilloXMA.ExtendedContentMessage{
		TargetType:   waArmadilloXMA.ExtendedContentMessage_IG_CLIPS_SHARE.Enum(),
		TitleText:    proto.String("someone"),
		SubtitleText: proto.String("reel caption"),
		MessageText:  proto.String("regarde ça"),
		Ctas: []*waArmadilloXMA.ExtendedContentMessage_CTA{{
			NativeURL: proto.String("instagram://reel/1"),
			ActionURL: proto.String("https://www.instagram.com/reel/abc/"),
		}},
	}))
	msg, _ := b.store.MessageInThread(testPeerID, "A1")
	if msg.Text != "regarde ça" || len(msg.Attachments) != 1 {
		t.Fatalf("unexpected %+v", msg)
	}
	att := msg.Attachments[0]
	if att.Kind != "link" || att.URL != "https://www.instagram.com/reel/abc/" || att.Source != "Instagram" || att.Title != "someone" || att.Description != "reel caption" || att.ID == "" {
		t.Fatalf("bad e2ee link %+v", att)
	}

	b.handleFBMessage(armadilloEvent("A2", &waArmadilloXMA.ExtendedContentMessage{
		TargetType: waArmadilloXMA.ExtendedContentMessage_FB_FEED_SHARE.Enum(),
	}))
	msg, _ = b.store.MessageInThread(testPeerID, "A2")
	if len(msg.Attachments) != 1 || msg.Attachments[0].Kind != "other" || msg.Attachments[0].Title != "Publication Facebook" || msg.Attachments[0].Source != "Facebook" {
		t.Fatalf("unknown share must carry a label: %+v", msg.Attachments)
	}

	b.handleFBMessage(consumerEvent("T1", &waConsumerApplication.ConsumerApplication_Content{
		Content: &waConsumerApplication.ConsumerApplication_Content_ExtendedTextMessage{ExtendedTextMessage: &waConsumerApplication.ConsumerApplication_ExtendedTextMessage{
			Text:         &waCommon.MessageText{Text: proto.String("vu https://example.org/a")},
			MatchedText:  proto.String("https://example.org/a"),
			CanonicalURL: proto.String("https://www.example.org/a"),
			Title:        proto.String("Example page"),
			Description:  proto.String("About"),
		}},
	}))
	msg, _ = b.store.MessageInThread(testPeerID, "T1")
	if msg.Text != "vu https://example.org/a" || len(msg.Attachments) != 1 || msg.Attachments[0].Kind != "link" || msg.Attachments[0].URL != "https://www.example.org/a" || msg.Attachments[0].Source != "example.org" {
		t.Fatalf("bad extended text link %+v", msg)
	}

	b.handleFBMessage(consumerEvent("T2", &waConsumerApplication.ConsumerApplication_Content{
		Content: &waConsumerApplication.ConsumerApplication_Content_ExtendedTextMessage{ExtendedTextMessage: &waConsumerApplication.ConsumerApplication_ExtendedTextMessage{
			Text:        &waCommon.MessageText{Text: proto.String("https://example.org/b")},
			MatchedText: proto.String("https://example.org/b"),
		}},
	}))
	msg, _ = b.store.MessageInThread(testPeerID, "T2")
	if msg.Text != "https://example.org/b" || len(msg.Attachments) != 0 {
		t.Fatalf("bare url must stay text: %+v", msg)
	}
}

func TestNicknames(t *testing.T) {
	b, buf, _ := newTestMetaBackend(t)
	b.store.SetSelfID(testSelf)
	ts := time.Now().UnixMilli()
	b.handleTable(&table.LSTable{
		LSDeleteThenInsertThread: []*table.LSDeleteThenInsertThread{
			{ThreadKey: 42, ThreadType: table.ONE_TO_ONE, LastActivityTimestampMs: ts},
			{ThreadKey: 77, ThreadType: table.GROUP_THREAD, ThreadName: "Team", LastActivityTimestampMs: ts},
		},
		LSVerifyContactRowExists: []*table.LSVerifyContactRowExists{{ContactId: 42, Name: "Real Name"}, {ContactId: 8, Name: "Member Eight"}},
		LSAddParticipantIdToGroupThread: []*table.LSAddParticipantIdToGroupThread{
			{ThreadKey: 42, ContactId: 42, Nickname: "Nick"},
			{ThreadKey: 77, ContactId: 8, Nickname: "Boss"},
		},
		LSInsertMessage: []*table.LSInsertMessage{
			{ThreadKey: 77, MessageId: "g1", TimestampMs: ts, SenderId: 8, Text: "x"},
			{ThreadKey: 42, MessageId: "p1", TimestampMs: ts, SenderId: 42, Text: "y"},
		},
	}, modeLive)
	if th, _ := b.store.ThreadView("42"); th.Name != "Nick" || th.PeerID != "42" {
		t.Fatalf("1:1 must use nickname: %+v", th)
	}
	if msgs := b.store.Messages("77", 0, 10); len(msgs) != 1 || msgs[0].SenderName != "Boss" {
		t.Fatalf("group sender must use nickname: %+v", msgs)
	}
	if !strings.Contains(strings.Join(buf.Lines(), "\n"), `"sender_name":"Boss"`) {
		t.Fatal("live message event must carry the nickname")
	}
	path := filepath.Join(t.TempDir(), "c.json")
	if err := b.store.Save(path); err != nil {
		t.Fatal(err)
	}
	reloaded := NewStore()
	if err := reloaded.Load(path); err != nil {
		t.Fatal(err)
	}
	if th, _ := reloaded.ThreadView("42"); th.Name != "Nick" {
		t.Fatal("nickname not persisted")
	}
	b.handleTable(&table.LSTable{
		LSAddParticipantIdToGroupThread: []*table.LSAddParticipantIdToGroupThread{{ThreadKey: 42, ContactId: 42}},
	}, modeLive)
	if th, _ := b.store.ThreadView("42"); th.Name != "Real Name" {
		t.Fatalf("cleared nickname must fall back to contact name: %+v", th)
	}
}

func TestReactions(t *testing.T) {
	b, buf, _ := newTestMetaBackend(t)
	b.store.SetSelfID(testSelf)
	ts := time.Now().UnixMilli()
	b.handleTable(&table.LSTable{
		LSInsertMessage: []*table.LSInsertMessage{{ThreadKey: 5, MessageId: "m1", TimestampMs: ts, SenderId: 7, Text: "x"}},
	}, modeLive)
	b.handleTable(&table.LSTable{
		LSUpsertReaction: []*table.LSUpsertReaction{
			{ThreadKey: 5, MessageId: "m1", ActorId: 7, Reaction: "❤️"},
			{ThreadKey: 5, MessageId: "m1", ActorId: 999, Reaction: "❤️"},
			{ThreadKey: 5, MessageId: "m1", ActorId: 8, Reaction: "😂"},
		},
	}, modeLive)
	msgs := b.store.Messages("5", 0, 10)
	if len(msgs) != 1 || len(msgs[0].Reactions) != 2 || !sameReaction(msgs[0].Reactions[0], "❤️", 2, true) || !sameReaction(msgs[0].Reactions[1], "😂", 1, false) {
		t.Fatalf("bad aggregation %+v", msgs)
	}
	updates := eventsOf(t, buf, EventMessageUpdate)
	if len(updates) != 1 || !strings.Contains(string(updates[0]), `"count":2`) {
		t.Fatalf("expected one message_updated with reactions, got %s", updates)
	}
	historyBefore := len(eventsOf(t, buf, EventMessageUpdate))
	b.handleTable(&table.LSTable{
		LSInsertNewMessageRange: []*table.LSInsertNewMessageRange{{ThreadKey: 5, MinTimestampMs: ts - 9000, MaxTimestampMs: ts - 9000}},
		LSUpsertMessage:         []*table.LSUpsertMessage{{ThreadKey: 5, MessageId: "old", TimestampMs: ts - 9000, SenderId: 7, Text: "o"}},
		LSUpsertReaction:        []*table.LSUpsertReaction{{ThreadKey: 5, MessageId: "old", ActorId: 7, Reaction: "👍"}},
	}, modeLive)
	if n := len(eventsOf(t, buf, EventMessageUpdate)); n != historyBefore {
		t.Fatalf("reactions on history upserts must not emit message_updated, got %d new", n-historyBefore)
	}
	if msgs := b.store.Messages("5", ts-1, 10); len(msgs) != 1 || len(msgs[0].Reactions) != 1 {
		t.Fatalf("history reaction must still be stored: %+v", msgs)
	}
	b.handleTable(&table.LSTable{LSDeleteReaction: []*table.LSDeleteReaction{{ThreadKey: 5, MessageId: "m1", ActorId: 999}}}, modeLive)
	if msgs := b.store.Messages("5", 0, 10); msgs[0].Reactions[0].Mine || msgs[0].Reactions[0].Count != 1 {
		t.Fatalf("delete not applied %+v", msgs[0].Reactions)
	}

	chat := waTypes.NewJID(testPeerID, waTypes.MessengerServer)
	b.handleFBMessage(consumerEvent("E1", &waConsumerApplication.ConsumerApplication_Content{
		Content: &waConsumerApplication.ConsumerApplication_Content_MessageText{MessageText: &waCommon.MessageText{Text: proto.String("hi")}},
	}))
	react := func(text string) *events.FBMessage {
		evt := consumerEvent("R-"+text, &waConsumerApplication.ConsumerApplication_Content{
			Content: &waConsumerApplication.ConsumerApplication_Content_ReactionMessage{ReactionMessage: &waConsumerApplication.ConsumerApplication_ReactionMessage{
				Key:  &waCommon.MessageKey{ID: proto.String("E1"), RemoteJID: proto.String(chat.String())},
				Text: proto.String(text),
			}},
		})
		return evt
	}
	before := len(eventsOf(t, buf, EventMessageUpdate))
	b.handleFBMessage(react("👍"))
	if msg, _ := b.store.MessageInThread(testPeerID, "E1"); len(b.store.Decorate(msg).Reactions) != 1 {
		t.Fatal("e2ee reaction not stored")
	}
	if _, ok := b.store.MessageInThread(testPeerID, "R-👍"); ok {
		t.Fatal("reaction must not become a message")
	}
	b.handleFBMessage(react(""))
	if msg, _ := b.store.MessageInThread(testPeerID, "E1"); len(b.store.Decorate(msg).Reactions) != 0 {
		t.Fatal("e2ee reaction removal not applied")
	}
	if n := len(eventsOf(t, buf, EventMessageUpdate)) - before; n != 2 {
		t.Fatalf("expected 2 reaction updates, got %d", n)
	}
	var decoded Message
	json.Unmarshal(eventsOf(t, buf, EventMessageUpdate)[before], &decoded)
	if len(decoded.Reactions) != 1 || decoded.Reactions[0].Emoji != "👍" {
		t.Fatalf("bad reaction event %+v", decoded)
	}
}

func TestMemberAvatarsAndPresence(t *testing.T) {
	b, buf, _ := newTestMetaBackend(t)
	b.store.SetSelfID(testSelf)
	ts := time.Now().UnixMilli()
	b.handleTable(&table.LSTable{
		LSDeleteThenInsertThread: []*table.LSDeleteThenInsertThread{
			{ThreadKey: 77, ThreadType: table.GROUP_THREAD, LastActivityTimestampMs: ts},
			{ThreadKey: 78, ThreadType: table.GROUP_THREAD, ThreadPictureUrl: "https://cdn/g.jpg", LastActivityTimestampMs: ts},
		},
		LSVerifyContactRowExists: []*table.LSVerifyContactRowExists{
			{ContactId: 1, Name: "A", ProfilePictureUrl: "https://cdn/1.jpg"},
			{ContactId: 2, Name: "B", ProfilePictureUrl: "https://cdn/2.jpg"},
			{ContactId: 3, Name: "C", ProfilePictureUrl: "https://cdn/3.jpg"},
			{ContactId: 999, Name: "Me", ProfilePictureUrl: "https://cdn/me.jpg"},
		},
		LSAddParticipantIdToGroupThread: []*table.LSAddParticipantIdToGroupThread{{ThreadKey: 77, ContactId: 1}, {ThreadKey: 77, ContactId: 2}, {ThreadKey: 77, ContactId: 3}, {ThreadKey: 77, ContactId: 999}},
		LSInsertMessage: []*table.LSInsertMessage{
			{ThreadKey: 77, MessageId: "a", TimestampMs: ts - 3000, SenderId: 2},
			{ThreadKey: 77, MessageId: "b", TimestampMs: ts - 2000, SenderId: 999},
			{ThreadKey: 77, MessageId: "c", TimestampMs: ts - 1000, SenderId: 3},
		},
		LSDeleteThenInsertContactPresence: []*table.LSDeleteThenInsertContactPresence{{ContactId: 3, Status: 2, LastActiveTimestampMs: ts}},
	}, modeLive)
	th, _ := b.store.ThreadView("77")
	if strings.Join(th.MemberAvatars, ",") != "https://cdn/3.jpg,https://cdn/2.jpg" {
		t.Fatalf("unexpected member avatars %v", th.MemberAvatars)
	}
	if th, _ := b.store.ThreadView("78"); th.MemberAvatars == nil || len(th.MemberAvatars) != 0 {
		t.Fatalf("group with picture must have empty member avatars %v", th.MemberAvatars)
	}
	if !strings.Contains(strings.Join(buf.Lines(), "\n"), `"member_avatars":[]`) {
		t.Fatal("member_avatars must serialize as []")
	}
	presence := eventsOf(t, buf, EventPresence)
	if len(presence) != 1 || !strings.Contains(string(presence[0]), `"user_id":"3","online":true`) {
		t.Fatalf("unexpected presence %s", presence)
	}
}

func TestDemoFeatures(t *testing.T) {
	h := newDemoHarness(t)
	h.send(`{"id":1,"cmd":"connect"}`)
	h.waitEvent(EventReady, 3*time.Second)
	h.send(`{"id":2,"cmd":"threads"}`)
	var threads []Thread
	h.okResult(2, &threads)
	var nicknamed, peers int
	for _, th := range threads {
		if th.Name == "Cam 🌸" || th.Name == "Loulou" {
			nicknamed++
		}
		if th.PeerID != "" {
			peers++
		}
		if th.MemberAvatars == nil {
			t.Fatal("member_avatars must not be null")
		}
	}
	if nicknamed != 2 || peers == 0 {
		t.Fatalf("nicknamed=%d peers=%d", nicknamed, peers)
	}
	if n := len(h.events(EventPresence)); n != 2 {
		t.Fatalf("expected 2 presence events, got %d", n)
	}
	var link *Message
	var linkAtt Attachment
	reactions := 0
	for _, th := range threads {
		for _, m := range h.demo.store.Messages(th.ID, 0, 0) {
			if len(m.Reactions) > 0 {
				reactions++
			}
			for _, a := range m.Attachments {
				if a.Kind == "link" && link == nil {
					mm := m
					link, linkAtt = &mm, a
				}
			}
		}
	}
	if link == nil || reactions == 0 || linkAtt.URL == "" || linkAtt.Title == "" || linkAtt.Source == "" {
		t.Fatalf("demo lacks links or reactions: link=%v reactions=%d", link != nil, reactions)
	}
	h.send(`{"id":3,"cmd":"media","params":{"thread_id":"` + link.ThreadID + `","message_id":"` + link.ID + `","attachment_id":"` + linkAtt.ID + `"}}`)
	var res MediaResult
	h.okResult(3, &res)
	if res.MimeType != "image/png" {
		t.Fatalf("link preview must be a PNG: %+v", res)
	}
}

func sameReaction(r Reaction, emoji string, count int, mine bool) bool {
	return r.Emoji == emoji && r.Count == count && r.Mine == mine
}
