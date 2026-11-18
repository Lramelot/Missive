package main

import (
	"context"
	"errors"
	"net/http"
	"os"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/proto/waConsumerApplication"
	"go.mau.fi/whatsmeow/proto/waMsgApplication"
	"google.golang.org/protobuf/proto"

	"go.mau.fi/mautrix-meta/pkg/messagix/httpclient"
	"go.mau.fi/mautrix-meta/pkg/messagix/methods"
	"go.mau.fi/mautrix-meta/pkg/messagix/socket"
	"go.mau.fi/mautrix-meta/pkg/messagix/table"
)

var (
	editConfirmWait  = 5 * time.Second
	groupConfirmWait = 5 * time.Second
)

const (
	editConfirmPoll   = 100 * time.Millisecond
	editWindow        = 15 * time.Minute
	maxForwardTargets = 10
	maxThreadImage    = 10 << 20
)

var errTaskRejected = errors.New("Meta rejected the request")

func lsTaskFailure(resp *table.LSTable) error {
	if resp == nil {
		return nil
	}
	switch {
	case len(resp.LSHandleFailedTask) > 0:
		return newError(CodeInternal, "%v: %s", errTaskRejected, resp.LSHandleFailedTask[0].Message)
	case len(resp.LSMarkOptimisticMessageFailed) > 0:
		return newError(CodeInternal, "%v: %s", errTaskRejected, resp.LSMarkOptimisticMessageFailed[0].Message)
	case len(resp.LSIssueNewError) > 0:
		return newError(CodeInternal, "%v: %s", errTaskRejected, firstNonEmpty(resp.LSIssueNewError[0].ErrorMessage, resp.LSIssueNewError[0].ErrorTitle))
	}
	for _, completion := range resp.LSMailboxTaskCompletionApiOnTaskCompletion {
		if !completion.Success {
			return newError(CodeInternal, "%v", errTaskRejected)
		}
	}
	return nil
}

func (b *MetaBackend) runTasks(ctx context.Context, tasks ...socket.Task) (*table.LSTable, error) {
	var resp *table.LSTable
	var err error
	if b.taskFn != nil {
		resp, err = b.taskFn(ctx, tasks...)
	} else {
		client, cerr := b.connectedClient(ctx)
		if cerr != nil {
			return nil, cerr
		}
		resp, err = client.ExecuteTasks(ctx, tasks...)
	}
	if err != nil {
		var be *BridgeError
		if errors.As(err, &be) {
			return nil, be
		}
		return nil, newError(CodeInternal, "Meta request failed: %v", err)
	}
	if ferr := lsTaskFailure(resp); ferr != nil {
		return resp, ferr
	}
	if resp != nil {
		b.handleTable(resp, modeLive)
	}
	return resp, nil
}

func (b *MetaBackend) emitThread(id string) {
	if view, ok := b.store.ThreadView(id); ok {
		b.out.Emit(EventThreads, ThreadsEvent{Threads: []Thread{view}})
	}
}

func (b *MetaBackend) loggedThread(threadID string) (threadRec, error) {
	rec, _, err := b.threadAndReply(threadID, "")
	return rec, err
}

func (b *MetaBackend) lsTarget(rec threadRec) (int64, int64, error) {
	key, err := parseThreadKey(rec.ID)
	if err != nil {
		return 0, 0, err
	}
	if rec.E2EE {
		if fbKey, ok := b.store.FBKeyFor(key); ok {
			return fbKey, syncGroupE2EE, nil
		}
	}
	return key, syncGroupInbox, nil
}

func (b *MetaBackend) ownMessage(threadID, messageID string) (threadRec, Message, error) {
	rec, err := b.loggedThread(threadID)
	if err != nil {
		return rec, Message{}, err
	}
	msg, ok := b.store.MessageInThread(threadID, messageID)
	if !ok {
		return rec, Message{}, newError(CodeNotFound, "unknown message %s", messageID)
	}
	if !msg.IsOutgoing && msg.SenderID != b.store.SelfID() {
		return rec, Message{}, newError(CodeBadRequest, "only your own messages can be changed")
	}
	return rec, msg, nil
}

func checkEditable(msg Message, now time.Time) error {
	switch {
	case msg.Deleted || msg.System || msg.Poll != nil:
		return newError(CodeBadRequest, "this message cannot be edited")
	case msg.Text == "" && len(msg.Attachments) > 0:
		return newError(CodeBadRequest, "only text messages can be edited")
	case now.Sub(time.UnixMilli(msg.TimestampMS)) > editWindow:
		return newError(CodeBadRequest, "Meta only allows edits during the first 15 minutes")
	}
	return nil
}

