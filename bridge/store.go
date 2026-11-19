package main

import (
	"cmp"
	"encoding/json"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/rs/zerolog"
)

const maxPersistedMessagesPerThread = 300

var writeStoreFile = writeSecretFile

type threadRec struct {
	ID              string          `json:"id"`
	Name            string          `json:"name,omitempty"`
	AvatarURL       string          `json:"avatar_url,omitempty"`
	Snippet         string          `json:"snippet,omitempty"`
	LastActivityMS  int64           `json:"last_activity_ms,omitempty"`
	SnippetMS       int64           `json:"snippet_ms,omitempty"`
	LastReadMS      int64           `json:"last_read_ms,omitempty"`
	MetaReadMS      int64           `json:"meta_read_ms,omitempty"`
	MarkedReadMS    int64           `json:"marked_read_ms,omitempty"`
	UnreadCount     int             `json:"unread_count,omitempty"`
	IsGroup         bool            `json:"is_group,omitempty"`
	E2EE            bool            `json:"e2ee,omitempty"`
	ThreadType      int64           `json:"thread_type,omitempty"`
	PeerID          string          `json:"peer_id,omitempty"`
	Members         map[string]bool `json:"members,omitempty"`
	MuteExpireMS    int64           `json:"mute_expire_ms,omitempty"`
	Folder          string          `json:"folder,omitempty"`
	ThemeColor      string          `json:"theme_color,omitempty"`
	ThemeID         int64           `json:"theme_id,omitempty"`
	QuickEmoji      string          `json:"quick_emoji,omitempty"`
	EphemeralTTLMS  int64           `json:"ephemeral_ttl_ms,omitempty"`
	Pinned          []pinRec        `json:"pinned,omitempty"`
	Admins          map[string]bool `json:"admins,omitempty"`
	Blocked         bool            `json:"blocked,omitempty"`
	EphemeralSeenMS int64           `json:"ephemeral_seen_ms,omitempty"`
	Left            bool            `json:"left,omitempty"`
	Leaving         bool            `json:"-"`
}

func isMuted(muteExpireMS, nowMS int64) bool {
	return muteExpireMS < 0 || muteExpireMS > nowMS
}

type contactRec struct {
	Name      string `json:"name,omitempty"`
	FirstName string `json:"first_name,omitempty"`
	AvatarURL string `json:"avatar_url,omitempty"`
}

type mediaRec struct {
	Kind            string `json:"kind,omitempty"`
	URL             string `json:"url,omitempty"`
	MimeType        string `json:"mime_type,omitempty"`
	FileName        string `json:"file_name,omitempty"`
	ExpiresMS       int64  `json:"expires_ms,omitempty"`
	WAType          string `json:"wa_type,omitempty"`
	WATransport     []byte `json:"wa_transport,omitempty"`
	ReceiverFetchID string `json:"receiver_fetch_id,omitempty"`
	Path            string `json:"path,omitempty"`
}

const storeSnapshotVersion = 1

type storeSnapshot struct {
	Version       int                                     `json:"v,omitempty"`
	SelfID        string                                  `json:"self_id"`
	Threads       map[string]*threadRec                   `json:"threads"`
	Messages      map[string][]Message                    `json:"messages"`
	Contacts      map[string]*contactRec                  `json:"contacts"`
	HasMoreBefore map[string]bool                         `json:"has_more_before"`
	Hybrid        map[int64]int64                         `json:"hybrid"`
	Media         map[string]*mediaRec                    `json:"media,omitempty"`
	Nicknames     map[string]map[string]string            `json:"nicknames,omitempty"`
	Reactions     map[string]map[string]string            `json:"reactions,omitempty"`
	ReactionsV2   map[string]map[string]reactionAggregate `json:"reactions_v2,omitempty"`
	ReactionTimes map[string]map[string]int64             `json:"reaction_times,omitempty"`
	Receipts      map[string]map[string]int64             `json:"receipts,omitempty"`
	Deliveries    map[string]map[string]int64             `json:"deliveries,omitempty"`
	Themes        map[string]map[string]int64             `json:"themes,omitempty"`
	Polls         map[string]*pollRec                     `json:"polls,omitempty"`
}

type reactionAggregate struct {
	Emoji string `json:"emoji"`
	Count int    `json:"count"`
	Mine  bool   `json:"mine"`
}

type Store struct {
	saveMu         sync.Mutex
	mu             sync.RWMutex
	selfID         string
	threads        map[string]*threadRec
	messages       map[string]map[string]*Message
	msgThread      map[string]string
	contacts       map[string]*contactRec
	hasMoreBefore  map[string]bool
	hybrid         map[int64]int64
	hybridSeen     map[int64]uint64
	hybridSeq      uint64
	media          map[string]*mediaRec
	nicknames      map[string]map[string]string
	reactions      map[string]map[string]string
	reactionsV2    map[string]map[string]reactionAggregate
	reactionTimes  map[string]map[string]int64
	receipts       map[string]map[string]int64
	deliveries     map[string]map[string]int64
	themeGradients map[string]map[string]int64
	polls          map[string]*pollRec
	pollIndex      map[string]string
	presence       map[string]presenceRec
	selfName       string
	gen            uint64
	savedGen       uint64
}

