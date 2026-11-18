package main

import (
	"strconv"
	"strings"
	"time"

	"go.mau.fi/mautrix-meta/pkg/messagix/table"
)

const lsPresenceActive = 2

func isChatThreadType(tt table.ThreadType) bool {
	switch tt {
	case table.FOLDER, table.COMMUNITY_FOLDER, table.COMMUNITY_CHANNEL_CATEGORY:
		return false
	}
	return tt < 100
}

func itoa(v int64) string {
	return strconv.FormatInt(v, 10)
}

type tableContext struct {
	b             *MetaBackend
	mode          tableMode
	changed       map[string]bool
	authoritative map[string]bool
	active        map[int64]bool
	upserted      map[string]bool
	historyMsgs   map[string]bool
}

func (tc *tableContext) threadID(key int64) string {
	return itoa(tc.b.store.MapThreadKey(key))
}

func (tc *tableContext) touch(id string) {
	tc.changed[id] = true
}

func (b *MetaBackend) handleTable(tbl *table.LSTable, mode tableMode) {
	if tbl == nil {
		return
	}
	b.sessionMu.RLock()
	defer b.sessionMu.RUnlock()
	if b.closed.Load() {
		return
	}
	tc := &tableContext{
		b:             b,
		mode:          mode,
		changed:       make(map[string]bool),
		authoritative: make(map[string]bool),
		active:        make(map[int64]bool),
		upserted:      make(map[string]bool),
		historyMsgs:   make(map[string]bool),
	}
	tc.collectActive(tbl)
	tc.handleHybrid(tbl)
	tc.handleContacts(tbl)
	tc.handleThreads(tbl)
	tc.handleParticipants(tbl)
	tc.handleV11(tbl)
	tc.handleThreadDeletes(tbl)
	tc.handleMessages(tbl)
	tc.handlePollSnapshots(tbl)
	tc.handleReactions(tbl)
	tc.handlePresence(tbl)
	tc.handleThreadMeta(tbl)
	tc.handleReceipts(tbl)
	tc.handleEphemeral(tbl)

	ids := make([]string, 0, len(tc.changed))
	for id := range tc.changed {
		ids = append(ids, id)
	}
	if len(ids) > 0 {
		if threads := b.store.ThreadViews(ids); len(threads) > 0 {
			b.out.Emit(EventThreads, ThreadsEvent{Threads: threads})
		}
	}
	for id := range tc.upserted {
		b.notifyFetchWaiters(id)
	}
}

func (tc *tableContext) collectActive(tbl *table.LSTable) {
	for _, t := range tbl.LSVerifyThreadExists {
		tc.active[t.ThreadKey] = true
	}
	for _, t := range tbl.LSUpdateOrInsertThread {
		tc.active[t.ThreadKey] = true
	}
	for _, t := range tbl.LSDeleteThenInsertThread {
		tc.active[t.ThreadKey] = true
	}
	for _, r := range tbl.LSInsertNewMessageRange {
		tc.active[r.ThreadKey] = true
	}
}

func (tc *tableContext) handleHybrid(tbl *table.LSTable) {
	store := tc.b.store
	for _, m := range tbl.LSUpdateThreadAuthorityAndMappingWithOTIDFromJID {
		if m.ThreadKey != 0 && m.ThreadJID != 0 {
			store.SetHybrid(m.ThreadKey, m.ThreadJID)
		}
	}
	for _, h := range tbl.LSVerifyHybridThreadExists {
		if h.ThreadKey == 0 || h.ThreadJID == 0 {
			continue
		}
		store.SetHybrid(h.ThreadKey, h.ThreadJID)
		waID := itoa(h.ThreadJID)
		if h.ThreadKey != h.ThreadJID {
			fbID := itoa(h.ThreadKey)
			if store.RemoveThread(fbID) {
				tc.b.out.Emit(EventThreadRemoved, ThreadRemovedEvent{ThreadID: fbID})
			}
		}
		store.UpdateThread(waID, func(rec *threadRec) {
			rec.E2EE = true
			if h.ThreadType != table.UNKNOWN_THREAD_TYPE {
				rec.ThreadType = int64(h.ThreadType)
				rec.IsGroup = !h.ThreadType.IsOneToOne()
			} else {
				rec.IsGroup = h.IsGroupThread
			}
			if !rec.IsGroup && rec.PeerID == "" {
				rec.PeerID = waID
			}
			if h.LastActivityTimestampMS > rec.LastActivityMS {
				rec.LastActivityMS = h.LastActivityTimestampMS
			}
			if h.LastReadWatermarkTimestampMS > rec.LastReadMS {
				rec.LastReadMS = h.LastReadWatermarkTimestampMS
			}
			rec.MetaReadMS = max(rec.MetaReadMS, h.LastReadWatermarkTimestampMS)
			rec.MarkedReadMS = min(rec.MarkedReadMS, h.LastReadWatermarkTimestampMS)
			if rec.LastActivityMS <= rec.LastReadMS {
				rec.UnreadCount = 0
			} else if rec.UnreadCount == 0 {
				rec.UnreadCount = 1
			}
		})
		tc.touch(waID)
	}
}

