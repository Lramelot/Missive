package main

import (
	"context"
	"slices"
	"strconv"
	"time"
)

func (d *DemoBackend) ownDemoMessage(threadID, messageID string) (threadRec, Message, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	rec, err := d.requireThread(threadID)
	if err != nil {
		return rec, Message{}, err
	}
	msg, ok := d.store.MessageInThread(threadID, messageID)
	if !ok {
		return rec, Message{}, newError(CodeNotFound, "unknown message %s", messageID)
	}
	if !msg.IsOutgoing {
		return rec, Message{}, newError(CodeBadRequest, "only your own messages can be changed")
	}
	return rec, d.store.Decorate(msg), nil
}

func (d *DemoBackend) EditMessage(ctx context.Context, threadID, messageID, text string) error {
	_, msg, err := d.ownDemoMessage(threadID, messageID)
	if err != nil {
		return err
	}
	if err := checkEditable(msg, time.Now()); err != nil {
		return err
	}
	if updated, ok := d.store.UpdateMessage(messageID, func(m *Message) bool {
		m.Text, m.Edited, m.Mentions = text, true, nil
		return true
	}); ok {
		d.out.Emit(EventMessageUpdate, d.store.Decorate(updated))
	}
	return nil
}

func (d *DemoBackend) Unsend(ctx context.Context, threadID, messageID string) error {
	_, msg, err := d.ownDemoMessage(threadID, messageID)
	if err != nil {
		return err
	}
	if msg.Deleted || msg.System {
		return newError(CodeBadRequest, "this message cannot be unsent")
	}
	if updated, ok := d.store.MarkUnsent(messageID); ok {
		d.out.Emit(EventMessageUpdate, d.store.Decorate(updated))
	}
	return nil
}

func (d *DemoBackend) DeleteForMe(ctx context.Context, threadID, messageID string) error {
	d.mu.Lock()
	_, err := d.requireThread(threadID)
	if err == nil {
		if _, ok := d.store.MessageInThread(threadID, messageID); !ok {
			err = newError(CodeNotFound, "unknown message %s", messageID)
		}
	}
	d.mu.Unlock()
	if err != nil {
		return err
	}
	if _, ok := d.store.RemoveMessage(messageID); ok {
		d.out.Emit(EventMessageRemoved, MessageRemovedEvent{ThreadID: threadID, MessageID: messageID})
		d.emitThreadIDs(threadID)
	}
	return nil
}

func (d *DemoBackend) Forward(ctx context.Context, fromThreadID, messageID string, toThreadIDs []string) ([]string, error) {
	d.mu.Lock()
	_, err := d.requireThread(fromThreadID)
	if err != nil {
		d.mu.Unlock()
		return nil, err
	}
	src, ok := d.store.MessageInThread(fromThreadID, messageID)
	if !ok {
		d.mu.Unlock()
		return nil, newError(CodeNotFound, "unknown message %s", messageID)
	}
	if src.Deleted || src.System || d.store.MessagePollID(messageID) != "" {
		d.mu.Unlock()
		return nil, newError(CodeBadRequest, "this message cannot be forwarded")
	}
	var sent []Message
	ids := make([]string, 0, len(toThreadIDs))
	for i, target := range toThreadIDs {
		rec, err := d.requireThread(target)
		if err != nil {
			d.mu.Unlock()
			return nil, err
		}
		msg := d.demoMessage(target, rec.E2EE, time.Now().UnixMilli()+int64(i), d.user.ID)
		msg.Text = src.Text
		msg.Forwarded = true
		msg.Attachments = slices.Clone(src.Attachments)
		for _, att := range msg.Attachments {
			if media, ok := d.store.Media(src.ID, att.ID); ok && media.Path != "" {
				d.store.SetMediaPath(msg.ID, att.ID, media.Path, media.MimeType)
			}
		}
		d.appendLive(msg)
		sent = append(sent, msg)
		ids = append(ids, msg.ID)
	}
	d.mu.Unlock()
	d.emitNew(sent...)
	return ids, nil
}

func (d *DemoBackend) SendVoice(ctx context.Context, threadID, path string, durationMS int64, clientID, replyToID string) (string, error) {
	d.mu.Lock()
	rec, err := d.requireThread(threadID)
	if err == nil && replyToID != "" {
		if _, found := d.store.MessageInThread(threadID, replyToID); !found {
			err = newError(CodeBadRequest, "reply target %s is not a message of this thread", replyToID)
		}
	}
	d.mu.Unlock()
	if err != nil {
		return "", err
	}
	file, err := prepareVoice(path, durationMS)
	if err != nil {
		return "", err
	}
	d.mu.Lock()
	msg := d.demoMessage(threadID, rec.E2EE, time.Now().UnixMilli(), d.user.ID)
	msg.ReplyToID = replyToID
	msg.Attachments = []Attachment{file.attachment("voice_0")}
	if saved, mimeType, err := saveMediaFile(d.cacheDir, file.Data, file.Mime, file.Name); err == nil {
		d.store.SetMediaPath(msg.ID, "voice_0", saved, mimeType)
	}
	d.appendLive(msg)
	d.mu.Unlock()
	echo := d.store.Decorate(msg)
	echo.ClientID = clientID
	d.out.Emit(EventMessage, echo)
	d.emitThreadIDs(threadID)
	return msg.ID, nil
}

func (d *DemoBackend) CreatePoll(ctx context.Context, threadID, question string, options []string) error {
	d.mu.Lock()
	_, err := d.demoGroup(threadID, true)
	if err != nil {
		d.mu.Unlock()
		return err
	}
	msg := d.demoMessage(threadID, false, time.Now().UnixMilli(), d.user.ID)
	d.counter++
	pollID := "poll_" + strconv.FormatInt(time.Now().UnixMilli(), 10) + "_" + strconv.FormatInt(d.counter, 10)
	opts := make([]pollOptionRec, len(options))
	for i, o := range options {
		opts[i] = pollOptionRec{ID: pollID + "_" + strconv.Itoa(i+1), Text: o}
	}
	d.appendLive(msg)
	d.store.UpsertPoll(pollID, threadID, msg.ID, question, opts, true)
	d.mu.Unlock()
	d.emitNew(msg)
	return nil
}

func (d *DemoBackend) VotePoll(ctx context.Context, threadID, pollID string, optionIDs []string) error {
	d.mu.Lock()
	_, err := d.requireThread(threadID)
	d.mu.Unlock()
	if err != nil {
		return err
	}
	poll, ok := d.store.PollRecord(pollID)
	if !ok || poll.ThreadID != threadID {
		return newError(CodeNotFound, "unknown poll %s", pollID)
	}
	for _, id := range optionIDs {
		if !slices.ContainsFunc(poll.Options, func(o pollOptionRec) bool { return o.ID == id }) {
			return newError(CodeBadRequest, "unknown poll option %s", id)
		}
	}
	if d.store.SetPollVotes(pollID, d.user.ID, optionIDs, time.Now().UnixMilli()) {
		if msg, ok := d.store.FindMessage(poll.MessageID); ok {
			d.out.Emit(EventMessageUpdate, d.store.Decorate(msg))
		}
	}
	return nil
}
