package main

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/coder/websocket"
	"github.com/rs/zerolog"
	"go.mau.fi/util/exhttp"
	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/proto/waConsumerApplication"
	"go.mau.fi/whatsmeow/store"
	"go.mau.fi/whatsmeow/store/sqlstore"
	waTypes "go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/util/keys"

	"go.mau.fi/mautrix-meta/pkg/messagix"
	"go.mau.fi/mautrix-meta/pkg/messagix/cookies"
	"go.mau.fi/mautrix-meta/pkg/messagix/dgw"
	"go.mau.fi/mautrix-meta/pkg/messagix/httpclient"
	"go.mau.fi/mautrix-meta/pkg/messagix/methods"
	"go.mau.fi/mautrix-meta/pkg/messagix/securehistory"
	"go.mau.fi/mautrix-meta/pkg/messagix/socket"
	"go.mau.fi/mautrix-meta/pkg/messagix/table"
	"go.mau.fi/mautrix-meta/pkg/messagix/types"
)

const (
	cookiesFile = "cookies.json"
	sessionFile = "session.json"
	stateFile   = "state.json"
	cacheFile   = "cache.json"
	waDBFile    = "whatsmeow.db"

	maxConnectRetries   = 10
	historyFetchTimeout = 12 * time.Second
	sendWaitTimeout     = 15 * time.Second
	pendingTableMaxAge  = 5 * time.Minute
	stateSaveInterval   = time.Minute
)

type session struct {
	User            User                  `json:"user"`
	WADeviceID      uint16                `json:"wa_device_id,omitempty"`
	HistoryDevice   *securehistory.Device `json:"history_device,omitempty"`
	HistoryRestored bool                  `json:"history_restored,omitempty"`
}

type tableMode int

const (
	modeLive tableMode = iota
	modeHistory
)

type MetaBackend struct {
	dataDir string
	out     Emitter
	log     zerolog.Logger
	store   *Store

	mu           sync.Mutex
	cookies      *cookies.Cookies
	sess         *session
	client       *messagix.Client
	pendingTable *table.LSTable
	pendingAt    time.Time
	connState    string
	connCtx      context.Context
	connCancel   context.CancelFunc
	readySent    bool
	reconnecting bool
	lastFullReco time.Time

	e2eeMu      sync.Mutex
	e2ee        *whatsmeow.Client
	waDevice    *store.Device
	waContainer *sqlstore.Container
	e2eeRetries atomic.Int32
	e2eeResets  atomic.Int32

	e2eeLogoutRetried atomic.Bool
	active            atomic.Bool
	waOfflineSyncing  atomic.Bool

	stateMu       sync.Mutex
	lastStateSave time.Time

	fetchMu      sync.Mutex
	fetchWaiters map[string][]chan struct{}

	contactsRequested sync.Map
	mediaLocks        sync.Map
	backfills         sync.Map
	backfillSem       chan struct{}
	olderPageFn       func(ctx context.Context, rec threadRec, beforeMS int64, count int) bool
	waThumbFn         func(ctx context.Context, ref waThumbRef) ([]byte, error)
	appStateFn        func(task *socket.ReportAppStateTask)
	taskFn            func(ctx context.Context, tasks ...socket.Task) (*table.LSTable, error)
	waSendFn          func(ctx context.Context, rec threadRec, consumer *waConsumerApplication.ConsumerApplication) error
	waReadFn          func(ctx context.Context, chat, sender waTypes.JID, ids []waTypes.MessageID) error
	readSent          sync.Map
	afterSendMu       sync.Mutex
	afterSendTail     map[string]chan struct{}
	archive           archiveJobs
	forwardFn         func(ctx context.Context, msg Message, rec threadRec) (string, error)
	historyUploadFn   func(chat string, messageID, deletedID string)
	contactFetchFn    func(ctx context.Context, ids []string)
	avatarRefreshed   sync.Map
	avatars           *avatarCache
	pendingClientIDs  sync.Map

	ordering      *orderingEmitter
	gen           uint64
	closed        atomic.Bool
	sessionMu     sync.RWMutex
	syncing       atomic.Bool
	lsCatchingUp  atomic.Bool
	lastHeavySync atomic.Int64

	historyMu      sync.Mutex
	historyBackup  *securehistory.Backup
	historyFactory func(*messagix.Client) historyService
	identityFn     func(context.Context) (*keys.KeyPair, error)

	stopSaver context.CancelFunc
	saverDone chan struct{}
}