func (tc *tableContext) contactChanged(id string) {
	for _, threadID := range tc.b.store.ThreadsReferencingContact(id) {
		tc.touch(threadID)
	}
}

func (tc *tableContext) handleContacts(tbl *table.LSTable) {
	store := tc.b.store
	for _, c := range tbl.LSDeleteThenInsertContact {
		id := itoa(c.Id)
		if store.SetContact(id, c.Name, c.FirstName, c.ProfilePictureUrl) {
			tc.contactChanged(id)
		}
	}
	for _, c := range tbl.LSVerifyContactRowExists {
		id := itoa(c.ContactId)
		if store.SetContact(id, c.Name, c.FirstName, c.ProfilePictureUrl) {
			tc.contactChanged(id)
		}
	}
}

type threadFields struct {
	key           int64
	threadType    table.ThreadType
	name          string
	pictureURL    string
	snippet       string
	lastActivity  int64
	lastRead      int64
	unread        int64
	folder        string
	hasUnreadInfo bool
	muteExpire    int64
	customEmoji   string
	bubbleColor   int64
	themeID       int64
}

func (tc *tableContext) applyThread(f threadFields) {
	if !isChatThreadType(f.threadType) {
		return
	}
	mapped := tc.b.store.MapThreadKey(f.key)
	id := itoa(mapped)
	legacy := mapped != f.key && !f.threadType.IsWhatsApp()
	tc.b.store.UpdateThread(id, func(rec *threadRec) {
		fresh := f.lastActivity >= rec.LastActivityMS
		if !legacy || rec.Name == "" {
			rec.Name = f.name
		}
		if f.pictureURL != "" && (!legacy || rec.AvatarURL == "") {
			rec.AvatarURL = f.pictureURL
		}
		if f.snippet != "" && ((fresh && !legacy) || rec.Snippet == "") {
			rec.Snippet = f.snippet
			if fresh && !legacy {
				rec.SnippetMS = max(rec.SnippetMS, f.lastActivity)
			}
		}
		rec.LastActivityMS = max(rec.LastActivityMS, f.lastActivity)
		rec.LastReadMS = max(rec.LastReadMS, f.lastRead)
		if !legacy {
			rec.MetaReadMS = max(rec.MetaReadMS, f.lastRead)
			rec.MarkedReadMS = min(rec.MarkedReadMS, f.lastRead)
		}
		if folder := lsFolder(f.folder); legacy && folder == FolderRequests {
			rec.Folder = folder
		} else if legacy && folder == FolderInbox && rec.Folder == FolderRequests {
			rec.Folder = folder
		}
		if !legacy {
			rec.MuteExpireMS = f.muteExpire
			if folder := lsFolder(f.folder); folder != "" {
				rec.Folder = folder
			}
			if f.customEmoji != "" || f.bubbleColor != 0 || f.themeID != 0 {
				rec.QuickEmoji = f.customEmoji
				rec.ThemeColor = colorHex(f.bubbleColor)
				rec.ThemeID = f.themeID
			}
		}
		if !legacy || rec.ThreadType == 0 {
			rec.ThreadType = int64(f.threadType)
		}
		if f.threadType != table.UNKNOWN_THREAD_TYPE && (!legacy || rec.ThreadType == int64(f.threadType)) {
			rec.IsGroup = !f.threadType.IsOneToOne()
		}
		rec.E2EE = rec.E2EE || mapped != f.key || f.threadType.IsWhatsApp()
		if !rec.IsGroup {
			rec.PeerID = id
		}
		switch {
		case rec.LastActivityMS <= rec.LastReadMS:
			rec.UnreadCount = 0
		case f.hasUnreadInfo && f.unread > 0 && !legacy:
			rec.UnreadCount = int(f.unread)
		default:
			rec.UnreadCount = max(rec.UnreadCount, 1)
		}
	})
	if !legacy {
		tc.authoritative[id] = true
	}
	tc.touch(id)
}

