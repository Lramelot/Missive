package main

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"go.mau.fi/whatsmeow/proto/waCommon"
	"go.mau.fi/whatsmeow/proto/waConsumerApplication"
	waTypes "go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
	"google.golang.org/protobuf/proto"

	"go.mau.fi/mautrix-meta/pkg/messagix/socket"
	"go.mau.fi/mautrix-meta/pkg/messagix/table"
)

func TestMentionOffsetConversion(t *testing.T) {
	text := "Salut 👋 Chloé et Théo !"
	start, length, ok := byteSpanFromUTF16(text, 9, 5)
	if !ok || text[start:start+length] != "Chloé" {
		t.Fatalf("utf16 9+5 must map to Chloé, got %d+%d %v", start, length, ok)
	}
	start, length, ok = byteSpanFromUTF16(text, 18, 4)
	if !ok || text[start:start+length] != "Théo" {
		t.Fatalf("utf16 18+4 must map to Théo, got %q", text[start:start+length])
	}
	u, l, ok := utf16SpanFromBytes(text, strings.Index(text, "Théo"), len("Théo"))
	if !ok || u != 18 || l != 4 {
		t.Fatalf("bytes of Théo must map back to utf16 18+4, got %d+%d", u, l)
	}
	u, l, ok = utf16SpanFromBytes(text, strings.Index(text, "👋"), len("👋"))
	if !ok || u != 6 || l != 2 {
		t.Fatalf("an emoji is two utf16 units, got %d+%d", u, l)
	}
	if start, length, ok := byteSpanFromUTF16(text, 6, 2); !ok || text[start:start+length] != "👋" {
		t.Fatal("a full surrogate pair must map to the emoji bytes")
	}
	if _, _, ok := byteSpanFromUTF16(text, 7, 3); ok {
		t.Fatal("an offset inside a surrogate pair must be rejected")
	}
	if _, _, ok := byteSpanFromUTF16(text, 6, 1); ok {
		t.Fatal("an end inside a surrogate pair must be rejected")
	}
	if _, _, ok := byteSpanFromUTF16(text, 40, 2); ok {
		t.Fatal("an offset past the text must be rejected")
	}
	if start, length, ok := byteSpanFromUTF16(text, 18, 50); !ok || start+length != len(text) {
		t.Fatal("a length past the end must be clamped like mautrix does")
	}
	if _, _, ok := utf16SpanFromBytes(text, strings.Index(text, "é")+1, 2); ok {
		t.Fatal("a byte offset inside a rune must be rejected")
	}
}

func groupBackend(t *testing.T) (*MetaBackend, *syncBuffer) {
	t.Helper()
	b, buf, _ := loggedInBackend(t)
	b.store.SetContact("7", "Camille Martin", "Camille", "https://scontent.xx.fbcdn.net/c.jpg")
	b.store.SetContact("8", "Théo Durand", "Théo", "")
	b.store.UpdateThread("55", func(rec *threadRec) {
		rec.IsGroup = true
		rec.Name = "Team"
		rec.Members = map[string]bool{testSelf: true, "7": true, "8": true}
	})
	return b, buf
}

