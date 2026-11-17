package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"go.mau.fi/mautrix-meta/pkg/messagix"
	"go.mau.fi/mautrix-meta/pkg/messagix/securehistory"
	"go.mau.fi/mautrix-meta/pkg/messagix/table"
	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/proto/waMediaTransport"
	waTypes "go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/util/keys"
	"google.golang.org/protobuf/proto"
)

func waitFor(t *testing.T, timeout time.Duration, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatal("condition not met in time")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func encodedImage(t *testing.T, w, h int, asJPEG bool) []byte {
	t.Helper()
	img := image.NewNRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			img.SetNRGBA(x, y, color.NRGBA{R: uint8(x), G: uint8(y), B: 90, A: 255})
		}
	}
	var buf bytes.Buffer
	var err error
	if asJPEG {
		err = jpeg.Encode(&buf, img, nil)
	} else {
		err = png.Encode(&buf, img)
	}
	if err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func TestSpeedMessagesAreCacheFirst(t *testing.T) {
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

func TestSpeedBackfillDroppedAfterLogout(t *testing.T) {
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
		_, running := b.backfills.Load("5|0")
		return !running
	})
	if len(eventsOf(t, buf, EventBackfilled)) != 0 {
		t.Fatal("a backfill started before logout must not emit")
	}
}

func TestSpeedClientIDOnLSEcho(t *testing.T) {
	b, buf, _ := loggedInBackend(t)
	now := time.Now().UnixMilli()
	b.pendingClientIDs.Store("777", "tmp-ls")
	b.handleTable(&table.LSTable{LSInsertMessage: []*table.LSInsertMessage{
		{ThreadKey: 5, MessageId: "mid.$mine", TimestampMs: now, SenderId: 999, OfflineThreadingId: "777", Text: "hello"},
		{ThreadKey: 5, MessageId: "mid.$theirs", TimestampMs: now + 1, SenderId: 7, OfflineThreadingId: "777", Text: "yo"},
	}}, modeLive)
	got := map[string]string{}
	for _, raw := range eventsOf(t, buf, EventMessage) {
		var m Message
		json.Unmarshal(raw, &m)
		got[m.ID] = m.ClientID
	}
	if got["mid.$mine"] != "tmp-ls" || got["mid.$theirs"] != "" {
		t.Fatalf("client_id must be echoed on our own message only: %v", got)
	}
	if stored, _ := b.store.MessageInThread("5", "mid.$mine"); stored.ClientID != "" {
		t.Fatal("client_id must not be persisted")
	}
	for _, raw := range eventsOf(t, buf, EventMessage) {
		if strings.Contains(string(raw), `"client_id":""`) {
			t.Fatal("empty client_id must be omitted")
		}
	}
}

func TestSpeedClientIDsOnDemoSendMedia(t *testing.T) {
	h := newDemoHarness(t)
	h.send(`{"id":1,"cmd":"connect"}`)
	h.waitEvent(EventReady, 3*time.Second)
	dir := t.TempDir()
	a, c := filepath.Join(dir, "a.png"), filepath.Join(dir, "b.png")
	os.WriteFile(a, encodedImage(t, 8, 8, false), 0o600)
	os.WriteFile(c, encodedImage(t, 8, 8, false), 0o600)
	thread := demoPeople[0].ID
	h.send(
		fmt.Sprintf(`{"id":2,"cmd":"send_media","params":{"thread_id":%q,"paths":[%q,%q],"client_ids":["x1","x2"]}}`, thread, a, c),
		fmt.Sprintf(`{"id":3,"cmd":"send_media","params":{"thread_id":%q,"paths":[%q,%q],"client_ids":["only-one"]}}`, thread, a, c),
	)
	var res SendMediaResult
	h.okResult(2, &res)
	if h.errCode(3) != CodeBadRequest {
		t.Fatal("client_ids must have one entry per path")
	}
	echoes := map[string]string{}
	for _, raw := range h.events(EventMessage) {
		var m Message
		json.Unmarshal(raw, &m)
		echoes[m.ID] = m.ClientID
	}
	if len(res.MessageIDs) != 2 || echoes[res.MessageIDs[0]] != "x1" || echoes[res.MessageIDs[1]] != "x2" {
		t.Fatalf("client_ids not echoed per item: %v %v", res.MessageIDs, echoes)
	}
}

