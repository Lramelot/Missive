package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/rs/zerolog"
)

func TestSafeFileName(t *testing.T) {
	cases := map[string]string{
		"Camille Martin":                "Camille Martin",
		`a<b>:c"d/e\f|g?h*i`:            "a_b__c_d_e_f_g_h_i",
		"CON":                           "_CON",
		"CON​":                          "_CON",
		"Clock$":                        "_Clock$",
		"x‮gpj.exe":                     "xgpj.exe",
		"..":                            "Conversation",
		"aux.txt":                       "_aux.txt",
		"lpt9 ":                         "_lpt9",
		"Fin...  ":                      "Fin",
		"  \n\t ":                       "Conversation",
		"???":                           "Conversation",
		"Ligne 1\nLigne 2\tfin":         "Ligne 1 Ligne 2 fin",
		"Équipe \u0007 été 🌞":           "Équipe _ été 🌞",
		strings.Repeat("é", 90):         strings.Repeat("é", maxSafeNameRunes),
		strings.Repeat("a", 59) + ". b": strings.Repeat("a", 59),
	}
	for in, want := range cases {
		if got := safeFileName(in, defaultExportBaseName); got != want {
			t.Errorf("safeFileName(%q) = %q, want %q", in, got, want)
		}
	}
}

func demoArchive(t *testing.T) (*DemoBackend, *syncBuffer) {
	t.Helper()
	buf := &syncBuffer{}
	d := NewDemoBackend(t.TempDir(), "", NewOutput(buf, zerolog.Nop()), zerolog.Nop())
	t.Cleanup(d.Close)
	return d, buf
}

func TestDemoExportHTMLWholeHistoryWithMedia(t *testing.T) {
	d, buf := demoArchive(t)
	ctx := context.Background()
	thread := d.mediaThreadID()
	initial := d.store.MessageCount(thread)
	dir := t.TempDir()
	res, err := d.ExportThread(ctx, ExportThreadParams{ThreadID: thread, Dir: dir, Format: ExportHTML, IncludeMedia: true})
	if err != nil {
		t.Fatal(err)
	}
	if res.Messages <= initial || res.Messages != d.store.MessageCount(thread) {
		t.Fatalf("the whole history must be loaded before exporting: %d (initial %d)", res.Messages, initial)
	}
	if filepath.Dir(res.Path) != dir || !strings.HasSuffix(filepath.Base(res.Path), time.Now().Format(exportDateLayout)) {
		t.Fatalf("unexpected export folder %q", res.Path)
	}
	data, err := os.ReadFile(filepath.Join(res.Path, "conversation.html"))
	if err != nil {
		t.Fatal(err)
	}
	page := string(data)
	for _, want := range []string{"<!doctype html>", "prefers-color-scheme:dark", "@media print", `class="day"`, `src="media/`} {
		if !strings.Contains(page, want) {
			t.Fatalf("export page misses %q", want)
		}
	}
	for _, banned := range []string{`src="http`, `href="http://`, "<script", "<link"} {
		if strings.Contains(page, banned) {
			t.Fatalf("export page must be self-contained, found %q", banned)
		}
	}
	entries, _ := os.ReadDir(filepath.Join(res.Path, exportMediaDirName))
	if res.Media == 0 || len(entries) != res.Media {
		t.Fatalf("media count mismatch: result %d, files %d", res.Media, len(entries))
	}
	if len(eventsOf(t, buf, EventExportProgress)) == 0 {
		t.Fatal("export_progress events are expected")
	}
	again, err := d.ExportThread(ctx, ExportThreadParams{ThreadID: thread, Dir: dir, Format: ExportMarkdown})
	if err != nil {
		t.Fatal(err)
	}
	if again.Path != res.Path+" (2)" || !fileExists(filepath.Join(again.Path, "conversation.md")) || !fileExists(filepath.Join(res.Path, "conversation.html")) {
		t.Fatalf("a second export must never overwrite the first: %q", again.Path)
	}
}

