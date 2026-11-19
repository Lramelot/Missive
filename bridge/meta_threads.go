package main

import (
	"context"
	"net/http"
	"os"
	"strconv"
	"time"

	"github.com/google/uuid"

	"go.mau.fi/mautrix-meta/pkg/messagix/httpclient"
	"go.mau.fi/mautrix-meta/pkg/messagix/methods"
	"go.mau.fi/mautrix-meta/pkg/messagix/socket"
	"go.mau.fi/mautrix-meta/pkg/messagix/table"
)

func (b *MetaBackend) lsGroup(threadID string, needManage bool) (threadRec, int64, error) {
	rec, err := b.loggedThread(threadID)
	if err != nil {
		return rec, 0, err
	}
	if !rec.IsGroup || rec.E2EE {
		return rec, 0, newError(CodeUnsupported, "only non-encrypted group conversations support this")
	}
	if needManage && !canManage(&rec, b.store.SelfID()) {
		return rec, 0, newError(CodeUnsupported, "you cannot manage this conversation")
	}
	key, err := parseThreadKey(rec.ID)
	return rec, key, err
}

var searchTypes = []table.SearchType{
	table.SearchTypeContact, table.SearchTypeGroup, table.SearchTypePage, table.SearchTypeNonContact,
	table.SearchTypeIGContactFollowing, table.SearchTypeIGContactNonFollowing,
	table.SearchTypeIGNonContactFollowing, table.SearchTypeIGNonContactNonFollowing,
	table.SearchTypeCommunityMessagingThread,
}

func (b *MetaBackend) SearchUsers(ctx context.Context, query string, limit int) ([]UserResult, error) {
	if err := b.requireLogin(); err != nil {
		return nil, err
	}
	results := b.store.SearchContacts(query, limit)
	if len(results) >= limit {
		return results, nil
	}
	resp, err := b.runTasks(ctx, &socket.SearchUserTask{Query: query, SupportedTypes: searchTypes, SurfaceType: 5})
	if err != nil {
		if len(results) > 0 {
			return results, nil
		}
		return nil, err
	}
	return mergeSearchResults(results, b.searchRows(resp), limit), nil
}

func (b *MetaBackend) searchRows(resp *table.LSTable) []UserResult {
	if resp == nil {
		return nil
	}
	var out []UserResult
	for _, r := range resp.LSInsertSearchResult {
		canMessage := r.CanViewerMessage || (r.OtherUserId != 0 && r.BlockedByViewerStatus == 0)
		if !r.ThreadType.IsOneToOne() || !canMessage {
			continue
		}
		id := r.OtherUserId
		if id == 0 {
			id, _ = strconv.ParseInt(r.ResultId, 10, 64)
		}
		if id == 0 {
			continue
		}
		user := UserResult{ID: itoa(id), Name: r.DisplayName, AvatarURL: r.ProfilePicUrl, IsFriend: r.Type_ == table.SearchTypeContact}
		user.ThreadID = b.store.PeerThreadID(user.ID)
		out = append(out, user)
	}
	return out
}

func mergeSearchResults(first, second []UserResult, limit int) []UserResult {
	seen := make(map[string]bool, len(first))
	out := make([]UserResult, 0, min(limit, len(first)+len(second)))
	for _, list := range [][]UserResult{first, second} {
		for _, u := range list {
			if len(out) == limit || seen[u.ID] {
				continue
			}
			seen[u.ID] = true
			out = append(out, u)
		}
	}
	return out
}

