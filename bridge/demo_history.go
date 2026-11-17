package main

import (
	"context"
	"time"
)

const (
	demoCorrectPIN  = "123456"
	demoPINAttempts = 10
	demoBumpThreads = 3
)

func (d *DemoBackend) HistoryStatus(ctx context.Context) (HistoryStatusResult, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if err := d.requireLogin(); err != nil {
		return HistoryStatusResult{}, err
	}
	if !d.connected {
		return HistoryStatusResult{}, newError(CodeNotConnected, "not connected")
	}
	return HistoryStatusResult{Backup: BackupAvailable, Restored: d.historyRestored}, nil
}

func (d *DemoBackend) RestoreHistory(ctx context.Context, pin string) error {
	d.mu.Lock()
	if err := d.requireLogin(); err != nil {
		d.mu.Unlock()
		return err
	}
	if !d.connected {
		d.mu.Unlock()
		return newError(CodeNotConnected, "not connected")
	}
	if d.pinAttempts <= 0 {
		d.mu.Unlock()
		return newError(CodePINLocked, "no PIN attempts remaining")
	}
	if pin != demoCorrectPIN {
		d.pinAttempts--
		remaining := d.pinAttempts
		d.mu.Unlock()
		if remaining <= 0 {
			return newError(CodePINLocked, "no PIN attempts remaining")
		}
		bad := newError(CodeBadPIN, "wrong PIN, %d attempts remaining", remaining)
		bad.Data = map[string]any{"attempts_remaining": remaining}
		return bad
	}
	d.historyRestored = true
	d.pinAttempts = demoPINAttempts
	var bumped []string
	now := time.Now()
	for _, th := range d.store.ListThreads(0) {
		if !th.E2EE || len(bumped) == demoBumpThreads {
			continue
		}
		ts := now.Add(-time.Duration(5+len(bumped)*7) * time.Minute).UnixMilli()
		msg := d.makeMessage(th.ID, true, ts, len(bumped)%2 == 1)
		msg.Deleted = false
		if msg.Text == "" && len(msg.Attachments) == 0 {
			msg.Text = demoLines[d.rng.IntN(len(demoLines))]
		}
		d.store.PutMessage(msg)
		d.store.UpdateThread(th.ID, func(rec *threadRec) {
			if ts > rec.LastActivityMS {
				rec.LastActivityMS = ts
				rec.Snippet = snippetFor(msg)
			}
		})
		bumped = append(bumped, th.ID)
	}
	d.mu.Unlock()
	if views := d.store.ThreadViews(bumped); len(views) > 0 {
		d.out.Emit(EventThreads, ThreadsEvent{Threads: views})
	}
	return nil
}