func NewMetaBackend(dataDir string, out Emitter, log zerolog.Logger) (*MetaBackend, error) {
	b := &MetaBackend{
		dataDir:      dataDir,
		log:          log,
		store:        NewStore(),
		fetchWaiters: make(map[string][]chan struct{}),
		saverDone:    make(chan struct{}),
		backfillSem:  make(chan struct{}, maxConcurrentBackfills),
	}
	b.ordering = newOrderingEmitter(out, b.store)
	b.out = b.ordering
	b.avatars = newAvatarCache(dataDir, httpAvatarFetcher, b.avatarStillValid)
	if err := b.loadSession(); err != nil {
		return nil, err
	}
	if b.cookies != nil {
		if err := b.store.Load(b.path(cacheFile)); err != nil && !errors.Is(err, os.ErrNotExist) {
			b.log.Warn().Err(err).Msg("Failed to load message cache, starting empty")
		}
		if b.sess != nil && b.store.SelfID() != "" && b.store.SelfID() != b.sess.User.ID {
			b.store.Clear()
		}
		if b.sess != nil {
			b.store.SetSelfID(b.sess.User.ID)
			b.store.SetSelfName(b.sess.User.Name)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	b.stopSaver = cancel
	go b.saverLoop(ctx)
	return b, nil
}

func (b *MetaBackend) path(name string) string {
	return filepath.Join(b.dataDir, name)
}

func (b *MetaBackend) loadSession() error {
	var sess session
	data, err := readSecretFile(b.path(sessionFile), b.log)
	switch {
	case err == nil:
		if err = json.Unmarshal(data, &sess); err != nil {
			b.log.Warn().Err(err).Msg("Invalid session file, ignoring")
		} else {
			b.sess = &sess
		}
	case errors.Is(err, errSecretUnreadable):
		b.log.Warn().Msg("Session file cannot be decrypted (other user or machine), a new login is required")
		return nil
	case !errors.Is(err, os.ErrNotExist):
		return fmt.Errorf("failed to read session: %w", err)
	}
	data, err = readSecretFile(b.path(cookiesFile), b.log)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	} else if errors.Is(err, errSecretUnreadable) {
		b.log.Warn().Msg("Cookies file cannot be decrypted (other user or machine), a new login is required")
		return nil
	} else if err != nil {
		return fmt.Errorf("failed to read cookies: %w", err)
	}
	c := &cookies.Cookies{Platform: types.Messenger}
	if err = json.Unmarshal(data, c); err != nil {
		b.log.Warn().Err(err).Msg("Invalid cookies file, ignoring")
		return nil
	}
	c.Platform = types.Messenger
	if !c.IsLoggedIn() || b.sess == nil {
		return nil
	}
	b.cookies = c
	return nil
}

func (b *MetaBackend) saveSessionLocked() {
	if b.sess != nil {
		if data, err := json.Marshal(b.sess); err == nil {
			if err = writeSecretFile(b.path(sessionFile), data); err != nil {
				b.log.Err(err).Msg("Failed to save session")
			}
		}
	}
	if b.cookies != nil {
		if data, err := json.Marshal(b.cookies); err == nil {
			if err = writeSecretFile(b.path(cookiesFile), data); err != nil {
				b.log.Err(err).Msg("Failed to save cookies")
			}
		}
	}
}

func (b *MetaBackend) saverLoop(ctx context.Context) {
	defer close(b.saverDone)
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			b.persist()
		}
	}
}

func (b *MetaBackend) persist() {
	b.sessionMu.RLock()
	defer b.sessionMu.RUnlock()
	b.mu.Lock()
	loggedIn := b.cookies != nil
	if loggedIn {
		b.saveSessionLocked()
	}
	b.mu.Unlock()
	if loggedIn {
		if err := b.store.Save(b.path(cacheFile)); err != nil {
			b.log.Err(err).Msg("Failed to save message cache")
		}
	}
}

func (b *MetaBackend) generation() uint64 {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.gen
}

func (b *MetaBackend) resetAccountState() {
	b.contactsRequested.Range(func(key, _ any) bool {
		b.contactsRequested.Delete(key)
		return true
	})
	b.e2eeResets.Store(0)
	b.e2eeLogoutRetried.Store(false)
	b.lastHeavySync.Store(0)
	b.mu.Lock()
	b.historyBackup = nil
	b.mu.Unlock()
	if err := os.RemoveAll(b.path(mediaDirName)); err != nil {
		b.log.Warn().Err(err).Msg("Failed to delete media cache")
	}
	if err := os.RemoveAll(b.path(avatarDirName)); err != nil {
		b.log.Warn().Err(err).Msg("Failed to delete avatar cache")
	}
}

func (b *MetaBackend) messagixConfig() *messagix.Config {
	return &messagix.Config{ClientSettings: exhttp.SensibleClientSettings}
}

func (b *MetaBackend) newClientLocked() *messagix.Client {
	client := messagix.NewClient(b.cookies, b.log.With().Str("component", "messagix").Logger(), b.messagixConfig())
	client.SetEventHandler(func(ctx context.Context, evt any) {
		b.handleMetaEvent(client, ctx, evt)
	})
	return client
}

func (b *MetaBackend) currentClient() *messagix.Client {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.client
}

func (b *MetaBackend) selfID() string {
	return b.store.SelfID()
}

func userFromInfo(info types.UserInfo) User {
	return User{
		ID:        strconv.FormatInt(info.GetFBID(), 10),
		Name:      info.GetName(),
		AvatarURL: info.GetAvatarURL(),
	}
}

func (b *MetaBackend) Status(ctx context.Context) (StatusResult, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	res := StatusResult{LoggedIn: b.cookies != nil}
	if b.cookies != nil && b.sess != nil {
		user := b.sess.User
		res.User = &user
	}
	res.Connected = b.client != nil && b.connState == StateConnected && b.client.IsConnected()
	return res, nil
}

func classifyLoginError(err error) error {
	switch {
	case errors.Is(err, httpclient.ErrTokenInvalidated):
		return newError(CodeAuthFailed, "cookies are invalid or expired: %v", err)
	case errors.Is(err, httpclient.ErrCheckpointRequired):
		return newError(CodeAuthFailed, "checkpoint required, open messenger.com in a browser: %v", err)
	case errors.Is(err, httpclient.ErrChallengeRequired):
		return newError(CodeAuthFailed, "challenge required, open messenger.com in a browser: %v", err)
	case errors.Is(err, httpclient.ErrConsentRequired):
		return newError(CodeAuthFailed, "consent required, open messenger.com in a browser: %v", err)
	case errors.Is(err, httpclient.ErrAccountSuspended):
		return newError(CodeAuthFailed, "account suspended: %v", err)
	case errors.Is(err, httpclient.ErrRateLimited):
		return newError(CodeInternal, "rate limited by Meta: %v", err)
	default:
		return newError(CodeInternal, "failed to load messenger.com: %v", err)
	}
}

