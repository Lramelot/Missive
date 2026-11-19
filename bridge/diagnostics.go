package main

import (
	"context"
	"runtime"
	"runtime/debug"
)

var bridgeVersion = "dev"

type DiagnosticsResult struct {
	Version           string   `json:"version"`
	GoVersion         string   `json:"go_version"`
	OS                string   `json:"os"`
	Connected         bool     `json:"connected"`
	E2EEReady         bool     `json:"e2ee_ready"`
	Threads           int      `json:"threads"`
	Messages          int      `json:"messages"`
	DataDirSizeBytes  int64    `json:"data_dir_size_bytes"`
	CacheDirSizeBytes int64    `json:"cache_dir_size_bytes"`
	LastErrors        []string `json:"last_errors"`
}

type diagnosticsState struct {
	Connected bool
	E2EEReady bool
	Threads   int
	Messages  int
	DataDir   string
	CacheDir  string
}

type diagnosticsSource interface {
	diagnosticsState() diagnosticsState
}

func versionString() string {
	version := bridgeVersion
	info, ok := debug.ReadBuildInfo()
	if !ok {
		return version
	}
	var revision string
	var dirty bool
	for _, s := range info.Settings {
		switch s.Key {
		case "vcs.revision":
			revision = s.Value
		case "vcs.modified":
			dirty = s.Value == "true"
		}
	}
	if len(revision) > 12 {
		revision = revision[:12]
	}
	if revision != "" {
		version += "+" + revision
		if dirty {
			version += ".dirty"
		}
	}
	return version
}

func (d *Dispatcher) diagnostics(ctx context.Context) (DiagnosticsResult, error) {
	res := DiagnosticsResult{
		Version:    versionString(),
		GoVersion:  runtime.Version(),
		OS:         runtime.GOOS + "/" + runtime.GOARCH,
		LastErrors: recentErrors.snapshot(),
	}
	if src, ok := d.backend.(diagnosticsSource); ok {
		st := src.diagnosticsState()
		res.Connected = st.Connected
		res.E2EEReady = st.E2EEReady
		res.Threads = st.Threads
		res.Messages = st.Messages
		if st.DataDir != "" {
			res.DataDirSizeBytes = dirSize(st.DataDir)
		}
		if st.CacheDir != "" && st.CacheDir != st.DataDir {
			res.CacheDirSizeBytes = dirSize(st.CacheDir)
		}
	} else if status, err := d.backend.Status(ctx); err == nil {
		res.Connected = status.Connected
	}
	return res, nil
}

func (b *MetaBackend) diagnosticsState() diagnosticsState {
	status, _ := b.Status(context.Background())
	st := diagnosticsState{
		Connected: status.Connected,
		Threads:   b.store.ThreadCount(),
		Messages:  b.store.TotalMessageCount(),
		DataDir:   b.dataDir,
		CacheDir:  b.cacheDir,
	}
	if cli := b.currentE2EE(); cli != nil {
		st.E2EEReady = cli.IsConnected() && cli.IsLoggedIn()
	}
	return st
}

func (d *DemoBackend) diagnosticsState() diagnosticsState {
	d.mu.Lock()
	connected := d.connected
	loggedIn := d.loggedIn
	d.mu.Unlock()
	return diagnosticsState{
		Connected: connected,
		E2EEReady: connected && loggedIn,
		Threads:   d.store.ThreadCount(),
		Messages:  d.store.TotalMessageCount(),
		DataDir:   d.dataDir,
		CacheDir:  d.cacheDir,
	}
}

func (s *Store) TotalMessageCount() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	total := 0
	for _, msgs := range s.messages {
		total += len(msgs)
	}
	return total
}
