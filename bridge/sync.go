package main

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"time"

	"go.mau.fi/mautrix-meta/pkg/messagix"
	"go.mau.fi/mautrix-meta/pkg/messagix/graphql"
	"go.mau.fi/mautrix-meta/pkg/messagix/socket"
	"go.mau.fi/mautrix-meta/pkg/messagix/table"
)

const (
	syncGroupInbox        = 1
	syncGroupE2EE         = 95
	threadRefreshTimeout  = 25 * time.Second
	maxInboxThreadPages   = 5
	maxE2EEThreadPages    = 10
	contactBatchSize      = 10
	maxContactsPerSync    = 200
	contactFetchTimeout   = 30 * time.Second
	firstPageActivityRef  = 9999999999999
	threadPageDelay       = 500 * time.Millisecond
	targetInboxThreadSize = 100
	threadRangeWait       = 20 * time.Second
	heavySyncInterval     = 30 * time.Minute
)

func (b *MetaBackend) syncThreads(ctx context.Context, client *messagix.Client, first bool) {
	if !b.syncing.CompareAndSwap(false, true) {
		if first {
			b.out.Emit(EventThreads, ThreadsEvent{Threads: b.store.ListThreads(0)})
			b.out.Emit(EventReady, ReadyEvent{User: b.currentUser()})
		}
		return
	}
	defer b.syncing.Store(false)
	refreshCtx, cancel := context.WithTimeout(ctx, threadRefreshTimeout)
	b.refreshThreads(refreshCtx, client)
	cancel()
	if ctx.Err() != nil || !b.isCurrent(client) {
		return
	}
	if first {
		b.out.Emit(EventThreads, ThreadsEvent{Threads: b.store.ListThreads(0)})
		b.out.Emit(EventReady, ReadyEvent{User: b.currentUser()})
		b.paginateThreads(ctx, client, syncGroupInbox, maxInboxThreadPages, func() bool {
			return b.store.ThreadCount() < targetInboxThreadSize
		})
		b.paginateE2EEThreads(ctx, client, maxE2EEThreadPages)
	} else {
		goSafe(b.log, "stale_catch_up", func() {
			if sleepCtx(ctx, staleCatchUpDelay) {
				b.catchUpStaleThreads()
			}
		})
	}
	if !b.shouldRunHeavySync(first) {
		return
	}
	b.resolveMissingContacts(ctx, client)
	b.refreshExpiringAvatars(ctx)
	if b.historyEnrolled() && ctx.Err() == nil {
		if _, svc, err := b.historyClient(); err == nil {
			b.applyHistory(ctx, svc, b.e2eeThreads())
		}
	}
	if ctx.Err() == nil {
		b.logThreadSummary(first)
	}
}

type messagixStateView struct {
	Configs struct {
		VersionID          int64
		BrowserConfigTable struct {
			LSPlatformMessengerSyncParams struct {
				Contact string `json:"contact"`
			}
		}
	}
}

func messagixSyncConfig(client *messagix.Client) (int64, string, error) {
	raw, err := client.DumpState()
	if err != nil {
		return 0, "", err
	} else if raw == nil {
		return 0, "", errors.New("messagix state not available yet")
	}
	return parseMessagixSyncConfig(raw)
}

func parseMessagixSyncConfig(raw []byte) (int64, string, error) {
	var view messagixStateView
	if err := json.Unmarshal(raw, &view); err != nil {
		return 0, "", err
	} else if view.Configs.VersionID == 0 {
		return 0, "", errors.New("messagix version id unknown")
	}
	return view.Configs.VersionID, view.Configs.BrowserConfigTable.LSPlatformMessengerSyncParams.Contact, nil
}

