package main

import (
	"context"
	"strconv"
	"time"

	"go.mau.fi/mautrix-meta/pkg/messagix"
	"go.mau.fi/mautrix-meta/pkg/messagix/socket"
)

func (b *MetaBackend) Threads(ctx context.Context, limit int) ([]Thread, error) {
	if err := b.requireLogin(); err != nil {
		return nil, err
	}
	threads := b.store.ListThreads(limit)
	b.ordering.markAnnounced(threads)
	return threads, nil
}

func parseThreadKey(threadID string) (int64, error) {
	key, err := strconv.ParseInt(threadID, 10, 64)
	if err != nil || key <= 0 {
		return 0, newError(CodeNotFound, "unknown thread %s", threadID)
	}
	return key, nil
}

func (b *MetaBackend) Messages(ctx context.Context, threadID string, beforeMS int64, limit int, fetchMissing bool) ([]Message, error) {
	if err := b.requireLogin(); err != nil {
		return nil, err
	}
	rec, ok := b.store.Thread(threadID)
	if !ok {
		return nil, newError(CodeNotFound, "unknown thread %s", threadID)
	}
	msgs := b.store.Messages(threadID, beforeMS, limit)
	if len(msgs) < limit && fetchMissing {
		b.startBackfill(rec, beforeMS, limit-len(msgs))
	}
	if beforeMS == 0 && len(msgs) > 0 {
		b.refreshSnippet(msgs[len(msgs)-1])
	}
	if beforeMS == 0 && fetchMissing && b.needsCatchUp(rec) {
		b.startCatchUp(rec)
	}
	return msgs, nil
}

func (b *MetaBackend) refreshSnippet(newest Message) {
	changed := false
	b.store.UpdateThread(newest.ThreadID, func(rec *threadRec) {
		before := rec.Snippet
		applySnippet(rec, newest, newest.TimestampMS)
		changed = rec.Snippet != before
	})
	if !changed {
		return
	}
	if view, ok := b.store.ThreadView(newest.ThreadID); ok {
		b.out.Emit(EventThreads, ThreadsEvent{Threads: []Thread{view}})
	}
}

func (b *MetaBackend) fetchLatestPage(ctx context.Context, rec threadRec) bool {
	if rec.E2EE {
		if !b.historyEnrolled() {
			return false
		}
		_, svc, err := b.historyClient()
		if err != nil {
			return false
		}
		_, err = b.fetchHistoryPage(ctx, svc, rec, time.Now().Add(historyFutureSlack).UnixMilli(), historyPageSize)
		return err == nil
	}
	key, err := parseThreadKey(rec.ID)
	if err != nil {
		return false
	}
	b.mu.Lock()
	client := b.client
	connected := b.connState == StateConnected
	b.mu.Unlock()
	if client == nil || !connected {
		return false
	}
	return b.requestHistory(ctx, client, rec.ID, &socket.CreateThreadTask{ThreadFBID: key, SyncGroup: 1})
}

func (b *MetaBackend) fetchOlderPage(ctx context.Context, rec threadRec, beforeMS int64, count int) bool {
	if more, known := b.store.HasMoreBefore(rec.ID); known && !more {
		if _, hasAny := b.store.OldestMessage(rec.ID); hasAny {
			return false
		}
	}
	if rec.E2EE {
		if !b.historyEnrolled() {
			return false
		}
		_, svc, err := b.historyClient()
		if err != nil {
			return false
		}
		if oldest, ok := b.store.OldestMessage(rec.ID); ok && (beforeMS == 0 || oldest.TimestampMS < beforeMS) {
			beforeMS = oldest.TimestampMS
		}
		if beforeMS == 0 {
			beforeMS = time.Now().Add(historyFutureSlack).UnixMilli()
		}
		_, err = b.fetchHistoryPage(ctx, svc, rec, beforeMS, max(count, historyPageSize))
		return err == nil
	}
	key, err := parseThreadKey(rec.ID)
	if err != nil {
		return false
	}
	b.mu.Lock()
	client := b.client
	connected := b.connState == StateConnected
	b.mu.Unlock()
	if client == nil || !connected {
		return false
	}
	return b.fetchHistory(ctx, client, rec.ID, key)
}

func (b *MetaBackend) addFetchWaiter(threadID string) chan struct{} {
	ch := make(chan struct{})
	b.fetchMu.Lock()
	b.fetchWaiters[threadID] = append(b.fetchWaiters[threadID], ch)
	b.fetchMu.Unlock()
	return ch
}

func (b *MetaBackend) removeFetchWaiter(threadID string, ch chan struct{}) {
	b.fetchMu.Lock()
	defer b.fetchMu.Unlock()
	waiters := b.fetchWaiters[threadID]
	for i, w := range waiters {
		if w == ch {
			b.fetchWaiters[threadID] = append(waiters[:i], waiters[i+1:]...)
			break
		}
	}
	if len(b.fetchWaiters[threadID]) == 0 {
		delete(b.fetchWaiters, threadID)
	}
}

func (b *MetaBackend) notifyFetchWaiters(threadID string) {
	b.fetchMu.Lock()
	waiters := b.fetchWaiters[threadID]
	delete(b.fetchWaiters, threadID)
	b.fetchMu.Unlock()
	for _, ch := range waiters {
		close(ch)
	}
}

