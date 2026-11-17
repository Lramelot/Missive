package main

import (
	"context"
	"strconv"
	"time"
)

const (
	demoMediaThreadSpec  = 7
	demoMediaHistoryMax  = 700
	demoMediaHistoryPage = 60
	demoBackfillDelay    = 600 * time.Millisecond
)

func (d *DemoBackend) mediaThreadID() string {
	return strconv.FormatInt(7_000_000_000_000_000+int64(demoMediaThreadSpec)*7919, 10)
}

func (d *DemoBackend) historyCap(threadID string) int {
	if rec, ok := d.store.Thread(threadID); ok && rec.Folder == FolderRequests {
		return 0
	}
	if threadID == d.mediaThreadID() {
		return demoMediaHistoryMax
	}
	return demoMaxHistory
}

func (d *DemoBackend) seedReceipts() {
	for i, th := range d.store.ListThreads(0) {
		if i%3 != 0 {
			continue
		}
		members := d.members[th.ID]
		for j, member := range members {
			if j == 2 {
				break
			}
			d.store.SetReadReceipt(th.ID, member, th.LastActivityMS-int64(j)*90_000)
		}
	}
}

func (d *DemoBackend) requireThread(threadID string) (threadRec, error) {
	if err := d.requireLogin(); err != nil {
		return threadRec{}, err
	}
	rec, ok := d.store.Thread(threadID)
	if !ok {
		return threadRec{}, newError(CodeNotFound, "unknown thread %s", threadID)
	}
	return rec, nil
}

func (d *DemoBackend) SendMedia(ctx context.Context, threadID string, files []sendFile, caption, replyToID string, clientIDs []string) ([]string, error) {
	d.mu.Lock()
	rec, err := d.requireThread(threadID)
	if err == nil && !d.connected {
		err = newError(CodeNotConnected, "not connected")
	}
	if err == nil && replyToID != "" {
		if _, found := d.store.MessageInThread(threadID, replyToID); !found {
			err = newError(CodeBadRequest, "reply target %s is not a message of this thread", replyToID)
		}
	}
	if err != nil {
		d.mu.Unlock()
		return nil, sendMediaError(err, nil, 0)
	}
	var sent []Message
	for i, file := range files {
		data, err := file.read()
		if err != nil {
			d.mu.Unlock()
			ids := make([]string, len(sent))
			for j, msg := range sent {
				ids[j] = msg.ID
			}
			return ids, sendMediaError(err, ids, i)
		}
		msg := Message{
			ID:          d.nextMessageID(rec.E2EE),
			ThreadID:    threadID,
			SenderID:    d.user.ID,
			SenderName:  d.user.Name,
			TimestampMS: time.Now().UnixMilli() + int64(i),
			IsOutgoing:  true,
			Attachments: []Attachment{file.attachment("media_0")},
		}
		if i == 0 {
			msg.Text = caption
			msg.ReplyToID = replyToID
		}
		d.store.PutMessage(msg)
		if path, mimeType, err := saveMediaFile(d.dataDir, data, file.Mime, file.Name); err == nil {
			d.store.SetMediaPath(msg.ID, "media_0", path, mimeType)
		}
		d.store.UpdateThread(threadID, func(rec *threadRec) {
			rec.LastActivityMS = msg.TimestampMS
			rec.Snippet = snippetFor(msg)
			rec.UnreadCount = 0
		})
		sent = append(sent, msg)
	}
	d.mu.Unlock()
	ids := make([]string, len(sent))
	for i, msg := range sent {
		ids[i] = msg.ID
		echo := d.store.Decorate(msg)
		echo.ClientID = clientIDAt(clientIDs, i)
		d.out.Emit(EventMessage, echo)
	}
	if view, ok := d.store.ThreadView(threadID); ok {
		d.out.Emit(EventThreads, ThreadsEvent{Threads: []Thread{view}})
	}
	return ids, nil
}

func (d *DemoBackend) React(ctx context.Context, threadID, messageID, emoji string) error {
	d.mu.Lock()
	if _, err := d.requireThread(threadID); err != nil {
		d.mu.Unlock()
		return err
	}
	msg, ok := d.store.MessageInThread(threadID, messageID)
	if !ok {
		d.mu.Unlock()
		return newError(CodeNotFound, "unknown message %s", messageID)
	}
	changed := d.store.SetReaction(messageID, d.user.ID, emoji, time.Now().UnixMilli())
	d.mu.Unlock()
	if changed {
		d.out.Emit(EventMessageUpdate, d.store.Decorate(msg))
	}
	return nil
}

func (d *DemoBackend) SetTyping(ctx context.Context, threadID string, typing bool) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	_, err := d.requireThread(threadID)
	return err
}

func (d *DemoBackend) SetActive(ctx context.Context, active bool) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.active = active
	return nil
}

func (d *DemoBackend) Search(ctx context.Context, query, threadID string, limit int) ([]SearchHit, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if err := d.requireLogin(); err != nil {
		return nil, err
	}
	return d.store.Search(query, threadID, limit), nil
}

func (d *DemoBackend) MediaHistory(ctx context.Context, threadID string, beforeMS int64, limit int, kinds []string, maxPages int) (MediaHistoryResult, error) {
	d.mu.Lock()
	rec, err := d.requireThread(threadID)
	if err != nil {
		d.mu.Unlock()
		return MediaHistoryResult{}, err
	}
	items := d.store.MediaItems(threadID, beforeMS, kinds)
	pages := 0
	for len(items) < limit && d.generated[threadID] < d.historyCap(threadID) && pages < maxPages {
		pages++
		d.generateOlder(threadID, rec.E2EE, demoMediaHistoryPage)
		items = d.store.MediaItems(threadID, beforeMS, kinds)
	}
	hasMore := d.generated[threadID] < d.historyCap(threadID)
	d.mu.Unlock()
	items, truncated := cutMediaItems(items, limit)
	if truncated {
		hasMore = true
	}
	return MediaHistoryResult{Items: items, HasMore: hasMore}, nil
}
