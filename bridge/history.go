package main

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"sync"
	"time"

	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/proto/waArmadilloBackupMessage"
	"go.mau.fi/whatsmeow/proto/waMediaTransport"
	"go.mau.fi/whatsmeow/proto/waMsgApplication"
	waTypes "go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
	"go.mau.fi/whatsmeow/util/keys"
	"google.golang.org/protobuf/proto"

	"go.mau.fi/mautrix-meta/pkg/messagix"
	"go.mau.fi/mautrix-meta/pkg/messagix/httpclient"
	"go.mau.fi/mautrix-meta/pkg/messagix/securehistory"
	"go.mau.fi/mautrix-meta/pkg/messagix/types"
)

const (
	historyPageSize      = 20
	historyWorkers       = 4
	historyThreadTimeout = 45 * time.Second
	historyFutureSlack   = time.Minute
)

type archiveItem struct {
	evt       *events.FBMessage
	editText  *string
	paths     map[string]string
	reactions map[string]string
	reactTS   map[string]int64
}

type historyPage struct {
	items   []archiveItem
	device  *securehistory.Device
	hasMore bool
}

type historyService interface {
	GetBackup(ctx context.Context) (*securehistory.Backup, error)
	Enroll(ctx context.Context, backup *securehistory.Backup, pin []byte, userID int64, identity *keys.KeyPair, persistPending func(*securehistory.Device) error) (*securehistory.Device, error)
	FetchPage(ctx context.Context, device *securehistory.Device, chat waTypes.JID, selfID int64, beforeMS int64, count int) (*historyPage, error)
}

type realHistory struct {
	c *securehistory.Client
}

func newRealHistory(client *messagix.Client) historyService {
	return realHistory{c: client.GetSecureHistoryClient()}
}

func (h realHistory) GetBackup(ctx context.Context) (*securehistory.Backup, error) {
	return h.c.GetBackup(ctx)
}

func (h realHistory) Enroll(ctx context.Context, backup *securehistory.Backup, pin []byte, userID int64, identity *keys.KeyPair, persistPending func(*securehistory.Device) error) (*securehistory.Device, error) {
	recovery, err := h.c.RecoverPIN(ctx, backup, pin)
	if err != nil {
		return nil, err
	}
	virtual, err := h.c.GetVirtualDevice(ctx, backup, userID, recovery.Code)
	if err != nil {
		return nil, err
	}
	epochs, err := virtual.DeriveEpochs()
	if err != nil {
		return nil, err
	}
	enrollment, err := virtual.PrepareDevice(backup.ID, h.c.RegistrationID, identity, epochs)
	if err != nil {
		return nil, err
	}
	if err = persistPending(enrollment.Device); err != nil {
		return nil, err
	}
	device, err := h.c.AddDevice(ctx, enrollment)
	if err != nil {
		var rejected *securehistory.EnrollmentError
		if errors.As(err, &rejected) {
			return nil, errors.Join(err, persistPending(nil))
		}
		return nil, err
	}
	return device, nil
}

func (h realHistory) FetchPage(ctx context.Context, device *securehistory.Device, chat waTypes.JID, selfID int64, beforeMS int64, count int) (*historyPage, error) {
	page, err := h.c.FetchMessages(ctx, device, chat.User, true, beforeMS, count)
	if err != nil {
		return nil, err
	}
	out := &historyPage{hasMore: page.Range != nil && page.Range.HasMoreBefore != nil && *page.Range.HasMoreBefore}
	if page.Epochs != nil && len(page.Epochs.Edges) > 0 {
		updated, err := device.WithEpochs(page.Epochs)
		if err != nil {
			return nil, err
		}
		out.device = updated
		device = updated
	}
	self := strconv.FormatInt(selfID, 10)
	for _, message := range page.Decrypt(ctx, device, chat.User, selfID, chat.Server == waTypes.GroupServer) {
		item, err := decodeArchiveMessage(chat, self, message)
		if err != nil || item == nil {
			continue
		}
		out.items = append(out.items, *item)
	}
	return out, nil
}