func wrapConsumerContent(content *waConsumerApplication.ConsumerApplication_Content) *waConsumerApplication.ConsumerApplication {
	return &waConsumerApplication.ConsumerApplication{Payload: &waConsumerApplication.ConsumerApplication_Payload{
		Payload: &waConsumerApplication.ConsumerApplication_Payload_Content{Content: content},
	}}
}

func waEditPayload(rec threadRec, msg Message, self, text string, nowMS int64) *waConsumerApplication.ConsumerApplication {
	return wrapConsumerContent(&waConsumerApplication.ConsumerApplication_Content{
		Content: &waConsumerApplication.ConsumerApplication_Content_EditMessage{EditMessage: &waConsumerApplication.ConsumerApplication_EditMessage{
			Key:         messageKeyFor(rec, msg, self),
			Message:     waOutgoingText(text, nil),
			TimestampMS: proto.Int64(nowMS),
		}},
	})
}

func waRevokePayload(rec threadRec, msg Message, self string) *waConsumerApplication.ConsumerApplication {
	return &waConsumerApplication.ConsumerApplication{Payload: &waConsumerApplication.ConsumerApplication_Payload{
		Payload: &waConsumerApplication.ConsumerApplication_Payload_ApplicationData{
			ApplicationData: &waConsumerApplication.ConsumerApplication_ApplicationData{
				ApplicationContent: &waConsumerApplication.ConsumerApplication_ApplicationData_Revoke{
					Revoke: &waConsumerApplication.ConsumerApplication_RevokeMessage{Key: messageKeyFor(rec, msg, self)},
				},
			},
		},
	}}
}

func (b *MetaBackend) sendWAConsumer(ctx context.Context, rec threadRec, consumer *waConsumerApplication.ConsumerApplication, deletedID string) error {
	id := strconv.FormatInt(methods.GenerateEpochID(), 10)
	meta := &waMsgApplication.MessageApplication_Metadata{}
	if b.waSendFn != nil {
		if err := b.waSendFn(ctx, rec, consumer); err != nil {
			return err
		}
	} else {
		if !b.connectedState() {
			return newError(CodeNotConnected, "not connected")
		}
		cli, err := b.waitE2EE(ctx)
		if err != nil {
			return err
		}
		if _, err := cli.SendFBMessage(ctx, chatJIDFor(rec), consumer, meta, whatsmeow.SendRequestExtra{ID: id}); err != nil {
			return newError(CodeInternal, "failed to send encrypted message: %v", err)
		}
	}
	if deletedID != "" {
		b.uploadSentToHistory(chatJIDFor(rec), id, deletedID, consumer, meta)
	}
	return nil
}

func (b *MetaBackend) lsEditAccepted(ctx context.Context, resp *table.LSTable, messageID, text string) bool {
	if resp == nil {
		return true
	}
	var confirmed *table.LSEditMessage
	for _, e := range resp.LSEditMessage {
		if e.MessageID == messageID {
			confirmed = e
		}
	}
	if confirmed == nil || confirmed.Text == text {
		return true
	}
	deadline := time.Now().Add(editConfirmWait)
	for time.Now().Before(deadline) {
		if !sleepCtx(ctx, editConfirmPoll) {
			break
		}
		if msg, ok := b.store.FindMessage(messageID); ok && msg.Text == text {
			return true
		}
	}
	return false
}

func (b *MetaBackend) EditMessage(ctx context.Context, threadID, messageID, text string) error {
	rec, msg, err := b.ownMessage(threadID, messageID)
	if err != nil {
		return err
	}
	if err := checkEditable(msg, time.Now()); err != nil {
		return err
	}
	if isLSMessageID(messageID) {
		var resp *table.LSTable
		resp, err = b.runTasks(ctx, &socket.EditMessageTask{MessageID: messageID, Text: text})
		if err == nil && !b.lsEditAccepted(ctx, resp, messageID, text) {
			return newError(CodeEditRejected, "Messenger rejected the edit")
		}
	} else {
		err = b.sendWAConsumer(ctx, rec, waEditPayload(rec, msg, b.store.SelfID(), text, time.Now().UnixMilli()), "")
	}
	if err != nil {
		return err
	}
	if updated, ok := b.store.UpdateMessage(messageID, func(m *Message) bool {
		if m.Text == text && m.Edited {
			return false
		}
		m.Text, m.Edited, m.Mentions = text, true, nil
		return true
	}); ok {
		b.emitMessage(EventMessageUpdate, updated)
	}
	return nil
}