func (b *MetaBackend) syncE2EEThreadList(ctx context.Context, client *messagix.Client) error {
	version, params, err := messagixSyncConfig(client)
	if err != nil {
		return err
	}
	vars := &graphql.LSPlatformGraphQLLightspeedVariables{Database: syncGroupE2EE, Version: version}
	if params != "" {
		vars.SyncParams = params
	}
	tbl, err := client.GetHTTP().MakeLSRequest(ctx, vars, 1)
	if err != nil {
		return err
	}
	b.log.Debug().
		Int("thread_rows", len(tbl.LSUpdateOrInsertThread)+len(tbl.LSDeleteThenInsertThread)).
		Int("hybrid_rows", len(tbl.LSVerifyHybridThreadExists)).
		Msg("Synced encrypted thread list")
	b.handleTable(tbl, modeHistory)
	client.PostHandlePublishResponse(tbl)
	return nil
}

func (b *MetaBackend) paginateE2EEThreads(ctx context.Context, client *messagix.Client, maxPages int) {
	for page := 0; page < maxPages; page++ {
		keyStore := client.GetSyncGroupKeyStore(syncGroupE2EE)
		if keyStore == nil || !keyStore.HasMoreBefore {
			return
		}
		prevMin, prevActivity := keyStore.MinThreadKey, keyStore.MinLastActivityTimestampMs
		gen := client.ThreadRangeGeneration(syncGroupE2EE)
		_, tbl, err := client.FetchMoreThreads(ctx, syncGroupE2EE)
		if err != nil {
			b.log.Warn().Err(err).Msg("Failed to fetch more encrypted threads")
			return
		}
		if tbl != nil {
			b.handleTable(tbl, modeHistory)
		}
		waitCtx, cancel := context.WithTimeout(ctx, threadRangeWait)
		err = client.WaitForThreadRange(waitCtx, syncGroupE2EE, gen)
		cancel()
		keyStore = client.GetSyncGroupKeyStore(syncGroupE2EE)
		b.log.Debug().Err(err).Int("page", page).Bool("more", keyStore != nil && keyStore.HasMoreBefore).Msg("Encrypted thread page")
		if err != nil || keyStore == nil || (keyStore.MinThreadKey == prevMin && keyStore.MinLastActivityTimestampMs == prevActivity) {
			return
		}
		if !sleepCtx(ctx, threadPageDelay) {
			return
		}
	}
}

func (b *MetaBackend) shouldRunHeavySync(first bool) bool {
	now := time.Now().UnixMilli()
	last := b.lastHeavySync.Load()
	if !first && last != 0 && now-last < heavySyncInterval.Milliseconds() {
		return false
	}
	return b.lastHeavySync.CompareAndSwap(last, now)
}

func (b *MetaBackend) refreshThreads(ctx context.Context, client *messagix.Client) {
	if err := b.syncE2EEThreadList(ctx, client); err != nil {
		b.log.Warn().Err(err).Msg("Failed to sync encrypted thread list")
	}
	for _, syncGroup := range []int{syncGroupInbox} {
		task := &socket.FetchThreadsTask{
			ParentThreadKey:            -1,
			ReferenceThreadKey:         0,
			ReferenceActivityTimestamp: firstPageActivityRef,
			SyncGroup:                  syncGroup,
		}
		if syncGroup == syncGroupInbox {
			task.Cursor = client.GetCursor(syncGroupInbox)
		}
		tbl, err := client.ExecuteTasks(ctx, task)
		if err != nil {
			b.log.Warn().Err(err).Int("sync_group", syncGroup).Msg("Failed to refresh thread list")
			continue
		}
		if tbl == nil {
			continue
		}
		b.log.Debug().
			Int("sync_group", syncGroup).
			Int("thread_rows", len(tbl.LSDeleteThenInsertThread)+len(tbl.LSUpdateOrInsertThread)).
			Int("thread_ranges", len(tbl.LSUpsertSyncGroupThreadsRange)).
			Msg("Refreshed first page of threads")
		b.handleTable(tbl, modeHistory)
		client.PostHandlePublishResponse(tbl)
	}
}

