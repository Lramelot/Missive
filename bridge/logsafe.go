package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/url"
	"os"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"unicode/utf8"
)

const (
	maxRecentErrors   = 20
	maxLogValueLength = 600
	redactedValue     = "[redacted]"
)

type errorRing struct {
	mu    sync.Mutex
	items []string
	next  int
}

func newErrorRing() *errorRing {
	return &errorRing{items: make([]string, 0, maxRecentErrors)}
}

func (r *errorRing) add(line string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.items) < maxRecentErrors {
		r.items = append(r.items, line)
		return
	}
	r.items[r.next] = line
	r.next = (r.next + 1) % maxRecentErrors
}

func (r *errorRing) snapshot() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]string, 0, len(r.items))
	out = append(out, r.items[r.next:]...)
	return append(out, r.items[:r.next]...)
}

var recentErrors = newErrorRing()

var (
	passthroughLogKeys = map[string]bool{"level": true, "time": true, "message": true, "component": true, "error": true, "caller": true}
	sensitiveLogKeys   = map[string]bool{
		"pin": true, "text": true, "name": true, "names": true, "url": true, "uri": true, "redirect": true, "account": true,
		"contact_point": true, "val": true, "item": true, "frame": true, "step_data": true, "block_data": true,
		"input_data": true, "blob_base64": true, "query": true, "caption": true, "title": true, "snippet": true,
		"headers": true, "dialog_action": true, "mfatype": true, "mfa_method": true,
		"username": true, "first_name": true, "full_name": true, "display_name": true, "push_name": true,
		"phone": true, "msisdn": true, "jid_alt": true, "content": true,
	}
	sensitiveLogKeyParts = []string{"cookie", "token", "password", "secret", "email", "body", "payload", "phone", "signature", "nonce", "fb_dtsg"}

	urlPattern        = regexp.MustCompile(`(?i)\b(?:https?|wss?)://[^\s"'<>\\]+`)
	secretPairPattern = regexp.MustCompile(`(?i)\b(xs|c_user|datr|fr|sb|wd|presence|fb_dtsg|lsd|jazoest|access_token|token|oh|oe|_nc_sid|_nc_ohc|pin|password|passwd|sessionid|csrftoken|ig_did|mid)=([^\s&;,"']+)`)
	secretJSONPattern = regexp.MustCompile(`(?i)"(xs|c_user|datr|fr|sb|fb_dtsg|lsd|access_token|token|pin|password|text|name)"\s*:\s*"(?:[^"\\]|\\.)*"`)
	bearerPattern     = regexp.MustCompile(`(?i)\b(bearer|oauth)\s+[A-Za-z0-9._~+/=-]{8,}`)
	waPhonePattern    = regexp.MustCompile(`\b\d{6,}(@s\.whatsapp\.net)`)
	emailPattern      = regexp.MustCompile(`[A-Za-z0-9._%+-]+@[A-Za-z0-9.-]+\.[A-Za-z]{2,}`)
	phonePattern      = regexp.MustCompile(`\+\d[\d\s().-]{6,}\d`)
)

var homeDir = func() string {
	home, err := os.UserHomeDir()
	if err != nil || len(home) < 3 {
		return ""
	}
	return home
}()

func redactURL(raw string) string {
	cut := len(raw)
	if i := strings.IndexAny(raw, "?#"); i >= 0 {
		cut = i
	}
	if cut < len(raw) {
		return raw[:cut] + "?" + redactedValue
	}
	return raw
}

