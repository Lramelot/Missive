package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/rs/zerolog"
	"go.mau.fi/whatsmeow/proto/waCommon"
	"go.mau.fi/whatsmeow/proto/waConsumerApplication"
	waTypes "go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
	"go.mau.fi/whatsmeow/util/keys"
	"google.golang.org/protobuf/proto"

	"go.mau.fi/mautrix-meta/pkg/messagix"
	"go.mau.fi/mautrix-meta/pkg/messagix/cookies"
	"go.mau.fi/mautrix-meta/pkg/messagix/securehistory"
	"go.mau.fi/mautrix-meta/pkg/messagix/table"
	"go.mau.fi/mautrix-meta/pkg/messagix/types"
)

const testPIN = "482915"

type fakeHistory struct {
	backupErr   error
	enrollErr   error
	enrolls     atomic.Int32
	fetches     atomic.Int32
	pages       map[string][]archiveItem
	sawPending  bool
	epochDevice *securehistory.Device
}

func (f *fakeHistory) GetBackup(ctx context.Context) (*securehistory.Backup, error) {
	if f.backupErr != nil {
		return nil, f.backupErr
	}
	return &securehistory.Backup{ID: "backup-1"}, nil
}

func (f *fakeHistory) Enroll(ctx context.Context, backup *securehistory.Backup, pin []byte, userID int64, identity *keys.KeyPair, persistPending func(*securehistory.Device) error) (*securehistory.Device, error) {
	f.enrolls.Add(1)
	if f.enrollErr != nil {
		return nil, f.enrollErr
	}
	if string(pin) != testPIN {
		return nil, &securehistory.RecoveryError{Code: "wrong pin", AttemptsRemaining: new(4)}
	}
	if err := persistPending(&securehistory.Device{BackupID: backup.ID}); err != nil {
		return nil, err
	}
	f.sawPending = true
	return &securehistory.Device{ID: "device-1", BackupID: backup.ID}, nil
}

func (f *fakeHistory) FetchPage(ctx context.Context, device *securehistory.Device, chat waTypes.JID, selfID int64, beforeMS int64, count int) (*historyPage, error) {
	f.fetches.Add(1)
	items, ok := f.pages[chat.User]
	if !ok {
		return nil, errors.New("archive thread was not found")
	}
	return &historyPage{items: items, device: f.epochDevice}, nil
}

func archiveText(chat waTypes.JID, sender, id, text string, ts time.Time) archiveItem {
	return archiveItem{evt: &events.FBMessage{
		Info: waTypes.MessageInfo{
			MessageSource: waTypes.MessageSource{Chat: chat, Sender: waTypes.NewJID(sender, waTypes.MessengerServer), IsFromMe: sender == testSelf},
			ID:            id,
			Timestamp:     ts,
		},
		Message: &waConsumerApplication.ConsumerApplication{Payload: &waConsumerApplication.ConsumerApplication_Payload{
			Payload: &waConsumerApplication.ConsumerApplication_Payload_Content{Content: &waConsumerApplication.ConsumerApplication_Content{
				Content: &waConsumerApplication.ConsumerApplication_Content_MessageText{MessageText: &waCommon.MessageText{Text: proto.String(text)}},
			}},
		}},
	}}
}

func newHistoryBackend(t *testing.T, fake *fakeHistory) (*MetaBackend, *syncBuffer, string) {
	t.Helper()
	b, buf, dir := newTestMetaBackend(t)
	c := &cookies.Cookies{Platform: types.Messenger}
	b.mu.Lock()
	b.cookies = c
	b.sess = &session{User: User{ID: testSelf, Name: "Me"}}
	b.client = messagix.NewClient(c, zerolog.Nop(), b.messagixConfig())
	b.connState = StateConnected
	b.historyFactory = func(*messagix.Client) historyService { return fake }
	b.identityFn = func(context.Context) (*keys.KeyPair, error) { return keys.NewKeyPair(), nil }
	b.mu.Unlock()
	b.store.SetSelfID(testSelf)
	return b, buf, dir
}