func (s *Store) SetReadReceipt(threadID, userID string, readMS int64) bool {
	if userID == "" || readMS <= 0 {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if userID == s.selfID || s.receipts[threadID][userID] >= readMS {
		return false
	}
	if s.receipts[threadID] == nil {
		s.receipts[threadID] = make(map[string]int64)
	}
	s.receipts[threadID][userID] = readMS
	s.gen++
	return true
}

func (s *Store) displayNameFor(threadID, userID, fallback string) string {
	if nick := s.nicknames[threadID][userID]; nick != "" {
		return nick
	}
	if c := s.contacts[userID]; c != nil && c.Name != "" {
		return c.Name
	}
	return fallback
}

func (s *Store) readReceiptsLocked(threadID string) []ReadReceipt {
	out := make([]ReadReceipt, 0, len(s.receipts[threadID]))
	users := make(map[string]bool, len(s.receipts[threadID])+len(s.deliveries[threadID]))
	for userID := range s.receipts[threadID] {
		users[userID] = true
	}
	for userID := range s.deliveries[threadID] {
		users[userID] = true
	}
	for userID := range users {
		if userID == s.selfID {
			continue
		}
		readMS := s.receipts[threadID][userID]
		receipt := ReadReceipt{UserID: userID, Name: s.displayNameFor(threadID, userID, ""), ReadMS: readMS}
		if delivered := s.deliveries[threadID][userID]; delivered > 0 {
			receipt.DeliveredMS = max(delivered, readMS)
		}
		if c := s.contacts[userID]; c != nil {
			receipt.AvatarURL = c.AvatarURL
		}
		out = append(out, receipt)
	}
	slices.SortFunc(out, func(a, b ReadReceipt) int {
		if c := cmp.Compare(b.ReadMS, a.ReadMS); c != 0 {
			return c
		}
		return cmp.Compare(a.UserID, b.UserID)
	})
	return out
}

func (s *Store) replyPreviewLocked(msg Message) *ReplyPreview {
	if msg.ReplyToID == "" {
		return nil
	}
	if threadID, ok := s.msgThread[msg.ReplyToID]; ok {
		if quoted, ok := s.messages[threadID][msg.ReplyToID]; ok {
			preview := &ReplyPreview{
				ID:         quoted.ID,
				SenderName: s.displayNameFor(quoted.ThreadID, quoted.SenderID, quoted.SenderName),
				Text:       quoted.Text,
			}
			if len(quoted.Attachments) > 0 {
				preview.Kind = quoted.Attachments[0].Kind
			}
			return preview
		}
	}
	return msg.ReplyTo
}

func searchableText(msg *Message) string {
	parts := []string{msg.Text}
	for _, att := range msg.Attachments {
		parts = append(parts, att.Title, att.Description, att.FileName)
	}
	return foldText(strings.Join(parts, "\n"))
}

func (s *Store) Search(query, threadID string, limit int) []SearchHit {
	return s.SearchFiltered(SearchFilter{Query: query, ThreadID: threadID}, limit)
}

func (s *Store) SearchFiltered(f SearchFilter, limit int) []SearchHit {
	needle := foldText(strings.TrimSpace(f.Query))
	s.mu.RLock()
	defer s.mu.RUnlock()
	var matches []*Message
	for tid, msgs := range s.messages {
		if f.ThreadID != "" && tid != f.ThreadID {
			continue
		}
		for _, msg := range msgs {
			if !msg.Deleted && f.matches(msg, needle, s.selfID) {
				matches = append(matches, msg)
			}
		}
	}
	slices.SortFunc(matches, func(a, b *Message) int {
		if c := cmp.Compare(b.TimestampMS, a.TimestampMS); c != 0 {
			return c
		}
		return cmp.Compare(b.ID, a.ID)
	})
	if limit > 0 && len(matches) > limit {
		matches = matches[:limit]
	}
	out := make([]SearchHit, len(matches))
	for i, msg := range matches {
		out[i] = SearchHit{ThreadID: msg.ThreadID, Message: s.decorateLocked(*msg)}
	}
	return out
}

func (s *Store) MediaItems(threadID string, beforeMS int64, kinds []string) []MediaItem {
	wanted := make(map[string]bool, len(kinds))
	for _, kind := range kinds {
		wanted[kind] = true
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	sorted := s.sortedMessages(threadID)
	var out []MediaItem
	for i := len(sorted) - 1; i >= 0; i-- {
		msg := sorted[i]
		if msg.Deleted || (beforeMS > 0 && msg.TimestampMS >= beforeMS) {
			continue
		}
		for _, att := range msg.Attachments {
			if !wanted[att.Kind] {
				continue
			}
			out = append(out, MediaItem{
				MessageID:   msg.ID,
				ThreadID:    msg.ThreadID,
				TimestampMS: msg.TimestampMS,
				SenderName:  s.displayNameFor(msg.ThreadID, msg.SenderID, msg.SenderName),
				IsOutgoing:  msg.IsOutgoing,
				Attachment:  att,
			})
		}
	}
	return out
}

func (s *Store) SetNickname(threadID, contactID, nickname string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.nicknames[threadID][contactID] == nickname {
		return false
	}
	if nickname == "" {
		delete(s.nicknames[threadID], contactID)
		if len(s.nicknames[threadID]) == 0 {
			delete(s.nicknames, threadID)
		}
	} else {
		if s.nicknames[threadID] == nil {
			s.nicknames[threadID] = make(map[string]string)
		}
		s.nicknames[threadID][contactID] = nickname
	}
	s.gen++
	return true
}

func (s *Store) Nickname(threadID, contactID string) string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.nicknames[threadID][contactID]
}

func (s *Store) SetReaction(messageID, actorID, emoji string, timestampMS int64) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.reactions[messageID][actorID] == emoji {
		if emoji == "" || timestampMS <= s.reactionTimes[messageID][actorID] {
			return false
		}
		if s.reactionTimes[messageID] == nil {
			s.reactionTimes[messageID] = make(map[string]int64)
		}
		s.reactionTimes[messageID][actorID] = timestampMS
		s.gen++
		return true
	}
	if emoji == "" {
		if _, ok := s.reactions[messageID][actorID]; !ok {
			return false
		}
		delete(s.reactions[messageID], actorID)
		delete(s.reactionTimes[messageID], actorID)
		if len(s.reactions[messageID]) == 0 {
			delete(s.reactions, messageID)
			delete(s.reactionTimes, messageID)
		}
	} else {
		if s.reactions[messageID] == nil {
			s.reactions[messageID] = make(map[string]string)
		}
		s.reactions[messageID][actorID] = emoji
		if s.reactionTimes[messageID] == nil {
			s.reactionTimes[messageID] = make(map[string]int64)
		}
		if timestampMS > 0 {
			s.reactionTimes[messageID][actorID] = timestampMS
		} else {
			delete(s.reactionTimes[messageID], actorID)
		}
	}
	s.gen++
	return true
}

