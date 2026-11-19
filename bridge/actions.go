package main

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"go.mau.fi/whatsmeow"
	armadillo "go.mau.fi/whatsmeow/proto"
	"go.mau.fi/whatsmeow/proto/waArmadilloApplication"
	"go.mau.fi/whatsmeow/proto/waCommon"
	"go.mau.fi/whatsmeow/proto/waConsumerApplication"
	"go.mau.fi/whatsmeow/proto/waMediaTransport"
	"go.mau.fi/whatsmeow/proto/waMsgApplication"
	"go.mau.fi/whatsmeow/types"
	"google.golang.org/protobuf/proto"

	"go.mau.fi/mautrix-meta/pkg/messagix/httpclient"
	"go.mau.fi/mautrix-meta/pkg/messagix/methods"
	"go.mau.fi/mautrix-meta/pkg/messagix/securehistory"
	"go.mau.fi/mautrix-meta/pkg/messagix/socket"
	"go.mau.fi/mautrix-meta/pkg/messagix/table"
)

const (
	maxMediaHistoryPages    = 10
	mediaHistoryArchivePage = 50
	mediaHistoryDelay       = 300 * time.Millisecond
	historyUploadTimeout    = 2 * time.Minute
)

func (b *MetaBackend) threadAndReply(threadID, replyToID string) (threadRec, *Message, error) {
	if err := b.requireLogin(); err != nil {
		return threadRec{}, nil, err
	}
	rec, ok := b.store.Thread(threadID)
	if !ok {
		return threadRec{}, nil, newError(CodeNotFound, "unknown thread %s", threadID)
	}
	if replyToID == "" {
		return rec, nil, nil
	}
	target, found := b.store.MessageInThread(threadID, replyToID)
	if !found {
		return threadRec{}, nil, newError(CodeBadRequest, "reply target %s is not a message of this thread", replyToID)
	}
	return rec, &target, nil
}

func isLSMessageID(id string) bool {
	return strings.HasPrefix(id, "mid.")
}

func (b *MetaBackend) lsThreadKey(rec threadRec) (int64, error) {
	key, err := parseThreadKey(rec.ID)
	if err != nil {
		return 0, err
	}
	if rec.E2EE {
		if fbKey, ok := b.store.FBKeyFor(key); ok {
			return fbKey, nil
		}
	}
	return key, nil
}

func lsReplyMeta(replyTo *Message) *socket.ReplyMetaData {
	if replyTo == nil || !isLSMessageID(replyTo.ID) {
		return nil
	}
	sender, _ := strconv.ParseInt(replyTo.SenderID, 10, 64)
	return &socket.ReplyMetaData{ReplyMessageId: replyTo.ID, ReplySourceType: 1, ReplyType: 0, ReplySender: sender}
}

func (b *MetaBackend) cacheSentFile(messageID, attachmentID string, file sendFile, data []byte, rec mediaRec) {
	rec.Kind, rec.FileName = file.Kind, file.Name
	if rec.MimeType == "" {
		rec.MimeType = file.Mime
	}
	b.store.PutMedia(messageID, attachmentID, rec)
	if path, mimeType, err := saveMediaFile(b.cacheDir, data, file.Mime, file.Name); err == nil {
		b.store.SetMediaPath(messageID, attachmentID, path, mimeType)
	}
}

func uncertainError(err *BridgeError) *BridgeError {
	err.Data = map[string]any{"uncertain": true}
	return err
}

func isUncertain(err error) bool {
	var be *BridgeError
	return errors.As(err, &be) && be.Data["uncertain"] == true
}

func sendMediaError(err error, sent []string, failedIndex int) error {
	body := toErrorBody(err)
	return &BridgeError{
		Code:    body.Code,
		Message: body.Message,
		Data: map[string]any{
			"message_ids":  append([]string{}, sent...),
			"failed_index": failedIndex,
			"uncertain":    isUncertain(err),
		},
	}
}