func TestExportEscapesEverything(t *testing.T) {
	b, _, _ := loggedInBackend(t)
	ctx := context.Background()
	evil := `<img src=x onerror=alert(1)>`
	b.store.UpdateThread("42", func(r *threadRec) {
		r.Name = `<script>alert("t")</script> CON`
		r.IsGroup = true
	})
	b.store.SetHasMoreBefore("42", false)
	b.store.PutMessage(Message{ID: "mid.$1", ThreadID: "42", SenderID: "7", SenderName: `</p><b onmouseover=x>`, Text: evil + "\n# titre\n1. liste", TimestampMS: readBaseMS,
		Attachments: []Attachment{{ID: "l1", Kind: "link", URL: "javascript:alert(1)", Title: `"><svg onload=1>`}}})
	b.store.PutMessage(Message{ID: "mid.$2", ThreadID: "42", SenderID: testSelf, IsOutgoing: true, Text: "ok & <b>", TimestampMS: readBaseMS + 1000, ReplyToID: "mid.$1"})
	b.store.SetReaction("mid.$1", testSelf, "❤️", readBaseMS+500)
	dir := t.TempDir()
	read := func(format, name string) string {
		res, err := b.ExportThread(ctx, ExportThreadParams{ThreadID: "42", Dir: dir, Format: format})
		if err != nil {
			t.Fatal(err)
		}
		if strings.ContainsAny(filepath.Base(res.Path), `<>:"/\|?*`) {
			t.Fatalf("unsafe folder name %q", res.Path)
		}
		data, err := os.ReadFile(filepath.Join(res.Path, name))
		if err != nil {
			t.Fatal(err)
		}
		return string(data)
	}
	page := read(ExportHTML, "conversation.html")
	for _, banned := range []string{"<script>", "<img src=x", "<b onmouseover", "<svg", "javascript:", "<b>"} {
		if strings.Contains(page, banned) {
			t.Fatalf("HTML export is injectable, found %q", banned)
		}
	}
	for _, want := range []string{"&lt;img src=x onerror=alert(1)&gt;", "ok &amp; &lt;b&gt;", `class="reply"`, `<div class="reactions">❤️ 1</div>`} {
		if !strings.Contains(page, want) {
			t.Fatalf("HTML export misses %q", want)
		}
	}
	md := read(ExportMarkdown, "conversation.md")
	for _, want := range []string{`\<img src=x onerror=alert(1)\>`, `\# titre`, `1\. liste`, "> En réponse à"} {
		if !strings.Contains(md, want) {
			t.Fatalf("Markdown export misses %q:\n%s", want, md)
		}
	}
	var msgs []Message
	if err := json.Unmarshal([]byte(read(ExportJSON, "conversation.json")), &msgs); err != nil {
		t.Fatal(err)
	}
	if len(msgs) != 2 || msgs[0].Text != evil+"\n# titre\n1. liste" || msgs[1].ReplyTo == nil {
		t.Fatalf("JSON export must keep protocol messages intact: %+v", msgs)
	}
}

func TestExportRejectsMissingDir(t *testing.T) {
	d, _ := demoArchive(t)
	thread := d.mediaThreadID()
	_, err := d.ExportThread(context.Background(), ExportThreadParams{ThreadID: thread, Dir: filepath.Join(t.TempDir(), "absent"), Format: ExportJSON})
	if toErrorBody(err).Code != CodeBadRequest {
		t.Fatalf("missing dir must be bad_request, got %v", err)
	}
	if _, err := d.ExportThread(context.Background(), ExportThreadParams{ThreadID: "nope", Dir: t.TempDir(), Format: ExportJSON}); toErrorBody(err).Code != CodeNotFound {
		t.Fatalf("unknown thread must be not_found, got %v", err)
	}
}