func (b *MetaBackend) Unsend(ctx context.Context, threadID, messageID string) error {
	rec, msg, err := b.ownMessage(threadID, messageID)
	if err != nil {
		return err
	}
	if msg.Deleted || msg.System {
		return newError(CodeBadRequest, "this message cannot be unsent")
	}
	if isLSMessageID(messageID) {
		_, err = b.runTasks(ctx, &socket.DeleteMessageTask{MessageId: messageID})
	} else {
		err = b.sendWAConsumer(ctx, rec, waRevokePayload(rec, msg, b.store.SelfID()), messageID)
	}
	if err != nil {
		return err
	}
	b.markMessageDeleted(messageID)
	return nil
}

func (b *MetaBackend) markMessageDeleted(messageID string) {
	if updated, ok := b.store.UpdateMessage(messageID, func(m *Message) bool {
		if m.Deleted {
			return false
		}
		m.Deleted, m.Text, m.Attachments, m.Mentions = true, "", []Attachment{}, nil
		return true
	}); ok {
		b.emitMessage(EventMessageUpdate, updated)
	}
}

func (b *MetaBackend) removeMessageLocally(threadID, messageID string) {
	if _, ok := b.store.RemoveMessage(messageID); ok {
		b.out.Emit(EventMessageRemoved, MessageRemovedEvent{ThreadID: threadID, MessageID: messageID})
		b.emitThread(threadID)
	}
}

func (b *MetaBackend) DeleteForMe(ctx context.Context, threadID, messageID string) error {
	rec, err := b.loggedThread(threadID)
	if err != nil {
		return err
	}
	if _, ok := b.store.MessageInThread(threadID, messageID); !ok {
		return newError(CodeNotFound, "unknown message %s", messageID)
	}
	if isLSMessageID(messageID) {
		key, err := b.lsThreadKey(rec)
		if err != nil {
			return err
		}
		if _, err := b.runTasks(ctx, &socket.DeleteMessageMeOnlyTask{ThreadKey: key, MessageId: messageID}); err != nil {
			return err
		}
	}
	b.removeMessageLocally(threadID, messageID)
	return nil
}

func lsForwardTask(key, otid int64, messageID string) *socket.SendMessageTask {
	return &socket.SendMessageTask{
		ThreadId:         key,
		Otid:             otid,
		Source:           table.MESSENGER_INBOX_IN_THREAD,
		InitiatingSource: table.FACEBOOK_INBOX,
		SendType:         table.FORWARD,
		SyncGroup:        1,
		ForwardedMsgId:   messageID,
	}
}

func (b *MetaBackend) Forward(ctx context.Context, fromThreadID, messageID string, toThreadIDs []string) ([]string, error) {
	if _, err := b.loggedThread(fromThreadID); err != nil {
		return nil, err
	}
	msg, ok := b.store.MessageInThread(fromThreadID, messageID)
	if !ok {
		return nil, newError(CodeNotFound, "unknown message %s", messageID)
	}
	if msg.Deleted || msg.System || msg.Poll != nil {
		return nil, newError(CodeBadRequest, "this message cannot be forwarded")
	}
	targets := make([]threadRec, 0, len(toThreadIDs))
	for _, id := range toThreadIDs {
		rec, ok := b.store.Thread(id)
		if !ok {
			return nil, newError(CodeNotFound, "unknown thread %s", id)
		}
		targets = append(targets, rec)
	}
	ids := make([]string, len(targets))
	var firstErr error
	failures := 0
	for i, rec := range targets {
		forward := b.forwardOne
		if b.forwardFn != nil {
			forward = b.forwardFn
		}
		id, err := forward(ctx, msg, rec)
		ids[i] = id
		if err != nil {
			if id == "" {
				failures++
			}
			if firstErr == nil {
				firstErr = err
			}
			b.log.Warn().Err(err).Bool("partial", id != "").Msg("Forward to one conversation failed")
		}
	}
	if failures == len(targets) && firstErr != nil {
		return nil, firstErr
	}
	return ids, nil
}