func (tc *tableContext) logThreadRows(tbl *table.LSTable) {
	if len(tbl.LSDeleteThenInsertThread)+len(tbl.LSUpdateOrInsertThread)+len(tbl.LSVerifyHybridThreadExists) == 0 {
		return
	}
	rows := make(map[string]int)
	for _, t := range tbl.LSDeleteThenInsertThread {
		rows["dti/type"+itoa(int64(t.ThreadType))+"/sg"+itoa(t.SyncGroup)]++
	}
	for _, t := range tbl.LSUpdateOrInsertThread {
		rows["uoi/type"+itoa(int64(t.ThreadType))+"/sg"+itoa(t.SyncGroup)]++
	}
	evt := tc.b.log.Debug().
		Any("thread_rows", rows).
		Int("hybrid_rows", len(tbl.LSVerifyHybridThreadExists)).
		Int("hybrid_mappings", len(tbl.LSUpdateThreadAuthorityAndMappingWithOTIDFromJID)).
		Int("contacts", len(tbl.LSDeleteThenInsertContact)+len(tbl.LSVerifyContactRowExists)).
		Int("thread_ranges", len(tbl.LSUpsertSyncGroupThreadsRange))
	evt.Msg("Thread rows in table")
}

func (tc *tableContext) handleThreads(tbl *table.LSTable) {
	tc.logThreadRows(tbl)
	for _, t := range tbl.LSDeleteThenInsertThread {
		tc.applyThread(threadFields{
			key:           t.ThreadKey,
			threadType:    t.ThreadType,
			name:          t.ThreadName,
			pictureURL:    t.ThreadPictureUrl,
			snippet:       t.Snippet,
			lastActivity:  t.LastActivityTimestampMs,
			lastRead:      t.LastReadWatermarkTimestampMs,
			unread:        t.UnreadMessageCount,
			folder:        t.FolderName,
			hasUnreadInfo: true,
			muteExpire:    t.MuteExpireTimeMs,
			customEmoji:   t.CustomEmoji,
			bubbleColor:   t.OutgoingBubbleColor,
			themeID:       t.ThemeFbid,
		})
	}
	for _, t := range tbl.LSUpdateOrInsertThread {
		tc.applyThread(threadFields{
			key:          t.ThreadKey,
			threadType:   t.ThreadType,
			name:         t.ThreadName,
			pictureURL:   t.ThreadPictureUrl,
			snippet:      t.Snippet,
			lastActivity: t.LastActivityTimestampMs,
			lastRead:     t.LastReadWatermarkTimestampMs,
			folder:       t.FolderName,
			muteExpire:   t.MuteExpireTimeMs,
		})
	}
	for _, t := range tbl.LSVerifyThreadExists {
		if !isChatThreadType(t.ThreadType) {
			continue
		}
		id := tc.threadID(t.ThreadKey)
		if tc.b.store.HasThread(id) {
			continue
		}
		tc.b.store.UpdateThread(id, func(rec *threadRec) {
			rec.ThreadType = int64(t.ThreadType)
			rec.IsGroup = !t.ThreadType.IsOneToOne()
			rec.E2EE = t.ThreadType.IsWhatsApp() || id != itoa(t.ThreadKey)
			rec.Folder = lsFolder(t.FolderName)
			if !rec.IsGroup {
				rec.PeerID = id
			}
		})
		tc.touch(id)
	}
}