func TestHistoryErrorMapping(t *testing.T) {
	cases := []struct {
		err       error
		code      string
		remaining any
	}{
		{securehistory.ErrNoPINBackup, CodeUnsupported, nil},
		{fmt.Errorf("wrapped: %w", &securehistory.RecoveryError{Code: "x", AttemptsRemaining: new(3)}), CodeBadPIN, 3},
		{&securehistory.RecoveryError{Code: "no attempts remaining", AttemptsRemaining: new(0)}, CodePINLocked, nil},
		{&securehistory.RecoveryError{Code: "x"}, CodeBadPIN, nil},
		{types.ErrPleaseLogIn, CodeAuthFailed, nil},
		{&securehistory.EnrollmentError{Code: "rejected"}, CodeInternal, nil},
		{errors.New("boom"), CodeInternal, nil},
		{newError(CodeNotConnected, "x"), CodeNotConnected, nil},
	}
	for i, c := range cases {
		body := toErrorBody(historyError(c.err))
		if body.Code != c.code {
			t.Errorf("case %d: got %s want %s", i, body.Code, c.code)
		}
		if c.remaining == nil && body.Data != nil {
			t.Errorf("case %d: unexpected data %v", i, body.Data)
		}
		if c.remaining != nil && (body.Data == nil || body.Data["attempts_remaining"] != c.remaining) {
			t.Errorf("case %d: bad data %v", i, body.Data)
		}
	}
	line, _ := json.Marshal(errResponse{ID: 1, Error: toErrorBody(historyError(&securehistory.RecoveryError{AttemptsRemaining: new(2)}))})
	if !strings.Contains(string(line), `"data":{"attempts_remaining":2}`) {
		t.Fatalf("data not serialized: %s", line)
	}
}

func TestRestoreHistoryAppliesArchive(t *testing.T) {
	chat := waTypes.NewJID(testPeerID, waTypes.MessengerServer)
	now := time.Now()
	fake := &fakeHistory{pages: map[string][]archiveItem{
		testPeerID: {
			archiveText(chat, testPeerID, "A1", "older", now.Add(-72*time.Hour)),
			archiveText(chat, testSelf, "A2", "newest", now.Add(-11*time.Minute)),
		},
	}}
	fake.epochDevice = &securehistory.Device{ID: "device-1", BackupID: "backup-1", Epochs: map[string]securehistory.Epoch{"e1": {}}}
	b, buf, dir := newHistoryBackend(t, fake)
	b.handleTable(&table.LSTable{
		LSVerifyHybridThreadExists: []*table.LSVerifyHybridThreadExists{{ThreadKey: testLegacyFB, ThreadJID: testPeer, ThreadType: table.ENCRYPTED_OVER_WA_ONE_TO_ONE, LastActivityTimestampMS: 1_600_000_000_000}},
	}, modeHistory)
	ctx := context.Background()

	status, err := b.HistoryStatus(ctx)
	if err != nil || status.Backup != BackupAvailable || status.Restored {
		t.Fatalf("unexpected status %+v %v", status, err)
	}
	if err := b.RestoreHistory(ctx, "000000"); toErrorBody(err).Code != CodeBadPIN || toErrorBody(err).Data["attempts_remaining"] != 4 {
		t.Fatalf("wrong pin must be bad_pin with attempts, got %+v", toErrorBody(err))
	}
	if b.historyEnrolled() {
		t.Fatal("failed restore must not enroll")
	}
	before := len(buf.Lines())
	if err := b.RestoreHistory(ctx, testPIN); err != nil {
		t.Fatal(err)
	}
	if !fake.sawPending {
		t.Fatal("pending enrollment must be persisted before AddDevice")
	}
	th, _ := b.store.ThreadView(testPeerID)
	if th.LastActivityMS != now.Add(-11*time.Minute).UnixMilli() || th.Snippet != "newest" {
		t.Fatalf("archive did not refresh thread: %+v", th)
	}
	msgs := b.store.Messages(testPeerID, 0, 10)
	if len(msgs) != 2 || !msgs[1].IsOutgoing || msgs[0].IsOutgoing {
		t.Fatalf("archive messages not stored: %+v", msgs)
	}
	newLines := strings.Join(buf.Lines()[before:], "\n")
	if strings.Contains(newLines, `"event":"message"`) {
		t.Fatal("restored history must not emit message events")
	}
	if !strings.Contains(newLines, `"event":"threads"`) {
		t.Fatal("restored history must emit threads upserts")
	}
	raw, err := readSecretFile(filepath.Join(dir, sessionFile), zerolog.Nop())
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), testPIN) || strings.Contains(strings.Join(buf.Lines(), "\n"), testPIN) {
		t.Fatal("PIN leaked")
	}
	var saved session
	if err := json.Unmarshal(raw, &saved); err != nil || !saved.HistoryRestored || saved.HistoryDevice == nil || saved.HistoryDevice.ID != "device-1" || len(saved.HistoryDevice.Epochs) != 1 {
		t.Fatalf("history enrollment not persisted: %s", raw)
	}
	status, _ = b.HistoryStatus(ctx)
	if !status.Restored {
		t.Fatal("status must report restored")
	}
	if err := b.RestoreHistory(ctx, testPIN); err != nil || fake.enrolls.Load() != 2 {
		t.Fatalf("second restore must reuse the enrollment: %v enrolls=%d", err, fake.enrolls.Load())
	}

	reloaded, err := NewMetaBackend(dir, "", NewOutput(&syncBuffer{}, zerolog.Nop()), zerolog.Nop())
	if err == nil {
		defer reloaded.Close()
		if !reloaded.historyEnrolled() {
			t.Fatal("enrollment must survive a restart")
		}
	}
}

