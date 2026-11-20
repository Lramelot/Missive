package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"go.mau.fi/mautrix-meta/pkg/messagix/useragent"
)

const (
	mediaDirName        = "media"
	maxMediaBytes       = 64 << 20
	maxLargeMediaBytes  = 1 << 30
	maxPreviewBytes     = 8 << 20
	mediaHTTPTimeout    = 2 * time.Minute
	maxMediaRedirects   = 5
	mediaSniffBytes     = 512
	mediaTempPattern    = "dl-*.part"
	defaultMediaExt     = ".bin"
	mediaFilePermission = 0o600
)

var errMediaExpired = errors.New("media expired")

var errMediaHostNotAllowed = errors.New("media host not allowed")

var mimeExtensions = map[string]string{
	"image/jpeg":       ".jpg",
	"image/jpg":        ".jpg",
	"image/png":        ".png",
	"image/gif":        ".gif",
	"image/webp":       ".webp",
	"image/heic":       ".heic",
	"image/bmp":        ".bmp",
	"video/mp4":        ".mp4",
	"video/quicktime":  ".mov",
	"video/webm":       ".webm",
	"audio/mpeg":       ".mp3",
	"audio/mp4":        ".m4a",
	"audio/aac":        ".aac",
	"audio/ogg":        ".ogg",
	"application/ogg":  ".ogg",
	"audio/opus":       ".opus",
	"audio/wav":        ".wav",
	"audio/x-wav":      ".wav",
	"application/pdf":  ".pdf",
	"application/zip":  ".zip",
	"text/plain":       ".txt",
	"application/json": ".txt",
}

var allowedFileExtensions = map[string]bool{
	".jpg": true, ".jpeg": true, ".png": true, ".gif": true, ".webp": true, ".heic": true, ".bmp": true,
	".mp4": true, ".mov": true, ".webm": true, ".mkv": true, ".m4v": true,
	".mp3": true, ".m4a": true, ".aac": true, ".ogg": true, ".opus": true, ".wav": true,
	".pdf": true, ".txt": true, ".csv": true, ".rtf": true,
	".doc": true, ".docx": true, ".xls": true, ".xlsx": true, ".ppt": true, ".pptx": true,
	".odt": true, ".ods": true, ".odp": true,
	".zip": true,
}

var mediaHostSuffixes = []string{"fbcdn.net", "fbsbx.com", "cdninstagram.com", "facebook.com", "messenger.com"}

func defaultMediaURLAllowed(u *url.URL) bool {
	if u == nil || u.Scheme != "https" || u.User != nil {
		return false
	}
	host := strings.ToLower(u.Hostname())
	for _, suffix := range mediaHostSuffixes {
		if host == suffix || strings.HasSuffix(host, "."+suffix) {
			return true
		}
	}
	return false
}

var mediaURLAllowed = defaultMediaURLAllowed

func maxDownloadBytes(kind string) int64 {
	switch kind {
	case "link", "other":
		return maxPreviewBytes
	case "video", "file":
		return maxLargeMediaBytes
	}
	return maxMediaBytes
}

func extensionFor(mimeType, fileName string) string {
	if ext := strings.ToLower(filepath.Ext(fileName)); allowedFileExtensions[ext] {
		return ext
	}
	base, _, _ := mime.ParseMediaType(mimeType)
	if ext, ok := mimeExtensions[base]; ok {
		return ext
	}
	return defaultMediaExt
}

func normalizeMime(mimeType string, sniff []byte) string {
	base, _, err := mime.ParseMediaType(mimeType)
	if err == nil && base != "" && base != "application/octet-stream" {
		return base
	}
	sniffed, _, _ := mime.ParseMediaType(http.DetectContentType(sniff))
	return sniffed
}

func mediaDir(dataDir string) (string, error) {
	dir := filepath.Join(dataDir, mediaDirName)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", fmt.Errorf("failed to create media dir: %w", err)
	}
	return dir, nil
}

func newMediaTemp(dataDir string) (*os.File, error) {
	dir, err := mediaDir(dataDir)
	if err != nil {
		return nil, err
	}
	return os.CreateTemp(dir, mediaTempPattern)
}

