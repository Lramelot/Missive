package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/rs/zerolog"
	"go.mau.fi/whatsmeow/types/events"

	"go.mau.fi/mautrix-meta/pkg/messagix/cookies"
	"go.mau.fi/mautrix-meta/pkg/messagix/securehistory"
	"go.mau.fi/mautrix-meta/pkg/messagix/table"
	"go.mau.fi/mautrix-meta/pkg/messagix/types"
)

func loggedInBackend(t *testing.T) (*MetaBackend, *syncBuffer, string) {
	t.Helper()
	b, buf, dir := newTestMetaBackend(t)
	b.mu.Lock()
	b.cookies = &cookies.Cookies{Platform: types.Messenger}
	b.sess = &session{User: User{ID: testSelf, Name: "Me"}}
	b.mu.Unlock()
	b.store.SetSelfID(testSelf)
	return b, buf, dir
}

func TestFixE2EEDisconnectClassification(t *testing.T) {
	b, _, _ := newTestMetaBackend(t)
	cases := []struct {
		evt  events.PermanentDisconnect
		want e2eeAction
	}{
		{&events.ConnectFailure{Reason: events.ConnectFailureNotFound}, e2eeActionReset},
		{&events.ConnectFailure{Reason: events.ConnectFailureClientUnknown}, e2eeActionReset},
		{&events.ConnectFailure{Reason: events.ConnectFailureGeneric}, e2eeActionFullReconnect},
		{&events.ConnectFailure{Reason: events.ConnectFailureReason(503)}, e2eeActionRetry},
		{&events.LoggedOut{Reason: events.ConnectFailureLoggedOut}, e2eeActionReset},
		{&events.LoggedOut{Reason: events.ConnectFailureLoggedOut}, e2eeActionGiveUp},
	}
	for i, c := range cases {
		if got := b.classifyE2EEDisconnect(c.evt); got != c.want {
			t.Errorf("case %d: got %d want %d", i, got, c.want)
		}
	}
	b.e2eeLogoutRetried.Store(false)
	if got := b.classifyE2EEDisconnect(&events.LoggedOut{}); got != e2eeActionReset {
		t.Fatal("a new connection must allow one logout reset again")
	}
	for i := 0; i < maxE2EEDeviceResets; i++ {
		if !b.allowE2EEReset() {
			t.Fatalf("reset %d must be allowed", i)
		}
	}
	if b.allowE2EEReset() {
		t.Fatal("device resets must be capped per session")
	}
}

func TestFixLinkTargetIsNeverFetched(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		w.Write([]byte("<html>router admin</html>"))
	}))
	defer srv.Close()
	allowTestServer(t, srv)
	b, _, _ := loggedInBackend(t)
	b.handleTable(&table.LSTable{
		LSInsertMessage:       []*table.LSInsertMessage{{ThreadKey: 5, MessageId: "mid.$l", TimestampMs: 10, SenderId: 7}},
		LSInsertXmaAttachment: []*table.LSInsertXmaAttachment{{MessageId: "mid.$l", AttachmentFbid: "1", AttachmentType: table.AttachmentTypeXMA, TitleText: "click", ActionUrl: srv.URL + "/admin?reboot=1"}},
	}, modeLive)
	msg, _ := b.store.MessageInThread("5", "mid.$l")
	if len(msg.Attachments) != 1 || msg.Attachments[0].Kind != "link" {
		t.Fatalf("setup: %+v", msg)
	}
	if _, err := b.Media(context.Background(), "5", "mid.$l", "1", VariantFull); err == nil || toErrorBody(err).Code != CodeNotFound {
		t.Fatalf("link without preview must be not_found, got %v", err)
	}
	if hits.Load() != 0 {
		t.Fatal("the link target was fetched")
	}
}

func TestFixMediaURLAllowList(t *testing.T) {
	for raw, want := range map[string]bool{
		"https://scontent.xx.fbcdn.net/v/t1/p.jpg":      true,
		"https://cdn.fbsbx.com/v/file.pdf":              true,
		"https://scontent.cdninstagram.com/x.jpg":       true,
		"https://lookaside.facebook.com/x":              true,
		"http://scontent.xx.fbcdn.net/p.jpg":            false,
		"https://evil.example/fbcdn.net/x":              false,
		"https://fbcdn.net.evil.example/x":              false,
		"https://user:pw@scontent.xx.fbcdn.net/p":       false,
		"https://127.0.0.1/admin":                       false,
		"file:///C:/Windows/system32/drivers/etc/hosts": false,
	} {
		u, _ := url.Parse(raw)
		if got := defaultMediaURLAllowed(u); got != want {
			t.Errorf("%s: got %v want %v", raw, got, want)
		}
	}
	outside := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("secret")) }))
	defer outside.Close()
	allowed := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/redirect" {
			http.Redirect(w, r, outside.URL+"/x", http.StatusFound)
			return
		}
		w.Write(make([]byte, 2048))
	}))
	defer allowed.Close()
	allowTestServer(t, allowed)
	dir := t.TempDir()
	f, _ := newMediaTemp(dir)
	defer discardMediaTemp(f)
	if _, err := httpDownloadToFile(context.Background(), allowed.URL+"/redirect", 1<<20, f); err == nil {
		t.Fatal("redirect to a non-Meta host must be refused")
	}
	if _, err := httpDownloadToFile(context.Background(), allowed.URL+"/big", 1024, f); err == nil || !strings.Contains(err.Error(), "larger") {
		t.Fatalf("size cap not enforced: %v", err)
	}
}