func (s *Store) SetSelfName(name string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.selfName = name
}

func (s *Store) reactorLocked(threadID, actorID string, timestampMS int64) Reactor {
	r := Reactor{UserID: actorID, Name: s.displayNameFor(threadID, actorID, ""), TimestampMS: timestampMS}
	if r.Name == "" && actorID == s.selfID {
		r.Name = s.selfName
	}
	if c := s.contacts[actorID]; c != nil {
		r.AvatarURL = c.AvatarURL
	}
	return r
}

func (s *Store) SetReactionV2(messageID, reactionID string, agg *reactionAggregate) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	old, exists := s.reactionsV2[messageID][reactionID]
	if agg == nil {
		if !exists {
			return false
		}
		delete(s.reactionsV2[messageID], reactionID)
		if len(s.reactionsV2[messageID]) == 0 {
			delete(s.reactionsV2, messageID)
		}
	} else {
		if exists && old == *agg {
			return false
		}
		if s.reactionsV2[messageID] == nil {
			s.reactionsV2[messageID] = make(map[string]reactionAggregate)
		}
		s.reactionsV2[messageID][reactionID] = *agg
	}
	s.gen++
	return true
}

func (s *Store) reactionsLocked(threadID, messageID string) []Reaction {
	counts := make(map[string]*Reaction)
	var order []string
	add := func(emoji string, count int, mine bool) {
		r, ok := counts[emoji]
		if !ok {
			r = &Reaction{Emoji: emoji}
			counts[emoji] = r
			order = append(order, emoji)
		}
		r.Count += count
		r.Mine = r.Mine || mine
	}
	if v2 := s.reactionsV2[messageID]; len(v2) > 0 {
		for _, agg := range v2 {
			add(agg.Emoji, agg.Count, agg.Mine)
		}
	} else {
		for actor, emoji := range s.reactions[messageID] {
			add(emoji, 1, actor == s.selfID)
		}
	}
	for actor, emoji := range s.reactions[messageID] {
		r, ok := counts[emoji]
		if !ok {
			continue
		}
		r.Reactors = append(r.Reactors, s.reactorLocked(threadID, actor, s.reactionTimes[messageID][actor]))
		r.Mine = r.Mine || actor == s.selfID
	}
	out := make([]Reaction, 0, len(order))
	for _, emoji := range order {
		r := counts[emoji]
		if r.Reactors == nil {
			r.Reactors = []Reactor{}
		}
		slices.SortFunc(r.Reactors, func(a, b Reactor) int {
			if c := cmp.Compare(b.TimestampMS, a.TimestampMS); c != 0 {
				return c
			}
			return cmp.Compare(a.UserID, b.UserID)
		})
		r.Count = max(r.Count, len(r.Reactors))
		out = append(out, *r)
	}
	slices.SortFunc(out, func(a, b Reaction) int {
		if c := cmp.Compare(b.Count, a.Count); c != 0 {
			return c
		}
		return cmp.Compare(a.Emoji, b.Emoji)
	})
	return out
}

func (s *Store) decorateLocked(msg Message) Message {
	if nick := s.nicknames[msg.ThreadID][msg.SenderID]; nick != "" {
		msg.SenderName = nick
	} else if msg.SenderName == "" {
		if c := s.contacts[msg.SenderID]; c != nil {
			msg.SenderName = c.Name
		}
	}
	msg.Reactions = s.reactionsLocked(msg.ThreadID, msg.ID)
	msg.Poll = s.pollViewLocked(msg.ThreadID, msg.ID)
	msg.ReplyTo = s.replyPreviewLocked(msg)
	return msg.normalized()
}

func (s *Store) Decorate(msg Message) Message {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.decorateLocked(msg)
}

func mediaKey(messageID, attachmentID string) string {
	return messageID + "\x00" + attachmentID
}

