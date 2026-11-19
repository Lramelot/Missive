package main

import (
	"context"
	"slices"
	"strconv"
	"strings"
	"time"

	"go.mau.fi/mautrix-meta/pkg/messagix/methods"
	"go.mau.fi/mautrix-meta/pkg/messagix/socket"
	"go.mau.fi/mautrix-meta/pkg/messagix/table"
	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/proto/waConsumerApplication"
	"go.mau.fi/whatsmeow/proto/waMsgApplication"
	"google.golang.org/protobuf/proto"
)

func (b *MetaBackend) ownMessage(threadID, messageID string) (threadRec, Message, error) {
	rec, err := b.loggedThread(threadID)
	if err != nil {
		return rec, Message{}, err
	}
	msg, ok := b.store.MessageInThread(threadID, messageID)
	if !ok {
		return rec, Message{}, newError(CodeNotFound, "unknown message %s", messageID)
	}
	if !msg.IsOutgoing && msg.SenderID != b.store.SelfID() {
		return rec, Message{}, newError(CodeBadRequest, "only your own messages can be changed")
	}
	return rec, msg, nil
}

func checkEditable(msg Message, now time.Time) error {
	switch {
	case msg.Deleted || msg.System || msg.Poll != nil:
		return newError(CodeBadRequest, "this message cannot be edited")
	case msg.Text == "" && len(msg.Attachments) > 0:
		return newError(CodeBadRequest, "only text messages can be edited")
	case now.Sub(time.UnixMilli(msg.TimestampMS)) > editWindow:
		return newError(CodeBadRequest, "Meta only allows edits during the first 15 minutes")
	}
	return nil
}

func wrapConsumerContent(content *waConsumerApplication.ConsumerApplication_Content) *waConsumerApplication.ConsumerApplication {
	return &waConsumerApplication.ConsumerApplication{Payload: &waConsumerApplication.ConsumerApplication_Payload{
		Payload: &waConsumerApplication.ConsumerApplication_Payload_Content{Content: content},
	}}
}

func waEditPayload(rec threadRec, msg Message, self, text string, nowMS int64) *waConsumerApplication.ConsumerApplication {
	return wrapConsumerContent(&waConsumerApplication.ConsumerApplication_Content{
		Content: &waConsumerApplication.ConsumerApplication_Content_EditMessage{EditMessage: &waConsumerApplication.ConsumerApplication_EditMessage{
			Key:         messageKeyFor(rec, msg, self),
			Message:     waOutgoingText(text, nil),
			TimestampMS: proto.Int64(nowMS),
		}},
	})
}

func waRevokePayload(rec threadRec, msg Message, self string) *waConsumerApplication.ConsumerApplication {
	return &waConsumerApplication.ConsumerApplication{Payload: &waConsumerApplication.ConsumerApplication_Payload{
		Payload: &waConsumerApplication.ConsumerApplication_Payload_ApplicationData{
			ApplicationData: &waConsumerApplication.ConsumerApplication_ApplicationData{
				ApplicationContent: &waConsumerApplication.ConsumerApplication_ApplicationData_Revoke{
					Revoke: &waConsumerApplication.ConsumerApplication_RevokeMessage{Key: messageKeyFor(rec, msg, self)},
				},
			},
		},
	}}
}

func (b *MetaBackend) sendWAConsumer(ctx context.Context, rec threadRec, consumer *waConsumerApplication.ConsumerApplication, deletedID string) error {
	id := strconv.FormatInt(methods.GenerateEpochID(), 10)
	meta := &waMsgApplication.MessageApplication_Metadata{}
	if b.waSendFn != nil {
		if err := b.waSendFn(ctx, rec, consumer); err != nil {
			return err
		}
	} else {
		if !b.connectedState() {
			return newError(CodeNotConnected, "not connected")
		}
		cli, err := b.waitE2EE(ctx)
		if err != nil {
			return err
		}
		if _, err := cli.SendFBMessage(ctx, chatJIDFor(rec), consumer, meta, whatsmeow.SendRequestExtra{ID: id}); err != nil {
			return newError(CodeInternal, "failed to send encrypted message: %v", err)
		}
	}
	if deletedID != "" {
		b.uploadSentToHistory(chatJIDFor(rec), id, deletedID, consumer, meta)
	}
	return nil
}

func (b *MetaBackend) lsEditAccepted(ctx context.Context, resp *table.LSTable, messageID, text string) bool {
	if resp == nil {
		return true
	}
	var confirmed *table.LSEditMessage
	for _, e := range resp.LSEditMessage {
		if e.MessageID == messageID {
			confirmed = e
		}
	}
	if confirmed == nil || confirmed.Text == text {
		return true
	}
	deadline := time.Now().Add(editConfirmWait)
	for time.Now().Before(deadline) {
		if !sleepCtx(ctx, editConfirmPoll) {
			break
		}
		if msg, ok := b.store.FindMessage(messageID); ok && msg.Text == text {
			return true
		}
	}
	return false
}

