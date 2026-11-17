package main

import (
	"net/url"

	"go.mau.fi/whatsmeow/proto/waCommon"

	"go.mau.fi/whatsmeow/proto/waArmadilloApplication"
	"go.mau.fi/whatsmeow/proto/waArmadilloXMA"
	"go.mau.fi/whatsmeow/proto/waConsumerApplication"
	"go.mau.fi/whatsmeow/proto/waMediaTransport"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
)

func waWaveform(anc *waMediaTransport.AudioTransport_Ancillary) []float64 {
	if raw := anc.GetWaveform(); len(raw) > 0 {
		values := make([]float64, len(raw))
		for i, v := range raw {
			values[i] = float64(v)
		}
		return clampWaveform(values)
	}
	return parseWaveform(anc.GetWaveformData())
}

func waLocationAttachment(loc *waConsumerApplication.ConsumerApplication_Location, address string, live bool) waAttachment {
	lat, long := loc.DegreesLatitude, loc.DegreesLongitude
	title := loc.GetName()
	if title == "" {
		title = "Position"
		if live {
			title = "Position en direct"
		}
	}
	att := Attachment{ID: "location_0", Kind: "location", Title: title, Description: address}
	if lat != nil && long != nil {
		la, lo := *lat, *long
		att.Latitude, att.Longitude = &la, &lo
		att.URL = mapsLink(&la, &lo, "")
	}
	return waAttachment{Attachment: att}
}

var storyTargets = map[waArmadilloXMA.ExtendedContentMessage_ExtendedContentType]bool{
	waArmadilloXMA.ExtendedContentMessage_IG_STORY_PHOTO_SHARE: true,
	waArmadilloXMA.ExtendedContentMessage_IG_STORY_VIDEO_SHARE: true,
	waArmadilloXMA.ExtendedContentMessage_IG_STORY_REPLY:       true,
	waArmadilloXMA.ExtendedContentMessage_IG_STORY_REACTION:    true,
	waArmadilloXMA.ExtendedContentMessage_FB_STORY_SHARE:       true,
	waArmadilloXMA.ExtendedContentMessage_FB_STORY_REPLY:       true,
	waArmadilloXMA.ExtendedContentMessage_FB_STORY_MENTION:     true,
}

var storyLabels = map[waArmadilloXMA.ExtendedContentMessage_ExtendedContentType]string{
	waArmadilloXMA.ExtendedContentMessage_IG_STORY_REPLY:    "A répondu à votre story",
	waArmadilloXMA.ExtendedContentMessage_FB_STORY_REPLY:    "A répondu à votre story",
	waArmadilloXMA.ExtendedContentMessage_IG_STORY_REACTION: "A réagi à votre story",
	waArmadilloXMA.ExtendedContentMessage_FB_STORY_MENTION:  "Vous a mentionné dans sa story",
}

func extendedSpecial(c *waArmadilloXMA.ExtendedContentMessage, att *waAttachment) {
	target := c.GetTargetType()
	for _, cta := range c.GetCtas() {
		parsed, err := url.Parse(cta.GetNativeURL())
		if err != nil || parsed.Scheme != "messenger" || parsed.Host != "location_share" {
			continue
		}
		lat, long := parseLatLong(parsed.RawQuery, cta.GetNativeURL(), cta.GetActionURL())
		att.Kind = "location"
		att.Title = firstNonEmpty(c.GetTitleText(), "Position")
		att.Description = c.GetSubtitleText()
		att.Latitude, att.Longitude = lat, long
		att.URL = mapsLink(lat, long, httpLink(cta.GetActionURL()))
		return
	}
	switch {
	case storyTargets[target]:
		att.Kind = "story"
		att.Title = firstNonEmpty(storyLabels[target], att.Title, xmaTypeLabels[target], "Story")
	case target == waArmadilloXMA.ExtendedContentMessage_MSG_CONTACT:
		att.Kind = "contact"
		att.URL = ""
		att.Title = firstNonEmpty(c.GetTitleText(), att.Title, "Contact")
		att.Description = firstNonEmpty(c.GetSubtitleText(), att.Description)
	}
}

func armadilloSystemText(content *waArmadilloApplication.Armadillo_Content) string {
	ext := content.GetExtendedContentMessage()
	if ext == nil {
		return ""
	}
	switch ext.GetTargetType() {
	case waArmadilloXMA.ExtendedContentMessage_RTC_AUDIO_CALL:
		return callSystemText("xma_rtc_audio", ext.GetTitleText(), ext.GetSubtitleText())
	case waArmadilloXMA.ExtendedContentMessage_RTC_VIDEO_CALL:
		return callSystemText("xma_rtc_video", ext.GetTitleText(), ext.GetSubtitleText())
	case waArmadilloXMA.ExtendedContentMessage_RTC_MISSED_AUDIO_CALL:
		return callSystemText("xma_rtc_missed_audio", ext.GetTitleText(), ext.GetSubtitleText())
	case waArmadilloXMA.ExtendedContentMessage_RTC_MISSED_VIDEO_CALL:
		return callSystemText("xma_rtc_missed_video", ext.GetTitleText(), ext.GetSubtitleText())
	}
	return ""
}

func (b *MetaBackend) handleWADelivery(evt *events.Receipt) {
	if evt.Type != types.ReceiptTypeDelivered || evt.Sender.User == "" || evt.Sender.User == b.store.SelfID() {
		return
	}
	threadID := evt.Chat.User
	deliveredMS := evt.Timestamp.UnixMilli()
	for _, id := range evt.MessageIDs {
		if msg, ok := b.store.MessageInThread(threadID, id); ok && msg.TimestampMS > 0 {
			deliveredMS = max(deliveredMS, msg.TimestampMS)
		}
	}
	if b.store.SetDeliveryReceipt(threadID, evt.Sender.User, deliveredMS) {
		if view, ok := b.store.ThreadView(threadID); ok {
			b.out.Emit(EventThreads, ThreadsEvent{Threads: []Thread{view}})
		}
	}
}

func (b *MetaBackend) waKeySender(chat types.JID, actor string, key *waCommon.MessageKey) (string, bool) {
	self := b.store.SelfID()
	switch {
	case key.GetFromMe():
		return actor, actor != ""
	case key.GetParticipant() != "":
		participant, err := types.ParseJID(key.GetParticipant())
		if err != nil || participant.User == "" {
			return "", false
		}
		return participant.User, true
	case chat.Server == types.MessengerServer || chat.Server == types.DefaultUserServer:
		if actor == self {
			return chat.User, true
		}
		return self, true
	}
	return "", false
}

func (b *MetaBackend) authorizedWATarget(conv fbConversion, revoke bool) bool {
	if conv.targetKey == nil || conv.threadID == "" {
		return false
	}
	if remote, err := types.ParseJID(conv.targetKey.GetRemoteJID()); err == nil && !remote.IsEmpty() && remote.User != conv.chat.User {
		return false
	}
	msg, ok := b.store.MessageInThread(conv.threadID, conv.targetKey.GetID())
	if !ok {
		return false
	}
	owner := msg.SenderID
	if msg.IsOutgoing {
		owner = b.store.SelfID()
	}
	keySender, ok := b.waKeySender(conv.chat, conv.actor, conv.targetKey)
	if !ok || keySender != owner {
		return false
	}
	if owner == conv.actor {
		return true
	}
	if !revoke || conv.chat.Server != types.GroupServer {
		return false
	}
	rec, ok := b.store.Thread(conv.threadID)
	return ok && rec.Admins[conv.actor]
}