func TestLSMentionsAreParsed(t *testing.T) {
	b, buf := groupBackend(t)
	now := time.Now().UnixMilli()
	text := "👋 @Camille Martin et @tout le monde, Théo ?"
	camU16, camLen, _ := utf16SpanFromBytes(text, strings.Index(text, "@Camille Martin"), len("@Camille Martin"))
	allU16, allLen, _ := utf16SpanFromBytes(text, strings.Index(text, "@tout le monde"), len("@tout le monde"))
	theoU16, theoLen, _ := utf16SpanFromBytes(text, strings.Index(text, "Théo"), len("Théo"))
	b.handleTable(&table.LSTable{LSInsertMessage: []*table.LSInsertMessage{
		{
			ThreadKey: 55, MessageId: "mid.$m1", TimestampMs: now, SenderId: 8, Text: text,
			MentionIds:     "8,55,7",
			MentionOffsets: fmt.Sprintf("%d,%d,%d", theoU16, allU16, camU16),
			MentionLengths: fmt.Sprintf("%d,%d,%d", theoLen, allLen, camLen),
			MentionTypes:   "s,t,p",
		},
		{ThreadKey: 55, MessageId: "mid.$m2", TimestampMs: now + 1, SenderId: 7, Text: "salut", MentionIds: "8,7", MentionOffsets: "0", MentionLengths: "2", MentionTypes: "p"},
	}}, modeLive)
	msg, _ := b.store.MessageInThread("55", "mid.$m1")
	want := []Mention{
		{Offset: strings.Index(text, "@Camille Martin"), Length: len("@Camille Martin"), UserID: "7"},
		{Offset: strings.Index(text, "Théo"), Length: len("Théo"), UserID: "8"},
	}
	if !slices.Equal(msg.Mentions, want) {
		t.Fatalf("person and silent mentions must be kept in byte offsets, thread mentions skipped: %+v", msg.Mentions)
	}
	if bad, _ := b.store.MessageInThread("55", "mid.$m2"); len(bad.Mentions) != 0 {
		t.Fatal("malformed mention data must be ignored")
	}
	var seen bool
	for _, raw := range eventsOf(t, buf, EventMessage) {
		var m Message
		json.Unmarshal(raw, &m)
		if m.ID == "mid.$m1" {
			seen = slices.Equal(m.Mentions, want)
		}
		if m.ID == "mid.$m2" && !strings.Contains(string(raw), `"mentions":[]`) {
			t.Fatalf("messages without mentions must carry an empty list: %s", raw)
		}
	}
	if !seen {
		t.Fatal("live message event must carry the mentions")
	}

	path := filepath.Join(t.TempDir(), "cache.json")
	if err := b.store.Save(path); err != nil {
		t.Fatal(err)
	}
	reloaded := NewStore()
	if err := reloaded.Load(path); err != nil {
		t.Fatal(err)
	}
	if again, _ := reloaded.MessageInThread("55", "mid.$m1"); !slices.Equal(again.Mentions, want) {
		t.Fatal("mentions must survive a store reload")
	}

	b.handleTable(&table.LSTable{LSEditMessage: []*table.LSEditMessage{{MessageID: "mid.$m1", Text: "changed", EditCount: 1}}}, modeLive)
	if edited, _ := b.store.MessageInThread("55", "mid.$m1"); len(edited.Mentions) != 0 {
		t.Fatal("an LS edit must drop the now meaningless mention spans")
	}
}

func fbText(chat, sender waTypes.JID, id string, mt *waCommon.MessageText) *events.FBMessage {
	return &events.FBMessage{
		Info: waTypes.MessageInfo{MessageSource: waTypes.MessageSource{Chat: chat, Sender: sender}, ID: id, Timestamp: time.Now()},
		Message: &waConsumerApplication.ConsumerApplication{Payload: &waConsumerApplication.ConsumerApplication_Payload{
			Payload: &waConsumerApplication.ConsumerApplication_Payload_Content{Content: &waConsumerApplication.ConsumerApplication_Content{
				Content: &waConsumerApplication.ConsumerApplication_Content_MessageText{MessageText: mt},
			}},
		}},
	}
}

