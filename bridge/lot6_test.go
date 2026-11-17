package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/rs/zerolog"
)

func requireDPAPI(t *testing.T) {
	t.Helper()
	if !secretsEncrypted {
		t.Skip("DPAPI is only available on Windows")
	}
}

func TestSecretFileRoundTripIsEncryptedAtRest(t *testing.T) {
	requireDPAPI(t)
	path := filepath.Join(t.TempDir(), cookiesFile)
	plain := []byte(`{"c_user":"4242","xs":"very-secret-xs","datr":"d"}`)
	if err := writeSecretFile(path, plain); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.HasPrefix(raw, secretMagic) || bytes.Contains(raw, []byte("very-secret-xs")) || bytes.Contains(raw, []byte("c_user")) {
		t.Fatalf("secret file must be DPAPI-encrypted on disk, got %q", raw)
	}
	got, err := readSecretFile(path, zerolog.Nop())
	if err != nil || !bytes.Equal(got, plain) {
		t.Fatalf("round trip failed: %q %v", got, err)
	}
	tampered := append([]byte(nil), raw...)
	tampered[len(tampered)-1] ^= 0x01
	if err := os.WriteFile(path, tampered, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := readSecretFile(path, zerolog.Nop()); !errors.Is(err, errSecretUnreadable) {
		t.Fatalf("a tampered file must be unreadable, got %v", err)
	}
	empty := filepath.Join(t.TempDir(), stateFile)
	if err := writeSecretFile(empty, nil); err != nil {
		t.Fatal(err)
	}
	if got, err := readSecretFile(empty, zerolog.Nop()); err != nil || len(got) != 0 {
		t.Fatalf("empty payload round trip failed: %q %v", got, err)
	}
}

func TestSecretFileMigratesPlaintextAndWipesIt(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, sessionFile)
	plain := []byte(`{"user":{"id":"4242","name":"Alice Secret"}}`)
	if err := os.WriteFile(path, plain, 0o644); err != nil {
		t.Fatal(err)
	}
	leftover := filepath.Join(dir, sessionFile+".123456.tmp")
	if err := os.WriteFile(leftover, plain, 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := readSecretFile(path, zerolog.Nop())
	if err != nil || !bytes.Equal(got, plain) {
		t.Fatalf("plaintext must still be readable during migration: %q %v", got, err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !secretsEncrypted {
		if info, err := os.Stat(path); err != nil || info.Mode().Perm() != 0o600 {
			t.Fatalf("plaintext session must be restricted to 0600: %v %v", info, err)
		}
		return
	}
	if !bytes.HasPrefix(raw, secretMagic) || bytes.Contains(raw, []byte("Alice Secret")) {
		t.Fatalf("plaintext session was not rewritten encrypted: %q", raw)
	}
	if _, err := os.Stat(leftover); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("plaintext leftovers must be removed: %v", err)
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != 1 {
		t.Fatalf("migration must leave exactly the encrypted file, got %d entries", len(entries))
	}
	again, err := readSecretFile(path, zerolog.Nop())
	if err != nil || !bytes.Equal(again, plain) {
		t.Fatalf("migrated file must decrypt: %q %v", again, err)
	}
}

func TestWipeFileOverwritesContent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "wipe")
	if err := os.WriteFile(path, []byte("secret-cookie-value"), 0o600); err != nil {
		t.Fatal(err)
	}
	wipeFile(path)
	raw, err := os.ReadFile(path)
	if err != nil || len(raw) != len("secret-cookie-value") || !bytes.Equal(raw, make([]byte, len(raw))) {
		t.Fatalf("wipe must zero the file in place: %q %v", raw, err)
	}
}

func TestUndecryptableSessionMeansLoggedOut(t *testing.T) {
	dir := t.TempDir()
	garbage := append(append([]byte{}, secretMagic...), []byte("not a dpapi blob from this user")...)
	for _, name := range []string{sessionFile, cookiesFile, stateFile, cacheFile} {
		if err := os.WriteFile(filepath.Join(dir, name), garbage, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	b, err := NewMetaBackend(dir, NewOutput(&syncBuffer{}, zerolog.Nop()), zerolog.Nop())
	if err != nil {
		t.Fatalf("an undecryptable session must not prevent startup: %v", err)
	}
	defer b.Close()
	status, _ := b.Status(context.Background())
	if status.LoggedIn || status.User != nil {
		t.Fatalf("an undecryptable session must read as logged out: %+v", status)
	}
	if b.loadCachedState(nil) {
		t.Fatal("an undecryptable connection state must not be used")
	}
	if fileExists(filepath.Join(dir, stateFile)) {
		t.Fatal("an undecryptable connection state must be discarded")
	}
}

func TestUndecryptableCookiesOnlyMeansLoggedOut(t *testing.T) {
	requireDPAPI(t)
	dir := t.TempDir()
	if err := writeSecretFile(filepath.Join(dir, sessionFile), []byte(`{"user":{"id":"4242","name":"Me"}}`)); err != nil {
		t.Fatal(err)
	}
	garbage := append(append([]byte{}, secretMagic...), 1, 2, 3, 4)
	if err := os.WriteFile(filepath.Join(dir, cookiesFile), garbage, 0o600); err != nil {
		t.Fatal(err)
	}
	b, err := NewMetaBackend(dir, NewOutput(&syncBuffer{}, zerolog.Nop()), zerolog.Nop())
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	if status, _ := b.Status(context.Background()); status.LoggedIn {
		t.Fatalf("undecryptable cookies must read as logged out: %+v", status)
	}
}

func TestPersistedPlaintextSessionIsMigratedOnStartup(t *testing.T) {
	requireDPAPI(t)
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, sessionFile), []byte(`{"user":{"id":"4242","name":"Me"}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, cookiesFile), []byte(`{"c_user":"4242","xs":"secret-xs","datr":"d"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	b, err := NewMetaBackend(dir, NewOutput(&syncBuffer{}, zerolog.Nop()), zerolog.Nop())
	if err != nil {
		t.Fatal(err)
	}
	status, _ := b.Status(context.Background())
	if !status.LoggedIn || status.User == nil || status.User.ID != "4242" {
		t.Fatalf("migrated session must stay logged in: %+v", status)
	}
	b.store.PutMessage(Message{ID: "m1", ThreadID: "t1", Text: "secret message body", TimestampMS: 1})
	b.persist()
	b.Close()
	for _, name := range []string{sessionFile, cookiesFile, cacheFile} {
		raw, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if !bytes.HasPrefix(raw, secretMagic) || bytes.Contains(raw, []byte("secret-xs")) || bytes.Contains(raw, []byte("secret message body")) {
			t.Fatalf("%s must be encrypted at rest: %q", name, raw)
		}
	}
	reloaded, err := NewMetaBackend(dir, NewOutput(&syncBuffer{}, zerolog.Nop()), zerolog.Nop())
	if err != nil {
		t.Fatal(err)
	}
	defer reloaded.Close()
	if status, _ := reloaded.Status(context.Background()); !status.LoggedIn {
		t.Fatalf("encrypted session must load back: %+v", status)
	}
	if msgs := reloaded.store.Messages("t1", 0, 0); len(msgs) != 1 || msgs[0].Text != "secret message body" {
		t.Fatalf("encrypted cache must load back: %+v", msgs)
	}
}

func TestDeviceStoreCreatedPrivate(t *testing.T) {
	path := filepath.Join(t.TempDir(), waDBFile)
	if err := ensurePrivateFile(path); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" && info.Mode().Perm() != 0o600 {
		t.Fatalf("device store must be 0600, got %v", info.Mode().Perm())
	}
}

func TestRedactText(t *testing.T) {
	home := homeDir
	cases := map[string][]string{
		`Get "https://scontent.xx.fbcdn.net/v/t1/abc.jpg?stp=dst&_nc_ohc=AbC&oh=00_secret&oe=65": EOF`: {"_nc_ohc", "00_secret", "stp=dst"},
		"cookie xs=48%3Asecretvalue; c_user=100004781234500; datr=abcdef":                              {"secretvalue", "100004781234500", "abcdef"},
		`response {"text":"bonjour Alice","name":"Alice Martin","token":"tok123"}`:                     {"bonjour", "Alice", "tok123"},
		"mail me at alice.martin@example.com or +33 6 12 34 56 78":                                     {"alice.martin", "12 34 56"},
		"from 33612345678@s.whatsapp.net":                                                              {"33612345678"},
		"Authorization: Bearer abcdefghijklmnopqrstuvwxyz":                                             {"abcdefghijklmnop"},
	}
	for input, forbidden := range cases {
		got := redactText(input)
		for _, f := range forbidden {
			if strings.Contains(got, f) {
				t.Errorf("redactText(%q) = %q still contains %q", input, got, f)
			}
		}
	}
	if got := redactText("https://www.messenger.com/t/123"); got != "https://www.messenger.com/t/123" {
		t.Errorf("URLs without query must be kept: %q", got)
	}
	if home != "" {
		if got := redactText(filepath.Join(home, "AppData", "Missive")); strings.Contains(got, home) || !strings.HasPrefix(got, "~") {
			t.Errorf("home directory must be shortened: %q", got)
		}
	}
	if got := redactText(strings.Repeat("a", 5000)); len([]rune(got)) > maxLogValueLength+1 {
		t.Errorf("long values must be truncated, got %d runes", len([]rune(got)))
	}
	if got := redactText("thread 1234567890 failed after 3 attempts"); got != "thread 1234567890 failed after 3 attempts" {
		t.Errorf("technical ids must be kept: %q", got)
	}
}

func TestSanitizingWriterRedactsFieldsAndRecordsErrors(t *testing.T) {
	var out bytes.Buffer
	ring := newErrorRing()
	log := zerolog.New(newSanitizingWriter(&out, ring)).With().Timestamp().Str("component", "meta").Logger()
	log.Info().Str("text", "salut c'est Alice").Str("thread_id", "t-42").Msg("Got message")
	log.Warn().Any("frame", map[string]any{"body": "secret body"}).Any("step", []string{"coucou toi"}).Msg("Unknown frame")
	log.Err(fmt.Errorf(`Get "https://cdn.example/x.jpg?oh=tok": dial failed`)).Bytes("body", []byte("raw response")).Msg("Download failed")
	log.Error().Str("xs_cookie", "abc").Str("pin", "123456").Msg("PIN 123456 rejected")
	text := out.String()
	for _, f := range []string{"salut", "Alice", "secret body", "coucou", "oh=tok", "raw response", `"abc"`, `"123456"`} {
		if strings.Contains(text, f) {
			t.Errorf("log output leaks %q: %s", f, text)
		}
	}
	if !strings.Contains(text, "t-42") || !strings.Contains(text, "Got message") {
		t.Errorf("technical fields must be kept: %s", text)
	}
	errs := ring.snapshot()
	if len(errs) != 3 {
		t.Fatalf("warn and error lines must be recorded, got %v", errs)
	}
	if !strings.Contains(errs[1], "meta: Download failed: Get \"https://cdn.example/x.jpg?[redacted]\": dial failed") || strings.Contains(errs[1], "oh=tok") {
		t.Fatalf("recorded error must be redacted and keep context: %q", errs[1])
	}
	for i := 0; i < 30; i++ {
		log.Error().Msg(fmt.Sprintf("failure %02d", i))
	}
	errs = ring.snapshot()
	if len(errs) != maxRecentErrors || !strings.HasSuffix(errs[0], "failure 10") || !strings.HasSuffix(errs[maxRecentErrors-1], "failure 29") {
		t.Fatalf("ring must keep the last %d errors oldest first: %v", maxRecentErrors, errs)
	}
	var plain bytes.Buffer
	w := newSanitizingWriter(&plain, nil)
	if _, err := io.WriteString(w, "plain text from https://x.example/a?token=zzz\n"); err != nil || strings.Contains(plain.String(), "zzz") {
		t.Fatalf("non-JSON lines must be redacted too: %q %v", plain.String(), err)
	}
}

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

func TestMigrationKeepsPlaintextWhenTheFileIsHeld(t *testing.T) {
	requireDPAPI(t)
	dir := t.TempDir()
	path := filepath.Join(dir, cookiesFile)
	plain := []byte(`{"c_user":"4242","xs":"secret-xs","datr":"d"}`)
	if err := os.WriteFile(path, plain, 0o600); err != nil {
		t.Fatal(err)
	}
	holder, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	data, err := readSecretFile(path, zerolog.Nop())
	holder.Close()
	if err != nil || !bytes.Equal(data, plain) {
		t.Fatalf("a held file must still be read: %q %v", data, err)
	}
	if onDisk, _ := os.ReadFile(path); !bytes.Equal(onDisk, plain) {
		t.Fatalf("a failed migration must leave the original untouched, got %q", onDisk)
	}
	if entries, _ := os.ReadDir(dir); len(entries) != 1 {
		t.Fatalf("a failed migration must not leave side files, got %d entries", len(entries))
	}
	if again, err := readSecretFile(path, zerolog.Nop()); err != nil || !bytes.Equal(again, plain) {
		t.Fatalf("the next start must read the session: %q %v", again, err)
	}
	if !validSecretFile(path) {
		t.Fatal("the migration must be retried once the file is free")
	}
}

func TestInterruptedMigrationIsRecovered(t *testing.T) {
	requireDPAPI(t)
	plain := []byte(`{"user":{"id":"4242"}}`)
	newer := []byte(`{"user":{"id":"4242","name":"x"}}`)
	for name, setup := range map[string]func(path string) []byte{
		"encrypted target": func(path string) []byte {
			if err := writeSecretFile(path, newer); err != nil {
				t.Fatal(err)
			}
			return newer
		},
		"missing target": func(string) []byte { return plain },
		"zeroed target": func(path string) []byte {
			if err := os.WriteFile(path, make([]byte, len(plain)), 0o600); err != nil {
				t.Fatal(err)
			}
			return plain
		},
	} {
		dir := t.TempDir()
		path := filepath.Join(dir, sessionFile)
		if err := os.WriteFile(migrationSidePath(path), plain, 0o600); err != nil {
			t.Fatal(err)
		}
		want := setup(path)
		got, err := readSecretFile(path, zerolog.Nop())
		if err != nil || !bytes.Equal(got, want) {
			t.Fatalf("%s: got %q %v, want %q", name, got, err, want)
		}
		if fileExists(migrationSidePath(path)) || !validSecretFile(path) {
			t.Fatalf("%s: the side file must be resolved and the target encrypted", name)
		}
	}
}

func TestErrorsNeverCarrySignedURLs(t *testing.T) {
	signed := "https://scontent.xx.fbcdn.net/v/t1/a.jpg?oh=SECRET&oe=65&_nc_sid=abc"
	ue := &url.Error{Op: "Get", URL: signed, Err: context.DeadlineExceeded}
	err := withoutURL(fmt.Errorf("download: %w", ue))
	if strings.Contains(err.Error(), "SECRET") || strings.Contains(err.Error(), "/v/t1") || !strings.Contains(err.Error(), "scontent.xx.fbcdn.net") || !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("wrapped URL errors must keep the host and the cause only: %q", err)
	}
	for _, e := range []error{newError(CodeInternal, "failed to download media: %v", ue), fmt.Errorf("raw %v", ue)} {
		if msg := toErrorBody(e).Message; strings.Contains(msg, "SECRET") || strings.Contains(msg, "_nc_sid=abc") {
			t.Fatalf("error bodies must be redacted: %q", msg)
		}
	}
	if withoutURL(nil) != nil {
		t.Fatal("nil must stay nil")
	}
}

func TestFailedCacheWriteIsRetried(t *testing.T) {
	s := NewStore()
	s.PutMessage(Message{ID: "m1", ThreadID: "t1", Text: "x", TimestampMS: 1})
	if err := s.Save(filepath.Join(t.TempDir(), "missing", cacheFile)); err == nil {
		t.Fatal("writing into a missing directory must fail")
	}
	path := filepath.Join(t.TempDir(), cacheFile)
	if err := s.Save(path); err != nil || !fileExists(path) {
		t.Fatalf("a failed cache write must be retried on the next save: %v", err)
	}
}