func TestRestoreHistoryErrors(t *testing.T) {
	fake := &fakeHistory{backupErr: securehistory.ErrNoPINBackup}
	b, _, _ := newHistoryBackend(t, fake)
	ctx := context.Background()
	if status, err := b.HistoryStatus(ctx); err != nil || status.Backup != BackupNone {
		t.Fatalf("unexpected %+v %v", status, err)
	}
	if err := b.RestoreHistory(ctx, testPIN); toErrorBody(err).Code != CodeUnsupported {
		t.Fatalf("no backup must be unsupported, got %v", err)
	}
	fake.backupErr = errors.New("network down")
	if status, _ := b.HistoryStatus(ctx); status.Backup != BackupUnknown {
		t.Fatalf("unexpected %+v", status)
	}
	fake.backupErr = nil
	fake.enrollErr = &securehistory.RecoveryError{Code: "locked", AttemptsRemaining: new(0)}
	if err := b.RestoreHistory(ctx, testPIN); toErrorBody(err).Code != CodePINLocked {
		t.Fatalf("expected pin_locked, got %v", err)
	}
	b.mu.Lock()
	b.connState = StateConnecting
	b.mu.Unlock()
	if _, err := b.HistoryStatus(ctx); toErrorBody(err).Code != CodeNotConnected {
		t.Fatalf("expected not_connected, got %v", err)
	}
}

func TestOldMessagesAreNotLive(t *testing.T) {
	b, buf, _ := newTestMetaBackend(t)
	b.store.SetSelfID(testSelf)
	old := time.Now().Add(-48 * time.Hour).UnixMilli()
	recent := time.Now().Add(-30 * time.Second).UnixMilli()
	b.handleTable(&table.LSTable{
		LSDeleteThenInsertThread: []*table.LSDeleteThenInsertThread{{ThreadKey: 5, ThreadType: table.GROUP_THREAD, LastActivityTimestampMs: old - 1000}},
	}, modeHistory)
	b.handleTable(&table.LSTable{
		LSInsertMessage: []*table.LSInsertMessage{{ThreadKey: 5, MessageId: "mid.$old", Text: "x", TimestampMs: old, SenderId: 7}},
	}, modeLive)
	if n := len(eventsOf(t, buf, EventMessage)); n != 0 {
		t.Fatalf("old LS insert must not be live, got %d message events", n)
	}
	if th, _ := b.store.ThreadView("5"); th.LastActivityMS != old || th.UnreadCount != 1 {
		t.Fatalf("old insert must still update activity without unread bump: %+v", th)
	}
	chat := waTypes.NewJID(testPeerID, waTypes.MessengerServer)
	b.handleFBMessage(archiveText(chat, testPeerID, "W1", "offline old", time.UnixMilli(old)).evt)
	if n := len(eventsOf(t, buf, EventMessage)); n != 0 {
		t.Fatalf("old offline e2ee message must not be live, got %d", n)
	}
	b.handleFBMessage(archiveText(chat, testPeerID, "W2", "offline recent", time.UnixMilli(recent)).evt)
	if n := len(eventsOf(t, buf, EventMessage)); n != 1 {
		t.Fatalf("recent offline e2ee message must be live, got %d", n)
	}
}

