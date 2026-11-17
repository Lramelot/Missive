package main

import (
	"cmp"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"slices"
	"strconv"
	"time"

	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/proto/armadilloutil"
	"go.mau.fi/whatsmeow/proto/waCommon"
	"go.mau.fi/whatsmeow/proto/waConsumerApplication"
	"go.mau.fi/whatsmeow/proto/waMediaTransport"
	"go.mau.fi/whatsmeow/proto/waMsgApplication"
	"go.mau.fi/whatsmeow/store/sqlstore"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
	waLog "go.mau.fi/whatsmeow/util/log"
	"google.golang.org/protobuf/proto"
	_ "modernc.org/sqlite"

	"go.mau.fi/mautrix-meta/pkg/messagix"
	"go.mau.fi/mautrix-meta/pkg/messagix/methods"
)

const (
	maxE2EEDeviceResets = 3
	maxE2EERetries      = 5
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

func (b *MetaBackend) scheduleE2EERetry(ctx context.Context, client *messagix.Client) {
	attempt := b.e2eeRetries.Add(1)
	if attempt > maxE2EERetries {
		b.log.Error().Msg("Giving up on E2EE connection, encrypted chats are unavailable")
		if attempt == maxE2EERetries+1 {
			b.out.Emit(EventConnection, ConnectionEvent{State: StateError, Detail: "encrypted chats are unavailable: connection keeps failing"})
		}
		return
	}
	wait := time.Duration(1<<min(attempt, 6)) * 5 * time.Second
	b.log.Info().Dur("retry_in", wait).Int32("attempt", attempt).Msg("Scheduling E2EE reconnect")
	go func() {
		if !sleepCtx(ctx, wait) || !b.isCurrent(client) {
			return
		}
		b.startE2EE(ctx, client)
	}()
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
		go func() {
			b.sendActivePresence(ctx, cli)
			b.subscribePresence(ctx, cli)
		}()
	case *events.OfflineSyncCompleted:
		b.waOfflineSyncing.Store(false)
	case *events.Presence:
		if evt.From.User == "" || evt.From.User == b.store.SelfID() {
			return
		}
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
		go b.fullReconnect("CAT refresh failed")
	case events.PermanentDisconnect:
		b.log.Error().Str("reason", evt.PermanentDisconnectDescription()).Msg("E2EE socket permanently disconnected")
		switch b.classifyE2EEDisconnect(evt) {
		case e2eeActionReset:
			go b.resetE2EEDevice(ctx, cli, client)
		case e2eeActionFullReconnect:
			go b.fullReconnect("encrypted socket rejected the session")
		case e2eeActionGiveUp:
			go b.giveUpE2EE(cli, "encrypted chats were logged out again; they stay unavailable until the next login")
		default:
			go b.restartE2EE(ctx, cli, client)
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
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		b.reportAppState(ctx, client, active)
		if cli != nil && cli.IsConnected() {
			b.sendActivePresence(ctx, cli)
			if active {
				b.subscribePresence(ctx, cli)
			}
		}
	}()
	return nil
}

func (b *MetaBackend) beginOfflineSync(ctx context.Context) {
	b.waOfflineSyncing.Store(true)
	go func() {
		if sleepCtx(ctx, offlineSyncDeadline) {
			b.waOfflineSyncing.Store(false)
		}
	}()
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
	subscribed := 0
	for _, peer := range b.presencePeers() {
		if ctx.Err() != nil {
			return
		}
		if err := cli.SubscribePresence(ctx, types.NewJID(peer, types.MessengerServer)); err != nil {
			b.log.Debug().Err(err).Msg("Failed to subscribe to presence")
			continue
		}
		subscribed++
	}
	b.log.Info().Int("subscribed", subscribed).Msg("Subscribed to encrypted chat presence")
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

type captioned interface {
	GetCaption() *waCommon.MessageText
}

type waAttachment struct {
	Attachment
	media mediaRec
}

func waMediaAttachment(kind string, transport *waMediaTransport.WAMediaTransport, mediaType whatsmeow.MediaType) waAttachment {
	anc := transport.GetAncillary()
	att := waAttachment{Attachment: Attachment{
		ID:       cmp.Or(anc.GetObjectID(), "media_0"),
		Kind:     kind,
		MimeType: anc.GetMimetype(),
		Size:     int64(anc.GetFileLength()),
	}}
	if transport.GetIntegral() != nil {
		if raw, err := proto.Marshal(transport); err == nil {
			att.media = mediaRec{Kind: kind, MimeType: anc.GetMimetype(), WAType: string(mediaType), WATransport: raw}
		}
	}
	return att
}

func waContentMedia(content *waConsumerApplication.ConsumerApplication_Content) (string, []waAttachment) {
	captionOf := func(c captioned) string { return c.GetCaption().GetText() }
	switch c := content.GetContent().(type) {
	case *waConsumerApplication.ConsumerApplication_Content_ExtendedTextMessage:
		return extendedTextToMessage(c.ExtendedTextMessage)
	case *waConsumerApplication.ConsumerApplication_Content_ImageMessage:
		att := waAttachment{Attachment: Attachment{ID: "media_0", Kind: "image"}}
		if t, err := c.ImageMessage.Decode(); err == nil {
			att = waMediaAttachment("image", t.GetIntegral().GetTransport(), whatsmeow.MediaImage)
			att.Width, att.Height = int(t.GetAncillary().GetWidth()), int(t.GetAncillary().GetHeight())
		}
		return captionOf(c.ImageMessage), []waAttachment{att}
	case *waConsumerApplication.ConsumerApplication_Content_VideoMessage:
		att := waAttachment{Attachment: Attachment{ID: "media_0", Kind: "video"}}
		if t, err := c.VideoMessage.Decode(); err == nil {
			att = waMediaAttachment("video", t.GetIntegral().GetTransport(), whatsmeow.MediaVideo)
			anc := t.GetAncillary()
			att.Width, att.Height = int(anc.GetWidth()), int(anc.GetHeight())
			att.DurationMS = int64(anc.GetSeconds()) * 1000
		}
		return captionOf(c.VideoMessage), []waAttachment{att}
	case *waConsumerApplication.ConsumerApplication_Content_AudioMessage:
		att := waAttachment{Attachment: Attachment{ID: "media_0", Kind: "audio"}}
		if t, err := c.AudioMessage.Decode(); err == nil {
			att = waMediaAttachment("audio", t.GetIntegral().GetTransport(), whatsmeow.MediaAudio)
			att.DurationMS = int64(t.GetAncillary().GetSeconds()) * 1000
			att.Waveform = waWaveform(t.GetAncillary())
		}
		att.Voice = true
		return "", []waAttachment{att}
	case *waConsumerApplication.ConsumerApplication_Content_DocumentMessage:
		att := waAttachment{Attachment: Attachment{ID: "media_0", Kind: "file"}}
		if t, err := c.DocumentMessage.Decode(); err == nil {
			att = waMediaAttachment("file", t.GetIntegral().GetTransport(), whatsmeow.MediaDocument)
		}
		att.FileName = c.DocumentMessage.GetFileName()
		att.media.FileName = att.FileName
		return "", []waAttachment{att}
	case *waConsumerApplication.ConsumerApplication_Content_StickerMessage:
		att := waAttachment{Attachment: Attachment{ID: "media_0", Kind: "sticker"}}
		if t, err := c.StickerMessage.Decode(); err == nil {
			att = waMediaAttachment("sticker", t.GetIntegral().GetTransport(), whatsmeow.MediaImage)
			anc := t.GetAncillary()
			att.Width, att.Height = int(anc.GetWidth()), int(anc.GetHeight())
			if id := anc.GetReceiverFetchID(); id != "" {
				att.media.Kind = "sticker"
				att.media.MimeType = att.MimeType
				att.media.ReceiverFetchID = id
			}
		}
		return "", []waAttachment{att}
	}
	switch c := content.GetContent().(type) {
	case *waConsumerApplication.ConsumerApplication_Content_LocationMessage:
		return "", []waAttachment{waLocationAttachment(c.LocationMessage.GetLocation(), c.LocationMessage.GetAddress(), false)}
	case *waConsumerApplication.ConsumerApplication_Content_LiveLocationMessage:
		return captionOf(c.LiveLocationMessage), []waAttachment{waLocationAttachment(c.LiveLocationMessage.GetLocation(), "", true)}
	case *waConsumerApplication.ConsumerApplication_Content_ContactMessage:
		return "", []waAttachment{{Attachment: Attachment{ID: "contact_0", Kind: "contact", Title: "Contact partagé"}}}
	}
	text, atts := waContentToMessage(content)
	out := make([]waAttachment, len(atts))
	for i, att := range atts {
		att.ID = "media_" + strconv.Itoa(i)
		out[i] = waAttachment{Attachment: att}
	}
	return text, out
}

func consumerMessageText(content *waConsumerApplication.ConsumerApplication_Content) *waCommon.MessageText {
	switch c := content.GetContent().(type) {
	case *waConsumerApplication.ConsumerApplication_Content_MessageText:
		return c.MessageText
	case *waConsumerApplication.ConsumerApplication_Content_ExtendedTextMessage:
		return c.ExtendedTextMessage.GetText()
	case *waConsumerApplication.ConsumerApplication_Content_ImageMessage:
		return c.ImageMessage.GetCaption()
	case *waConsumerApplication.ConsumerApplication_Content_VideoMessage:
		return c.VideoMessage.GetCaption()
	}
	return nil
}

func waContentToMessage(content *waConsumerApplication.ConsumerApplication_Content) (string, []Attachment) {
	switch c := content.GetContent().(type) {
	case *waConsumerApplication.ConsumerApplication_Content_MessageText:
		return c.MessageText.GetText(), nil
	case *waConsumerApplication.ConsumerApplication_Content_ExtendedTextMessage:
		return c.ExtendedTextMessage.GetText().GetText(), nil
	case *waConsumerApplication.ConsumerApplication_Content_ViewOnceMessage:
		return "", []Attachment{{Kind: "other"}}
	case *waConsumerApplication.ConsumerApplication_Content_LocationMessage:
		loc := c.LocationMessage
		text := loc.GetLocation().GetName()
		if addr := loc.GetAddress(); addr != "" {
			if text != "" {
				text += "\n"
			}
			text += addr
		}
		if text == "" {
			text = fmt.Sprintf("%f,%f", loc.GetLocation().GetDegreesLatitude(), loc.GetLocation().GetDegreesLongitude())
		}
		return text, nil
	default:
		return "", []Attachment{{Kind: "other"}}
	}
}

type fbConversion struct {
	msg          *Message
	media        []waAttachment
	editID       string
	editText     string
	editMentions []Mention
	revokeID     string
	reactTarget  string
	reactEmoji   string
	reactActor   string
	reactTS      int64
	system       bool
	threadID     string
	chat         types.JID
	actor        string
	targetKey    *waCommon.MessageKey
}

func (b *MetaBackend) convertFBMessage(threadID string, evt *events.FBMessage) fbConversion {
	conv := fbConversion{threadID: threadID, chat: evt.Info.Chat, actor: evt.Info.Sender.User}
	if evt.Info.IsFromMe {
		conv.actor = b.store.SelfID()
	}
	var text string
	var mentions []Mention
	var attachments []Attachment
	if ca := evt.GetConsumerApplication(); ca != nil {
		switch payload := ca.GetPayload().GetPayload().(type) {
		case *waConsumerApplication.ConsumerApplication_Payload_Content:
			switch c := payload.Content.GetContent().(type) {
			case *waConsumerApplication.ConsumerApplication_Content_EditMessage:
				conv.editID = c.EditMessage.GetKey().GetID()
				conv.targetKey = c.EditMessage.GetKey()
				conv.editText, conv.editMentions = b.waTextWithMentions(threadID, c.EditMessage.GetMessage())
				return conv
			case *waConsumerApplication.ConsumerApplication_Content_ReactionMessage:
				conv.reactTarget = c.ReactionMessage.GetKey().GetID()
				conv.reactEmoji = c.ReactionMessage.GetText()
				conv.reactActor = evt.Info.Sender.User
				conv.reactTS = evt.Info.Timestamp.UnixMilli()
				if evt.Info.IsFromMe {
					conv.reactActor = b.store.SelfID()
				}
				return conv
			}
			text, conv.media = waContentMedia(payload.Content)
			for _, att := range conv.media {
				attachments = append(attachments, att.Attachment)
			}
			if mt := consumerMessageText(payload.Content); mt != nil && mt.GetText() == text {
				text, mentions = b.waTextWithMentions(threadID, mt)
			}
		case *waConsumerApplication.ConsumerApplication_Payload_ApplicationData:
			if revoke := payload.ApplicationData.GetRevoke(); revoke != nil {
				conv.revokeID = revoke.GetKey().GetID()
				conv.targetKey = revoke.GetKey()
			}
			return conv
		default:
			return conv
		}
	} else if arm := evt.GetArmadillo(); arm != nil && arm.GetPayload().GetContent() != nil {
		text, conv.media = armadilloToMessage(arm.GetPayload().GetContent())
		if systemText := armadilloSystemText(arm.GetPayload().GetContent()); systemText != "" {
			text, conv.media, conv.system = systemText, nil, true
		}
		for _, att := range conv.media {
			attachments = append(attachments, att.Attachment)
		}
	} else {
		return conv
	}
	if attachments == nil {
		attachments = []Attachment{}
	}
	sender := evt.Info.Sender.User
	senderName := b.store.ContactName(sender)
	if senderName == "" {
		senderName = evt.Info.PushName
	}
	isOutgoing := evt.Info.IsFromMe || sender == b.store.SelfID()
	if isOutgoing && senderName == "" {
		senderName = b.currentUser().Name
	}
	conv.msg = &Message{
		ID:          evt.Info.ID,
		ThreadID:    threadID,
		SenderID:    sender,
		SenderName:  senderName,
		Text:        text,
		TimestampMS: evt.Info.Timestamp.UnixMilli(),
		IsOutgoing:  isOutgoing,
		Attachments: attachments,
		ReplyToID:   evt.FBApplication.GetMetadata().GetQuotedMessage().GetStanzaID(),
		Mentions:    mentions,
		System:      conv.system,
	}
	if quoted := evt.FBApplication.GetMetadata().GetQuotedMessage(); quoted != nil && quoted.GetStanzaID() != "" {
		conv.msg.ReplyTo = b.quotedPreview(quoted)
	}
	return conv
}

func (b *MetaBackend) quotedPreview(quoted *waMsgApplication.MessageApplication_Metadata_QuotedMessage) *ReplyPreview {
	preview := &ReplyPreview{ID: quoted.GetStanzaID()}
	if participant, err := types.ParseJID(quoted.GetParticipant()); err == nil && participant.User != "" {
		preview.SenderName = b.store.ContactName(participant.User)
		if participant.User == b.store.SelfID() && preview.SenderName == "" {
			preview.SenderName = b.currentUser().Name
		}
	}
	if sp := quoted.GetPayload().GetSubProtocol().GetConsumerMessage(); sp != nil {
		if consumer, err := armadilloutil.Unmarshal(&waConsumerApplication.ConsumerApplication{}, sp, sp.GetVersion()); err == nil {
			text, atts := waContentMedia(consumer.GetPayload().GetContent())
			preview.Text = text
			if len(atts) > 0 {
				preview.Kind = atts[0].Kind
			}
		}
	}
	return preview
}

func (b *MetaBackend) applyEditOrRevoke(conv fbConversion) bool {
	switch {
	case conv.reactTarget != "":
		prev := b.store.ReactionOf(conv.reactTarget, conv.reactActor)
		if b.store.SetReaction(conv.reactTarget, conv.reactActor, conv.reactEmoji, conv.reactTS) {
			if msg, ok := b.store.FindMessage(conv.reactTarget); ok {
				b.emitMessage(EventMessageUpdate, msg)
			}
			if b.liveE2EEReaction(conv.reactTS) {
				if evt, ok := b.store.reactionAddedEvent(prev, conv.reactTarget, conv.reactActor, conv.reactEmoji); ok {
					b.out.Emit(EventReactionAdded, evt)
				}
			}
		}
		return true
	case conv.editID != "":
		if !b.authorizedWATarget(conv, false) {
			b.log.Warn().Msg("Ignoring encrypted edit of a message the sender does not own")
			return true
		}
		text, mentions := conv.editText, conv.editMentions
		if updated, ok := b.store.UpdateMessage(conv.editID, func(m *Message) bool {
			if m.Text == text && m.Edited && slices.Equal(m.Mentions, mentions) {
				return false
			}
			m.Mentions = mentions
			m.Text = text
			m.Edited = true
			return true
		}); ok {
			b.emitMessage(EventMessageUpdate, updated)
		}
		return true
	case conv.revokeID != "":
		if !b.authorizedWATarget(conv, true) {
			b.log.Warn().Msg("Ignoring encrypted revoke of a message the sender does not own")
			return true
		}
		if updated, ok := b.store.UpdateMessage(conv.revokeID, func(m *Message) bool {
			if m.Deleted {
				return false
			}
			m.Deleted = true
			m.Text = ""
			m.Attachments = []Attachment{}
			return true
		}); ok {
			b.emitMessage(EventMessageUpdate, updated)
		}
		return true
	}
	return false
}

func (b *MetaBackend) storeConvertedMedia(conv fbConversion) {
	for _, att := range conv.media {
		if att.media.WATransport != nil || att.media.ReceiverFetchID != "" {
			b.store.PutMedia(conv.msg.ID, att.ID, att.media)
		}
	}
}

func (b *MetaBackend) handleFBMessage(evt *events.FBMessage) {
	threadID := b.ensureE2EEThread(evt.Info.Chat)
	conv := b.convertFBMessage(threadID, evt)
	if b.applyEditOrRevoke(conv) || conv.msg == nil {
		return
	}
	b.storeConvertedMedia(conv)
	b.recordNewMessage(*conv.msg, isLiveTimestamp(conv.msg.TimestampMS) && !b.waOfflineSyncing.Load())
	b.ensureContact(conv.msg.SenderID)
	if evt.Info.Chat.Server != types.GroupServer {
		b.ensureContact(evt.Info.Chat.User)
	}
}

func (b *MetaBackend) downloadE2EEMedia(ctx context.Context, rec mediaRec, maxBytes int64, f *os.File) (string, error) {
	if rec.ReceiverFetchID != "" {
		client := b.currentClient()
		if client == nil {
			return "", newError(CodeNotConnected, "not connected")
		}
		fetched, err := client.FetchReceiverFetchMedia(ctx, rec.ReceiverFetchID)
		if err != nil {
			return "", newError(CodeUnsupported, "sticker is no longer available: %v", err)
		}
		mimeType, err := httpDownloadToFile(ctx, fetched.URL, maxBytes, f)
		return cmp.Or(fetched.MimeType, mimeType), err
	}
	var transport waMediaTransport.WAMediaTransport
	if err := proto.Unmarshal(rec.WATransport, &transport); err != nil {
		return "", newError(CodeInternal, "corrupt media record: %v", err)
	}
	if length := transport.GetAncillary().GetFileLength(); length > uint64(maxBytes) {
		return "", newError(CodeUnsupported, "media larger than %d bytes", maxBytes)
	}
	cli, err := b.waitE2EE(ctx)
	if err != nil {
		return "", err
	}
	if err = cli.DownloadFBToFile(ctx, transport.GetIntegral(), whatsmeow.MediaType(rec.WAType), f); err != nil {
		err = withoutURL(err)
		if errors.Is(err, whatsmeow.ErrMediaDownloadFailedWith404) || errors.Is(err, whatsmeow.ErrMediaDownloadFailedWith410) {
			return "", fmt.Errorf("%w: %v", errMediaExpired, err)
		}
		return "", err
	}
	return cmp.Or(rec.MimeType, transport.GetAncillary().GetMimetype()), nil
}

func (b *MetaBackend) handleWAReceipt(evt *events.Receipt) {
	if evt.Type == types.ReceiptTypeDelivered {
		b.handleWADelivery(evt)
		return
	}
	switch evt.Type {
	case types.ReceiptTypeRead, types.ReceiptTypeReadSelf:
	default:
		return
	}
	threadID := evt.Chat.User
	ts := evt.Timestamp.UnixMilli()
	self := b.store.SelfID()
	if evt.Type == types.ReceiptTypeReadSelf || evt.Sender.User == self {
		if b.store.HasThread(threadID) {
			b.store.UpdateThread(threadID, func(rec *threadRec) {
				rec.LastReadMS = max(rec.LastReadMS, ts)
				rec.MetaReadMS = max(rec.MetaReadMS, ts)
				if rec.LastReadMS >= rec.LastActivityMS {
					rec.UnreadCount = 0
				}
			})
			if view, ok := b.store.ThreadView(threadID); ok {
				b.out.Emit(EventThreads, ThreadsEvent{Threads: []Thread{view}})
			}
		}
		b.out.Emit(EventRead, ReadEvent{ThreadID: threadID, UserID: self, TimestampMS: ts})
		return
	}
	readMS := ts
	for _, id := range evt.MessageIDs {
		if msg, ok := b.store.MessageInThread(threadID, id); ok && msg.TimestampMS > 0 {
			readMS = max(readMS, msg.TimestampMS)
		}
	}
	if b.store.SetReadReceipt(threadID, evt.Sender.User, readMS) {
		if view, ok := b.store.ThreadView(threadID); ok {
			b.out.Emit(EventThreads, ThreadsEvent{Threads: []Thread{view}})
		}
	}
	b.out.Emit(EventRead, ReadEvent{ThreadID: threadID, UserID: evt.Sender.User, TimestampMS: readMS})
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
