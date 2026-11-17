package main

import (
	"context"
	"math"
	"strconv"
	"time"
)

const (
	backfillRounds         = 3
	backfillDelay          = 400 * time.Millisecond
	backfillTimeout        = 90 * time.Second
	maxConcurrentBackfills = 2
)

func (b *MetaBackend) startBackfill(rec threadRec, beforeMS int64, want int) {
	key := rec.ID + "|" + strconv.FormatInt(beforeMS, 10)
	if _, running := b.backfills.LoadOrStore(key, true); running {
		return
	}
	gen := b.generation()
	go func() {
		defer b.backfills.Delete(key)
		b.backfillSem <- struct{}{}
		defer func() { <-b.backfillSem }()
		ctx, cancel := context.WithTimeout(context.Background(), backfillTimeout)
		defer cancel()
		b.runBackfill(ctx, rec, beforeMS, want, gen)
	}()
}

func (b *MetaBackend) runBackfill(ctx context.Context, rec threadRec, beforeMS int64, want int, gen uint64) {
	known := b.store.MessageIDs(rec.ID)
	ceiling := beforeMS
	if ceiling == 0 {
		ceiling = math.MaxInt64
		if oldest, ok := b.store.OldestMessage(rec.ID); ok {
			ceiling = oldest.TimestampMS + 1
		}
	}
	found := 0
	for round := 0; round < backfillRounds && found < want; round++ {
		if round > 0 && !sleepCtx(ctx, backfillDelay) {
			break
		}
		before := b.store.MessageCount(rec.ID)
		fetch := b.olderPageFn
		if fetch == nil {
			fetch = b.fetchOlderPage
		}
		if !fetch(ctx, rec, beforeMS, want-found) {
			break
		}
		added := b.store.MessageCount(rec.ID) - before
		if added <= 0 {
			break
		}
		found += added
	}
	if b.generation() != gen {
		return
	}
	fresh := b.store.MessagesNotIn(rec.ID, ceiling, known)
	if len(fresh) == 0 {
		return
	}
	b.out.Emit(EventBackfilled, MessagesBackfilledEvent{ThreadID: rec.ID, Messages: fresh})
}
