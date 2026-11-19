package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"sync"
	"time"

	"github.com/rs/zerolog"
)

const (
	avatarDirName      = "avatars"
	maxAvatarBytes     = 2 << 20
	avatarRefreshAge   = 7 * 24 * time.Hour
	avatarRetryAfter   = 10 * time.Minute
	avatarTimeout      = time.Minute
	avatarTempPattern  = "avatar-*.part"
	avatarExpiryMargin = 24 * time.Hour
)

type avatarFetcher func(ctx context.Context, rawURL string, f *os.File) (string, error)

type avatarFlight struct {
	done chan struct{}
	path string
	err  error
}

type avatarCache struct {
	dir     string
	fetch   avatarFetcher
	valid   func() func() bool
	parent  func() context.Context
	log     zerolog.Logger
	flights sync.Map
	failed  sync.Map
}

func newAvatarCache(cacheDir string, fetch avatarFetcher, valid func() func() bool) *avatarCache {
	return &avatarCache{dir: filepath.Join(cacheDir, avatarDirName), fetch: fetch, valid: valid, log: zerolog.Nop()}
}

func httpAvatarFetcher(ctx context.Context, rawURL string, f *os.File) (string, error) {
	return httpDownloadToFile(ctx, rawURL, maxAvatarBytes, f)
}

func avatarKey(u *url.URL) string {
	sum := sha256.Sum256([]byte(u.Path + "?" + u.Query().Get("stp")))
	return hex.EncodeToString(sum[:])
}

func (c *avatarCache) lookup(key string) (string, time.Time, bool) {
	matches, _ := filepath.Glob(filepath.Join(c.dir, key+".*"))
	for _, path := range matches {
		if info, err := os.Stat(path); err == nil && !info.IsDir() {
			return path, info.ModTime(), true
		}
	}
	return "", time.Time{}, false
}

func (c *avatarCache) Get(ctx context.Context, rawURL string) (string, error) {
	path, err := c.get(ctx, rawURL)
	return path, avatarError(err)
}

func (c *avatarCache) get(ctx context.Context, rawURL string) (string, error) {
	parsed, err := checkMediaURL(rawURL)
	if err != nil {
		return "", err
	}
	key := avatarKey(parsed)
	if path, modified, ok := c.lookup(key); ok {
		if time.Since(modified) > avatarRefreshAge && !c.recentlyFailed(rawURL) {
			c.start(key, rawURL)
		}
		return path, nil
	}
	flight := c.start(key, rawURL)
	select {
	case <-flight.done:
	case <-ctx.Done():
		return "", ctx.Err()
	}
	return flight.path, flight.err
}

func avatarError(err error) error {
	var be *BridgeError
	switch {
	case err == nil:
		return nil
	case errors.As(err, &be):
		return be
	case errors.Is(err, errMediaHostNotAllowed):
		return newError(CodeUnsupported, "avatar is not hosted on a Meta CDN")
	case errors.Is(err, errMediaExpired):
		return newError(CodeUnsupported, "avatar is no longer available: %v", err)
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		return err
	default:
		return newError(CodeInternal, "failed to download avatar: %v", err)
	}
}

func (c *avatarCache) recentlyFailed(rawURL string) bool {
	value, ok := c.failed.Load(rawURL)
	return ok && time.Since(value.(time.Time)) < avatarRetryAfter
}

func (c *avatarCache) start(key, rawURL string) *avatarFlight {
	flight := &avatarFlight{done: make(chan struct{})}
	if existing, loaded := c.flights.LoadOrStore(key, flight); loaded {
		return existing.(*avatarFlight)
	}
	stillValid := func() bool { return true }
	if c.valid != nil {
		stillValid = c.valid()
	}
	parent := context.Background()
	if c.parent != nil {
		parent = c.parent()
	}
	goSafe(c.log, "avatar", func() {
		defer func() {
			if flight.path == "" && flight.err == nil {
				flight.err = errors.New("avatar download aborted")
			}
			c.flights.Delete(key)
			close(flight.done)
		}()
		ctx, cancel := context.WithTimeout(parent, avatarTimeout)
		defer cancel()
		flight.path, flight.err = c.download(ctx, key, rawURL, stillValid)
		switch {
		case flight.err == nil:
			c.failed.Delete(rawURL)
		case !errors.Is(flight.err, context.Canceled):
			c.failed.Store(rawURL, time.Now())
		}
	})
	return flight
}