func (b *MetaBackend) Login(ctx context.Context, raw map[string]string) (User, error) {
	values := make(map[cookies.MetaCookieName]string, len(raw))
	for k, v := range raw {
		values[cookies.MetaCookieName(k)] = v
	}
	var missing []string
	for _, name := range []string{"xs", "c_user", "datr"} {
		if raw[name] == "" {
			missing = append(missing, name)
		}
	}
	c := &cookies.Cookies{Platform: types.Messenger}
	c.UpdateValues(values)
	for _, name := range c.GetMissingCookieNames() {
		if !strings.Contains(strings.Join(missing, ","), string(name)) {
			missing = append(missing, string(name))
		}
	}
	if len(missing) > 0 {
		return User{}, newError(CodeBadRequest, "missing cookies: %s", strings.Join(missing, ", "))
	}

	client := messagix.NewClient(c, b.log.With().Str("component", "messagix").Logger(), b.messagixConfig())
	info, tbl, err := client.LoadMessagesPage(ctx)
	if err != nil {
		b.log.Err(err).Msg("Login failed")
		return User{}, classifyLoginError(err)
	}
	if info.GetFBID() != c.GetUserID() {
		return User{}, newError(CodeAuthFailed, "logged-in account %d does not match c_user cookie", info.GetFBID())
	}
	user := userFromInfo(info)

	if b.teardown(false) {
		b.out.Emit(EventConnection, ConnectionEvent{State: StateDisconnected, Detail: "new login"})
	}

	b.mu.Lock()
	var oldDevice uint16
	accountChanged := b.sess != nil && b.sess.User.ID != user.ID
	if accountChanged {
		oldDevice = b.sess.WADeviceID
		b.store.Clear()
	}
	b.mu.Unlock()
	if accountChanged {
		b.resetAccountState()
	}
	b.mu.Lock()
	b.gen++
	b.closed.Store(false)
	newSess := &session{User: user}
	if b.sess != nil && b.sess.User.ID == user.ID {
		newSess.WADeviceID = b.sess.WADeviceID
		newSess.HistoryDevice = b.sess.HistoryDevice
		newSess.HistoryRestored = b.sess.HistoryRestored
	}
	b.historyBackup = nil
	b.sess = newSess
	b.cookies = c
	client.SetEventHandler(func(ctx context.Context, evt any) {
		b.handleMetaEvent(client, ctx, evt)
	})
	b.client = client
	b.pendingTable = tbl
	b.pendingAt = time.Now()
	b.saveSessionLocked()
	b.mu.Unlock()
	_ = os.Remove(b.path(stateFile))
	if oldDevice != 0 {
		b.deleteWADevice(ctx, oldDevice)
	}

	b.store.SetSelfID(user.ID)
	b.store.SetSelfName(user.Name)
	b.handleTable(tbl, modeHistory)
	b.persist()
	b.log.Info().Msg("Logged in")
	return user, nil
}

func (b *MetaBackend) Logout(ctx context.Context) error {
	b.mu.Lock()
	loggedIn := b.cookies != nil
	var deviceID uint16
	if b.sess != nil {
		deviceID = b.sess.WADeviceID
	}
	b.cookies = nil
	b.sess = nil
	b.pendingTable = nil
	b.gen++
	b.closed.Store(true)
	b.mu.Unlock()
	wasConnected := b.teardown(false)
	if deviceID != 0 {
		b.deleteWADevice(ctx, deviceID)
	}
	b.sessionMu.Lock()
	for _, name := range []string{cookiesFile, sessionFile, stateFile, cacheFile} {
		if err := os.Remove(b.path(name)); err != nil && !errors.Is(err, os.ErrNotExist) {
			b.log.Warn().Err(err).Str("file", name).Msg("Failed to delete session file")
		}
	}
	b.store.Clear()
	b.resetAccountState()
	b.ordering.reset()
	b.sessionMu.Unlock()
	if wasConnected {
		b.out.Emit(EventConnection, ConnectionEvent{State: StateDisconnected, Detail: "logged out"})
	}
	if loggedIn {
		b.log.Info().Msg("Logged out")
	}
	return nil
}

func (b *MetaBackend) Connect(ctx context.Context) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.cookies == nil {
		return newError(CodeNotLoggedIn, "not logged in")
	}
	if b.connCancel != nil {
		return nil
	}
	b.startConnectLocked()
	return nil
}

func (b *MetaBackend) startConnectLocked() {
	connCtx, cancel := context.WithCancel(context.Background())
	connCtx = b.log.WithContext(connCtx)
	b.connCtx = connCtx
	b.connCancel = cancel
	b.connState = StateConnecting
	b.readySent = false
	go b.connectLoop(connCtx)
}

func (b *MetaBackend) setConnState(state string) {
	b.mu.Lock()
	b.connState = state
	b.mu.Unlock()
}

func (b *MetaBackend) emitConnection(state, detail string, permanent bool) {
	b.setConnState(state)
	b.out.Emit(EventConnection, ConnectionEvent{State: state, Detail: redactText(detail), Permanent: permanent})
}

