package main

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/rs/zerolog"
)

func requireDPAPI(t *testing.T) {
	t.Helper()
	if !secretsEncrypted {
		t.Skip("DPAPI is only available on Windows")
	}
}

func TestSecretFileRoundTripIsEncryptedAtRest(t *testing.T) {
	requireDPAPI(t)
	path := filepath.Join(t.TempDir(), cookiesFile)
	plain := []byte(`{"c_user":"4242","xs":"very-secret-xs","datr":"d"}`)
	if err := writeSecretFile(path, plain); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.HasPrefix(raw, secretMagic) || bytes.Contains(raw, []byte("very-secret-xs")) || bytes.Contains(raw, []byte("c_user")) {
		t.Fatalf("secret file must be DPAPI-encrypted on disk, got %q", raw)
	}
	got, err := readSecretFile(path, zerolog.Nop())
	if err != nil || !bytes.Equal(got, plain) {
		t.Fatalf("round trip failed: %q %v", got, err)
	}
	tampered := append([]byte(nil), raw...)
	tampered[len(tampered)-1] ^= 0x01
	if err := os.WriteFile(path, tampered, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := readSecretFile(path, zerolog.Nop()); !errors.Is(err, errSecretUnreadable) {
		t.Fatalf("a tampered file must be unreadable, got %v", err)
	}
	empty := filepath.Join(t.TempDir(), stateFile)
	if err := writeSecretFile(empty, nil); err != nil {
		t.Fatal(err)
	}
	if got, err := readSecretFile(empty, zerolog.Nop()); err != nil || len(got) != 0 {
		t.Fatalf("empty payload round trip failed: %q %v", got, err)
	}
}

func TestSecretFileMigratesPlaintextAndWipesIt(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, sessionFile)
	plain := []byte(`{"user":{"id":"4242","name":"Alice Secret"}}`)
	if err := os.WriteFile(path, plain, 0o644); err != nil {
		t.Fatal(err)
	}
	leftover := filepath.Join(dir, sessionFile+".123456.tmp")
	if err := os.WriteFile(leftover, plain, 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := readSecretFile(path, zerolog.Nop())
	if err != nil || !bytes.Equal(got, plain) {
		t.Fatalf("plaintext must still be readable during migration: %q %v", got, err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !secretsEncrypted {
		if info, err := os.Stat(path); err != nil || info.Mode().Perm() != 0o600 {
			t.Fatalf("plaintext session must be restricted to 0600: %v %v", info, err)
		}
		return
	}
	if !bytes.HasPrefix(raw, secretMagic) || bytes.Contains(raw, []byte("Alice Secret")) {
		t.Fatalf("plaintext session was not rewritten encrypted: %q", raw)
	}
	if _, err := os.Stat(leftover); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("plaintext leftovers must be removed: %v", err)
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != 1 {
		t.Fatalf("migration must leave exactly the encrypted file, got %d entries", len(entries))
	}
	again, err := readSecretFile(path, zerolog.Nop())
	if err != nil || !bytes.Equal(again, plain) {
		t.Fatalf("migrated file must decrypt: %q %v", again, err)
	}
}

func TestWipeFileOverwritesContent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "wipe")
	if err := os.WriteFile(path, []byte("secret-cookie-value"), 0o600); err != nil {
		t.Fatal(err)
	}
	wipeFile(path)
	raw, err := os.ReadFile(path)
	if err != nil || len(raw) != len("secret-cookie-value") || !bytes.Equal(raw, make([]byte, len(raw))) {
		t.Fatalf("wipe must zero the file in place: %q %v", raw, err)
	}
}

func TestUndecryptableSessionMeansLoggedOut(t *testing.T) {
	dir := t.TempDir()
	garbage := append(append([]byte{}, secretMagic...), []byte("not a dpapi blob from this user")...)
	for _, name := range []string{sessionFile, cookiesFile, stateFile, cacheFile} {
		if err := os.WriteFile(filepath.Join(dir, name), garbage, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	b, err := NewMetaBackend(dir, "", NewOutput(&syncBuffer{}, zerolog.Nop()), zerolog.Nop())
	if err != nil {
		t.Fatalf("an undecryptable session must not prevent startup: %v", err)
	}
	defer b.Close()
	status, _ := b.Status(context.Background())
	if status.LoggedIn || status.User != nil {
		t.Fatalf("an undecryptable session must read as logged out: %+v", status)
	}
	if b.loadCachedState(nil) {
		t.Fatal("an undecryptable connection state must not be used")
	}
	if fileExists(filepath.Join(dir, stateFile)) {
		t.Fatal("an undecryptable connection state must be discarded")
	}
}

func TestUndecryptableCookiesOnlyMeansLoggedOut(t *testing.T) {
	requireDPAPI(t)
	dir := t.TempDir()
	if err := writeSecretFile(filepath.Join(dir, sessionFile), []byte(`{"user":{"id":"4242","name":"Me"}}`)); err != nil {
		t.Fatal(err)
	}
	garbage := append(append([]byte{}, secretMagic...), 1, 2, 3, 4)
	if err := os.WriteFile(filepath.Join(dir, cookiesFile), garbage, 0o600); err != nil {
		t.Fatal(err)
	}
	b, err := NewMetaBackend(dir, "", NewOutput(&syncBuffer{}, zerolog.Nop()), zerolog.Nop())
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	if status, _ := b.Status(context.Background()); status.LoggedIn {
		t.Fatalf("undecryptable cookies must read as logged out: %+v", status)
	}
}

func TestPersistedPlaintextSessionIsMigratedOnStartup(t *testing.T) {
	requireDPAPI(t)
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, sessionFile), []byte(`{"user":{"id":"4242","name":"Me"}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, cookiesFile), []byte(`{"c_user":"4242","xs":"secret-xs","datr":"d"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	b, err := NewMetaBackend(dir, "", NewOutput(&syncBuffer{}, zerolog.Nop()), zerolog.Nop())
	if err != nil {
		t.Fatal(err)
	}
	status, _ := b.Status(context.Background())
	if !status.LoggedIn || status.User == nil || status.User.ID != "4242" {
		t.Fatalf("migrated session must stay logged in: %+v", status)
	}
	b.store.PutMessage(Message{ID: "m1", ThreadID: "t1", Text: "secret message body", TimestampMS: 1})
	b.persist()
	b.Close()
	for _, name := range []string{sessionFile, cookiesFile, cacheFile} {
		raw, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if !bytes.HasPrefix(raw, secretMagic) || bytes.Contains(raw, []byte("secret-xs")) || bytes.Contains(raw, []byte("secret message body")) {
			t.Fatalf("%s must be encrypted at rest: %q", name, raw)
		}
	}
	reloaded, err := NewMetaBackend(dir, "", NewOutput(&syncBuffer{}, zerolog.Nop()), zerolog.Nop())
	if err != nil {
		t.Fatal(err)
	}
	defer reloaded.Close()
	if status, _ := reloaded.Status(context.Background()); !status.LoggedIn {
		t.Fatalf("encrypted session must load back: %+v", status)
	}
	if msgs := reloaded.store.Messages("t1", 0, 0); len(msgs) != 1 || msgs[0].Text != "secret message body" {
		t.Fatalf("encrypted cache must load back: %+v", msgs)
	}
}

func TestDeviceStoreCreatedPrivate(t *testing.T) {
	path := filepath.Join(t.TempDir(), waDBFile)
	if err := ensurePrivateFile(path); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" && info.Mode().Perm() != 0o600 {
		t.Fatalf("device store must be 0600, got %v", info.Mode().Perm())
	}
}

func TestMigrationKeepsPlaintextWhenTheFileIsHeld(t *testing.T) {
	requireDPAPI(t)
	dir := t.TempDir()
	path := filepath.Join(dir, cookiesFile)
	plain := []byte(`{"c_user":"4242","xs":"secret-xs","datr":"d"}`)
	if err := os.WriteFile(path, plain, 0o600); err != nil {
		t.Fatal(err)
	}
	holder, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	data, err := readSecretFile(path, zerolog.Nop())
	holder.Close()
	if err != nil || !bytes.Equal(data, plain) {
		t.Fatalf("a held file must still be read: %q %v", data, err)
	}
	if onDisk, _ := os.ReadFile(path); !bytes.Equal(onDisk, plain) {
		t.Fatalf("a failed migration must leave the original untouched, got %q", onDisk)
	}
	if entries, _ := os.ReadDir(dir); len(entries) != 1 {
		t.Fatalf("a failed migration must not leave side files, got %d entries", len(entries))
	}
	if again, err := readSecretFile(path, zerolog.Nop()); err != nil || !bytes.Equal(again, plain) {
		t.Fatalf("the next start must read the session: %q %v", again, err)
	}
	if !validSecretFile(path) {
		t.Fatal("the migration must be retried once the file is free")
	}
}

func TestInterruptedMigrationIsRecovered(t *testing.T) {
	requireDPAPI(t)
	plain := []byte(`{"user":{"id":"4242"}}`)
	newer := []byte(`{"user":{"id":"4242","name":"x"}}`)
	for name, setup := range map[string]func(path string) []byte{
		"encrypted target": func(path string) []byte {
			if err := writeSecretFile(path, newer); err != nil {
				t.Fatal(err)
			}
			return newer
		},
		"missing target": func(string) []byte { return plain },
		"zeroed target": func(path string) []byte {
			if err := os.WriteFile(path, make([]byte, len(plain)), 0o600); err != nil {
				t.Fatal(err)
			}
			return plain
		},
	} {
		dir := t.TempDir()
		path := filepath.Join(dir, sessionFile)
		if err := os.WriteFile(migrationSidePath(path), plain, 0o600); err != nil {
			t.Fatal(err)
		}
		want := setup(path)
		got, err := readSecretFile(path, zerolog.Nop())
		if err != nil || !bytes.Equal(got, want) {
			t.Fatalf("%s: got %q %v, want %q", name, got, err, want)
		}
		if fileExists(migrationSidePath(path)) || !validSecretFile(path) {
			t.Fatalf("%s: the side file must be resolved and the target encrypted", name)
		}
	}
}