func (c *avatarCache) download(ctx context.Context, key, rawURL string, stillValid func() bool) (string, error) {
	if err := os.MkdirAll(c.dir, 0o700); err != nil {
		return "", err
	}
	f, err := os.CreateTemp(c.dir, avatarTempPattern)
	if err != nil {
		return "", err
	}
	tempName := f.Name()
	defer os.Remove(tempName)
	mimeType, err := c.fetch(ctx, rawURL, f)
	if err == nil {
		_, err = f.Seek(0, io.SeekStart)
	}
	sniff := make([]byte, mediaSniffBytes)
	n := 0
	if err == nil {
		n, _ = io.ReadFull(f, sniff)
	}
	if closeErr := f.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return "", err
	}
	mimeType = normalizeMime(mimeType, sniff[:n])
	switch mimeType {
	case "image/jpeg", "image/png", "image/gif", "image/webp":
	default:
		return "", newError(CodeUnsupported, "avatar is not an image")
	}
	if !stillValid() {
		return "", newError(CodeNotLoggedIn, "logged out during avatar download")
	}
	path := filepath.Join(c.dir, key+extensionFor(mimeType, ""))
	if old, _, ok := c.lookup(key); ok && old != path {
		_ = os.Remove(old)
	}
	if err := renameWithRetry(tempName, path); err != nil {
		return "", err
	}
	now := time.Now()
	_ = os.Chtimes(path, now, now)
	return path, nil
}

func (b *MetaBackend) Avatar(ctx context.Context, rawURL string) (string, error) {
	if err := b.requireLogin(); err != nil {
		return "", err
	}
	path, err := b.avatars.get(ctx, rawURL)
	if errors.Is(err, errMediaExpired) {
		if fresh := b.refreshAvatarURL(ctx, rawURL); fresh != "" {
			path, err = b.avatars.get(ctx, fresh)
		}
	}
	return path, avatarError(err)
}

func (b *MetaBackend) refreshAvatarURL(ctx context.Context, rawURL string) string {
	ids := b.store.ContactsWithAvatar(rawURL)
	if len(ids) == 0 {
		return ""
	}
	now := time.Now()
	var due []string
	for _, id := range ids {
		if last, ok := b.avatarRefreshed.Load(id); ok && now.Sub(last.(time.Time)) < avatarRetryAfter {
			continue
		}
		b.avatarRefreshed.Store(id, now)
		due = append(due, id)
	}
	if len(due) > 0 {
		fetchCtx, cancel := context.WithTimeout(ctx, contactFetchTimeout)
		b.refetchContacts(fetchCtx, due)
		cancel()
	}
	for _, id := range ids {
		if c, ok := b.store.Contact(id); ok && c.AvatarURL != "" && c.AvatarURL != rawURL {
			return c.AvatarURL
		}
	}
	return ""
}

func (b *MetaBackend) refetchContacts(ctx context.Context, ids []string) {
	if b.contactFetchFn != nil {
		b.contactFetchFn(ctx, ids)
		return
	}
	if client := b.currentClient(); client != nil {
		b.fetchContacts(ctx, client, ids)
	}
}

func (b *MetaBackend) refreshExpiringAvatars(ctx context.Context) {
	ids := b.store.ContactsWithAvatarExpiringBefore(time.Now().Add(avatarExpiryMargin))
	if len(ids) > maxContactsPerSync {
		ids = ids[:maxContactsPerSync]
	}
	if len(ids) > 0 {
		b.log.Info().Int("contacts", len(ids)).Msg("Refreshing contacts whose avatar URL is about to expire")
		b.refetchContacts(ctx, ids)
	}
}

func avatarURLExpiry(rawURL string) (time.Time, bool) {
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return time.Time{}, false
	}
	oe := parsed.Query().Get("oe")
	if oe == "" {
		return time.Time{}, false
	}
	sec, err := strconv.ParseInt(oe, 16, 64)
	if err != nil || sec <= 0 {
		return time.Time{}, false
	}
	return time.Unix(sec, 0), true
}

func (s *Store) ContactsWithAvatar(rawURL string) []string {
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return nil
	}
	key := avatarKey(parsed)
	s.mu.RLock()
	defer s.mu.RUnlock()
	var out []string
	for id, c := range s.contacts {
		if c.AvatarURL == "" {
			continue
		}
		if c.AvatarURL == rawURL {
			out = append(out, id)
			continue
		}
		if other, err := url.Parse(c.AvatarURL); err == nil && avatarKey(other) == key {
			out = append(out, id)
		}
	}
	slices.Sort(out)
	return out
}

func (s *Store) ContactsWithAvatarExpiringBefore(deadline time.Time) []string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var out []string
	for id, c := range s.contacts {
		if expiry, ok := avatarURLExpiry(c.AvatarURL); ok && expiry.Before(deadline) {
			out = append(out, id)
		}
	}
	slices.Sort(out)
	return out
}

func (b *MetaBackend) avatarStillValid() func() bool {
	gen := b.generation()
	return func() bool { return b.generation() == gen && b.requireLogin() == nil }
}