func TestDemoSaveMediaSkipsExisting(t *testing.T) {
	d, buf := demoArchive(t)
	ctx := context.Background()
	thread := d.store.ListThreads(1)[0].ID
	dir := t.TempDir()
	first, err := d.SaveMedia(ctx, SaveMediaParams{ThreadID: thread, Dir: dir, Kinds: []string{"image", "file"}})
	if err != nil {
		t.Fatal(err)
	}
	if first.Saved == 0 || first.Failed != 0 || !strings.HasSuffix(first.Dir, " - médias") {
		t.Fatalf("unexpected first save: %+v", first)
	}
	entries, _ := os.ReadDir(first.Dir)
	if len(entries) != first.Saved {
		t.Fatalf("%d files for %d saved", len(entries), first.Saved)
	}
	for _, e := range entries {
		if !strings.HasPrefix(e.Name(), "20") || strings.Count(e.Name(), " ") < 2 {
			t.Fatalf("file name must start with the date and time: %q", e.Name())
		}
	}
	second, err := d.SaveMedia(ctx, SaveMediaParams{ThreadID: thread, Dir: dir, Kinds: []string{"image", "file"}})
	if err != nil {
		t.Fatal(err)
	}
	if second.Saved != 0 || second.Skipped != first.Saved || second.Dir != first.Dir {
		t.Fatalf("existing files must be skipped: %+v", second)
	}
	var last SaveMediaProgressEvent
	progress := eventsOf(t, buf, EventSaveMediaProgress)
	if len(progress) == 0 || json.Unmarshal(progress[len(progress)-1], &last) != nil || last.Total == nil || *last.Total != first.Saved {
		t.Fatalf("save_media_progress must report the total: %d events", len(progress))
	}
}

func TestSearchFilters(t *testing.T) {
	s := NewStore()
	s.SetSelfID(testSelf)
	s.PutMessage(Message{ID: "a", ThreadID: "1", SenderID: "7", Text: "Photo de l'été", TimestampMS: 1000, Attachments: []Attachment{{ID: "i", Kind: "image"}}})
	s.PutMessage(Message{ID: "b", ThreadID: "1", SenderID: testSelf, IsOutgoing: true, Text: "voir HTTPS://Example.com", TimestampMS: 2000})
	s.PutMessage(Message{ID: "c", ThreadID: "2", SenderID: "8", Text: "Ete", TimestampMS: 3000, Attachments: []Attachment{{ID: "v", Kind: "audio", Voice: true}}})
	s.PutMessage(Message{ID: "d", ThreadID: "2", SenderID: "8", Text: "x a renommé le groupe", TimestampMS: 4000, System: true})
	ids := func(f SearchFilter) string {
		var out []string
		for _, h := range s.SearchFiltered(f, 0) {
			out = append(out, h.Message.ID)
		}
		return strings.Join(out, ",")
	}
	cases := []struct {
		f    SearchFilter
		want string
	}{
		{SearchFilter{Query: "ete"}, "c,a"},
		{SearchFilter{Query: "ete", ThreadID: "1"}, "a"},
		{SearchFilter{SenderID: testSelf}, "b"},
		{SearchFilter{Kinds: []string{"image"}}, "a"},
		{SearchFilter{Kinds: []string{"link"}}, "b"},
		{SearchFilter{Kinds: []string{"voice"}}, "c"},
		{SearchFilter{Kinds: []string{"audio", "image"}}, "c,a"},
		{SearchFilter{AfterMS: 2000}, "c,b"},
		{SearchFilter{BeforeMS: 2000}, "a"},
		{SearchFilter{AfterMS: 1000, BeforeMS: 3000, Query: "voir"}, "b"},
		{SearchFilter{ThreadID: "2"}, "c"},
		{SearchFilter{Query: "renommé"}, "d"},
	}
	for _, c := range cases {
		if got := ids(c.f); got != c.want {
			t.Errorf("SearchFiltered(%+v) = %q, want %q", c.f, got, c.want)
		}
	}
}

