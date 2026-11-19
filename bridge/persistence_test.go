package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/rs/zerolog"
	"go.mau.fi/mautrix-meta/pkg/messagix/table"
)

func TestStoreSaveWritesOnlyChanges(t *testing.T) {
	path := filepath.Join(t.TempDir(), cacheFile)
	s := NewStore()
	s.SetContact("7", "Camille", "", "")
	if err := s.Save(path); err != nil {
		t.Fatal(err)
	}
	data, err := readSecretFile(path, zerolog.Nop())
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), `"v":1`) {
		t.Fatalf("the cache must carry its format version: %s", data)
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := s.Save(path); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("an unchanged store must not be written again")
	}
	s.SetContact("8", "Théo", "", "")
	if err := s.Save(path); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatal("a changed store must be written")
	}
}

func TestStoreLoadsSnapshotWithoutVersion(t *testing.T) {
	path := filepath.Join(t.TempDir(), cacheFile)
	legacy := `{"self_id":"999","threads":{"5":{"id":"5","name":"Old"}},"messages":{"5":[{"id":"m1","text":"hi","timestamp_ms":1}]},"contacts":{},"has_more_before":{},"hybrid":{}}`
	if err := writeSecretFile(path, []byte(legacy)); err != nil {
		t.Fatal(err)
	}
	s := NewStore()
	if err := s.Load(path); err != nil {
		t.Fatal(err)
	}
	if m, ok := s.FindMessage("m1"); !ok || m.Text != "hi" {
		t.Fatal("a snapshot without version must still load")
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := s.Save(path); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("a freshly loaded store has nothing to save")
	}
}

func TestStoreSnapshotIsIndependentOfLiveState(t *testing.T) {
	s := NewStore()
	s.UpdateThread("5", func(rec *threadRec) {
		rec.Members = map[string]bool{"7": true}
		rec.Pinned = []pinRec{{MessageID: "m1", PinnedMS: 1}}
	})
	s.PutMessage(Message{ID: "m1", ThreadID: "5", Text: "a", TimestampMS: 1, Attachments: []Attachment{{ID: "x", Kind: "image"}}})
	s.PutMedia("m1", "x", mediaRec{Kind: "image", Path: "old"})
	s.SetNickname("5", "7", "Cam")
	s.SetReaction("m1", "7", "👍", 1)
	s.mu.RLock()
	snap := s.snapshotLocked()
	s.mu.RUnlock()
	s.UpdateThread("5", func(rec *threadRec) {
		rec.Members["8"] = true
		rec.Pinned[0].PinnedMS = 2
	})
	s.SetMediaPath("m1", "x", "new", "")
	s.SetNickname("5", "7", "Camille")
	s.SetReaction("m1", "8", "❤️", 2)
	if len(snap.Threads["5"].Members) != 1 || snap.Threads["5"].Pinned[0].PinnedMS != 1 {
		t.Fatal("thread maps and slices must be copied before serializing")
	}
	if snap.Media[mediaKey("m1", "x")].Path != "old" || snap.Nicknames["5"]["7"] != "Cam" || len(snap.Reactions["m1"]) != 1 {
		t.Fatal("media, nicknames and reactions must be copied before serializing")
	}
}

func TestPersistWritesSessionOnlyWhenChanged(t *testing.T) {
	b, _, dir := loggedInBackend(t)
	b.persist()
	session, cookiesPath := filepath.Join(dir, sessionFile), filepath.Join(dir, cookiesFile)
	for _, p := range []string{session, cookiesPath} {
		if _, err := os.Stat(p); err != nil {
			t.Fatalf("first persist must write %s", filepath.Base(p))
		}
		if err := os.Remove(p); err != nil {
			t.Fatal(err)
		}
	}
	b.persist()
	for _, p := range []string{session, cookiesPath} {
		if _, err := os.Stat(p); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("unchanged %s must not be rewritten", filepath.Base(p))
		}
	}
	b.mu.Lock()
	b.sess.User.Name = "Renamed"
	b.mu.Unlock()
	b.persist()
	if _, err := os.Stat(session); err != nil {
		t.Fatal("a changed session must be written")
	}
	if _, err := os.Stat(cookiesPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("unchanged cookies must not be rewritten with the session")
	}
}

