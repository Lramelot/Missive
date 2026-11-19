package main

import (
	"context"
	"os"
)

func (b *MetaBackend) archiveThread(threadID string) (threadRec, error) {
	rec, _, err := b.threadAndReply(threadID, "")
	return rec, err
}

func (b *MetaBackend) archiveStore() *Store { return b.store }

func (b *MetaBackend) archiveOut() Emitter { return b.out }

func (b *MetaBackend) archiveJobs() *archiveJobs { return &b.archive }

func (b *MetaBackend) loadFullHistory(ctx context.Context, rec threadRec, progress func()) {
	idle := 0
	for page := 0; page < maxArchivePages; page++ {
		if more, known := b.store.HasMoreBefore(rec.ID); known && !more {
			return
		}
		if page > 0 && !archivePause(ctx) {
			return
		}
		var progressed, possible bool
		if b.olderPageFn != nil {
			before := b.store.MessageCount(rec.ID)
			possible = b.olderPageFn(ctx, rec, 0, mediaHistoryArchivePage)
			progressed = b.store.MessageCount(rec.ID) > before
		} else {
			progressed, possible = b.mediaHistoryPage(ctx, rec)
		}
		progress()
		if !possible || ctx.Err() != nil {
			return
		}
		if progressed {
			idle = 0
			continue
		}
		idle++
		if idle >= maxArchiveIdlePages {
			return
		}
	}
}

func (b *MetaBackend) archiveMedia(ctx context.Context, msg Message, att Attachment, dst *os.File) (string, error) {
	rec, err := b.mediaSource(msg.ID, att)
	if err != nil {
		return "", err
	}
	if fileExists(rec.Path) {
		return rec.MimeType, copyFileTo(rec.Path, dst)
	}
	return b.downloadMediaTo(ctx, rec, att, dst)
}

func (b *MetaBackend) SearchFiltered(ctx context.Context, f SearchFilter, limit int) ([]SearchHit, error) {
	if err := b.requireLogin(); err != nil {
		return nil, err
	}
	return b.store.SearchFiltered(f, limit), nil
}

func (b *MetaBackend) ExportThread(ctx context.Context, p ExportThreadParams) (ExportThreadResult, error) {
	defer b.store.TrimThread(p.ThreadID, maxPersistedMessagesPerThread, b.store.MessageIDs(p.ThreadID))
	return exportThread(ctx, b, p)
}

func (b *MetaBackend) SaveMedia(ctx context.Context, p SaveMediaParams) (SaveMediaResult, error) {
	defer b.store.TrimThread(p.ThreadID, maxPersistedMessagesPerThread, b.store.MessageIDs(p.ThreadID))
	return saveThreadMedia(ctx, b, p)
}

func (b *MetaBackend) CancelArchive(ctx context.Context) (CancelArchiveResult, error) {
	return CancelArchiveResult{Cancelled: b.archive.stop()}, nil
}

func (d *DemoBackend) archiveThread(threadID string) (threadRec, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.requireThread(threadID)
}

func (d *DemoBackend) archiveStore() *Store { return d.store }

func (d *DemoBackend) archiveOut() Emitter { return d.out }

func (d *DemoBackend) archiveJobs() *archiveJobs { return &d.archive }

func (d *DemoBackend) loadFullHistory(ctx context.Context, rec threadRec, progress func()) {
	for ctx.Err() == nil {
		d.mu.Lock()
		if d.generated[rec.ID] >= d.historyCap(rec.ID) {
			d.mu.Unlock()
			return
		}
		before := d.generated[rec.ID]
		d.generateOlder(rec.ID, rec.E2EE, demoMediaHistoryPage)
		stalled := d.generated[rec.ID] == before
		d.mu.Unlock()
		progress()
		if stalled {
			return
		}
	}
}

func (d *DemoBackend) archiveMedia(ctx context.Context, msg Message, att Attachment, dst *os.File) (string, error) {
	if rec, ok := d.store.Media(msg.ID, att.ID); ok && fileExists(rec.Path) {
		return rec.MimeType, copyFileTo(rec.Path, dst)
	}
	data, mimeType, err := demoMediaData(msg.ID, att)
	if err != nil {
		return "", err
	}
	_, err = dst.Write(data)
	return mimeType, err
}

func (d *DemoBackend) SearchFiltered(ctx context.Context, f SearchFilter, limit int) ([]SearchHit, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if err := d.requireLogin(); err != nil {
		return nil, err
	}
	return d.store.SearchFiltered(f, limit), nil
}

func (d *DemoBackend) ExportThread(ctx context.Context, p ExportThreadParams) (ExportThreadResult, error) {
	return exportThread(ctx, d, p)
}

func (d *DemoBackend) SaveMedia(ctx context.Context, p SaveMediaParams) (SaveMediaResult, error) {
	return saveThreadMedia(ctx, d, p)
}

func (d *DemoBackend) CancelArchive(ctx context.Context) (CancelArchiveResult, error) {
	return CancelArchiveResult{Cancelled: d.archive.stop()}, nil
}