func (s *Store) PutMedia(messageID, attachmentID string, rec mediaRec) {
	s.mu.Lock()
	defer s.mu.Unlock()
	key := mediaKey(messageID, attachmentID)
	if old, ok := s.media[key]; ok && old.Path != "" {
		rec.Path = old.Path
	}
	s.media[key] = &rec
	s.gen++
}

func (s *Store) Media(messageID, attachmentID string) (mediaRec, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	rec, ok := s.media[mediaKey(messageID, attachmentID)]
	if !ok {
		return mediaRec{}, false
	}
	return *rec, true
}

func (s *Store) SetMediaPath(messageID, attachmentID, path, mimeType string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	rec, ok := s.media[mediaKey(messageID, attachmentID)]
	if !ok {
		rec = &mediaRec{}
		s.media[mediaKey(messageID, attachmentID)] = rec
	}
	rec.Path = path
	if mimeType != "" {
		rec.MimeType = mimeType
	}
	s.gen++
}

func (s *Store) MessageInThread(threadID, messageID string) (Message, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	msg, ok := s.messages[threadID][messageID]
	if !ok {
		return Message{}, false
	}
	return *msg, true
}

func NewStore() *Store {
	s := &Store{}
	s.reset()
	return s
}

func (s *Store) reset() {
	s.threads = make(map[string]*threadRec)
	s.messages = make(map[string]map[string]*Message)
	s.msgThread = make(map[string]string)
	s.contacts = make(map[string]*contactRec)
	s.hasMoreBefore = make(map[string]bool)
	s.hybrid = make(map[int64]int64)
	s.hybridSeen = make(map[int64]uint64)
	s.media = make(map[string]*mediaRec)
	s.nicknames = make(map[string]map[string]string)
	s.reactions = make(map[string]map[string]string)
	s.reactionsV2 = make(map[string]map[string]reactionAggregate)
	s.reactionTimes = make(map[string]map[string]int64)
	s.receipts = make(map[string]map[string]int64)
	s.deliveries = make(map[string]map[string]int64)
	s.themeGradients = make(map[string]map[string]int64)
	s.polls = make(map[string]*pollRec)
	s.pollIndex = make(map[string]string)
	s.presence = make(map[string]presenceRec)
}

func (s *Store) Clear() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.selfID = ""
	s.reset()
	s.gen++
}

func (s *Store) SetSelfID(id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.selfID != id {
		s.selfID = id
		s.gen++
	}
}

func (s *Store) SelfID() string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.selfID
}

func (s *Store) peerOf(rec *threadRec) string {
	if rec.IsGroup {
		return ""
	}
	if rec.PeerID != "" {
		return rec.PeerID
	}
	members := make([]string, 0, len(rec.Members))
	for member := range rec.Members {
		if member != s.selfID {
			members = append(members, member)
		}
	}
	slices.Sort(members)
	if len(members) > 0 {
		return members[0]
	}
	return ""
}

func (s *Store) displayName(rec *threadRec) string {
	if !rec.IsGroup {
		peer := s.peerOf(rec)
		if nick := s.nicknames[rec.ID][peer]; nick != "" {
			return nick
		}
		if rec.Name != "" {
			return rec.Name
		}
		if c := s.contacts[peer]; c != nil && c.Name != "" {
			return c.Name
		}
		return peer
	}
	if rec.Name != "" {
		return rec.Name
	}
	members := make([]string, 0, len(rec.Members))
	for member := range rec.Members {
		if member != s.selfID {
			members = append(members, member)
		}
	}
	slices.Sort(members)
	names := make([]string, 0, 4)
	for _, member := range members {
		if len(names) == 4 {
			break
		}
		c := s.contacts[member]
		if c == nil {
			continue
		}
		if c.FirstName != "" {
			names = append(names, c.FirstName)
		} else if c.Name != "" {
			names = append(names, c.Name)
		}
	}
	return strings.Join(names, ", ")
}

func (s *Store) memberAvatarsLocked(rec *threadRec) []string {
	out := make([]string, 0, 2)
	seen := map[string]bool{s.selfID: true}
	add := func(id string) {
		if len(out) == 2 || seen[id] {
			return
		}
		seen[id] = true
		if c := s.contacts[id]; c != nil && c.AvatarURL != "" {
			out = append(out, c.AvatarURL)
		}
	}
	sorted := s.sortedMessages(rec.ID)
	for i := len(sorted) - 1; i >= 0 && len(out) < 2; i-- {
		add(sorted[i].SenderID)
	}
	members := make([]string, 0, len(rec.Members))
	for member := range rec.Members {
		members = append(members, member)
	}
	slices.Sort(members)
	for _, member := range members {
		add(member)
	}
	return out
}

