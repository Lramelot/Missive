package main

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"math/rand/v2"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"
)

const (
	maxArchivePages             = 2000
	maxArchiveIdlePages         = 2
	maxConsecutiveMediaFailures = 8
	archiveProgressEvery        = 250 * time.Millisecond
	maxSafeNameRunes            = 60
	maxUniqueAttempts           = 1000
	exportMediaDirName          = "media"
	exportDirPermission         = 0o755
	exportFilePermission        = 0o644
	exportDateLayout            = "2006-01-02"
	exportFileStampLayout       = "2006-01-02 150405"
	defaultExportBaseName       = "Conversation"
	archiveTempPattern          = ".missive-*.part"
	zoneIdentifierStream        = ":Zone.Identifier"
	zoneIdentifierInternet      = "[ZoneTransfer]\r\nZoneId=3\r\n"
)

var (
	archivePageDelay  = 1500 * time.Millisecond
	archivePageJitter = time.Second
)

var (
	exportMediaKinds      = []string{"image", "video", "audio", "file", "sticker"}
	defaultSaveMediaKinds = []string{"image", "video"}
	errArchiveCancelled   = errors.New("archive job cancelled")
)

var windowsReservedNames = map[string]bool{
	"CON": true, "PRN": true, "AUX": true, "NUL": true, "CLOCK$": true,
	"COM0": true, "COM1": true, "COM2": true, "COM3": true, "COM4": true, "COM5": true, "COM6": true, "COM7": true, "COM8": true, "COM9": true,
	"LPT0": true, "LPT1": true, "LPT2": true, "LPT3": true, "LPT4": true, "LPT5": true, "LPT6": true, "LPT7": true, "LPT8": true, "LPT9": true,
	"COM¹": true, "COM²": true, "COM³": true, "LPT¹": true, "LPT²": true, "LPT³": true,
	"CONIN$": true, "CONOUT$": true,
}

type archiveSource interface {
	archiveThread(threadID string) (threadRec, error)
	loadFullHistory(ctx context.Context, rec threadRec, progress func())
	archiveMedia(ctx context.Context, msg Message, att Attachment, dst *os.File) (string, error)
	archiveStore() *Store
	archiveOut() Emitter
	archiveJobs() *archiveJobs
}

type archiveJobs struct {
	mu     sync.Mutex
	cancel context.CancelCauseFunc
}

func (j *archiveJobs) start(ctx context.Context) (context.Context, func(), error) {
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.cancel != nil {
		return nil, nil, newError(CodeBusy, "an archive job is already running")
	}
	jobCtx, cancel := context.WithCancelCause(ctx)
	j.cancel = cancel
	return jobCtx, func() {
		cancel(nil)
		j.mu.Lock()
		j.cancel = nil
		j.mu.Unlock()
	}, nil
}

func (j *archiveJobs) stop() bool {
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.cancel == nil {
		return false
	}
	j.cancel(errArchiveCancelled)
	return true
}

func cancelledError(ctx context.Context) error {
	if ctx.Err() == nil {
		return nil
	}
	return newError(CodeCancelled, "Export annulé")
}

func archivePause(ctx context.Context) bool {
	jitter := time.Duration(0)
	if archivePageJitter > 0 {
		jitter = rand.N(archivePageJitter)
	}
	return sleepCtx(ctx, archivePageDelay+jitter)
}

func safeFileName(name string, fallback string) string {
	var b strings.Builder
	space := false
	for _, r := range name {
		if unicode.IsSpace(r) {
			if !space && b.Len() > 0 {
				b.WriteByte(' ')
			}
			space = true
			continue
		}
		if unicode.Is(unicode.Cf, r) && r != '‍' {
			continue
		}
		space = false
		if unicode.IsControl(r) || r == utf8.RuneError || strings.ContainsRune(`<>:"/\|?*`, r) {
			r = '_'
		}
		b.WriteRune(r)
	}
	out := b.String()
	if utf8.RuneCountInString(out) > maxSafeNameRunes {
		out = string([]rune(out)[:maxSafeNameRunes])
	}
	out = strings.TrimRight(out, ". ")
	out = strings.TrimLeft(out, " ")
	if out == "" || strings.Trim(out, "_.") == "" {
		out = fallback
	}
	stem := out
	if i := strings.IndexByte(stem, '.'); i >= 0 {
		stem = stem[:i]
	}
	if windowsReservedNames[strings.ToUpper(strings.TrimRight(stem, " "))] {
		out = "_" + out
	}
	return out
}

