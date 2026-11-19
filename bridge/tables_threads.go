package main

import (
	"go.mau.fi/mautrix-meta/pkg/messagix/table"
)

func lsFolder(name string) string {
	switch name {
	case "":
		return ""
	case "pending", "other", "e2ee_cutover_pending", "e2ee_cutover_other", "restricted", "salsa_restricted", "messenger_marketing_message", "page_background":
		return FolderRequests
	case "spam", "blocked":
		return FolderSpam
	case "archived", "e2ee_cutover_archived", "hidden", "disabled":
		return FolderArchived
	default:
		return FolderInbox
	}
}

func (tc *tableContext) setThread(key int64, fn func(rec *threadRec) bool) {
	id := tc.threadID(key)
	if !tc.b.store.HasThread(id) {
		return
	}
	changed := false
	tc.b.store.UpdateThread(id, func(rec *threadRec) { changed = fn(rec) })
	if changed {
		tc.touch(id)
	}
}

func (tc *tableContext) peerThread(contactID int64) string {
	id := itoa(contactID)
	for _, threadID := range tc.b.store.ThreadsWithUser(id) {
		if rec, ok := tc.b.store.Thread(threadID); ok && !rec.IsGroup {
			return threadID
		}
	}
	return ""
}

func (tc *tableContext) handleThreadSettings(tbl *table.LSTable) {
	store := tc.b.store
	self := store.SelfID()
	setFolder := func(folder string) func(rec *threadRec) bool {
		return func(rec *threadRec) bool {
			if rec.Folder == folder {
				return false
			}
			rec.Folder = folder
			return true
		}
	}
	for _, m := range tbl.LSMoveThreadToArchivedFolder {
		tc.setThread(m.ThreadKey, setFolder(FolderArchived))
	}
	for _, m := range tbl.LSMoveThreadToInboxAndUpdateParent {
		tc.setThread(m.ThreadKey, setFolder(FolderInbox))
	}
	for _, r := range tbl.LSDeleteThenInsertMessageRequest {
		isRequest := r.MessageRequestStatus == 1
		tc.setThread(r.ThreadKey, func(rec *threadRec) bool {
			switch {
			case rec.Folder == FolderSpam:
				return false
			case isRequest && rec.Folder != FolderRequests:
				rec.Folder = FolderRequests
				return true
			case !isRequest && rec.Folder == FolderRequests:
				rec.Folder = FolderInbox
				return true
			}
			return false
		})
	}
	for _, a := range tbl.LSUpdateThreadParticipantAdminStatus {
		member, admin := itoa(a.ContactId), a.IsAdmin
		tc.setThread(a.ThreadKey, func(rec *threadRec) bool { return setAdmin(rec, member, admin) })
	}
	for _, p := range tbl.LSAddParticipantIdToGroupThread {
		member, admin := itoa(p.ContactId), p.IsAdmin || p.IsSuperAdmin
		tc.setThread(p.ThreadKey, func(rec *threadRec) bool { return setAdmin(rec, member, admin) })
		if member != self && store.SetDeliveryReceipt(tc.threadID(p.ThreadKey), member, p.DeliveredWatermarkTimestampMs) && store.HasThread(tc.threadID(p.ThreadKey)) {
			tc.touch(tc.threadID(p.ThreadKey))
		}
	}
	for _, d := range tbl.LSUpdateDeliveryReceipt {
		id, user := tc.threadID(d.ThreadKey), itoa(d.ContactId)
		if user != self && store.SetDeliveryReceipt(id, user, d.DeliveredWatermarkTimestampMs) && store.HasThread(id) {
			tc.touch(id)
		}
	}
	for _, c := range tbl.LSClearPinnedMessages {
		tc.setThread(c.ThreadKey, func(rec *threadRec) bool {
			if len(rec.Pinned) == 0 {
				return false
			}
			rec.Pinned = nil
			return true
		})
	}
	for _, p := range tbl.LSSetPinnedMessage {
		messageID, pinnedMS := p.MessageId, p.PinnedTimestampMs
		tc.setThread(p.ThreadKey, func(rec *threadRec) bool {
			if pinnedMS <= 0 {
				before := len(rec.Pinned)
				kept := rec.Pinned[:0]
				for _, pin := range rec.Pinned {
					if pin.MessageID != messageID {
						kept = append(kept, pin)
					}
				}
				rec.Pinned = kept
				return len(kept) != before
			}
			return setPin(rec, messageID, pinnedMS)
		})
	}
	for _, g := range tbl.LSUpsertGradientColor {
		if store.SetThemeGradient(g.ThemeFbid, g.GradientIndex, g.Color) {
			for _, id := range store.ThreadsWithTheme(g.ThemeFbid) {
				tc.touch(id)
			}
		}
	}
	for _, v := range tbl.LSChangeViewerStatus {
		if v.CannotReplyReason != "" && v.CannotReplyReason != "blocked" {
			continue
		}
		blocked := v.CannotReplyReason == "blocked"
		tc.setThread(v.ThreadKey, func(rec *threadRec) bool {
			if rec.IsGroup || rec.Blocked == blocked {
				return false
			}
			rec.Blocked = blocked
			return true
		})
	}
	for _, r := range tbl.LSResetUserBlockStatus {
		if !r.Success {
			continue
		}
		blocked := r.UserBlockAction == 1
		if r.BlockedByViewerStatus != nil {
			blocked = *r.BlockedByViewerStatus != 0
		}
		if id := tc.peerThread(r.ContactID); id != "" {
			store.UpdateThread(id, func(rec *threadRec) { rec.Blocked = blocked })
			tc.touch(id)
		}
	}
}

func setAdmin(rec *threadRec, member string, admin bool) bool {
	if rec.Admins[member] == admin {
		return false
	}
	if admin {
		if rec.Admins == nil {
			rec.Admins = make(map[string]bool)
		}
		rec.Admins[member] = true
	} else {
		delete(rec.Admins, member)
	}
	return true
}

func (b *MetaBackend) applyEphemeral(threadID string, wm *table.WrappedMessage) {
	ttl := wm.EphemeralDurationInSec * 1000
	b.store.UpdateThread(threadID, func(rec *threadRec) {
		if wm.TimestampMs < rec.EphemeralSeenMS {
			return
		}
		rec.EphemeralSeenMS = wm.TimestampMs
		rec.EphemeralTTLMS = ttl
	})
}

func (tc *tableContext) removeMessage(messageID string) {
	if threadID, ok := tc.b.store.RemoveMessage(messageID); ok {
		tc.b.out.Emit(EventMessageRemoved, MessageRemovedEvent{ThreadID: threadID, MessageID: messageID})
		if tc.b.store.HasThread(threadID) {
			tc.touch(threadID)
		}
	}
}