func discardMediaTemp(f *os.File) {
	name := f.Name()
	_ = f.Close()
	_ = os.Remove(name)
}

func finalizeMediaTemp(f *os.File, mimeType, fileName string) (string, string, error) {
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		discardMediaTemp(f)
		return "", "", err
	}
	sniff := make([]byte, mediaSniffBytes)
	n, _ := io.ReadFull(f, sniff)
	sniff = sniff[:n]
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		discardMediaTemp(f)
		return "", "", err
	}
	hash := sha256.New()
	if _, err := io.Copy(hash, f); err != nil {
		discardMediaTemp(f)
		return "", "", err
	}
	tempName := f.Name()
	if err := f.Close(); err != nil {
		_ = os.Remove(tempName)
		return "", "", err
	}
	mimeType = normalizeMime(mimeType, sniff)
	path := filepath.Join(filepath.Dir(tempName), hex.EncodeToString(hash.Sum(nil))+extensionFor(mimeType, fileName))
	if fileExists(path) {
		_ = os.Remove(tempName)
		return path, mimeType, nil
	}
	if err := os.Rename(tempName, path); err != nil {
		_ = os.Remove(tempName)
		if fileExists(path) {
			return path, mimeType, nil
		}
		return "", "", fmt.Errorf("failed to store media file: %w", err)
	}
	return path, mimeType, nil
}

func saveMediaFile(dataDir string, data []byte, mimeType, fileName string) (string, string, error) {
	f, err := newMediaTemp(dataDir)
	if err != nil {
		return "", "", err
	}
	if _, err = f.Write(data); err != nil {
		discardMediaTemp(f)
		return "", "", fmt.Errorf("failed to write media file: %w", err)
	}
	return finalizeMediaTemp(f, mimeType, fileName)
}

func fileExists(path string) bool {
	if path == "" {
		return false
	}
	info, err := os.Stat(path)
	return err == nil && !info.IsDir()
}

func checkMediaURL(raw string) (*url.URL, error) {
	parsed, err := url.Parse(raw)
	if err != nil || !mediaURLAllowed(parsed) {
		return nil, errMediaHostNotAllowed
	}
	return parsed, nil
}

var mediaHTTPClient = &http.Client{
	CheckRedirect: func(req *http.Request, via []*http.Request) error {
		if len(via) >= maxMediaRedirects {
			return errors.New("too many redirects")
		}
		if !mediaURLAllowed(req.URL) {
			return errMediaHostNotAllowed
		}
		return nil
	},
}

func httpDownloadToFile(ctx context.Context, rawURL string, maxBytes int64, f *os.File) (string, error) {
	parsed, err := checkMediaURL(rawURL)
	if err != nil {
		return "", err
	}
	ctx, cancel := context.WithTimeout(ctx, mediaHTTPTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, parsed.String(), nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("User-Agent", useragent.UserAgent)
	resp, err := mediaHTTPClient.Do(req)
	if err != nil {
		if errors.Is(err, errMediaHostNotAllowed) {
			return "", errMediaHostNotAllowed
		}
		return "", withoutURL(err)
	}
	defer resp.Body.Close()
	switch {
	case resp.StatusCode == http.StatusForbidden, resp.StatusCode == http.StatusNotFound, resp.StatusCode == http.StatusGone:
		return "", fmt.Errorf("%w (HTTP %d)", errMediaExpired, resp.StatusCode)
	case resp.StatusCode < 200 || resp.StatusCode >= 300:
		return "", fmt.Errorf("unexpected HTTP status %d", resp.StatusCode)
	}
	if resp.ContentLength > maxBytes {
		return "", fmt.Errorf("media larger than %d bytes", maxBytes)
	}
	written, err := io.Copy(f, io.LimitReader(resp.Body, maxBytes+1))
	if err != nil {
		return "", err
	}
	if written > maxBytes {
		return "", fmt.Errorf("media larger than %d bytes", maxBytes)
	}
	return resp.Header.Get("Content-Type"), nil
}

func findAttachment(msg Message, attachmentID string) (Attachment, bool) {
	for _, att := range msg.Attachments {
		if att.ID == attachmentID {
			return att, true
		}
	}
	return Attachment{}, false
}
