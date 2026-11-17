package main

import (
	"net/url"
	"strings"

	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/proto/armadilloutil"
	"go.mau.fi/whatsmeow/proto/waArmadilloApplication"
	"go.mau.fi/whatsmeow/proto/waArmadilloXMA"
	"go.mau.fi/whatsmeow/proto/waCommon"
	"go.mau.fi/whatsmeow/proto/waConsumerApplication"
	"go.mau.fi/whatsmeow/proto/waMediaTransport"
	"go.mau.fi/whatsmeow/proto/waMsgApplication"

	"go.mau.fi/mautrix-meta/pkg/messagix/table"
)

func removeLPHP(addr string) string {
	parsed, err := url.Parse(addr)
	if err == nil && parsed.Path == "/l.php" {
		if target := parsed.Query().Get("u"); target != "" {
			return target
		}
	}
	return addr
}

func httpLink(candidates ...string) string {
	for _, candidate := range candidates {
		if candidate == "" {
			continue
		}
		candidate = removeLPHP(candidate)
		parsed, err := url.Parse(candidate)
		if err == nil && (parsed.Scheme == "https" || parsed.Scheme == "http") && parsed.Host != "" {
			return candidate
		}
	}
	return ""
}

func linkSource(target string) string {
	parsed, err := url.Parse(target)
	if err != nil || parsed.Host == "" {
		return ""
	}
	host := strings.ToLower(parsed.Hostname())
	for _, prefix := range []string{"www.", "m.", "l.", "lm."} {
		host = strings.TrimPrefix(host, prefix)
	}
	switch {
	case host == "instagram.com" || strings.HasSuffix(host, ".instagram.com"):
		return "Instagram"
	case host == "facebook.com" || strings.HasSuffix(host, ".facebook.com") || host == "fb.watch" || host == "fb.com" || host == "fb.me":
		return "Facebook"
	case host == "threads.net" || host == "threads.com":
		return "Threads"
	case host == "messenger.com":
		return "Messenger"
	}
	return host
}

func isImageURL(link string) bool {
	parsed, err := url.Parse(link)
	if err != nil {
		return false
	}
	host := strings.ToLower(parsed.Hostname())
	return strings.Contains(host, "fbcdn.net") || strings.Contains(host, "cdninstagram.com")
}

func xmaToAttachment(id string, x *table.WrappedXMA) (Attachment, mediaRec) {
	thumb := firstNonEmpty(x.PreviewUrlLarge, x.PreviewUrl, x.ImageUrl, x.HeaderImageUrl)
	thumbMime := firstNonEmpty(x.PreviewUrlMimeType, x.HeaderImageUrlMimeType)
	att := Attachment{
		ID:          id,
		Title:       firstNonEmpty(x.TitleText, x.HeaderTitle),
		Description: firstNonEmpty(x.SubtitleText, x.CaptionBodyText, x.HeaderSubtitleText),
		Width:       int(x.PreviewWidth),
		Height:      int(x.PreviewHeight),
	}
	thumbRec := mediaRec{Kind: "image", URL: thumb, MimeType: thumbMime, ExpiresMS: x.PreviewUrlExpirationTimestampMs}
	ctaType, ctaAction, ctaNative := "", "", ""
	if x.CTA != nil {
		ctaType, ctaAction, ctaNative = x.CTA.Type_, x.CTA.ActionUrl, x.CTA.NativeUrl
	}
	switch {
	case strings.HasPrefix(ctaType, "xma_rtc_"):
		att.Kind = "other"
		att.Title = firstNonEmpty(att.Title, "Appel")
		return att, mediaRec{}
	case ctaType == "xma_live_location_sharing":
		att.Kind = "other"
		att.Title = firstNonEmpty(att.Title, "Position partagée")
		return att, mediaRec{}
	}
	target := httpLink(ctaAction, x.ActionUrl, ctaNative)
	if target != "" && !isImageURL(target) {
		att.Kind = "link"
		att.URL = target
		att.ThumbnailURL = thumb
		att.Source = firstNonEmpty(x.SourceText, linkSource(target))
		if att.Title == "" {
			att.Title = target
		}
		return att, thumbRec
	}
	kind := attachmentKind(x.AttachmentType)
	if kind == "other" {
		att.Kind = "other"
		att.ThumbnailURL = thumb
		att.Title = firstNonEmpty(att.Title, "Publication partagée")
		return att, thumbRec
	}
	media := lsMedia{
		kind: x.AttachmentType, fileName: x.Filename,
		playable: x.PlayableUrl, playableMime: x.PlayableUrlMimeType, playableExp: x.PlayableUrlExpirationTimestampMs,
		preview: firstNonEmpty(x.PreviewUrl, x.ImageUrl), previewMime: x.PreviewUrlMimeType, previewExp: x.PreviewUrlExpirationTimestampMs,
		previewLarge: x.PreviewUrlLarge, width: x.PreviewWidth, height: x.PreviewHeight,
	}
	converted, rec := media.convert(id)
	converted.Title, converted.Description = att.Title, att.Description
	return converted, rec
}