func (b *MetaBackend) forwardOne(ctx context.Context, msg Message, rec threadRec) (string, error) {
	if isLSMessageID(msg.ID) && !rec.E2EE {
		key, err := parseThreadKey(rec.ID)
		if err != nil {
			return "", err
		}
		client, err := b.sendClient(ctx)
		if err != nil {
			return "", err
		}
		otid := methods.GenerateEpochID()
		echo := Message{ThreadID: rec.ID, Text: msg.Text, Attachments: []Attachment{}, Forwarded: true}
		return b.executeLSSend(ctx, client, key, otid, lsForwardTask(key, otid, msg.ID), echo, nil)
	}
	var first string
	if strings.TrimSpace(msg.Text) != "" {
		id, err := b.Send(ctx, rec.ID, msg.Text, "", "", nil)
		if err != nil {
			return "", err
		}
		first = id
	}
	for _, att := range msg.Attachments {
		file, ok, err := b.forwardableFile(ctx, msg, att)
		if err != nil {
			return first, err
		}
		if !ok {
			continue
		}
		var id string
		if rec.E2EE {
			id, err = b.sendMediaE2EE(ctx, rec, file, "", nil, "")
		} else {
			id, err = b.sendMediaLS(ctx, rec, file, "", nil, "")
		}
		if err != nil {
			return first, err
		}
		if first == "" {
			first = id
		}
	}
	if first == "" {
		return "", newError(CodeUnsupported, "nothing in this message can be forwarded")
	}
	return first, nil
}

func (b *MetaBackend) forwardableFile(ctx context.Context, msg Message, att Attachment) (sendFile, bool, error) {
	switch att.Kind {
	case "image", "video", "audio", "file", "sticker":
	default:
		return sendFile{}, false, nil
	}
	res, err := b.fullMedia(ctx, msg.ID, att)
	if err != nil {
		return sendFile{}, false, err
	}
	files, err := loadSendFiles([]string{res.Path})
	if err != nil {
		return sendFile{}, false, err
	}
	file := files[0]
	if att.FileName != "" {
		file.Name = att.FileName
	}
	file.Voice, file.Waveform, file.DurationMS = att.Voice, att.Waveform, att.DurationMS
	if att.Voice {
		file.Kind = "audio"
	}
	return file, true, nil
}

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

func (b *MetaBackend) CreatePoll(ctx context.Context, threadID, question string, options []string) error {
	_, key, err := b.lsGroup(threadID, true)
	if err != nil {
		return err
	}
	_, err = b.runTasks(ctx, &socket.CreatePollTask{QuestionText: question, ThreadKey: key, Options: options, SyncGroup: 1})
	return err
}

func (b *MetaBackend) VotePoll(ctx context.Context, threadID, pollID string, optionIDs []string) error {
	rec, err := b.loggedThread(threadID)
	if err != nil {
		return err
	}
	poll, ok := b.store.PollRecord(pollID)
	if !ok || poll.ThreadID != threadID {
		return newError(CodeNotFound, "unknown poll %s", pollID)
	}
	selected, err := pollSelection(poll, optionIDs)
	if err != nil {
		return err
	}
	if rec.E2EE {
		return newError(CodeUnsupported, "polls are not available in encrypted conversations")
	}
	key, err := parseThreadKey(rec.ID)
	if err != nil {
		return err
	}
	pid, err := strconv.ParseInt(pollID, 10, 64)
	if err != nil {
		return newError(CodeBadRequest, "invalid poll id")
	}
	if _, err := b.runTasks(ctx, &socket.UpdatePollTask{ThreadKey: key, PollID: pid, SelectedOptions: selected, SyncGroup: 1}); err != nil {
		return err
	}
	if b.store.SetPollVotes(pollID, b.store.SelfID(), optionIDs, time.Now().UnixMilli()) {
		if msg, ok := b.store.FindMessage(poll.MessageID); ok {
			b.emitMessage(EventMessageUpdate, msg)
		}
	}
	return nil
}

func pollSelection(poll pollRec, optionIDs []string) ([]int64, error) {
	selected := make([]int64, 0, len(optionIDs))
	for _, id := range optionIDs {
		if !slices.ContainsFunc(poll.Options, func(o pollOptionRec) bool { return o.ID == id }) {
			return nil, newError(CodeBadRequest, "unknown poll option %s", id)
		}
		v, err := strconv.ParseInt(id, 10, 64)
		if err != nil {
			return nil, newError(CodeBadRequest, "invalid poll option %s", id)
		}
		selected = append(selected, v)
	}
	slices.Sort(selected)
	return slices.Compact(selected), nil
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
