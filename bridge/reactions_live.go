package main

import (
	"slices"
	"strings"
	"time"
)

const (
	EventReactionAdded = "reaction_added"

	reactionPreviewMaxRunes  = 80
	demoReactionChanceOutOf  = 3
	demoReactionCandidateMax = 20
)

type ReactionAddedEvent struct {
	ThreadID      string `json:"thread_id"`
	MessageID     string `json:"message_id"`
	Emoji         string `json:"emoji"`
	ActorID       string `json:"actor_id"`
	ActorName     string `json:"actor_name"`
	TargetPreview string `json:"target_preview"`
}

func (s *Store) ReactionOf(messageID, actorID string) string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.reactions[messageID][actorID]
}

func (s *Store) ReactorName(threadID, actorID string) string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.reactorLocked(threadID, actorID, 0).Name
}

func reactionAttachmentLabel(att Attachment) string {
	switch att.Kind {
	case "image", "gif":
		return "Photo"
	case "video":
		return "Vidéo"
	case "audio":
		if att.Voice {
			return "Message vocal"
		}
		return "Fichier"
	case "file":
		return "Fichier"
	case "link":
		return "Lien"
	case "sticker":
		return "Autocollant"
	default:
		return "Pièce jointe"
	}
}

func reactionTargetPreview(msg Message) string {
	if text := strings.TrimSpace(msg.Text); text != "" {
		runes := []rune(text)
		if len(runes) > reactionPreviewMaxRunes {
			runes = runes[:reactionPreviewMaxRunes]
		}
		return string(runes)
	}
	if len(msg.Attachments) > 0 {
		return reactionAttachmentLabel(msg.Attachments[0])
	}
	return ""
}

func (s *Store) reactionAddedEvent(prevEmoji, messageID, actorID, emoji string) (ReactionAddedEvent, bool) {
	if emoji == "" || emoji == prevEmoji || actorID == "" || actorID == s.SelfID() {
		return ReactionAddedEvent{}, false
	}
	msg, ok := s.FindMessage(messageID)
	if !ok || !msg.IsOutgoing {
		return ReactionAddedEvent{}, false
	}
	return ReactionAddedEvent{
		ThreadID:      msg.ThreadID,
		MessageID:     msg.ID,
		Emoji:         emoji,
		ActorID:       actorID,
		ActorName:     s.ReactorName(msg.ThreadID, actorID),
		TargetPreview: reactionTargetPreview(msg),
	}, true
}

func (tc *tableContext) liveReaction(messageID string, timestampMS int64) bool {
	if tc.mode != modeLive || tc.b.lsCatchingUp.Load() || tc.historyMsgs[messageID] {
		return false
	}
	return timestampMS <= 0 || isLiveTimestamp(timestampMS)
}

func (b *MetaBackend) liveE2EEReaction(timestampMS int64) bool {
	return isLiveTimestamp(timestampMS) && !b.waOfflineSyncing.Load()
}

func (d *DemoBackend) simulateReaction() bool {
	d.mu.Lock()
	var candidates []Message
	for _, th := range d.store.ListThreads(8) {
		for _, msg := range d.store.Messages(th.ID, 0, demoReactionCandidateMax) {
			if msg.IsOutgoing && !msg.Deleted && !msg.System && len(d.members[th.ID]) > 0 {
				candidates = append(candidates, msg)
			}
		}
	}
	if len(candidates) == 0 {
		d.mu.Unlock()
		return false
	}
	msg := candidates[d.rng.IntN(len(candidates))]
	members := d.members[msg.ThreadID]
	actor := members[d.rng.IntN(len(members))]
	emoji := demoReactionEmojis[d.rng.IntN(len(demoReactionEmojis))]
	prev := d.store.ReactionOf(msg.ID, actor)
	if prev == emoji {
		emoji = demoReactionEmojis[(slices.Index(demoReactionEmojis, emoji)+1)%len(demoReactionEmojis)]
	}
	d.store.SetReaction(msg.ID, actor, emoji, time.Now().UnixMilli())
	evt, ok := d.store.reactionAddedEvent(prev, msg.ID, actor, emoji)
	updated, found := d.store.MessageInThread(msg.ThreadID, msg.ID)
	d.mu.Unlock()
	if found {
		d.out.Emit(EventMessageUpdate, d.store.Decorate(updated))
	}
	if ok {
		d.out.Emit(EventReactionAdded, evt)
	}
	return ok
}
