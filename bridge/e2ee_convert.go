package main

import (
	"cmp"
	"fmt"
	"net/url"
	"strconv"

	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/proto/armadilloutil"
	"go.mau.fi/whatsmeow/proto/waCommon"
	"go.mau.fi/whatsmeow/proto/waMsgApplication"
	"google.golang.org/protobuf/proto"

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

type captioned interface {
	GetCaption() *waCommon.MessageText
}

type waAttachment struct {
	Attachment
	media mediaRec
}

func waMediaAttachment(kind string, transport *waMediaTransport.WAMediaTransport, mediaType whatsmeow.MediaType) waAttachment {
	anc := transport.GetAncillary()
	att := waAttachment{Attachment: Attachment{
		ID:       cmp.Or(anc.GetObjectID(), "media_0"),
		Kind:     kind,
		MimeType: anc.GetMimetype(),
		Size:     int64(anc.GetFileLength()),
	}}
	if transport.GetIntegral() != nil {
		if raw, err := proto.Marshal(transport); err == nil {
			att.media = mediaRec{Kind: kind, MimeType: anc.GetMimetype(), WAType: string(mediaType), WATransport: raw}
		}
	}
	return att
}

func waContentMedia(content *waConsumerApplication.ConsumerApplication_Content) (string, []waAttachment) {
	captionOf := func(c captioned) string { return c.GetCaption().GetText() }
	switch c := content.GetContent().(type) {
	case *waConsumerApplication.ConsumerApplication_Content_ExtendedTextMessage:
		return extendedTextToMessage(c.ExtendedTextMessage)
	case *waConsumerApplication.ConsumerApplication_Content_ImageMessage:
		att := waAttachment{Attachment: Attachment{ID: "media_0", Kind: "image"}}
		if t, err := c.ImageMessage.Decode(); err == nil {
			att = waMediaAttachment("image", t.GetIntegral().GetTransport(), whatsmeow.MediaImage)
			att.Width, att.Height = int(t.GetAncillary().GetWidth()), int(t.GetAncillary().GetHeight())
		}
		return captionOf(c.ImageMessage), []waAttachment{att}
	case *waConsumerApplication.ConsumerApplication_Content_VideoMessage:
		att := waAttachment{Attachment: Attachment{ID: "media_0", Kind: "video"}}
		if t, err := c.VideoMessage.Decode(); err == nil {
			att = waMediaAttachment("video", t.GetIntegral().GetTransport(), whatsmeow.MediaVideo)
			anc := t.GetAncillary()
			att.Width, att.Height = int(anc.GetWidth()), int(anc.GetHeight())
			att.DurationMS = int64(anc.GetSeconds()) * 1000
			att.Gif = anc.GetGifPlayback()
		}
		return captionOf(c.VideoMessage), []waAttachment{att}
	case *waConsumerApplication.ConsumerApplication_Content_AudioMessage:
		att := waAttachment{Attachment: Attachment{ID: "media_0", Kind: "audio"}}
		if t, err := c.AudioMessage.Decode(); err == nil {
			att = waMediaAttachment("audio", t.GetIntegral().GetTransport(), whatsmeow.MediaAudio)
			att.DurationMS = int64(t.GetAncillary().GetSeconds()) * 1000
			att.Waveform = waWaveform(t.GetAncillary())
		}
		att.Voice = true
		return "", []waAttachment{att}
	case *waConsumerApplication.ConsumerApplication_Content_DocumentMessage:
		att := waAttachment{Attachment: Attachment{ID: "media_0", Kind: "file"}}
		if t, err := c.DocumentMessage.Decode(); err == nil {
			att = waMediaAttachment("file", t.GetIntegral().GetTransport(), whatsmeow.MediaDocument)
		}
		att.FileName = c.DocumentMessage.GetFileName()
		att.media.FileName = att.FileName
		return "", []waAttachment{att}
	case *waConsumerApplication.ConsumerApplication_Content_StickerMessage:
		att := waAttachment{Attachment: Attachment{ID: "media_0", Kind: "sticker"}}
		if t, err := c.StickerMessage.Decode(); err == nil {
			att = waMediaAttachment("sticker", t.GetIntegral().GetTransport(), whatsmeow.MediaImage)
			anc := t.GetAncillary()
			att.Width, att.Height = int(anc.GetWidth()), int(anc.GetHeight())
			if id := anc.GetReceiverFetchID(); id != "" {
				att.media.Kind = "sticker"
				att.media.MimeType = att.MimeType
				att.media.ReceiverFetchID = id
			}
		}
		return "", []waAttachment{att}
	}
	switch c := content.GetContent().(type) {
	case *waConsumerApplication.ConsumerApplication_Content_LocationMessage:
		return "", []waAttachment{waLocationAttachment(c.LocationMessage.GetLocation(), c.LocationMessage.GetAddress(), false)}
	case *waConsumerApplication.ConsumerApplication_Content_LiveLocationMessage:
		return captionOf(c.LiveLocationMessage), []waAttachment{waLocationAttachment(c.LiveLocationMessage.GetLocation(), "", true)}
	case *waConsumerApplication.ConsumerApplication_Content_ContactMessage:
		return "", []waAttachment{{Attachment: Attachment{ID: "contact_0", Kind: "contact", Title: "Contact partagé"}}}
	}
	text, atts := waContentToMessage(content)
	out := make([]waAttachment, len(atts))
	for i, att := range atts {
		att.ID = "media_" + strconv.Itoa(i)
		out[i] = waAttachment{Attachment: att}
	}
	return text, out
}

func consumerMessageText(content *waConsumerApplication.ConsumerApplication_Content) *waCommon.MessageText {
	switch c := content.GetContent().(type) {
	case *waConsumerApplication.ConsumerApplication_Content_MessageText:
		return c.MessageText
	case *waConsumerApplication.ConsumerApplication_Content_ExtendedTextMessage:
		return c.ExtendedTextMessage.GetText()
	case *waConsumerApplication.ConsumerApplication_Content_ImageMessage:
		return c.ImageMessage.GetCaption()
	case *waConsumerApplication.ConsumerApplication_Content_VideoMessage:
		return c.VideoMessage.GetCaption()
	}
	return nil
}

func waContentToMessage(content *waConsumerApplication.ConsumerApplication_Content) (string, []Attachment) {
	switch c := content.GetContent().(type) {
	case *waConsumerApplication.ConsumerApplication_Content_MessageText:
		return c.MessageText.GetText(), nil
	case *waConsumerApplication.ConsumerApplication_Content_ExtendedTextMessage:
		return c.ExtendedTextMessage.GetText().GetText(), nil
	case *waConsumerApplication.ConsumerApplication_Content_ViewOnceMessage:
		return "", []Attachment{{Kind: "other"}}
	case *waConsumerApplication.ConsumerApplication_Content_LocationMessage:
		loc := c.LocationMessage
		text := loc.GetLocation().GetName()
		if addr := loc.GetAddress(); addr != "" {
			if text != "" {
				text += "\n"
			}
			text += addr
		}
		if text == "" {
			text = fmt.Sprintf("%f,%f", loc.GetLocation().GetDegreesLatitude(), loc.GetLocation().GetDegreesLongitude())
		}
		return text, nil
	default:
		return "", []Attachment{{Kind: "other"}}
	}
}

type fbConversion struct {
	msg          *Message
	media        []waAttachment
	editID       string
	editText     string
	editMentions []Mention
	revokeID     string
	reactTarget  string
	reactEmoji   string
	reactActor   string
	reactTS      int64
	system       bool
	threadID     string
	chat         types.JID
	actor        string
	targetKey    *waCommon.MessageKey
}

func (b *MetaBackend) convertFBMessage(threadID string, evt *events.FBMessage) fbConversion {
	conv := fbConversion{threadID: threadID, chat: evt.Info.Chat, actor: evt.Info.Sender.User}
	if evt.Info.IsFromMe {
		conv.actor = b.store.SelfID()
	}
	var text string
	var mentions []Mention
	var attachments []Attachment
	if ca := evt.GetConsumerApplication(); ca != nil {
		switch payload := ca.GetPayload().GetPayload().(type) {
		case *waConsumerApplication.ConsumerApplication_Payload_Content:
			switch c := payload.Content.GetContent().(type) {
			case *waConsumerApplication.ConsumerApplication_Content_EditMessage:
				conv.editID = c.EditMessage.GetKey().GetID()
				conv.targetKey = c.EditMessage.GetKey()
				conv.editText, conv.editMentions = b.waTextWithMentions(threadID, c.EditMessage.GetMessage())
				return conv
			case *waConsumerApplication.ConsumerApplication_Content_ReactionMessage:
				conv.reactTarget = c.ReactionMessage.GetKey().GetID()
				conv.reactEmoji = c.ReactionMessage.GetText()
				conv.reactActor = evt.Info.Sender.User
				conv.reactTS = evt.Info.Timestamp.UnixMilli()
				if evt.Info.IsFromMe {
					conv.reactActor = b.store.SelfID()
				}
				return conv
			}
			text, conv.media = waContentMedia(payload.Content)
			for _, att := range conv.media {
				attachments = append(attachments, att.Attachment)
			}
			if mt := consumerMessageText(payload.Content); mt != nil && mt.GetText() == text {
				text, mentions = b.waTextWithMentions(threadID, mt)
			}
		case *waConsumerApplication.ConsumerApplication_Payload_ApplicationData:
			if revoke := payload.ApplicationData.GetRevoke(); revoke != nil {
				conv.revokeID = revoke.GetKey().GetID()
				conv.targetKey = revoke.GetKey()
			}
			return conv
		default:
			return conv
		}
	} else if arm := evt.GetArmadillo(); arm != nil && arm.GetPayload().GetContent() != nil {
		text, conv.media = armadilloToMessage(arm.GetPayload().GetContent())
		if systemText := armadilloSystemText(arm.GetPayload().GetContent()); systemText != "" {
			text, conv.media, conv.system = systemText, nil, true
		}
		for _, att := range conv.media {
			attachments = append(attachments, att.Attachment)
		}
	} else {
		return conv
	}
	if attachments == nil {
		attachments = []Attachment{}
	}
	sender := evt.Info.Sender.User
	senderName := b.store.ContactName(sender)
	if senderName == "" {
		senderName = evt.Info.PushName
	}
	isOutgoing := evt.Info.IsFromMe || sender == b.store.SelfID()
	if isOutgoing && senderName == "" {
		senderName = b.currentUser().Name
	}
	conv.msg = &Message{
		ID:          evt.Info.ID,
		ThreadID:    threadID,
		SenderID:    sender,
		SenderName:  senderName,
		Text:        text,
		TimestampMS: evt.Info.Timestamp.UnixMilli(),
		IsOutgoing:  isOutgoing,
		Attachments: attachments,
		ReplyToID:   evt.FBApplication.GetMetadata().GetQuotedMessage().GetStanzaID(),
		Mentions:    mentions,
		System:      conv.system,
	}
	if quoted := evt.FBApplication.GetMetadata().GetQuotedMessage(); quoted != nil && quoted.GetStanzaID() != "" {
		conv.msg.ReplyTo = b.quotedPreview(quoted)
	}
	return conv
}

func (b *MetaBackend) quotedPreview(quoted *waMsgApplication.MessageApplication_Metadata_QuotedMessage) *ReplyPreview {
	preview := &ReplyPreview{ID: quoted.GetStanzaID()}
	if participant, err := types.ParseJID(quoted.GetParticipant()); err == nil && participant.User != "" {
		preview.SenderName = b.store.ContactName(participant.User)
		if participant.User == b.store.SelfID() && preview.SenderName == "" {
			preview.SenderName = b.currentUser().Name
		}
	}
	if sp := quoted.GetPayload().GetSubProtocol().GetConsumerMessage(); sp != nil {
		if consumer, err := armadilloutil.Unmarshal(&waConsumerApplication.ConsumerApplication{}, sp, sp.GetVersion()); err == nil {
			text, atts := waContentMedia(consumer.GetPayload().GetContent())
			preview.Text = text
			if len(atts) > 0 {
				preview.Kind = atts[0].Kind
			}
		}
	}
	return preview
}
