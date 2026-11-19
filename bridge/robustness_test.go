package main

import (
	"context"
	"errors"
	"fmt"
	"math"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/rs/zerolog"
	"go.mau.fi/mautrix-meta/pkg/messagix"
	"go.mau.fi/mautrix-meta/pkg/messagix/cookies"
	"go.mau.fi/mautrix-meta/pkg/messagix/httpclient"
	"go.mau.fi/mautrix-meta/pkg/messagix/table"
	"go.mau.fi/mautrix-meta/pkg/messagix/types"
	waTypes "go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
)

type panicEmitter struct{}

func (panicEmitter) Emit(event string, data any) {
	panic(fmt.Sprintf("secret message text %s", event))
}

type recordingEmitter struct {
	mu     sync.Mutex
	events []ConnectionEvent
}

func (r *recordingEmitter) Emit(event string, data any) {
	if c, ok := data.(ConnectionEvent); ok && event == EventConnection {
		r.mu.Lock()
		r.events = append(r.events, c)
		r.mu.Unlock()
	}
}

func (r *recordingEmitter) snapshot() []ConnectionEvent {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]ConnectionEvent(nil), r.events...)
}

func capturingLog(buf *syncBuffer) zerolog.Logger {
	return zerolog.New(buf)
}

func assertRecovered(t *testing.T, logs *syncBuffer, where string) {
	t.Helper()
	out := strings.Join(logs.Lines(), "\n")
	if !strings.Contains(out, "Recovered from panic") || !strings.Contains(out, `"where":"`+where+`"`) {
		t.Fatalf("the panic in %s must be logged, got %s", where, out)
	}
	if strings.Contains(out, "secret message text") {
		t.Fatalf("a recovered panic must not log message content: %s", out)
	}
}

func TestTableHandlerRecoversFromPanic(t *testing.T) {
	b, _, _ := loggedInBackend(t)
	logs := &syncBuffer{}
	b.log = capturingLog(logs)
	b.out = panicEmitter{}
	b.handleTable(&table.LSTable{LSDeleteThenInsertThread: []*table.LSDeleteThenInsertThread{
		{ThreadKey: 5, ThreadType: table.ONE_TO_ONE, LastActivityTimestampMs: 1},
	}}, modeLive)
	assertRecovered(t, logs, "table")
	if !b.sessionMu.TryLock() {
		t.Fatal("the session lock must be released after a recovered panic")
	}
	b.sessionMu.Unlock()
}

func TestWAEventHandlerRecoversFromPanic(t *testing.T) {
	b, _, _ := loggedInBackend(t)
	logs := &syncBuffer{}
	b.log = capturingLog(logs)
	b.out = panicEmitter{}
	peer := waTypes.NewJID(testPeerID, waTypes.MessengerServer)
	b.handleWAEvent(context.Background(), nil, nil, &events.ChatPresence{
		MessageSource: waTypes.MessageSource{Chat: peer, Sender: peer},
		State:         waTypes.ChatPresenceComposing,
	})
	assertRecovered(t, logs, "wa_event")
}

func TestMetaEventHandlerRecoversFromPanic(t *testing.T) {
	b, _, _ := loggedInBackend(t)
	logs := &syncBuffer{}
	b.log = capturingLog(logs)
	b.out = panicEmitter{}
	client := messagix.NewClient(&cookies.Cookies{Platform: types.Messenger}, zerolog.Nop(), b.messagixConfig())
	b.mu.Lock()
	b.client = client
	b.mu.Unlock()
	b.handleMetaEvent(client, context.Background(), &messagix.ConnectedEvent{})
	assertRecovered(t, logs, "meta_event")
}

func TestGoSafeRecoversDetachedGoroutines(t *testing.T) {
	logs := &syncBuffer{}
	done := make(chan struct{})
	goSafe(capturingLog(logs), "detached", func() {
		defer close(done)
		var m map[string]int
		m["secret message text"]++
	})
	<-done
	waitFor(t, 5*time.Second, func() bool { return len(logs.Lines()) > 0 })
	assertRecovered(t, logs, "detached")
	if out := strings.Join(logs.Lines(), "\n"); !strings.Contains(out, "assignment to entry in nil map") {
		t.Fatalf("a runtime error must be described: %s", out)
	}
}