func TestE2EEMentionsAreParsed(t *testing.T) {
	b, buf := groupBackend(t)
	b.store.SetNickname("66", "8", "Le Théo 🎸")
	chat := waTypes.NewJID("66", waTypes.GroupServer)
	sender := waTypes.NewJID("7", waTypes.MessengerServer)
	theo := waTypes.NewJID("8", waTypes.MessengerServer).String()
	self := waTypes.NewJID(testSelf, waTypes.MessengerServer).String()
	b.handleFBMessage(fbText(chat, sender, "W1", &waCommon.MessageText{
		Text:         proto.String("Été 🌞 @" + theo + " et @" + testSelf + " !"),
		MentionedJID: []string{theo, self},
	}))
	msg, ok := b.store.MessageInThread("66", "W1")
	wantText := "Été 🌞 Le Théo 🎸 et Me !"
	want := []Mention{
		{Offset: strings.Index(wantText, "Le Théo"), Length: len("Le Théo 🎸"), UserID: "8"},
		{Offset: strings.Index(wantText, "Me !"), Length: len("Me"), UserID: testSelf},
	}
	if !ok || msg.Text != wantText || !slices.Equal(msg.Mentions, want) {
		t.Fatalf("placeholders must become names with byte spans: %q %+v", msg.Text, msg.Mentions)
	}
	if len(eventsOf(t, buf, EventMessage)) == 0 {
		t.Fatal("live E2EE message not emitted")
	}

	structured := "Hé 👋 Camille"
	b.handleFBMessage(fbText(chat, sender, "W2", &waCommon.MessageText{
		Text: proto.String(structured),
		Mentions: []*waCommon.Mention{{
			MentionedJID: proto.String(waTypes.NewJID("7", waTypes.MessengerServer).String()),
			Offset:       proto.Uint32(6),
			Length:       proto.Uint32(7),
		}},
	}))
	if msg, _ := b.store.MessageInThread("66", "W2"); msg.Text != structured || len(msg.Mentions) != 1 || structured[msg.Mentions[0].Offset:msg.Mentions[0].Offset+msg.Mentions[0].Length] != "Camille" {
		t.Fatalf("structured utf16 mentions must map to byte spans: %+v", msg.Mentions)
	}

	conv := b.convertFBMessage("66", &events.FBMessage{
		Info: waTypes.MessageInfo{MessageSource: waTypes.MessageSource{Chat: chat, Sender: sender}, ID: "W3", Timestamp: time.Now()},
		Message: &waConsumerApplication.ConsumerApplication{Payload: &waConsumerApplication.ConsumerApplication_Payload{
			Payload: &waConsumerApplication.ConsumerApplication_Payload_Content{Content: &waConsumerApplication.ConsumerApplication_Content{
				Content: &waConsumerApplication.ConsumerApplication_Content_EditMessage{EditMessage: &waConsumerApplication.ConsumerApplication_EditMessage{
					Key:     &waCommon.MessageKey{ID: proto.String("W1"), FromMe: proto.Bool(true)},
					Message: &waCommon.MessageText{Text: proto.String("ok @" + theo), MentionedJID: []string{theo}},
				}},
			}},
		}},
	})
	b.applyEditOrRevoke(conv)
	if msg, _ := b.store.MessageInThread("66", "W1"); msg.Text != "ok Le Théo 🎸" || len(msg.Mentions) != 1 || msg.Mentions[0].Offset != 3 {
		t.Fatalf("edits must carry their own mentions: %q %+v", msg.Text, msg.Mentions)
	}
}

func TestOutgoingMentionPayloads(t *testing.T) {
	text := "Salut 👋 Camille et Théo"
	mentions := []Mention{
		{Offset: strings.Index(text, "Camille"), Length: len("Camille"), UserID: "7"},
		{Offset: strings.Index(text, "Théo"), Length: len("Théo"), UserID: "8"},
	}
	data := lsMentionData(text, mentions)
	if data == nil || data.MentionIDs != "7,8" || data.MentionOffsets != "9,20" || data.MentionLengths != "7,4" || data.MentionTypes != "p,p" {
		t.Fatalf("unexpected LS mention data %+v", data)
	}
	parsed, err := data.Parse()
	if err != nil || len(parsed) != 2 || parsed[0].Type != socket.MentionTypePerson {
		t.Fatalf("LS mention data must parse back: %v %+v", err, parsed)
	}
	if lsMentionData(text, nil) != nil {
		t.Fatal("no mentions must send no mention data")
	}

	wa := waOutgoingText(text, mentions)
	camille := waTypes.NewJID("7", waTypes.MessengerServer).String()
	theo := waTypes.NewJID("8", waTypes.MessengerServer).String()
	if wa.GetText() != "Salut 👋 @"+camille+" et @"+theo || !slices.Equal(wa.GetMentionedJID(), []string{camille, theo}) {
		t.Fatalf("unexpected E2EE text %q %v", wa.GetText(), wa.GetMentionedJID())
	}
	twice := waOutgoingText("Camille Camille", []Mention{{Offset: 0, Length: 7, UserID: "7"}, {Offset: 8, Length: 7, UserID: "7"}})
	if twice.GetText() != "@"+camille+" @"+camille || !slices.Equal(twice.GetMentionedJID(), []string{camille}) {
		t.Fatalf("a user mentioned twice must be listed once: %q %v", twice.GetText(), twice.GetMentionedJID())
	}
	if plain := waOutgoingText("hi", nil); plain.GetText() != "hi" || len(plain.GetMentionedJID()) != 0 {
		t.Fatal("plain text must stay untouched")
	}

	b, _ := groupBackend(t)
	b.store.SetContact("7", "Camille", "", "")
	b.store.SetContact("8", "Théo", "", "")
	back, backMentions := b.waTextWithMentions("55", wa)
	if back != text || !slices.Equal(backMentions, mentions) {
		t.Fatalf("E2EE payload must round-trip: %q %+v", back, backMentions)
	}
}