func TestSearchCommandValidation(t *testing.T) {
	h := newDemoHarness(t)
	h.send(
		`{"id":1,"cmd":"search","params":{"query":"  "}}`,
		`{"id":2,"cmd":"search","params":{"sender_id":"100004781234500"}}`,
		`{"id":3,"cmd":"search","params":{"kinds":["photo"]}}`,
		`{"id":4,"cmd":"search","params":{"kinds":["image"],"before_ms":0}}`,
		`{"id":5,"cmd":"export_thread","params":{"thread_id":"1","dir":"x","format":"pdf"}}`,
		`{"id":6,"cmd":"save_media","params":{"thread_id":"1","dir":"x","kinds":["link"]}}`,
	)
	if code := h.errCode(1); code != CodeBadRequest {
		t.Fatalf("empty query without filter: %s", code)
	}
	var hits []SearchHit
	h.okResult(2, &hits)
	if len(hits) == 0 {
		t.Fatal("a sender filter alone must return the user's messages")
	}
	for _, hit := range hits {
		if !hit.Message.IsOutgoing {
			t.Fatalf("sender filter leaked %+v", hit.Message.ID)
		}
	}
	for _, id := range []int64{3, 4, 5, 6} {
		if code := h.errCode(id); code != CodeBadRequest {
			t.Fatalf("request %d: %s", id, code)
		}
	}
}

func TestDemoExportThroughDispatcher(t *testing.T) {
	h := newDemoHarness(t)
	thread := h.demo.store.ListThreads(1)[0].ID
	dir := t.TempDir()
	params, _ := json.Marshal(ExportThreadParams{ThreadID: thread, Dir: dir, Format: ExportJSON})
	media, _ := json.Marshal(SaveMediaParams{ThreadID: thread, Dir: dir, Kinds: []string{"image", "file"}})
	h.send(`{"id":1,"cmd":"export_thread","params":` + string(params) + `}`)
	h.send(`{"id":2,"cmd":"save_media","params":` + string(media) + `}`)
	h.send(`{"id":3,"cmd":"cancel_archive","params":{}}`)
	var cancelled CancelArchiveResult
	h.okResult(3, &cancelled)
	if cancelled.Cancelled {
		t.Fatal("cancel_archive with no running job reports cancelled: false")
	}
	var res ExportThreadResult
	h.okResult(1, &res)
	data, err := os.ReadFile(filepath.Join(res.Path, "conversation.json"))
	if err != nil {
		t.Fatal(err)
	}
	var msgs []Message
	if err := json.Unmarshal(data, &msgs); err != nil || len(msgs) != res.Messages || res.Messages == 0 {
		t.Fatalf("export_thread must write the whole conversation: %d/%d %v", len(msgs), res.Messages, err)
	}
	var saved SaveMediaResult
	h.okResult(2, &saved)
	if saved.Dir == "" || saved.Failed != 0 {
		t.Fatalf("unexpected save_media result %+v", saved)
	}
	if len(h.events(EventExportProgress)) == 0 || len(h.events(EventSaveMediaProgress)) == 0 {
		t.Fatal("progress events are expected in demo mode")
	}
}

func TestExportRenderHardening(t *testing.T) {
	if webURL("https://user:pass@evil.example/") != "" || webURL("javascript:alert(1)") != "" || webURL("HTTPS://ok.example/a") == "" {
		t.Fatal("only plain http(s) links without credentials are kept")
	}
	var md strings.Builder
	doc := exportDoc{Title: "T\n[x]: javascript:alert(1)", ExportedAt: time.Now(), Messages: []Message{{ID: "1", SenderName: "A\n# B", Text: "ok", TimestampMS: readBaseMS}}}
	if err := renderExportMarkdown(&md, doc); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(md.String(), `# T \[x\]: javascript:alert(1)`+"\n") || strings.Contains(md.String(), "\n# B") {
		t.Fatalf("single-line fields must stay on one line:\n%s", md.String())
	}
}