func (b *MetaBackend) paginateThreads(ctx context.Context, client *messagix.Client, syncGroup int64, maxPages int, wantMore func() bool) {
	var prevMin int64
	for page := 0; page < maxPages; page++ {
		if wantMore != nil && !wantMore() {
			return
		}
		keyStore, tbl, err := client.FetchMoreThreads(ctx, syncGroup)
		if err != nil {
			b.log.Warn().Err(err).Int64("sync_group", syncGroup).Msg("Failed to fetch more threads")
			return
		} else if tbl == nil {
			return
		}
		b.handleTable(tbl, modeHistory)
		if keyStore == nil || !keyStore.HasMoreBefore || keyStore.MinThreadKey == prevMin {
			return
		}
		prevMin = keyStore.MinThreadKey
		if !sleepCtx(ctx, threadPageDelay) {
			return
		}
	}
}

func (b *MetaBackend) resolveMissingContacts(ctx context.Context, client *messagix.Client) {
	var missing []string
	for _, id := range b.store.Stats().ContactsMissing {
		if _, requested := b.contactsRequested.Load(id); !requested {
			missing = append(missing, id)
		}
	}
	if len(missing) > maxContactsPerSync {
		missing = missing[:maxContactsPerSync]
	}
	b.fetchContacts(ctx, client, missing)
}

func (b *MetaBackend) fetchContacts(ctx context.Context, client *messagix.Client, ids []string) {
	for start := 0; start < len(ids); start += contactBatchSize {
		end := min(start+contactBatchSize, len(ids))
		tasks := make([]socket.Task, 0, end-start)
		for _, id := range ids[start:end] {
			contactID, err := strconv.ParseInt(id, 10, 64)
			if err != nil || contactID <= 0 {
				continue
			}
			b.contactsRequested.Store(id, true)
			tasks = append(tasks, &socket.GetContactsFullTask{ContactID: contactID})
		}
		if len(tasks) == 0 {
			continue
		}
		tbl, err := client.ExecuteTasks(ctx, tasks...)
		if err != nil {
			b.log.Warn().Err(err).Int("count", len(tasks)).Msg("Failed to fetch contacts")
			return
		}
		b.handleTable(contactsOnly(tbl), modeHistory)
	}
}

func contactsOnly(tbl *table.LSTable) *table.LSTable {
	if tbl == nil {
		return nil
	}
	return &table.LSTable{
		LSDeleteThenInsertContact: tbl.LSDeleteThenInsertContact,
		LSVerifyContactRowExists:  tbl.LSVerifyContactRowExists,
	}
}

func (b *MetaBackend) ensureContact(id string) {
	if id == "" || id == b.store.SelfID() {
		return
	}
	if c, ok := b.store.Contact(id); ok && c.Name != "" {
		return
	}
	if _, already := b.contactsRequested.LoadOrStore(id, true); already {
		return
	}
	client := b.currentClient()
	if client == nil {
		b.contactsRequested.Delete(id)
		return
	}
	life := b.lifeCtx()
	goSafe(b.log, "fetch_contact", func() {
		ctx, cancel := context.WithTimeout(life, contactFetchTimeout)
		defer cancel()
		b.fetchContacts(ctx, client, []string{id})
	})
}

func (b *MetaBackend) logThreadSummary(initial bool) {
	st := b.store.Stats()
	byType := make(map[string]int, len(st.ByType))
	for tt, n := range st.ByType {
		byType[strconv.FormatInt(tt, 10)] = n
	}
	known, online := b.store.PresenceStats()
	b.log.Info().
		Bool("initial", initial).
		Int("presence_known", known).
		Int("presence_online", online).
		Int64("presence_updates_ls", b.presenceLS.Load()).
		Int64("presence_updates_e2ee", b.presenceE2EE.Load()).
		Any("threads_by_type", byType).
		Int("one_to_one", st.OneToOne).
		Int("one_to_one_unnamed", st.OneToOneNoName).
		Int("e2ee_one_to_one", st.E2EEOneToOne).
		Int("e2ee_one_to_one_unnamed", st.E2EENoName).
		Int("e2ee_one_to_one_without_activity", st.E2EENoActivity).
		Msg("Thread sync summary")
}
