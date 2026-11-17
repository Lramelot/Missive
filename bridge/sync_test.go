package main

import (
	"strings"
	"testing"
	"time"

	"go.mau.fi/mautrix-meta/pkg/messagix/table"
)

const (
	testSelf     = "999"
	testPeer     = 100004000000001
	testPeerID   = "100004000000001"
	testLegacyFB = 88880000000001
)

func TestE2EEThreadRowsAreNotOverwrittenByLegacyRows(t *testing.T) {
	b, buf, _ := newTestMetaBackend(t)
	b.store.SetSelfID(testSelf)

	b.handleTable(&table.LSTable{
		LSVerifyHybridThreadExists: []*table.LSVerifyHybridThreadExists{{
			ThreadKey:                    testLegacyFB,
			ThreadJID:                    testPeer,
			ThreadType:                   table.ENCRYPTED_OVER_WA_ONE_TO_ONE,
			LastActivityTimestampMS:      1_600_000_000_000,
			LastReadWatermarkTimestampMS: 1_600_000_000_000,
		}},
	}, modeHistory)
	th, ok := b.store.ThreadView(testPeerID)
	if !ok || !th.E2EE || th.IsGroup || th.LastActivityMS != 1_600_000_000_000 {
		t.Fatalf("hybrid row not mapped to an e2ee 1:1 thread: %+v", th)
	}

	b.handleTable(&table.LSTable{
		LSDeleteThenInsertThread: []*table.LSDeleteThenInsertThread{{
			ThreadKey:                    testPeer,
			ThreadType:                   table.ENCRYPTED_OVER_WA_ONE_TO_ONE,
			SyncGroup:                    95,
			LastActivityTimestampMs:      1_790_000_000_000,
			LastReadWatermarkTimestampMs: 1_780_000_000_000,
			UnreadMessageCount:           3,
		}},
		LSVerifyContactRowExists: []*table.LSVerifyContactRowExists{{ContactId: testPeer, Name: "Peer Person", ProfilePictureUrl: "https://cdn.example/p.jpg"}},
	}, modeHistory)
	th, _ = b.store.ThreadView(testPeerID)
	if th.LastActivityMS != 1_790_000_000_000 || th.UnreadCount != 3 || th.Name != "Peer Person" || th.AvatarURL == "" {
		t.Fatalf("sync group 95 row not applied: %+v", th)
	}

	b.handleTable(&table.LSTable{
		LSDeleteThenInsertThread: []*table.LSDeleteThenInsertThread{{
			ThreadKey:                    testLegacyFB,
			ThreadType:                   table.ONE_TO_ONE,
			SyncGroup:                    1,
			Snippet:                      "legacy snippet",
			LastActivityTimestampMs:      1_550_000_000_000,
			LastReadWatermarkTimestampMs: 1_550_000_000_000,
		}},
	}, modeHistory)
	th, _ = b.store.ThreadView(testPeerID)
	if th.LastActivityMS != 1_790_000_000_000 || th.UnreadCount != 3 || !th.E2EE {
		t.Fatalf("legacy row regressed the e2ee thread: %+v", th)
	}
	if b.store.HasThread(itoa(testLegacyFB)) {
		t.Fatal("legacy key must not create its own thread")
	}
	if th.Snippet != "legacy snippet" {
		t.Fatalf("legacy snippet should fill an empty snippet, got %q", th.Snippet)
	}
	if strings.Contains(strings.Join(buf.Lines(), "\n"), `"thread_id":"`+itoa(testLegacyFB)+`"`) {
		t.Fatal("legacy thread id leaked into events")
	}
}

func TestEncryptedThreadListRowsKeyedByLegacyKey(t *testing.T) {
	b, buf, _ := newTestMetaBackend(t)
	b.store.SetSelfID(testSelf)
	fresh := time.Now().Add(-11 * time.Minute).UnixMilli()
	b.handleTable(&table.LSTable{
		LSUpdateThreadAuthorityAndMappingWithOTIDFromJID: []*table.LSUpdateThreadAuthorityAndMappingWithOTIDFromJID{{ThreadJID: testPeer, ThreadKey: testLegacyFB}},
		LSVerifyHybridThreadExists: []*table.LSVerifyHybridThreadExists{{
			ThreadKey: testLegacyFB, ThreadJID: testPeer, ThreadType: table.ENCRYPTED_OVER_WA_ONE_TO_ONE, LastActivityTimestampMS: fresh,
		}},
		LSUpdateOrInsertThread: []*table.LSUpdateOrInsertThread{{
			ThreadKey:                    testLegacyFB,
			ThreadType:                   table.ENCRYPTED_OVER_WA_ONE_TO_ONE,
			LastActivityTimestampMs:      fresh,
			LastReadWatermarkTimestampMs: fresh - 60_000,
			MuteExpireTimeMs:             -1,
		}},
		LSVerifyContactRowExists: []*table.LSVerifyContactRowExists{{ContactId: testPeer, Name: "Peer Person"}},
	}, modeHistory)
	if b.store.HasThread(itoa(testLegacyFB)) {
		t.Fatal("encrypted row must be stored under the WhatsApp key")
	}
	th, ok := b.store.ThreadView(testPeerID)
	if !ok || !th.E2EE || th.IsGroup || th.Name != "Peer Person" || th.LastActivityMS != fresh || th.UnreadCount != 1 || !th.Muted {
		t.Fatalf("unexpected encrypted thread %+v", th)
	}
	if n := len(eventsOf(t, buf, EventMessage)); n != 0 {
		t.Fatalf("thread list sync must not emit messages, got %d", n)
	}
}

