package main

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"go.mau.fi/mautrix-meta/pkg/messagix"
	"go.mau.fi/mautrix-meta/pkg/messagix/cookies"
	"go.mau.fi/mautrix-meta/pkg/messagix/httpclient"
	"go.mau.fi/mautrix-meta/pkg/messagix/types"
)

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
	b.writeSessionLocked(false)
}

func (b *MetaBackend) writeSessionLocked(onlyChanged bool) {
	if b.sess != nil {
		b.writeSecretJSON(sessionFile, b.sess, &b.sessionHash, onlyChanged)
	}
	if b.cookies != nil {
		b.writeSecretJSON(cookiesFile, b.cookies, &b.cookiesHash, onlyChanged)
	}
}

func (b *MetaBackend) writeSecretJSON(name string, value any, last *[32]byte, onlyChanged bool) {
	data, err := json.Marshal(value)
	if err != nil {
		return
	}
	sum := sha256.Sum256(data)
	if onlyChanged && sum == *last {
		return
	}
	if err = writeSecretFile(b.path(name), data); err != nil {
		b.log.Err(err).Str("file", name).Msg("Failed to save session file")
		return
	}
	*last = sum
}

func (b *MetaBackend) forgetSessionHashesLocked() {
	b.sessionHash = [32]byte{}
	b.cookiesHash = [32]byte{}
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
		b.writeSessionLocked(true)
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
	b.presenceLS.Store(0)
	b.presenceE2EE.Store(0)
	b.clearReadSent()
	b.mu.Lock()
	b.historyBackup = nil
	b.mu.Unlock()
	if err := os.RemoveAll(b.cachePath(mediaDirName)); err != nil {
		b.log.Warn().Err(err).Msg("Failed to delete media cache")
	}
	if err := os.RemoveAll(b.cachePath(avatarDirName)); err != nil {
		b.log.Warn().Err(err).Msg("Failed to delete avatar cache")
	}
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
	b.forgetSessionHashesLocked()
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
	b.forgetSessionHashesLocked()
	b.mu.Unlock()
	b.endLife(true)
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

func (b *MetaBackend) invalidateSession() {
	b.endLife(true)
	b.teardown(false)
	b.mu.Lock()
	b.cookies = nil
	b.pendingTable = nil
	b.cookiesHash = [32]byte{}
	b.mu.Unlock()
	_ = os.Remove(b.path(cookiesFile))
	_ = os.Remove(b.path(stateFile))
	b.log.Warn().Msg("Session invalidated, a new login is required")
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
