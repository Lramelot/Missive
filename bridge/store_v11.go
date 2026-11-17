package main

import (
	"cmp"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"
)

type pinRec struct {
	MessageID string `json:"message_id"`
	PinnedMS  int64  `json:"pinned_ms"`
}

type pollOptionRec struct {
	ID   string `json:"id"`
	Text string `json:"text"`
}

type pollRec struct {
	ID        string              `json:"id"`
	ThreadID  string              `json:"thread_id"`
	MessageID string              `json:"message_id"`
	Question  string              `json:"question"`
	Options   []pollOptionRec     `json:"options"`
	Votes     map[string][]string `json:"votes,omitempty"`
	VoteTimes map[string]int64    `json:"vote_times,omitempty"`
}

func colorHex(argb int64) string {
	if argb == 0 {
		return ""
	}
	return fmt.Sprintf("#%06X", argb&0xFFFFFF)
}

func (s *Store) SetThemeGradient(themeID, index, color int64) bool {
	if themeID == 0 {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	key := strconv.FormatInt(themeID, 10)
	if s.themeGradients[key] == nil {
		s.themeGradients[key] = make(map[string]int64)
	}
	idx := strconv.FormatInt(index, 10)
	if s.themeGradients[key][idx] == color {
		return false
	}
	s.themeGradients[key][idx] = color
	s.dirty = true
	return true
}

func (s *Store) ThreadsWithTheme(themeID int64) []string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var out []string
	for id, rec := range s.threads {
		if rec.ThemeID == themeID {
			out = append(out, id)
		}
	}
	return out
}

func (s *Store) themeLocked(rec *threadRec) *ThreadTheme {
	if rec.ThemeColor == "" {
		return nil
	}
	theme := &ThreadTheme{Accent: rec.ThemeColor, Gradient: []string{}}
	stops := s.themeGradients[strconv.FormatInt(rec.ThemeID, 10)]
	if len(stops) > 1 {
		keys := make([]int64, 0, len(stops))
		for k := range stops {
			if v, err := strconv.ParseInt(k, 10, 64); err == nil {
				keys = append(keys, v)
			}
		}
		slices.Sort(keys)
		for _, k := range keys {
			if hex := colorHex(stops[strconv.FormatInt(k, 10)]); hex != "" {
				theme.Gradient = append(theme.Gradient, hex)
			}
		}
	}
	return theme
}

func mutedUntil(muteExpireMS, nowMS int64) *int64 {
	switch {
	case muteExpireMS < 0:
		v := int64(-1)
		return &v
	case muteExpireMS > nowMS:
		v := muteExpireMS
		return &v
	}
	return nil
}

func (s *Store) applyV11View(rec *threadRec, view *Thread) {
	view.Folder = rec.Folder
	if view.Folder == "" {
		view.Folder = FolderInbox
	}
	view.MutedUntilMS = mutedUntil(rec.MuteExpireMS, time.Now().UnixMilli())
	view.Theme = s.themeLocked(rec)
	view.QuickEmoji = rec.QuickEmoji
	view.EphemeralTTLMS = rec.EphemeralTTLMS
	view.PinnedMessageIDs = pinnedIDs(rec.Pinned)
	view.IsAdmin = rec.Admins[s.selfID]
	view.CanManage = canManage(rec, s.selfID)
	view.Blocked = !rec.IsGroup && rec.Blocked
}

func canManage(rec *threadRec, self string) bool {
	if !rec.IsGroup || rec.E2EE || rec.Left {
		return false
	}
	return len(rec.Members) == 0 || rec.Members[self]
}

func pinnedIDs(pins []pinRec) []string {
	sorted := slices.Clone(pins)
	slices.SortStableFunc(sorted, func(a, b pinRec) int { return cmp.Compare(b.PinnedMS, a.PinnedMS) })
	out := make([]string, 0, len(sorted))
	for _, p := range sorted {
		out = append(out, p.MessageID)
	}
	return out
}