func decodeBackupMessage(chat waTypes.JID, self string, message *waArmadilloBackupMessage.BackupMessage, timestampMS int64) (*events.FBMessage, error) {
	payload, ok := message.GetPayload().(*waArmadilloBackupMessage.BackupMessage_EncryptedTransportMessage)
	if !ok {
		return nil, nil
	}
	var application waMsgApplication.MessageApplication
	if err := proto.Unmarshal(payload.EncryptedTransportMessage, &application); err != nil {
		return nil, fmt.Errorf("invalid archived application: %w", err)
	}
	decoded, err := whatsmeow.DecodeFBApplication(&application)
	if err != nil {
		return nil, err
	}
	metadata := message.GetMetadata()
	if metadata.GetMessageID() == "" || metadata.GetSenderID() == "" || timestampMS < 0 {
		return nil, errors.New("missing archive metadata")
	}
	decoded.Info = waTypes.MessageInfo{
		MessageSource: waTypes.MessageSource{
			Chat:     chat,
			Sender:   waTypes.NewJID(metadata.GetSenderID(), waTypes.MessengerServer),
			IsFromMe: metadata.GetSenderID() == self,
			IsGroup:  chat.Server == waTypes.GroupServer,
		},
		ID:        metadata.GetMessageID(),
		Timestamp: time.UnixMilli(timestampMS),
	}
	return &decoded, nil
}

func decodeArchiveMessage(chat waTypes.JID, self string, message *securehistory.DecryptedMessage) (*archiveItem, error) {
	if message == nil || message.Message == nil || message.Message.Message == nil {
		return nil, errors.New("missing archive message")
	}
	parent, err := decodeBackupMessage(chat, self, message.Message.Message, message.Message.TimestampMS)
	if err != nil || parent == nil {
		return nil, err
	}
	item := &archiveItem{evt: parent}
	var editTS int64
	reactionTS := make(map[string]int64)
	for _, supplemental := range message.Supplementals {
		if supplemental == nil || supplemental.Message == nil {
			continue
		}
		decoded, err := decodeBackupMessage(chat, self, supplemental.Message, supplemental.TimestampMS)
		if err != nil || decoded == nil {
			continue
		}
		if reaction := decoded.GetConsumerApplication().GetPayload().GetContent().GetReactionMessage(); reaction != nil {
			if target := reaction.GetKey().GetID(); target != "" && target != parent.Info.ID {
				continue
			}
			actor := decoded.Info.Sender.User
			if prev, ok := reactionTS[actor]; !ok || supplemental.TimestampMS >= prev {
				reactionTS[actor] = supplemental.TimestampMS
				if item.reactions == nil {
					item.reactions = make(map[string]string)
					item.reactTS = make(map[string]int64)
				}
				item.reactions[actor] = reaction.GetText()
				item.reactTS[actor] = supplemental.TimestampMS
			}
			continue
		}
		edit := decoded.GetConsumerApplication().GetPayload().GetContent().GetEditMessage()
		if edit == nil || (edit.GetKey().GetID() != "" && edit.GetKey().GetID() != parent.Info.ID) {
			continue
		}
		if item.editText == nil || supplemental.TimestampMS > editTS {
			text := edit.GetMessage().GetText()
			item.editText = &text
			editTS = supplemental.TimestampMS
		}
	}
	if len(message.Attachments) > 0 {
		item.paths = make(map[string]string, len(message.Attachments))
		for _, att := range message.Attachments {
			if att.ObjectID != "" && att.URL != "" {
				item.paths[att.ObjectID] = att.URL
			}
		}
	}
	return item, nil
}

func historyError(err error) error {
	var recoveryErr *securehistory.RecoveryError
	var enrollErr *securehistory.EnrollmentError
	var be *BridgeError
	switch {
	case errors.As(err, &be):
		return be
	case errors.Is(err, securehistory.ErrNoPINBackup):
		return newError(CodeUnsupported, "this account has no encrypted history backup")
	case errors.As(err, &recoveryErr):
		if recoveryErr.AttemptsRemaining == nil {
			return newError(CodeBadPIN, "Messenger could not verify the PIN (%s)", recoveryErr.Code)
		}
		remaining := *recoveryErr.AttemptsRemaining
		if remaining <= 0 {
			return newError(CodePINLocked, "no PIN attempts remaining")
		}
		bad := newError(CodeBadPIN, "wrong PIN, %d attempts remaining", remaining)
		bad.Data = map[string]any{"attempts_remaining": remaining}
		return bad
	case errors.Is(err, types.ErrPleaseLogIn), errors.Is(err, httpclient.ErrTokenInvalidated):
		return newError(CodeAuthFailed, "Meta session is no longer valid: %v", err)
	case errors.As(err, &enrollErr):
		return newError(CodeInternal, "Meta rejected the history device enrollment: %v", err)
	default:
		return newError(CodeInternal, "history restore failed: %v", err)
	}
}

func (b *MetaBackend) historyDevice() *securehistory.Device {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.sess == nil {
		return nil
	}
	return b.sess.HistoryDevice
}

func (b *MetaBackend) historyEnrolled() bool {
	device := b.historyDevice()
	return device != nil && device.ID != ""
}