func avatarServer(t *testing.T, body func() []byte) (*httptest.Server, *atomic.Int32) {
	t.Helper()
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		if strings.Contains(r.URL.Path, "gone") {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		if strings.Contains(r.URL.Path, "slow") {
			time.Sleep(100 * time.Millisecond)
		}
		w.Header().Set("Content-Type", "image/png")
		w.Write(body())
	}))
	t.Cleanup(srv.Close)
	allowTestServer(t, srv)
	return srv, &hits
}

func TestSpeedAvatarCacheAndRefresh(t *testing.T) {
	var version atomic.Int32
	version.Store(1)
	srv, hits := avatarServer(t, func() []byte { return encodedImage(t, 4+int(version.Load()), 4, false) })
	b, _, dir := loggedInBackend(t)
	ctx := context.Background()

	var wg sync.WaitGroup
	paths := make([]string, 4)
	for i := range paths {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			paths[i], _ = b.Avatar(ctx, srv.URL+"/v/slow.png?stp=a&oe=1")
		}(i)
	}
	wg.Wait()
	if hits.Load() != 1 {
		t.Fatalf("concurrent avatar requests must share one download, got %d", hits.Load())
	}
	path := paths[0]
	if filepath.Dir(path) != filepath.Join(dir, avatarDirName) || filepath.Ext(path) != ".png" || paths[3] != path {
		t.Fatalf("unexpected avatar paths %v", paths)
	}
	if again, err := b.Avatar(ctx, srv.URL+"/v/slow.png?stp=a&oe=2"); err != nil || again != path || hits.Load() != 1 {
		t.Fatalf("a fresh cached avatar must be served from disk even with rotated signatures: %v %v hits=%d", again, err, hits.Load())
	}
	original, _ := os.ReadFile(path)

	old := time.Now().Add(-8 * 24 * time.Hour)
	os.Chtimes(path, old, old)
	version.Store(2)
	started := time.Now()
	stale, err := b.Avatar(ctx, srv.URL+"/v/slow.png?stp=a&oe=3")
	if err != nil || stale != path || time.Since(started) > 80*time.Millisecond {
		t.Fatalf("a stale avatar must be served immediately: %v %v %v", stale, err, time.Since(started))
	}
	waitFor(t, 3*time.Second, func() bool {
		info, err := os.Stat(path)
		return hits.Load() == 2 && err == nil && info.ModTime().After(old.Add(time.Hour))
	})
	if refreshed, _ := os.ReadFile(path); bytes.Equal(refreshed, original) {
		t.Fatal("background refresh must replace the avatar")
	}

	if _, err := b.Avatar(ctx, srv.URL+"/gone.png"); toErrorBody(err).Code != CodeUnsupported {
		t.Fatalf("missing avatar must be unsupported, got %v", err)
	}
	if _, err := b.Avatar(ctx, "https://evil.example/a.png"); toErrorBody(err).Code != CodeUnsupported {
		t.Fatalf("non-Meta host must be refused, got %v", err)
	}
	b.resetAccountState()
	if _, err := os.Stat(filepath.Join(dir, avatarDirName)); !os.IsNotExist(err) {
		t.Fatal("avatars dir must be deleted on logout")
	}
}

func TestSpeedDemoAvatar(t *testing.T) {
	h := newDemoHarness(t)
	var threads []Thread
	h.send(`{"id":1,"cmd":"threads"}`)
	h.okResult(1, &threads)
	var avatarURL string
	for _, th := range threads {
		if !th.IsGroup && th.AvatarURL != "" {
			avatarURL = th.AvatarURL
			break
		}
	}
	if !strings.HasPrefix(avatarURL, "https://"+demoAvatarHost+"/") {
		t.Fatalf("demo contacts must carry avatar URLs, got %q", avatarURL)
	}
	h.send(fmt.Sprintf(`{"id":2,"cmd":"avatar","params":{"url":%q}}`, avatarURL))
	var res AvatarResult
	h.okResult(2, &res)
	f, err := os.Open(res.Path)
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := png.DecodeConfig(f)
	f.Close()
	if err != nil || cfg.Width != demoAvatarSide {
		t.Fatalf("demo avatar is not a generated PNG: %v %+v", err, cfg)
	}
}

