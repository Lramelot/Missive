package main

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strconv"
	"time"

	"go.mau.fi/whatsmeow/types"

	"go.mau.fi/mautrix-meta/pkg/messagix/socket"
)

const (
	maxReadReceiptIDs = 100
	afterSendReadWait = 5 * time.Second
)

type noSendReadMark struct{}

var sendCommands = map[string]bool{"send": true, "send_media": true, "send_voice": true, "forward": true}

func sendReadContext(ctx context.Context, req *Request) context.Context {
	if !sendCommands[req.Cmd] {
		return ctx
	}
	var p struct {
		MarkRead *bool `json:"mark_read"`
	}
	if json.Unmarshal(req.Params, &p) == nil && p.MarkRead != nil && !*p.MarkRead {
		return context.WithValue(ctx, noSendReadMark{}, true)
	}
	return ctx
}

func sendMarksRead(ctx context.Context) bool {
	skip, _ := ctx.Value(noSendReadMark{}).(bool)
	return !skip
}

type waReadBatch struct {
	sender types.JID
	ids    []types.MessageID
}

type e2eeReadPlan struct {
	receipts []waReadBatch
	tasks    []*socket.ThreadMarkReadTask
	inferred bool
	readUpTo int64
}

func (s *Store) HybridFBKey(waKey int64) (int64, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.hybridKeyLocked(waKey, true)
}

func (s *Store) ThreadMessages(threadID string) []Message {
	s.mu.RLock()
	defer s.mu.RUnlock()
	sorted := s.sortedMessages(threadID)
	out := make([]Message, len(sorted))
	for i, msg := range sorted {
		out[i] = *msg
	}
	return out
}

func waReadTimestamp(ms int64) int64 {
	if ms%1000 == 0 {
		return ms + 999
	}
	return ms
}

func (b *MetaBackend) planE2EERead(rec threadRec) e2eeReadPlan {
	var plan e2eeReadPlan
	key, _ := strconv.ParseInt(rec.ID, 10, 64)
	fbKey, mapped := b.store.HybridFBKey(key)
	if !mapped && !rec.IsGroup && key != 0 {
		fbKey, mapped, plan.inferred = key, true, true
	}
	marks := make(map[int64]int64)
	var candidates []Message
	for _, msg := range b.store.ThreadMessages(rec.ID) {
		plan.readUpTo = max(plan.readUpTo, msg.TimestampMS)
		if isLSMessageID(msg.ID) {
			threadKey := key
			if mapped {
				threadKey = fbKey
			}
			marks[threadKey] = max(marks[threadKey], msg.TimestampMS)
			continue
		}
		if mapped {
			marks[fbKey] = max(marks[fbKey], waReadTimestamp(msg.TimestampMS))
		}
		if msg.IsOutgoing || msg.TimestampMS <= rec.MetaReadMS {
			continue
		}
		if _, sent := b.readSent.Load(msg.ID); sent {
			continue
		}
		candidates = append(candidates, msg)
	}
	if len(candidates) > maxReadReceiptIDs {
		candidates = candidates[len(candidates)-maxReadReceiptIDs:]
	}
	index := make(map[types.JID]int)
	for _, msg := range candidates {
		var sender types.JID
		if rec.IsGroup {
			sender = types.NewJID(msg.SenderID, types.MessengerServer)
		}
		i, ok := index[sender]
		if !ok {
			i = len(plan.receipts)
			index[sender] = i
			plan.receipts = append(plan.receipts, waReadBatch{sender: sender})
		}
		plan.receipts[i].ids = append(plan.receipts[i].ids, msg.ID)
	}
	if rec.UnreadCount == 0 && rec.MarkedReadMS >= plan.readUpTo {
		return plan
	}
	threadKeys := make([]int64, 0, len(marks))
	for threadKey, watermark := range marks {
		if threadKey != 0 && watermark > 0 {
			threadKeys = append(threadKeys, threadKey)
		}
	}
	slices.SortFunc(threadKeys, cmp.Compare[int64])
	for _, threadKey := range threadKeys {
		syncGroup := int64(syncGroupInbox)
		if mapped && threadKey == fbKey {
			syncGroup = syncGroupE2EE
		}
		plan.tasks = append(plan.tasks, &socket.ThreadMarkReadTask{
			ThreadId:            threadKey,
			LastReadWatermarkTs: marks[threadKey],
			SyncGroup:           syncGroup,
		})
	}
	return plan
}

func (b *MetaBackend) sendWAReadReceipt(ctx context.Context, chat, sender types.JID, ids []types.MessageID) error {
	if b.waReadFn != nil {
		return b.waReadFn(ctx, chat, sender, ids)
	}
	cli, err := b.waitE2EE(ctx)
	if err != nil {
		return err
	}
	return cli.MarkRead(ctx, ids, time.Now(), chat, sender)
}

func (b *MetaBackend) markReadE2EE(ctx context.Context, rec threadRec) error {
	plan := b.planE2EERead(rec)
	chat := chatJIDFor(rec)
	var errs []error
	for _, batch := range plan.receipts {
		if err := b.sendWAReadReceipt(ctx, chat, batch.sender, batch.ids); err != nil {
			errs = append(errs, err)
			continue
		}
		for _, id := range batch.ids {
			b.readSent.Store(id, struct{}{})
		}
	}
	marked := len(plan.tasks) > 0
	for _, task := range plan.tasks {
		if _, err := b.runTasks(ctx, task); err != nil {
			marked = false
			if plan.inferred {
				b.log.Debug().Err(err).Int64("sync_group", task.SyncGroup).Msg("Mark-read on the inferred Facebook key was not accepted")
				continue
			}
			errs = append(errs, err)
		}
	}
	if marked {
		b.store.UpdateThread(rec.ID, func(r *threadRec) {
			r.MarkedReadMS = max(r.MarkedReadMS, plan.readUpTo)
		})
	}
	if len(errs) > 0 {
		return newError(CodeInternal, "failed to mark encrypted chat as read: %v", errors.Join(errs...))
	}
	return nil
}

func (b *MetaBackend) markReadAfterSend(ctx context.Context, threadID string) {
	if !sendMarksRead(ctx) {
		return
	}
	rec, ok := b.store.Thread(threadID)
	if !ok || !rec.E2EE {
		return
	}
	b.queueAfterSend(threadID, func() {
		ctx, cancel := context.WithTimeout(context.Background(), afterSendReadWait)
		defer cancel()
		current, ok := b.store.Thread(threadID)
		if !ok {
			return
		}
		if err := b.markReadE2EE(ctx, current); err != nil {
			b.log.Warn().Err(err).Msg("Failed to mark the encrypted chat read after sending")
		}
	})
}

func (b *MetaBackend) queueAfterSend(threadID string, job func()) {
	done := make(chan struct{})
	b.afterSendMu.Lock()
	if b.afterSendTail == nil {
		b.afterSendTail = make(map[string]chan struct{})
	}
	prev := b.afterSendTail[threadID]
	b.afterSendTail[threadID] = done
	b.afterSendMu.Unlock()
	go func() {
		if prev != nil {
			<-prev
		}
		job()
		close(done)
		b.afterSendMu.Lock()
		if b.afterSendTail[threadID] == done {
			delete(b.afterSendTail, threadID)
		}
		b.afterSendMu.Unlock()
	}()
}