func (b *MetaBackend) connectLoop(ctx context.Context) {
	b.emitConnection(StateConnecting, "", false)
	for attempt := 0; ; attempt++ {
		if attempt > 0 {
			wait := min(time.Duration(1<<min(attempt, 8))*time.Second, messagix.MaxConnectBackoff)
			b.log.Info().Dur("retry_in", wait).Int("attempt", attempt).Msg("Retrying connection")
			if !sleepCtx(ctx, wait) {
				return
			}
			b.emitConnection(StateConnecting, fmt.Sprintf("retry %d", attempt), false)
		}
		err := b.connectOnce(ctx)
		if err == nil || ctx.Err() != nil {
			return
		}
		b.log.Err(err).Msg("Connection attempt failed")
		permanent, invalidate := classifyConnectError(err)
		if invalidate {
			b.emitConnection(StateError, err.Error(), true)
			b.invalidateSession()
			return
		}
		if permanent || attempt+1 >= maxConnectRetries || errors.Is(err, httpclient.ErrVersionIDNotFound) {
			b.emitConnection(StateError, err.Error(), permanent)
			b.stopConnect(ctx)
			return
		}
		b.emitConnection(StateError, err.Error(), false)
	}
}

func (b *MetaBackend) stopConnect(ctx context.Context) {
	b.mu.Lock()
	if b.connCtx == ctx && b.connCancel != nil {
		b.connCancel()
		b.connCancel = nil
		b.connCtx = nil
	}
	b.mu.Unlock()
}

func classifyConnectError(err error) (permanent, invalidate bool) {
	var lsErr *types.ErrorResponse
	switch {
	case errors.Is(err, httpclient.ErrTokenInvalidated):
		return true, true
	case errors.As(err, &lsErr) && lsErr.ErrorCode == 1357053:
		return true, true
	case errors.Is(err, httpclient.ErrCheckpointRequired),
		errors.Is(err, httpclient.ErrChallengeRequired),
		errors.Is(err, httpclient.ErrConsentRequired),
		errors.Is(err, httpclient.ErrAccountSuspended):
		return true, false
	default:
		return false, false
	}
}

func (b *MetaBackend) invalidateSession() {
	b.teardown(false)
	b.mu.Lock()
	b.cookies = nil
	b.pendingTable = nil
	b.mu.Unlock()
	_ = os.Remove(b.path(cookiesFile))
	_ = os.Remove(b.path(stateFile))
	b.log.Warn().Msg("Session invalidated, a new login is required")
}

func (b *MetaBackend) connectOnce(ctx context.Context) error {
	b.mu.Lock()
	if b.cookies == nil {
		b.mu.Unlock()
		return httpclient.ErrTokenInvalidated
	}
	gen := b.gen
	client := b.client
	if client == nil {
		client = b.newClientLocked()
		b.client = client
	}
	pending := b.pendingTable
	pendingFresh := pending != nil && time.Since(b.pendingAt) < pendingTableMaxAge
	b.pendingTable = nil
	b.mu.Unlock()

	var initial *table.LSTable
	switch {
	case pendingFresh:
		initial = pending
	case b.loadCachedState(client):
		b.log.Info().Msg("Reconnecting with cached state")
	default:
		info, tbl, err := client.LoadMessagesPage(ctx)
		if err != nil {
			return err
		}
		if b.generation() != gen {
			return context.Canceled
		}
		if strconv.FormatInt(info.GetFBID(), 10) != b.selfIDOrEmpty() && b.selfIDOrEmpty() != "" {
			return fmt.Errorf("%w: account changed", httpclient.ErrTokenInvalidated)
		}
		b.updateUser(userFromInfo(info))
		initial = tbl
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if b.generation() != gen {
		return context.Canceled
	}
	if initial != nil {
		b.handleTable(initial, modeHistory)
	}
	b.lsCatchingUp.Store(true)
	return client.Connect(ctx)
}

func (b *MetaBackend) selfIDOrEmpty() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.sess == nil {
		return ""
	}
	return b.sess.User.ID
}

func (b *MetaBackend) updateUser(user User) {
	b.mu.Lock()
	if b.sess == nil || b.cookies == nil {
		b.mu.Unlock()
		return
	}
	b.sess.User = user
	b.saveSessionLocked()
	b.mu.Unlock()
	b.store.SetSelfID(user.ID)
	b.store.SetSelfName(user.Name)
}

func (b *MetaBackend) loadCachedState(client *messagix.Client) bool {
	data, err := readSecretFile(b.path(stateFile), b.log)
	if err != nil {
		if errors.Is(err, errSecretUnreadable) {
			_ = os.Remove(b.path(stateFile))
		}
		return false
	}
	if err = client.LoadState(data); err != nil {
		b.log.Info().Err(err).Msg("Not using cached connection state")
		_ = os.Remove(b.path(stateFile))
		return false
	}
	if info, err := client.GetCurrentAccount(); err == nil && info.GetFBID() != 0 {
		user := userFromInfo(info)
		if user.Name == "" {
			user.Name = b.currentUser().Name
		}
		b.updateUser(user)
	}
	return true
}

func (b *MetaBackend) currentUser() User {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.sess == nil {
		return User{}
	}
	return b.sess.User
}

func (b *MetaBackend) teardown(saveState bool) (wasConnected bool) {
	b.mu.Lock()
	client := b.client
	cancel := b.connCancel
	wasConnected = b.connState == StateConnected
	b.client = nil
	b.connCancel = nil
	b.connCtx = nil
	b.connState = ""
	b.readySent = false
	b.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	b.stopE2EE()
	if client != nil {
		client.SetEventHandler(nil)
		client.Disconnect()
		if saveState {
			b.saveState(client, true)
		}
	}
	return
}

