package main

import (
	"fmt"
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSendMediaCapsAndExtensions(t *testing.T) {
	dir := t.TempDir()
	var many []string
	for i := 0; i < maxSendFiles+1; i++ {
		p := filepath.Join(dir, fmt.Sprintf("f%d.txt", i))
		os.WriteFile(p, []byte("x"), 0o600)
		many = append(many, p)
	}
	if _, err := loadSendFiles(many); toErrorBody(err).Code != CodeBadRequest {
		t.Fatalf("more than %d files must be refused, got %v", maxSendFiles, err)
	}
	var big []string
	for i := 0; i < 3; i++ {
		p := filepath.Join(dir, fmt.Sprintf("big%d.bin", i))
		f, _ := os.Create(p)
		f.Truncate(80 << 20)
		f.Close()
		big = append(big, p)
	}
	if _, err := loadSendFiles(big); toErrorBody(err).Code != CodeBadRequest {
		t.Fatalf("more than 200 MB in total must be refused, got %v", err)
	}
	for _, c := range [][3]string{
		{"application/octet-stream", "evil.exe", ".bin"},
		{"", "photo.JPG", ".jpg"},
		{"image/png", "page.html", ".png"},
		{"text/html", "x.lnk", ".bin"},
		{"application/pdf", "doc", ".pdf"},
	} {
		if got := extensionFor(c[0], c[1]); got != c[2] {
			t.Errorf("extensionFor(%q,%q) = %q, want %q", c[0], c[1], got, c[2])
		}
	}
}

func TestLoadSendFiles(t *testing.T) {
	dir := t.TempDir()
	pngPath := filepath.Join(dir, "pic.png")
	img := image.NewNRGBA(image.Rect(0, 0, 30, 20))
	img.Set(1, 1, color.NRGBA{R: 255, A: 255})
	f, _ := os.Create(pngPath)
	png.Encode(f, img)
	f.Write([]byte("trailing-garbage"))
	f.Close()
	mp4Path := filepath.Join(dir, "clip.mp4")
	os.WriteFile(mp4Path, []byte("\x00\x00\x00\x18ftypmp42\x00\x00\x00\x00mp42isom"), 0o600)
	txtPath := filepath.Join(dir, "notes.txt")
	os.WriteFile(txtPath, []byte("hello"), 0o600)
	files, err := loadSendFiles([]string{pngPath, mp4Path, txtPath})
	if err != nil {
		t.Fatal(err)
	}
	pngData, _ := files[0].read()
	if files[0].Kind != "image" || files[0].Mime != "image/png" || files[0].Width != 30 || files[0].Height != 20 || strings.HasSuffix(string(pngData), "trailing-garbage") {
		t.Fatalf("bad png %+v", files[0].Mime)
	}
	if files[1].Kind != "video" || files[1].Mime != "video/mp4" {
		t.Fatalf("bad mp4 %s %s", files[1].Kind, files[1].Mime)
	}
	if files[2].Kind != "file" {
		t.Fatalf("bad text %s", files[2].Kind)
	}
	big := filepath.Join(dir, "big.bin")
	bf, _ := os.Create(big)
	bf.Truncate(maxSendBytes + 1)
	bf.Close()
	for _, bad := range [][]string{{"relative.png"}, {filepath.Join(dir, "missing.png")}, {big}, {dir}} {
		if _, err := loadSendFiles(bad); toErrorBody(err).Code != CodeBadRequest {
			t.Fatalf("%v: expected bad_request, got %v", bad, err)
		}
	}
}
