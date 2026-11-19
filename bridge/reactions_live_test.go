package main

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/rs/zerolog"
	waTypes "go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"

	"go.mau.fi/mautrix-meta/pkg/messagix/table"
)

func reactionAddedEvents(t *testing.T, buf *syncBuffer) []ReactionAddedEvent {
	t.Helper()
	var out []ReactionAddedEvent
	for _, line := range buf.Lines() {
		var env struct {
			Event string             `json:"event"`
			Data  ReactionAddedEvent `json:"data"`
		}
		if err := json.Unmarshal([]byte(line), &env); err != nil {
			t.Fatalf("invalid line %q: %v", line, err)
		}
		if env.Event == EventReactionAdded {
			out = append(out, env.Data)
		}
	}
	return out
}

func reactionBackend(t *testing.T) (*MetaBackend, *syncBuffer) {
	t.Helper()
	b, buf := groupBackend(t)
	b.store.SetNickname("55", "7", "Cam 🌸")
	now := time.Now().UnixMilli()
	long := strings.Repeat("é", 100)
	b.store.PutMessage(Message{ID: "mid.$mine", ThreadID: "55", SenderID: testSelf, IsOutgoing: true, Text: long, TimestampMS: now - 60_000})
	b.store.PutMessage(Message{ID: "mid.$photo", ThreadID: "55", SenderID: testSelf, IsOutgoing: true, TimestampMS: now - 50_000, Attachments: []Attachment{{ID: "a0", Kind: "image"}}})
	b.store.PutMessage(Message{ID: "mid.$theirs", ThreadID: "55", SenderID: "8", Text: "pas à moi", TimestampMS: now - 40_000})
	return b, buf
}

func lsReaction(msgID string, actor int64, emoji string, ts int64) *table.LSTable {
	return &table.LSTable{LSUpsertReaction: []*table.LSUpsertReaction{{ThreadKey: 55, MessageId: msgID, ActorId: actor, Reaction: emoji, TimestampMs: ts}}}
}

func TestLSReactionAddedOnMyMessage(t *testing.T) {
	b, buf := reactionBackend(t)
	now := time.Now().UnixMilli()
	b.handleTable(lsReaction("mid.$mine", 7, "❤️", now), modeLive)
	evts := reactionAddedEvents(t, buf)
	if len(evts) != 1 {
		t.Fatalf("expected one reaction_added, got %+v", evts)
	}
	e := evts[0]
	if e.ThreadID != "55" || e.MessageID != "mid.$mine" || e.Emoji != "❤️" || e.ActorID != "7" || e.ActorName != "Cam 🌸" || e.TargetPreview != strings.Repeat("é", reactionPreviewMaxRunes) {
		t.Fatalf("unexpected event %+v", e)
	}
	if !lineHas(buf, `"event":"message_updated"`) {
		t.Fatal("message_updated must still be emitted")
	}

	b.handleTable(lsReaction("mid.$mine", 7, "❤️", now+1000), modeLive)
	if n := len(reactionAddedEvents(t, buf)); n != 1 {
		t.Fatalf("a re-delivery of the same reaction must not notify again, got %d", n)
	}
	b.handleTable(lsReaction("mid.$mine", 7, "😂", now+2000), modeLive)
	if evts := reactionAddedEvents(t, buf); len(evts) != 2 || evts[1].Emoji != "😂" {
		t.Fatalf("a changed emoji must notify: %+v", evts)
	}
	b.handleTable(&table.LSTable{LSDeleteReaction: []*table.LSDeleteReaction{{ThreadKey: 55, MessageId: "mid.$mine", ActorId: 7}}}, modeLive)
	if n := len(reactionAddedEvents(t, buf)); n != 2 {
		t.Fatalf("a removal must not notify, got %d", n)
	}

	b.handleTable(lsReaction("mid.$photo", 8, "👍", now), modeLive)
	if evts := reactionAddedEvents(t, buf); len(evts) != 3 || evts[2].TargetPreview != "Photo" || evts[2].ActorName != "Théo Durand" {
		t.Fatalf("attachment preview / contact name expected: %+v", evts)
	}
}

func TestLSReactionAddedIsFiltered(t *testing.T) {
	b, buf := reactionBackend(t)
	now := time.Now().UnixMilli()
	b.handleTable(lsReaction("mid.$theirs", 7, "❤️", now), modeLive)
	b.handleTable(lsReaction("mid.$mine", 999, "❤️", now), modeLive)
	b.handleTable(lsReaction("mid.$unknown", 7, "❤️", now), modeLive)
	b.handleTable(lsReaction("mid.$mine", 8, "😮", now-int64(10*time.Minute/time.Millisecond)), modeLive)
	b.handleTable(lsReaction("mid.$photo", 7, "😢", now), modeHistory)
	b.lsCatchingUp.Store(true)
	b.handleTable(lsReaction("mid.$photo", 8, "😢", now), modeLive)
	b.lsCatchingUp.Store(false)
	if evts := reactionAddedEvents(t, buf); len(evts) != 0 {
		t.Fatalf("not-mine, self, unknown, stale, history and catch-up reactions must not notify: %+v", evts)
	}
	if b.store.SelfID() != testSelf || testSelf != "999" {
		t.Fatalf("test assumes self id 999, got %q", b.store.SelfID())
	}
}