func inlineTransport(t *testing.T, thumb []byte) []byte {
	t.Helper()
	raw, err := proto.Marshal(&waMediaTransport.WAMediaTransport{
		Integral: &waMediaTransport.WAMediaTransport_Integral{DirectPath: proto.String("/v/t62/x")},
		Ancillary: &waMediaTransport.WAMediaTransport_Ancillary{
			Mimetype:  proto.String("image/jpeg"),
			Thumbnail: &waMediaTransport.WAMediaTransport_Ancillary_Thumbnail{JPEGThumbnail: thumb},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func TestSpeedThumbSourcePriority(t *testing.T) {
	var thumbHits, fullHits atomic.Int32
	big := encodedImage(t, 1600, 1200, false)
	small := encodedImage(t, 200, 150, true)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/thumb.jpg":
			thumbHits.Add(1)
			w.Header().Set("Content-Type", "image/jpeg")
			w.Write(small)
		default:
			fullHits.Add(1)
			w.Header().Set("Content-Type", "image/png")
			w.Write(big)
		}
	}))
	defer srv.Close()
	allowTestServer(t, srv)
	b, _, dir := loggedInBackend(t)
	ctx := context.Background()
	inline := encodedImage(t, 48, 36, true)
	b.store.PutMessage(Message{ID: "e2ee", ThreadID: "5", TimestampMS: 1, Attachments: []Attachment{{ID: "a", Kind: "image", ThumbnailURL: srv.URL + "/thumb.jpg"}}})
	b.store.PutMedia("e2ee", "a", mediaRec{Kind: "image", WAType: "image", WATransport: inlineTransport(t, inline)})
	b.store.PutMessage(Message{ID: "ls", ThreadID: "5", TimestampMS: 2, Attachments: []Attachment{{ID: "a", Kind: "image", URL: srv.URL + "/full.png", ThumbnailURL: srv.URL + "/thumb.jpg"}}})
	b.store.PutMessage(Message{ID: "full", ThreadID: "5", TimestampMS: 3, Attachments: []Attachment{{ID: "a", Kind: "image", URL: srv.URL + "/full.png"}}})
	b.store.PutMessage(Message{ID: "vid", ThreadID: "5", TimestampMS: 4, Attachments: []Attachment{{ID: "a", Kind: "video", URL: srv.URL + "/full.mp4"}}})
	b.store.PutMessage(Message{ID: "poster", ThreadID: "5", TimestampMS: 5, Attachments: []Attachment{{ID: "a", Kind: "video", URL: srv.URL + "/full.mp4", ThumbnailURL: srv.URL + "/thumb.jpg"}}})

	res, err := b.Media(ctx, "5", "e2ee", "a", VariantThumb)
	if err != nil || res.Width != 48 || res.Height != 36 || thumbHits.Load()+fullHits.Load() != 0 {
		t.Fatalf("inline thumbnail must win without network: %+v %v", res, err)
	}
	if filepath.Dir(res.Path) != filepath.Join(dir, mediaDirName, thumbDirName) || res.MimeType != "image/jpeg" {
		t.Fatalf("unexpected thumb location %+v", res)
	}

	res, err = b.Media(ctx, "5", "ls", "a", VariantThumb)
	if err != nil || res.Width != 200 || thumbHits.Load() != 1 || fullHits.Load() != 0 {
		t.Fatalf("thumbnail_url must come before the full download: %+v %v", res, err)
	}
	if again, _ := b.Media(ctx, "5", "ls", "a", VariantThumb); again != res || thumbHits.Load() != 1 {
		t.Fatal("thumbs must be cached on disk")
	}

	res, err = b.Media(ctx, "5", "full", "a", VariantThumb)
	if err != nil || res.Width != maxThumbSide || res.Height != 480 || fullHits.Load() != 1 {
		t.Fatalf("full image must be downscaled to %d px: %+v %v", maxThumbSide, res, err)
	}
	info, _ := os.Stat(res.Path)
	if info.Size() >= int64(len(big)) {
		t.Fatal("thumb must be smaller than the full image")
	}

	if _, err := b.Media(ctx, "5", "vid", "a", VariantThumb); toErrorBody(err).Code != CodeNotFound {
		t.Fatalf("video without poster must be not_found, got %v", err)
	}
	if fullHits.Load() != 1 {
		t.Fatal("a video must never be downloaded to build a thumb")
	}
	if res, err := b.Media(ctx, "5", "poster", "a", VariantThumb); err != nil || res.Width != 200 {
		t.Fatalf("video poster from thumbnail_url failed: %+v %v", res, err)
	}

	cachePath := filepath.Join(dir, "reload.json")
	if err := b.store.Save(cachePath); err != nil {
		t.Fatal(err)
	}
	b2, _, dir2 := loggedInBackend(t)
	if err := b2.store.Load(cachePath); err != nil {
		t.Fatal(err)
	}
	srv.Close()
	res, err = b2.Media(ctx, "5", "e2ee", "a", VariantThumb)
	if err != nil || res.Width != 48 || filepath.Dir(filepath.Dir(filepath.Dir(res.Path))) != dir2 {
		t.Fatalf("inline thumbnail must survive a store reload: %+v %v", res, err)
	}
}