func TestMentionSpansDoNotOverflow(t *testing.T) {
	if start, length, ok := byteSpanFromUTF16("héllo", 1, math.MaxInt); !ok || start != 1 || length != len("éllo") {
		t.Fatalf("a huge length must be clamped to the text, got %d %d %v", start, length, ok)
	}
	if _, _, ok := byteSpanFromUTF16("héllo", math.MaxInt, math.MaxInt); ok {
		t.Fatal("an offset past the text must be rejected")
	}
	if _, _, ok := utf16SpanFromBytes("hello", 2, math.MaxInt); ok {
		t.Fatal("a byte span overflowing the text must be rejected")
	}
}

func TestConnectBackoffIsCappedWithJitter(t *testing.T) {
	for attempt := 1; attempt < 40; attempt++ {
		base := min(time.Second<<min(attempt, 9), maxConnectBackoff)
		for _, rnd := range []float64{0, 0.5, 0.999} {
			d := connectBackoff(attempt, rnd)
			if d > maxConnectBackoff || d < base*8/10 {
				t.Fatalf("attempt %d rnd %v: %v outside [%v, %v]", attempt, rnd, d, base*8/10, maxConnectBackoff)
			}
		}
	}
	if connectBackoff(1, 0) == connectBackoff(1, 0.999) {
		t.Fatal("the backoff must be jittered")
	}
	if got := connectBackoff(30, 0); got != 4*time.Minute {
		t.Fatalf("the capped backoff keeps a downward jitter, got %v", got)
	}
}

func TestConnectLoopRetriesTransientErrorsWithoutLimit(t *testing.T) {
	b, _, _ := loggedInBackend(t)
	rec := &recordingEmitter{}
	b.out = rec
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var calls int
	var waits []time.Duration
	b.sleepFn = func(ctx context.Context, d time.Duration) bool {
		waits = append(waits, d)
		return ctx.Err() == nil
	}
	b.connectFn = func(ctx context.Context) error {
		calls++
		if calls == 25 {
			cancel()
		}
		return errors.New("dial tcp: network is unreachable")
	}
	b.connectLoop(ctx)
	if calls != 25 {
		t.Fatalf("transient errors must be retried without limit, got %d attempts", calls)
	}
	for _, w := range waits {
		if w > maxConnectBackoff {
			t.Fatalf("a retry waits at most %v, got %v", maxConnectBackoff, w)
		}
	}
	retries := 0
	for _, evt := range rec.snapshot() {
		if evt.State == StateError {
			t.Fatalf("a transient error must not be reported as an error state: %+v", evt)
		}
		if evt.State == StateConnecting && strings.HasPrefix(evt.Detail, "retry ") {
			retries++
		}
	}
	if retries != 24 || rec.snapshot()[len(rec.snapshot())-1].Detail != "retry 24" {
		t.Fatalf("each retry must be announced as connecting with its number, got %+v", rec.snapshot())
	}
}

func TestConnectLoopStopsOnPermanentError(t *testing.T) {
	b, _, _ := loggedInBackend(t)
	rec := &recordingEmitter{}
	b.out = rec
	calls := 0
	b.sleepFn = func(ctx context.Context, d time.Duration) bool { return true }
	b.connectFn = func(ctx context.Context) error {
		calls++
		return httpclient.ErrCheckpointRequired
	}
	b.connectLoop(context.Background())
	events := rec.snapshot()
	if calls != 1 || len(events) != 2 || events[1].State != StateError || !events[1].Permanent {
		t.Fatalf("a permanent error must stop at once and be reported: %d calls, %+v", calls, events)
	}
}