func (s *Store) view(rec *threadRec) Thread {
	avatar := rec.AvatarURL
	peer := s.peerOf(rec)
	if avatar == "" && !rec.IsGroup {
		if c := s.contacts[peer]; c != nil {
			avatar = c.AvatarURL
		}
	}
	memberAvatars := []string{}
	if rec.IsGroup && avatar == "" {
		memberAvatars = s.memberAvatarsLocked(rec)
	}
	snippet, lastActivity := rec.Snippet, rec.LastActivityMS
	if latest := s.latestMessageLocked(rec.ID); latest != nil {
		if latest.TimestampMS > lastActivity {
			lastActivity = latest.TimestampMS
			if text := snippetFor(*latest); text != "" {
				snippet = text
			}
		} else if snippet == "" {
			snippet = snippetFor(*latest)
		}
	}
	view := Thread{
		ID:             rec.ID,
		Name:           s.displayName(rec),
		AvatarURL:      avatar,
		MemberAvatars:  memberAvatars,
		PeerID:         peer,
		Snippet:        snippet,
		LastActivityMS: lastActivity,
		UnreadCount:    rec.UnreadCount,
		IsGroup:        rec.IsGroup,
		E2EE:           rec.E2EE,
		Muted:          isMuted(rec.MuteExpireMS, time.Now().UnixMilli()),
		ReadReceipts:   s.readReceiptsLocked(rec.ID),
		Participants:   s.participantsLocked(rec),
	}
	s.applyThreadSettings(rec, &view)
	if !rec.IsGroup && peer != "" {
		view.PeerOnline, view.PeerLastActiveMS = s.presenceLocked(peer)
	}
	return view
}

func (s *Store) latestMessageLocked(threadID string) *Message {
	var latest *Message
	for _, msg := range s.messages[threadID] {
		if latest == nil || msg.TimestampMS > latest.TimestampMS || (msg.TimestampMS == latest.TimestampMS && msg.ID > latest.ID) {
			latest = msg
		}
	}
	return latest
}

func (s *Store) ThreadView(id string) (Thread, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	rec, ok := s.threads[id]
	if !ok {
		return Thread{}, false
	}
	return s.view(rec), true
}

func (s *Store) ThreadViews(ids []string) []Thread {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]Thread, 0, len(ids))
	seen := make(map[string]bool, len(ids))
	for _, id := range ids {
		if seen[id] {
			continue
		}
		seen[id] = true
		if rec, ok := s.threads[id]; ok {
			out = append(out, s.view(rec))
		}
	}
	return out
}

func (s *Store) Thread(id string) (threadRec, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	rec, ok := s.threads[id]
	if !ok {
		return threadRec{}, false
	}
	return *rec, true
}

func (s *Store) HasThread(id string) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	_, ok := s.threads[id]
	return ok
}

func (s *Store) ListThreads(limit int) []Thread {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]Thread, 0, len(s.threads))
	for _, rec := range s.threads {
		out = append(out, s.view(rec))
	}
	slices.SortFunc(out, func(a, b Thread) int {
		if c := cmp.Compare(b.LastActivityMS, a.LastActivityMS); c != 0 {
			return c
		}
		return cmp.Compare(a.ID, b.ID)
	})
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out
}

type ThreadStats struct {
	ByType          map[int64]int
	OneToOne        int
	OneToOneNoName  int
	E2EEOneToOne    int
	E2EENoName      int
	E2EENoActivity  int
	ContactsMissing []string
}

func (s *Store) Stats() ThreadStats {
	s.mu.RLock()
	defer s.mu.RUnlock()
	st := ThreadStats{ByType: make(map[int64]int)}
	for _, rec := range s.threads {
		st.ByType[rec.ThreadType]++
		if rec.IsGroup {
			continue
		}
		name := s.displayName(rec)
		unnamed := name == "" || name == rec.PeerID
		st.OneToOne++
		if unnamed {
			st.OneToOneNoName++
			if rec.PeerID != "" && rec.PeerID != s.selfID {
				st.ContactsMissing = append(st.ContactsMissing, rec.PeerID)
			}
		}
		if rec.E2EE {
			st.E2EEOneToOne++
			if unnamed {
				st.E2EENoName++
			}
			if rec.LastActivityMS == 0 && s.latestMessageLocked(rec.ID) == nil {
				st.E2EENoActivity++
			}
		}
	}
	slices.Sort(st.ContactsMissing)
	return st
}

func (s *Store) ThreadCount() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.threads)
}

func (s *Store) UpdateExistingThread(id string, fn func(rec *threadRec)) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	rec, ok := s.threads[id]
	if !ok {
		return false
	}
	fn(rec)
	s.gen++
	return true
}

func (s *Store) UpdateThread(id string, fn func(rec *threadRec)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	rec, ok := s.threads[id]
	if !ok {
		rec = &threadRec{ID: id}
		s.threads[id] = rec
	}
	fn(rec)
	s.gen++
}

func (s *Store) RemoveThread(id string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.threads[id]; !ok {
		return false
	}
	delete(s.threads, id)
	for msgID := range s.messages[id] {
		delete(s.msgThread, msgID)
	}
	delete(s.messages, id)
	delete(s.hasMoreBefore, id)
	s.gen++
	return true
}

func (s *Store) ThreadsReferencingContact(contactID string) []string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var out []string
	for id, rec := range s.threads {
		if rec.PeerID == contactID || rec.Members[contactID] {
			out = append(out, id)
		}
	}
	return out
}

func (s *Store) SetContact(id, name, firstName, avatar string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	c, ok := s.contacts[id]
	if !ok {
		c = &contactRec{}
		s.contacts[id] = c
	}
	changed := !ok
	if name != "" && c.Name != name {
		c.Name = name
		changed = true
	}
	if firstName != "" && c.FirstName != firstName {
		c.FirstName = firstName
		changed = true
	}
	if avatar != "" && c.AvatarURL != avatar {
		c.AvatarURL = avatar
		changed = true
	}
	if changed {
		s.gen++
	}
	return changed
}

func (s *Store) ContactName(id string) string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if c := s.contacts[id]; c != nil {
		return c.Name
	}
	return ""
}