func TestSpeedDemoThumbAndMaxPages(t *testing.T) {
	h := newDemoHarness(t)
	h.send(`{"id":1,"cmd":"connect"}`)
	h.waitEvent(EventReady, 3*time.Second)
	thread := h.demo.mediaThreadID()
	before := h.demo.generated[thread]
	h.send(fmt.Sprintf(`{"id":2,"cmd":"media_history","params":{"thread_id":%q,"limit":60,"max_pages":0}}`, thread))
	var res MediaHistoryResult
	h.okResult(2, &res)
	h.demo.mu.Lock()
	after := h.demo.generated[thread]
	h.demo.mu.Unlock()
	if after != before || !res.HasMore {
		t.Fatalf("max_pages 0 must answer from the store only: generated %d -> %d, %+v", before, after, res)
	}
	h.send(fmt.Sprintf(`{"id":3,"cmd":"media_history","params":{"thread_id":%q,"limit":60,"max_pages":-1}}`, thread))
	if h.errCode(3) != CodeBadRequest {
		t.Fatal("negative max_pages must be rejected")
	}
	h.send(fmt.Sprintf(`{"id":4,"cmd":"media_history","params":{"thread_id":%q,"limit":5}}`, thread))
	h.okResult(4, &res)
	if len(res.Items) == 0 {
		t.Fatal("default max_pages must fetch")
	}
	item := res.Items[0]
	h.send(
		fmt.Sprintf(`{"id":5,"cmd":"media","params":{"thread_id":%q,"message_id":%q,"attachment_id":%q,"variant":"thumb"}}`, thread, item.MessageID, item.Attachment.ID),
		fmt.Sprintf(`{"id":6,"cmd":"media","params":{"thread_id":%q,"message_id":%q,"attachment_id":%q}}`, thread, item.MessageID, item.Attachment.ID),
		fmt.Sprintf(`{"id":7,"cmd":"media","params":{"thread_id":%q,"message_id":%q,"attachment_id":%q,"variant":"huge"}}`, thread, item.MessageID, item.Attachment.ID),
	)
	var thumb, full MediaResult
	h.okResult(5, &thumb)
	h.okResult(6, &full)
	if h.errCode(7) != CodeBadRequest {
		t.Fatal("unknown variant must be rejected")
	}
	thumbInfo, _ := os.Stat(thumb.Path)
	fullInfo, _ := os.Stat(full.Path)
	if thumb.Width == 0 || max(thumb.Width, thumb.Height) > demoThumbSide || thumbInfo == nil || fullInfo == nil || thumbInfo.Size() >= fullInfo.Size() {
		t.Fatalf("demo thumb must be a smaller image: %+v %+v", thumb, full)
	}
}

func TestSpeedMetaMediaHistoryMaxPagesZero(t *testing.T) {
	chat := waTypes.NewJID(testPeerID, waTypes.MessengerServer)
	paged := &pagedHistory{}
	for i := 0; i < 30; i++ {
		paged.items = append(paged.items, imageItem(chat, fmt.Sprintf("Z%03d", i), time.Now().Add(-time.Duration(i+1)*time.Hour)))
	}
	hb, _, _ := newHistoryBackend(t, &paged.fakeHistory)
	hb.mu.Lock()
	hb.sess.HistoryDevice = &securehistory.Device{ID: "dev", BackupID: "b"}
	hb.historyFactory = func(*messagix.Client) historyService { return paged }
	hb.identityFn = func(context.Context) (*keys.KeyPair, error) { return keys.NewKeyPair(), nil }
	hb.mu.Unlock()
	hb.ensureE2EEThread(chat)
	hb.store.PutMessage(Message{ID: "local", ThreadID: testPeerID, TimestampMS: time.Now().UnixMilli(), Attachments: []Attachment{{ID: "a", Kind: "image"}}})
	ctx := context.Background()
	res, err := hb.MediaHistory(ctx, testPeerID, 0, 10, []string{"image"}, 0)
	if err != nil || len(res.Items) != 1 || !res.HasMore || paged.fetches.Load() != 0 {
		t.Fatalf("max_pages 0 must answer from the store only: %+v %v fetches=%d", res, err, paged.fetches.Load())
	}
	res, err = hb.MediaHistory(ctx, testPeerID, 0, 10, []string{"image"}, 1)
	if err != nil || len(res.Items) <= 1 || paged.fetches.Load() != 1 {
		t.Fatalf("max_pages 1 must fetch exactly one page: %d items, %v, fetches=%d", len(res.Items), err, paged.fetches.Load())
	}
}

