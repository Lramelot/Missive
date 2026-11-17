package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"go.mau.fi/whatsmeow/proto/waCommon"
	"go.mau.fi/whatsmeow/proto/waConsumerApplication"
	waTypes "go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
	"google.golang.org/protobuf/proto"

	"go.mau.fi/mautrix-meta/pkg/messagix/socket"
	"go.mau.fi/mautrix-meta/pkg/messagix/table"
)

func TestReactorsAreExposed(t *testing.T) {
	b, _ := groupBackend(t)
	b.store.SetSelfName("Me")
	b.store.SetNickname("55", "8", "Zed")
	b.store.PutMessage(Message{ID: "m1", ThreadID: "55", SenderID: "7", TimestampMS: 1000})
	b.handleTable(&table.LSTable{LSUpsertReaction: []*table.LSUpsertReaction{
		{ThreadKey: 55, MessageId: "m1", ActorId: 7, Reaction: "❤️", TimestampMs: 2000},
		{ThreadKey: 55, MessageId: "m1", ActorId: 999, Reaction: "❤️", TimestampMs: 3000},
		{ThreadKey: 55, MessageId: "m1", ActorId: 8, Reaction: "😂", TimestampMs: 2500},
	}}, modeLive)
	msg, _ := decorated(b.store, "55", "m1")
	if len(msg.Reactions) != 2 {
		t.Fatalf("unexpected reactions %+v", msg.Reactions)
	}
	heart := msg.Reactions[0]
	want := []Reactor{
		{UserID: testSelf, Name: "Me", TimestampMS: 3000},
		{UserID: "7", Name: "Camille Martin", AvatarURL: "https://scontent.xx.fbcdn.net/c.jpg", TimestampMS: 2000},
	}
	if heart.Emoji != "❤️" || heart.Count != 2 || !heart.Mine || fmt.Sprint(heart.Reactors) != fmt.Sprint(want) {
		t.Fatalf("reactors must be most recent first with names and avatars: %+v", heart)
	}
	if laugh := msg.Reactions[1]; len(laugh.Reactors) != 1 || laugh.Reactors[0].Name != "Zed" || laugh.Mine {
		t.Fatalf("reactor names must follow the nickname rule: %+v", laugh)
	}

	b.handleTable(&table.LSTable{LSUpsertReaction: []*table.LSUpsertReaction{{ThreadKey: 55, MessageId: "m1", ActorId: 7, Reaction: "❤️", TimestampMs: 4000}}}, modeLive)
	if msg, _ := decorated(b.store, "55", "m1"); msg.Reactions[0].Reactors[0].UserID != "7" {
		t.Fatalf("a newer reaction must move its reactor first: %+v", msg.Reactions[0].Reactors)
	}

	b.store.SetReactionV2("m1", "r1", &reactionAggregate{Emoji: "❤️", Count: 5, Mine: true})
	if msg, _ := decorated(b.store, "55", "m1"); len(msg.Reactions) != 1 || msg.Reactions[0].Count != 5 || len(msg.Reactions[0].Reactors) != 2 {
		t.Fatalf("aggregates keep their count while listing the known reactors: %+v", msg.Reactions)
	}
	b.store.SetReactionV2("m1", "r1", nil)

	path := filepath.Join(t.TempDir(), "cache.json")
	if err := b.store.Save(path); err != nil {
		t.Fatal(err)
	}
	reloaded := NewStore()
	if err := reloaded.Load(path); err != nil {
		t.Fatal(err)
	}
	if again, _ := decorated(reloaded, "55", "m1"); again.Reactions[0].Reactors[0].TimestampMS != 4000 {
		t.Fatalf("reaction times must survive a reload: %+v", again.Reactions)
	}

	b.handleTable(&table.LSTable{LSDeleteReaction: []*table.LSDeleteReaction{{ThreadKey: 55, MessageId: "m1", ActorId: 7}}}, modeLive)
	if msg, _ := decorated(b.store, "55", "m1"); len(msg.Reactions[0].Reactors) != 1 || msg.Reactions[0].Reactors[0].UserID != testSelf {
		t.Fatalf("a removed reaction must drop its reactor: %+v", msg.Reactions)
	}

	chat := waTypes.NewJID("66", waTypes.GroupServer)
	b.handleFBMessage(fbText(chat, waTypes.NewJID("7", waTypes.MessengerServer), "W9", &waCommon.MessageText{Text: proto.String("x")}))
	reactAt := time.UnixMilli(5_000_000)
	conv := b.convertFBMessage("66", &events.FBMessage{
		Info:    waTypes.MessageInfo{MessageSource: waTypes.MessageSource{Chat: chat, Sender: waTypes.NewJID("8", waTypes.MessengerServer)}, ID: "R1", Timestamp: reactAt},
		Message: reactionPayload("W9", "👍"),
	})
	b.applyEditOrRevoke(conv)
	if msg, ok := decorated(b.store, "66", "W9"); !ok || len(msg.Reactions) != 1 || msg.Reactions[0].Reactors[0].TimestampMS != reactAt.UnixMilli() || msg.Reactions[0].Reactors[0].UserID != "8" {
		t.Fatalf("E2EE reactions must carry their actor and time: %+v", msg.Reactions)
	}
}

