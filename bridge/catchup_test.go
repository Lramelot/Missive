package main

import (
	"context"
	"encoding/json"
	"sync/atomic"
	"testing"
	"time"
)

func TestSnippetFollowsNewestStoredMessage(t *testing.T) {
	b, _, _ := loggedInBackend(t)
	b.store.PutMessage(Message{ID: "old", ThreadID: "5", TimestampMS: 1_000, Text: "hier", IsOutgoing: true})
	b.store.UpdateThread("5", func(rec *threadRec) {
		rec.Snippet = "hier"
		rec.SnippetMS = 1_000
		rec.LastActivityMS = 2_000
	})
	snippet := func() string {
		rec, _ := b.store.Thread("5")
		return rec.Snippet
	}

	b.recordNewMessage(Message{ID: "today", ThreadID: "5", TimestampMS: 1_900, Text: "ce matin"}, false)
	if got := snippet(); got != "ce matin" {
		t.Fatalf("a message newer than the snippet must replace it even before last activity, got %q", got)
	}
	b.recordNewMessage(Message{ID: "ancient", ThreadID: "5", TimestampMS: 500, Text: "il y a longtemps"}, false)
	if got := snippet(); got != "ce matin" {
		t.Fatalf("an older history message must not replace the snippet, got %q", got)
	}
	b.recordNewMessage(Message{ID: "live", ThreadID: "5", TimestampMS: 3_000, Text: "à l'instant"}, true)
	if got := snippet(); got != "à l'instant" {
		t.Fatalf("a live message must replace the snippet, got %q", got)
	}
	if rec, _ := b.store.Thread("5"); rec.LastActivityMS != 3_000 {
		t.Fatalf("last activity must follow the newest message, got %d", rec.LastActivityMS)
	}
}

func TestOpeningAThreadRepairsAStaleSnippet(t *testing.T) {
	b, buf, _ := loggedInBackend(t)
	b.store.PutMessage(Message{ID: "today", ThreadID: "5", TimestampMS: 1_900, Text: "ce matin"})
	b.store.UpdateThread("5", func(rec *threadRec) {
		rec.Snippet = "hier"
		rec.SnippetMS = 1_000
		rec.LastActivityMS = 1_900
	})
	if _, err := b.Messages(context.Background(), "5", 0, 10, false); err != nil {
		t.Fatal(err)
	}
	if rec, _ := b.store.Thread("5"); rec.Snippet != "ce matin" {
		t.Fatalf("opening the thread must repair the snippet, got %q", rec.Snippet)
	}
	if len(eventsOf(t, buf, EventThreads)) == 0 {
		t.Fatal("a repaired snippet must be pushed to the app")
	}
	before := len(eventsOf(t, buf, EventThreads))
	if _, err := b.Messages(context.Background(), "5", 0, 10, false); err != nil {
		t.Fatal(err)
	}
	if len(eventsOf(t, buf, EventThreads)) != before {
		t.Fatal("an unchanged snippet must not be re-emitted")
	}
}

func TestCatchUpFetchesMessagesNewerThanCache(t *testing.T) {
	b, buf, _ := loggedInBackend(t)
	ctx := context.Background()
	b.store.PutMessage(Message{ID: "a", ThreadID: "5", TimestampMS: 1_000_000})
	b.store.UpdateThread("5", func(rec *threadRec) { rec.LastActivityMS = 2_000_000 })
	var calls atomic.Int32
	var addNew atomic.Bool
	addNew.Store(true)
	b.latestPageFn = func(ctx context.Context, rec threadRec) bool {
		calls.Add(1)
		if addNew.Load() {
			b.store.PutMessage(Message{ID: "new", ThreadID: rec.ID, TimestampMS: 2_000_000})
		}
		return true
	}
	idle := func() bool {
		_, running := b.backfills.Load("5|latest")
		return !running
	}

	if _, err := b.Messages(ctx, "5", 0, 1, true); err != nil {
		t.Fatal(err)
	}
	waitFor(t, 5*time.Second, func() bool { return len(eventsOf(t, buf, EventBackfilled)) == 1 })
	var event MessagesBackfilledEvent
	if err := json.Unmarshal(eventsOf(t, buf, EventBackfilled)[0], &event); err != nil {
		t.Fatal(err)
	}
	if len(event.Messages) != 1 || event.Messages[0].ID != "new" {
		t.Fatalf("catch-up must emit only the missing recent message, got %+v", event.Messages)
	}
	waitFor(t, 5*time.Second, idle)

	if _, err := b.Messages(ctx, "5", 0, 1, true); err != nil {
		t.Fatal(err)
	}
	time.Sleep(100 * time.Millisecond)
	if calls.Load() != 1 {
		t.Fatalf("an up-to-date cache must not trigger a fetch, got %d calls", calls.Load())
	}

	addNew.Store(false)
	b.store.UpdateThread("5", func(rec *threadRec) { rec.LastActivityMS = 3_000_000 })
	if _, err := b.Messages(ctx, "5", 0, 1, true); err != nil {
		t.Fatal(err)
	}
	waitFor(t, 5*time.Second, func() bool { return calls.Load() == 2 && idle() })
	if _, err := b.Messages(ctx, "5", 0, 1, true); err != nil {
		t.Fatal(err)
	}
	time.Sleep(100 * time.Millisecond)
	if calls.Load() != 2 {
		t.Fatalf("activity without new messages must be fetched once, got %d calls", calls.Load())
	}
	if _, err := b.Messages(ctx, "5", 1_500_000, 1, true); err != nil {
		t.Fatal(err)
	}
	time.Sleep(100 * time.Millisecond)
	if calls.Load() != 2 {
		t.Fatal("loading older pages must not trigger a catch-up")
	}
	if len(eventsOf(t, buf, EventBackfilled)) != 1 {
		t.Fatal("a catch-up that finds nothing must not emit")
	}
}