type laneBackend struct {
	*DemoBackend
	mu    sync.Mutex
	order []string
}

func (l *laneBackend) Send(ctx context.Context, threadID, text, replyToID, clientID string, mentions []Mention) (string, error) {
	if text == "first" {
		time.Sleep(150 * time.Millisecond)
	}
	l.mu.Lock()
	l.order = append(l.order, threadID+":"+text)
	l.mu.Unlock()
	return text, nil
}

func TestFixSendsAreSerializedPerThread(t *testing.T) {
	buf := &syncBuffer{}
	out := NewOutput(buf, zerolog.Nop())
	lb := &laneBackend{DemoBackend: NewDemoBackend(t.TempDir(), out, zerolog.Nop())}
	disp := NewDispatcher(lb, out, zerolog.Nop())
	disp.Run(strings.NewReader(strings.Join([]string{
		`{"id":1,"cmd":"send","params":{"thread_id":"A","text":"first"}}`,
		`{"id":2,"cmd":"send","params":{"thread_id":"A","text":"second"}}`,
		`{"id":3,"cmd":"send","params":{"thread_id":"B","text":"other"}}`,
		`{"id":4,"cmd":"send","params":{"thread_id":"A","text":"third"}}`,
	}, "\n") + "\n"))
	disp.Wait()
	disp.Shutdown(time.Second)
	got := strings.Join(lb.order, ",")
	if !strings.HasPrefix(got, "B:other,") || !strings.HasSuffix(got, "A:first,A:second,A:third") {
		t.Fatalf("unexpected order %s", got)
	}
}

func TestFixReplyMustBelongToThread(t *testing.T) {
	b, _, _ := loggedInBackend(t)
	b.store.UpdateThread("5", func(rec *threadRec) {})
	b.store.UpdateThread("6", func(rec *threadRec) {})
	b.store.PutMessage(Message{ID: "mid.$other", ThreadID: "6", TimestampMS: 1})
	if _, err := b.Send(context.Background(), "5", "hi", "mid.$other", "", nil); toErrorBody(err).Code != CodeBadRequest {
		t.Fatalf("reply to another thread must be bad_request, got %v", err)
	}
	if _, err := b.SendMedia(context.Background(), "5", nil, "", "mid.$other", nil); toErrorBody(err).Code != CodeBadRequest {
		t.Fatalf("send_media reply to another thread must be bad_request, got %v", err)
	}
	if lsReplyMeta(&Message{ID: "3EB0ABC"}) != nil || lsReplyMeta(&Message{ID: "mid.$x", SenderID: "7"}) == nil {
		t.Fatal("LS reply metadata must only quote mid.$ ids")
	}
	if waQuotedMeta(&Message{ID: "mid.$x"}).QuotedMessage != nil || waQuotedMeta(&Message{ID: "3EB0ABC", SenderID: "7"}).QuotedMessage == nil {
		t.Fatal("E2EE quotes must only quote WhatsApp ids")
	}
	b.store.SetHybrid(testLegacyFB, testPeer)
	if key, _ := b.lsThreadKey(threadRec{ID: testPeerID, E2EE: true}); key != testLegacyFB {
		t.Fatalf("LS actions on a hybrid thread must use the FB key, got %d", key)
	}
	if key, _ := b.lsThreadKey(threadRec{ID: "5"}); key != 5 {
		t.Fatalf("plain LS thread key changed: %d", key)
	}
}