var xmaTypeLabels = map[waArmadilloXMA.ExtendedContentMessage_ExtendedContentType]string{
	waArmadilloXMA.ExtendedContentMessage_IG_SINGLE_IMAGE_POST_SHARE: "Publication Instagram",
	waArmadilloXMA.ExtendedContentMessage_IG_MULTIPOST_SHARE:         "Publication Instagram",
	waArmadilloXMA.ExtendedContentMessage_IG_SINGLE_VIDEO_POST_SHARE: "Publication Instagram",
	waArmadilloXMA.ExtendedContentMessage_IG_CLIPS_SHARE:             "Reel Instagram",
	waArmadilloXMA.ExtendedContentMessage_IG_IGTV_SHARE:              "Vidéo Instagram",
	waArmadilloXMA.ExtendedContentMessage_IG_PROFILE_SHARE:           "Profil Instagram",
	waArmadilloXMA.ExtendedContentMessage_IG_STORY_PHOTO_SHARE:       "Story Instagram",
	waArmadilloXMA.ExtendedContentMessage_IG_STORY_VIDEO_SHARE:       "Story Instagram",
	waArmadilloXMA.ExtendedContentMessage_IG_STORY_REPLY:             "Réponse à une story Instagram",
	waArmadilloXMA.ExtendedContentMessage_IG_STORY_REACTION:          "Réaction à une story Instagram",
	waArmadilloXMA.ExtendedContentMessage_IG_EXTERNAL_LINK:           "Lien",
	waArmadilloXMA.ExtendedContentMessage_MSG_IG_MEDIA_SHARE:         "Publication Instagram",
	waArmadilloXMA.ExtendedContentMessage_FB_FEED_SHARE:              "Publication Facebook",
	waArmadilloXMA.ExtendedContentMessage_FB_FEED_VIDEO_SHARE:        "Vidéo Facebook",
	waArmadilloXMA.ExtendedContentMessage_FB_GROUP_POST:              "Publication Facebook",
	waArmadilloXMA.ExtendedContentMessage_FB_SHORT:                   "Reel Facebook",
	waArmadilloXMA.ExtendedContentMessage_FB_STORY_SHARE:             "Story Facebook",
	waArmadilloXMA.ExtendedContentMessage_FB_STORY_REPLY:             "Réponse à une story Facebook",
	waArmadilloXMA.ExtendedContentMessage_FB_STORY_MENTION:           "Mention dans une story",
	waArmadilloXMA.ExtendedContentMessage_FB_EVENT:                   "Évènement Facebook",
	waArmadilloXMA.ExtendedContentMessage_MSG_EXTERNAL_LINK_SHARE:    "Lien",
	waArmadilloXMA.ExtendedContentMessage_MSG_THREADS_POST_SHARE:     "Publication Threads",
	waArmadilloXMA.ExtendedContentMessage_MSG_REELS_LIST:             "Reels",
	waArmadilloXMA.ExtendedContentMessage_MSG_CONTACT:                "Contact partagé",
	waArmadilloXMA.ExtendedContentMessage_MSG_FILE:                   "Fichier",
	waArmadilloXMA.ExtendedContentMessage_MSG_NOTE_REPLY:             "Réponse à une note",
	waArmadilloXMA.ExtendedContentMessage_MSG_MUSIC_STICKER:          "Musique",
	waArmadilloXMA.ExtendedContentMessage_RTC_AUDIO_CALL:             "Appel audio",
	waArmadilloXMA.ExtendedContentMessage_RTC_VIDEO_CALL:             "Appel vidéo",
	waArmadilloXMA.ExtendedContentMessage_RTC_MISSED_AUDIO_CALL:      "Appel audio manqué",
	waArmadilloXMA.ExtendedContentMessage_RTC_MISSED_VIDEO_CALL:      "Appel vidéo manqué",
}

