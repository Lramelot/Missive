package main

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"os"
	"slices"

	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/proto/waCommon"
	"go.mau.fi/whatsmeow/proto/waMediaTransport"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
	"google.golang.org/protobuf/proto"
)

func (b *MetaBackend) applyEditOrRevoke(conv fbConversion) bool {
	switch {
	case conv.reactTarget != "":
		prev := b.store.ReactionOf(conv.reactTarget, conv.reactActor)
		if b.store.SetReaction(conv.reactTarget, conv.reactActor, conv.reactEmoji, conv.reactTS) {
			if msg, ok := b.store.FindMessage(conv.reactTarget); ok {
				b.emitMessage(EventMessageUpdate, msg)
			}
			if b.liveE2EEReaction(conv.reactTS) {
				if evt, ok := b.store.reactionAddedEvent(prev, conv.reactTarget, conv.reactActor, conv.reactEmoji); ok {
					b.out.Emit(EventReactionAdded, evt)
				}
			}
		}
		return true
	case conv.editID != "":
		if !b.authorizedWATarget(conv, false) {
			b.log.Warn().Msg("Ignoring encrypted edit of a message the sender does not own")
			return true
		}
		text, mentions := conv.editText, conv.editMentions
		if updated, ok := b.store.UpdateMessage(conv.editID, func(m *Message) bool {
			if m.Text == text && m.Edited && slices.Equal(m.Mentions, mentions) {
				return false
			}
			m.Mentions = mentions
			m.Text = text
			m.Edited = true
			return true
		}); ok {
			b.emitMessage(EventMessageUpdate, updated)
		}
		return true
	case conv.revokeID != "":
		if !b.authorizedWATarget(conv, true) {
			b.log.Warn().Msg("Ignoring encrypted revoke of a message the sender does not own")
			return true
		}
		b.markUnsent(conv.revokeID)
		return true
	}
	return false
}

func (b *MetaBackend) storeConvertedMedia(conv fbConversion) {
	for _, att := range conv.media {
		if att.media.WATransport != nil || att.media.ReceiverFetchID != "" {
			b.store.PutMedia(conv.msg.ID, att.ID, att.media)
		}
	}
}

func (b *MetaBackend) handleFBMessage(evt *events.FBMessage) {
	threadID := b.ensureE2EEThread(evt.Info.Chat)
	conv := b.convertFBMessage(threadID, evt)
	if b.applyEditOrRevoke(conv) || conv.msg == nil {
		return
	}
	b.storeConvertedMedia(conv)
	b.recordNewMessage(*conv.msg, isLiveTimestamp(conv.msg.TimestampMS) && !b.waOfflineSyncing.Load())
	b.ensureContact(conv.msg.SenderID)
	if evt.Info.Chat.Server != types.GroupServer {
		b.ensureContact(evt.Info.Chat.User)
	}
}

func (b *MetaBackend) downloadE2EEMedia(ctx context.Context, rec mediaRec, maxBytes int64, f *os.File) (string, error) {
	if rec.ReceiverFetchID != "" {
		client := b.currentClient()
		if client == nil {
			return "", newError(CodeNotConnected, "not connected")
		}
		fetched, err := client.FetchReceiverFetchMedia(ctx, rec.ReceiverFetchID)
		if err != nil {
			return "", newError(CodeUnsupported, "sticker is no longer available: %v", err)
		}
		mimeType, err := httpDownloadToFile(ctx, fetched.URL, maxBytes, f)
		return cmp.Or(fetched.MimeType, mimeType), err
	}
	var transport waMediaTransport.WAMediaTransport
	if err := proto.Unmarshal(rec.WATransport, &transport); err != nil {
		return "", newError(CodeInternal, "corrupt media record: %v", err)
	}
	if length := transport.GetAncillary().GetFileLength(); length > uint64(maxBytes) {
		return "", newError(CodeUnsupported, "media larger than %d bytes", maxBytes)
	}
	cli, err := b.waitE2EE(ctx)
	if err != nil {
		return "", err
	}
	if err = cli.DownloadFBToFile(ctx, transport.GetIntegral(), whatsmeow.MediaType(rec.WAType), f); err != nil {
		err = withoutURL(err)
		if errors.Is(err, whatsmeow.ErrMediaDownloadFailedWith404) || errors.Is(err, whatsmeow.ErrMediaDownloadFailedWith410) {
			return "", fmt.Errorf("%w: %v", errMediaExpired, err)
		}
		return "", err
	}
	return cmp.Or(rec.MimeType, transport.GetAncillary().GetMimetype()), nil
}

func (b *MetaBackend) handleWAReceipt(evt *events.Receipt) {
	if evt.Type == types.ReceiptTypeDelivered {
		b.handleWADelivery(evt)
		return
	}
	switch evt.Type {
	case types.ReceiptTypeRead, types.ReceiptTypeReadSelf:
	default:
		return
	}
	threadID := evt.Chat.User
	ts := evt.Timestamp.UnixMilli()
	self := b.store.SelfID()
	if evt.Type == types.ReceiptTypeReadSelf || evt.Sender.User == self {
		if b.store.HasThread(threadID) {
			b.store.UpdateThread(threadID, func(rec *threadRec) {
				rec.LastReadMS = max(rec.LastReadMS, ts)
				rec.MetaReadMS = max(rec.MetaReadMS, ts)
				if rec.LastReadMS >= rec.LastActivityMS {
					rec.UnreadCount = 0
				}
			})
			if view, ok := b.store.ThreadView(threadID); ok {
				b.out.Emit(EventThreads, ThreadsEvent{Threads: []Thread{view}})
			}
		}
		b.out.Emit(EventRead, ReadEvent{ThreadID: threadID, UserID: self, TimestampMS: ts})
		return
	}
	readMS := ts
	for _, id := range evt.MessageIDs {
		if msg, ok := b.store.MessageInThread(threadID, id); ok && msg.TimestampMS > 0 {
			readMS = max(readMS, msg.TimestampMS)
		}
	}
	if b.store.SetReadReceipt(threadID, evt.Sender.User, readMS) {
		if view, ok := b.store.ThreadView(threadID); ok {
			b.out.Emit(EventThreads, ThreadsEvent{Threads: []Thread{view}})
		}
	}
	b.out.Emit(EventRead, ReadEvent{ThreadID: threadID, UserID: evt.Sender.User, TimestampMS: readMS})
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
	if remote, err := types.ParseJID(conv.targetKey.GetRemoteJID()); err == nil && !remote.IsEmpty() && remote.Server == types.GroupServer && (conv.chat.Server != types.GroupServer || remote.User != conv.chat.User) {
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