func TestLSPresenceFillsThreads(t *testing.T) {
	b, buf := groupBackend(t)
	b.store.UpdateThread("7", func(rec *threadRec) { rec.PeerID = "7"; rec.Members = map[string]bool{testSelf: true, "7": true} })
	now := time.Now().UnixMilli()
	b.handleTable(&table.LSTable{LSDeleteThenInsertContactPresence: []*table.LSDeleteThenInsertContactPresence{
		{ContactId: 7, Status: lsPresenceActive, LastActiveTimestampMs: now, ExpirationTimestampMs: now + 60_000},
		{ContactId: 8, Status: 0, LastActiveTimestampMs: now - 300_000},
		{ContactId: 999, Status: lsPresenceActive, LastActiveTimestampMs: now},
	}}, modeLive)
	one, _ := b.store.ThreadView("7")
	if one.PeerOnline == nil || !*one.PeerOnline || one.PeerLastActiveMS != now {
		t.Fatalf("1:1 thread must expose peer presence: %+v", one)
	}
	group, _ := b.store.ThreadView("55")
	byID := map[string]Participant{}
	for _, p := range group.Participants {
		byID[p.UserID] = p
	}
	if p := byID["7"]; p.Online == nil || !*p.Online {
		t.Fatalf("online participant expected: %+v", p)
	}
	if p := byID["8"]; p.Online == nil || *p.Online || p.LastActiveMS != now-300_000 {
		t.Fatalf("offline participant with last active expected: %+v", p)
	}
	upserted := map[string]bool{}
	for _, raw := range eventsOf(t, buf, EventThreads) {
		var ev ThreadsEvent
		json.Unmarshal(raw, &ev)
		for _, th := range ev.Threads {
			upserted[th.ID] = true
		}
	}
	if !upserted["7"] || !upserted["55"] {
		t.Fatalf("presence changes must upsert the affected threads: %v", upserted)
	}
	if n := len(eventsOf(t, buf, EventPresence)); n != 2 {
		t.Fatalf("expected presence events for the two contacts only, got %d", n)
	}
	if raw, _ := json.Marshal(group); !jsonHas(raw, `"online":true`) || jsonHas(raw, `"peer_online"`) {
		t.Fatalf("group JSON must carry participant presence, not peer presence: %s", raw)
	}

	b.handleTable(&table.LSTable{LSDeleteThenInsertContactPresence: []*table.LSDeleteThenInsertContactPresence{
		{ContactId: 7, Status: lsPresenceActive, LastActiveTimestampMs: now - 1000, ExpirationTimestampMs: now - 1},
	}}, modeHistory)
	if one, _ := b.store.ThreadView("7"); one.PeerOnline == nil || *one.PeerOnline || one.PeerLastActiveMS != now {
		t.Fatalf("expired presence must read offline and keep the latest last-active: %+v", one)
	}
	if n := len(eventsOf(t, buf, EventPresence)); n != 2 {
		t.Fatal("history presence rows must not emit presence events")
	}
	b.handleTable(&table.LSTable{LSTruncatePresenceDatabase: []*table.LSTruncatePresenceDatabase{{ShouldTruncate: true}}}, modeLive)
	if one, _ := b.store.ThreadView("7"); one.PeerOnline != nil || one.PeerLastActiveMS != 0 {
		t.Fatalf("truncation must forget presence: %+v", one)
	}
	if raw, _ := json.Marshal(Thread{}.normalized()); jsonHas(raw, "peer_online") || jsonHas(raw, "peer_last_active_ms") {
		t.Fatal("unknown presence must be absent from JSON")
	}
}