func clientIDAt(clientIDs []string, i int) string {
	if i < len(clientIDs) {
		return clientIDs[i]
	}
	return ""
}

func (b *MetaBackend) SendMedia(ctx context.Context, threadID string, files []sendFile, caption, replyToID string, clientIDs []string) ([]string, error) {
	rec, replyTo, err := b.threadAndReply(threadID, replyToID)
	if err != nil {
		return nil, sendMediaError(err, nil, 0)
	}
	ids := make([]string, 0, len(files))
	for i, file := range files {
		text, reply := "", (*Message)(nil)
		if i == 0 {
			text, reply = caption, replyTo
		}
		clientID := clientIDAt(clientIDs, i)
		var id string
		if rec.E2EE {
			id, err = b.sendMediaE2EE(ctx, rec, file, text, reply, clientID)
		} else {
			id, err = b.sendMediaLS(ctx, rec, file, text, reply, clientID)
		}
		if err != nil {
			if len(ids) > 0 {
				b.log.Warn().Err(err).Int("sent", len(ids)).Msg("Media send stopped after partial success")
			}
			return ids, sendMediaError(err, ids, i)
		}
		ids = append(ids, id)
	}
	return ids, nil
}

func (b *MetaBackend) sendMediaLS(ctx context.Context, rec threadRec, file sendFile, caption string, replyTo *Message, clientID string) (string, error) {
	key, err := parseThreadKey(rec.ID)
	if err != nil {
		return "", err
	}
	client, err := b.connectedClient(ctx)
	if err != nil {
		return "", err
	}
	data, err := file.read()
	if err != nil {
		return "", err
	}
	upload := &httpclient.MercuryUploadMedia{
		Filename:  file.Name,
		MimeType:  file.Mime,
		MediaData: data,
	}
	if file.Voice {
		upload.IsVoiceClip = true
		if len(file.Waveform) > 0 {
			upload.WaveformData = &httpclient.WaveformData{Amplitudes: file.Waveform, SamplingFrequency: waveformSamplingHz(file.DurationMS, len(file.Waveform))}
		}
	}
	resp, err := client.GetHTTP().SendMercuryUploadRequest(ctx, key, upload)
	if err != nil {
		return "", newError(CodeInternal, "media upload failed: %v", err)
	}
	fbid := resp.Payload.RealMetadata.GetFbId()
	if fbid == 0 {
		return "", newError(CodeUnsupported, "Meta did not accept this file")
	}
	otid := methods.GenerateEpochID()
	task := &socket.SendMessageTask{
		ThreadId:         key,
		Otid:             otid,
		Source:           table.MESSENGER_INBOX_IN_THREAD,
		InitiatingSource: table.FACEBOOK_INBOX,
		SendType:         table.MEDIA,
		AttachmentFBIds:  []int64{fbid},
		SyncGroup:        1,
		Text:             caption,
		ReplyMetaData:    lsReplyMeta(replyTo),
	}
	attID := strconv.FormatInt(fbid, 10)
	echo := Message{ThreadID: rec.ID, Text: caption, Attachments: []Attachment{file.attachment(attID)}, ClientID: clientID}
	if replyTo != nil && isLSMessageID(replyTo.ID) {
		echo.ReplyToID = replyTo.ID
	}
	return b.executeLSSend(ctx, client, key, otid, task, echo, func(msgID string) {
		if msg, ok := b.store.FindMessage(msgID); ok {
			for _, att := range msg.Attachments {
				if r, ok := b.store.Media(msgID, att.ID); ok && r.Path == "" {
					if path, mimeType, err := saveMediaFile(b.cacheDir, data, file.Mime, file.Name); err == nil {
						b.store.SetMediaPath(msgID, att.ID, path, mimeType)
					}
				}
				break
			}
			return
		}
		b.cacheSentFile(msgID, attID, file, data, mediaRec{})
	})
}

