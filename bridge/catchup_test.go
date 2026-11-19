package main

import (
	"context"
	"encoding/json"
	"slices"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"go.mau.fi/mautrix-meta/pkg/messagix/table"
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

func backfilledIDs(t *testing.T, buf *syncBuffer) []string {
	t.Helper()
	var ids []string
	for _, raw := range eventsOf(t, buf, EventBackfilled) {
		var event MessagesBackfilledEvent
		if err := json.Unmarshal(raw, &event); err != nil {
			t.Fatal(err)
		}
		for _, msg := range event.Messages {
			ids = append(ids, msg.ID)
		}
	}
	slices.Sort(ids)
	return ids
}

func TestMessagesMissedDuringSleepReachTheApp(t *testing.T) {
	b, buf, _ := loggedInBackend(t)
	now := time.Now().UnixMilli()
	b.store.PutMessage(Message{ID: "seen", ThreadID: "5", TimestampMS: now - 3_600_000})
	b.lsCatchingUp.Store(true)
	b.handleTable(&table.LSTable{LSInsertMessage: []*table.LSInsertMessage{
		{ThreadKey: 5, MessageId: "mid.$asleep", TimestampMs: now - 60_000, SenderId: 7, Text: "pendant la veille"},
		{ThreadKey: 5, MessageId: "mid.$ancient", TimestampMs: now - 7_200_000, SenderId: 7, Text: "vieux"},
	}}, modeLive)
	b.lsCatchingUp.Store(false)
	if n := len(eventsOf(t, buf, EventMessage)); n != 0 {
		t.Fatalf("catch-up messages must not be announced as live, got %d", n)
	}
	if got := backfilledIDs(t, buf); !slices.Equal(got, []string{"mid.$asleep"}) {
		t.Fatalf("a message newer than the cache must be pushed to the app, got %v", got)
	}

	b.handleTable(&table.LSTable{
		LSInsertNewMessageRange: []*table.LSInsertNewMessageRange{{ThreadKey: 5, MinTimestampMs: now - 30_000, MaxTimestampMs: now - 30_000, HasMoreBefore: true}},
		LSUpsertMessage: []*table.LSUpsertMessage{
			{ThreadKey: 5, MessageId: "mid.$synced", TimestampMs: now - 30_000, SenderId: 7, Text: "resync"},
		},
	}, modeLive)
	if got := backfilledIDs(t, buf); !slices.Equal(got, []string{"mid.$asleep", "mid.$synced"}) {
		t.Fatalf("an upserted recent message must be pushed to the app, got %v", got)
	}

	b.recordNewMessage(Message{ID: "wa-offline", ThreadID: "5", TimestampMS: now - 10_000, Text: "chiffré"}, false)
	b.recordNewMessage(Message{ID: "wa-old", ThreadID: "5", TimestampMS: now - 9_000_000, Text: "archive"}, false)
	if got := backfilledIDs(t, buf); !slices.Equal(got, []string{"mid.$asleep", "mid.$synced", "wa-offline"}) {
		t.Fatalf("only an offline encrypted message newer than the cache must be pushed, got %v", got)
	}
}

func TestReconnectCatchesUpStaleThreads(t *testing.T) {
	b, _, _ := loggedInBackend(t)
	b.store.PutMessage(Message{ID: "a", ThreadID: "5", TimestampMS: 1_000_000})
	b.store.UpdateThread("5", func(rec *threadRec) { rec.LastActivityMS = 2_000_000 })
	b.store.PutMessage(Message{ID: "b", ThreadID: "6", TimestampMS: 1_000_000})
	b.store.UpdateThread("6", func(rec *threadRec) { rec.LastActivityMS = 1_000_000 })
	b.store.UpdateThread("7", func(rec *threadRec) { rec.LastActivityMS = 3_000_000 })
	var fetched sync.Map
	b.latestPageFn = func(ctx context.Context, rec threadRec) bool {
		fetched.Store(rec.ID, true)
		return true
	}
	b.catchUpStaleThreads()
	waitFor(t, 5*time.Second, func() bool {
		_, ok := fetched.Load("5")
		return ok
	})
	for _, id := range []string{"6", "7"} {
		if _, ok := fetched.Load(id); ok {
			t.Fatalf("thread %s is up to date or was never opened and must not be fetched", id)
		}
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
	if !idle() || calls.Load() != 1 {
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
	if !idle() || calls.Load() != 2 {
		t.Fatalf("activity without new messages must be fetched once, got %d calls", calls.Load())
	}
	if _, err := b.Messages(ctx, "5", 1_500_000, 1, true); err != nil {
		t.Fatal(err)
	}
	if !idle() || calls.Load() != 2 {
		t.Fatal("loading older pages must not trigger a catch-up")
	}
	if len(eventsOf(t, buf, EventBackfilled)) != 1 {
		t.Fatal("a catch-up that finds nothing must not emit")
	}
}

func TestCatchUpAndStaleMessagesAreHistory(t *testing.T) {
	b, buf, _ := loggedInBackend(t)
	now := time.Now()
	b.lsCatchingUp.Store(true)
	b.handleTable(&table.LSTable{LSInsertMessage: []*table.LSInsertMessage{{ThreadKey: 5, MessageId: "c1", TimestampMs: now.UnixMilli(), SenderId: 7}}}, modeLive)
	b.lsCatchingUp.Store(false)
	b.handleTable(&table.LSTable{LSInsertMessage: []*table.LSInsertMessage{{ThreadKey: 5, MessageId: "c2", TimestampMs: now.Add(-5 * time.Minute).UnixMilli(), SenderId: 7}}}, modeLive)
	if n := len(eventsOf(t, buf, EventMessage)); n != 0 {
		t.Fatalf("catch-up and stale messages must not notify, got %d", n)
	}
	b.handleTable(&table.LSTable{LSInsertMessage: []*table.LSInsertMessage{{ThreadKey: 5, MessageId: "c3", TimestampMs: now.UnixMilli(), SenderId: 7}}}, modeLive)
	if n := len(eventsOf(t, buf, EventMessage)); n != 1 {
		t.Fatalf("live message must notify, got %d", n)
	}
}
