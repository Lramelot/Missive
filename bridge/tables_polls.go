package main

import (
	"strings"

	"go.mau.fi/mautrix-meta/pkg/messagix/table"
)

func pollOptionsOf(opts []table.PollOption) []pollOptionRec {
	out := make([]pollOptionRec, 0, len(opts))
	for _, o := range opts {
		if o.OptionID != 0 {
			out = append(out, pollOptionRec{ID: itoa(o.OptionID), Text: o.Text})
		}
	}
	return out
}

func (tc *tableContext) applyPoll(threadID string, wm *table.WrappedMessage) bool {
	if wm.PollID == 0 {
		return false
	}
	store := tc.b.store
	pollID := itoa(wm.PollID)
	switch wm.PollEventType {
	case table.PollEventCreate:
		store.UpsertPoll(pollID, threadID, wm.MessageId, wm.PollQuestion, pollOptionsOf(wm.PollOptions), wm.PollOptionsComplete)
		return false
	case table.PollEventAddOption:
		if store.UpsertPoll(pollID, threadID, "", wm.PollQuestion, pollOptionsOf(wm.PollOptions), false) {
			tc.pollChanged(pollID)
		}
		return true
	case table.PollEventVote:
		voter := itoa(wm.SenderId)
		selection := store.PollVotes(pollID, voter)
		for _, r := range wm.PollRemovals {
			if r.ContactID == wm.SenderId {
				selection = removeString(selection, itoa(r.OptionID))
			}
		}
		for _, v := range wm.PollVotes {
			if v.ContactID == wm.SenderId {
				selection = append(selection, itoa(v.OptionID))
			}
		}
		store.UpsertPoll(pollID, threadID, "", "", nil, false)
		if store.SetPollVotes(pollID, voter, selection, wm.TimestampMs) {
			tc.pollChanged(pollID)
		}
		return strings.TrimSpace(wm.Text) == ""
	}
	return false
}

func removeString(list []string, value string) []string {
	out := list[:0]
	for _, v := range list {
		if v != value {
			out = append(out, v)
		}
	}
	return out
}

func (tc *tableContext) pollChanged(pollID string) {
	p, ok := tc.b.store.PollRecord(pollID)
	if !ok || p.MessageID == "" {
		return
	}
	if msg, ok := tc.b.store.FindMessage(p.MessageID); ok && tc.mode == modeLive {
		tc.b.emitMessage(EventMessageUpdate, msg)
	}
}

func (tc *tableContext) handlePollSnapshots(tbl *table.LSTable) {
	store := tc.b.store
	for _, data := range tbl.WrapPollData() {
		pollID := itoa(data.PollID)
		changed := store.UpsertPoll(pollID, "", "", "", pollOptionsOf(data.Options), true)
		for voter, options := range data.Votes {
			ids := make([]string, 0, len(options))
			for _, o := range options {
				ids = append(ids, itoa(o))
			}
			if store.SetPollVotes(pollID, itoa(voter), ids, data.VoteTimestamps[voter]) {
				changed = true
			}
		}
		if changed {
			tc.pollChanged(pollID)
		}
	}
}