func waQuotedMeta(replyTo *Message) *waMsgApplication.MessageApplication_Metadata {
	meta := &waMsgApplication.MessageApplication_Metadata{}
	if replyTo != nil && !isLSMessageID(replyTo.ID) {
		meta.QuotedMessage = &waMsgApplication.MessageApplication_Metadata_QuotedMessage{
			StanzaID:    proto.String(replyTo.ID),
			Participant: proto.String(types.NewJID(replyTo.SenderID, types.MessengerServer).String()),
		}
	}
	return meta
}

func (b *MetaBackend) sendMediaE2EE(ctx context.Context, rec threadRec, file sendFile, caption string, replyTo *Message, clientID string) (string, error) {
	if !b.connectedState() {
		return "", newError(CodeNotConnected, "not connected")
	}
	cli, err := b.waitE2EE(ctx)
	if err != nil {
		return "", err
	}
	mediaType := whatsmeow.MediaDocument
	kind := file.Kind
	switch {
	case file.Voice:
		mediaType = whatsmeow.MediaAudio
	case kind == "image" && file.Mime == "image/gif":
		mediaType = whatsmeow.MediaVideo
	case kind == "image":
		mediaType = whatsmeow.MediaImage
	case kind == "video":
		mediaType = whatsmeow.MediaVideo
	}
	data, err := file.read()
	if err != nil {
		return "", err
	}
	uploaded, err := cli.Upload(ctx, data, mediaType)
	if err != nil {
		return "", newError(CodeInternal, "encrypted upload failed: %v", err)
	}
	thumbW, thumbH := clampTo400(file.Width, file.Height)
	if thumbW == 0 {
		thumbW, thumbH = 400, 400
	}
	fileLength := proto.Uint64(uint64(len(data)))
	if mediaType == whatsmeow.MediaVideo {
		fileLength = nil
	}
	mimeType := file.Mime
	transport := &waMediaTransport.WAMediaTransport{
		Integral: &waMediaTransport.WAMediaTransport_Integral{
			FileSHA256:        uploaded.FileSHA256,
			MediaKey:          uploaded.MediaKey,
			FileEncSHA256:     uploaded.FileEncSHA256,
			DirectPath:        proto.String(uploaded.DirectPath),
			MediaKeyTimestamp: proto.Int64(time.Now().Unix()),
		},
		Ancillary: &waMediaTransport.WAMediaTransport_Ancillary{
			FileLength: fileLength,
			Mimetype:   &mimeType,
			Thumbnail: &waMediaTransport.WAMediaTransport_Ancillary_Thumbnail{
				ThumbnailWidth:  proto.Uint32(uint32(thumbW)),
				ThumbnailHeight: proto.Uint32(uint32(thumbH)),
			},
			ObjectID: proto.String(uploaded.ObjectID),
		},
	}
	captionText := &waCommon.MessageText{Text: proto.String(caption)}
	var content waConsumerApplication.ConsumerApplication_Content
	switch mediaType {
	case whatsmeow.MediaImage:
		msg := &waConsumerApplication.ConsumerApplication_ImageMessage{Caption: captionText}
		err = msg.Set(&waMediaTransport.ImageTransport{
			Integral:  &waMediaTransport.ImageTransport_Integral{Transport: transport},
			Ancillary: &waMediaTransport.ImageTransport_Ancillary{Width: proto.Uint32(uint32(file.Width)), Height: proto.Uint32(uint32(file.Height))},
		})
		content.Content = &waConsumerApplication.ConsumerApplication_Content_ImageMessage{ImageMessage: msg}
	case whatsmeow.MediaVideo:
		msg := &waConsumerApplication.ConsumerApplication_VideoMessage{Caption: captionText}
		err = msg.Set(&waMediaTransport.VideoTransport{
			Integral: &waMediaTransport.VideoTransport_Integral{Transport: transport},
			Ancillary: &waMediaTransport.VideoTransport_Ancillary{
				Width:       proto.Uint32(uint32(file.Width)),
				Height:      proto.Uint32(uint32(file.Height)),
				Caption:     captionText,
				GifPlayback: proto.Bool(file.Mime == "image/gif"),
				Sidecar:     uploaded.StreamingSidecar,
				IsHd:        proto.Bool(false),
			},
		})
		content.Content = &waConsumerApplication.ConsumerApplication_Content_VideoMessage{VideoMessage: msg}
	case whatsmeow.MediaAudio:
		transport.Ancillary.Thumbnail = nil
		msg := &waConsumerApplication.ConsumerApplication_AudioMessage{PTT: proto.Bool(true)}
		err = msg.Set(&waMediaTransport.AudioTransport{
			Integral: &waMediaTransport.AudioTransport_Integral{Transport: transport},
			Ancillary: &waMediaTransport.AudioTransport_Ancillary{
				Seconds:  proto.Uint32(uint32((file.DurationMS + 999) / 1000)),
				Waveform: waveformBytes(file.Waveform),
			},
		})
		content.Content = &waConsumerApplication.ConsumerApplication_Content_AudioMessage{AudioMessage: msg}
	default:
		fileName := file.Name
		msg := &waConsumerApplication.ConsumerApplication_DocumentMessage{FileName: &fileName}
		err = msg.Set(&waMediaTransport.DocumentTransport{
			Integral:  &waMediaTransport.DocumentTransport_Integral{Transport: transport},
			Ancillary: &waMediaTransport.DocumentTransport_Ancillary{},
		})
		content.Content = &waConsumerApplication.ConsumerApplication_Content_DocumentMessage{DocumentMessage: msg}
	}
	if err != nil {
		return "", newError(CodeInternal, "failed to build media message: %v", err)
	}
	consumer := &waConsumerApplication.ConsumerApplication{Payload: &waConsumerApplication.ConsumerApplication_Payload{
		Payload: &waConsumerApplication.ConsumerApplication_Payload_Content{Content: &content},
	}}
	if caption != "" && mediaType == whatsmeow.MediaDocument {
		if _, err := b.sendE2EE(ctx, rec, caption, replyTo, clientID, nil); err != nil {
			return "", err
		}
		replyTo = nil
	}
	meta := waQuotedMeta(replyTo)
	chat := chatJIDFor(rec)
	messageID := strconv.FormatInt(methods.GenerateEpochID(), 10)
	resp, err := cli.SendFBMessage(ctx, chat, consumer, meta, whatsmeow.SendRequestExtra{ID: messageID})
	if err != nil {
		return "", newError(CodeInternal, "failed to send encrypted media: %v", err)
	}
	b.uploadSentToHistory(chat, messageID, "", consumer, meta)
	ts := resp.Timestamp.UnixMilli()
	if resp.Timestamp.IsZero() {
		ts = time.Now().UnixMilli()
	}
	attID := uploaded.ObjectID
	if attID == "" {
		attID = "media_0"
	}
	att := file.attachment(attID)
	if mediaType == whatsmeow.MediaDocument {
		att.Kind = "file"
	}
	user := b.currentUser()
	echo := Message{
		ID:          messageID,
		ThreadID:    rec.ID,
		SenderID:    user.ID,
		SenderName:  user.Name,
		TimestampMS: ts,
		IsOutgoing:  true,
		Attachments: []Attachment{att},
		ClientID:    clientID,
	}
	if mediaType != whatsmeow.MediaDocument {
		echo.Text = caption
	}
	if replyTo != nil && !isLSMessageID(replyTo.ID) {
		echo.ReplyToID = replyTo.ID
	}
	if raw, err := proto.Marshal(transport); err == nil {
		b.cacheSentFile(messageID, attID, file, data, mediaRec{MimeType: file.Mime, WAType: string(mediaType), WATransport: raw})
	}
	b.recordNewMessage(echo, true)
	b.markReadAfterSend(ctx, rec.ID)
	return messageID, nil
}