func (tc *tableContext) handleParticipants(tbl *table.LSTable) {
	store := tc.b.store
	for _, p := range tbl.LSAddParticipantIdToGroupThread {
		id := tc.threadID(p.ThreadKey)
		if !store.HasThread(id) {
			continue
		}
		member := itoa(p.ContactId)
		self := store.SelfID()
		store.UpdateThread(id, func(rec *threadRec) {
			if rec.Members == nil {
				rec.Members = make(map[string]bool)
			}
			rec.Members[member] = true
			if member == self {
				rec.Left = false
			}
		})
		store.SetNickname(id, member, p.Nickname)
		tc.touch(id)
	}
	for _, p := range tbl.LSVerifyContactParticipantExist {
		id := tc.threadID(p.ThreadKey)
		member := itoa(p.ContactID)
		if p.Name == "" || !store.HasThread(id) {
			continue
		}
		if c, ok := store.Contact(member); ok && c.Name != "" && c.Name != p.Name && store.SetNickname(id, member, p.Name) {
			tc.touch(id)
		}
	}
	self := store.SelfID()
	for _, p := range tbl.LSRemoveParticipantFromThread {
		id := tc.threadID(p.ThreadKey)
		member := itoa(p.ParticipantId)
		if member == self {
			if rec, ok := store.Thread(id); ok && rec.IsGroup && (rec.Left || rec.Leaving) {
				store.UpdateExistingThread(id, func(rec *threadRec) { markLeft(rec, self) })
				tc.touch(id)
				continue
			}
			if !tc.active[p.ThreadKey] {
				tc.removeThread(id)
				continue
			}
		}
		if !store.HasThread(id) {
			continue
		}
		store.UpdateThread(id, func(rec *threadRec) {
			delete(rec.Members, member)
		})
		tc.touch(id)
	}
}

func (tc *tableContext) removeThread(id string) {
	if tc.b.store.RemoveThread(id) {
		delete(tc.changed, id)
		tc.b.out.Emit(EventThreadRemoved, ThreadRemovedEvent{ThreadID: id})
	}
}

func (tc *tableContext) handleThreadDeletes(tbl *table.LSTable) {
	for _, d := range tbl.LSDeleteThread {
		if !tc.active[d.ThreadKey] {
			tc.removeThread(tc.threadID(d.ThreadKey))
		}
	}
	for _, d := range tbl.LSDeletePartialThread {
		if !tc.active[d.ThreadKey] {
			tc.removeThread(tc.threadID(d.ThreadKey))
		}
	}
	for _, d := range tbl.LSDeleteMessageRequest {
		if !tc.active[d.ThreadKey] {
			tc.removeThread(tc.threadID(d.ThreadKey))
		}
	}
}

func messageChanged(a, b Message) bool {
	return a.Text != b.Text || a.Deleted != b.Deleted || a.Edited != b.Edited || len(a.Attachments) != len(b.Attachments)
}

func (tc *tableContext) handleMessages(tbl *table.LSTable) {
	b := tc.b
	store := b.store
	upsert, insert := tbl.WrapMessages()
	for key, um := range upsert {
		id := tc.threadID(key)
		for _, wm := range um.Messages {
			if wm.LSInsertMessage == nil || wm.MessageId == "" || tc.applyPoll(id, wm) {
				continue
			}
			msg := b.convertMessage(id, wm)
			if store.HasThread(id) {
				b.applyEphemeral(id, wm)
			}
			tc.historyMsgs[msg.ID] = true
			prev, existed := store.PutMessage(msg)
			if existed && messageChanged(prev, msg) {
				b.emitMessage(EventMessageUpdate, msg)
			}
		}
		if um.Range != nil {
			oldest, ok := store.OldestMessage(id)
			if !ok || um.Range.MinTimestampMs <= oldest.TimestampMS || len(um.Messages) == 0 {
				store.SetHasMoreBefore(id, um.Range.HasMoreBefore)
			}
		}
		tc.upserted[id] = true
	}
	for _, wm := range insert {
		if wm.LSInsertMessage == nil || wm.MessageId == "" || wm.MessageId == wm.OfflineThreadingId {
			continue
		}
		id := tc.threadID(wm.ThreadKey)
		if tc.applyPoll(id, wm) {
			continue
		}
		msg := b.convertMessage(id, wm)
		if store.HasThread(id) {
			b.applyEphemeral(id, wm)
		}
		prev, existed := store.PutMessage(msg)
		if existed {
			if messageChanged(prev, msg) {
				b.emitMessage(EventMessageUpdate, msg)
			}
			continue
		}
		if tc.mode == modeLive {
			live := isLiveTimestamp(msg.TimestampMS) && !b.lsCatchingUp.Load()
			if !tc.authoritative[id] {
				if live {
					b.applyMessageToThread(msg)
				} else {
					b.applyHistoryToThread(msg)
				}
				tc.touch(id)
			}
			if live {
				b.emitMessage(EventMessage, msg)
			}
		}
	}
	for _, edit := range tbl.LSEditMessage {
		text := edit.Text
		if updated, ok := store.UpdateMessage(edit.MessageID, func(m *Message) bool {
			if m.Text == text && m.Edited {
				return false
			}
			if m.Text != text {
				m.Mentions = nil
			}
			m.Text = text
			m.Edited = true
			return true
		}); ok {
			b.emitMessage(EventMessageUpdate, updated)
		}
	}
	for _, del := range tbl.LSDeleteMessage {
		tc.removeMessage(del.MessageId)
	}
	for _, del := range tbl.LSDeleteThenInsertMessage {
		if del.IsUnsent {
			tc.markDeleted(del.MessageId)
		}
	}
}