func TestE2EERetryNeverGivesUp(t *testing.T) {
	b, _, _ := loggedInBackend(t)
	rec := &recordingEmitter{}
	b.out = rec
	waited := make(chan time.Duration, 1)
	b.sleepFn = func(ctx context.Context, d time.Duration) bool {
		waited <- d
		return false
	}
	b.e2eeRetries.Store(5)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	b.scheduleE2EERetry(ctx, nil)
	select {
	case d := <-waited:
		if d > maxE2EEBackoff || d < time.Minute {
			t.Fatalf("the sixth retry must wait a capped, growing delay, got %v", d)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the E2EE retry must still be scheduled after many attempts")
	}
	if events := rec.snapshot(); len(events) != 0 {
		t.Fatalf("encrypted chats must not be given up after repeated failures: %+v", events)
	}
	for attempt := int32(1); attempt < 60; attempt++ {
		if d := e2eeBackoff(attempt, 0.999); d > maxE2EEBackoff {
			t.Fatalf("attempt %d waits %v, more than %v", attempt, d, maxE2EEBackoff)
		}
	}
}

func TestBackfillOneInFlightPerThread(t *testing.T) {
	b, _, _ := loggedInBackend(t)
	b.store.UpdateThread("5", func(rec *threadRec) {})
	rec, _ := b.store.Thread("5")
	release := make(chan struct{})
	var calls atomic.Int32
	b.olderPageFn = func(ctx context.Context, rec threadRec, beforeMS int64, count int) bool {
		calls.Add(1)
		<-release
		return false
	}
	b.startBackfill(rec, 0, 10)
	b.startBackfill(rec, 5000, 10)
	b.startBackfill(rec, 9000, 10)
	waitFor(t, 5*time.Second, func() bool { return calls.Load() == 1 })
	close(release)
	waitFor(t, 5*time.Second, func() bool {
		_, running := b.backfills.Load("5|older")
		return !running
	})
	if calls.Load() != 1 {
		t.Fatalf("only one backfill may run per thread, got %d", calls.Load())
	}
}

func TestBackfillWaitingForASlotStopsOnClose(t *testing.T) {
	b, _, _ := loggedInBackend(t)
	b.store.UpdateThread("5", func(rec *threadRec) {})
	rec, _ := b.store.Thread("5")
	for i := 0; i < cap(b.backfillSem); i++ {
		b.backfillSem <- struct{}{}
	}
	var calls atomic.Int32
	b.olderPageFn = func(ctx context.Context, rec threadRec, beforeMS int64, count int) bool {
		calls.Add(1)
		return false
	}
	b.startBackfill(rec, 0, 10)
	b.Close()
	waitFor(t, 5*time.Second, func() bool {
		_, running := b.backfills.Load("5|older")
		return !running
	})
	if calls.Load() != 0 {
		t.Fatal("a backfill still waiting for a slot must give up when the backend closes")
	}
}

func TestLogoutCancelsDetachedWork(t *testing.T) {
	b, _, _ := loggedInBackend(t)
	life := b.lifeCtx()
	if err := b.Logout(context.Background()); err != nil {
		t.Fatal(err)
	}
	if life.Err() == nil {
		t.Fatal("logout must cancel the work started for the previous session")
	}
	if b.lifeCtx().Err() != nil {
		t.Fatal("a new session must get a fresh context after logout")
	}
	b.Close()
	if b.lifeCtx().Err() == nil {
		t.Fatal("close must cancel the backend context")
	}
}

func TestInvalidatedSessionCancelsDetachedWork(t *testing.T) {
	b, _, _ := loggedInBackend(t)
	life := b.lifeCtx()
	b.invalidateSession()
	if life.Err() == nil {
		t.Fatal("an invalidated session must cancel the work started for it")
	}
	if b.lifeCtx().Err() != nil {
		t.Fatal("the next login must get a fresh context after an invalidation")
	}
}

func TestTeardownKeepsTheEventHandler(t *testing.T) {
	b, _, _ := loggedInBackend(t)
	client := messagix.NewClient(&cookies.Cookies{Platform: types.Messenger}, zerolog.Nop(), b.messagixConfig())
	client.SetEventHandler(func(ctx context.Context, evt any) { b.handleMetaEvent(client, ctx, evt) })
	b.mu.Lock()
	b.client = client
	b.mu.Unlock()
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < 200; i++ {
			client.HandleEvent(context.Background(), &table.LSTable{})
		}
	}()
	b.teardown(false)
	wg.Wait()
	client.HandleEvent(context.Background(), &table.LSTable{LSDeleteThenInsertThread: []*table.LSDeleteThenInsertThread{
		{ThreadKey: 42, ThreadType: table.ONE_TO_ONE, LastActivityTimestampMs: 1},
	}})
	if b.store.HasThread("42") {
		t.Fatal("events from a torn down client must be ignored")
	}
}