func clampTo400(w, h int) (int, int) {
	if w > 400 {
		h = h * 400 / w
		w = 400
	}
	if h > 400 {
		w = w * 400 / h
		h = 400
	}
	return w, h
}

func (b *MetaBackend) connectedState() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.client != nil && b.connState != ""
}

func marshalSentFBMessage(message armadillo.RealMessageApplicationSub, metadata *waMsgApplication.MessageApplication_Metadata) ([]byte, error) {
	var subproto waMsgApplication.MessageApplication_SubProtocolPayload
	subproto.FutureProof = waCommon.FutureProofBehavior_PLACEHOLDER.Enum()
	switch typed := message.(type) {
	case *waConsumerApplication.ConsumerApplication:
		payload, err := proto.Marshal(typed)
		if err != nil {
			return nil, err
		}
		subproto.SubProtocol = &waMsgApplication.MessageApplication_SubProtocolPayload_ConsumerMessage{
			ConsumerMessage: &waCommon.SubProtocol{Payload: payload, Version: proto.Int32(whatsmeow.FBConsumerMessageVersion)},
		}
	case *waArmadilloApplication.Armadillo:
		payload, err := proto.Marshal(typed)
		if err != nil {
			return nil, err
		}
		subproto.SubProtocol = &waMsgApplication.MessageApplication_SubProtocolPayload_Armadillo{
			Armadillo: &waCommon.SubProtocol{Payload: payload, Version: proto.Int32(whatsmeow.FBArmadilloMessageVersion)},
		}
	default:
		return nil, fmt.Errorf("unsupported message type %T", message)
	}
	return proto.Marshal(&waMsgApplication.MessageApplication{
		Payload: &waMsgApplication.MessageApplication_Payload{
			Content: &waMsgApplication.MessageApplication_Payload_SubProtocol{SubProtocol: &subproto},
		},
		Metadata: metadata,
	})
}

