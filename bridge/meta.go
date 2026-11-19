package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"

	"github.com/rs/zerolog"
	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/proto/waConsumerApplication"
	"go.mau.fi/whatsmeow/store"
	"go.mau.fi/whatsmeow/store/sqlstore"
	waTypes "go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/util/keys"

	"go.mau.fi/mautrix-meta/pkg/messagix"
	"go.mau.fi/mautrix-meta/pkg/messagix/cookies"
	"go.mau.fi/mautrix-meta/pkg/messagix/securehistory"
	"go.mau.fi/mautrix-meta/pkg/messagix/socket"
	"go.mau.fi/mautrix-meta/pkg/messagix/table"
)

const (
	cookiesFile = "cookies.json"
	sessionFile = "session.json"
	stateFile   = "state.json"
	cacheFile   = "cache.json"
	waDBFile    = "whatsmeow.db"

	maxConnectBackoff   = 5 * time.Minute
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
	dataDir  string
	cacheDir string
	out      Emitter
	log      zerolog.Logger
	store    *Store

	rootMu     sync.Mutex
	rootCtx    context.Context
	rootCancel context.CancelFunc

	mu           sync.Mutex
	sessionHash  [32]byte
	cookiesHash  [32]byte
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
	mediaLocksMu      sync.Mutex
	mediaLocks        map[string]*mediaLock
	backfills         sync.Map
	backfillSem       chan struct{}
	olderPageFn       func(ctx context.Context, rec threadRec, beforeMS int64, count int) bool
	latestPageFn      func(ctx context.Context, rec threadRec) bool
	catchUps          sync.Map
	waThumbFn         func(ctx context.Context, ref waThumbRef) ([]byte, error)
	appStateFn        func(task *socket.ReportAppStateTask)
	taskFn            func(ctx context.Context, tasks ...socket.Task) (*table.LSTable, error)
	waSendFn          func(ctx context.Context, rec threadRec, consumer *waConsumerApplication.ConsumerApplication) error
	waReadFn          func(ctx context.Context, chat, sender waTypes.JID, ids []waTypes.MessageID) error
	readSent          sync.Map
	readSentCount     atomic.Int64
	connectFn         func(ctx context.Context) error
	sleepFn           func(ctx context.Context, d time.Duration) bool
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
	presenceLS    atomic.Int64
	presenceE2EE  atomic.Int64

	historyMu      sync.Mutex
	historyBackup  *securehistory.Backup
	historyFactory func(*messagix.Client) historyService
	identityFn     func(context.Context) (*keys.KeyPair, error)

	stopSaver context.CancelFunc
	saverDone chan struct{}
}

func NewMetaBackend(dataDir, cacheDir string, out Emitter, log zerolog.Logger) (*MetaBackend, error) {
	if cacheDir == "" {
		cacheDir = dataDir
	}
	b := &MetaBackend{
		dataDir:      dataDir,
		cacheDir:     cacheDir,
		log:          log,
		store:        NewStore(),
		fetchWaiters: make(map[string][]chan struct{}),
		saverDone:    make(chan struct{}),
		backfillSem:  make(chan struct{}, maxConcurrentBackfills),
		mediaLocks:   make(map[string]*mediaLock),
	}
	b.rootCtx, b.rootCancel = context.WithCancel(context.Background())
	b.ordering = newOrderingEmitter(out, b.store)
	b.out = b.ordering
	b.avatars = newAvatarCache(cacheDir, httpAvatarFetcher, b.avatarStillValid)
	b.avatars.log = log
	b.avatars.parent = b.lifeCtx
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
		if cacheDir != dataDir {
			b.store.RebaseMediaPaths(filepath.Join(dataDir, mediaDirName), filepath.Join(cacheDir, mediaDirName))
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	b.stopSaver = cancel
	goSafe(b.log, "saver", func() { b.saverLoop(ctx) })
	return b, nil
}

func (b *MetaBackend) path(name string) string {
	return filepath.Join(b.dataDir, name)
}

func (b *MetaBackend) cachePath(name string) string {
	return filepath.Join(b.cacheDir, name)
}

func (b *MetaBackend) lifeCtx() context.Context {
	b.rootMu.Lock()
	defer b.rootMu.Unlock()
	return b.rootCtx
}

func (b *MetaBackend) endLife(renew bool) {
	b.rootMu.Lock()
	defer b.rootMu.Unlock()
	b.rootCancel()
	if renew {
		b.rootCtx, b.rootCancel = context.WithCancel(context.Background())
	}
}

func (b *MetaBackend) sleep(ctx context.Context, d time.Duration) bool {
	if b.sleepFn != nil {
		return b.sleepFn(ctx, d)
	}
	return sleepCtx(ctx, d)
}

func (b *MetaBackend) selfID() string {
	return b.store.SelfID()
}

func (b *MetaBackend) requireLogin() error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.cookies == nil {
		return newError(CodeNotLoggedIn, "not logged in")
	}
	return nil
}

func (b *MetaBackend) Close() {
	b.mu.Lock()
	loggedIn := b.cookies != nil
	b.mu.Unlock()
	b.endLife(false)
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