func numbered(base string, n int) string {
	if n <= 1 {
		return base
	}
	return base + " (" + strconv.Itoa(n) + ")"
}

func createUniqueDir(parent, base string) (string, error) {
	for n := 1; n <= maxUniqueAttempts; n++ {
		path := filepath.Join(parent, numbered(base, n))
		err := os.Mkdir(path, exportDirPermission)
		if err == nil {
			return path, nil
		}
		if !errors.Is(err, fs.ErrExist) {
			return "", err
		}
	}
	return "", fmt.Errorf("too many existing folders named %q", base)
}

func checkTargetDir(dir string) (string, error) {
	if strings.TrimSpace(dir) == "" {
		return "", newError(CodeBadRequest, "dir is required")
	}
	abs, err := filepath.Abs(dir)
	if err != nil {
		return "", newError(CodeBadRequest, "invalid dir")
	}
	info, err := os.Stat(abs)
	if err != nil || !info.IsDir() {
		return "", newError(CodeBadRequest, "dir does not exist or is not a folder")
	}
	return abs, nil
}

func mediaFileStem(ts int64, att Attachment, messageID string) string {
	stamp := time.UnixMilli(ts).Local().Format(exportFileStampLayout)
	original := strings.TrimSuffix(att.FileName, filepath.Ext(att.FileName))
	if strings.TrimSpace(original) == "" {
		original = att.ID
	}
	if strings.TrimSpace(original) == "" {
		original = messageID
	}
	return safeFileName(stamp+" "+original, stamp)
}

func predictedMediaExt(att Attachment) string {
	ext := extensionFor(att.MimeType, att.FileName)
	if ext != defaultMediaExt {
		return ext
	}
	switch att.Kind {
	case "image", "sticker":
		return ".jpg"
	case "video":
		return ".mp4"
	case "audio":
		return ".m4a"
	}
	return ext
}

func markFromInternet(path string) {
	if runtime.GOOS != "windows" {
		return
	}
	_ = os.WriteFile(path+zoneIdentifierStream, []byte(zoneIdentifierInternet), exportFilePermission)
}

func fileDigest(path string) (int64, []byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return 0, nil, err
	}
	defer f.Close()
	h := sha256.New()
	n, err := io.Copy(h, f)
	return n, h.Sum(nil), err
}

func sameContent(a, b string) bool {
	ia, err := os.Stat(a)
	if err != nil {
		return false
	}
	ib, err := os.Stat(b)
	if err != nil || ia.Size() != ib.Size() {
		return false
	}
	_, ha, errA := fileDigest(a)
	_, hb, errB := fileDigest(b)
	return errA == nil && errB == nil && bytes.Equal(ha, hb)
}

func alreadySaved(dir, stem, ext string, size int64) bool {
	if size <= 0 {
		return false
	}
	for n := 1; n <= maxUniqueAttempts; n++ {
		info, err := os.Stat(filepath.Join(dir, numbered(stem, n)+ext))
		if err != nil {
			return false
		}
		if info.Mode().IsRegular() && info.Size() == size {
			return true
		}
	}
	return false
}

func placeFile(tempPath, dir, stem, ext string, dedupe bool) (string, bool, error) {
	for n := 1; n <= maxUniqueAttempts; n++ {
		name := numbered(stem, n) + ext
		path := filepath.Join(dir, name)
		if _, err := os.Lstat(path); err == nil {
			if dedupe && sameContent(path, tempPath) {
				_ = os.Remove(tempPath)
				return name, true, nil
			}
			continue
		} else if !errors.Is(err, fs.ErrNotExist) {
			return "", false, err
		}
		reserved, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, exportFilePermission)
		if errors.Is(err, fs.ErrExist) {
			continue
		}
		if err != nil {
			return "", false, err
		}
		_ = reserved.Close()
		if err := os.Rename(tempPath, path); err != nil {
			_ = os.Remove(path)
			return "", false, err
		}
		markFromInternet(path)
		return name, false, nil
	}
	return "", false, fmt.Errorf("too many existing files named %q", stem)
}

