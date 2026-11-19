package main

import (
	"cmp"
	"slices"
)

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
		s.gen++
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
	s.gen++
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