func (b *MetaBackend) saveState(client *messagix.Client, force bool) {
	b.stateMu.Lock()
	defer b.stateMu.Unlock()
	if !force && time.Since(b.lastStateSave) < stateSaveInterval {
		return
	}
	state, err := client.DumpState()
	if err != nil {
		b.log.Err(err).Msg("Failed to dump connection state")
		return
	}
	if state == nil {
		return
	}
	if err = writeSecretFile(b.path(stateFile), state); err != nil {
		b.log.Err(err).Msg("Failed to save connection state")
		return
	}
	b.lastStateSave = time.Now()
}

func (b *MetaBackend) fullReconnect(reason string) {
	b.mu.Lock()
	if b.reconnecting || b.cookies == nil || time.Since(b.lastFullReco) < 2*time.Minute {
		b.mu.Unlock()
		return
	}
	b.reconnecting = true
	b.lastFullReco = time.Now()
	b.mu.Unlock()
	defer func() {
		b.mu.Lock()
		b.reconnecting = false
		b.mu.Unlock()
	}()
	b.log.Info().Str("reason", reason).Msg("Doing full reconnect")
	_ = os.Remove(b.path(stateFile))
	b.teardown(false)
	b.mu.Lock()
	if b.cookies != nil && b.connCancel == nil {
		b.startConnectLocked()
	}
	b.mu.Unlock()
}

func (b *MetaBackend) isCurrent(client *messagix.Client) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.client == client
}

func (b *MetaBackend) handleMetaEvent(client *messagix.Client, ctx context.Context, rawEvt any) {
	if !b.isCurrent(client) {
		return
	}
	switch evt := rawEvt.(type) {
	case *table.LSTable:
		b.handleTable(evt, modeLive)
		client.PostHandlePublishResponse(evt)
		b.saveState(client, false)
	case *messagix.ConnectedEvent:
		b.onConnected(client)
	case *messagix.ReconnectedEvent:
		b.onConnected(client)
	case *messagix.TransientDisconnectEvent:
		b.lsCatchingUp.Store(true)
		detail := ""
		if evt.Err != nil {
			detail = evt.Err.Error()
		}
		b.emitConnection(StateDisconnected, detail, false)
		if errors.Is(evt.Err, messagix.ErrFatalSocketSyncError) {
			go b.fullReconnect("socket sync failed")
		}
	case *messagix.PermanentErrorEvent:
		detail := ""
		if evt.Err != nil {
			detail = evt.Err.Error()
		}
		if websocket.CloseStatus(evt.Err) == dgw.CloseStatusUnauthorized {
			b.emitConnection(StateError, detail, true)
			go b.invalidateSession()
		} else {
			b.emitConnection(StateError, detail, false)
		}
	default:
		b.log.Debug().Type("event_type", rawEvt).Msg("Unhandled messagix event")
	}
}

func (b *MetaBackend) onConnected(client *messagix.Client) {
	b.lsCatchingUp.Store(false)
	b.mu.Lock()
	b.connState = StateConnected
	firstReady := !b.readySent
	b.readySent = true
	connCtx := b.connCtx
	b.mu.Unlock()
	b.out.Emit(EventConnection, ConnectionEvent{State: StateConnected})
	b.saveState(client, true)
	if connCtx == nil {
		return
	}
	go b.startE2EE(connCtx, client)
	go b.syncThreads(connCtx, client, firstReady)
}

func (b *MetaBackend) requireLogin() error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.cookies == nil {
		return newError(CodeNotLoggedIn, "not logged in")
	}
	return nil
}

func (b *MetaBackend) connectedClient(ctx context.Context) (*messagix.Client, error) {
	b.mu.Lock()
	client := b.client
	state := b.connState
	b.mu.Unlock()
	if client == nil || state == "" {
		return nil, newError(CodeNotConnected, "not connected")
	}
	if err := client.WaitUntilCanSendMessages(ctx, sendWaitTimeout); err != nil {
		return nil, newError(CodeNotConnected, "not connected: %v", err)
	}
	return client, nil
}

func (b *MetaBackend) Threads(ctx context.Context, limit int) ([]Thread, error) {
	if err := b.requireLogin(); err != nil {
		return nil, err
	}
	threads := b.store.ListThreads(limit)
	b.ordering.markAnnounced(threads)
	return threads, nil
}

func parseThreadKey(threadID string) (int64, error) {
	key, err := strconv.ParseInt(threadID, 10, 64)
	if err != nil || key <= 0 {
		return 0, newError(CodeNotFound, "unknown thread %s", threadID)
	}
	return key, nil
}

func (b *MetaBackend) Messages(ctx context.Context, threadID string, beforeMS int64, limit int, fetchMissing bool) ([]Message, error) {
	if err := b.requireLogin(); err != nil {
		return nil, err
	}
	rec, ok := b.store.Thread(threadID)
	if !ok {
		return nil, newError(CodeNotFound, "unknown thread %s", threadID)
	}
	msgs := b.store.Messages(threadID, beforeMS, limit)
	if len(msgs) < limit && fetchMissing {
		b.startBackfill(rec, beforeMS, limit-len(msgs))
	}
	return msgs, nil
}