func (b *MetaBackend) fetchHistory(ctx context.Context, client *messagix.Client, threadID string, key int64) bool {
	var task socket.Task
	if oldest, ok := b.store.OldestMessage(threadID); ok {
		task = &socket.FetchMessagesTask{
			ThreadKey:            key,
			Direction:            0,
			ReferenceTimestampMs: oldest.TimestampMS,
			ReferenceMessageId:   oldest.ID,
			SyncGroup:            1,
			Cursor:               client.GetCursor(1),
		}
	} else {
		task = &socket.CreateThreadTask{ThreadFBID: key, SyncGroup: 1}
	}
	return b.requestHistory(ctx, client, threadID, task)
}

func (b *MetaBackend) requestHistory(ctx context.Context, client *messagix.Client, threadID string, task socket.Task) bool {
	waiter := b.addFetchWaiter(threadID)
	defer b.removeFetchWaiter(threadID, waiter)
	tbl, err := client.ExecuteTasks(ctx, task)
	if err != nil {
		b.log.Err(err).Str("thread_id", threadID).Msg("Failed to request message history")
		return false
	}
	if tbl != nil {
		b.handleTable(tbl, modeHistory)
	}
	timer := time.NewTimer(historyFetchTimeout)
	defer timer.Stop()
	select {
	case <-waiter:
		return true
	case <-timer.C:
		b.log.Debug().Str("thread_id", threadID).Msg("Timed out waiting for message history")
		return false
	case <-ctx.Done():
		return false
	}
}

func abs64(v int64) int64 {
	if v < 0 {
		return -v
	}
	return v
}

const liveMessageWindow = 2 * time.Minute

func (b *MetaBackend) emitMessage(event string, msg Message) {
	b.out.Emit(event, b.store.Decorate(msg))
}

func isLiveTimestamp(ts int64) bool {
	return ts >= time.Now().Add(-liveMessageWindow).UnixMilli()
}

func (b *MetaBackend) recordNewMessage(msg Message, live bool) {
	latest := b.store.LatestTimestamp(msg.ThreadID)
	if _, existed := b.store.PutMessage(msg); existed {
		return
	}
	if live {
		b.applyMessageToThread(msg)
		b.emitMessage(EventMessage, msg)
	} else {
		b.applyHistoryToThread(msg)
		if msg.TimestampMS > latest {
			b.out.Emit(EventBackfilled, MessagesBackfilledEvent{ThreadID: msg.ThreadID, Messages: []Message{msg}})
		}
	}
	if view, ok := b.store.ThreadView(msg.ThreadID); ok {
		b.out.Emit(EventThreads, ThreadsEvent{Threads: []Thread{view}})
	}
}

func applySnippet(rec *threadRec, msg Message, latest int64) {
	rec.LastActivityMS = max(rec.LastActivityMS, msg.TimestampMS)
	if msg.TimestampMS < latest || msg.TimestampMS < rec.SnippetMS {
		return
	}
	if s := snippetFor(msg); s != "" {
		rec.Snippet = s
		rec.SnippetMS = msg.TimestampMS
	}
}

func (b *MetaBackend) applyHistoryToThread(msg Message) {
	if !b.store.HasThread(msg.ThreadID) {
		return
	}
	latest := b.store.LatestTimestamp(msg.ThreadID)
	b.store.UpdateThread(msg.ThreadID, func(rec *threadRec) {
		applySnippet(rec, msg, latest)
		if msg.IsOutgoing {
			rec.LastReadMS = max(rec.LastReadMS, msg.TimestampMS)
			if rec.LastReadMS >= rec.LastActivityMS {
				rec.UnreadCount = 0
			}
		}
	})
}

func (b *MetaBackend) applyMessageToThread(msg Message) {
	if !b.store.HasThread(msg.ThreadID) {
		return
	}
	latest := b.store.LatestTimestamp(msg.ThreadID)
	b.store.UpdateThread(msg.ThreadID, func(rec *threadRec) {
		applySnippet(rec, msg, latest)
		if msg.IsOutgoing {
			rec.UnreadCount = 0
			rec.LastReadMS = max(rec.LastReadMS, msg.TimestampMS)
		} else if msg.TimestampMS > rec.LastReadMS {
			rec.UnreadCount++
		}
	})
}

func (b *MetaBackend) MarkRead(ctx context.Context, threadID string) error {
	if err := b.requireLogin(); err != nil {
		return err
	}
	rec, ok := b.store.Thread(threadID)
	if !ok {
		return newError(CodeNotFound, "unknown thread %s", threadID)
	}
	watermark := max(rec.LastActivityMS, b.store.LatestTimestamp(threadID))
	if watermark == 0 {
		watermark = time.Now().UnixMilli()
	}
	if rec.E2EE {
		if err := b.markReadE2EE(ctx, rec); err != nil {
			return err
		}
	} else if rec.UnreadCount > 0 || rec.MarkedReadMS < watermark {
		key, err := parseThreadKey(threadID)
		if err != nil {
			return err
		}
		if _, err = b.runTasks(ctx, &socket.ThreadMarkReadTask{ThreadId: key, LastReadWatermarkTs: watermark, SyncGroup: syncGroupInbox}); err != nil {
			return err
		}
		b.store.UpdateThread(threadID, func(rec *threadRec) { rec.MarkedReadMS = max(rec.MarkedReadMS, watermark) })
	}
	b.store.UpdateThread(threadID, func(rec *threadRec) {
		rec.UnreadCount = 0
		rec.LastReadMS = max(rec.LastReadMS, watermark)
	})
	if view, ok := b.store.ThreadView(threadID); ok {
		b.out.Emit(EventThreads, ThreadsEvent{Threads: []Thread{view}})
	}
	return nil
}