func (tc *tableContext) markDeleted(messageID string) {
	if updated, ok := tc.b.store.UpdateMessage(messageID, func(m *Message) bool {
		if m.Deleted {
			return false
		}
		m.Deleted = true
		m.Text = ""
		m.Attachments = []Attachment{}
		return true
	}); ok {
		tc.b.emitMessage(EventMessageUpdate, updated)
	}
}

func (tc *tableContext) reactionChanged(messageID string) {
	if tc.mode != modeLive || tc.historyMsgs[messageID] {
		return
	}
	if msg, ok := tc.b.store.FindMessage(messageID); ok {
		tc.b.emitMessage(EventMessageUpdate, msg)
	}
}

func (tc *tableContext) handleReactions(tbl *table.LSTable) {
	store := tc.b.store
	changed := make(map[string]bool)
	var added []ReactionAddedEvent
	for _, r := range tbl.LSUpsertReaction {
		actor := itoa(r.ActorId)
		prev := store.ReactionOf(r.MessageId, actor)
		if store.SetReaction(r.MessageId, actor, r.Reaction, r.TimestampMs) {
			changed[r.MessageId] = true
			if tc.liveReaction(r.MessageId, r.TimestampMs) {
				if evt, ok := store.reactionAddedEvent(prev, r.MessageId, actor, r.Reaction); ok {
					added = append(added, evt)
				}
			}
		}
	}
	for _, r := range tbl.LSDeleteReaction {
		if store.SetReaction(r.MessageId, itoa(r.ActorId), "", 0) {
			changed[r.MessageId] = true
		}
	}
	for _, r := range tbl.LSUpdateOrInsertReactionV2 {
		agg := &reactionAggregate{Emoji: r.ReactionLiteral, Count: int(r.Count), Mine: r.ViewerIsReactor}
		if r.Count <= 0 || r.ReactionLiteral == "" {
			agg = nil
		}
		if store.SetReactionV2(r.MessageID, itoa(r.ReactionFBID), agg) {
			changed[r.MessageID] = true
		}
	}
	for _, r := range tbl.LSDeleteReactionV2 {
		if store.SetReactionV2(r.MessageID, itoa(r.ReactionFBID), nil) {
			changed[r.MessageID] = true
		}
	}
	for messageID := range changed {
		tc.reactionChanged(messageID)
	}
	for _, evt := range added {
		tc.b.out.Emit(EventReactionAdded, evt)
	}
}

func (tc *tableContext) handlePresence(tbl *table.LSTable) {
	store := tc.b.store
	for _, t := range tbl.LSTruncatePresenceDatabase {
		if t.ShouldTruncate {
			for _, id := range store.ClearPresence() {
				tc.touch(id)
			}
		}
	}
	now := time.Now().UnixMilli()
	for _, p := range tbl.LSDeleteThenInsertContactPresence {
		user := itoa(p.ContactId)
		if user == store.SelfID() {
			continue
		}
		online := p.Status == lsPresenceActive && (p.ExpirationTimestampMs == 0 || p.ExpirationTimestampMs > now)
		if store.SetPresence(user, online, p.LastActiveTimestampMs, p.ExpirationTimestampMs) {
			for _, id := range store.ThreadsWithUser(user) {
				tc.touch(id)
			}
		}
		if tc.mode == modeLive {
			tc.b.out.Emit(EventPresence, PresenceEvent{UserID: user, Online: online, LastSeenMS: p.LastActiveTimestampMs})
		}
	}
}