func TestFixSendMediaErrorData(t *testing.T) {
	err := sendMediaError(uncertainError(newError(CodeInternal, "Meta did not confirm the message")), []string{"m1"}, 1)
	body := toErrorBody(err)
	raw, _ := json.Marshal(body)
	if body.Code != CodeInternal || !strings.Contains(string(raw), `"data":{"failed_index":1,"message_ids":["m1"],"uncertain":true}`) {
		t.Fatalf("unexpected error body %s", raw)
	}
	b, _, _ := loggedInBackend(t)
	b.store.UpdateThread("5", func(rec *threadRec) { rec.E2EE = true })
	dir := t.TempDir()
	path := filepath.Join(dir, "a.txt")
	os.WriteFile(path, []byte("x"), 0o600)
	files, _ := loadSendFiles([]string{path})
	_, err = b.SendMedia(context.Background(), "5", files, "", "", nil)
	body = toErrorBody(err)
	if body.Code != CodeNotConnected || body.Data["failed_index"] != 0 || body.Data["uncertain"] != false || len(body.Data["message_ids"].([]string)) != 0 {
		t.Fatalf("unexpected first-item failure %+v", body)
	}

	h := newDemoHarness(t)
	h.send(`{"id":1,"cmd":"connect"}`)
	h.waitEvent(EventReady, 3*time.Second)
	keep := filepath.Join(dir, "keep.txt")
	gone := filepath.Join(dir, "gone.txt")
	os.WriteFile(keep, []byte("k"), 0o600)
	os.WriteFile(gone, []byte("g"), 0o600)
	files, _ = loadSendFiles([]string{keep, gone})
	os.Remove(gone)
	ids, err := h.demo.SendMedia(context.Background(), demoPeople[0].ID, files, "", "", nil)
	body = toErrorBody(err)
	if len(ids) != 1 || body.Code != CodeBadRequest || body.Data["failed_index"] != 1 || len(body.Data["message_ids"].([]string)) != 1 {
		t.Fatalf("partial failure must report what was sent: %v %+v", ids, body)
	}
}

func TestFixUnconfirmedEnrollmentIsNotRedone(t *testing.T) {
	fake := &fakeHistory{}
	b, _, _ := newHistoryBackend(t, fake)
	b.mu.Lock()
	b.sess.HistoryDevice = &securehistory.Device{BackupID: "backup-1"}
	b.mu.Unlock()
	err := b.RestoreHistory(context.Background(), testPIN)
	if err == nil {
		t.Fatal("restore over an unconfirmed enrollment must fail")
	}
	if body := toErrorBody(err); body.Code != CodeInternal || body.Message != "history enrollment unconfirmed" {
		t.Fatalf("unexpected %+v", body)
	}
	if fake.enrolls.Load() != 0 {
		t.Fatal("must never enroll twice")
	}
}

func TestFixTrimmedCacheKeepsHasMore(t *testing.T) {
	s := NewStore()
	s.SetSelfID("1")
	s.UpdateThread("5", func(rec *threadRec) {})
	for i := 0; i < 400; i++ {
		s.PutMessage(Message{ID: fmt.Sprintf("m%03d", i), ThreadID: "5", TimestampMS: int64(1000 + i)})
	}
	s.SetHasMoreBefore("5", false)
	path := filepath.Join(t.TempDir(), "cache.json")
	if err := s.Save(path); err != nil {
		t.Fatal(err)
	}
	s2 := NewStore()
	if err := s2.Load(path); err != nil {
		t.Fatal(err)
	}
	if more, known := s2.HasMoreBefore("5"); s2.MessageCount("5") == 400 || !known || !more {
		t.Fatalf("trimmed thread must keep has_more_before=true (known=%v more=%v)", known, more)
	}
	if more, _ := s.HasMoreBefore("5"); more {
		t.Fatal("saving must not change the in-memory flag")
	}
}

func TestFixPagingNeverSplitsSameTimestamp(t *testing.T) {
	s := NewStore()
	s.UpdateThread("5", func(rec *threadRec) {})
	s.PutMessage(Message{ID: "m1", ThreadID: "5", TimestampMS: 1000, Attachments: []Attachment{{ID: "a", Kind: "image"}, {ID: "b", Kind: "image"}, {ID: "c", Kind: "image"}}})
	s.PutMessage(Message{ID: "m0", ThreadID: "5", TimestampMS: 900, Attachments: []Attachment{{ID: "z", Kind: "image"}}})
	page1, more := cutMediaItems(s.MediaItems("5", 0, []string{"image"}), 2)
	if len(page1) != 3 || !more {
		t.Fatalf("one message's attachments must stay together: %d %v", len(page1), more)
	}
	page2, _ := cutMediaItems(s.MediaItems("5", page1[len(page1)-1].TimestampMS, []string{"image"}), 2)
	if len(page2) != 1 || page2[0].MessageID != "m0" {
		t.Fatalf("lossless continuation failed: %+v", page2)
	}
	for i, ts := range []int64{100, 200, 200, 200, 300} {
		s.PutMessage(Message{ID: fmt.Sprintf("t%d", i), ThreadID: "9", TimestampMS: ts})
	}
	msgs := s.Messages("9", 300, 2)
	if len(msgs) != 3 || msgs[0].TimestampMS != 200 {
		t.Fatalf("messages page must include the whole same-timestamp group: %+v", msgs)
	}
	if older := s.Messages("9", msgs[0].TimestampMS, 10); len(older) != 1 || older[0].TimestampMS != 100 {
		t.Fatalf("lossless older page failed: %+v", older)
	}
}