func (s *Store) Contact(id string) (contactRec, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if c := s.contacts[id]; c != nil {
		return *c, true
	}
	return contactRec{}, false
}

func keepUnsentContent(prev, msg Message) Message {
	if !prev.Deleted && !msg.Deleted {
		return msg
	}
	msg.Deleted = true
	if msg.Text == "" && len(msg.Attachments) == 0 {
		msg.Text = prev.Text
		msg.Attachments = prev.Attachments
		if len(msg.Mentions) == 0 {
			msg.Mentions = prev.Mentions
		}
	}
	if msg.Attachments == nil {
		msg.Attachments = []Attachment{}
	}
	return msg
}

func (s *Store) MarkUnsent(messageID string) (Message, bool) {
	return s.UpdateMessage(messageID, func(m *Message) bool {
		if m.Deleted {
			return false
		}
		m.Deleted = true
		return true
	})
}

func (s *Store) PutMessage(msg Message) (prev Message, existed bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if oldThread, ok := s.msgThread[msg.ID]; ok {
		if old, ok := s.messages[oldThread][msg.ID]; ok {
			prev = *old
			existed = true
			msg = keepUnsentContent(prev, msg)
			if oldThread != msg.ThreadID {
				delete(s.messages[oldThread], msg.ID)
			}
		}
	}
	msgs, ok := s.messages[msg.ThreadID]
	if !ok {
		msgs = make(map[string]*Message)
		s.messages[msg.ThreadID] = msgs
	}
	stored := msg
	stored.ClientID = ""
	stored.Reactions = nil
	stored.Poll = nil
	msgs[msg.ID] = &stored
	s.msgThread[msg.ID] = msg.ThreadID
	s.gen++
	return
}

func (s *Store) FindMessage(msgID string) (Message, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	threadID, ok := s.msgThread[msgID]
	if !ok {
		return Message{}, false
	}
	msg, ok := s.messages[threadID][msgID]
	if !ok {
		return Message{}, false
	}
	return *msg, true
}

func (s *Store) UpdateMessage(msgID string, fn func(msg *Message) bool) (Message, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	threadID, ok := s.msgThread[msgID]
	if !ok {
		return Message{}, false
	}
	msg, ok := s.messages[threadID][msgID]
	if !ok {
		return Message{}, false
	}
	if !fn(msg) {
		return *msg, false
	}
	s.gen++
	return *msg, true
}

func (s *Store) sortedMessages(threadID string) []*Message {
	msgs := make([]*Message, 0, len(s.messages[threadID]))
	for _, msg := range s.messages[threadID] {
		msgs = append(msgs, msg)
	}
	slices.SortFunc(msgs, func(a, b *Message) int {
		if c := cmp.Compare(a.TimestampMS, b.TimestampMS); c != 0 {
			return c
		}
		return cmp.Compare(a.ID, b.ID)
	})
	return msgs
}

func (s *Store) Messages(threadID string, beforeMS int64, limit int) []Message {
	s.mu.RLock()
	defer s.mu.RUnlock()
	sorted := s.sortedMessages(threadID)
	if beforeMS > 0 {
		cut, _ := slices.BinarySearchFunc(sorted, beforeMS, func(m *Message, ts int64) int {
			return cmp.Compare(m.TimestampMS, ts)
		})
		sorted = sorted[:cut]
	}
	if limit > 0 && len(sorted) > limit {
		start := len(sorted) - limit
		for start > 0 && sorted[start-1].TimestampMS == sorted[start].TimestampMS {
			start--
		}
		sorted = sorted[start:]
	}
	out := make([]Message, len(sorted))
	for i, msg := range sorted {
		out[i] = s.decorateLocked(*msg)
	}
	return out
}

func cutMediaItems(items []MediaItem, limit int) ([]MediaItem, bool) {
	if limit <= 0 || len(items) <= limit {
		return items, false
	}
	end := limit
	for end < len(items) && items[end].TimestampMS == items[end-1].TimestampMS {
		end++
	}
	return items[:end], end < len(items)
}

func (s *Store) MessageIDs(threadID string) map[string]bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make(map[string]bool, len(s.messages[threadID]))
	for id := range s.messages[threadID] {
		out[id] = true
	}
	return out
}

func (s *Store) MessagesNotIn(threadID string, beforeMS int64, known map[string]bool) []Message {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var out []Message
	for _, msg := range s.sortedMessages(threadID) {
		if !known[msg.ID] && msg.TimestampMS < beforeMS {
			out = append(out, s.decorateLocked(*msg))
		}
	}
	return out
}

func (s *Store) MessageCount(threadID string) int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.messages[threadID])
}

func (s *Store) OldestMessage(threadID string) (Message, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var oldest *Message
	for _, msg := range s.messages[threadID] {
		if oldest == nil || msg.TimestampMS < oldest.TimestampMS || (msg.TimestampMS == oldest.TimestampMS && msg.ID < oldest.ID) {
			oldest = msg
		}
	}
	if oldest == nil {
		return Message{}, false
	}
	return *oldest, true
}

func (s *Store) LatestTimestamp(threadID string) int64 {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var latest int64
	for _, msg := range s.messages[threadID] {
		latest = max(latest, msg.TimestampMS)
	}
	return latest
}

func (s *Store) SetHasMoreBefore(threadID string, more bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.hasMoreBefore[threadID] = more
	s.gen++
}