func TestMutedThreads(t *testing.T) {
	b, buf, _ := newTestMetaBackend(t)
	now := time.Now().UnixMilli()
	b.handleTable(&table.LSTable{
		LSDeleteThenInsertThread: []*table.LSDeleteThenInsertThread{
			{ThreadKey: 1, ThreadType: table.GROUP_THREAD, MuteExpireTimeMs: -1},
			{ThreadKey: 2, ThreadType: table.GROUP_THREAD, MuteExpireTimeMs: now + 3_600_000},
			{ThreadKey: 3, ThreadType: table.GROUP_THREAD, MuteExpireTimeMs: now - 3_600_000},
			{ThreadKey: 4, ThreadType: table.GROUP_THREAD},
		},
	}, modeHistory)
	want := map[string]bool{"1": true, "2": true, "3": false, "4": false}
	for id, muted := range want {
		if th, _ := b.store.ThreadView(id); th.Muted != muted {
			t.Errorf("thread %s muted=%v want %v", id, th.Muted, muted)
		}
	}
	b.handleTable(&table.LSTable{
		LSUpdateThreadMuteSetting: []*table.LSUpdateThreadMuteSetting{{ThreadKey: 4, MuteExpireTimeMS: -1}, {ThreadKey: 1, MuteExpireTimeMS: 0}},
	}, modeLive)
	if th, _ := b.store.ThreadView("4"); !th.Muted {
		t.Fatal("mute update not applied")
	}
	if th, _ := b.store.ThreadView("1"); th.Muted {
		t.Fatal("unmute not applied")
	}
	last := eventsOf(t, buf, EventThreads)
	if len(last) == 0 || !strings.Contains(string(last[len(last)-1]), `"muted":true`) {
		t.Fatalf("mute change not emitted: %s", last)
	}
}

func TestDemoHistoryProtocol(t *testing.T) {
	h := newDemoHarness(t)
	h.send(`{"id":1,"cmd":"history_status"}`)
	if code := h.errCode(1); code != CodeNotConnected {
		t.Fatalf("got %s", code)
	}
	h.send(`{"id":2,"cmd":"connect"}`)
	h.waitEvent(EventReady, 3*time.Second)
	h.send(`{"id":3,"cmd":"history_status"}`, `{"id":4,"cmd":"threads"}`, `{"id":5,"cmd":"restore_history","params":{"pin":"12ab56"}}`)
	var status HistoryStatusResult
	h.okResult(3, &status)
	if status.Backup != BackupAvailable || status.Restored {
		t.Fatalf("unexpected status %+v", status)
	}
	var threads []Thread
	h.okResult(4, &threads)
	muted := 0
	for _, th := range threads {
		if th.Muted {
			muted++
		}
	}
	if muted == 0 || muted == len(threads) {
		t.Fatalf("expected some muted demo threads, got %d", muted)
	}
	if code := h.errCode(5); code != CodeBadRequest {
		t.Fatalf("non-digit pin: got %s", code)
	}
	h.send(`{"id":6,"cmd":"restore_history","params":{"pin":"000000"}}`)
	resp := h.response(6)
	var body ErrorBody
	json.Unmarshal(resp["error"], &body)
	if body.Code != CodeBadPIN || body.Data["attempts_remaining"] != float64(9) {
		t.Fatalf("unexpected %+v", body)
	}
	before := len(h.out.Lines())
	h.send(`{"id":7,"cmd":"restore_history","params":{"pin":"123456"}}`)
	h.okResult(7, nil)
	after := strings.Join(h.out.Lines()[before:], "\n")
	if !strings.Contains(after, `"event":"threads"`) || strings.Contains(after, `"event":"message"`) {
		t.Fatalf("restore must bump threads without message events: %s", after)
	}
	h.send(`{"id":8,"cmd":"history_status"}`)
	h.okResult(8, &status)
	if !status.Restored {
		t.Fatal("restore not reflected in status")
	}
	for i := 0; i < demoPINAttempts; i++ {
		h.send(fmt.Sprintf(`{"id":%d,"cmd":"restore_history","params":{"pin":"999999"}}`, 100+i))
	}
	if code := h.errCode(100 + demoPINAttempts - 2); code != CodeBadPIN {
		t.Fatalf("got %s", code)
	}
	if code := h.errCode(100 + demoPINAttempts - 1); code != CodePINLocked {
		t.Fatalf("got %s", code)
	}
	h.send(`{"id":200,"cmd":"restore_history","params":{"pin":"123456"}}`)
	if code := h.errCode(200); code != CodePINLocked {
		t.Fatalf("locked demo must stay locked, got %s", code)
	}
}

func TestUnconfirmedEnrollmentIsNotRedone(t *testing.T) {
	fake := &fakeHistory{}
	b, _, _ := newHistoryBackend(t, fake)
	b.mu.Lock()
	b.sess.HistoryDevice = &securehistory.Device{BackupID: "backup-1"}
	b.mu.Unlock()
	err := b.RestoreHistory(context.Background(), testPIN)
	if err == nil {
		t.Fatal("restore over an unconfirmed enrollment must fail")
	}
	if body := toErrorBody(err); body.Code != CodeInternal || body.Message != "history enrollment unconfirmed" {
		t.Fatalf("unexpected %+v", body)
	}
	if fake.enrolls.Load() != 0 {
		t.Fatal("must never enroll twice")
	}
}