func setPin(rec *threadRec, messageID string, pinnedMS int64) bool {
	for i, p := range rec.Pinned {
		if p.MessageID == messageID {
			if p.PinnedMS == pinnedMS {
				return false
			}
			rec.Pinned[i].PinnedMS = pinnedMS
			return true
		}
	}
	rec.Pinned = append(rec.Pinned, pinRec{MessageID: messageID, PinnedMS: pinnedMS})
	return true
}

func (s *Store) SetDeliveryReceipt(threadID, userID string, deliveredMS int64) bool {
	if userID == "" || deliveredMS <= 0 {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if userID == s.selfID || s.deliveries[threadID][userID] >= deliveredMS {
		return false
	}
	if s.deliveries[threadID] == nil {
		s.deliveries[threadID] = make(map[string]int64)
	}
	s.deliveries[threadID][userID] = deliveredMS
	s.dirty = true
	return true
}

func (s *Store) RemoveMessage(messageID string) (string, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	threadID, ok := s.msgThread[messageID]
	if !ok {
		return "", false
	}
	delete(s.messages[threadID], messageID)
	delete(s.msgThread, messageID)
	delete(s.reactions, messageID)
	delete(s.reactionsV2, messageID)
	delete(s.reactionTimes, messageID)
	if pollID, ok := s.pollIndex[messageID]; ok {
		delete(s.pollIndex, messageID)
		if p := s.polls[pollID]; p != nil && p.MessageID == messageID {
			delete(s.polls, pollID)
		}
	}
	if rec := s.threads[threadID]; rec != nil {
		rec.Pinned = slices.DeleteFunc(rec.Pinned, func(p pinRec) bool { return p.MessageID == messageID })
	}
	s.dirty = true
	return threadID, true
}

func (s *Store) UpsertPoll(pollID, threadID, messageID, question string, options []pollOptionRec, replace bool) bool {
	if pollID == "" {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	p := s.polls[pollID]
	changed := false
	if p == nil {
		p = &pollRec{ID: pollID, Votes: map[string][]string{}, VoteTimes: map[string]int64{}}
		s.polls[pollID] = p
		changed = true
	}
	if threadID != "" && p.ThreadID != threadID {
		p.ThreadID = threadID
		changed = true
	}
	if messageID != "" && p.MessageID != messageID {
		if p.MessageID != "" {
			delete(s.pollIndex, p.MessageID)
		}
		p.MessageID = messageID
		s.pollIndex[messageID] = pollID
		changed = true
	}
	if question != "" && p.Question != question {
		p.Question = question
		changed = true
	}
	if replace && len(options) > 0 {
		if !slices.Equal(p.Options, options) {
			p.Options = slices.Clone(options)
			changed = true
		}
	} else {
		for _, opt := range options {
			idx := slices.IndexFunc(p.Options, func(o pollOptionRec) bool { return o.ID == opt.ID })
			switch {
			case idx < 0:
				p.Options = append(p.Options, opt)
				changed = true
			case opt.Text != "" && p.Options[idx].Text != opt.Text:
				p.Options[idx].Text = opt.Text
				changed = true
			}
		}
	}
	if changed {
		s.dirty = true
	}
	return changed
}

func (s *Store) SetPollVotes(pollID, voterID string, optionIDs []string, timestampMS int64) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	p := s.polls[pollID]
	if p == nil || voterID == "" {
		return false
	}
	selection := slices.Clone(optionIDs)
	slices.Sort(selection)
	selection = slices.Compact(selection)
	if slices.Equal(p.Votes[voterID], selection) {
		return false
	}
	if p.Votes == nil {
		p.Votes = map[string][]string{}
		p.VoteTimes = map[string]int64{}
	}
	if len(selection) == 0 {
		delete(p.Votes, voterID)
		delete(p.VoteTimes, voterID)
	} else {
		p.Votes[voterID] = selection
		if timestampMS > 0 {
			p.VoteTimes[voterID] = timestampMS
		}
	}
	s.dirty = true
	return true
}

func (s *Store) PollVotes(pollID, voterID string) []string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if p := s.polls[pollID]; p != nil {
		return slices.Clone(p.Votes[voterID])
	}
	return nil
}

func (s *Store) PollRecord(pollID string) (pollRec, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	p := s.polls[pollID]
	if p == nil {
		return pollRec{}, false
	}
	return *p, true
}