func (b *MetaBackend) EditMessage(ctx context.Context, threadID, messageID, text string) error {
	rec, msg, err := b.ownMessage(threadID, messageID)
	if err != nil {
		return err
	}
	if err := checkEditable(msg, time.Now()); err != nil {
		return err
	}
	if isLSMessageID(messageID) {
		var resp *table.LSTable
		resp, err = b.runTasks(ctx, &socket.EditMessageTask{MessageID: messageID, Text: text})
		if err == nil && !b.lsEditAccepted(ctx, resp, messageID, text) {
			return newError(CodeEditRejected, "Messenger rejected the edit")
		}
	} else {
		err = b.sendWAConsumer(ctx, rec, waEditPayload(rec, msg, b.store.SelfID(), text, time.Now().UnixMilli()), "")
	}
	if err != nil {
		return err
	}
	if updated, ok := b.store.UpdateMessage(messageID, func(m *Message) bool {
		if m.Text == text && m.Edited {
			return false
		}
		m.Text, m.Edited, m.Mentions = text, true, nil
		return true
	}); ok {
		b.emitMessage(EventMessageUpdate, updated)
	}
	return nil
}

func (b *MetaBackend) Unsend(ctx context.Context, threadID, messageID string) error {
	rec, msg, err := b.ownMessage(threadID, messageID)
	if err != nil {
		return err
	}
	if msg.Deleted || msg.System {
		return newError(CodeBadRequest, "this message cannot be unsent")
	}
	if isLSMessageID(messageID) {
		_, err = b.runTasks(ctx, &socket.DeleteMessageTask{MessageId: messageID})
	} else {
		err = b.sendWAConsumer(ctx, rec, waRevokePayload(rec, msg, b.store.SelfID()), messageID)
	}
	if err != nil {
		return err
	}
	b.markUnsent(messageID)
	return nil
}

func (b *MetaBackend) markUnsent(messageID string) {
	if updated, ok := b.store.MarkUnsent(messageID); ok {
		b.emitMessage(EventMessageUpdate, updated)
	}
}

func (b *MetaBackend) removeMessageLocally(threadID, messageID string) {
	if _, ok := b.store.RemoveMessage(messageID); ok {
		b.out.Emit(EventMessageRemoved, MessageRemovedEvent{ThreadID: threadID, MessageID: messageID})
		b.emitThread(threadID)
	}
}

func (b *MetaBackend) DeleteForMe(ctx context.Context, threadID, messageID string) error {
	rec, err := b.loggedThread(threadID)
	if err != nil {
		return err
	}
	if _, ok := b.store.MessageInThread(threadID, messageID); !ok {
		return newError(CodeNotFound, "unknown message %s", messageID)
	}
	if isLSMessageID(messageID) {
		key, err := b.lsThreadKey(rec)
		if err != nil {
			return err
		}
		if _, err := b.runTasks(ctx, &socket.DeleteMessageMeOnlyTask{ThreadKey: key, MessageId: messageID}); err != nil {
			return err
		}
	}
	b.removeMessageLocally(threadID, messageID)
	return nil
}

func lsForwardTask(key, otid int64, messageID string) *socket.SendMessageTask {
	return &socket.SendMessageTask{
		ThreadId:         key,
		Otid:             otid,
		Source:           table.MESSENGER_INBOX_IN_THREAD,
		InitiatingSource: table.FACEBOOK_INBOX,
		SendType:         table.FORWARD,
		SyncGroup:        1,
		ForwardedMsgId:   messageID,
	}
}

func (b *MetaBackend) Forward(ctx context.Context, fromThreadID, messageID string, toThreadIDs []string) ([]string, error) {
	if _, err := b.loggedThread(fromThreadID); err != nil {
		return nil, err
	}
	msg, ok := b.store.MessageInThread(fromThreadID, messageID)
	if !ok {
		return nil, newError(CodeNotFound, "unknown message %s", messageID)
	}
	if msg.Deleted || msg.System || msg.Poll != nil {
		return nil, newError(CodeBadRequest, "this message cannot be forwarded")
	}
	targets := make([]threadRec, 0, len(toThreadIDs))
	for _, id := range toThreadIDs {
		rec, ok := b.store.Thread(id)
		if !ok {
			return nil, newError(CodeNotFound, "unknown thread %s", id)
		}
		targets = append(targets, rec)
	}
	ids := make([]string, len(targets))
	var firstErr error
	failures := 0
	for i, rec := range targets {
		forward := b.forwardOne
		if b.forwardFn != nil {
			forward = b.forwardFn
		}
		id, err := forward(ctx, msg, rec)
		ids[i] = id
		if err != nil {
			if id == "" {
				failures++
			}
			if firstErr == nil {
				firstErr = err
			}
			b.log.Warn().Err(err).Bool("partial", id != "").Msg("Forward to one conversation failed")
		}
	}
	if failures == len(targets) && firstErr != nil {
		return nil, firstErr
	}
	return ids, nil
}