func (b *MetaBackend) uploadSentToHistory(chat types.JID, messageID, deletedID string, message armadillo.RealMessageApplicationSub, metadata *waMsgApplication.MessageApplication_Metadata) {
	if b.historyUploadFn != nil {
		b.historyUploadFn(chat.String(), messageID, deletedID)
		return
	}
	device := b.historyDevice()
	if device == nil || device.ID == "" || chat.Server != types.MessengerServer || metadata == nil || len(metadata.GetFrankingKey()) == 0 {
		return
	}
	client := b.currentClient()
	b.e2eeMu.Lock()
	waDevice := b.waDevice
	b.e2eeMu.Unlock()
	if client == nil || waDevice == nil || waDevice.IdentityKey == nil {
		return
	}
	historyClient := client.GetSecureHistoryClient()
	selfID, _ := strconv.ParseInt(b.store.SelfID(), 10, 64)
	otherID, err1 := strconv.ParseInt(chat.User, 10, 64)
	otid, err2 := strconv.ParseInt(messageID, 10, 64)
	if historyClient.AppID == "" || selfID == 0 || err1 != nil || err2 != nil {
		return
	}
	identity := waDevice.IdentityKey
	backupID := device.BackupID
	life := b.lifeCtx()
	goSafe(b.log, "history_upload", func() {
		ctx, cancel := context.WithTimeout(life, historyUploadTimeout)
		defer cancel()
		transportBytes, err := marshalSentFBMessage(message, metadata)
		if err == nil {
			franking := hmac.New(sha256.New, metadata.GetFrankingKey())
			franking.Write(transportBytes)
			err = historyClient.UploadSentMessage(ctx, &securehistory.MinosSentMessage{
				BackupID:       backupID,
				SenderID:       selfID,
				OtherUserID:    otherID,
				MessageID:      messageID,
				DeletedID:      deletedID,
				TimestampMS:    otid >> 22,
				TransportBytes: transportBytes,
				FrankingTag:    franking.Sum(nil),
				IdentityPriv:   identity.Priv,
				IdentityPub:    identity.Pub,
			})
		}
		if err != nil {
			b.log.Warn().Err(err).Msg("Failed to upload sent message to encrypted history")
		}
	})
}

