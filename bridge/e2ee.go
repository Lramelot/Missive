package main

import (
	"context"
	"database/sql"
	"fmt"
	"math/rand/v2"
	"strconv"
	"time"

	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/proto/waConsumerApplication"
	"go.mau.fi/whatsmeow/proto/waMsgApplication"
	"go.mau.fi/whatsmeow/store/sqlstore"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
	waLog "go.mau.fi/whatsmeow/util/log"
	_ "modernc.org/sqlite"

	"go.mau.fi/mautrix-meta/pkg/messagix"
	"go.mau.fi/mautrix-meta/pkg/messagix/methods"
)

const (
	maxE2EEDeviceResets = 3
	maxE2EEBackoff      = 10 * time.Minute
	e2eeWaitTimeout     = 15 * time.Second
	waDialectSQLite3    = "sqlite3"
)

func (b *MetaBackend) containerLocked(ctx context.Context) (*sqlstore.Container, error) {
	if b.waContainer != nil {
		return b.waContainer, nil
	}
	if err := ensurePrivateFile(b.path(waDBFile)); err != nil {
		b.log.Warn().Err(err).Msg("Failed to create the device store with private permissions")
	}
	dsn := b.path(waDBFile) + "?_pragma=foreign_keys(1)&_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)&_pragma=synchronous(NORMAL)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("failed to open whatsmeow database: %w", err)
	}
	db.SetMaxOpenConns(1)
	container := sqlstore.NewWithDB(db, waDialectSQLite3, waLog.Zerolog(b.log.With().Str("component", "whatsmeow-store").Logger()))
	if err = container.Upgrade(ctx); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("failed to upgrade whatsmeow database: %w", err)
	}
	b.waContainer = container
	return container, nil
}

func (b *MetaBackend) deleteWADevice(ctx context.Context, deviceID uint16) {
	b.e2eeMu.Lock()
	defer b.e2eeMu.Unlock()
	container, err := b.containerLocked(ctx)
	if err != nil {
		b.log.Err(err).Msg("Failed to open device store")
		return
	}
	devices, err := container.GetAllDevices(ctx)
	if err != nil {
		b.log.Err(err).Msg("Failed to list devices")
		return
	}
	for _, dev := range devices {
		if dev.ID != nil && dev.ID.Device == deviceID && dev.ID.Server == types.MessengerServer {
			if err = dev.Delete(ctx); err != nil {
				b.log.Err(err).Msg("Failed to delete E2EE device")
			}
		}
	}
	b.waDevice = nil
}

func (b *MetaBackend) stopE2EE() {
	b.e2eeMu.Lock()
	cli := b.e2ee
	b.e2ee = nil
	b.waDevice = nil
	b.e2eeMu.Unlock()
	b.e2eeRetries.Store(0)
	if cli != nil {
		cli.Disconnect()
	}
}

func (b *MetaBackend) startE2EE(ctx context.Context, client *messagix.Client) {
	err := b.connectE2EE(ctx, client)
	if err == nil || ctx.Err() != nil {
		return
	}
	b.log.Err(err).Msg("Failed to connect E2EE client")
	b.scheduleE2EERetry(ctx, client)
}

func e2eeBackoff(attempt int32, rnd float64) time.Duration {
	base := min(5*time.Second<<min(attempt, 8), maxE2EEBackoff)
	return min(jittered(base, rnd), maxE2EEBackoff)
}

func (b *MetaBackend) scheduleE2EERetry(ctx context.Context, client *messagix.Client) {
	attempt := b.e2eeRetries.Add(1)
	wait := e2eeBackoff(attempt, rand.Float64())
	b.log.Info().Dur("retry_in", wait).Int32("attempt", attempt).Msg("Scheduling E2EE reconnect")
	goSafe(b.log, "e2ee_retry", func() {
		if !b.sleep(ctx, wait) || !b.isCurrent(client) {
			return
		}
		b.startE2EE(ctx, client)
	})
}