func fetchArchiveMedia(ctx context.Context, src archiveSource, dir string, msg Message, att Attachment, dedupe bool) (string, bool, error) {
	temp, err := os.CreateTemp(dir, archiveTempPattern)
	if err != nil {
		return "", false, err
	}
	tempPath := temp.Name()
	mimeType, err := src.archiveMedia(ctx, msg, att, temp)
	if cerr := temp.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		_ = os.Remove(tempPath)
		return "", false, err
	}
	mimeType = normalizeMime(mimeType, sniffFile(tempPath))
	name, skipped, err := placeFile(tempPath, dir, mediaFileStem(msg.TimestampMS, att, msg.ID), extensionFor(mimeType, att.FileName), dedupe)
	if err != nil {
		_ = os.Remove(tempPath)
	}
	return name, skipped, err
}

func sniffFile(path string) []byte {
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer f.Close()
	buf := make([]byte, mediaSniffBytes)
	n, _ := io.ReadFull(f, buf)
	return buf[:n]
}

func copyFileTo(src string, dst *os.File) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	_, err = io.Copy(dst, in)
	return err
}

type progressThrottle struct {
	last time.Time
}

func (p *progressThrottle) due() bool {
	now := time.Now()
	if now.Sub(p.last) < archiveProgressEvery {
		return false
	}
	p.last = now
	return true
}

func archiveTitle(store *Store, rec threadRec) string {
	if view, ok := store.ThreadView(rec.ID); ok && strings.TrimSpace(view.Name) != "" {
		return view.Name
	}
	return defaultExportBaseName
}

func exportThread(ctx context.Context, src archiveSource, p ExportThreadParams) (ExportThreadResult, error) {
	rec, err := src.archiveThread(p.ThreadID)
	if err != nil {
		return ExportThreadResult{}, err
	}
	parent, err := checkTargetDir(p.Dir)
	if err != nil {
		return ExportThreadResult{}, err
	}
	ctx, done, err := src.archiveJobs().start(ctx)
	if err != nil {
		return ExportThreadResult{}, err
	}
	defer done()
	store, out := src.archiveStore(), src.archiveOut()
	title := archiveTitle(store, rec)
	exportedAt := time.Now()
	folder, err := createUniqueDir(parent, safeFileName(title, defaultExportBaseName)+" "+exportedAt.Format(exportDateLayout))
	if err != nil {
		return ExportThreadResult{}, newError(CodeBadRequest, "dir is not writable: %v", err)
	}
	result, err := writeExport(ctx, src, rec, p, folder, title, exportedAt, out, store)
	if err != nil {
		if cerr := cancelledError(ctx); cerr != nil {
			_ = os.RemoveAll(folder)
			return ExportThreadResult{}, cerr
		}
		return result, err
	}
	return result, nil
}

func writeExport(ctx context.Context, src archiveSource, rec threadRec, p ExportThreadParams, folder, title string, exportedAt time.Time, out Emitter, store *Store) (ExportThreadResult, error) {
	var throttle progressThrottle
	emit := func(force bool, messages, media int) {
		if force || throttle.due() {
			out.Emit(EventExportProgress, ExportProgressEvent{ThreadID: rec.ID, Messages: messages, Media: media})
		}
	}
	emit(true, store.MessageCount(rec.ID), 0)
	src.loadFullHistory(ctx, rec, func() { emit(false, store.MessageCount(rec.ID), 0) })
	if err := cancelledError(ctx); err != nil {
		return ExportThreadResult{}, err
	}
	messages := store.Messages(rec.ID, 0, 0)
	for i := range messages {
		messages[i] = messages[i].normalized()
	}
	result := ExportThreadResult{Path: folder, Messages: len(messages)}
	emit(true, len(messages), 0)
	files := map[string]string{}
	if p.IncludeMedia {
		mediaDir := filepath.Join(folder, exportMediaDirName)
		if err := os.Mkdir(mediaDir, exportDirPermission); err != nil {
			return result, newError(CodeInternal, "failed to create media folder: %v", err)
		}
		failures := 0
		for _, msg := range messages {
			for _, att := range msg.Attachments {
				if !slices.Contains(exportMediaKinds, att.Kind) {
					continue
				}
				if err := cancelledError(ctx); err != nil {
					return result, err
				}
				if failures >= maxConsecutiveMediaFailures {
					result.MediaFailed++
					continue
				}
				name, _, err := fetchArchiveMedia(ctx, src, mediaDir, msg, att, false)
				if err != nil {
					if !msg.Deleted {
						failures++
					}
					result.MediaFailed++
					continue
				}
				if !msg.Deleted {
					failures = 0
				}
				files[mediaKey(msg.ID, att.ID)] = exportMediaDirName + "/" + name
				result.Media++
				emit(false, len(messages), result.Media)
			}
		}
		if result.Media == 0 && result.MediaFailed == 0 {
			_ = os.Remove(mediaDir)
		}
	}
	doc := exportDoc{Title: title, ExportedAt: exportedAt, Messages: messages, Files: files, Thread: rec, Locale: p.Locale}
	if err := writeExportFile(folder, p.Format, doc); err != nil {
		return result, newError(CodeInternal, "failed to write export: %v", err)
	}
	emit(true, len(messages), result.Media)
	return result, nil
}