func TestMentionValidation(t *testing.T) {
	b, _ := groupBackend(t)
	b.store.UpdateThread("5", func(rec *threadRec) { rec.Members = map[string]bool{testSelf: true, "7": true} })
	text := "Coucou Camille 👋"
	ok := Mention{Offset: 7, Length: len("Camille"), UserID: "7"}
	cases := map[string]struct {
		thread   string
		mentions []Mention
	}{
		"one-to-one":      {"5", []Mention{ok}},
		"past end":        {"55", []Mention{{Offset: 7, Length: 40, UserID: "7"}}},
		"negative":        {"55", []Mention{{Offset: -1, Length: 2, UserID: "7"}}},
		"empty":           {"55", []Mention{{Offset: 7, Length: 0, UserID: "7"}}},
		"inside rune":     {"55", []Mention{{Offset: len(text) - 2, Length: 2, UserID: "7"}}},
		"overlap":         {"55", []Mention{ok, {Offset: 9, Length: 3, UserID: "8"}}},
		"not participant": {"55", []Mention{{Offset: 7, Length: 7, UserID: "123"}}},
		"self":            {"55", []Mention{{Offset: 7, Length: 7, UserID: testSelf}}},
	}
	for name, c := range cases {
		if _, err := b.store.CheckMentions(c.thread, text, c.mentions); toErrorBody(err).Code != CodeBadRequest {
			t.Errorf("%s: expected bad_request, got %v", name, err)
		}
		if _, err := b.Send(context.Background(), c.thread, text, "", "", c.mentions); toErrorBody(err).Code != CodeBadRequest {
			t.Errorf("%s: send must reject before reaching Meta, got %v", name, err)
		}
	}
	second := Mention{Offset: len("Coucou Camille "), Length: len("👋"), UserID: "8"}
	sorted, err := b.store.CheckMentions("55", text, []Mention{second, ok})
	if err != nil || !slices.Equal(sorted, []Mention{ok, second}) {
		t.Fatalf("valid mentions must be accepted and sorted: %v %+v", err, sorted)
	}
}

func TestParticipantsInThreadView(t *testing.T) {
	b, buf := groupBackend(t)
	b.store.SetNickname("55", "8", "Zed")
	view, _ := b.store.ThreadView("55")
	want := []Participant{
		{UserID: "7", Name: "Camille Martin", AvatarURL: "https://scontent.xx.fbcdn.net/c.jpg"},
		{UserID: "8", Name: "Zed"},
	}
	if !slices.Equal(view.Participants, want) {
		t.Fatalf("participants must exclude self and follow the nickname rule: %+v", view.Participants)
	}
	b.store.UpdateThread("5", func(rec *threadRec) { rec.Members = map[string]bool{testSelf: true, "7": true} })
	if one, _ := b.store.ThreadView("5"); one.Participants == nil || len(one.Participants) != 0 {
		t.Fatal("1:1 threads must have an empty participant list")
	}

	b.handleTable(&table.LSTable{LSRemoveParticipantFromThread: []*table.LSRemoveParticipantFromThread{{ThreadKey: 55, ParticipantId: 8}}}, modeLive)
	b.handleTable(&table.LSTable{LSVerifyContactRowExists: []*table.LSVerifyContactRowExists{{ContactId: 7, Name: "Camille M."}}}, modeLive)
	var last Thread
	count := 0
	for _, raw := range eventsOf(t, buf, EventThreads) {
		var ev ThreadsEvent
		json.Unmarshal(raw, &ev)
		for _, th := range ev.Threads {
			if th.ID == "55" {
				last = th
				count++
			}
		}
	}
	if count < 2 || len(last.Participants) != 1 || last.Participants[0].Name != "Camille M." {
		t.Fatalf("membership and contact changes of a named group must upsert its participants: %d %+v", count, last.Participants)
	}
}

