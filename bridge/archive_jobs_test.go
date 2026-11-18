package main

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestArchiveSingleFlightAndCancel(t *testing.T) {
	d, _ := demoArchive(t)
	ctx := context.Background()
	dir := t.TempDir()
	errc := make(chan error, 1)
	go func() {
		_, err := d.ExportThread(ctx, ExportThreadParams{ThreadID: d.mediaThreadID(), Dir: dir, Format: ExportHTML, IncludeMedia: true})
		errc <- err
	}()
	waitFor(t, 5*time.Second, func() bool {
		d.archive.mu.Lock()
		defer d.archive.mu.Unlock()
		return d.archive.cancel != nil
	})
	_, err := d.SaveMedia(ctx, SaveMediaParams{ThreadID: d.mediaThreadID(), Dir: dir})
	if body := toErrorBody(err); body.Code != CodeBusy {
		t.Fatalf("a second archive job must be refused while one runs: %v", err)
	}
	if res, _ := d.CancelArchive(ctx); !res.Cancelled {
		t.Fatal("cancel_archive must report the running job")
	}
	select {
	case err := <-errc:
		if toErrorBody(err).Code != CodeCancelled {
			t.Fatalf("a cancelled export must fail with %q, got %v", CodeCancelled, err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("cancel_archive did not stop the export")
	}
	if entries, _ := os.ReadDir(dir); len(entries) != 0 {
		t.Fatalf("a cancelled export must not leave a partial folder: %d entries", len(entries))
	}
	if res, _ := d.CancelArchive(ctx); res.Cancelled {
		t.Fatal("nothing runs anymore")
	}
}

func TestMetaArchivePacesPagesAndCancels(t *testing.T) {
	b, _, _ := loggedInBackend(t)
	previous := archivePageDelay
	archivePageDelay = time.Hour
	t.Cleanup(func() { archivePageDelay = previous })
	b.store.UpdateThread("42", func(r *threadRec) { r.Name = "Groupe"; r.IsGroup = true })
	var pages atomic.Int32
	b.olderPageFn = func(ctx context.Context, rec threadRec, beforeMS int64, count int) bool {
		n := pages.Add(1)
		b.store.PutMessage(Message{ID: "mid.$p" + string(rune('a'+n)), ThreadID: rec.ID, Text: "x", TimestampMS: readBaseMS - int64(n)*1000})
		return true
	}
	errc := make(chan error, 1)
	go func() {
		_, err := b.ExportThread(context.Background(), ExportThreadParams{ThreadID: "42", Dir: t.TempDir(), Format: ExportJSON})
		errc <- err
	}()
	waitFor(t, 3*time.Second, func() bool { return pages.Load() == 1 })
	time.Sleep(50 * time.Millisecond)
	if res, _ := b.CancelArchive(context.Background()); !res.Cancelled {
		t.Fatal("the export must be cancellable while it waits between pages")
	}
	select {
	case err := <-errc:
		if toErrorBody(err).Code != CodeCancelled {
			t.Fatalf("expected %q, got %v", CodeCancelled, err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("the pause between Meta pages is not cancellable")
	}
	if n := pages.Load(); n != 1 {
		t.Fatalf("Meta pages must be spaced out, fetched %d", n)
	}
}

func TestMetaArchiveMediaBypassesCacheAndHandlesCollisions(t *testing.T) {
	payloads := map[string][]byte{"/a": bytes.Repeat([]byte("A"), 64), "/b": bytes.Repeat([]byte("B"), 80)}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/jpeg")
		_, _ = w.Write(payloads[r.URL.Path])
	}))
	t.Cleanup(srv.Close)
	allowTestServer(t, srv)
	b, _, dataDir := loggedInBackend(t)
	b.store.UpdateThread("42", func(r *threadRec) { r.Name = "Album"; r.IsGroup = true })
	b.store.SetHasMoreBefore("42", false)
	for i, path := range []string{"/a", "/b"} {
		b.store.PutMessage(Message{ID: "mid.$" + path[1:], ThreadID: "42", SenderID: "7", TimestampMS: readBaseMS + int64(i*100),
			Attachments: []Attachment{{ID: "img", Kind: "image", URL: srv.URL + path, FileName: "image.jpg"}}})
	}
	dir := t.TempDir()
	first, err := b.SaveMedia(context.Background(), SaveMediaParams{ThreadID: "42", Dir: dir})
	if err != nil {
		t.Fatal(err)
	}
	if first.Saved != 2 || first.Skipped != 0 {
		t.Fatalf("two distinct media sharing a name must both be saved: %+v", first)
	}
	stem := mediaFileStem(readBaseMS, Attachment{ID: "img", FileName: "image.jpg"}, "")
	for _, name := range []string{stem + ".jpg", stem + " (2).jpg"} {
		data, err := os.ReadFile(filepath.Join(first.Dir, name))
		if err != nil || (!bytes.Equal(data, payloads["/a"]) && !bytes.Equal(data, payloads["/b"])) {
			t.Fatalf("missing or wrong file %q: %v", name, err)
		}
		if runtime.GOOS == "windows" {
			zone, err := os.ReadFile(filepath.Join(first.Dir, name) + zoneIdentifierStream)
			if err != nil || !strings.Contains(string(zone), "ZoneId=3") {
				t.Fatalf("saved media must carry the Mark-of-the-Web: %q %v", zone, err)
			}
		}
	}
	second, err := b.SaveMedia(context.Background(), SaveMediaParams{ThreadID: "42", Dir: dir})
	if err != nil {
		t.Fatal(err)
	}
	if second.Saved != 0 || second.Skipped != 2 {
		t.Fatalf("identical files already present are skipped: %+v", second)
	}
	if rec, ok := b.store.Media("mid.$a", "img"); ok && rec.Path != "" {
		t.Fatalf("archive downloads must not fill the private cache: %+v", rec)
	}
	if entries, _ := os.ReadDir(filepath.Join(dataDir, mediaDirName)); len(entries) != 0 {
		t.Fatalf("archive downloads must not be kept in the bridge cache: %d files", len(entries))
	}
	res, err := b.ExportThread(context.Background(), ExportThreadParams{ThreadID: "42", Dir: dir, Format: ExportHTML, IncludeMedia: true})
	if err != nil || res.Media != 2 {
		t.Fatalf("export with media failed: %+v %v", res, err)
	}
	if runtime.GOOS == "windows" {
		if zone, err := os.ReadFile(filepath.Join(res.Path, "conversation.html") + zoneIdentifierStream); err != nil || !strings.Contains(string(zone), "ZoneId=3") {
			t.Fatalf("the exported page must carry the Mark-of-the-Web: %v", err)
		}
	}
}
