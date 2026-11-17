package main

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/binary"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/rs/zerolog"
)

const (
	secretKeySize   = 32
	migratingSuffix = ".migrating"
)

var (
	secretMagic         = []byte("MISSIVE-DPAPI-1\n")
	secretEntropy       = []byte("missive-bridge-session-v1")
	errSecretUnreadable = errors.New("secret file cannot be decrypted by this user on this machine")
)

func writeSecretFile(path string, data []byte) error {
	if !secretsEncrypted {
		if err := writeFileAtomic(path, data); err != nil {
			return err
		}
		restrictFile(path)
		return nil
	}
	sealed, err := sealSecret(data)
	if err != nil {
		return err
	}
	return writeFileAtomic(path, sealed)
}

func secretAEAD(key []byte) (cipher.AEAD, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(block)
}

func sealSecret(data []byte) ([]byte, error) {
	key := make([]byte, secretKeySize)
	if _, err := rand.Read(key); err != nil {
		return nil, err
	}
	wrapped, err := protectSecret(key)
	if err != nil {
		return nil, fmt.Errorf("failed to encrypt secret file: %w", err)
	}
	aead, err := secretAEAD(key)
	if err != nil {
		return nil, err
	}
	nonce := make([]byte, aead.NonceSize())
	if _, err = rand.Read(nonce); err != nil {
		return nil, err
	}
	sealed := make([]byte, 0, len(secretMagic)+4+len(wrapped)+len(nonce)+len(data)+aead.Overhead())
	sealed = append(sealed, secretMagic...)
	sealed = binary.BigEndian.AppendUint32(sealed, uint32(len(wrapped)))
	sealed = append(sealed, wrapped...)
	sealed = append(sealed, nonce...)
	return aead.Seal(sealed, nonce, data, secretMagic), nil
}

func openSecret(body []byte) ([]byte, error) {
	if len(body) < 4 {
		return nil, errors.New("truncated header")
	}
	wrappedLen := int(binary.BigEndian.Uint32(body))
	body = body[4:]
	if wrappedLen <= 0 || wrappedLen > len(body) {
		return nil, errors.New("invalid key length")
	}
	key, err := unprotectSecret(body[:wrappedLen])
	if err != nil {
		return nil, err
	}
	if len(key) != secretKeySize {
		return nil, errors.New("invalid key")
	}
	aead, err := secretAEAD(key)
	if err != nil {
		return nil, err
	}
	body = body[wrappedLen:]
	if len(body) < aead.NonceSize() {
		return nil, errors.New("truncated nonce")
	}
	return aead.Open(nil, body[:aead.NonceSize()], body[aead.NonceSize():], secretMagic)
}

func readSecretFile(path string, log zerolog.Logger) ([]byte, error) {
	mu := pathLock(path)
	mu.Lock()
	defer mu.Unlock()
	if secretsEncrypted {
		if err := recoverMigrationLocked(path); err != nil {
			log.Warn().Err(err).Str("file", filepath.Base(path)).Msg("Failed to recover an interrupted encryption at rest")
		}
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	if bytes.HasPrefix(raw, secretMagic) {
		if !secretsEncrypted {
			return nil, errSecretUnreadable
		}
		data, err := openSecret(raw[len(secretMagic):])
		if err != nil {
			return nil, fmt.Errorf("%w: %v", errSecretUnreadable, err)
		}
		return data, nil
	}
	if !secretsEncrypted {
		restrictFile(path)
		return raw, nil
	}
	if err = migrateSecretFileLocked(path, raw); err != nil {
		log.Warn().Err(err).Str("file", filepath.Base(path)).Msg("Failed to encrypt plaintext file at rest, keeping it")
	} else {
		log.Info().Str("file", filepath.Base(path)).Msg("Encrypted plaintext file at rest")
	}
	return raw, nil
}

func migrationSidePath(path string) string {
	return path + migratingSuffix
}

func validSecretFile(path string) bool {
	raw, err := os.ReadFile(path)
	if err != nil || !bytes.HasPrefix(raw, secretMagic) {
		return false
	}
	_, err = openSecret(raw[len(secretMagic):])
	return err == nil
}

func recoverMigrationLocked(path string) error {
	side := migrationSidePath(path)
	if _, err := os.Stat(side); err != nil {
		return nil
	}
	if validSecretFile(path) {
		wipeFile(side)
		return os.Remove(side)
	}
	return renameWithRetry(side, path)
}

func migrateSecretFileLocked(path string, plaintext []byte) error {
	sealed, err := sealSecret(plaintext)
	if err != nil {
		return err
	}
	tmp, err := writeTempSibling(path, sealed)
	if err != nil {
		return err
	}
	side := migrationSidePath(path)
	if err = renameWithRetry(path, side); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("failed to move the plaintext aside: %w", err)
	}
	if err = renameWithRetry(tmp, path); err != nil {
		_ = os.Remove(tmp)
		if restoreErr := renameWithRetry(side, path); restoreErr != nil {
			return fmt.Errorf("failed to install the encrypted file (%v) and to restore the plaintext: %w", err, restoreErr)
		}
		return fmt.Errorf("failed to install the encrypted file: %w", err)
	}
	wipeFile(side)
	_ = os.Remove(side)
	removePlaintextLeftovers(path)
	return nil
}

func removePlaintextLeftovers(path string) {
	dir, prefix := filepath.Dir(path), filepath.Base(path)+"."
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	for _, entry := range entries {
		name := entry.Name()
		if !entry.Type().IsRegular() || !strings.HasPrefix(name, prefix) || !strings.HasSuffix(name, ".tmp") {
			continue
		}
		leftover := filepath.Join(dir, name)
		wipeFile(leftover)
		_ = os.Remove(leftover)
	}
}

func wipeFile(path string) {
	f, err := os.OpenFile(path, os.O_WRONLY, 0)
	if err != nil {
		return
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() {
		return
	}
	zeros := make([]byte, 32*1024)
	remaining := info.Size()
	for remaining > 0 {
		n := int64(len(zeros))
		if remaining < n {
			n = remaining
		}
		if _, err = f.Write(zeros[:n]); err != nil {
			return
		}
		remaining -= n
	}
	_ = f.Sync()
}

func ensurePrivateFile(path string) error {
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return err
	}
	if err = f.Close(); err != nil {
		return err
	}
	for _, p := range []string{path, path + "-wal", path + "-shm", path + "-journal"} {
		if _, err := os.Stat(p); err == nil {
			restrictFile(p)
		}
	}
	return nil
}

func dirSize(root string) int64 {
	var total int64
	_ = filepath.WalkDir(root, func(_ string, d os.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.Type().IsRegular() {
			if info, err := d.Info(); err == nil {
				total += info.Size()
			}
		}
		return nil
	})
	return total
}
