package main

import (
	"slices"
	"testing"
	"time"

	"go.mau.fi/whatsmeow/proto/waCommon"
	"go.mau.fi/whatsmeow/proto/waConsumerApplication"
	"go.mau.fi/whatsmeow/proto/waMediaTransport"
	waTypes "go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
	"google.golang.org/protobuf/proto"
)

func TestE2EEMessageConversion(t *testing.T) {
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

func TestE2EEDisconnectClassification(t *testing.T) {
	b, _, _ := newTestMetaBackend(t)
	cases := []struct {
		evt  events.PermanentDisconnect
		want e2eeAction
	}{
		{&events.ConnectFailure{Reason: events.ConnectFailureNotFound}, e2eeActionReset},
		{&events.ConnectFailure{Reason: events.ConnectFailureClientUnknown}, e2eeActionReset},
		{&events.ConnectFailure{Reason: events.ConnectFailureGeneric}, e2eeActionFullReconnect},
		{&events.ConnectFailure{Reason: events.ConnectFailureReason(503)}, e2eeActionRetry},
		{&events.LoggedOut{Reason: events.ConnectFailureLoggedOut}, e2eeActionReset},
		{&events.LoggedOut{Reason: events.ConnectFailureLoggedOut}, e2eeActionGiveUp},
	}
	for i, c := range cases {
		if got := b.classifyE2EEDisconnect(c.evt); got != c.want {
			t.Errorf("case %d: got %d want %d", i, got, c.want)
		}
	}
	b.e2eeLogoutRetried.Store(false)
	if got := b.classifyE2EEDisconnect(&events.LoggedOut{}); got != e2eeActionReset {
		t.Fatal("a new connection must allow one logout reset again")
	}
	for i := 0; i < maxE2EEDeviceResets; i++ {
		if !b.allowE2EEReset() {
			t.Fatalf("reset %d must be allowed", i)
		}
	}
	if b.allowE2EEReset() {
		t.Fatal("device resets must be capped per session")
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

func TestE2EEEditAuthorization(t *testing.T) {
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
