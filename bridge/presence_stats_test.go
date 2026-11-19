package main

import (
	"context"
	"testing"
	"time"

	"go.mau.fi/mautrix-meta/pkg/messagix/table"
	waTypes "go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
)

func TestPresenceUpdatesAreCounted(t *testing.T) {
	b, _, _ := loggedInBackend(t)
	now := time.Now().UnixMilli()
	b.handleTable(&table.LSTable{LSDeleteThenInsertContactPresence: []*table.LSDeleteThenInsertContactPresence{
		{ContactId: 7, Status: lsPresenceActive, LastActiveTimestampMs: now},
		{ContactId: 8, Status: 0, LastActiveTimestampMs: now - 60_000},
	}}, modeLive)
	if got := b.presenceLS.Load(); got != 2 {
		t.Fatalf("each LightSpeed presence row must be counted, got %d", got)
	}
	if known, online := b.store.PresenceStats(); known != 2 || online != 1 {
		t.Fatalf("expected 2 known and 1 online, got %d and %d", known, online)
	}
	b.store.SetPresence("9", true, now, now-1)
	if known, online := b.store.PresenceStats(); known != 3 || online != 1 {
		t.Fatalf("an expired presence must not count as online, got %d known and %d online", known, online)
	}
}

func TestEncryptedPresenceUpdatesAreCounted(t *testing.T) {
	b, _, _ := loggedInBackend(t)
	peer := waTypes.NewJID(testPeerID, waTypes.MessengerServer)
	self := waTypes.NewJID(testSelf, waTypes.MessengerServer)
	b.handleWAEvent(context.Background(), nil, nil, &events.Presence{From: peer})
	b.handleWAEvent(context.Background(), nil, nil, &events.Presence{From: peer, Unavailable: true, LastSeen: time.Now()})
	b.handleWAEvent(context.Background(), nil, nil, &events.Presence{From: self})
	if got := b.presenceE2EE.Load(); got != 2 {
		t.Fatalf("each encrypted presence from someone else must be counted, got %d", got)
	}
	if b.presenceLS.Load() != 0 {
		t.Fatal("encrypted presence must not count as LightSpeed presence")
	}
	if known, online := b.store.PresenceStats(); known != 1 || online != 0 {
		t.Fatalf("the latest encrypted presence must be stored, got %d known and %d online", known, online)
	}
}