func (b *MetaBackend) fetchOlderPage(ctx context.Context, rec threadRec, beforeMS int64, count int) bool {
	if more, known := b.store.HasMoreBefore(rec.ID); known && !more {
		if _, hasAny := b.store.OldestMessage(rec.ID); hasAny {
			return false
		}
	}
	if rec.E2EE {
		if !b.historyEnrolled() {
			return false
		}
		_, svc, err := b.historyClient()
		if err != nil {
			return false
		}
		if oldest, ok := b.store.OldestMessage(rec.ID); ok && (beforeMS == 0 || oldest.TimestampMS < beforeMS) {
			beforeMS = oldest.TimestampMS
		}
		if beforeMS == 0 {
			beforeMS = time.Now().Add(historyFutureSlack).UnixMilli()
		}
		_, err = b.fetchHistoryPage(ctx, svc, rec, beforeMS, max(count, historyPageSize))
		return err == nil
	}
	key, err := parseThreadKey(rec.ID)
	if err != nil {
		return false
	}
	b.mu.Lock()
	client := b.client
	connected := b.connState == StateConnected
	b.mu.Unlock()
	if client == nil || !connected {
		return false
	}
	return b.fetchHistory(ctx, client, rec.ID, key)
}

func (b *MetaBackend) addFetchWaiter(threadID string) chan struct{} {
	ch := make(chan struct{})
	b.fetchMu.Lock()
	b.fetchWaiters[threadID] = append(b.fetchWaiters[threadID], ch)
	b.fetchMu.Unlock()
	return ch
}

func (b *MetaBackend) removeFetchWaiter(threadID string, ch chan struct{}) {
	b.fetchMu.Lock()
	defer b.fetchMu.Unlock()
	waiters := b.fetchWaiters[threadID]
	for i, w := range waiters {
		if w == ch {
			b.fetchWaiters[threadID] = append(waiters[:i], waiters[i+1:]...)
			break
		}
	}
	if len(b.fetchWaiters[threadID]) == 0 {
		delete(b.fetchWaiters, threadID)
	}
}

func (b *MetaBackend) notifyFetchWaiters(threadID string) {
	b.fetchMu.Lock()
	waiters := b.fetchWaiters[threadID]
	delete(b.fetchWaiters, threadID)
	b.fetchMu.Unlock()
	for _, ch := range waiters {
		close(ch)
	}
}

func (b *MetaBackend) fetchHistory(ctx context.Context, client *messagix.Client, threadID string, key int64) bool {
	var task socket.Task
	if oldest, ok := b.store.OldestMessage(threadID); ok {
		task = &socket.FetchMessagesTask{
			ThreadKey:            key,
			Direction:            0,
			ReferenceTimestampMs: oldest.TimestampMS,
			ReferenceMessageId:   oldest.ID,
			SyncGroup:            1,
			Cursor:               client.GetCursor(1),
		}
	} else {
		task = &socket.CreateThreadTask{ThreadFBID: key, SyncGroup: 1}
	}
	waiter := b.addFetchWaiter(threadID)
	defer b.removeFetchWaiter(threadID, waiter)
	tbl, err := client.ExecuteTasks(ctx, task)
	if err != nil {
		b.log.Err(err).Str("thread_id", threadID).Msg("Failed to request message history")
		return false
	}
	if tbl != nil {
		b.handleTable(tbl, modeHistory)
	}
	timer := time.NewTimer(historyFetchTimeout)
	defer timer.Stop()
	select {
	case <-waiter:
		return true
	case <-timer.C:
		b.log.Debug().Str("thread_id", threadID).Msg("Timed out waiting for message history")
		return false
	case <-ctx.Done():
		return false
	}
}

func (b *MetaBackend) Send(ctx context.Context, threadID, text, replyToID, clientID string, mentions []Mention) (string, error) {
	if err := b.requireLogin(); err != nil {
		return "", err
	}
	rec, ok := b.store.Thread(threadID)
	if !ok {
		return "", newError(CodeNotFound, "unknown thread %s", threadID)
	}
	var replyTo *Message
	if replyToID != "" {
		target, found := b.store.MessageInThread(threadID, replyToID)
		if !found {
			return "", newError(CodeBadRequest, "reply target %s is not a message of this thread", replyToID)
		}
		replyTo = &target
	}
	mentions, err := b.store.CheckMentions(threadID, text, mentions)
	if err != nil {
		return "", err
	}
	if rec.E2EE {
		return b.sendE2EE(ctx, rec, text, replyTo, clientID, mentions)
	}
	key, err := parseThreadKey(threadID)
	if err != nil {
		return "", err
	}
	client, err := b.sendClient(ctx)
	if err != nil {
		return "", err
	}
	otid := methods.GenerateEpochID()
	task := &socket.SendMessageTask{
		ThreadId:         key,
		Otid:             otid,
		Source:           table.MESSENGER_INBOX_IN_THREAD,
		InitiatingSource: table.FACEBOOK_INBOX,
		SendType:         table.TEXT,
		SyncGroup:        1,
		Text:             text,
		MentionData:      lsMentionData(text, mentions),
	}
	if strings.Contains(text, "http://") || strings.Contains(text, "https://") {
		task.TextHasLinks = 1
	}
	task.ReplyMetaData = lsReplyMeta(replyTo)
	echo := Message{ThreadID: threadID, Text: text, Attachments: []Attachment{}, ClientID: clientID, Mentions: mentions}
	if task.ReplyMetaData != nil {
		echo.ReplyToID = replyTo.ID
	}
	return b.executeLSSend(ctx, client, key, otid, task, echo, nil)
}

func (b *MetaBackend) sendClient(ctx context.Context) (*messagix.Client, error) {
	if b.taskFn != nil {
		return nil, nil
	}
	return b.connectedClient(ctx)
}