func xmaTypeSource(t waArmadilloXMA.ExtendedContentMessage_ExtendedContentType) string {
	name := t.String()
	switch {
	case strings.HasPrefix(name, "IG_") || t == waArmadilloXMA.ExtendedContentMessage_MSG_IG_MEDIA_SHARE:
		return "Instagram"
	case strings.HasPrefix(name, "FB_"):
		return "Facebook"
	case t == waArmadilloXMA.ExtendedContentMessage_MSG_THREADS_POST_SHARE:
		return "Threads"
	}
	return ""
}

func decodeImageSubProtocol(sp *waCommon.SubProtocol) *waMediaTransport.ImageTransport {
	if sp == nil || len(sp.GetPayload()) == 0 {
		return nil
	}
	image, err := armadilloutil.Unmarshal(&waMediaTransport.ImageTransport{}, sp, sp.GetVersion())
	if err != nil {
		return nil
	}
	return image
}

func attachPreview(att *waAttachment, sp *waCommon.SubProtocol) {
	image := decodeImageSubProtocol(sp)
	if image == nil || image.GetIntegral().GetTransport().GetIntegral().GetDirectPath() == "" {
		return
	}
	preview := waMediaAttachment("image", image.GetIntegral().GetTransport(), whatsmeow.MediaImage)
	att.media = preview.media
	if att.Width == 0 {
		att.Width, att.Height = int(image.GetAncillary().GetWidth()), int(image.GetAncillary().GetHeight())
	}
	if att.MimeType == "" {
		att.MimeType = preview.MimeType
	}
}

func storyReplyText(c *waArmadilloXMA.ExtendedContentMessage) string {
	if c.GetAssociatedMessage() == nil {
		return ""
	}
	var assoc waMsgApplication.MessageApplication
	if _, err := armadilloutil.Unmarshal(&assoc, c.GetAssociatedMessage(), 2); err != nil {
		return ""
	}
	var consumer waConsumerApplication.ConsumerApplication
	if _, err := armadilloutil.Unmarshal(&consumer, assoc.GetPayload().GetSubProtocol().GetConsumerMessage(), 1); err != nil {
		return ""
	}
	text, _ := waContentToMessage(consumer.GetPayload().GetContent())
	return text
}

