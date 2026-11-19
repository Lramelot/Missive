package main

import (
	"context"
	"strconv"
	"sync/atomic"
	"time"

	"go.mau.fi/mautrix-meta/pkg/messagix"
	"go.mau.fi/mautrix-meta/pkg/messagix/methods"
	"go.mau.fi/mautrix-meta/pkg/messagix/socket"
	"go.mau.fi/mautrix-meta/pkg/messagix/table"
)

type presenceRec struct {
	Online       bool
	LastActiveMS int64
	ExpiresMS    int64
}

func (s *Store) SetPresence(userID string, online bool, lastActiveMS, expiresMS int64) bool {
	if userID == "" {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	old, existed := s.presence[userID]
	rec := presenceRec{Online: online, LastActiveMS: max(lastActiveMS, old.LastActiveMS), ExpiresMS: expiresMS}
	if existed && old == rec {
		return false
	}
	s.presence[userID] = rec
	return true
}

func (s *Store) presenceLocked(userID string) (*bool, int64) {
	rec, ok := s.presence[userID]
	if !ok {
		return nil, 0
	}
	online := rec.Online && (rec.ExpiresMS == 0 || rec.ExpiresMS > time.Now().UnixMilli())
	return &online, rec.LastActiveMS
}

func (s *Store) PresenceStats() (known, online int) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	now := time.Now().UnixMilli()
	for _, rec := range s.presence {
		known++
		if rec.Online && (rec.ExpiresMS == 0 || rec.ExpiresMS > now) {
			online++
		}
	}
	return
}

func (b *MetaBackend) countPresence(counter *atomic.Int64, source string) {
	if counter.Add(1) == 1 {
		b.log.Info().Str("source", source).Msg("First presence update received")
	}
}

func (s *Store) ClearPresence() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.presence) == 0 {
		return nil
	}
	users := make(map[string]bool, len(s.presence))
	for id := range s.presence {
		users[id] = true
	}
	s.presence = make(map[string]presenceRec)
	var out []string
	for id, rec := range s.threads {
		if users[s.peerOf(rec)] {
			out = append(out, id)
			continue
		}
		for member := range rec.Members {
			if users[member] {
				out = append(out, id)
				break
			}
		}
	}
	return out
}

func (s *Store) ThreadsWithUser(userID string) []string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var out []string
	for id, rec := range s.threads {
		if rec.IsGroup && rec.Members[userID] || !rec.IsGroup && s.peerOf(rec) == userID {
			out = append(out, id)
		}
	}
	return out
}

func (b *MetaBackend) emitThreadsWithUser(userID string) {
	var views []Thread
	for _, id := range b.store.ThreadsWithUser(userID) {
		if view, ok := b.store.ThreadView(id); ok {
			views = append(views, view)
		}
	}
	if len(views) > 0 {
		b.out.Emit(EventThreads, ThreadsEvent{Threads: views})
	}
}

func (b *MetaBackend) reportAppState(ctx context.Context, client *messagix.Client, active bool) {
	state := table.BACKGROUND
	if active {
		state = table.FOREGROUND
	}
	task := &socket.ReportAppStateTask{AppState: state, RequestID: strconv.FormatInt(methods.GenerateEpochID(), 10)}
	if b.appStateFn != nil {
		b.appStateFn(task)
		return
	}
	if client == nil {
		return
	}
	if err := client.ExecuteStatelessTask(ctx, task); err != nil {
		b.log.Debug().Err(err).Msg("Failed to report app state")
	}
}