func TestExportTrimsTheThreadInMemory(t *testing.T) {
	b, _, _ := loggedInBackend(t)
	b.store.UpdateThread("5", func(rec *threadRec) { rec.Name = "Long" })
	for i := 300; i < 450; i++ {
		b.store.PutMessage(Message{ID: "m" + strconv.Itoa(i), ThreadID: "5", Text: "x", TimestampMS: int64(1000 + i)})
	}
	b.store.SetHasMoreBefore("5", true)
	b.olderPageFn = func(context.Context, threadRec, int64, int) bool {
		for i := 0; i < 300; i++ {
			b.store.PutMessage(Message{ID: "m" + strconv.Itoa(i), ThreadID: "5", Text: "x", TimestampMS: int64(1000 + i)})
		}
		b.store.SetReaction("m0", "7", "👍", 1)
		b.store.SetHasMoreBefore("5", false)
		return false
	}
	res, err := b.ExportThread(context.Background(), ExportThreadParams{ThreadID: "5", Dir: t.TempDir(), Format: ExportJSON})
	if err != nil {
		t.Fatal(err)
	}
	if res.Messages != 450 {
		t.Fatalf("the export must include every loaded message, got %d", res.Messages)
	}
	if n := b.store.MessageCount("5"); n != maxPersistedMessagesPerThread {
		t.Fatalf("after an export the thread must be trimmed to %d messages, got %d", maxPersistedMessagesPerThread, n)
	}
	if _, ok := b.store.FindMessage("m0"); ok {
		t.Fatal("the oldest messages must be dropped")
	}
	if b.store.ReactionOf("m0", "7") != "" {
		t.Fatal("the reactions of dropped messages must be released")
	}
	if more, _ := b.store.HasMoreBefore("5"); !more {
		t.Fatal("a trimmed thread must allow loading older pages again")
	}
}

func TestExportKeepsMessagesLoadedBeforeIt(t *testing.T) {
	b, _, _ := loggedInBackend(t)
	b.store.UpdateThread("5", func(rec *threadRec) { rec.Name = "Long" })
	for i := 0; i < 400; i++ {
		msg := Message{ID: "m" + strconv.Itoa(i), ThreadID: "5", Text: "x", TimestampMS: int64(1000 + i)}
		if i == 0 {
			msg.Attachments = []Attachment{{ID: "a0", Kind: "image"}}
		}
		b.store.PutMessage(msg)
	}
	b.store.PutMedia("m0", "a0", mediaRec{Kind: "image", URL: "https://example.invalid/a0"})
	b.store.SetHasMoreBefore("5", false)
	if _, err := b.ExportThread(context.Background(), ExportThreadParams{ThreadID: "5", Dir: t.TempDir(), Format: ExportJSON}); err != nil {
		t.Fatal(err)
	}
	if n := b.store.MessageCount("5"); n != 400 {
		t.Fatalf("an export must not drop messages loaded before it, got %d", n)
	}
	if _, ok := b.store.FindMessage("m0"); !ok {
		t.Fatal("the oldest message loaded before the export must stay known")
	}
	if _, ok := b.store.Media("m0", "a0"); !ok {
		t.Fatal("the media of a message loaded before the export must stay known")
	}
}

func TestStoreSaveKeepsChangesMadeDuringTheWrite(t *testing.T) {
	path := filepath.Join(t.TempDir(), cacheFile)
	s := NewStore()
	s.SetContact("7", "Camille", "", "")
	write := writeStoreFile
	t.Cleanup(func() { writeStoreFile = write })
	writeStoreFile = func(p string, data []byte) error {
		if p == path {
			writeStoreFile = write
			s.SetContact("8", "Théo", "", "")
		}
		return write(p, data)
	}
	if err := s.Save(path); err != nil {
		t.Fatal(err)
	}
	if err := s.Save(path); err != nil {
		t.Fatal(err)
	}
	data, err := readSecretFile(path, zerolog.Nop())
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "Théo") {
		t.Fatalf("a change made while the cache was written must be saved by the next save: %s", data)
	}
}

