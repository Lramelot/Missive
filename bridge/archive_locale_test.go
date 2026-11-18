package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func localeSampleDoc(locale string) exportDoc {
	lat, lon := 48.85, 2.35
	base := time.Date(2024, 3, 4, 9, 5, 0, 0, time.Local).UnixMilli()
	day := int64(24 * time.Hour / time.Millisecond)
	return exportDoc{
		Title:      "Conv",
		ExportedAt: time.Date(2024, 8, 15, 14, 7, 0, 0, time.Local),
		Locale:     locale,
		Files:      map[string]string{mediaKey("4", "x1"): "media/a.jpg", mediaKey("4", "x5"): "media/doc.pdf"},
		Messages: []Message{
			{ID: "1", SenderID: "a", SenderName: "Alice", Text: "Hello", TimestampMS: base, Reactions: []Reaction{{Emoji: "👍", Count: 2}}},
			{ID: "2", SenderID: "me", IsOutgoing: true, Text: "Hi", TimestampMS: base + 60000, Forwarded: true, Edited: true},
			{ID: "3", SenderID: "b", TimestampMS: base + day, Deleted: true},
			{ID: "4", SenderID: "b", TimestampMS: base + 2*day, ReplyTo: &ReplyPreview{Kind: "image"}, Attachments: []Attachment{
				{ID: "x1", Kind: "image"}, {ID: "x2", Kind: "video"}, {ID: "x3", Kind: "audio", Voice: true},
				{ID: "x5", Kind: "file", FileName: "doc.pdf"}, {ID: "x6", Kind: "sticker"},
				{ID: "x8", Kind: "location", Latitude: &lat, Longitude: &lon}, {ID: "x11", Kind: "weird"},
			}},
			{ID: "5", SenderID: "c", SenderName: "Chloé", TimestampMS: base + 2*day + 1000, ReplyTo: &ReplyPreview{Text: "q"}, Poll: &Poll{Question: "Q?", Options: []PollOption{{Text: "A"}, {Text: "B", VoteCount: 1}, {Text: "C", VoteCount: 1234}}}},
		},
	}
}

var frenchExportWords = []string{"exporté", "Vous", "Inconnu", "Transféré", "transféré", "modifié", "retiré", "Vidéo", "Message vocal", "Fichier", "Autocollant", "Position", "Pièce jointe", "Sondage", "Réactions", "En réponse", "lundi", "mars", "août", `lang="fr"`, "Export Missive"}

func TestExportMarkdownInEnglish(t *testing.T) {
	var md strings.Builder
	if err := renderExportMarkdown(&md, localeSampleDoc("en")); err != nil {
		t.Fatal(err)
	}
	out := md.String()
	for _, want := range []string{
		"_5 messages · exported on August 15, 2024 at 14:07 from Missive_\n",
		"\n## Monday, March 4, 2024\n",
		"\n## Wednesday, March 6, 2024\n",
		"\nReactions: 👍 2\n",
		"\n**You** · 09:06 · forwarded · edited\n",
		"\n**Unknown** · 09:05\n_Message removed_\n",
		"> In reply to a message: Photo\n",
		"![Photo](media/a.jpg)\n_[Video]_\n_[Voice message]_\n[File · doc.pdf](media/doc.pdf)\n_[Sticker]_\nLocation\n_[Attachment]_\n",
		"\nPoll: Q?\n- A — 0 votes\n- B — 1 vote\n- C — 1,234 votes\n",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("English markdown misses %q:\n%s", want, out)
		}
	}
	for _, banned := range frenchExportWords {
		if strings.Contains(out, banned) {
			t.Fatalf("English markdown contains French %q:\n%s", banned, out)
		}
	}
}

func TestExportHTMLInEnglish(t *testing.T) {
	var page strings.Builder
	if err := renderExportHTML(&page, localeSampleDoc("en")); err != nil {
		t.Fatal(err)
	}
	out := page.String()
	for _, want := range []string{
		`<html lang="en">`,
		`<p class="meta">5 messages · exported on August 15, 2024 at 14:07 from Missive</p>`,
		`<h2 class="day">Monday, March 4, 2024</h2>`,
		`<div class="who">You · `,
		`title="2024-03-04 09:06"`,
		`<div class="tag">Forwarded</div>`,
		`<span class="edited">edited</span>`,
		`<p class="text deleted">Message removed</p>`,
		`<span class="rs">Message</span>Photo</blockquote>`,
		`<span class="att">Voice message</span>`,
		`<span class="att">Attachment</span>`,
		`<p class="text">Poll: Q?</p>`,
		`<li>C — 1,234 votes</li>`,
		`<footer>Missive export · August 15, 2024</footer>`,
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("English page misses %q:\n%s", want, out)
		}
	}
	for _, banned := range frenchExportWords {
		if strings.Contains(out, banned) {
			t.Fatalf("English page contains French %q", banned)
		}
	}
}

func TestExportLocaleFallsBackToFrench(t *testing.T) {
	for locale, want := range map[string]string{
		"en": LocaleEnglish, "EN": LocaleEnglish, " en-US ": LocaleEnglish, "en_GB": LocaleEnglish,
		"": LocaleFrench, "fr": LocaleFrench, "fr-BE": LocaleFrench, "de": LocaleFrench, "eng": LocaleFrench,
	} {
		if got := exportTextFor(locale).lang; got != want {
			t.Errorf("exportTextFor(%q) = %q, want %q", locale, got, want)
		}
	}
	var page strings.Builder
	if err := renderExportHTML(&page, localeSampleDoc("")); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(page.String(), `<html lang="fr">`) || !strings.Contains(page.String(), "5 messages · exporté le 15 août 2024 à 14:07 depuis Missive") {
		t.Fatal("an export without locale must stay in French")
	}
}

func TestArchiveParamsCarryLocale(t *testing.T) {
	var export ExportThreadParams
	var save SaveMediaParams
	if err := json.Unmarshal([]byte(`{"thread_id":"1","dir":"d","format":"html","include_media":false,"locale":"en"}`), &export); err != nil || export.Locale != "en" {
		t.Fatalf("export_thread must read locale: %+v %v", export, err)
	}
	if err := json.Unmarshal([]byte(`{"thread_id":"1","dir":"d","locale":"en"}`), &save); err != nil || save.Locale != "en" {
		t.Fatalf("save_media must read locale: %+v %v", save, err)
	}
}

func TestDemoArchiveInEnglish(t *testing.T) {
	d, _ := demoArchive(t)
	ctx := context.Background()
	thread := d.mediaThreadID()
	dir := t.TempDir()
	res, err := d.ExportThread(ctx, ExportThreadParams{ThreadID: thread, Dir: dir, Format: ExportMarkdown, Locale: "en"})
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(res.Path, "conversation.md"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), " · exported on ") || strings.Contains(string(data), "exporté le") {
		t.Fatalf("export_thread must follow the requested locale:\n%s", data)
	}
	saved, err := d.SaveMedia(ctx, SaveMediaParams{ThreadID: thread, Dir: dir, Kinds: []string{"image"}, Locale: "en"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(saved.Dir, " - media") {
		t.Fatalf("save_media folder must be named in English, got %q", saved.Dir)
	}
}