func jsonHas(raw []byte, part string) bool {
	return strings.Contains(string(raw), part)
}

func reactionPayload(target, emoji string) *waConsumerApplication.ConsumerApplication {
	return &waConsumerApplication.ConsumerApplication{Payload: &waConsumerApplication.ConsumerApplication_Payload{
		Payload: &waConsumerApplication.ConsumerApplication_Payload_Content{Content: &waConsumerApplication.ConsumerApplication_Content{
			Content: &waConsumerApplication.ConsumerApplication_Content_ReactionMessage{ReactionMessage: &waConsumerApplication.ConsumerApplication_ReactionMessage{
				Key:  &waCommon.MessageKey{ID: proto.String(target)},
				Text: proto.String(emoji),
			}},
		}},
	}}
}

func TestE2EEPresenceFillsThreads(t *testing.T) {
	b, buf := groupBackend(t)
	b.ensureE2EEThread(waTypes.NewJID("7", waTypes.MessengerServer))
	seen := time.UnixMilli(1_700_000_000_000)
	b.handleWAEvent(context.Background(), nil, nil, &events.Presence{From: waTypes.NewJID("7", waTypes.MessengerServer), Unavailable: true, LastSeen: seen})
	view, _ := b.store.ThreadView("7")
	if view.PeerOnline == nil || *view.PeerOnline || view.PeerLastActiveMS != seen.UnixMilli() {
		t.Fatalf("E2EE presence must fill the 1:1 thread: %+v", view)
	}
	before := time.Now().UnixMilli()
	b.handleWAEvent(context.Background(), nil, nil, &events.Presence{From: waTypes.NewJID("7", waTypes.MessengerServer)})
	view, _ = b.store.ThreadView("7")
	if !*view.PeerOnline || view.PeerLastActiveMS < before {
		t.Fatalf("available presence means active now: %+v", view)
	}
	found := false
	for _, raw := range eventsOf(t, buf, EventThreads) {
		var ev ThreadsEvent
		json.Unmarshal(raw, &ev)
		for _, th := range ev.Threads {
			found = found || th.ID == "7" && th.PeerOnline != nil && *th.PeerOnline
		}
	}
	if !found || len(eventsOf(t, buf, EventPresence)) != 2 {
		t.Fatal("E2EE presence must emit presence events and a threads upsert")
	}
}

func TestSetActiveReportsAppState(t *testing.T) {
	b, _ := groupBackend(t)
	var mu sync.Mutex
	var states []table.AppState
	b.appStateFn = func(task *socket.ReportAppStateTask) {
		mu.Lock()
		states = append(states, task.AppState)
		mu.Unlock()
	}
	count := func() int {
		mu.Lock()
		defer mu.Unlock()
		return len(states)
	}
	b.SetActive(context.Background(), true)
	waitFor(t, time.Second, func() bool { return count() == 1 })
	b.SetActive(context.Background(), true)
	b.SetActive(context.Background(), false)
	waitFor(t, time.Second, func() bool { return count() == 2 })
	time.Sleep(50 * time.Millisecond)
	mu.Lock()
	defer mu.Unlock()
	if len(states) != 2 || states[0] != table.FOREGROUND || states[1] != table.BACKGROUND {
		t.Fatalf("app state must be reported on focus changes only: %v", states)
	}
}