func (b *MetaBackend) setHistoryDevice(device *securehistory.Device, restored bool) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.sess == nil {
		return errors.New("not logged in")
	}
	b.sess.HistoryDevice = device
	b.sess.HistoryRestored = restored
	b.saveSessionLocked()
	return nil
}

func (b *MetaBackend) updateHistoryEpochs(previousID string, updated *securehistory.Device) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.sess == nil || b.sess.HistoryDevice == nil || b.sess.HistoryDevice.ID != previousID {
		return
	}
	b.sess.HistoryDevice = updated
	b.saveSessionLocked()
}

func (b *MetaBackend) historyClient() (*messagix.Client, historyService, error) {
	if err := b.requireLogin(); err != nil {
		return nil, nil, err
	}
	b.mu.Lock()
	client := b.client
	connected := b.connState == StateConnected
	factory := b.historyFactory
	b.mu.Unlock()
	if client == nil || !connected {
		return nil, nil, newError(CodeNotConnected, "not connected")
	}
	if factory == nil {
		factory = newRealHistory
	}
	return client, factory(client), nil
}

func (b *MetaBackend) HistoryStatus(ctx context.Context) (HistoryStatusResult, error) {
	_, svc, err := b.historyClient()
	if err != nil {
		return HistoryStatusResult{}, err
	}
	if b.historyEnrolled() {
		return HistoryStatusResult{Backup: BackupAvailable, Restored: true}, nil
	}
	backup, err := svc.GetBackup(ctx)
	switch {
	case errors.Is(err, securehistory.ErrNoPINBackup):
		return HistoryStatusResult{Backup: BackupNone}, nil
	case err != nil:
		b.log.Warn().Err(err).Msg("Failed to check encrypted history backup")
		return HistoryStatusResult{Backup: BackupUnknown}, nil
	}
	b.mu.Lock()
	b.historyBackup = backup
	b.mu.Unlock()
	return HistoryStatusResult{Backup: BackupAvailable}, nil
}

func (b *MetaBackend) identityKey(ctx context.Context) (*keys.KeyPair, error) {
	if _, err := b.waitE2EE(ctx); err != nil {
		return nil, err
	}
	b.e2eeMu.Lock()
	defer b.e2eeMu.Unlock()
	if b.waDevice == nil || b.waDevice.IdentityKey == nil {
		return nil, newError(CodeNotConnected, "encrypted chat device is not ready")
	}
	return b.waDevice.IdentityKey, nil
}

func (b *MetaBackend) RestoreHistory(ctx context.Context, pin string) error {
	_, svc, err := b.historyClient()
	if err != nil {
		return err
	}
	b.historyMu.Lock()
	defer b.historyMu.Unlock()
	if !b.historyEnrolled() {
		if pending := b.historyDevice(); pending != nil {
			b.log.Warn().Msg("Refusing to enroll again over an unconfirmed history enrollment")
			return newError(CodeInternal, "history enrollment unconfirmed")
		}
		userID, err := strconv.ParseInt(b.selfIDOrEmpty(), 10, 64)
		if err != nil || userID == 0 {
			return newError(CodeInternal, "own user ID is unknown")
		}
		identityFn := b.identityFn
		if identityFn == nil {
			identityFn = b.identityKey
		}
		identity, err := identityFn(ctx)
		if err != nil {
			return err
		}
		b.mu.Lock()
		backup := b.historyBackup
		b.mu.Unlock()
		if backup == nil {
			if backup, err = svc.GetBackup(ctx); err != nil {
				return historyError(err)
			}
		}
		secret := []byte(pin)
		device, err := svc.Enroll(ctx, backup, secret, userID, identity, func(pending *securehistory.Device) error {
			return b.setHistoryDevice(pending, false)
		})
		clear(secret)
		if err != nil {
			b.log.Warn().Err(err).Msg("Encrypted history restore failed")
			return historyError(err)
		}
		if err = b.setHistoryDevice(device, true); err != nil {
			return newError(CodeInternal, "failed to save history enrollment: %v", err)
		}
		b.log.Info().Msg("Enrolled encrypted history device")
	}
	b.applyHistory(ctx, svc, b.e2eeThreads())
	return nil
}

func (b *MetaBackend) e2eeThreads() []threadRec {
	var out []threadRec
	for _, th := range b.store.ListThreads(0) {
		if !th.E2EE {
			continue
		}
		if rec, ok := b.store.Thread(th.ID); ok {
			out = append(out, rec)
		}
	}
	return out
}