func TestSentFilesAreCachedInCacheDir(t *testing.T) {
	dataDir, cacheDir := t.TempDir(), t.TempDir()
	b, err := NewMetaBackend(dataDir, cacheDir, NewOutput(&syncBuffer{}, zerolog.Nop()), zerolog.Nop())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(b.Close)
	b.store.PutMessage(Message{ID: "mid.$s", ThreadID: "5", Text: "", TimestampMS: 1, Attachments: []Attachment{{ID: "a0", Kind: "file"}}})
	b.cacheSentFile("mid.$s", "a0", sendFile{Name: "plan.pdf", Mime: "application/pdf", Kind: "file"}, []byte("%PDF-1.4"), mediaRec{})
	rec, ok := b.store.Media("mid.$s", "a0")
	if !ok || !strings.HasPrefix(rec.Path, filepath.Join(cacheDir, mediaDirName)+string(filepath.Separator)) {
		t.Fatalf("a sent file must be cached under the cache dir: %+v", rec)
	}
	if _, err := os.Stat(filepath.Join(dataDir, mediaDirName)); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("a sent file must not recreate media/ in the data dir")
	}
}

func TestMediaLocksAreReleased(t *testing.T) {
	b, _, _ := loggedInBackend(t)
	unlock := b.lockMediaKey("a")
	acquired := make(chan struct{})
	go func() {
		release := b.lockMediaKey("a")
		close(acquired)
		release()
	}()
	select {
	case <-acquired:
		t.Fatal("the same media key must be serialized")
	default:
	}
	unlock()
	<-acquired
	waitFor(t, 5*time.Second, func() bool { return b.mediaLockCount() == 0 })
	for i := 0; i < 100; i++ {
		b.lockMediaKey("k" + strconv.Itoa(i))()
	}
	if n := b.mediaLockCount(); n != 0 {
		t.Fatalf("finished downloads must not leave locks behind, got %d", n)
	}
}

func TestReadSentIsBounded(t *testing.T) {
	b, _, _ := loggedInBackend(t)
	for i := 0; i < maxReadSentIDs+50; i++ {
		b.markReadSent("id" + strconv.Itoa(i))
	}
	if n := b.readSentSize(); n == 0 || n > maxReadSentIDs {
		t.Fatalf("the read-receipt memory must stay bounded, got %d", n)
	}
	if _, ok := b.readSent.Load("id" + strconv.Itoa(maxReadSentIDs+49)); !ok {
		t.Fatal("the latest receipt must be remembered")
	}
}

func TestAccountResetClearsPresenceCounters(t *testing.T) {
	b, _, _ := loggedInBackend(t)
	b.presenceLS.Store(4)
	b.presenceE2EE.Store(3)
	b.markReadSent("x")
	b.resetAccountState()
	if b.presenceLS.Load() != 0 || b.presenceE2EE.Load() != 0 {
		t.Fatal("presence counters belong to the account and must be reset")
	}
	if b.readSentSize() != 0 {
		t.Fatal("receipts sent for the previous account must be forgotten")
	}
}

func TestCacheDirsMoveOutOfDataDir(t *testing.T) {
	dataDir, cacheDir := t.TempDir(), filepath.Join(t.TempDir(), "cache")
	write := func(path, content string) {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write(filepath.Join(dataDir, mediaDirName, "a.jpg"), "a")
	write(filepath.Join(dataDir, mediaDirName, thumbDirName, "t.jpg"), "t")
	write(filepath.Join(dataDir, avatarDirName, "p.jpg"), "p")
	write(filepath.Join(dataDir, cacheFile), "secret")
	write(filepath.Join(dataDir, waDBFile), "db")
	write(filepath.Join(cacheDir, avatarDirName, "q.jpg"), "q")
	migrateCacheDirs(dataDir, cacheDir, zerolog.Nop())
	for _, rel := range []string{filepath.Join(mediaDirName, "a.jpg"), filepath.Join(mediaDirName, thumbDirName, "t.jpg"), filepath.Join(avatarDirName, "p.jpg"), filepath.Join(avatarDirName, "q.jpg")} {
		if !fileExists(filepath.Join(cacheDir, rel)) {
			t.Fatalf("%s must be in the cache dir", rel)
		}
	}
	for _, name := range []string{mediaDirName, avatarDirName} {
		if _, err := os.Stat(filepath.Join(dataDir, name)); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("%s must leave the data dir", name)
		}
	}
	for _, name := range []string{cacheFile, waDBFile} {
		if !fileExists(filepath.Join(dataDir, name)) || fileExists(filepath.Join(cacheDir, name)) {
			t.Fatalf("%s must stay in the data dir", name)
		}
	}
	migrateCacheDirs(cacheDir, cacheDir, zerolog.Nop())
	if !fileExists(filepath.Join(cacheDir, mediaDirName, "a.jpg")) {
		t.Fatal("using the same directory must not move anything")
	}
}