func TestExpiredAvatarIsRefreshed(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		if r.URL.Query().Get("oe") == "old" {
			w.WriteHeader(http.StatusForbidden)
			return
		}
		w.Header().Set("Content-Type", "image/png")
		w.Write(encodedImage(t, 6, 6, false))
	}))
	defer srv.Close()
	allowTestServer(t, srv)
	b, _ := groupBackend(t)
	oldURL := srv.URL + "/v/pic.jpg?stp=a&oe=old"
	newURL := srv.URL + "/v/pic.jpg?stp=a&oe=new"
	b.store.SetContact("7", "", "", oldURL)
	var fetches atomic.Int32
	b.contactFetchFn = func(ctx context.Context, ids []string) {
		fetches.Add(1)
		if len(ids) == 1 && ids[0] == "7" {
			b.store.SetContact("7", "", "", newURL)
		}
	}
	path, err := b.Avatar(context.Background(), oldURL)
	if err != nil || path == "" || fetches.Load() != 1 {
		t.Fatalf("an expired avatar URL must be refreshed from the contact row: %q %v fetches=%d", path, err, fetches.Load())
	}
	again, err := b.Avatar(context.Background(), oldURL)
	if err != nil || again != path || fetches.Load() != 1 {
		t.Fatalf("the refreshed file must be served for the old URL without refetching: %v fetches=%d", err, fetches.Load())
	}
	if _, err := b.Avatar(context.Background(), srv.URL+"/v/unknown.jpg?oe=old"); toErrorBody(err).Code != CodeUnsupported || fetches.Load() != 1 {
		t.Fatalf("an expired URL of no known contact stays unsupported: %v", err)
	}
	stuck := srv.URL + "/v/stuck.jpg?oe=old"
	b.store.SetContact("8", "", "", stuck)
	for i := 0; i < 2; i++ {
		if _, err := b.Avatar(context.Background(), stuck); toErrorBody(err).Code != CodeUnsupported {
			t.Fatalf("a contact still pointing at the dead URL stays unsupported: %v", err)
		}
	}
	if fetches.Load() != 2 {
		t.Fatalf("a contact must not be refetched again right after a refresh, got %d fetches", fetches.Load())
	}

	soon := strconv.FormatInt(time.Now().Add(2*time.Hour).Unix(), 16)
	later := strconv.FormatInt(time.Now().Add(72*time.Hour).Unix(), 16)
	b.store.SetContact("7", "", "", "https://scontent.xx.fbcdn.net/a.jpg?oe="+soon)
	b.store.SetContact("8", "", "", "https://scontent.xx.fbcdn.net/b.jpg?oe="+later)
	var refreshed []string
	b.contactFetchFn = func(ctx context.Context, ids []string) { refreshed = append(refreshed, ids...) }
	b.refreshExpiringAvatars(context.Background())
	if fmt.Sprint(refreshed) != "[7]" {
		t.Fatalf("only contacts whose avatar expires within a day are refreshed: %v", refreshed)
	}
}

func TestDemoReactorsPresenceAndPictures(t *testing.T) {
	h := newDemoHarness(t)
	threads := h.demo.store.ListThreads(0)
	var reactors, online, offlineWithLast, noPicture int
	for _, th := range threads {
		if !th.IsGroup && th.AvatarURL == "" {
			noPicture++
		}
		if th.PeerOnline != nil {
			if *th.PeerOnline {
				online++
			} else if th.PeerLastActiveMS > 0 {
				offlineWithLast++
			}
		}
		for _, p := range th.Participants {
			if p.Online != nil && *p.Online {
				online++
			}
		}
		for _, m := range h.demo.store.Messages(th.ID, 0, 0) {
			for _, r := range m.Reactions {
				if len(r.Reactors) != r.Count {
					t.Fatalf("demo reaction without full reactors %+v", r)
				}
				for i, rr := range r.Reactors {
					if rr.Name == "" || rr.TimestampMS == 0 || i > 0 && r.Reactors[i-1].TimestampMS < rr.TimestampMS {
						t.Fatalf("bad demo reactor list %+v", r.Reactors)
					}
				}
				reactors += len(r.Reactors)
			}
		}
	}
	if reactors == 0 || online == 0 || offlineWithLast == 0 || noPicture == 0 {
		t.Fatalf("demo lacks variety: reactors=%d online=%d offline=%d no_picture=%d", reactors, online, offlineWithLast, noPicture)
	}
}

func decorated(s *Store, threadID, messageID string) (Message, bool) {
	m, ok := s.MessageInThread(threadID, messageID)
	return s.Decorate(m), ok
}