func (s *Store) HasMoreBefore(threadID string) (more, known bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	more, known = s.hasMoreBefore[threadID]
	return
}

func (s *Store) SetHybrid(fbKey, waKey int64) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.hybridSeq++
	s.hybridSeen[fbKey] = s.hybridSeq
	if s.hybrid[fbKey] == waKey {
		return false
	}
	s.hybrid[fbKey] = waKey
	s.gen++
	return true
}

func (s *Store) hybridKeyLocked(waKey int64, allowIdentity bool) (int64, bool) {
	var best int64
	var bestSeen uint64
	found := false
	for fb, wa := range s.hybrid {
		if wa != waKey || (fb == waKey && !allowIdentity) {
			continue
		}
		seen := s.hybridSeen[fb]
		if !found || seen > bestSeen || (seen == bestSeen && fb > best) {
			best, bestSeen, found = fb, seen, true
		}
	}
	return best, found
}

func (s *Store) MapThreadKey(key int64) int64 {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if mapped, ok := s.hybrid[key]; ok {
		return mapped
	}
	return key
}

func (s *Store) FBKeyFor(waKey int64) (int64, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.hybridKeyLocked(waKey, false)
}

func cloneNested[K comparable, K2 comparable, V any](m map[K]map[K2]V) map[K]map[K2]V {
	out := make(map[K]map[K2]V, len(m))
	for k, inner := range m {
		out[k] = maps.Clone(inner)
	}
	return out
}

func (s *Store) snapshotLocked() storeSnapshot {
	snap := storeSnapshot{
		Version:       storeSnapshotVersion,
		SelfID:        s.selfID,
		Threads:       make(map[string]*threadRec, len(s.threads)),
		Messages:      make(map[string][]Message, len(s.messages)),
		Contacts:      make(map[string]*contactRec, len(s.contacts)),
		HasMoreBefore: maps.Clone(s.hasMoreBefore),
		Hybrid:        maps.Clone(s.hybrid),
		Media:         make(map[string]*mediaRec, len(s.media)),
		Nicknames:     cloneNested(s.nicknames),
		Receipts:      cloneNested(s.receipts),
		Deliveries:    cloneNested(s.deliveries),
		Themes:        cloneNested(s.themeGradients),
		Polls:         make(map[string]*pollRec, len(s.polls)),
		Reactions:     make(map[string]map[string]string),
		ReactionsV2:   make(map[string]map[string]reactionAggregate),
		ReactionTimes: make(map[string]map[string]int64),
	}
	for id, rec := range s.threads {
		c := *rec
		c.Members = maps.Clone(rec.Members)
		c.Admins = maps.Clone(rec.Admins)
		c.Pinned = slices.Clone(rec.Pinned)
		snap.Threads[id] = &c
	}
	for id, c := range s.contacts {
		copied := *c
		snap.Contacts[id] = &copied
	}
	for id, p := range s.polls {
		c := *p
		c.Options = slices.Clone(p.Options)
		c.Votes = maps.Clone(p.Votes)
		c.VoteTimes = maps.Clone(p.VoteTimes)
		snap.Polls[id] = &c
	}
	for threadID := range s.messages {
		sorted := s.sortedMessages(threadID)
		if len(sorted) > maxPersistedMessagesPerThread {
			sorted = sorted[keepFrom(sorted, maxPersistedMessagesPerThread):]
			snap.HasMoreBefore[threadID] = true
		}
		list := make([]Message, len(sorted))
		for i, msg := range sorted {
			list[i] = *msg
			list[i].Reactions = nil
			list[i].Attachments = slices.Clone(msg.Attachments)
			list[i].Mentions = slices.Clone(msg.Mentions)
			if r := s.reactions[msg.ID]; len(r) > 0 {
				snap.Reactions[msg.ID] = maps.Clone(r)
			}
			if r := s.reactionTimes[msg.ID]; len(r) > 0 {
				snap.ReactionTimes[msg.ID] = maps.Clone(r)
			}
			if r := s.reactionsV2[msg.ID]; len(r) > 0 {
				snap.ReactionsV2[msg.ID] = maps.Clone(r)
			}
			for _, att := range msg.Attachments {
				key := mediaKey(msg.ID, att.ID)
				if rec, ok := s.media[key]; ok {
					copied := *rec
					snap.Media[key] = &copied
				}
			}
		}
		snap.Messages[threadID] = list
	}
	return snap
}

func keepFrom(sorted []*Message, keep int) int {
	start := len(sorted) - keep
	for start > 0 && sorted[start-1].TimestampMS == sorted[start].TimestampMS {
		start--
	}
	return start
}

func (s *Store) Save(path string) error {
	s.saveMu.Lock()
	defer s.saveMu.Unlock()
	s.mu.RLock()
	gen := s.gen
	if gen == s.savedGen {
		s.mu.RUnlock()
		return nil
	}
	snap := s.snapshotLocked()
	s.mu.RUnlock()
	data, err := json.Marshal(&snap)
	if err != nil {
		return err
	}
	if err = writeStoreFile(path, data); err != nil {
		return err
	}
	s.mu.Lock()
	s.savedGen = gen
	s.mu.Unlock()
	return nil
}

func (s *Store) Generation() uint64 {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.gen
}

