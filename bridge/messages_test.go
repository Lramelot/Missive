package main

import (
	"context"
	"encoding/json"
	"fmt"
	"sync/atomic"
	"testing"
	"time"
)

func TestMessagesAreServedFromCacheFirst(t *testing.T) {
	b, buf, _ := loggedInBackend(t)
	b.store.UpdateThread("5", func(rec *threadRec) { rec.IsGroup = true })
	for i := 0; i < 3; i++ {
		b.store.PutMessage(Message{ID: fmt.Sprintf("c%d", i), ThreadID: "5", TimestampMS: int64(10_000 + i*1000)})
	}
	release := make(chan struct{})
	var calls atomic.Int32
	b.olderPageFn = func(ctx context.Context, rec threadRec, beforeMS int64, count int) bool {
		calls.Add(1)
		select {
		case <-release:
		case <-ctx.Done():
			return false
		}
		if calls.Load() > 1 {
			return false
		}
		for i := 0; i < 4; i++ {
			b.store.PutMessage(Message{ID: fmt.Sprintf("o%d", i), ThreadID: rec.ID, TimestampMS: int64(1000 + i*100)})
		}
		return true
	}
	ctx := context.Background()
	started := time.Now()
	msgs, err := b.Messages(ctx, "5", 0, 10, true)
	if err != nil || len(msgs) != 3 {
		t.Fatalf("cached messages not returned: %v %v", msgs, err)
	}
	if again, _ := b.Messages(ctx, "5", 0, 10, true); len(again) != 3 {
		t.Fatal("second call must also be cache-first")
	}
	if elapsed := time.Since(started); elapsed > 200*time.Millisecond {
		t.Fatalf("messages blocked on Meta for %v", elapsed)
	}
	if _, err := b.Messages(ctx, "5", 0, 10, false); err != nil {
		t.Fatal(err)
	}
	waitFor(t, time.Second, func() bool { return calls.Load() == 1 })
	time.Sleep(50 * time.Millisecond)
	if calls.Load() != 1 {
		t.Fatalf("backfill must be single-flight, got %d fetches", calls.Load())
	}
	if len(eventsOf(t, buf, EventBackfilled)) != 0 {
		t.Fatal("no backfill event before Meta answered")
	}
	close(release)
	waitFor(t, 5*time.Second, func() bool { return len(eventsOf(t, buf, EventBackfilled)) == 1 })
	var ev MessagesBackfilledEvent
	if err := json.Unmarshal(eventsOf(t, buf, EventBackfilled)[0], &ev); err != nil {
		t.Fatal(err)
	}
	if ev.ThreadID != "5" || len(ev.Messages) != 4 {
		t.Fatalf("unexpected backfill event %+v", ev)
	}
	for i, m := range ev.Messages {
		if m.ID != fmt.Sprintf("o%d", i) || m.Reactions == nil {
			t.Fatalf("backfill must carry only the new older messages, oldest first: %+v", ev.Messages)
		}
	}
	if len(eventsOf(t, buf, EventMessage)) != 0 {
		t.Fatal("backfilled history must not be emitted as live messages")
	}
	if msgs, _ := b.Messages(ctx, "5", 0, 10, false); len(msgs) != 7 {
		t.Fatalf("store must hold the backfilled messages, got %d", len(msgs))
	}
}