func TestDemoMentions(t *testing.T) {
	h := newDemoHarness(t)
	unreadGroups := 0
	for _, th := range h.demo.store.ListThreads(0) {
		if !th.IsGroup || th.UnreadCount == 0 {
			continue
		}
		unreadGroups++
		msgs := h.demo.store.Messages(th.ID, 0, 1)
		if len(msgs) != 1 || len(msgs[0].Mentions) != 1 || msgs[0].Mentions[0].UserID != h.demo.user.ID {
			t.Fatalf("the newest message of unread group %s must mention the self user: %+v", th.ID, msgs)
		}
	}
	if unreadGroups == 0 {
		t.Fatal("demo has no unread group")
	}
	h.send(`{"id":1,"cmd":"connect"}`, `{"id":2,"cmd":"threads"}`)
	h.waitEvent(EventReady, 3*time.Second)
	var threads []Thread
	h.okResult(2, &threads)
	var group Thread
	for _, th := range threads {
		if !th.IsGroup && len(th.Participants) != 0 {
			t.Fatalf("1:1 thread %s has participants", th.ID)
		}
		if th.IsGroup && group.ID == "" {
			group = th
		}
		for _, p := range th.Participants {
			if p.UserID == h.demo.user.ID || p.Name == "" {
				t.Fatalf("bad participant %+v", p)
			}
		}
	}
	if group.ID == "" || len(group.Participants) < 2 {
		t.Fatalf("demo groups must list participants: %+v", group)
	}
	selfMentioned := false
	for _, th := range threads {
		h.send(fmt.Sprintf(`{"id":3,"cmd":"messages","params":{"thread_id":%q,"fetch_missing":false}}`, th.ID))
		var msgs []Message
		h.okResult(3, &msgs)
		h.out.mu.Lock()
		h.out.buf.Reset()
		h.out.mu.Unlock()
		for _, m := range msgs {
			for _, mention := range m.Mentions {
				if mention.Offset < 0 || mention.Offset+mention.Length > len(m.Text) {
					t.Fatalf("demo mention outside text %+v", m)
				}
				if mention.UserID == h.demo.user.ID && m.Text[mention.Offset:mention.Offset+mention.Length] == h.demo.user.Name {
					selfMentioned = true
				}
			}
		}
	}
	if !selfMentioned {
		t.Fatal("demo must contain a message mentioning the self user")
	}
	target := group.Participants[0]
	text := "Merci " + target.Name + " 🙏"
	h.send(
		fmt.Sprintf(`{"id":4,"cmd":"send","params":{"thread_id":%q,"text":%q,"mentions":[{"offset":6,"length":%d,"user_id":%q}]}}`, group.ID, text, len(target.Name), target.UserID),
		fmt.Sprintf(`{"id":5,"cmd":"send","params":{"thread_id":%q,"text":%q,"mentions":[{"offset":6,"length":200,"user_id":%q}]}}`, group.ID, text, target.UserID),
	)
	var sent SendResult
	h.okResult(4, &sent)
	if h.errCode(5) != CodeBadRequest {
		t.Fatal("an out-of-range mention must be rejected")
	}
	for _, raw := range h.events(EventMessage) {
		var m Message
		json.Unmarshal(raw, &m)
		if m.ID == sent.MessageID {
			if len(m.Mentions) != 1 || m.Mentions[0].UserID != target.UserID || m.Text[6:6+m.Mentions[0].Length] != target.Name {
				t.Fatalf("send echo must carry mentions: %+v", m)
			}
			return
		}
	}
	t.Fatal("no echo for the mention send")
}