func (b *MetaBackend) OpenThread(ctx context.Context, userID string) (Thread, error) {
	if err := b.requireLogin(); err != nil {
		return Thread{}, err
	}
	if userID == b.store.SelfID() {
		return Thread{}, newError(CodeBadRequest, "cannot open a conversation with yourself")
	}
	if rec, ok := b.store.Thread(userID); ok && rec.IsGroup {
		return Thread{}, newError(CodeBadRequest, "%s is a group conversation, not a user", userID)
	}
	if id := b.store.PeerThreadID(userID); id != "" {
		view, _ := b.store.ThreadView(id)
		return view, nil
	}
	fbid, err := strconv.ParseInt(userID, 10, 64)
	if err != nil || fbid <= 0 {
		return Thread{}, newError(CodeBadRequest, "invalid user id")
	}
	if _, err := b.runTasks(ctx, &socket.CreateThreadTask{ThreadFBID: fbid, SyncGroup: 1}); err != nil {
		return Thread{}, err
	}
	id := b.store.PeerThreadID(userID)
	if id == "" {
		id = userID
		self := b.store.SelfID()
		b.store.UpdateThread(id, func(rec *threadRec) {
			rec.PeerID = userID
			rec.ThreadType = int64(table.ONE_TO_ONE)
			rec.Members = map[string]bool{userID: true, self: true}
			rec.LastActivityMS = max(rec.LastActivityMS, time.Now().UnixMilli())
		})
	}
	b.emitThread(id)
	view, _ := b.store.ThreadView(id)
	return view, nil
}

func (b *MetaBackend) CreateGroup(ctx context.Context, userIDs []string, name string) (Thread, error) {
	if err := b.requireLogin(); err != nil {
		return Thread{}, err
	}
	participants := make([]int64, 0, len(userIDs))
	for _, id := range userIDs {
		v, err := strconv.ParseInt(id, 10, 64)
		if err != nil || v <= 0 {
			return Thread{}, newError(CodeBadRequest, "invalid user id %s", id)
		}
		participants = append(participants, v)
	}
	knownGroups := b.store.GroupIDs()
	threadKey, otid := methods.GenerateEpochID(), methods.GenerateEpochID()
	resp, err := b.runTasks(ctx, &socket.CreateGroupTask{
		Participants: participants,
		SendPayload:  socket.CreateGroupPayload{ThreadID: threadKey, OTID: strconv.FormatInt(otid, 10), Source: 0, SendType: 8},
	})
	if err != nil {
		return Thread{}, err
	}
	realKey := int64(0)
	if resp != nil {
		for _, repl := range resp.LSReplaceOptimisticThread {
			if repl.ThreadKey1 == threadKey {
				realKey = repl.ThreadKey2
			}
		}
	}
	id := itoa(realKey)
	if realKey == 0 {
		id = b.awaitNewGroup(ctx, knownGroups, userIDs)
		if id == "" {
			return Thread{}, uncertainError(newError(CodeInternal, "Meta did not confirm the new group; it may have been created, check the conversation list"))
		}
		realKey = mustThreadKey(id)
	}
	self := b.store.SelfID()
	b.store.UpdateThread(id, func(rec *threadRec) {
		rec.IsGroup = true
		rec.ThreadType = int64(table.GROUP_THREAD)
		if rec.Members == nil {
			rec.Members = make(map[string]bool)
		}
		rec.Members[self] = true
		for _, u := range userIDs {
			rec.Members[u] = true
		}
		setAdmin(rec, self, true)
		rec.LastActivityMS = max(rec.LastActivityMS, time.Now().UnixMilli())
	})
	if name != "" {
		if _, err := b.runTasks(ctx, &socket.RenameThreadTask{ThreadKey: realKey, ThreadName: name, SyncGroup: 1}); err != nil {
			b.log.Warn().Err(err).Msg("Failed to name the new group")
		} else {
			b.store.UpdateExistingThread(id, func(rec *threadRec) { rec.Name = name })
		}
	}
	b.emitThread(id)
	view, _ := b.store.ThreadView(id)
	return view, nil
}

func (b *MetaBackend) AcceptRequest(ctx context.Context, threadID string) error {
	rec, err := b.loggedThread(threadID)
	if err != nil {
		return err
	}
	key, syncGroup, err := b.lsTarget(rec)
	if err != nil {
		return err
	}
	if _, err := b.runTasks(ctx, &socket.AcceptMessageRequestTask{ThreadKey: key, SyncGroup: syncGroup}); err != nil {
		return err
	}
	b.store.UpdateExistingThread(threadID, func(rec *threadRec) { rec.Folder = FolderInbox })
	b.emitThread(threadID)
	return nil
}

