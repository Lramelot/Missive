package main

import (
	"cmp"
	"context"
	"errors"
	"os"
	"sync"
	"time"
)

func (b *MetaBackend) Media(ctx context.Context, threadID, messageID, attachmentID, variant string) (MediaResult, error) {
	if err := b.requireLogin(); err != nil {
		return MediaResult{}, err
	}
	msg, ok := b.store.MessageInThread(threadID, messageID)
	if !ok {
		return MediaResult{}, newError(CodeNotFound, "unknown message %s in thread %s", messageID, threadID)
	}
	att, ok := findAttachment(msg, attachmentID)
	if !ok {
		return MediaResult{}, newError(CodeNotFound, "unknown attachment %s", attachmentID)
	}
	if variant == VariantThumb {
		return b.mediaThumb(ctx, messageID, att)
	}
	return b.fullMedia(ctx, messageID, att)
}

func (b *MetaBackend) mediaSource(messageID string, att Attachment) (mediaRec, error) {
	rec, ok := b.store.Media(messageID, att.ID)
	if !ok {
		if att.Kind == "link" || att.URL == "" {
			return mediaRec{}, newError(CodeNotFound, "attachment %s has no downloadable media", att.ID)
		}
		rec = mediaRec{Kind: att.Kind, URL: att.URL, MimeType: att.MimeType, FileName: att.FileName}
	}
	return rec, nil
}

func (b *MetaBackend) downloadMediaTo(ctx context.Context, rec mediaRec, att Attachment, f *os.File) (string, error) {
	maxBytes := int64(maxMediaBytes)
	if att.Kind == "link" || att.Kind == "other" {
		maxBytes = maxPreviewBytes
	}
	if rec.WATransport == nil && rec.ReceiverFetchID == "" && rec.URL == "" {
		return "", newError(CodeNotFound, "attachment %s has no downloadable media", att.ID)
	}
	var mimeType string
	var err error
	if rec.WATransport != nil || rec.ReceiverFetchID != "" {
		mimeType, err = b.downloadE2EEMedia(ctx, rec, maxBytes, f)
	} else {
		mimeType, err = httpDownloadToFile(ctx, rec.URL, maxBytes, f)
		mimeType = cmp.Or(rec.MimeType, mimeType)
	}
	if err == nil {
		return mimeType, nil
	}
	var be *BridgeError
	switch {
	case errors.As(err, &be):
		return "", be
	case errors.Is(err, errMediaHostNotAllowed):
		return "", newError(CodeUnsupported, "media is not hosted on a Meta CDN")
	case errors.Is(err, errMediaExpired):
		return "", newError(CodeUnsupported, "media is no longer available on Meta's servers: %v", err)
	case rec.ExpiresMS > 0 && rec.ExpiresMS < time.Now().UnixMilli():
		return "", newError(CodeUnsupported, "media URL expired: %v", err)
	default:
		return "", newError(CodeInternal, "failed to download media: %v", err)
	}
}

func (b *MetaBackend) fullMedia(ctx context.Context, messageID string, att Attachment) (MediaResult, error) {
	unlock := b.lockMediaKey(mediaKey(messageID, att.ID))
	defer unlock()
	rec, err := b.mediaSource(messageID, att)
	if err != nil {
		return MediaResult{}, err
	}
	if fileExists(rec.Path) {
		return MediaResult{Path: rec.Path, MimeType: rec.MimeType}, nil
	}
	f, err := newMediaTemp(b.cacheDir)
	if err != nil {
		return MediaResult{}, newError(CodeInternal, "%v", err)
	}
	mimeType, err := b.downloadMediaTo(ctx, rec, att, f)
	if err != nil {
		discardMediaTemp(f)
		return MediaResult{}, err
	}
	path, mimeType, err := finalizeMediaTemp(f, mimeType, cmp.Or(rec.FileName, att.FileName))
	if err != nil {
		return MediaResult{}, newError(CodeInternal, "%v", err)
	}
	b.store.SetMediaPath(messageID, att.ID, path, mimeType)
	return MediaResult{Path: path, MimeType: mimeType}, nil
}

type mediaLock struct {
	mu   sync.Mutex
	refs int
}

func (b *MetaBackend) lockMediaKey(key string) func() {
	b.mediaLocksMu.Lock()
	lock := b.mediaLocks[key]
	if lock == nil {
		lock = &mediaLock{}
		b.mediaLocks[key] = lock
	}
	lock.refs++
	b.mediaLocksMu.Unlock()
	lock.mu.Lock()
	return func() {
		lock.mu.Unlock()
		b.mediaLocksMu.Lock()
		lock.refs--
		if lock.refs == 0 {
			delete(b.mediaLocks, key)
		}
		b.mediaLocksMu.Unlock()
	}
}

func (b *MetaBackend) mediaLockCount() int {
	b.mediaLocksMu.Lock()
	defer b.mediaLocksMu.Unlock()
	return len(b.mediaLocks)
}