func extendedContentToMessage(c *waArmadilloXMA.ExtendedContentMessage) (string, []waAttachment) {
	text := c.GetMessageText()
	var native, action []string
	for _, cta := range c.GetCtas() {
		native = append(native, cta.GetNativeURL())
		action = append(action, cta.GetActionURL())
		if parsed, err := url.Parse(cta.GetNativeURL()); err == nil && parsed.Scheme == "messenger" && parsed.Host == "location_share" {
			att := waAttachment{Attachment: Attachment{ID: "location_0"}}
			extendedSpecial(c, &att)
			return text, []waAttachment{att}
		}
	}
	targetType := c.GetTargetType()
	if text == "" && (targetType == waArmadilloXMA.ExtendedContentMessage_FB_STORY_REPLY || targetType == waArmadilloXMA.ExtendedContentMessage_IG_STORY_REPLY) {
		text = storyReplyText(c)
	}
	label := xmaTypeLabels[targetType]
	att := waAttachment{Attachment: Attachment{
		ID:          "xma_0",
		Title:       firstNonEmpty(c.GetTitleText(), c.GetHeaderTitle(), c.GetOverlayTitle()),
		Description: firstNonEmpty(c.GetSubtitleText(), c.GetOverlayDescription(), c.GetHeaderSubtitle()),
	}}
	target := httpLink(append(native, action...)...)
	if target != "" {
		att.Kind = "link"
		att.URL = target
		att.Source = firstNonEmpty(xmaTypeSource(targetType), linkSource(target))
		att.Title = firstNonEmpty(att.Title, label, target)
	} else {
		att.Kind = "other"
		att.Title = firstNonEmpty(att.Title, label, "Publication partagée")
		att.Source = xmaTypeSource(targetType)
	}
	for _, preview := range c.GetPreviews() {
		attachPreview(&att, preview)
		if att.media.WATransport != nil {
			break
		}
	}
	if att.media.WATransport == nil {
		attachPreview(&att, c.GetHeaderImage())
	}
	extendedSpecial(c, &att)
	return text, []waAttachment{att}
}

func extendedTextToMessage(e *waConsumerApplication.ConsumerApplication_ExtendedTextMessage) (string, []waAttachment) {
	text := e.GetText().GetText()
	if e.GetTitle() == "" && e.GetDescription() == "" {
		return text, nil
	}
	target := httpLink(e.GetCanonicalURL(), e.GetMatchedText())
	if target == "" {
		return text, nil
	}
	att := waAttachment{Attachment: Attachment{
		ID:          "link_0",
		Kind:        "link",
		URL:         target,
		Title:       firstNonEmpty(e.GetTitle(), target),
		Description: e.GetDescription(),
		Source:      linkSource(target),
	}}
	attachPreview(&att, e.GetThumbnail())
	return text, []waAttachment{att}
}

func armadilloToMessage(content *waArmadilloApplication.Armadillo_Content) (string, []waAttachment) {
	switch c := content.GetContent().(type) {
	case *waArmadilloApplication.Armadillo_Content_ExtendedContentMessage:
		return extendedContentToMessage(c.ExtendedContentMessage)
	case *waArmadilloApplication.Armadillo_Content_ImageGalleryMessage_:
		images, err := c.ImageGalleryMessage.Decode()
		if err != nil {
			return "", []waAttachment{{Attachment: Attachment{ID: "media_0", Kind: "other", Title: "Galerie"}}}
		}
		out := make([]waAttachment, 0, len(images))
		for i, image := range images {
			att := waMediaAttachment("image", image.GetIntegral().GetTransport(), whatsmeow.MediaImage)
			if att.ID == "media_0" || att.ID == "" {
				att.ID = "media_" + itoa(int64(i))
			}
			att.Width, att.Height = int(image.GetAncillary().GetWidth()), int(image.GetAncillary().GetHeight())
			out = append(out, att)
		}
		return "", out
	case *waArmadilloApplication.Armadillo_Content_RavenMessage_:
		return "", []waAttachment{{Attachment: Attachment{ID: "media_0", Kind: "other", Title: "Photo ou vidéo éphémère"}}}
	case *waArmadilloApplication.Armadillo_Content_RavenMessageMsgr:
		return "", []waAttachment{{Attachment: Attachment{ID: "media_0", Kind: "other", Title: "Photo ou vidéo éphémère"}}}
	case *waArmadilloApplication.Armadillo_Content_BumpExistingMessage_:
		return "", []waAttachment{{Attachment: Attachment{ID: "media_0", Kind: "other", Title: "Message remonté"}}}
	}
	return "", []waAttachment{{Attachment: Attachment{ID: "media_0", Kind: "other", Title: "Message non pris en charge"}}}
}
