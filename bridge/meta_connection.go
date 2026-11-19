package main

import (
	"context"
	"errors"
	"fmt"
	"math/rand/v2"
	"os"
	"strconv"
	"time"

	"github.com/coder/websocket"
	"go.mau.fi/mautrix-meta/pkg/messagix"
	"go.mau.fi/mautrix-meta/pkg/messagix/dgw"
	"go.mau.fi/mautrix-meta/pkg/messagix/httpclient"
	"go.mau.fi/mautrix-meta/pkg/messagix/table"
	"go.mau.fi/mautrix-meta/pkg/messagix/types"
	"go.mau.fi/util/exhttp"
)

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
	connCtx, cancel := context.WithCancel(b.lifeCtx())
	connCtx = b.log.WithContext(connCtx)
	b.connCtx = connCtx
	b.connCancel = cancel
	b.connState = StateConnecting
	b.readySent = false
	goSafe(b.log, "connect", func() { b.connectLoop(connCtx) })
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

func jittered(d time.Duration, rnd float64) time.Duration {
	return time.Duration(float64(d) * (0.8 + 0.4*rnd))
}

func connectBackoff(attempt int, rnd float64) time.Duration {
	base := min(time.Second<<min(attempt, 9), maxConnectBackoff)
	return min(jittered(base, rnd), maxConnectBackoff)
}

func (b *MetaBackend) connectAttempt(ctx context.Context) error {
	if b.connectFn != nil {
		return b.connectFn(ctx)
	}
	return b.connectOnce(ctx)
}

func (b *MetaBackend) connectLoop(ctx context.Context) {
	b.emitConnection(StateConnecting, "", false)
	for attempt := 0; ; attempt++ {
		if attempt > 0 {
			wait := connectBackoff(attempt, rand.Float64())
			b.log.Info().Dur("retry_in", wait).Int("attempt", attempt).Msg("Retrying connection")
			if !b.sleep(ctx, wait) {
				return
			}
		}
		err := b.connectAttempt(ctx)
		if err == nil || ctx.Err() != nil {
			return
		}
		b.log.Err(err).Int("attempt", attempt+1).Msg("Connection attempt failed")
		permanent, invalidate := classifyConnectError(err)
		if invalidate {
			b.emitConnection(StateError, err.Error(), true)
			b.invalidateSession()
			return
		}
		if permanent || errors.Is(err, httpclient.ErrVersionIDNotFound) {
			b.emitConnection(StateError, err.Error(), permanent)
			b.stopConnect(ctx)
			return
		}
		b.emitConnection(StateConnecting, fmt.Sprintf("retry %d", attempt+1), false)
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
		client.Disconnect()
		if saveState {
			b.saveState(client, true)
		}
	}
	return
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
	defer b.recoverHandler("meta_event")
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
			goSafe(b.log, "full_reconnect", func() { b.fullReconnect("socket sync failed") })
		}
	case *messagix.PermanentErrorEvent:
		detail := ""
		if evt.Err != nil {
			detail = evt.Err.Error()
		}
		if websocket.CloseStatus(evt.Err) == dgw.CloseStatusUnauthorized {
			b.emitConnection(StateError, detail, true)
			goSafe(b.log, "invalidate_session", b.invalidateSession)
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
	goSafe(b.log, "start_e2ee", func() { b.startE2EE(connCtx, client) })
	goSafe(b.log, "sync_threads", func() { b.syncThreads(connCtx, client, firstReady) })
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
