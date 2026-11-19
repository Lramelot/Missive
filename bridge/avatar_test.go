package main

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

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

func TestAvatarCacheAndRefresh(t *testing.T) {
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