func (b *MetaBackend) DeleteThread(ctx context.Context, threadID string) error {
	rec, err := b.loggedThread(threadID)
	if err != nil {
		return err
	}
	key, syncGroup, err := b.lsTarget(rec)
	if err != nil {
		return err
	}
	if _, err := b.runTasks(ctx, &socket.DeleteThreadTask{ThreadKey: key, RemoveType: 0, SyncGroup: syncGroup}); err != nil {
		return err
	}
	if b.store.RemoveThread(threadID) {
		b.out.Emit(EventThreadRemoved, ThreadRemovedEvent{ThreadID: threadID})
	}
	return nil
}

func (b *MetaBackend) Mute(ctx context.Context, threadID string, untilMS int64) error {
	rec, err := b.loggedThread(threadID)
	if err != nil {
		return err
	}
	key, syncGroup, err := b.lsTarget(rec)
	if err != nil {
		return err
	}
	if _, err := b.runTasks(ctx, &socket.MuteThreadTask{ThreadKey: key, MailboxType: 0, MuteExpireTimeMS: untilMS, SyncGroup: syncGroup}); err != nil {
		return err
	}
	b.store.UpdateExistingThread(threadID, func(rec *threadRec) { rec.MuteExpireMS = untilMS })
	b.emitThread(threadID)
	return nil
}

func (b *MetaBackend) RenameThread(ctx context.Context, threadID, name string) error {
	_, key, err := b.lsGroup(threadID, true)
	if err != nil {
		return err
	}
	if _, err := b.runTasks(ctx, &socket.RenameThreadTask{ThreadKey: key, ThreadName: name, SyncGroup: 1}); err != nil {
		return err
	}
	b.store.UpdateExistingThread(threadID, func(rec *threadRec) { rec.Name = name })
	b.emitThread(threadID)
	return nil
}

func readThreadImage(path string) ([]byte, string, error) {
	info, err := os.Stat(path)
	if err != nil || info.IsDir() {
		return nil, "", newError(CodeBadRequest, "image file is unreadable")
	}
	if info.Size() > maxThreadImage {
		return nil, "", newError(CodeBadRequest, "image is larger than 10 MB")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, "", newError(CodeBadRequest, "image file is unreadable")
	}
	mime := http.DetectContentType(data)
	switch mime {
	case "image/jpeg", "image/png", "image/gif", "image/webp":
	default:
		return nil, "", newError(CodeBadRequest, "the file is not a supported image")
	}
	return data, mime, nil
}

func (b *MetaBackend) SetThreadImage(ctx context.Context, threadID, path string) error {
	_, key, err := b.lsGroup(threadID, true)
	if err != nil {
		return err
	}
	data, mime, err := readThreadImage(path)
	if err != nil {
		return err
	}
	client, err := b.connectedClient(ctx)
	if err != nil {
		return err
	}
	resp, err := client.GetHTTP().SendMercuryUploadRequest(ctx, key, &httpclient.MercuryUploadMedia{Filename: "avatar.jpg", MimeType: mime, MediaData: data})
	if err != nil {
		return newError(CodeInternal, "image upload failed: %v", err)
	}
	imageID := resp.Payload.RealMetadata.GetFbId()
	if imageID == 0 {
		return newError(CodeUnsupported, "Meta did not accept this image")
	}
	_, err = b.runTasks(ctx, &socket.SetThreadImageTask{ThreadKey: key, ImageID: imageID, SyncGroup: 1})
	return err
}

func parseUserIDs(ids []string) ([]int64, error) {
	out := make([]int64, 0, len(ids))
	for _, id := range ids {
		v, err := strconv.ParseInt(id, 10, 64)
		if err != nil || v <= 0 {
			return nil, newError(CodeBadRequest, "invalid user id %s", id)
		}
		out = append(out, v)
	}
	return out, nil
}

func (b *MetaBackend) AddParticipants(ctx context.Context, threadID string, userIDs []string) error {
	_, key, err := b.lsGroup(threadID, true)
	if err != nil {
		return err
	}
	ids, err := parseUserIDs(userIDs)
	if err != nil {
		return err
	}
	if _, err := b.runTasks(ctx, &socket.AddParticipantsTask{ThreadKey: key, ContactIDs: ids, SyncGroup: 1}); err != nil {
		return err
	}
	b.store.UpdateExistingThread(threadID, func(rec *threadRec) {
		if rec.Members == nil {
			rec.Members = make(map[string]bool)
		}
		for _, u := range userIDs {
			rec.Members[u] = true
		}
	})
	b.emitThread(threadID)
	return nil
}

