package main

import (
	"context"
	"path/filepath"
	"testing"

	"go.mau.fi/mautrix-meta/pkg/messagix/securehistory"
	"go.mau.fi/mautrix-meta/pkg/messagix/table"
)

func TestLogoutWipesAccountData(t *testing.T) {
	b, _, dir := loggedInBackend(t)
	mediaPath, _, err := saveMediaFile(dir, []byte("secret"), "text/plain", "a.txt")
	if err != nil {
		t.Fatal(err)
	}
	b.contactsRequested.Store("42", true)
	b.mu.Lock()
	b.historyBackup = &securehistory.Backup{ID: "x"}
	b.mu.Unlock()
	b.store.UpdateThread("5", func(rec *threadRec) {})
	if err := b.Logout(context.Background()); err != nil {
		t.Fatal(err)
	}
	if fileExists(mediaPath) {
		t.Fatal("media cache survived logout")
	}
	if _, ok := b.contactsRequested.Load("42"); ok || b.historyBackup != nil {
		t.Fatal("per-account state survived logout")
	}
	b.handleTable(&table.LSTable{LSDeleteThenInsertThread: []*table.LSDeleteThenInsertThread{{ThreadKey: 77, ThreadType: table.GROUP_THREAD}}}, modeLive)
	if b.store.ThreadCount() != 0 {
		t.Fatal("tables arriving after logout must be ignored")
	}
	b.persist()
	if fileExists(filepath.Join(dir, cacheFile)) {
		t.Fatal("persist after logout must not write the cache")
	}
}