func TestMovedMediaPathsAreRebased(t *testing.T) {
	dataDir, cacheDir := t.TempDir(), t.TempDir()
	oldPath := filepath.Join(dataDir, mediaDirName, "a.jpg")
	newPath := filepath.Join(cacheDir, mediaDirName, "a.jpg")
	stayed := filepath.Join(dataDir, mediaDirName, "b.jpg")
	for _, p := range []string{newPath, stayed} {
		if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	s := NewStore()
	s.PutMedia("m1", "a", mediaRec{Path: oldPath})
	s.PutMedia("m2", "b", mediaRec{Path: stayed})
	s.PutMedia("m3", "c", mediaRec{Path: filepath.Join(t.TempDir(), "elsewhere.jpg")})
	if n := s.RebaseMediaPaths(filepath.Join(dataDir, mediaDirName), filepath.Join(cacheDir, mediaDirName)); n != 1 {
		t.Fatalf("exactly the moved file must be rebased, got %d", n)
	}
	if rec, _ := s.Media("m1", "a"); rec.Path != newPath {
		t.Fatalf("a moved file must point to the cache dir, got %s", rec.Path)
	}
	if rec, _ := s.Media("m2", "b"); rec.Path != stayed {
		t.Fatal("a file left in place must keep its path")
	}
}

func TestCacheEvictionSparesMedia(t *testing.T) {
	cacheDir := t.TempDir()
	now := time.Now()
	files := []struct {
		rel  string
		size int
		age  time.Duration
	}{
		{filepath.Join(mediaDirName, thumbDirName, "old.jpg"), 400, 3 * time.Hour},
		{filepath.Join(avatarDirName, "mid.jpg"), 400, 2 * time.Hour},
		{filepath.Join(mediaDirName, thumbDirName, "new.jpg"), 400, time.Hour},
		{filepath.Join(avatarDirName, "avatar-1.part"), 400, 4 * time.Hour},
		{filepath.Join(mediaDirName, "e2ee.bin"), 5000, 5 * time.Hour},
	}
	for _, f := range files {
		p := filepath.Join(cacheDir, f.rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, make([]byte, f.size), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Chtimes(p, now.Add(-f.age), now.Add(-f.age)); err != nil {
			t.Fatal(err)
		}
	}
	removed, freed := evictCache(evictableCacheDirs(cacheDir), 800)
	if removed != 1 || freed != 400 {
		t.Fatalf("only the oldest thumbnail must go, got %d files / %d bytes", removed, freed)
	}
	if fileExists(filepath.Join(cacheDir, files[0].rel)) {
		t.Fatal("the oldest file must be evicted first")
	}
	for _, f := range files[1:] {
		if !fileExists(filepath.Join(cacheDir, f.rel)) {
			t.Fatalf("%s must be kept", f.rel)
		}
	}
	if removed, _ := evictCache(evictableCacheDirs(cacheDir), 1<<20); removed != 0 {
		t.Fatal("a cache under the cap must not be touched")
	}
}

type limitBackend struct {
	*DemoBackend
	mu      sync.Mutex
	limits  map[string]int
	release chan struct{}
	firstIn chan struct{}
	active  int
	peak    int
	order   []string
}

func (l *limitBackend) record(name string, limit int) {
	l.mu.Lock()
	l.limits[name] = limit
	l.mu.Unlock()
}

func (l *limitBackend) Threads(ctx context.Context, limit int) ([]Thread, error) {
	l.record("threads", limit)
	return nil, nil
}

func (l *limitBackend) Messages(ctx context.Context, threadID string, beforeMS int64, limit int, fetchMissing bool) ([]Message, error) {
	l.record("messages"+threadID, limit)
	return nil, nil
}

func (l *limitBackend) MediaHistory(ctx context.Context, threadID string, beforeMS int64, limit int, kinds []string, maxPages int) (MediaHistoryResult, error) {
	l.record("media_history", limit)
	return MediaHistoryResult{}, nil
}

func (l *limitBackend) SearchFiltered(ctx context.Context, f SearchFilter, limit int) ([]SearchHit, error) {
	l.record("search", limit)
	return nil, nil
}

func (l *limitBackend) Status(ctx context.Context) (StatusResult, error) {
	l.mu.Lock()
	l.active++
	l.peak = max(l.peak, l.active)
	l.mu.Unlock()
	<-l.release
	l.mu.Lock()
	l.active--
	l.mu.Unlock()
	return StatusResult{}, nil
}

func (l *limitBackend) Send(ctx context.Context, threadID, text, replyToID, clientID string, mentions []Mention) (string, error) {
	if text == "first" {
		close(l.firstIn)
		<-l.release
	}
	l.mu.Lock()
	l.order = append(l.order, text)
	l.mu.Unlock()
	return text, nil
}

func (l *limitBackend) Close() {
	l.mu.Lock()
	l.order = append(l.order, "close")
	l.mu.Unlock()
}

func newLimitBackend(t *testing.T) (*limitBackend, *Dispatcher, *syncBuffer) {
	buf := &syncBuffer{}
	out := NewOutput(buf, zerolog.Nop())
	lb := &limitBackend{DemoBackend: NewDemoBackend(t.TempDir(), "", out, zerolog.Nop()), limits: map[string]int{}, release: make(chan struct{}), firstIn: make(chan struct{})}
	return lb, NewDispatcher(lb, out, zerolog.Nop()), buf
}

func responseFor(t *testing.T, buf *syncBuffer, id int64) (map[string]any, bool) {
	t.Helper()
	for _, line := range buf.Lines() {
		var resp map[string]any
		if json.Unmarshal([]byte(line), &resp) == nil {
			if v, ok := resp["id"].(float64); ok && int64(v) == id {
				return resp, true
			}
		}
	}
	return nil, false
}

func TestDispatcherCapsListLimits(t *testing.T) {
	lb, disp, _ := newLimitBackend(t)
	disp.Run(strings.NewReader(strings.Join([]string{
		`{"id":1,"cmd":"threads","params":{"limit":100000}}`,
		`{"id":2,"cmd":"messages","params":{"thread_id":"5","limit":100000}}`,
		`{"id":3,"cmd":"media_history","params":{"thread_id":"5","limit":100000}}`,
		`{"id":4,"cmd":"search","params":{"query":"x","limit":100000}}`,
		`{"id":5,"cmd":"messages","params":{"thread_id":"6","limit":20}}`,
	}, "\n") + "\n"))
	disp.Wait()
	for _, name := range []string{"threads", "media_history", "search"} {
		if lb.limits[name] != maxListLimit {
			t.Fatalf("%s limit must be capped at %d, got %d", name, maxListLimit, lb.limits[name])
		}
	}
	if lb.limits["messages5"] != maxListLimit || lb.limits["messages6"] != 20 {
		t.Fatalf("messages limit must be capped and small limits kept, got %v", lb.limits)
	}
}

func TestDispatcherBoundsConcurrentRequests(t *testing.T) {
	lb, disp, buf := newLimitBackend(t)
	var lines []string
	for i := 1; i <= maxConcurrentRequests+6; i++ {
		lines = append(lines, `{"id":`+strconv.Itoa(i)+`,"cmd":"status"}`)
	}
	disp.Run(strings.NewReader(strings.Join(lines, "\n") + "\n"))
	waitFor(t, 5*time.Second, func() bool {
		lb.mu.Lock()
		defer lb.mu.Unlock()
		return lb.active == maxConcurrentRequests
	})
	if len(disp.slots) != cap(disp.slots) || cap(disp.slots) != maxConcurrentRequests {
		t.Fatalf("every request slot must be taken while %d requests block", maxConcurrentRequests)
	}
	close(lb.release)
	disp.Wait()
	if lb.peak > maxConcurrentRequests {
		t.Fatalf("at most %d requests may run at once, got %d", maxConcurrentRequests, lb.peak)
	}
	if n := len(buf.Lines()); n != maxConcurrentRequests+6 {
		t.Fatalf("every request must be answered, got %d", n)
	}
}

func TestLaneWaitStopsOnShutdown(t *testing.T) {
	lb, disp, buf := newLimitBackend(t)
	disp.HandleLine([]byte(`{"id":1,"cmd":"send","params":{"thread_id":"A","text":"first"}}`))
	<-lb.firstIn
	disp.HandleLine([]byte(`{"id":2,"cmd":"send","params":{"thread_id":"A","text":"second"}}`))
	disp.cancel()
	waitFor(t, 5*time.Second, func() bool {
		_, ok := responseFor(t, buf, 2)
		return ok
	})
	resp, _ := responseFor(t, buf, 2)
	if resp["ok"] != false {
		t.Fatalf("a request waiting behind its lane must fail on shutdown, got %v", resp)
	}
	if _, ok := responseFor(t, buf, 1); ok {
		t.Fatal("the request holding the lane is still running")
	}
	close(lb.release)
	disp.Wait()
	lb.mu.Lock()
	defer lb.mu.Unlock()
	if strings.Join(lb.order, ",") != "first" {
		t.Fatalf("the cancelled request must not run, got %v", lb.order)
	}
}

func TestShutdownCancelsBeforeWaiting(t *testing.T) {
	lb, disp, _ := newLimitBackend(t)
	blocking := &ctxBackend{limitBackend: lb, entered: make(chan struct{})}
	disp.backend = blocking
	disp.HandleLine([]byte(`{"id":1,"cmd":"status"}`))
	<-blocking.entered
	start := time.Now()
	disp.Shutdown(20 * time.Second)
	if time.Since(start) > 10*time.Second {
		t.Fatal("shutdown must cancel in-flight requests before waiting for them")
	}
	lb.mu.Lock()
	defer lb.mu.Unlock()
	if strings.Join(lb.order, ",") != "cancelled,close" {
		t.Fatalf("the backend must close after in-flight requests stop, got %v", lb.order)
	}
}

type ctxBackend struct {
	*limitBackend
	entered chan struct{}
}

func (c *ctxBackend) Status(ctx context.Context) (StatusResult, error) {
	close(c.entered)
	<-ctx.Done()
	c.mu.Lock()
	c.order = append(c.order, "cancelled")
	c.mu.Unlock()
	return StatusResult{}, ctx.Err()
}

func TestFailedCacheWriteIsRetried(t *testing.T) {
	s := NewStore()
	s.PutMessage(Message{ID: "m1", ThreadID: "t1", Text: "x", TimestampMS: 1})
	if err := s.Save(filepath.Join(t.TempDir(), "missing", cacheFile)); err == nil {
		t.Fatal("writing into a missing directory must fail")
	}
	path := filepath.Join(t.TempDir(), cacheFile)
	if err := s.Save(path); err != nil || !fileExists(path) {
		t.Fatalf("a failed cache write must be retried on the next save: %v", err)
	}
}

func TestAtomicWritesAndSingleFlightMedia(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "state.json")
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			if err := writeFileAtomic(path, []byte(fmt.Sprintf("payload-%02d", i))); err != nil {
				t.Error(err)
			}
		}(i)
	}
	wg.Wait()
	entries, _ := os.ReadDir(dir)
	data, _ := os.ReadFile(path)
	if len(entries) != 1 || !strings.HasPrefix(string(data), "payload-") {
		t.Fatalf("leftover temp files or corrupt content: %d entries, %q", len(entries), data)
	}

	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		time.Sleep(100 * time.Millisecond)
		w.Write([]byte("\x89PNG\r\n\x1a\nimage"))
	}))
	defer srv.Close()
	allowTestServer(t, srv)
	b, _, _ := loggedInBackend(t)
	b.handleTable(&table.LSTable{
		LSInsertMessage:        []*table.LSInsertMessage{{ThreadKey: 5, MessageId: "mid.$m", TimestampMs: 10, SenderId: 7}},
		LSInsertBlobAttachment: []*table.LSInsertBlobAttachment{{MessageId: "mid.$m", AttachmentFbid: "1", AttachmentType: table.AttachmentTypeImage, PreviewUrl: srv.URL + "/p"}},
	}, modeLive)
	var mwg sync.WaitGroup
	for i := 0; i < 5; i++ {
		mwg.Add(1)
		go func() {
			defer mwg.Done()
			if _, err := b.Media(context.Background(), "5", "mid.$m", "1", VariantFull); err != nil {
				t.Error(err)
			}
		}()
	}
	mwg.Wait()
	if hits.Load() != 1 {
		t.Fatalf("concurrent media requests must download once, got %d", hits.Load())
	}
}