func e2eeReaction(b *MetaBackend, sender, target, emoji string, at time.Time) {
	chat := waTypes.NewJID("55", waTypes.GroupServer)
	conv := b.convertFBMessage("55", &events.FBMessage{
		Info:    waTypes.MessageInfo{MessageSource: waTypes.MessageSource{Chat: chat, Sender: waTypes.NewJID(sender, waTypes.MessengerServer)}, ID: "R-" + sender + emoji, Timestamp: at},
		Message: reactionPayload(target, emoji),
	})
	b.applyEditOrRevoke(conv)
}

func TestE2EEReactionAdded(t *testing.T) {
	b, buf := reactionBackend(t)
	now := time.Now()
	e2eeReaction(b, "7", "mid.$photo", "👍", now)
	evts := reactionAddedEvents(t, buf)
	if len(evts) != 1 || evts[0].MessageID != "mid.$photo" || evts[0].ActorID != "7" || evts[0].ActorName != "Cam 🌸" || evts[0].TargetPreview != "Photo" {
		t.Fatalf("expected one E2EE reaction_added: %+v", evts)
	}
	e2eeReaction(b, "7", "mid.$photo", "👍", now.Add(time.Second))
	e2eeReaction(b, "7", "mid.$photo", "", now.Add(2*time.Second))
	e2eeReaction(b, "8", "mid.$theirs", "👍", now)
	e2eeReaction(b, testSelf, "mid.$mine", "👍", now)
	e2eeReaction(b, "8", "mid.$mine", "😮", now.Add(-10*time.Minute))
	b.waOfflineSyncing.Store(true)
	e2eeReaction(b, "8", "mid.$mine", "😂", now)
	b.waOfflineSyncing.Store(false)
	if evts := reactionAddedEvents(t, buf); len(evts) != 1 {
		t.Fatalf("replay, removal, not-mine, self, stale and offline-sync reactions must not notify: %+v", evts)
	}
	e2eeReaction(b, "8", "mid.$mine", "😢", now)
	if evts := reactionAddedEvents(t, buf); len(evts) != 2 || evts[1].ActorID != "8" || evts[1].Emoji != "😢" {
		t.Fatalf("a new live reaction after offline sync must notify: %+v", evts)
	}
}

func TestReactionTargetPreview(t *testing.T) {
	cases := map[string]Message{
		"Message vocal": {Attachments: []Attachment{{Kind: "audio", Voice: true}}},
		"Fichier":       {Attachments: []Attachment{{Kind: "file"}}},
		"Vidéo":         {Attachments: []Attachment{{Kind: "video"}}},
		"Lien":          {Attachments: []Attachment{{Kind: "link"}}},
		"Autocollant":   {Attachments: []Attachment{{Kind: "sticker"}}},
		"Pièce jointe":  {Attachments: []Attachment{{Kind: "location"}}},
		"court":         {Text: "  court  "},
	}
	for want, msg := range cases {
		if got := reactionTargetPreview(msg); got != want {
			t.Errorf("preview of %+v = %q, want %q", msg, got, want)
		}
	}
}

func TestDemoSimulatesReactionAdded(t *testing.T) {
	buf := &syncBuffer{}
	d := NewDemoBackend(t.TempDir(), "", NewOutput(buf, zerolog.Nop()), zerolog.Nop())
	fired := false
	for i := 0; i < 20 && !fired; i++ {
		fired = d.simulateReaction()
	}
	if !fired {
		t.Fatal("demo must be able to simulate a reaction on an outgoing message")
	}
	evts := reactionAddedEvents(t, buf)
	if len(evts) == 0 {
		t.Fatal("no reaction_added emitted")
	}
	e := evts[len(evts)-1]
	msg, ok := d.store.MessageInThread(e.ThreadID, e.MessageID)
	if !ok || !msg.IsOutgoing || e.ActorID == d.user.ID || e.ActorName == "" || e.TargetPreview == "" {
		t.Fatalf("inconsistent demo reaction %+v (msg %+v)", e, msg)
	}
	if d.store.ReactionOf(e.MessageID, e.ActorID) != e.Emoji {
		t.Fatal("the demo store must hold the announced reaction")
	}
	if !lineHas(buf, `"event":"message_updated"`) {
		t.Fatal("demo must emit message_updated with the reaction")
	}
}

func lineHas(buf *syncBuffer, part string) bool {
	for _, line := range buf.Lines() {
		if strings.Contains(line, part) {
			return true
		}
	}
	return false
}
