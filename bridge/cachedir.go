package main

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"time"

	"github.com/rs/zerolog"
)

const maxEvictableCacheBytes = 500 << 20

var movableCacheDirs = []string{mediaDirName, avatarDirName}

func sameDir(a, b string) bool {
	a, b = filepath.Clean(a), filepath.Clean(b)
	if runtime.GOOS == "windows" {
		return strings.EqualFold(a, b)
	}
	return a == b
}

func migrateCacheDirs(dataDir, cacheDir string, log zerolog.Logger) {
	if sameDir(dataDir, cacheDir) {
		return
	}
	for _, name := range movableCacheDirs {
		from, to := filepath.Join(dataDir, name), filepath.Join(cacheDir, name)
		info, err := os.Stat(from)
		if err != nil || !info.IsDir() {
			continue
		}
		if err := moveDir(from, to); err != nil {
			log.Warn().Err(err).Str("dir", name).Msg("Failed to move a cache directory out of the data dir, leaving it in place")
			continue
		}
		log.Info().Str("dir", name).Msg("Moved a cache directory out of the data dir")
	}
}

func moveDir(from, to string) error {
	if err := os.MkdirAll(filepath.Dir(to), 0o700); err != nil {
		return err
	}
	if _, err := os.Stat(to); errors.Is(err, os.ErrNotExist) {
		if err = os.Rename(from, to); err == nil {
			return nil
		}
	}
	if err := copyTree(from, to); err != nil {
		return err
	}
	return os.RemoveAll(from)
}

func copyTree(from, to string) error {
	return filepath.WalkDir(from, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(from, path)
		if err != nil {
			return err
		}
		target := filepath.Join(to, rel)
		if d.IsDir() {
			return os.MkdirAll(target, 0o700)
		}
		if !d.Type().IsRegular() || fileExists(target) {
			return nil
		}
		return copyFile(path, target)
	})
}

func copyFile(from, to string) error {
	src, err := os.Open(from)
	if err != nil {
		return err
	}
	defer src.Close()
	tmp, err := os.CreateTemp(filepath.Dir(to), ".move-*.part")
	if err != nil {
		return err
	}
	_, err = io.Copy(tmp, src)
	if closeErr := tmp.Close(); err == nil {
		err = closeErr
	}
	if err == nil {
		err = os.Rename(tmp.Name(), to)
	}
	if err != nil {
		_ = os.Remove(tmp.Name())
		return fmt.Errorf("failed to copy %s: %w", filepath.Base(from), err)
	}
	return nil
}

type evictCandidate struct {
	path    string
	size    int64
	modTime time.Time
}

func evictableCacheDirs(cacheDir string) []string {
	return []string{filepath.Join(cacheDir, mediaDirName, thumbDirName), filepath.Join(cacheDir, avatarDirName)}
}

func evictCache(dirs []string, maxBytes int64) (removed int, freed int64) {
	var files []evictCandidate
	var total int64
	for _, dir := range dirs {
		_ = filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() || !d.Type().IsRegular() || strings.HasSuffix(d.Name(), ".part") {
				return nil
			}
			info, err := d.Info()
			if err != nil {
				return nil
			}
			files = append(files, evictCandidate{path: path, size: info.Size(), modTime: info.ModTime()})
			total += info.Size()
			return nil
		})
	}
	if total <= maxBytes {
		return 0, 0
	}
	slices.SortFunc(files, func(a, b evictCandidate) int { return a.modTime.Compare(b.modTime) })
	for _, f := range files {
		if total <= maxBytes {
			break
		}
		if os.Remove(f.path) == nil {
			total -= f.size
			freed += f.size
			removed++
		}
	}
	return removed, freed
}

func startCacheEviction(cacheDir string, log zerolog.Logger) {
	goSafe(log, "cache_eviction", func() {
		removed, freed := evictCache(evictableCacheDirs(cacheDir), maxEvictableCacheBytes)
		if removed > 0 {
			log.Info().Int("files", removed).Int64("bytes", freed).Msg("Evicted old thumbnails and avatars")
		}
	})
}