func redactText(s string) string {
	if s == "" {
		return s
	}
	s = urlPattern.ReplaceAllStringFunc(s, redactURL)
	s = secretPairPattern.ReplaceAllString(s, "${1}="+redactedValue)
	s = secretJSONPattern.ReplaceAllString(s, `"${1}":"`+redactedValue+`"`)
	s = bearerPattern.ReplaceAllString(s, "${1} "+redactedValue)
	s = waPhonePattern.ReplaceAllString(s, "[phone]${1}")
	s = emailPattern.ReplaceAllString(s, "[email]")
	s = phonePattern.ReplaceAllString(s, "[phone]")
	if homeDir != "" {
		s = replaceFold(s, homeDir, "~")
		if alt := strings.ReplaceAll(homeDir, `\`, "/"); alt != homeDir {
			s = replaceFold(s, alt, "~")
		}
	}
	if utf8.RuneCountInString(s) > maxLogValueLength {
		runes := []rune(s)
		s = string(runes[:maxLogValueLength]) + "…"
	}
	return s
}

func replaceFold(s, old, repl string) string {
	lower, lowerOld := strings.ToLower(s), strings.ToLower(old)
	if len(lower) != len(s) || len(lowerOld) != len(old) || !strings.Contains(lower, lowerOld) {
		return s
	}
	var b strings.Builder
	for {
		i := strings.Index(lower, lowerOld)
		if i < 0 {
			b.WriteString(s)
			return b.String()
		}
		b.WriteString(s[:i])
		b.WriteString(repl)
		s, lower = s[i+len(old):], lower[i+len(old):]
	}
}

func sensitiveLogKey(key string) bool {
	k := strings.ToLower(key)
	if sensitiveLogKeys[k] {
		return true
	}
	for _, part := range sensitiveLogKeyParts {
		if strings.Contains(k, part) {
			return true
		}
	}
	return false
}

func sanitizeLogFields(fields map[string]any) {
	for key, value := range fields {
		if !passthroughLogKeys[key] && sensitiveLogKey(key) {
			fields[key] = redactedValue
			continue
		}
		switch v := value.(type) {
		case string:
			fields[key] = redactText(v)
		case map[string]any, []any:
			fields[key] = redactedValue
		}
	}
}

func errorSummary(fields map[string]any) string {
	var parts []string
	for _, key := range []string{"time", "level"} {
		if v, ok := fields[key].(string); ok && v != "" {
			parts = append(parts, v)
		}
	}
	head := strings.Join(parts, " ")
	if c, ok := fields["component"].(string); ok && c != "" {
		head += " " + c + ":"
	}
	msg, _ := fields["message"].(string)
	if e, ok := fields["error"].(string); ok && e != "" {
		if msg != "" {
			msg += ": "
		}
		msg += e
	}
	return strings.TrimSpace(head + " " + msg)
}

type urlFreeError struct {
	msg string
	err error
}

func (e *urlFreeError) Error() string { return e.msg }

func (e *urlFreeError) Unwrap() error { return e.err }

func withoutURL(err error) error {
	var ue *url.Error
	if err == nil || !errors.As(err, &ue) || ue.URL == "" {
		return err
	}
	host := "server"
	if parsed, perr := url.Parse(ue.URL); perr == nil && parsed.Host != "" {
		host = parsed.Host
	}
	msg := strings.ReplaceAll(err.Error(), strconv.Quote(ue.URL), host)
	msg = strings.ReplaceAll(msg, ue.URL, host)
	return &urlFreeError{msg: msg, err: err}
}

type sanitizingWriter struct {
	next io.Writer
	ring *errorRing
}

func newSanitizingWriter(next io.Writer, ring *errorRing) *sanitizingWriter {
	return &sanitizingWriter{next: next, ring: ring}
}

func (w *sanitizingWriter) Write(p []byte) (int, error) {
	dec := json.NewDecoder(bytes.NewReader(p))
	dec.UseNumber()
	var fields map[string]any
	if err := dec.Decode(&fields); err != nil || fields == nil {
		line := redactText(strings.TrimRight(string(p), "\r\n"))
		if _, err := io.WriteString(w.next, line+"\n"); err != nil {
			return 0, err
		}
		return len(p), nil
	}
	sanitizeLogFields(fields)
	if w.ring != nil {
		switch fields["level"] {
		case "warn", "error", "fatal", "panic":
			w.ring.add(errorSummary(fields))
		}
	}
	out, err := json.Marshal(fields)
	if err != nil {
		return 0, err
	}
	if _, err = w.next.Write(append(out, '\n')); err != nil {
		return 0, err
	}
	return len(p), nil
}