func (b *MetaBackend) applyHistory(ctx context.Context, svc historyService, threads []threadRec) {
	if len(threads) == 0 || !b.historyEnrolled() {
		return
	}
	jobs := make(chan threadRec)
	var wg sync.WaitGroup
	var mu sync.Mutex
	changed := make([]string, 0, len(threads))
	var failed, applied int
	beforeMS := time.Now().Add(historyFutureSlack).UnixMilli()
	for w := 0; w < historyWorkers; w++ {
		wg.Add(1)
		goSafe(b.log, "history_worker", func() {
			defer wg.Done()
			for rec := range jobs {
				n, err := b.fetchHistoryPage(ctx, svc, rec, beforeMS, historyPageSize)
				mu.Lock()
				if err != nil {
					failed++
				} else {
					applied += n
					changed = append(changed, rec.ID)
				}
				mu.Unlock()
			}
		})
	}
	for _, rec := range threads {
		select {
		case jobs <- rec:
		case <-ctx.Done():
		}
		if ctx.Err() != nil {
			break
		}
	}
	close(jobs)
	wg.Wait()
	if views := b.store.ThreadViews(changed); len(views) > 0 {
		b.out.Emit(EventThreads, ThreadsEvent{Threads: views})
	}
	b.log.Info().
		Int("threads", len(threads)).
		Int("threads_failed", failed).
		Int("messages_applied", applied).
		Msg("Applied encrypted history")
}

func (b *MetaBackend) fetchHistoryPage(ctx context.Context, svc historyService, rec threadRec, beforeMS int64, count int) (int, error) {
	device := b.historyDevice()
	if device == nil || device.ID == "" {
		return 0, errors.New("history device not enrolled")
	}
	self, _ := strconv.ParseInt(b.store.SelfID(), 10, 64)
	ctx, cancel := context.WithTimeout(ctx, historyThreadTimeout)
	defer cancel()
	page, err := svc.FetchPage(ctx, device, chatJIDFor(rec), self, beforeMS, count)
	if err != nil {
		b.log.Debug().Err(err).Str("thread_id", rec.ID).Msg("Failed to fetch encrypted history page")
		return 0, err
	}
	if page.device != nil {
		b.updateHistoryEpochs(device.ID, page.device)
	}
	b.store.SetHasMoreBefore(rec.ID, page.hasMore)
	return b.applyArchive(rec.ID, page.items), nil
}

func (b *MetaBackend) applyArchive(threadID string, items []archiveItem) int {
	applied := 0
	for _, item := range items {
		conv := b.convertFBMessage(threadID, item.evt)
		if conv.msg == nil {
			continue
		}
		if item.editText != nil {
			conv.msg.Text = *item.editText
			conv.msg.Edited = true
		}
		overrideDirectPaths(&conv, item.paths)
		b.storeConvertedMedia(conv)
		for actor, emoji := range item.reactions {
			b.store.SetReaction(conv.msg.ID, actor, emoji, item.reactTS[actor])
		}
		if _, existed := b.store.PutMessage(*conv.msg); !existed {
			b.applyHistoryToThread(*conv.msg)
			applied++
		}
	}
	return applied
}

func overrideDirectPaths(conv *fbConversion, paths map[string]string) {
	if len(paths) == 0 {
		return
	}
	for i, att := range conv.media {
		path := paths[att.ID]
		if path == "" || att.media.WATransport == nil {
			continue
		}
		var transport waMediaTransport.WAMediaTransport
		if proto.Unmarshal(att.media.WATransport, &transport) != nil || transport.Integral == nil {
			continue
		}
		transport.Integral.DirectPath = proto.String(path)
		if raw, err := proto.Marshal(&transport); err == nil {
			conv.media[i].media.WATransport = raw
		}
	}
}

func (b *MetaBackend) fetchOlderHistory(ctx context.Context, rec threadRec, beforeMS int64, count int) {
	_, svc, err := b.historyClient()
	if err != nil || !b.historyEnrolled() {
		return
	}
	if oldest, ok := b.store.OldestMessage(rec.ID); ok && (beforeMS == 0 || oldest.TimestampMS < beforeMS) {
		beforeMS = oldest.TimestampMS
	}
	if beforeMS == 0 {
		beforeMS = time.Now().Add(historyFutureSlack).UnixMilli()
	}
	if _, err := b.fetchHistoryPage(ctx, svc, rec, beforeMS, max(count, historyPageSize)); err == nil {
		if view, ok := b.store.ThreadView(rec.ID); ok {
			b.out.Emit(EventThreads, ThreadsEvent{Threads: []Thread{view}})
		}
	}
}