func messageKeyFor(rec threadRec, msg Message, self string) *waCommon.MessageKey {
	chat := chatJIDFor(rec)
	key := &waCommon.MessageKey{RemoteJID: proto.String(chat.String()), ID: proto.String(msg.ID)}
	if msg.SenderID == self || msg.IsOutgoing {
		key.FromMe = proto.Bool(true)
	}
	if rec.IsGroup {
		key.Participant = proto.String(types.NewJID(msg.SenderID, types.MessengerServer).String())
	}
	return key
}

func (b *MetaBackend) React(ctx context.Context, threadID, messageID, emoji string) error {
	rec, _, err := b.threadAndReply(threadID, "")
	if err != nil {
		return err
	}
	msg, ok := b.store.MessageInThread(threadID, messageID)
	if !ok {
		return newError(CodeNotFound, "unknown message %s", messageID)
	}
	self := b.store.SelfID()
	if !isLSMessageID(messageID) {
		if !b.connectedState() {
			return newError(CodeNotConnected, "not connected")
		}
		cli, err := b.waitE2EE(ctx)
		if err != nil {
			return err
		}
		consumer := &waConsumerApplication.ConsumerApplication{Payload: &waConsumerApplication.ConsumerApplication_Payload{
			Payload: &waConsumerApplication.ConsumerApplication_Payload_Content{Content: &waConsumerApplication.ConsumerApplication_Content{
				Content: &waConsumerApplication.ConsumerApplication_Content_ReactionMessage{ReactionMessage: &waConsumerApplication.ConsumerApplication_ReactionMessage{
					Key:               messageKeyFor(rec, msg, self),
					Text:              proto.String(emoji),
					SenderTimestampMS: proto.Int64(time.Now().UnixMilli()),
				}},
			}},
		}}
		id := strconv.FormatInt(methods.GenerateEpochID(), 10)
		if _, err := cli.SendFBMessage(ctx, chatJIDFor(rec), consumer, nil, whatsmeow.SendRequestExtra{ID: id}); err != nil {
			return newError(CodeInternal, "failed to send reaction: %v", err)
		}
	} else {
		key, err := b.lsThreadKey(rec)
		if err != nil {
			return err
		}
		client, err := b.connectedClient(ctx)
		if err != nil {
			return err
		}
		actor, _ := strconv.ParseInt(self, 10, 64)
		if _, err := client.ExecuteTasks(ctx, &socket.SendReactionTask{
			ThreadKey:       key,
			MessageID:       messageID,
			ActorID:         actor,
			Reaction:        emoji,
			SyncGroup:       1,
			SendAttribution: table.MESSENGER_INBOX_IN_THREAD,
		}); err != nil {
			return newError(CodeInternal, "failed to send reaction: %v", err)
		}
	}
	if b.store.SetReaction(messageID, self, emoji, time.Now().UnixMilli()) {
		b.emitMessage(EventMessageUpdate, msg)
	}
	return nil
}