func (b *MetaBackend) executeSendTasks(ctx context.Context, client *messagix.Client, tasks ...socket.Task) (*table.LSTable, error) {
	if b.taskFn != nil {
		return b.taskFn(ctx, tasks...)
	}
	return client.ExecuteTasks(ctx, tasks...)
}

func (b *MetaBackend) executeLSSend(ctx context.Context, client *messagix.Client, key, otid int64, task *socket.SendMessageTask, echo Message, onEcho func(msgID string)) (string, error) {
	otidStr := strconv.FormatInt(otid, 10)
	if echo.ClientID != "" {
		b.pendingClientIDs.Store(otidStr, echo.ClientID)
		defer b.pendingClientIDs.Delete(otidStr)
	}
	tasks := []socket.Task{task}
	marksRead := sendMarksRead(ctx)
	readAt := time.Now().UnixMilli()
	if marksRead {
		tasks = append(tasks, &socket.ThreadMarkReadTask{ThreadId: key, SyncGroup: 1, LastReadWatermarkTs: readAt})
	}
	resp, err := b.executeSendTasks(ctx, client, tasks...)
	if err != nil {
		return "", uncertainError(newError(CodeInternal, "failed to send message: %v", err))
	}
	var msgID string
	if resp != nil {
		for _, replace := range resp.LSReplaceOptimsiticMessage {
			if replace.OfflineThreadingId == otidStr {
				msgID = replace.MessageId
			}
		}
		if msgID == "" {
			for _, failed := range resp.LSMarkOptimisticMessageFailed {
				return "", newError(CodeInternal, "message rejected: %s", failed.Message)
			}
			for _, failed := range resp.LSHandleFailedTask {
				return "", uncertainError(newError(CodeInternal, "message not confirmed: %s", failed.Message))
			}
		}
		b.handleTable(resp, modeLive)
	}
	if msgID == "" {
		return "", uncertainError(newError(CodeInternal, "Meta did not confirm the message"))
	}
	if onEcho != nil {
		onEcho(msgID)
	}
	if marksRead {
		b.store.UpdateExistingThread(echo.ThreadID, func(rec *threadRec) { rec.MarkedReadMS = max(rec.MarkedReadMS, readAt) })
	}
	if _, found := b.store.FindMessage(msgID); !found {
		ts := time.Now().UnixMilli()
		if parsed, err := methods.ParseMessageID(msgID); err == nil && parsed > 0 && abs64(parsed-ts) < int64(time.Hour/time.Millisecond) {
			ts = parsed
		}
		user := b.currentUser()
		echo.ID = msgID
		echo.SenderID = user.ID
		echo.SenderName = user.Name
		echo.TimestampMS = ts
		echo.IsOutgoing = true
		b.recordNewMessage(echo, true)
	}
	return msgID, nil
}

func abs64(v int64) int64 {
	if v < 0 {
		return -v
	}
	return v
}

const liveMessageWindow = 2 * time.Minute

func (b *MetaBackend) emitMessage(event string, msg Message) {
	b.out.Emit(event, b.store.Decorate(msg))
}

func isLiveTimestamp(ts int64) bool {
	return ts >= time.Now().Add(-liveMessageWindow).UnixMilli()
}

func (b *MetaBackend) recordNewMessage(msg Message, live bool) {
	if _, existed := b.store.PutMessage(msg); existed {
		return
	}
	if live {
		b.applyMessageToThread(msg)
		b.emitMessage(EventMessage, msg)
	} else {
		b.applyHistoryToThread(msg)
	}
	if view, ok := b.store.ThreadView(msg.ThreadID); ok {
		b.out.Emit(EventThreads, ThreadsEvent{Threads: []Thread{view}})
	}
}

func (b *MetaBackend) applyHistoryToThread(msg Message) {
	if !b.store.HasThread(msg.ThreadID) {
		return
	}
	b.store.UpdateThread(msg.ThreadID, func(rec *threadRec) {
		if msg.TimestampMS >= rec.LastActivityMS {
			rec.LastActivityMS = msg.TimestampMS
			if s := snippetFor(msg); s != "" {
				rec.Snippet = s
			}
		}
		if msg.IsOutgoing {
			rec.LastReadMS = max(rec.LastReadMS, msg.TimestampMS)
			if rec.LastReadMS >= rec.LastActivityMS {
				rec.UnreadCount = 0
			}
		}
	})
}

func (b *MetaBackend) applyMessageToThread(msg Message) {
	if !b.store.HasThread(msg.ThreadID) {
		return
	}
	b.store.UpdateThread(msg.ThreadID, func(rec *threadRec) {
		if msg.TimestampMS >= rec.LastActivityMS {
			rec.LastActivityMS = msg.TimestampMS
			if s := snippetFor(msg); s != "" {
				rec.Snippet = s
			}
		}
		if msg.IsOutgoing {
			rec.UnreadCount = 0
			rec.LastReadMS = max(rec.LastReadMS, msg.TimestampMS)
		} else if msg.TimestampMS > rec.LastReadMS {
			rec.UnreadCount++
		}
	})
}