func (tc *tableContext) handleThreadMeta(tbl *table.LSTable) {
	store := tc.b.store
	for _, s := range tbl.LSUpdateThreadSnippet {
		id := tc.threadID(s.ThreadKey)
		if !store.HasThread(id) {
			continue
		}
		store.UpdateThread(id, func(rec *threadRec) { rec.Snippet = s.Snippet })
		tc.touch(id)
	}
	for _, n := range tbl.LSSyncUpdateThreadName {
		id := tc.threadID(n.ThreadKey)
		if !store.HasThread(id) {
			continue
		}
		store.UpdateThread(id, func(rec *threadRec) {
			if rec.IsGroup {
				rec.Name = n.ThreadName
			}
		})
		tc.touch(id)
	}
	for _, m := range tbl.LSUpdateThreadMuteSetting {
		id := tc.threadID(m.ThreadKey)
		if !store.HasThread(id) {
			continue
		}
		store.UpdateThread(id, func(rec *threadRec) { rec.MuteExpireMS = m.MuteExpireTimeMS })
		tc.touch(id)
	}
	for _, img := range tbl.LSSetThreadImageURL {
		id := tc.threadID(img.ThreadKey)
		if !store.HasThread(id) {
			continue
		}
		store.UpdateThread(id, func(rec *threadRec) { rec.AvatarURL = img.ImageURL })
		tc.touch(id)
	}
	self := store.SelfID()
	markRead := func(key, watermark int64) {
		id := tc.threadID(key)
		if store.HasThread(id) {
			store.UpdateThread(id, func(rec *threadRec) {
				rec.LastReadMS = max(rec.LastReadMS, watermark)
				rec.MetaReadMS = max(rec.MetaReadMS, watermark)
				rec.MarkedReadMS = min(rec.MarkedReadMS, watermark)
				if rec.LastReadMS >= rec.LastActivityMS {
					rec.UnreadCount = 0
				}
			})
			tc.touch(id)
		}
		if tc.mode == modeLive && self != "" {
			tc.b.out.Emit(EventRead, ReadEvent{ThreadID: id, UserID: self, TimestampMS: watermark})
		}
	}
	for _, r := range tbl.LSMarkThreadRead {
		markRead(r.ThreadKey, r.LastReadWatermarkTimestampMs)
	}
	for _, r := range tbl.LSMarkThreadReadV2 {
		markRead(r.ThreadKey, r.LastReadWatermarkTimestampMs)
	}
}

func (tc *tableContext) handleReceipts(tbl *table.LSTable) {
	store := tc.b.store
	self := store.SelfID()
	for _, r := range tbl.LSUpdateReadReceipt {
		user := itoa(r.ContactId)
		if user == self {
			continue
		}
		id := tc.threadID(r.ThreadKey)
		if store.SetReadReceipt(id, user, r.ReadWatermarkTimestampMs) && store.HasThread(id) {
			tc.touch(id)
		}
		if tc.mode == modeLive {
			tc.b.out.Emit(EventRead, ReadEvent{ThreadID: id, UserID: user, TimestampMS: r.ReadWatermarkTimestampMs})
		}
	}
	for _, p := range tbl.LSAddParticipantIdToGroupThread {
		user := itoa(p.ContactId)
		if user == self {
			continue
		}
		id := tc.threadID(p.ThreadKey)
		if store.SetReadReceipt(id, user, p.ReadWatermarkTimestampMs) && store.HasThread(id) {
			tc.touch(id)
		}
	}
}

func (tc *tableContext) handleEphemeral(tbl *table.LSTable) {
	if tc.mode != modeLive {
		return
	}
	self := tc.b.store.SelfID()
	for _, t := range tbl.LSUpdateTypingIndicator {
		user := itoa(t.SenderId)
		if user == self {
			continue
		}
		tc.b.out.Emit(EventTyping, TypingEvent{ThreadID: tc.threadID(t.ThreadKey), UserID: user, Typing: t.IsTyping})
	}
}

