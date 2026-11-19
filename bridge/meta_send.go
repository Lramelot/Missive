package main

import (
	"context"
	"strconv"
	"strings"
	"time"

	"go.mau.fi/mautrix-meta/pkg/messagix"
	"go.mau.fi/mautrix-meta/pkg/messagix/methods"
	"go.mau.fi/mautrix-meta/pkg/messagix/socket"
	"go.mau.fi/mautrix-meta/pkg/messagix/table"
)

func (b *MetaBackend) Send(ctx context.Context, threadID, text, replyToID, clientID string, mentions []Mention) (string, error) {
	if err := b.requireLogin(); err != nil {
		return "", err
	}
	rec, ok := b.store.Thread(threadID)
	if !ok {
		return "", newError(CodeNotFound, "unknown thread %s", threadID)
	}
	var replyTo *Message
	if replyToID != "" {
		target, found := b.store.MessageInThread(threadID, replyToID)
		if !found {
			return "", newError(CodeBadRequest, "reply target %s is not a message of this thread", replyToID)
		}
		replyTo = &target
	}
	mentions, err := b.store.CheckMentions(threadID, text, mentions)
	if err != nil {
		return "", err
	}
	if rec.E2EE {
		return b.sendE2EE(ctx, rec, text, replyTo, clientID, mentions)
	}
	key, err := parseThreadKey(threadID)
	if err != nil {
		return "", err
	}
	client, err := b.sendClient(ctx)
	if err != nil {
		return "", err
	}
	otid := methods.GenerateEpochID()
	task := &socket.SendMessageTask{
		ThreadId:         key,
		Otid:             otid,
		Source:           table.MESSENGER_INBOX_IN_THREAD,
		InitiatingSource: table.FACEBOOK_INBOX,
		SendType:         table.TEXT,
		SyncGroup:        1,
		Text:             text,
		MentionData:      lsMentionData(text, mentions),
	}
	if strings.Contains(text, "http://") || strings.Contains(text, "https://") {
		task.TextHasLinks = 1
	}
	task.ReplyMetaData = lsReplyMeta(replyTo)
	echo := Message{ThreadID: threadID, Text: text, Attachments: []Attachment{}, ClientID: clientID, Mentions: mentions}
	if task.ReplyMetaData != nil {
		echo.ReplyToID = replyTo.ID
	}
	return b.executeLSSend(ctx, client, key, otid, task, echo, nil)
}

func (b *MetaBackend) sendClient(ctx context.Context) (*messagix.Client, error) {
	if b.taskFn != nil {
		return nil, nil
	}
	return b.connectedClient(ctx)
}

func (b *MetaBackend) executeSendTasks(ctx context.Context, client *messagix.Client, tasks ...socket.Task) (*table.LSTable, error) {
	if b.taskFn != nil {
		return b.taskFn(ctx, tasks...)
	}
	return client.ExecuteTasks(ctx, tasks...)
}

func (b *MetaBackend) executeLSSend(ctx context.Context, client *messagix.Client, key, otid int64, task *socket.SendMessageTask, echo Message, onEcho func(msgID string)) (string, error) {
	otidStr := strconv.FormatInt(otid, 10)
	if echo.ClientID != "" {
		b.pendingClientIDs.Store(otidStr, echo.ClientID)
		defer b.pendingClientIDs.Delete(otidStr)
	}
	tasks := []socket.Task{task}
	marksRead := sendMarksRead(ctx)
	readAt := time.Now().UnixMilli()
	if marksRead {
		tasks = append(tasks, &socket.ThreadMarkReadTask{ThreadId: key, SyncGroup: 1, LastReadWatermarkTs: readAt})
	}
	resp, err := b.executeSendTasks(ctx, client, tasks...)
	if err != nil {
		return "", uncertainError(newError(CodeInternal, "failed to send message: %v", err))
	}
	var msgID string
	if resp != nil {
		for _, replace := range resp.LSReplaceOptimsiticMessage {
			if replace.OfflineThreadingId == otidStr {
				msgID = replace.MessageId
			}
		}
		if msgID == "" {
			for _, failed := range resp.LSMarkOptimisticMessageFailed {
				return "", newError(CodeInternal, "message rejected: %s", failed.Message)
			}
			for _, failed := range resp.LSHandleFailedTask {
				return "", uncertainError(newError(CodeInternal, "message not confirmed: %s", failed.Message))
			}
		}
		b.handleTable(resp, modeLive)
	}
	if msgID == "" {
		return "", uncertainError(newError(CodeInternal, "Meta did not confirm the message"))
	}
	if onEcho != nil {
		onEcho(msgID)
	}
	if marksRead {
		b.store.UpdateExistingThread(echo.ThreadID, func(rec *threadRec) { rec.MarkedReadMS = max(rec.MarkedReadMS, readAt) })
	}
	if _, found := b.store.FindMessage(msgID); !found {
		ts := time.Now().UnixMilli()
		if parsed, err := methods.ParseMessageID(msgID); err == nil && parsed > 0 && abs64(parsed-ts) < int64(time.Hour/time.Millisecond) {
			ts = parsed
		}
		user := b.currentUser()
		echo.ID = msgID
		echo.SenderID = user.ID
		echo.SenderName = user.Name
		echo.TimestampMS = ts
		echo.IsOutgoing = true
		b.recordNewMessage(echo, true)
	}
	return msgID, nil
}