func TestParseMessagixSyncConfig(t *testing.T) {
	raw := []byte(`{"Configs":{"VersionID":29437738132510565,"BrowserConfigTable":{"LSPlatformMessengerSyncParams":{"mailbox":"m","contact":"{\"locale\":\"en_US\"}","e2ee":"e"}}},"SyncStore":{}}`)
	version, params, err := parseMessagixSyncConfig(raw)
	if err != nil || version != 29437738132510565 || params != `{"locale":"en_US"}` {
		t.Fatalf("unexpected %d %q %v", version, params, err)
	}
	if _, _, err := parseMessagixSyncConfig([]byte(`{"Configs":{}}`)); err == nil {
		t.Fatal("missing version must fail")
	}
}

func TestThreadViewUsesLatestCachedMessage(t *testing.T) {
	s := NewStore()
	s.SetSelfID(testSelf)
	s.UpdateThread("1", func(rec *threadRec) { rec.E2EE = true; rec.PeerID = "1"; rec.LastActivityMS = 1000 })
	s.UpdateThread("2", func(rec *threadRec) { rec.LastActivityMS = 3000; rec.Snippet = "group"; rec.IsGroup = true })
	if threads := s.ListThreads(0); threads[0].ID != "2" || threads[1].Snippet != "" {
		t.Fatalf("unexpected order before messages: %+v", threads)
	}
	s.PutMessage(Message{ID: "m1", ThreadID: "1", SenderID: "1", Text: "fresh", TimestampMS: 5000})
	threads := s.ListThreads(0)
	if threads[0].ID != "1" || threads[0].Snippet != "fresh" || threads[0].LastActivityMS != 5000 {
		t.Fatalf("cached message did not bump the thread: %+v", threads)
	}
	s.PutMessage(Message{ID: "m0", ThreadID: "2", SenderID: "1", Text: "older", TimestampMS: 2000})
	if th, _ := s.ThreadView("2"); th.Snippet != "group" || th.LastActivityMS != 3000 {
		t.Fatalf("older message must not override thread metadata: %+v", th)
	}
}

func TestStatsCountUnnamedOneToOne(t *testing.T) {
	s := NewStore()
	s.SetSelfID(testSelf)
	s.SetContact("10", "Named", "", "")
	s.UpdateThread("10", func(rec *threadRec) { rec.PeerID = "10"; rec.E2EE = true; rec.ThreadType = 15 })
	s.UpdateThread("11", func(rec *threadRec) { rec.PeerID = "11"; rec.E2EE = true; rec.ThreadType = 15 })
	s.UpdateThread("12", func(rec *threadRec) { rec.PeerID = "12"; rec.ThreadType = 1 })
	s.UpdateThread("13", func(rec *threadRec) { rec.IsGroup = true; rec.ThreadType = 2 })
	st := s.Stats()
	if st.OneToOne != 3 || st.OneToOneNoName != 2 || st.E2EEOneToOne != 2 || st.E2EENoName != 1 || st.ByType[15] != 2 || st.ByType[2] != 1 {
		t.Fatalf("unexpected stats %+v", st)
	}
	if strings.Join(st.ContactsMissing, ",") != "11,12" {
		t.Fatalf("unexpected missing contacts %v", st.ContactsMissing)
	}
}

func TestContactsOnlyFiltersTable(t *testing.T) {
	filtered := contactsOnly(&table.LSTable{
		LSDeleteThenInsertContact: []*table.LSDeleteThenInsertContact{{Id: 1, Name: "A"}},
		LSDeleteThread:            []*table.LSDeleteThread{{ThreadKey: 5}},
	})
	if len(filtered.LSDeleteThenInsertContact) != 1 || len(filtered.LSDeleteThread) != 0 {
		t.Fatalf("unexpected filtered table %+v", filtered)
	}
}