func (b *MetaBackend) forwardOne(ctx context.Context, msg Message, rec threadRec) (string, error) {
	if isLSMessageID(msg.ID) && !rec.E2EE {
		key, err := parseThreadKey(rec.ID)
		if err != nil {
			return "", err
		}
		client, err := b.sendClient(ctx)
		if err != nil {
			return "", err
		}
		otid := methods.GenerateEpochID()
		echo := Message{ThreadID: rec.ID, Text: msg.Text, Attachments: []Attachment{}, Forwarded: true}
		return b.executeLSSend(ctx, client, key, otid, lsForwardTask(key, otid, msg.ID), echo, nil)
	}
	var first string
	if strings.TrimSpace(msg.Text) != "" {
		id, err := b.Send(ctx, rec.ID, msg.Text, "", "", nil)
		if err != nil {
			return "", err
		}
		first = id
	}
	for _, att := range msg.Attachments {
		file, ok, err := b.forwardableFile(ctx, msg, att)
		if err != nil {
			return first, err
		}
		if !ok {
			continue
		}
		var id string
		if rec.E2EE {
			id, err = b.sendMediaE2EE(ctx, rec, file, "", nil, "")
		} else {
			id, err = b.sendMediaLS(ctx, rec, file, "", nil, "")
		}
		if err != nil {
			return first, err
		}
		if first == "" {
			first = id
		}
	}
	if first == "" {
		return "", newError(CodeUnsupported, "nothing in this message can be forwarded")
	}
	return first, nil
}

func (b *MetaBackend) forwardableFile(ctx context.Context, msg Message, att Attachment) (sendFile, bool, error) {
	switch att.Kind {
	case "image", "video", "audio", "file", "sticker":
	default:
		return sendFile{}, false, nil
	}
	res, err := b.fullMedia(ctx, msg.ID, att)
	if err != nil {
		return sendFile{}, false, err
	}
	files, err := loadSendFiles([]string{res.Path})
	if err != nil {
		return sendFile{}, false, err
	}
	file := files[0]
	if att.FileName != "" {
		file.Name = att.FileName
	}
	file.Voice, file.Waveform, file.DurationMS = att.Voice, att.Waveform, att.DurationMS
	if att.Voice {
		file.Kind = "audio"
	}
	return file, true, nil
}

func (b *MetaBackend) CreatePoll(ctx context.Context, threadID, question string, options []string) error {
	_, key, err := b.lsGroup(threadID, true)
	if err != nil {
		return err
	}
	_, err = b.runTasks(ctx, &socket.CreatePollTask{QuestionText: question, ThreadKey: key, Options: options, SyncGroup: 1})
	return err
}

func (b *MetaBackend) VotePoll(ctx context.Context, threadID, pollID string, optionIDs []string) error {
	rec, err := b.loggedThread(threadID)
	if err != nil {
		return err
	}
	poll, ok := b.store.PollRecord(pollID)
	if !ok || poll.ThreadID != threadID {
		return newError(CodeNotFound, "unknown poll %s", pollID)
	}
	selected, err := pollSelection(poll, optionIDs)
	if err != nil {
		return err
	}
	if rec.E2EE {
		return newError(CodeUnsupported, "polls are not available in encrypted conversations")
	}
	key, err := parseThreadKey(rec.ID)
	if err != nil {
		return err
	}
	pid, err := strconv.ParseInt(pollID, 10, 64)
	if err != nil {
		return newError(CodeBadRequest, "invalid poll id")
	}
	if _, err := b.runTasks(ctx, &socket.UpdatePollTask{ThreadKey: key, PollID: pid, SelectedOptions: selected, SyncGroup: 1}); err != nil {
		return err
	}
	if b.store.SetPollVotes(pollID, b.store.SelfID(), optionIDs, time.Now().UnixMilli()) {
		if msg, ok := b.store.FindMessage(poll.MessageID); ok {
			b.emitMessage(EventMessageUpdate, msg)
		}
	}
	return nil
}

func pollSelection(poll pollRec, optionIDs []string) ([]int64, error) {
	selected := make([]int64, 0, len(optionIDs))
	for _, id := range optionIDs {
		if !slices.ContainsFunc(poll.Options, func(o pollOptionRec) bool { return o.ID == id }) {
			return nil, newError(CodeBadRequest, "unknown poll option %s", id)
		}
		v, err := strconv.ParseInt(id, 10, 64)
		if err != nil {
			return nil, newError(CodeBadRequest, "invalid poll option %s", id)
		}
		selected = append(selected, v)
	}
	slices.Sort(selected)
	return slices.Compact(selected), nil
}