func (b *MetaBackend) MarkRead(ctx context.Context, threadID string) error {
	if err := b.requireLogin(); err != nil {
		return err
	}
	rec, ok := b.store.Thread(threadID)
	if !ok {
		return newError(CodeNotFound, "unknown thread %s", threadID)
	}
	watermark := max(rec.LastActivityMS, b.store.LatestTimestamp(threadID))
	if watermark == 0 {
		watermark = time.Now().UnixMilli()
	}
	if rec.E2EE {
		if err := b.markReadE2EE(ctx, rec); err != nil {
			return err
		}
	} else if rec.UnreadCount > 0 || rec.MarkedReadMS < watermark {
		key, err := parseThreadKey(threadID)
		if err != nil {
			return err
		}
		if _, err = b.runTasks(ctx, &socket.ThreadMarkReadTask{ThreadId: key, LastReadWatermarkTs: watermark, SyncGroup: syncGroupInbox}); err != nil {
			return err
		}
		b.store.UpdateThread(threadID, func(rec *threadRec) { rec.MarkedReadMS = max(rec.MarkedReadMS, watermark) })
	}
	b.store.UpdateThread(threadID, func(rec *threadRec) {
		rec.UnreadCount = 0
		rec.LastReadMS = max(rec.LastReadMS, watermark)
	})
	if view, ok := b.store.ThreadView(threadID); ok {
		b.out.Emit(EventThreads, ThreadsEvent{Threads: []Thread{view}})
	}
	return nil
}

func (b *MetaBackend) Media(ctx context.Context, threadID, messageID, attachmentID, variant string) (MediaResult, error) {
	if err := b.requireLogin(); err != nil {
		return MediaResult{}, err
	}
	msg, ok := b.store.MessageInThread(threadID, messageID)
	if !ok {
		return MediaResult{}, newError(CodeNotFound, "unknown message %s in thread %s", messageID, threadID)
	}
	att, ok := findAttachment(msg, attachmentID)
	if !ok {
		return MediaResult{}, newError(CodeNotFound, "unknown attachment %s", attachmentID)
	}
	if variant == VariantThumb {
		return b.mediaThumb(ctx, messageID, att)
	}
	return b.fullMedia(ctx, messageID, att)
}

func (b *MetaBackend) mediaSource(messageID string, att Attachment) (mediaRec, error) {
	rec, ok := b.store.Media(messageID, att.ID)
	if !ok {
		if att.Kind == "link" || att.URL == "" {
			return mediaRec{}, newError(CodeNotFound, "attachment %s has no downloadable media", att.ID)
		}
		rec = mediaRec{Kind: att.Kind, URL: att.URL, MimeType: att.MimeType, FileName: att.FileName}
	}
	return rec, nil
}

func (b *MetaBackend) downloadMediaTo(ctx context.Context, rec mediaRec, att Attachment, f *os.File) (string, error) {
	maxBytes := int64(maxMediaBytes)
	if att.Kind == "link" || att.Kind == "other" {
		maxBytes = maxPreviewBytes
	}
	if rec.WATransport == nil && rec.ReceiverFetchID == "" && rec.URL == "" {
		return "", newError(CodeNotFound, "attachment %s has no downloadable media", att.ID)
	}
	var mimeType string
	var err error
	if rec.WATransport != nil || rec.ReceiverFetchID != "" {
		mimeType, err = b.downloadE2EEMedia(ctx, rec, maxBytes, f)
	} else {
		mimeType, err = httpDownloadToFile(ctx, rec.URL, maxBytes, f)
		mimeType = cmp.Or(rec.MimeType, mimeType)
	}
	if err == nil {
		return mimeType, nil
	}
	var be *BridgeError
	switch {
	case errors.As(err, &be):
		return "", be
	case errors.Is(err, errMediaHostNotAllowed):
		return "", newError(CodeUnsupported, "media is not hosted on a Meta CDN")
	case errors.Is(err, errMediaExpired):
		return "", newError(CodeUnsupported, "media is no longer available on Meta's servers: %v", err)
	case rec.ExpiresMS > 0 && rec.ExpiresMS < time.Now().UnixMilli():
		return "", newError(CodeUnsupported, "media URL expired: %v", err)
	default:
		return "", newError(CodeInternal, "failed to download media: %v", err)
	}
}

func (b *MetaBackend) fullMedia(ctx context.Context, messageID string, att Attachment) (MediaResult, error) {
	unlock := b.lockMediaKey(mediaKey(messageID, att.ID))
	defer unlock()
	rec, err := b.mediaSource(messageID, att)
	if err != nil {
		return MediaResult{}, err
	}
	if fileExists(rec.Path) {
		return MediaResult{Path: rec.Path, MimeType: rec.MimeType}, nil
	}
	f, err := newMediaTemp(b.dataDir)
	if err != nil {
		return MediaResult{}, newError(CodeInternal, "%v", err)
	}
	mimeType, err := b.downloadMediaTo(ctx, rec, att, f)
	if err != nil {
		discardMediaTemp(f)
		return MediaResult{}, err
	}
	path, mimeType, err := finalizeMediaTemp(f, mimeType, cmp.Or(rec.FileName, att.FileName))
	if err != nil {
		return MediaResult{}, newError(CodeInternal, "%v", err)
	}
	b.store.SetMediaPath(messageID, att.ID, path, mimeType)
	return MediaResult{Path: path, MimeType: mimeType}, nil
}

func (b *MetaBackend) lockMediaKey(key string) func() {
	value, _ := b.mediaLocks.LoadOrStore(key, &sync.Mutex{})
	mu := value.(*sync.Mutex)
	mu.Lock()
	return mu.Unlock
}

func (b *MetaBackend) Close() {
	b.mu.Lock()
	loggedIn := b.cookies != nil
	b.mu.Unlock()
	b.teardown(loggedIn)
	b.stopSaver()
	<-b.saverDone
	b.persist()
	b.e2eeMu.Lock()
	if b.waContainer != nil {
		_ = b.waContainer.Close()
		b.waContainer = nil
	}
	b.e2eeMu.Unlock()
}