func (s *Store) TrimThread(threadID string, keep int, retained map[string]bool) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	sorted := s.sortedMessages(threadID)
	if len(sorted) <= keep {
		return 0
	}
	dropped := sorted[:keepFrom(sorted, keep)]
	for i, msg := range dropped {
		if retained[msg.ID] {
			dropped = dropped[:i]
			break
		}
	}
	for len(dropped) > 0 && dropped[len(dropped)-1].TimestampMS == sorted[len(dropped)].TimestampMS {
		dropped = dropped[:len(dropped)-1]
	}
	for _, msg := range dropped {
		delete(s.messages[threadID], msg.ID)
		delete(s.msgThread, msg.ID)
		delete(s.reactions, msg.ID)
		delete(s.reactionsV2, msg.ID)
		delete(s.reactionTimes, msg.ID)
		for _, att := range msg.Attachments {
			delete(s.media, mediaKey(msg.ID, att.ID))
		}
	}
	if len(dropped) > 0 {
		s.hasMoreBefore[threadID] = true
		s.gen++
	}
	return len(dropped)
}

func (s *Store) RebaseMediaPaths(from, to string) int {
	from, to = filepath.Clean(from), filepath.Clean(to)
	if from == to {
		return 0
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	moved := 0
	for _, rec := range s.media {
		if rec.Path == "" {
			continue
		}
		rel, err := filepath.Rel(from, rec.Path)
		if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			continue
		}
		target := filepath.Join(to, rel)
		if fileExists(rec.Path) || !fileExists(target) {
			continue
		}
		rec.Path = target
		moved++
	}
	if moved > 0 {
		s.gen++
	}
	return moved
}

func (s *Store) Load(path string) error {
	data, err := readSecretFile(path, zerolog.Nop())
	if err != nil {
		return err
	}
	var snap storeSnapshot
	if err = json.Unmarshal(data, &snap); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.reset()
	s.selfID = snap.SelfID
	for id, rec := range snap.Threads {
		if rec != nil {
			rec.ID = id
			s.threads[id] = rec
		}
	}
	for threadID, msgs := range snap.Messages {
		m := make(map[string]*Message, len(msgs))
		for i := range msgs {
			msg := msgs[i]
			msg.ThreadID = threadID
			for j := range msg.Attachments {
				if msg.Attachments[j].ID == "" {
					msg.Attachments[j].ID = "a" + strconv.Itoa(j)
				}
			}
			m[msg.ID] = &msg
			s.msgThread[msg.ID] = threadID
		}
		s.messages[threadID] = m
	}
	for id, c := range snap.Contacts {
		if c != nil {
			s.contacts[id] = c
		}
	}
	for id, more := range snap.HasMoreBefore {
		s.hasMoreBefore[id] = more
	}
	for fb, wa := range snap.Hybrid {
		s.hybrid[fb] = wa
	}
	for key, rec := range snap.Media {
		if rec != nil {
			s.media[key] = rec
		}
	}
	for thread, receipts := range snap.Receipts {
		if len(receipts) > 0 {
			s.receipts[thread] = receipts
		}
	}
	for thread, deliveries := range snap.Deliveries {
		if len(deliveries) > 0 {
			s.deliveries[thread] = deliveries
		}
	}
	for id, stops := range snap.Themes {
		if len(stops) > 0 {
			s.themeGradients[id] = stops
		}
	}
	for id, p := range snap.Polls {
		if p != nil {
			p.ID = id
			s.polls[id] = p
			if p.MessageID != "" {
				s.pollIndex[p.MessageID] = id
			}
		}
	}
	for thread, nicks := range snap.Nicknames {
		if len(nicks) > 0 {
			s.nicknames[thread] = nicks
		}
	}
	for msgID, r := range snap.Reactions {
		if len(r) > 0 {
			s.reactions[msgID] = r
		}
	}
	for msgID, r := range snap.ReactionsV2 {
		if len(r) > 0 {
			s.reactionsV2[msgID] = r
		}
	}
	for msgID, r := range snap.ReactionTimes {
		if len(r) > 0 {
			s.reactionTimes[msgID] = r
		}
	}
	s.gen++
	s.savedGen = s.gen
	return nil
}

var atomicWriteLocks sync.Map

func renameWithRetry(from, to string) error {
	var err error
	for attempt := 0; attempt < 5; attempt++ {
		if err = os.Rename(from, to); err == nil {
			return nil
		}
		time.Sleep(time.Duration(attempt+1) * 20 * time.Millisecond)
	}
	return err
}

func pathLock(path string) *sync.Mutex {
	lock, _ := atomicWriteLocks.LoadOrStore(filepath.Clean(path), &sync.Mutex{})
	return lock.(*sync.Mutex)
}

func writeTempSibling(path string, data []byte) (string, error) {
	f, err := os.CreateTemp(filepath.Dir(path), filepath.Base(path)+".*.tmp")
	if err != nil {
		return "", err
	}
	tmp := f.Name()
	if _, err = f.Write(data); err == nil {
		err = f.Sync()
	}
	if closeErr := f.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		_ = os.Remove(tmp)
		return "", err
	}
	return tmp, nil
}

func writeFileAtomic(path string, data []byte) error {
	mu := pathLock(path)
	mu.Lock()
	defer mu.Unlock()
	tmp, err := writeTempSibling(path, data)
	if err != nil {
		return err
	}
	if err = renameWithRetry(tmp, path); err != nil {
		_ = os.Remove(tmp)
	}
	return err
}