func (b *MetaBackend) connectE2EE(ctx context.Context, client *messagix.Client) error {
	self := b.selfIDOrEmpty()
	fbid, err := strconv.ParseInt(self, 10, 64)
	if err != nil || fbid == 0 {
		return fmt.Errorf("unknown own user ID")
	}
	b.mu.Lock()
	var deviceID uint16
	if b.sess != nil {
		deviceID = b.sess.WADeviceID
	}
	b.mu.Unlock()

	b.e2eeMu.Lock()
	defer b.e2eeMu.Unlock()
	if b.e2ee != nil {
		return nil
	}
	container, err := b.containerLocked(ctx)
	if err != nil {
		return err
	}
	dev := b.waDevice
	if dev == nil && deviceID != 0 {
		dev, err = container.GetDevice(ctx, types.JID{User: self, Device: deviceID, Server: types.MessengerServer})
		if err != nil {
			return fmt.Errorf("failed to load E2EE device: %w", err)
		}
		if dev == nil {
			b.log.Warn().Uint16("device_id", deviceID).Msg("Stored E2EE device not found, registering a new one")
		}
	}
	isNew := dev == nil
	if isNew {
		dev = container.NewDevice()
	}
	client.SetDevice(dev)
	if isNew {
		b.log.Info().Msg("Registering new E2EE device")
		if err = client.RegisterE2EE(ctx, fbid); err != nil {
			return fmt.Errorf("failed to register E2EE device: %w", err)
		}
		if err = dev.Save(ctx); err != nil {
			return fmt.Errorf("failed to save E2EE device: %w", err)
		}
		b.mu.Lock()
		if b.sess != nil {
			b.sess.WADeviceID = dev.ID.Device
			b.saveSessionLocked()
		}
		b.mu.Unlock()
	}
	b.waDevice = dev
	cli, err := client.PrepareE2EEClient()
	if err != nil {
		return fmt.Errorf("failed to prepare E2EE client: %w", err)
	}
	cli.AddEventHandler(func(evt any) {
		b.handleWAEvent(ctx, client, cli, evt)
	})
	b.beginOfflineSync(ctx)
	if err = cli.Connect(); err != nil {
		cli.Disconnect()
		return fmt.Errorf("failed to connect E2EE socket: %w", err)
	}
	b.e2ee = cli
	return nil
}

func (b *MetaBackend) currentE2EE() *whatsmeow.Client {
	b.e2eeMu.Lock()
	defer b.e2eeMu.Unlock()
	return b.e2ee
}

func (b *MetaBackend) resetE2EEDevice(ctx context.Context, cli *whatsmeow.Client, client *messagix.Client) {
	if !b.allowE2EEReset() {
		b.giveUpE2EE(cli, "encrypted chat device registration keeps failing; encrypted chats are unavailable until the next login")
		return
	}
	b.e2eeMu.Lock()
	if b.e2ee != cli {
		b.e2eeMu.Unlock()
		return
	}
	b.e2ee = nil
	dev := b.waDevice
	b.waDevice = nil
	b.e2eeMu.Unlock()
	cli.Disconnect()
	if dev != nil {
		if err := dev.Delete(ctx); err != nil {
			b.log.Warn().Err(err).Msg("Failed to delete E2EE device")
		}
	}
	b.mu.Lock()
	if b.sess != nil {
		b.sess.WADeviceID = 0
		b.saveSessionLocked()
	}
	b.mu.Unlock()
	b.scheduleE2EERetry(ctx, client)
}

func (b *MetaBackend) handleWAEvent(ctx context.Context, client *messagix.Client, cli *whatsmeow.Client, rawEvt any) {
	defer b.recoverHandler("wa_event")
	switch evt := rawEvt.(type) {
	case *events.FBMessage:
		b.handleFBMessage(evt)
	case *events.UndecryptableMessage:
		b.log.Warn().
			Str("message_id", evt.Info.ID).
			Stringer("chat", evt.Info.Chat).
			Bool("unavailable", evt.IsUnavailable).
			Msg("Received undecryptable E2EE message")
	case *events.ChatPresence:
		if evt.Sender.User == b.store.SelfID() {
			return
		}
		b.out.Emit(EventTyping, TypingEvent{
			ThreadID: evt.Chat.User,
			UserID:   evt.Sender.User,
			Typing:   evt.State == types.ChatPresenceComposing,
		})
	case *events.Receipt:
		b.handleWAReceipt(evt)
	case *events.Connected:
		b.log.Info().Msg("Connected to E2EE socket")
		b.e2eeRetries.Store(0)
		b.e2eeLogoutRetried.Store(false)
		goSafe(b.log, "e2ee_presence", func() {
			b.sendActivePresence(ctx, cli)
			b.subscribePresence(ctx, cli)
		})
	case *events.OfflineSyncCompleted:
		b.waOfflineSyncing.Store(false)
	case *events.Presence:
		if evt.From.User == "" || evt.From.User == b.store.SelfID() {
			return
		}
		b.countPresence(&b.presenceE2EE, "e2ee")
		presence := PresenceEvent{UserID: evt.From.User, Online: !evt.Unavailable}
		if !evt.LastSeen.IsZero() {
			presence.LastSeenMS = evt.LastSeen.UnixMilli()
		} else if presence.Online {
			presence.LastSeenMS = time.Now().UnixMilli()
		}
		b.out.Emit(EventPresence, presence)
		if b.store.SetPresence(presence.UserID, presence.Online, presence.LastSeenMS, 0) {
			b.emitThreadsWithUser(presence.UserID)
		}
	case *events.Disconnected:
		b.log.Info().Msg("Disconnected from E2EE socket")
	case *events.CATRefreshError:
		b.log.Err(evt.Error).Msg("E2EE crypto auth token refresh failed")
		goSafe(b.log, "full_reconnect", func() { b.fullReconnect("CAT refresh failed") })
	case events.PermanentDisconnect:
		b.log.Error().Str("reason", evt.PermanentDisconnectDescription()).Msg("E2EE socket permanently disconnected")
		switch b.classifyE2EEDisconnect(evt) {
		case e2eeActionReset:
			goSafe(b.log, "e2ee_reset", func() { b.resetE2EEDevice(ctx, cli, client) })
		case e2eeActionFullReconnect:
			goSafe(b.log, "full_reconnect", func() { b.fullReconnect("encrypted socket rejected the session") })
		case e2eeActionGiveUp:
			goSafe(b.log, "e2ee_give_up", func() {
				b.giveUpE2EE(cli, "encrypted chats were logged out again; they stay unavailable until the next login")
			})
		default:
			goSafe(b.log, "e2ee_restart", func() { b.restartE2EE(ctx, cli, client) })
		}
	}
}