func (b *MetaBackend) RemoveParticipant(ctx context.Context, threadID, userID string) error {
	if userID == b.store.SelfID() {
		return b.LeaveThread(ctx, threadID)
	}
	_, key, err := b.lsGroup(threadID, true)
	if err != nil {
		return err
	}
	ids, err := parseUserIDs([]string{userID})
	if err != nil {
		return err
	}
	if _, err := b.runTasks(ctx, &socket.RemoveParticipantTask{ThreadID: key, ContactID: ids[0]}); err != nil {
		return err
	}
	b.store.UpdateExistingThread(threadID, func(rec *threadRec) {
		delete(rec.Members, userID)
		delete(rec.Admins, userID)
	})
	b.emitThread(threadID)
	return nil
}

func (b *MetaBackend) LeaveThread(ctx context.Context, threadID string) error {
	_, key, err := b.lsGroup(threadID, true)
	if err != nil {
		return err
	}
	self := b.store.SelfID()
	ids, err := parseUserIDs([]string{self})
	if err != nil {
		return err
	}
	b.store.UpdateExistingThread(threadID, func(rec *threadRec) { rec.Leaving = true })
	_, err = b.runTasks(ctx, &socket.RemoveParticipantTask{ThreadID: key, ContactID: ids[0]})
	b.store.UpdateExistingThread(threadID, func(rec *threadRec) {
		rec.Leaving = false
		if err == nil {
			markLeft(rec, self)
		}
	})
	if err != nil {
		return err
	}
	b.emitThread(threadID)
	return nil
}

func markLeft(rec *threadRec, self string) {
	rec.Left = true
	delete(rec.Members, self)
	delete(rec.Admins, self)
}

func (b *MetaBackend) SetAdmin(ctx context.Context, threadID, userID string, admin bool) error {
	rec, key, err := b.lsGroup(threadID, true)
	if err != nil {
		return err
	}
	if !rec.Admins[b.store.SelfID()] {
		return newError(CodeUnsupported, "only group admins can change admins")
	}
	if !rec.Members[userID] {
		return newError(CodeBadRequest, "user %s is not a member of this group", userID)
	}
	ids, err := parseUserIDs([]string{userID})
	if err != nil {
		return err
	}
	flag := 0
	if admin {
		flag = 1
	}
	if _, err := b.runTasks(ctx, &socket.UpdateAdminTask{ThreadKey: key, ContactID: ids[0], IsAdmin: flag}); err != nil {
		return err
	}
	b.store.UpdateExistingThread(threadID, func(rec *threadRec) { setAdmin(rec, userID, admin) })
	b.emitThread(threadID)
	return nil
}

func (b *MetaBackend) BlockUser(ctx context.Context, userID string, blocked bool) error {
	if err := b.requireLogin(); err != nil {
		return err
	}
	ids, err := parseUserIDs([]string{userID})
	if err != nil {
		return err
	}
	action := 0
	if blocked {
		action = 1
	}
	resp, err := b.runTasks(ctx, &socket.SetUserBlockStatusTask{BlockeeID: ids[0], RequestID: uuid.NewString(), UserBlockAction: action})
	if err != nil {
		return err
	}
	if resp != nil {
		for _, reset := range resp.LSResetUserBlockStatus {
			if reset.ContactID == ids[0] && !reset.Success {
				return newError(CodeInternal, "Meta rejected the block change")
			}
		}
	}
	if id := b.store.PeerThreadID(userID); id != "" {
		b.store.UpdateExistingThread(id, func(rec *threadRec) { rec.Blocked = blocked })
		b.emitThread(id)
	}
	return nil
}

const groupConfirmPoll = 200 * time.Millisecond

func mustThreadKey(id string) int64 {
	v, _ := strconv.ParseInt(id, 10, 64)
	return v
}

func (b *MetaBackend) awaitNewGroup(ctx context.Context, known map[string]bool, members []string) string {
	deadline := time.Now().Add(groupConfirmWait)
	for {
		if id := b.store.NewGroupWith(known, members); id != "" {
			return id
		}
		if time.Now().After(deadline) || !sleepCtx(ctx, groupConfirmPoll) {
			return ""
		}
	}
}
