package main

import (
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/rs/zerolog"
)

func TestDiagnosticsCommand(t *testing.T) {
	h := newDemoHarness(t)
	if err := os.WriteFile(filepath.Join(h.demo.dataDir, "probe.bin"), make([]byte, 1234), 0o600); err != nil {
		t.Fatal(err)
	}
	log := zerolog.New(newSanitizingWriter(io.Discard, recentErrors)).With().Str("component", "test").Logger()
	log.Error().Msg("diagnostics probe for alice@example.com")
	h.send(`{"id":1,"cmd":"connect"}`)
	waitFor(t, 5*time.Second, func() bool {
		h.demo.mu.Lock()
		defer h.demo.mu.Unlock()
		return h.demo.connected
	})
	h.send(`{"id":2,"cmd":"diagnostics","params":{}}`, `{"id":3,"cmd":"diagnostics","params":[]}`)
	var res DiagnosticsResult
	h.okResult(2, &res)
	if res.Version == "" || res.GoVersion != runtime.Version() || res.OS != runtime.GOOS+"/"+runtime.GOARCH {
		t.Fatalf("unexpected build info: %+v", res)
	}
	if !res.Connected || !res.E2EEReady || res.Threads == 0 || res.Messages < res.Threads || res.DataDirSizeBytes < 1234 {
		t.Fatalf("unexpected diagnostics: %+v", res)
	}
	found := false
	for _, line := range res.LastErrors {
		if strings.Contains(line, "alice@example.com") {
			t.Fatalf("last_errors must be redacted: %q", line)
		}
		found = found || strings.Contains(line, "diagnostics probe for [email]")
	}
	if !found || len(res.LastErrors) > maxRecentErrors {
		t.Fatalf("last_errors must carry the recent redacted errors: %v", res.LastErrors)
	}
	if code := h.errCode(3); code != CodeBadRequest {
		t.Fatalf("non-object params must be rejected, got %s", code)
	}
	resp := h.response(2)
	if !strings.Contains(string(resp["result"]), `"last_errors":[`) {
		t.Fatalf("last_errors must always be an array: %s", resp["result"])
	}
}

func TestDiagnosticsEmptyErrorsIsArray(t *testing.T) {
	if got := newErrorRing().snapshot(); got == nil || len(got) != 0 {
		t.Fatalf("an empty ring must snapshot to an empty, non-nil slice: %#v", got)
	}
}
