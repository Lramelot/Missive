package main

import (
	"cmp"
	"slices"
	"strconv"
	"strings"
	"unicode/utf16"
	"unicode/utf8"

	"go.mau.fi/whatsmeow/proto/waCommon"
	"go.mau.fi/whatsmeow/types"
	"google.golang.org/protobuf/proto"

	"go.mau.fi/mautrix-meta/pkg/messagix/socket"
	"go.mau.fi/mautrix-meta/pkg/messagix/table"
)

func utf16ByteOffsets(text string) []int {
	offsets := make([]int, 0, len(text)+1)
	for i, r := range text {
		offsets = append(offsets, i)
		if utf16.RuneLen(r) == 2 {
			offsets = append(offsets, -1)
		}
	}
	return append(offsets, len(text))
}

func byteSpanFromUTF16(text string, offset, length int) (int, int, bool) {
	offsets := utf16ByteOffsets(text)
	units := len(offsets) - 1
	if offset < 0 || length <= 0 || offset >= units {
		return 0, 0, false
	}
	end := min(offset+length, units)
	start, stop := offsets[offset], offsets[end]
	if start < 0 || stop < 0 || stop <= start {
		return 0, 0, false
	}
	return start, stop - start, true
}

func utf16SpanFromBytes(text string, offset, length int) (int, int, bool) {
	if offset < 0 || length <= 0 || offset+length > len(text) || !runeBoundary(text, offset) || !runeBoundary(text, offset+length) {
		return 0, 0, false
	}
	start := len(utf16.Encode([]rune(text[:offset])))
	return start, len(utf16.Encode([]rune(text[offset : offset+length]))), true
}

func runeBoundary(text string, i int) bool {
	return i == len(text) || (i >= 0 && i < len(text) && utf8.RuneStart(text[i]))
}

func lsMentions(text string, msg *table.LSInsertMessage) []Mention {
	if msg == nil || msg.MentionIds == "" {
		return nil
	}
	parsed, err := (&socket.MentionData{
		MentionIDs:     msg.MentionIds,
		MentionOffsets: msg.MentionOffsets,
		MentionLengths: msg.MentionLengths,
		MentionTypes:   msg.MentionTypes,
	}).Parse()
	if err != nil {
		return nil
	}
	var out []Mention
	prevEnd := 0
	for _, m := range parsed {
		if m.Type != socket.MentionTypePerson && m.Type != socket.MentionTypeSilent || m.ID == 0 {
			continue
		}
		start, length, ok := byteSpanFromUTF16(text, m.Offset, m.Length)
		if !ok || start < prevEnd {
			continue
		}
		out = append(out, Mention{Offset: start, Length: length, UserID: itoa(m.ID)})
		prevEnd = start + length
	}
	return out
}

func lsMentionData(text string, mentions []Mention) *socket.MentionData {
	var out socket.Mentions
	for _, m := range mentions {
		start, length, ok := utf16SpanFromBytes(text, m.Offset, m.Length)
		id, err := strconv.ParseInt(m.UserID, 10, 64)
		if !ok || err != nil || id <= 0 {
			continue
		}
		out = append(out, socket.Mention{ID: id, Offset: start, Length: length, Type: socket.MentionTypePerson})
	}
	if len(out) == 0 {
		return nil
	}
	return out.ToData()
}

func waOutgoingText(text string, mentions []Mention) *waCommon.MessageText {
	if len(mentions) == 0 {
		return &waCommon.MessageText{Text: proto.String(text)}
	}
	var body strings.Builder
	var jids []string
	seen := make(map[string]bool)
	prev := 0
	for _, m := range mentions {
		jid := types.NewJID(m.UserID, types.MessengerServer).String()
		body.WriteString(text[prev:m.Offset])
		body.WriteString("@" + jid)
		prev = m.Offset + m.Length
		if !seen[jid] {
			seen[jid] = true
			jids = append(jids, jid)
		}
	}
	body.WriteString(text[prev:])
	return &waCommon.MessageText{Text: proto.String(body.String()), MentionedJID: jids}
}

type waMentionToken struct {
	token  string
	userID string
}