type e2eeAction int

const (
	e2eeActionRetry e2eeAction = iota
	e2eeActionReset
	e2eeActionFullReconnect
	e2eeActionGiveUp
)

func (b *MetaBackend) classifyE2EEDisconnect(evt events.PermanentDisconnect) e2eeAction {
	switch e := evt.(type) {
	case *events.LoggedOut:
		if b.e2eeLogoutRetried.Swap(true) {
			return e2eeActionGiveUp
		}
		return e2eeActionReset
	case *events.ConnectFailure:
		switch e.Reason {
		case events.ConnectFailureNotFound, events.ConnectFailureClientUnknown:
			return e2eeActionReset
		case events.ConnectFailureGeneric:
			return e2eeActionFullReconnect
		}
	}
	return e2eeActionRetry
}

func (b *MetaBackend) allowE2EEReset() bool {
	return b.e2eeResets.Add(1) <= maxE2EEDeviceResets
}

func (b *MetaBackend) detachE2EE(cli *whatsmeow.Client) bool {
	b.e2eeMu.Lock()
	current := b.e2ee == cli
	if current {
		b.e2ee = nil
	}
	b.e2eeMu.Unlock()
	cli.Disconnect()
	return current
}

func (b *MetaBackend) restartE2EE(ctx context.Context, cli *whatsmeow.Client, client *messagix.Client) {
	if b.detachE2EE(cli) {
		b.scheduleE2EERetry(ctx, client)
	}
}

func (b *MetaBackend) giveUpE2EE(cli *whatsmeow.Client, detail string) {
	b.detachE2EE(cli)
	b.log.Error().Msg("Encrypted chat connection given up")
	b.out.Emit(EventConnection, ConnectionEvent{State: StateError, Detail: detail})
}

const (
	presencePeers       = 20
	offlineSyncDeadline = time.Minute
)

func (b *MetaBackend) sendActivePresence(ctx context.Context, cli *whatsmeow.Client) {
	presence := types.PresenceUnavailable
	if b.active.Load() {
		presence = types.PresenceAvailable
	}
	if err := cli.SendPresence(ctx, presence); err != nil {
		b.log.Debug().Err(err).Msg("Failed to set E2EE presence")
	}
}

func (b *MetaBackend) SetActive(ctx context.Context, active bool) error {
	if b.active.Swap(active) == active {
		return nil
	}
	cli := b.currentE2EE()
	client := b.currentClient()
	life := b.lifeCtx()
	goSafe(b.log, "set_active", func() {
		ctx, cancel := context.WithTimeout(life, 30*time.Second)
		defer cancel()
		b.reportAppState(ctx, client, active)
		if cli != nil && cli.IsConnected() {
			b.sendActivePresence(ctx, cli)
			if active {
				b.subscribePresence(ctx, cli)
			}
		}
	})
	return nil
}

func (b *MetaBackend) beginOfflineSync(ctx context.Context) {
	b.waOfflineSyncing.Store(true)
	goSafe(b.log, "offline_sync_deadline", func() {
		if sleepCtx(ctx, offlineSyncDeadline) {
			b.waOfflineSyncing.Store(false)
		}
	})
}

func (b *MetaBackend) presencePeers() []string {
	var peers []string
	for _, th := range b.store.ListThreads(0) {
		if len(peers) == presencePeers {
			break
		}
		if th.E2EE && !th.IsGroup && th.PeerID != "" {
			peers = append(peers, th.PeerID)
		}
	}
	return peers
}

