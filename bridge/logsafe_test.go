package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/url"
	"path/filepath"
	"strings"
	"testing"

	"github.com/rs/zerolog"
)

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