func (s *Store) pollViewLocked(threadID, messageID string) *Poll {
	pollID, ok := s.pollIndex[messageID]
	if !ok {
		return nil
	}
	p := s.polls[pollID]
	if p == nil || len(p.Options) == 0 {
		return nil
	}
	view := &Poll{ID: p.ID, Question: p.Question, Options: make([]PollOption, 0, len(p.Options))}
	for _, opt := range p.Options {
		po := PollOption{ID: opt.ID, Text: opt.Text, Voters: []Reactor{}}
		for voter, selection := range p.Votes {
			if slices.Contains(selection, opt.ID) {
				po.Voters = append(po.Voters, s.reactorLocked(threadID, voter, p.VoteTimes[voter]))
				if voter == s.selfID {
					po.Mine = true
				}
			}
		}
		slices.SortFunc(po.Voters, func(a, b Reactor) int {
			if c := cmp.Compare(b.TimestampMS, a.TimestampMS); c != 0 {
				return c
			}
			return cmp.Compare(a.UserID, b.UserID)
		})
		po.VoteCount = len(po.Voters)
		view.TotalVotes += po.VoteCount
		view.Options = append(view.Options, po)
	}
	return view
}

func (s *Store) MessagePollID(messageID string) string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.pollIndex[messageID]
}

func (s *Store) PeerThreadID(userID string) string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	best := ""
	var bestActivity int64 = -1
	for id, rec := range s.threads {
		if rec.IsGroup || s.peerOf(rec) != userID {
			continue
		}
		if rec.LastActivityMS > bestActivity {
			best, bestActivity = id, rec.LastActivityMS
		}
	}
	return best
}

func (s *Store) SearchContacts(query string, limit int) []UserResult {
	needle := foldText(strings.TrimSpace(query))
	if needle == "" || limit <= 0 {
		return []UserResult{}
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	peers := make(map[string]string)
	for id, rec := range s.threads {
		if !rec.IsGroup {
			if peer := s.peerOf(rec); peer != "" {
				if prev, ok := peers[peer]; !ok || s.threads[prev].LastActivityMS < rec.LastActivityMS {
					peers[peer] = id
				}
			}
		}
	}
	type scored struct {
		user     UserResult
		prefix   bool
		activity int64
	}
	var hits []scored
	for id, c := range s.contacts {
		if id == s.selfID || c.Name == "" {
			continue
		}
		name := foldText(c.Name)
		if !strings.Contains(name, needle) {
			continue
		}
		user := UserResult{ID: id, Name: c.Name, AvatarURL: c.AvatarURL, ThreadID: peers[id], IsFriend: peers[id] != ""}
		var activity int64
		if rec := s.threads[peers[id]]; rec != nil {
			activity = rec.LastActivityMS
		}
		hits = append(hits, scored{user: user, prefix: strings.HasPrefix(name, needle), activity: activity})
	}
	slices.SortFunc(hits, func(a, b scored) int {
		if a.prefix != b.prefix {
			if a.prefix {
				return -1
			}
			return 1
		}
		if c := cmp.Compare(b.activity, a.activity); c != 0 {
			return c
		}
		return cmp.Compare(a.user.Name, b.user.Name)
	})
	out := make([]UserResult, 0, min(limit, len(hits)))
	for _, h := range hits {
		if len(out) == limit {
			break
		}
		out = append(out, h.user)
	}
	return out
}

func (s *Store) GroupIDs() map[string]bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make(map[string]bool)
	for id, rec := range s.threads {
		if rec.IsGroup {
			out[id] = true
		}
	}
	return out
}

func (s *Store) NewGroupWith(known map[string]bool, members []string) string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	best, bestActivity := "", int64(-1)
	for id, rec := range s.threads {
		if !rec.IsGroup || known[id] {
			continue
		}
		complete := true
		for _, m := range members {
			if !rec.Members[m] {
				complete = false
				break
			}
		}
		if complete && rec.LastActivityMS > bestActivity {
			best, bestActivity = id, rec.LastActivityMS
		}
	}
	return best
}