func TestFixLogoutWipesAccountData(t *testing.T) {
	b, _, dir := loggedInBackend(t)
	mediaPath, _, err := saveMediaFile(dir, []byte("secret"), "text/plain", "a.txt")
	if err != nil {
		t.Fatal(err)
	}
	b.contactsRequested.Store("42", true)
	b.mu.Lock()
	b.historyBackup = &securehistory.Backup{ID: "x"}
	b.mu.Unlock()
	b.store.UpdateThread("5", func(rec *threadRec) {})
	if err := b.Logout(context.Background()); err != nil {
		t.Fatal(err)
	}
	if fileExists(mediaPath) {
		t.Fatal("media cache survived logout")
	}
	if _, ok := b.contactsRequested.Load("42"); ok || b.historyBackup != nil {
		t.Fatal("per-account state survived logout")
	}
	b.handleTable(&table.LSTable{LSDeleteThenInsertThread: []*table.LSDeleteThenInsertThread{{ThreadKey: 77, ThreadType: table.GROUP_THREAD}}}, modeLive)
	if b.store.ThreadCount() != 0 {
		t.Fatal("tables arriving after logout must be ignored")
	}
	b.persist()
	if fileExists(filepath.Join(dir, cacheFile)) {
		t.Fatal("persist after logout must not write the cache")
	}
}

func TestFixSyncStormGuards(t *testing.T) {
	b, buf, _ := loggedInBackend(t)
	b.syncing.Store(true)
	b.syncThreads(context.Background(), nil, false)
	b.syncThreads(context.Background(), nil, true)
	if len(eventsOf(t, buf, EventReady)) != 1 {
		t.Fatal("a concurrent first sync must still emit ready")
	}
	b.syncing.Store(false)
	if !b.shouldRunHeavySync(true) || b.shouldRunHeavySync(false) {
		t.Fatal("heavy sync must run once and then be rate-limited")
	}
	b.lastHeavySync.Store(time.Now().Add(-31 * time.Minute).UnixMilli())
	if !b.shouldRunHeavySync(false) {
		t.Fatal("heavy sync must run again after 30 minutes")
	}
	b.store.UpdateThread("42", func(rec *threadRec) { rec.PeerID = "42" })
	b.contactsRequested.Store("42", true)
	b.resolveMissingContacts(context.Background(), nil)
}

func TestFixAtomicWritesAndSingleFlightMedia(t *testing.T) {
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

func TestFixSetActive(t *testing.T) {
	h := newDemoHarness(t)
	h.send(`{"id":1,"cmd":"set_active","params":{"active":true}}`, `{"id":2,"cmd":"set_active","params":{}}`)
	h.okResult(1, nil)
	if code := h.errCode(2); code != CodeBadRequest {
		t.Fatalf("got %s", code)
	}
	b, _, _ := loggedInBackend(t)
	if b.active.Load() {
		t.Fatal("must start inactive")
	}
	b.SetActive(context.Background(), true)
	if !b.active.Load() {
		t.Fatal("set_active not stored")
	}
}

func TestFixCatchUpAndStaleMessagesAreHistory(t *testing.T) {
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

func TestFixSendMediaCapsAndExtensions(t *testing.T) {
	dir := t.TempDir()
	var many []string
	for i := 0; i < maxSendFiles+1; i++ {
		p := filepath.Join(dir, fmt.Sprintf("f%d.txt", i))
		os.WriteFile(p, []byte("x"), 0o600)
		many = append(many, p)
	}
	if _, err := loadSendFiles(many); toErrorBody(err).Code != CodeBadRequest {
		t.Fatalf("more than %d files must be refused, got %v", maxSendFiles, err)
	}
	var big []string
	for i := 0; i < 3; i++ {
		p := filepath.Join(dir, fmt.Sprintf("big%d.bin", i))
		f, _ := os.Create(p)
		f.Truncate(80 << 20)
		f.Close()
		big = append(big, p)
	}
	if _, err := loadSendFiles(big); toErrorBody(err).Code != CodeBadRequest {
		t.Fatalf("more than 200 MB in total must be refused, got %v", err)
	}
	for _, c := range [][3]string{
		{"application/octet-stream", "evil.exe", ".bin"},
		{"", "photo.JPG", ".jpg"},
		{"image/png", "page.html", ".png"},
		{"text/html", "x.lnk", ".bin"},
		{"application/pdf", "doc", ".pdf"},
	} {
		if got := extensionFor(c[0], c[1]); got != c[2] {
			t.Errorf("extensionFor(%q,%q) = %q, want %q", c[0], c[1], got, c[2])
		}
	}
}