func downloadableTransport(t *testing.T, inline []byte) []byte {
	t.Helper()
	raw, err := proto.Marshal(&waMediaTransport.WAMediaTransport{
		Integral: &waMediaTransport.WAMediaTransport_Integral{DirectPath: proto.String("/v/t62/full"), MediaKey: []byte("full-key")},
		Ancillary: &waMediaTransport.WAMediaTransport_Ancillary{
			Thumbnail: &waMediaTransport.WAMediaTransport_Ancillary_Thumbnail{
				JPEGThumbnail: inline,
				DownloadableThumbnail: &waMediaTransport.WAMediaTransport_Ancillary_Thumbnail_DownloadableThumbnail{
					DirectPath:    proto.String("/v/t62/thumb"),
					FileSHA256:    []byte("sha"),
					FileEncSHA256: []byte("enc"),
				},
			},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func TestSpeedDownloadableE2EEThumb(t *testing.T) {
	b, _, dir := loggedInBackend(t)
	ctx := context.Background()
	fullPath := filepath.Join(dir, "full.png")
	os.WriteFile(fullPath, encodedImage(t, 1600, 1200, false), 0o600)
	var refs []waThumbRef
	fail := false
	b.waThumbFn = func(ctx context.Context, ref waThumbRef) ([]byte, error) {
		refs = append(refs, ref)
		if fail {
			return nil, fmt.Errorf("boom")
		}
		return encodedImage(t, 300, 200, true), nil
	}
	put := func(id, kind, waType string, inline []byte) {
		b.store.PutMessage(Message{ID: id, ThreadID: "5", TimestampMS: 1, Attachments: []Attachment{{ID: "a", Kind: kind}}})
		b.store.PutMedia(id, "a", mediaRec{Kind: kind, WAType: waType, WATransport: downloadableTransport(t, inline)})
		b.store.SetMediaPath(id, "a", fullPath, "image/png")
	}
	put("img", "image", string(whatsmeow.MediaImage), nil)
	put("vid", "video", string(whatsmeow.MediaVideo), nil)
	put("both", "image", "image", encodedImage(t, 40, 30, true))
	put("broken", "image", "image", nil)
	put("brokenvid", "video", string(whatsmeow.MediaVideo), nil)

	res, err := b.Media(ctx, "5", "img", "a", VariantThumb)
	if err != nil || res.Width != 300 || len(refs) != 1 {
		t.Fatalf("downloadable thumbnail must be preferred over the full file: %+v %v", res, err)
	}
	if r := refs[0]; r.DirectPath != "/v/t62/thumb" || string(r.MediaKey) != "full-key" || string(r.EncSHA256) != "enc" || string(r.SHA256) != "sha" || r.MediaType != "WhatsApp Image Keys" || r.MMSType != "thumbnail-image" {
		t.Fatalf("unexpected thumbnail download parameters %+v", r)
	}
	res, err = b.Media(ctx, "5", "vid", "a", VariantThumb)
	if err != nil || res.Width != 300 || refs[1].MMSType != "thumbnail-video" || refs[1].MediaType != "WhatsApp Video Keys" {
		t.Fatalf("video poster must come from the downloadable thumbnail: %+v %v %+v", res, err, refs)
	}
	if res, err := b.Media(ctx, "5", "both", "a", VariantThumb); err != nil || res.Width != 40 || len(refs) != 2 {
		t.Fatalf("inline bytes must win over the thumbnail download: %+v %v", res, err)
	}

	fail = true
	res, err = b.Media(ctx, "5", "broken", "a", VariantThumb)
	if err != nil || res.Width != maxThumbSide || len(refs) != 3 {
		t.Fatalf("a failed thumbnail download must fall back to the full image: %+v %v", res, err)
	}
	if _, err := b.Media(ctx, "5", "brokenvid", "a", VariantThumb); toErrorBody(err).Code != CodeNotFound {
		t.Fatalf("a video whose thumbnail fails must be not_found, got %v", err)
	}

	cachePath := filepath.Join(dir, "reload.json")
	if err := b.store.Save(cachePath); err != nil {
		t.Fatal(err)
	}
	reloaded := NewStore()
	if err := reloaded.Load(cachePath); err != nil {
		t.Fatal(err)
	}
	rec, _ := reloaded.Media("img", "a")
	if ref, ok := downloadableThumbnail(rec); !ok || ref.DirectPath != "/v/t62/thumb" {
		t.Fatal("thumbnail download details must survive a store reload")
	}
}