func (b *MetaBackend) convertMessage(threadID string, wm *table.WrappedMessage) Message {
	sender := itoa(wm.SenderId)
	self := b.store.SelfID()
	msg := Message{
		ID:          wm.MessageId,
		ThreadID:    threadID,
		SenderID:    sender,
		SenderName:  b.store.ContactName(sender),
		Text:        wm.Text,
		TimestampMS: wm.TimestampMs,
		IsOutgoing:  sender == self,
		Attachments: []Attachment{},
		ReplyToID:   wm.ReplySourceId,
		Edited:      wm.EditCount > 0,
		Deleted:     wm.IsUnsent,
		Forwarded:   wm.IsForwarded,
		System:      wm.IsAdminMessage,
	}
	if wm.PollID != 0 && wm.PollEventType == table.PollEventCreate {
		msg.System = false
		msg.Text = ""
	}
	if msg.System {
		msg.Text = translateAdminText(wm.Text)
	}
	if msg.IsOutgoing && msg.SenderName == "" {
		msg.SenderName = b.currentUser().Name
	}
	msg.Mentions = lsMentions(wm.Text, wm.LSInsertMessage)
	if msg.IsOutgoing && wm.OfflineThreadingId != "" {
		if clientID, ok := b.pendingClientIDs.Load(wm.OfflineThreadingId); ok {
			msg.ClientID = clientID.(string)
		}
	}
	if wm.ReplySourceId != "" && (wm.ReplyMessageText != "" || wm.ReplyToUserId != 0 || wm.ReplyAttachmentType != 0) {
		replySender := itoa(wm.ReplyToUserId)
		preview := &ReplyPreview{ID: wm.ReplySourceId, SenderName: b.store.ContactName(replySender), Text: wm.ReplyMessageText}
		if replySender == self && preview.SenderName == "" {
			preview.SenderName = b.currentUser().Name
		}
		if wm.ReplyAttachmentType != 0 {
			preview.Kind = attachmentKind(wm.ReplyAttachmentType)
		}
		msg.ReplyTo = preview
	}
	if msg.Deleted {
		msg.Text = ""
		return msg
	}
	ids := make(map[string]bool)
	add := func(prefix string, index int, m lsMedia) {
		id := m.fbid
		if id == "" || ids[id] {
			id = prefix + strconv.Itoa(index)
		}
		ids[id] = true
		att, rec := m.convert(id)
		if att.Kind == "audio" {
			att.Waveform = parseWaveform(m.waveform)
			att.Voice = m.waveform != "" || strings.HasPrefix(strings.ToLower(m.fileName), "audioclip")
		}
		msg.Attachments = append(msg.Attachments, att)
		if rec.URL != "" {
			b.store.PutMedia(msg.ID, id, rec)
		}
	}
	for i, blob := range wm.BlobAttachments {
		add("blob_", i, lsMedia{
			kind: blob.AttachmentType, fbid: blob.AttachmentFbid, fileName: blob.Filename,
			playable: blob.PlayableUrl, playableMime: blob.PlayableUrlMimeType, playableExp: blob.PlayableUrlExpirationTimestampMs,
			preview: blob.PreviewUrl, previewMime: blob.PreviewUrlMimeType, previewExp: blob.PreviewUrlExpirationTimestampMs,
			previewLarge: blob.PreviewUrlLarge, attMime: blob.AttachmentMimeType,
			size: blob.Filesize, duration: blob.PlayableDurationMs, width: blob.PreviewWidth, height: blob.PreviewHeight,
			waveform: blob.WaveformData,
		})
	}
	for i, att := range wm.Attachments {
		add("attachment_", i, lsMedia{
			kind: att.AttachmentType, fbid: att.AttachmentFbid, fileName: att.Filename,
			playable: att.PlayableUrl, playableMime: att.PlayableUrlMimeType, playableExp: att.PlayableUrlExpirationTimestampMs,
			preview: att.PreviewUrl, previewMime: att.PreviewUrlMimeType, previewExp: att.PreviewUrlExpirationTimestampMs,
			previewLarge: att.ImageUrl, attMime: firstNonEmpty(att.AttachmentMimeType, att.ImageUrlMimeType),
			size: att.Filesize, duration: att.PlayableDurationMs, width: att.PreviewWidth, height: att.PreviewHeight,
		})
	}
	for i, sticker := range wm.Stickers {
		add("sticker_", i, lsMedia{
			kind: table.AttachmentTypeSticker, fbid: sticker.AttachmentFbid,
			playable: sticker.PlayableUrl, playableMime: sticker.PlayableUrlMimeType, playableExp: sticker.PlayableUrlExpirationTimestampMs,
			preview: firstNonEmpty(sticker.PreviewUrl, sticker.ImageUrl), previewMime: firstNonEmpty(sticker.PreviewUrlMimeType, sticker.ImageUrlMimeType),
			previewExp: sticker.PreviewUrlExpirationTimestampMs,
			width:      sticker.PreviewWidth, height: sticker.PreviewHeight,
		})
	}
	for i, xma := range wm.XMAAttachments {
		id := xma.AttachmentFbid
		if id == "" || ids[id] {
			id = "xma_" + strconv.Itoa(i)
		}
		ids[id] = true
		if special, systemText, ok := xmaSpecial(id, xma); ok {
			if systemText != "" {
				msg.System = true
				msg.Text = systemText
				continue
			}
			if special.ThumbnailURL != "" {
				b.store.PutMedia(msg.ID, id, mediaRec{Kind: "image", URL: special.ThumbnailURL})
			}
			msg.Attachments = append(msg.Attachments, special)
			continue
		}
		att, rec := xmaToAttachment(id, xma)
		msg.Attachments = append(msg.Attachments, att)
		if rec.URL != "" {
			b.store.PutMedia(msg.ID, id, rec)
		}
	}
	if len(wm.Stickers) == 0 && wm.StickerId != 0 {
		msg.Attachments = append(msg.Attachments, Attachment{ID: "sticker_" + itoa(wm.StickerId), Kind: "sticker"})
	}
	return msg
}

