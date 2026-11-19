package main

import (
	"context"
	"testing"
	"time"
)

func TestBackfillDroppedAfterLogout(t *testing.T) {
	b, buf, _ := loggedInBackend(t)
	b.store.UpdateThread("5", func(rec *threadRec) { rec.IsGroup = true })
	b.store.PutMessage(Message{ID: "c", ThreadID: "5", TimestampMS: 5000})
	release := make(chan struct{})
	b.olderPageFn = func(ctx context.Context, rec threadRec, beforeMS int64, count int) bool {
		<-release
		b.store.PutMessage(Message{ID: "old", ThreadID: rec.ID, TimestampMS: 1000})
		return true
	}
	if _, err := b.Messages(context.Background(), "5", 0, 10, true); err != nil {
		t.Fatal(err)
	}
	b.mu.Lock()
	b.gen++
	b.mu.Unlock()
	close(release)
	waitFor(t, 5*time.Second, func() bool {
		_, running := b.backfills.Load("5|older")
		return !running
	})
	if len(eventsOf(t, buf, EventBackfilled)) != 0 {
		t.Fatal("a backfill started before logout must not emit")
	}
}
