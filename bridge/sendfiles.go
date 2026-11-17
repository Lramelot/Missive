package main

import (
	"bufio"
	"bytes"
	"encoding/binary"
	"image"
	_ "image/gif"
	_ "image/jpeg"
	_ "image/png"
	"io"
	"mime"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"unicode"

	"golang.org/x/text/runes"
	"golang.org/x/text/transform"
	"golang.org/x/text/unicode/norm"
)

const (
	maxSendBytes      = 100 << 20
	maxSendTotalBytes = 200 << 20
	maxSendFiles      = 10
)

type sendFile struct {
	Path   string
	Name   string
	Size   int64
	Mime   string
	Kind   string
	Width  int
	Height int

	Data       []byte
	Voice      bool
	Waveform   []float64
	DurationMS int64
}

func (f sendFile) read() ([]byte, error) {
	if f.Data != nil {
		return f.Data, nil
	}
	data, err := os.ReadFile(f.Path)
	if err != nil {
		return nil, newError(CodeBadRequest, "file is unreadable: %s", f.Name)
	}
	if int64(len(data)) > maxSendBytes {
		return nil, newError(CodeBadRequest, "file is larger than 100 MB: %s", f.Name)
	}
	if f.Mime == "image/png" {
		data = trimPNGTrailer(data)
	}
	return data, nil
}

var extraMimeTypes = map[string]string{
	".mp4":  "video/mp4",
	".m4v":  "video/mp4",
	".mov":  "video/quicktime",
	".webm": "video/webm",
	".mkv":  "video/x-matroska",
	".heic": "image/heic",
	".webp": "image/webp",
	".mp3":  "audio/mpeg",
	".m4a":  "audio/mp4",
	".ogg":  "audio/ogg",
	".pdf":  "application/pdf",
}

func detectMime(name string, data []byte) string {
	detected, _, _ := mime.ParseMediaType(http.DetectContentType(data))
	if detected != "" && detected != "application/octet-stream" && detected != "text/plain" {
		return detected
	}
	ext := strings.ToLower(filepath.Ext(name))
	if known, ok := extraMimeTypes[ext]; ok {
		return known
	}
	if byExt := mime.TypeByExtension(ext); byExt != "" {
		if base, _, err := mime.ParseMediaType(byExt); err == nil {
			return base
		}
	}
	if detected != "" {
		return detected
	}
	return "application/octet-stream"
}

func sendKind(mimeType string) string {
	switch {
	case mimeType == "image/svg+xml":
		return "file"
	case strings.HasPrefix(mimeType, "image/"):
		return "image"
	case strings.HasPrefix(mimeType, "video/"):
		return "video"
	default:
		return "file"
	}
}

func inspectSendFile(path string) (sendFile, error) {
	name := filepath.Base(path)
	if !filepath.IsAbs(path) {
		return sendFile{}, newError(CodeBadRequest, "path must be absolute: %s", name)
	}
	info, err := os.Stat(path)
	if err != nil || info.IsDir() {
		return sendFile{}, newError(CodeBadRequest, "file is missing or unreadable: %s", name)
	}
	if info.Size() > maxSendBytes {
		return sendFile{}, newError(CodeBadRequest, "file is larger than 100 MB: %s", name)
	}
	f, err := os.Open(path)
	if err != nil {
		return sendFile{}, newError(CodeBadRequest, "file is unreadable: %s", name)
	}
	defer f.Close()
	header := make([]byte, 512)
	n, _ := io.ReadFull(f, header)
	file := sendFile{Path: path, Name: name, Size: info.Size()}
	file.Mime = detectMime(name, header[:n])
	file.Kind = sendKind(file.Mime)
	if file.Kind == "image" {
		if _, err := f.Seek(0, io.SeekStart); err == nil {
			if cfg, _, err := image.DecodeConfig(bufio.NewReader(f)); err == nil {
				file.Width, file.Height = cfg.Width, cfg.Height
			}
		}
	}
	return file, nil
}

func loadSendFiles(paths []string) ([]sendFile, error) {
	if len(paths) > maxSendFiles {
		return nil, newError(CodeBadRequest, "at most %d files per send_media", maxSendFiles)
	}
	files := make([]sendFile, 0, len(paths))
	var total int64
	for _, path := range paths {
		file, err := inspectSendFile(path)
		if err != nil {
			return nil, err
		}
		total += file.Size
		if total > maxSendTotalBytes {
			return nil, newError(CodeBadRequest, "send_media is limited to 200 MB in total")
		}
		files = append(files, file)
	}
	return files, nil
}

const pngHeader = "\x89PNG\r\n\x1a\n"

func trimPNGTrailer(data []byte) []byte {
	if !bytes.HasPrefix(data, []byte(pngHeader)) {
		return data
	}
	for pos := 8; pos+12 <= len(data); {
		length := int(binary.BigEndian.Uint32(data[pos : pos+4]))
		end := pos + 12 + length
		if end > len(data) || end <= pos || length < 0 {
			break
		}
		if string(data[pos+4:pos+8]) == "IEND" {
			if length != 0 {
				break
			}
			return data[:end]
		}
		pos = end
	}
	return data
}

func (f sendFile) attachment(id string) Attachment {
	return Attachment{
		ID:         id,
		Kind:       f.Kind,
		FileName:   f.Name,
		MimeType:   f.Mime,
		Size:       f.Size,
		Width:      f.Width,
		Height:     f.Height,
		Voice:      f.Voice,
		Waveform:   f.Waveform,
		DurationMS: f.DurationMS,
	}
}

var foldTransformer = transform.Chain(norm.NFD, runes.Remove(runes.In(unicode.Mn)), norm.NFC)

func foldText(s string) string {
	folded, _, err := transform.String(foldTransformer, s)
	if err != nil {
		folded = s
	}
	return strings.ToLower(folded)
}