func (b *MetaBackend) waTextWithMentions(threadID string, mt *waCommon.MessageText) (string, []Mention) {
	text := mt.GetText()
	var tokens []waMentionToken
	addJID := func(raw string) {
		jid, err := types.ParseJID(raw)
		if err != nil || jid.User == "" {
			return
		}
		tokens = append(tokens, waMentionToken{"@" + raw, jid.User})
		if raw != jid.User {
			tokens = append(tokens, waMentionToken{"@" + jid.User, jid.User})
		}
	}
	for _, raw := range mt.GetMentionedJID() {
		addJID(raw)
	}
	for _, m := range mt.GetMentions() {
		addJID(m.GetMentionedJID())
	}
	if len(tokens) == 0 {
		return text, nil
	}
	slices.SortStableFunc(tokens, func(a, b waMentionToken) int { return cmp.Compare(len(b.token), len(a.token)) })
	var out strings.Builder
	var mentions []Mention
	rest := text
	for {
		best, bestIdx := -1, -1
		for i, tok := range tokens {
			idx := strings.Index(rest, tok.token)
			if idx < 0 {
				continue
			}
			if bestIdx < 0 || idx < bestIdx {
				best, bestIdx = i, idx
			}
		}
		if best < 0 {
			break
		}
		out.WriteString(rest[:bestIdx])
		name := b.mentionName(threadID, tokens[best].userID)
		mentions = append(mentions, Mention{Offset: out.Len(), Length: len(name), UserID: tokens[best].userID})
		out.WriteString(name)
		rest = rest[bestIdx+len(tokens[best].token):]
	}
	if len(mentions) > 0 {
		out.WriteString(rest)
		return out.String(), mentions
	}
	for _, m := range mt.GetMentions() {
		jid, err := types.ParseJID(m.GetMentionedJID())
		if err != nil || jid.User == "" {
			continue
		}
		if start, length, ok := byteSpanFromUTF16(text, int(m.GetOffset()), int(m.GetLength())); ok {
			mentions = append(mentions, Mention{Offset: start, Length: length, UserID: jid.User})
		}
	}
	slices.SortStableFunc(mentions, func(a, b Mention) int { return cmp.Compare(a.Offset, b.Offset) })
	kept := mentions[:0]
	for _, m := range mentions {
		if len(kept) > 0 && m.Offset < kept[len(kept)-1].Offset+kept[len(kept)-1].Length {
			continue
		}
		kept = append(kept, m)
	}
	return text, kept
}

func (b *MetaBackend) mentionName(threadID, userID string) string {
	if name := b.store.MemberName(threadID, userID); name != "" {
		return name
	}
	if userID == b.store.SelfID() {
		if name := b.currentUser().Name; name != "" {
			return name
		}
	}
	return userID
}

func (s *Store) MemberName(threadID, userID string) string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.displayNameFor(threadID, userID, "")
}

func (s *Store) participantsLocked(rec *threadRec) []Participant {
	out := []Participant{}
	if !rec.IsGroup {
		return out
	}
	for member := range rec.Members {
		if member == s.selfID || member == "" {
			continue
		}
		p := Participant{UserID: member, Name: s.displayNameFor(rec.ID, member, "")}
		if c := s.contacts[member]; c != nil {
			p.AvatarURL = c.AvatarURL
		}
		p.Online, p.LastActiveMS = s.presenceLocked(member)
		p.IsAdmin = rec.Admins[member]
		out = append(out, p)
	}
	slices.SortFunc(out, func(a, b Participant) int {
		if c := cmp.Compare(strings.ToLower(a.Name), strings.ToLower(b.Name)); c != 0 {
			return c
		}
		return cmp.Compare(a.UserID, b.UserID)
	})
	return out
}

func (s *Store) CheckMentions(threadID, text string, mentions []Mention) ([]Mention, error) {
	if len(mentions) == 0 {
		return nil, nil
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	rec := s.threads[threadID]
	if rec == nil || !rec.IsGroup {
		return nil, newError(CodeBadRequest, "mentions are only allowed in group threads")
	}
	sorted := slices.Clone(mentions)
	slices.SortFunc(sorted, func(a, b Mention) int { return cmp.Compare(a.Offset, b.Offset) })
	prevEnd := 0
	for i, m := range sorted {
		if m.Offset < 0 || m.Length <= 0 || m.Offset+m.Length > len(text) || !runeBoundary(text, m.Offset) || !runeBoundary(text, m.Offset+m.Length) {
			return nil, newError(CodeBadRequest, "mention %d is not a valid span of text", i)
		}
		if i > 0 && m.Offset < prevEnd {
			return nil, newError(CodeBadRequest, "mentions must not overlap")
		}
		if m.UserID == "" || m.UserID == s.selfID || !rec.Members[m.UserID] {
			return nil, newError(CodeBadRequest, "mentioned user %s is not a participant of this thread", m.UserID)
		}
		prevEnd = m.Offset + m.Length
	}
	return sorted, nil
}
