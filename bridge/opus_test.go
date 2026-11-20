package main

import (
	"bytes"
	"context"
	"encoding/binary"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tphakala/go-opus/oggopus"
)

func oggOpusSine(t *testing.T, ms int) []byte {
	t.Helper()
	var out bytes.Buffer
	enc, err := oggopus.NewEncoder(&out, oggopus.Config{SampleRate: 48000, Channels: 1})
	if err != nil {
		t.Fatal(err)
	}
	frames := 48 * ms
	pcm := make([]byte, frames*2)
	for i := range frames {
		v := int16(8000 * math.Sin(2*math.Pi*440*float64(i)/48000))
		binary.LittleEndian.PutUint16(pcm[i*2:], uint16(v))
	}
	if _, err := enc.Write(pcm); err != nil {
		t.Fatal(err)
	}
	if err := enc.Close(); err != nil {
		t.Fatal(err)
	}
	return out.Bytes()
}

func TestOggOpusDecodesToWAV(t *testing.T) {
	path := filepath.Join(t.TempDir(), "voice.bin")
	if err := os.WriteFile(path, oggOpusSine(t, 1000), 0o600); err != nil {
		t.Fatal(err)
	}
	if !isOggOpus(path) {
		t.Fatal("an Ogg Opus stream must be recognised")
	}
	wav, mimeType, err := transcodeOggOpus(t.TempDir(), path)
	if err != nil || mimeType != wavMime || !strings.HasSuffix(wav, ".wav") {
		t.Fatalf("transcode failed: %q %q %v", wav, mimeType, err)
	}
	f, err := os.Open(wav)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	audio, err := decodeWAV(f)
	if err != nil {
		t.Fatalf("the transcoded file must be a valid WAV: %v", err)
	}
	if audio.SampleRate != 48000 || audio.DurationMS() < 980 || audio.DurationMS() > 1020 {
		t.Fatalf("unexpected decoded audio: %d Hz, %d ms", audio.SampleRate, audio.DurationMS())
	}
	var energy float64
	for _, s := range audio.Samples {
		energy += float64(s) * float64(s)
	}
	if rms := math.Sqrt(energy / float64(len(audio.Samples))); rms < 3000 {
		t.Fatalf("decoded audio is too quiet to be the encoded tone: rms %.0f", rms)
	}
}

func TestIsOggOpusRejectsOtherFiles(t *testing.T) {
	dir := t.TempDir()
	for name, data := range map[string][]byte{
		"vorbis": append([]byte("OggS"), append(make([]byte, 24), []byte("\x01vorbis\x00\x00")...)...),
		"short":  []byte("OggS"),
		"mp4":    []byte("\x00\x00\x00\x20ftypM4A \x00\x00\x00\x00M4A mp42isom\x00\x00\x00\x00"),
	} {
		path := filepath.Join(dir, name)
		os.WriteFile(path, data, 0o600)
		if isOggOpus(path) {
			t.Fatalf("%s must not be taken for Ogg Opus", name)
		}
	}
	if isOggOpus(filepath.Join(dir, "missing")) {
		t.Fatal("a missing file is not Ogg Opus")
	}
}

func TestOpusVoiceServedAsWAV(t *testing.T) {
	b, _, dir := loggedInBackend(t)
	ctx := context.Background()
	put := func(id string, data []byte) string {
		path := filepath.Join(dir, id+".bin")
		os.WriteFile(path, data, 0o600)
		b.store.PutMessage(Message{ID: id, ThreadID: "5", TimestampMS: 1, Attachments: []Attachment{{ID: "a", Kind: "audio"}}})
		b.store.PutMedia(id, "a", mediaRec{Kind: "audio", WAType: "WhatsApp Audio Keys", WATransport: []byte{1}})
		b.store.SetMediaPath(id, "a", path, "application/ogg")
		return path
	}
	put("voice", oggOpusSine(t, 500))
	res, err := b.Media(ctx, "5", "voice", "a", VariantFull)
	if err != nil || res.MimeType != wavMime || !strings.HasSuffix(res.Path, ".wav") {
		t.Fatalf("an Opus voice message must be served as WAV: %+v %v", res, err)
	}
	if rec, _ := b.store.Media("voice", "a"); rec.Path != res.Path || rec.MimeType != wavMime {
		t.Fatalf("the WAV must replace the Opus file in the store: %+v", rec)
	}
	again, err := b.Media(ctx, "5", "voice", "a", VariantFull)
	if err != nil || again.Path != res.Path {
		t.Fatalf("a second request must reuse the WAV: %+v %v", again, err)
	}

	broken := oggOpusSine(t, 500)[:120]
	original := put("broken", broken)
	res, err = b.Media(ctx, "5", "broken", "a", VariantFull)
	if err != nil || res.Path != original {
		t.Fatalf("an undecodable Opus file must still be served as is: %+v %v", res, err)
	}
}

func TestLargeDownloadsAllowedForVideosAndFiles(t *testing.T) {
	if maxDownloadBytes("video") <= maxMediaBytes || maxDownloadBytes("file") <= maxMediaBytes {
		t.Fatal("videos and files must accept more than the default media limit")
	}
	if maxDownloadBytes("audio") != maxMediaBytes || maxDownloadBytes("image") != maxMediaBytes {
		t.Fatal("other media keep the default limit")
	}
	if maxDownloadBytes("link") != maxPreviewBytes || maxDownloadBytes("other") != maxPreviewBytes {
		t.Fatal("previews keep the preview limit")
	}
}