func writeExportFile(folder, format string, doc exportDoc) error {
	var name string
	var render func(io.Writer, exportDoc) error
	switch format {
	case ExportHTML:
		name, render = "conversation.html", renderExportHTML
	case ExportMarkdown:
		name, render = "conversation.md", renderExportMarkdown
	case ExportJSON:
		name, render = "conversation.json", renderExportJSON
	default:
		return fmt.Errorf("unknown format %q", format)
	}
	path := filepath.Join(folder, name)
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, exportFilePermission)
	if err != nil {
		return err
	}
	w := bufio.NewWriter(f)
	if err := render(w, doc); err != nil {
		_ = f.Close()
		return err
	}
	if err := w.Flush(); err != nil {
		_ = f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	markFromInternet(path)
	return nil
}

func saveThreadMedia(ctx context.Context, src archiveSource, p SaveMediaParams) (SaveMediaResult, error) {
	rec, err := src.archiveThread(p.ThreadID)
	if err != nil {
		return SaveMediaResult{}, err
	}
	parent, err := checkTargetDir(p.Dir)
	if err != nil {
		return SaveMediaResult{}, err
	}
	ctx, done, err := src.archiveJobs().start(ctx)
	if err != nil {
		return SaveMediaResult{}, err
	}
	defer done()
	kinds := p.Kinds
	if len(kinds) == 0 {
		kinds = defaultSaveMediaKinds
	}
	store, out := src.archiveStore(), src.archiveOut()
	dir := filepath.Join(parent, safeFileName(archiveTitle(store, rec), defaultExportBaseName)+exportTextFor(p.Locale).mediaSuffix)
	if err := os.MkdirAll(dir, exportDirPermission); err != nil {
		return SaveMediaResult{}, newError(CodeBadRequest, "dir is not writable: %v", err)
	}
	result := SaveMediaResult{Dir: dir}
	var throttle progressThrottle
	emit := func(force bool, total *int) {
		if force || throttle.due() {
			out.Emit(EventSaveMediaProgress, SaveMediaProgressEvent{ThreadID: rec.ID, Saved: result.Saved, Total: total, Skipped: result.Skipped, Failed: result.Failed})
		}
	}
	emit(true, nil)
	src.loadFullHistory(ctx, rec, func() { emit(false, nil) })
	if err := cancelledError(ctx); err != nil {
		return SaveMediaResult{}, err
	}
	items := store.MediaItems(rec.ID, 0, kinds)
	total := len(items)
	emit(true, &total)
	failures := 0
	for i := len(items) - 1; i >= 0; i-- {
		item := items[i]
		if err := cancelledError(ctx); err != nil {
			return SaveMediaResult{}, err
		}
		if failures >= maxConsecutiveMediaFailures {
			result.Failed++
			continue
		}
		stem := mediaFileStem(item.TimestampMS, item.Attachment, item.MessageID)
		if alreadySaved(dir, stem, predictedMediaExt(item.Attachment), item.Attachment.Size) {
			result.Skipped++
			emit(false, &total)
			continue
		}
		msg := Message{ID: item.MessageID, ThreadID: item.ThreadID, TimestampMS: item.TimestampMS}
		_, skipped, err := fetchArchiveMedia(ctx, src, dir, msg, item.Attachment, true)
		switch {
		case err != nil:
			failures++
			result.Failed++
		case skipped:
			failures = 0
			result.Skipped++
		default:
			failures = 0
			result.Saved++
		}
		emit(false, &total)
	}
	emit(true, &total)
	return result, nil
}