type lsMedia struct {
	kind         table.AttachmentType
	fbid         string
	fileName     string
	playable     string
	playableMime string
	playableExp  int64
	preview      string
	previewMime  string
	previewExp   int64
	previewLarge string
	attMime      string
	size         int64
	duration     int64
	width        int64
	height       int64
	waveform     string
}

func (m lsMedia) kindName() string {
	return attachmentKind(m.kind)
}

func (m lsMedia) convert(id string) (Attachment, mediaRec) {
	kind := m.kindName()
	att := Attachment{
		ID:         id,
		Kind:       kind,
		FileName:   m.fileName,
		Size:       m.size,
		DurationMS: m.duration,
		Width:      int(m.width),
		Height:     int(m.height),
	}
	var expires int64
	switch {
	case kind == "other":
		att.ThumbnailURL = firstNonEmpty(m.previewLarge, m.preview)
		att.MimeType = m.previewMime
		return att, mediaRec{Kind: kind, URL: att.ThumbnailURL, MimeType: m.previewMime, FileName: m.fileName, ExpiresMS: m.previewExp}
	case m.playable != "":
		att.URL = m.playable
		att.MimeType = firstNonEmpty(m.playableMime, m.attMime)
		expires = m.playableExp
	case m.previewLarge != "":
		att.URL = m.previewLarge
		att.MimeType = firstNonEmpty(m.attMime, m.previewMime)
		expires = m.previewExp
	default:
		att.URL = m.preview
		att.MimeType = firstNonEmpty(m.previewMime, m.attMime)
		expires = m.previewExp
	}
	if m.preview != "" && m.preview != att.URL {
		att.ThumbnailURL = m.preview
	}
	if m.isGifVideo() {
		kind = "video"
		att.Kind = kind
		att.Gif = true
	}
	return att, mediaRec{Kind: kind, URL: att.URL, MimeType: att.MimeType, FileName: m.fileName, ExpiresMS: expires}
}

func (m lsMedia) isGifVideo() bool {
	if m.playable == "" || !strings.HasPrefix(strings.ToLower(firstNonEmpty(m.playableMime, m.attMime)), "video/") {
		return false
	}
	if m.kind == table.AttachmentTypeAnimatedImage {
		return true
	}
	if attachmentKind(m.kind) != "video" {
		return false
	}
	return strings.EqualFold(m.previewMime, "image/gif") || strings.HasSuffix(strings.ToLower(m.fileName), ".gif")
}

func attachmentKind(t table.AttachmentType) string {
	switch t {
	case table.AttachmentTypeSticker, table.AttachmentTypeSelfieSticker, table.AttachmentTypeThirdPartySticker:
		return "sticker"
	case table.AttachmentTypeImage, table.AttachmentTypeAnimatedImage, table.AttachmentTypeEphemeralImage:
		return "image"
	case table.AttachmentTypeVideo, table.AttachmentTypeEphemeralVideo:
		return "video"
	case table.AttachmentTypeAudio, table.AttachmentTypeSoundBite:
		return "audio"
	case table.AttachmentTypeFile:
		return "file"
	default:
		return "other"
	}
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}