func (b *MetaBackend) SetTyping(ctx context.Context, threadID string, typing bool) error {
	rec, _, err := b.threadAndReply(threadID, "")
	if err != nil {
		return err
	}
	life := b.lifeCtx()
	goSafe(b.log, "set_typing", func() {
		ctx, cancel := context.WithTimeout(life, 15*time.Second)
		defer cancel()
		if rec.E2EE {
			cli := b.currentE2EE()
			if cli == nil {
				return
			}
			state := types.ChatPresencePaused
			if typing {
				state = types.ChatPresenceComposing
			}
			if err := cli.SendChatPresence(ctx, chatJIDFor(rec), state, types.ChatPresenceMediaText); err != nil {
				b.log.Debug().Err(err).Msg("Failed to send encrypted typing state")
			}
			return
		}
		key, err := parseThreadKey(threadID)
		client := b.currentClient()
		if err != nil || client == nil {
			return
		}
		isGroup, isTyping := int64(0), int64(0)
		if rec.IsGroup {
			isGroup = 1
		}
		if typing {
			isTyping = 1
		}
		if err := client.ExecuteStatelessTask(ctx, &socket.UpdatePresenceTask{
			ThreadKey:     key,
			IsGroupThread: isGroup,
			IsTyping:      isTyping,
			SyncGroup:     1,
			ThreadType:    rec.ThreadType,
		}); err != nil {
			b.log.Debug().Err(err).Msg("Failed to send typing state")
		}
	})
	return nil
}

func (b *MetaBackend) mediaHistoryPage(ctx context.Context, rec threadRec) (progressed, possible bool) {
	threadID := rec.ID
	before := b.store.MessageCount(threadID)
	if rec.E2EE {
		if !b.historyEnrolled() {
			return false, false
		}
		_, svc, err := b.historyClient()
		if err != nil {
			return false, true
		}
		reference := time.Now().Add(historyFutureSlack).UnixMilli()
		if oldest, ok := b.store.OldestMessage(threadID); ok {
			reference = oldest.TimestampMS
		}
		if _, err := b.fetchHistoryPage(ctx, svc, rec, reference, mediaHistoryArchivePage); err != nil {
			return false, true
		}
		return b.store.MessageCount(threadID) > before, true
	}
	key, err := parseThreadKey(threadID)
	if err != nil {
		return false, false
	}
	b.mu.Lock()
	client := b.client
	connected := b.connState == StateConnected
	b.mu.Unlock()
	if client == nil || !connected {
		return false, true
	}
	if !b.fetchHistory(ctx, client, threadID, key) {
		return false, true
	}
	return b.store.MessageCount(threadID) > before, true
}

func (b *MetaBackend) MediaHistory(ctx context.Context, threadID string, beforeMS int64, limit int, kinds []string, maxPages int) (MediaHistoryResult, error) {
	rec, _, err := b.threadAndReply(threadID, "")
	if err != nil {
		return MediaHistoryResult{}, err
	}
	pages := 0
	fetchPage := func() (progressed, possible bool) {
		return b.mediaHistoryPage(ctx, rec)
	}
	exhausted, blocked := false, false
	items := b.store.MediaItems(threadID, beforeMS, kinds)
	for len(items) < limit && !exhausted && !blocked {
		if more, known := b.store.HasMoreBefore(threadID); known && !more {
			exhausted = true
			break
		}
		if pages >= maxPages || ctx.Err() != nil {
			break
		}
		if pages > 0 && !sleepCtx(ctx, mediaHistoryDelay) {
			break
		}
		pages++
		progressed, possible := fetchPage()
		switch {
		case !possible:
			exhausted = true
		case !progressed:
			blocked = true
			if more, known := b.store.HasMoreBefore(threadID); known && !more {
				exhausted = true
			}
		}
		items = b.store.MediaItems(threadID, beforeMS, kinds)
	}
	b.log.Info().Int("pages", pages).Int("items", len(items)).Bool("exhausted", exhausted).Bool("e2ee", rec.E2EE).Msg("Media history request")
	hasMore := !exhausted
	if more, known := b.store.HasMoreBefore(threadID); known && !more && len(items) <= limit {
		hasMore = false
	}
	items, truncated := cutMediaItems(items, limit)
	if truncated {
		hasMore = true
	}
	return MediaHistoryResult{Items: items, HasMore: hasMore}, nil
}