func (b *MetaBackend) subscribePresence(ctx context.Context, cli *whatsmeow.Client) {
	subscribed, failed := 0, 0
	for _, peer := range b.presencePeers() {
		if ctx.Err() != nil {
			return
		}
		if err := cli.SubscribePresence(ctx, types.NewJID(peer, types.MessengerServer)); err != nil {
			b.log.Debug().Err(err).Msg("Failed to subscribe to presence")
			failed++
			continue
		}
		subscribed++
	}
	b.log.Info().Int("subscribed", subscribed).Int("failed", failed).Msg("Subscribed to encrypted chat presence")
}

func (b *MetaBackend) ensureE2EEThread(chat types.JID) string {
	threadID := chat.User
	if b.store.HasThread(threadID) {
		return threadID
	}
	isGroup := chat.Server == types.GroupServer
	b.store.UpdateThread(threadID, func(rec *threadRec) {
		rec.E2EE = true
		rec.IsGroup = isGroup
		if isGroup {
			rec.ThreadType = 16
		} else {
			rec.ThreadType = 15
			rec.PeerID = threadID
		}
	})
	return threadID
}

func chatJIDFor(rec threadRec) types.JID {
	server := types.MessengerServer
	if rec.IsGroup {
		server = types.GroupServer
	}
	return types.JID{User: rec.ID, Server: server}
}

func (b *MetaBackend) waitE2EE(ctx context.Context) (*whatsmeow.Client, error) {
	deadline := time.Now().Add(e2eeWaitTimeout)
	for {
		if cli := b.currentE2EE(); cli != nil && cli.IsConnected() {
			return cli, nil
		}
		if time.Now().After(deadline) {
			return nil, newError(CodeNotConnected, "encrypted chat connection is not available")
		}
		if !sleepCtx(ctx, 250*time.Millisecond) {
			return nil, newError(CodeNotConnected, "cancelled")
		}
	}
}

func (b *MetaBackend) sendFBMessage(ctx context.Context, rec threadRec, content *waConsumerApplication.ConsumerApplication, meta *waMsgApplication.MessageApplication_Metadata, messageID string) (time.Time, error) {
	if b.waSendFn != nil {
		if err := b.waSendFn(ctx, rec, content); err != nil {
			return time.Time{}, err
		}
		return time.Now(), nil
	}
	if !b.connectedState() {
		return time.Time{}, newError(CodeNotConnected, "not connected")
	}
	cli, err := b.waitE2EE(ctx)
	if err != nil {
		return time.Time{}, err
	}
	resp, err := cli.SendFBMessage(ctx, chatJIDFor(rec), content, meta, whatsmeow.SendRequestExtra{ID: messageID})
	if err != nil {
		return time.Time{}, newError(CodeInternal, "failed to send encrypted message: %v", err)
	}
	return resp.Timestamp, nil
}

func (b *MetaBackend) sendE2EE(ctx context.Context, rec threadRec, text string, replyTo *Message, clientID string, mentions []Mention) (string, error) {
	content := &waConsumerApplication.ConsumerApplication{
		Payload: &waConsumerApplication.ConsumerApplication_Payload{
			Payload: &waConsumerApplication.ConsumerApplication_Payload_Content{
				Content: &waConsumerApplication.ConsumerApplication_Content{
					Content: &waConsumerApplication.ConsumerApplication_Content_MessageText{
						MessageText: waOutgoingText(text, mentions),
					},
				},
			},
		},
	}
	meta := waQuotedMeta(replyTo)
	if meta.QuotedMessage == nil {
		replyTo = nil
	}
	messageID := strconv.FormatInt(methods.GenerateEpochID(), 10)
	sentAt, err := b.sendFBMessage(ctx, rec, content, meta, messageID)
	if err != nil {
		return "", err
	}
	b.uploadSentToHistory(chatJIDFor(rec), messageID, "", content, meta)
	ts := sentAt.UnixMilli()
	if sentAt.IsZero() {
		ts = time.Now().UnixMilli()
	}
	user := b.currentUser()
	msg := Message{
		ID:          messageID,
		ThreadID:    rec.ID,
		SenderID:    user.ID,
		SenderName:  user.Name,
		Text:        text,
		TimestampMS: ts,
		IsOutgoing:  true,
		Attachments: []Attachment{},
		ClientID:    clientID,
		Mentions:    mentions,
	}
	if replyTo != nil {
		msg.ReplyToID = replyTo.ID
	}
	b.recordNewMessage(msg, true)
	b.markReadAfterSend(ctx, rec.ID)
	return messageID, nil
}
