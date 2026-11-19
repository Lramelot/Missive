package main

import (
	"context"
	"strings"
	"testing"
	"time"

	"go.mau.fi/mautrix-meta/pkg/messagix/socket"
	"go.mau.fi/mautrix-meta/pkg/messagix/table"
)

func TestLeaveKeepsThread(t *testing.T) {
	b, buf, rec := recordingBackend(t)
	b.store.PutMessage(Message{ID: "mid.$keep", ThreadID: "55", SenderID: "7", Text: "hello", TimestampMS: time.Now().UnixMilli()})
	self := mustThreadKey(b.store.SelfID())
	rec.resp = func([]socket.Task) *table.LSTable {
		return &table.LSTable{LSRemoveParticipantFromThread: []*table.LSRemoveParticipantFromThread{{ThreadKey: 55, ParticipantId: self}}}
	}
	if err := b.LeaveThread(context.Background(), "55"); err != nil {
		t.Fatal(err)
	}
	if task := rec.last().(*socket.RemoveParticipantTask); task.ContactID != self || task.ThreadID != 55 {
		t.Fatalf("unexpected leave task %+v", task)
	}
	v, ok := b.store.ThreadView("55")
	if _, kept := b.store.MessageInThread("55", "mid.$keep"); !ok || !kept || !v.IsGroup || v.Name != "Team" || v.CanManage || v.IsAdmin {
		t.Fatalf("leaving must keep the group and its history with can_manage false: %+v kept=%v", v, kept)
	}
	if len(eventsOf(t, buf, EventThreadRemoved)) != 0 {
		t.Fatal("leaving must not emit thread_removed")
	}
	if canManage(&threadRec{IsGroup: true, Left: true}, testSelf) {
		t.Fatal("a left group with no known members must not be manageable")
	}
	b.handleTable(&table.LSTable{LSRemoveParticipantFromThread: []*table.LSRemoveParticipantFromThread{{ThreadKey: 55, ParticipantId: self}}}, modeLive)
	if !b.store.HasThread("55") {
		t.Fatal("a later self-removal row must not delete a group we left")
	}
	if err := b.RenameThread(context.Background(), "55", "x"); toErrorBody(err).Code != CodeUnsupported {
		t.Fatalf("a left group cannot be managed, got %v", err)
	}
	b.handleTable(&table.LSTable{LSAddParticipantIdToGroupThread: []*table.LSAddParticipantIdToGroupThread{{ThreadKey: 55, ContactId: self}}}, modeLive)
	if v, _ := b.store.ThreadView("55"); !v.CanManage {
		t.Fatal("being added back must make the group manageable again")
	}
}

func TestRemovedThreadsDoNotComeBack(t *testing.T) {
	for name, run := range map[string]func(b *MetaBackend) error{
		"accept": func(b *MetaBackend) error { return b.AcceptRequest(context.Background(), "5") },
		"mute":   func(b *MetaBackend) error { return b.Mute(context.Background(), "5", -1) },
		"block":  func(b *MetaBackend) error { return b.BlockUser(context.Background(), "7", true) },
	} {
		b, buf, rec := recordingBackend(t)
		rec.resp = func([]socket.Task) *table.LSTable {
			return &table.LSTable{LSDeleteThread: []*table.LSDeleteThread{{ThreadKey: 5}}}
		}
		if err := run(b); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if b.store.HasThread("5") {
			t.Fatalf("%s: a thread deleted by the response must not be re-created", name)
		}
		removedAt := -1
		for i, line := range buf.Lines() {
			if strings.Contains(line, `"thread_removed"`) {
				removedAt = i
			} else if removedAt >= 0 && strings.Contains(line, `"threads"`) && strings.Contains(line, `"id":"5"`) {
				t.Fatalf("%s: thread 5 upserted after its removal", name)
			}
		}
		if removedAt < 0 {
			t.Fatalf("%s: missing thread_removed", name)
		}
	}
}
