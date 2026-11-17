package main

import (
	"context"
	"encoding/json"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/proto/waCommon"
	"go.mau.fi/whatsmeow/proto/waConsumerApplication"
	"go.mau.fi/whatsmeow/proto/waMsgApplication"
	waTypes "go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
	"go.mau.fi/whatsmeow/util/keys"
	"google.golang.org/protobuf/proto"

	"go.mau.fi/mautrix-meta/pkg/messagix"
	"go.mau.fi/mautrix-meta/pkg/messagix/cookies"
	"go.mau.fi/mautrix-meta/pkg/messagix/securehistory"
	"go.mau.fi/mautrix-meta/pkg/messagix/table"
	"go.mau.fi/mautrix-meta/pkg/messagix/types"
)

func TestReadReceipts(t *testing.T) {
	b, buf, _ := newTestMetaBackend(t)
	b.store.SetSelfID(testSelf)
	ts := time.Now().UnixMilli()
	b.handleTable(&table.LSTable{
		LSDeleteThenInsertThread: []*table.LSDeleteThenInsertThread{{ThreadKey: 77, ThreadType: table.GROUP_THREAD, ThreadName: "Team", LastActivityTimestampMs: ts}},
		LSVerifyContactRowExists: []*table.LSVerifyContactRowExists{{ContactId: 8, Name: "Eight", ProfilePictureUrl: "https://cdn/8.jpg"}, {ContactId: 9, Name: "Nine"}},
		LSAddParticipantIdToGroupThread: []*table.LSAddParticipantIdToGroupThread{
			{ThreadKey: 77, ContactId: 8, Nickname: "Huit", ReadWatermarkTimestampMs: ts - 5000},
			{ThreadKey: 77, ContactId: 999, ReadWatermarkTimestampMs: ts},
		},
	}, modeHistory)
	th, _ := b.store.ThreadView("77")
	if len(th.ReadReceipts) != 1 || th.ReadReceipts[0] != (ReadReceipt{UserID: "8", Name: "Huit", AvatarURL: "https://cdn/8.jpg", ReadMS: ts - 5000}) {
		t.Fatalf("unexpected receipts %+v", th.ReadReceipts)
	}
	before := len(eventsOf(t, buf, EventThreads))
	b.handleTable(&table.LSTable{
		LSUpdateReadReceipt: []*table.LSUpdateReadReceipt{{ThreadKey: 77, ContactId: 9, ReadWatermarkTimestampMs: ts - 1000}, {ThreadKey: 77, ContactId: 8, ReadWatermarkTimestampMs: ts - 9000}},
	}, modeLive)
	th, _ = b.store.ThreadView("77")
	if len(th.ReadReceipts) != 2 || th.ReadReceipts[0].UserID != "9" || th.ReadReceipts[1].ReadMS != ts-5000 {
		t.Fatalf("receipts must be max-merged and sorted newest first: %+v", th.ReadReceipts)
	}
	if len(eventsOf(t, buf, EventThreads)) != before+1 || len(eventsOf(t, buf, EventRead)) != 2 {
		t.Fatal("live receipt change must emit a threads upsert and read events")
	}

	chat := waTypes.NewJID(testPeerID, waTypes.MessengerServer)
	b.ensureE2EEThread(chat)
	b.handleWAReceipt(&events.Receipt{MessageSource: waTypes.MessageSource{Chat: chat, Sender: chat}, Type: waTypes.ReceiptTypeRead, Timestamp: time.UnixMilli(ts)})
	if th, _ := b.store.ThreadView(testPeerID); len(th.ReadReceipts) != 1 || th.ReadReceipts[0].ReadMS != ts {
		t.Fatalf("e2ee receipt not stored: %+v", th.ReadReceipts)
	}
	b.store.UpdateThread("1234", func(rec *threadRec) { rec.IsGroup = true })
	empty, _ := b.store.ThreadView("1234")
	if raw, _ := json.Marshal(empty); !strings.Contains(string(raw), `"read_receipts":[]`) {
		t.Fatalf("empty read_receipts must serialize as []: %s", raw)
	}
}

func TestReplyPreviews(t *testing.T) {
	b, _, _ := newTestMetaBackend(t)
	b.store.SetSelfID(testSelf)
	ts := time.Now().UnixMilli()
	b.handleTable(&table.LSTable{
		LSVerifyContactRowExists: []*table.LSVerifyContactRowExists{{ContactId: 7, Name: "Seven"}},
		LSInsertMessage: []*table.LSInsertMessage{{
			ThreadKey: 5, MessageId: "mid.$r", TimestampMs: ts, SenderId: 999, Text: "answer",
			ReplySourceId: "mid.$q", ReplyMessageText: "question", ReplyToUserId: 7,
		}},
	}, modeLive)
	msgs := b.store.Messages("5", 0, 10)
	if len(msgs) != 1 || msgs[0].ReplyTo == nil || *msgs[0].ReplyTo != (ReplyPreview{ID: "mid.$q", SenderName: "Seven", Text: "question"}) {
		t.Fatalf("LS replied-to fallback missing: %+v", msgs[0].ReplyTo)
	}
	b.handleTable(&table.LSTable{
		LSAddParticipantIdToGroupThread: []*table.LSAddParticipantIdToGroupThread{},
		LSUpsertMessage:                 []*table.LSUpsertMessage{{ThreadKey: 5, MessageId: "mid.$q", TimestampMs: ts - 1000, SenderId: 7, Text: "question (full)"}},
		LSInsertNewMessageRange:         []*table.LSInsertNewMessageRange{{ThreadKey: 5, MinTimestampMs: ts - 1000, MaxTimestampMs: ts - 1000}},
		LSInsertBlobAttachment:          []*table.LSInsertBlobAttachment{{MessageId: "mid.$q", AttachmentFbid: "1", AttachmentType: table.AttachmentTypeImage, PreviewUrl: "https://cdn/x.jpg"}},
	}, modeHistory)
	msg, _ := b.store.MessageInThread("5", "mid.$r")
	if preview := b.store.Decorate(msg).ReplyTo; preview == nil || preview.Text != "question (full)" || preview.Kind != "image" || preview.SenderName != "Seven" {
		t.Fatalf("stored quoted message must win: %+v", preview)
	}

	quotedApp, _ := proto.Marshal(&waConsumerApplication.ConsumerApplication{Payload: &waConsumerApplication.ConsumerApplication_Payload{
		Payload: &waConsumerApplication.ConsumerApplication_Payload_Content{Content: &waConsumerApplication.ConsumerApplication_Content{
			Content: &waConsumerApplication.ConsumerApplication_Content_MessageText{MessageText: &waCommon.MessageText{Text: proto.String("quoted e2ee")}},
		}},
	}})
	evt := consumerEvent("E1", &waConsumerApplication.ConsumerApplication_Content{
		Content: &waConsumerApplication.ConsumerApplication_Content_MessageText{MessageText: &waCommon.MessageText{Text: proto.String("reply")}},
	})
	evt.FBApplication = &waMsgApplication.MessageApplication{Metadata: &waMsgApplication.MessageApplication_Metadata{
		QuotedMessage: &waMsgApplication.MessageApplication_Metadata_QuotedMessage{
			StanzaID:    proto.String("UNKNOWN1"),
			Participant: proto.String(waTypes.NewJID(testSelf, waTypes.MessengerServer).String()),
			Payload: &waMsgApplication.MessageApplication_Payload{Content: &waMsgApplication.MessageApplication_Payload_SubProtocol{
				SubProtocol: &waMsgApplication.MessageApplication_SubProtocolPayload{SubProtocol: &waMsgApplication.MessageApplication_SubProtocolPayload_ConsumerMessage{
					ConsumerMessage: &waCommon.SubProtocol{Payload: quotedApp, Version: proto.Int32(whatsmeow.FBConsumerMessageVersion)},
				}},
			}},
		},
	}}
	b.mu.Lock()
	b.sess = &session{User: User{ID: testSelf, Name: "Me"}}
	b.mu.Unlock()
	b.handleFBMessage(evt)
	msg, _ = b.store.MessageInThread(testPeerID, "E1")
	if preview := b.store.Decorate(msg).ReplyTo; msg.ReplyToID != "UNKNOWN1" || preview == nil || preview.Text != "quoted e2ee" || preview.SenderName != "Me" {
		t.Fatalf("e2ee quoted context missing: %+v", preview)
	}
}

func TestSearch(t *testing.T) {
	s := NewStore()
	s.SetSelfID(testSelf)
	for i, m := range []Message{
		{ID: "a", ThreadID: "1", Text: "Rendez-vous à l'ÉCOLE demain", TimestampMS: 100},
		{ID: "b", ThreadID: "2", Text: "ecole fermée", TimestampMS: 300},
		{ID: "c", ThreadID: "1", Attachments: []Attachment{{ID: "x", Kind: "link", Title: "École numérique"}}, TimestampMS: 200},
		{ID: "d", ThreadID: "1", Attachments: []Attachment{{ID: "y", Kind: "file", FileName: "Plan_ECOLE.pdf"}}, TimestampMS: 50},
		{ID: "e", ThreadID: "1", Text: "école", Deleted: true, TimestampMS: 400},
		{ID: "f", ThreadID: "1", Text: "rien à voir", TimestampMS: 500},
	} {
		_ = i
		s.PutMessage(m)
	}
	hits := s.Search("ÉcOle", "", 10)
	var ids []string
	for _, h := range hits {
		ids = append(ids, h.Message.ID)
		if h.ThreadID != h.Message.ThreadID || h.Message.Reactions == nil {
			t.Fatalf("bad hit %+v", h)
		}
	}
	if strings.Join(ids, ",") != "b,c,a,d" {
		t.Fatalf("unexpected hits %v", ids)
	}
	if hits := s.Search("ecole", "1", 2); len(hits) != 2 || hits[0].Message.ID != "c" {
		t.Fatalf("thread filter / limit broken: %+v", hits)
	}
}

type pagedHistory struct {
	fakeHistory
	items []archiveItem
}

func (p *pagedHistory) FetchPage(ctx context.Context, device *securehistory.Device, chat waTypes.JID, selfID int64, beforeMS int64, count int) (*historyPage, error) {
	p.fetches.Add(1)
	var page []archiveItem
	more := false
	for _, item := range p.items {
		if item.evt.Info.Timestamp.UnixMilli() < beforeMS {
			if len(page) == count {
				more = true
				break
			}
			page = append(page, item)
		}
	}
	return &historyPage{items: page, hasMore: more}, nil
}

func imageItem(chat waTypes.JID, id string, ts time.Time) archiveItem {
	image := &waConsumerApplication.ConsumerApplication_ImageMessage{}
	return archiveItem{evt: &events.FBMessage{
		Info: waTypes.MessageInfo{MessageSource: waTypes.MessageSource{Chat: chat, Sender: chat}, ID: id, Timestamp: ts},
		Message: &waConsumerApplication.ConsumerApplication{Payload: &waConsumerApplication.ConsumerApplication_Payload{
			Payload: &waConsumerApplication.ConsumerApplication_Payload_Content{Content: &waConsumerApplication.ConsumerApplication_Content{
				Content: &waConsumerApplication.ConsumerApplication_Content_ImageMessage{ImageMessage: image},
			}},
		}},
	}}
}

func TestMediaHistoryLocalAndArchive(t *testing.T) {
	b, _, _ := newTestMetaBackend(t)
	b.store.SetSelfID(testSelf)
	b.mu.Lock()
	b.cookies = &cookies.Cookies{Platform: types.Messenger}
	b.mu.Unlock()
	b.store.UpdateThread("5", func(rec *threadRec) { rec.IsGroup = true })
	for i := 0; i < 5; i++ {
		b.store.PutMessage(Message{ID: fmt.Sprintf("m%d", i), ThreadID: "5", TimestampMS: int64(1000 * (i + 1)), Attachments: []Attachment{{ID: "a", Kind: []string{"image", "video", "file", "link", "image"}[i]}}})
	}
	b.store.SetHasMoreBefore("5", false)
	ctx := context.Background()
	res, err := b.MediaHistory(ctx, "5", 0, 2, []string{"image", "video"}, maxMediaHistoryPages)
	if err != nil || len(res.Items) != 2 || res.Items[0].MessageID != "m4" || res.Items[1].MessageID != "m1" || !res.HasMore {
		t.Fatalf("unexpected first page %+v %v", res, err)
	}
	res, _ = b.MediaHistory(ctx, "5", res.Items[1].TimestampMS, 10, []string{"image", "video"}, maxMediaHistoryPages)
	if len(res.Items) != 1 || res.Items[0].MessageID != "m0" || res.HasMore {
		t.Fatalf("unexpected last page %+v", res)
	}

	chat := waTypes.NewJID(testPeerID, waTypes.MessengerServer)
	now := time.Now()
	paged := &pagedHistory{}
	for i := 0; i < 130; i++ {
		paged.items = append(paged.items, imageItem(chat, fmt.Sprintf("W%03d", i), now.Add(-time.Duration(i+1)*time.Hour)))
	}
	hb, _, _ := newHistoryBackend(t, &paged.fakeHistory)
	hb.mu.Lock()
	hb.sess.HistoryDevice = &securehistory.Device{ID: "dev", BackupID: "b"}
	hb.historyFactory = func(*messagix.Client) historyService { return paged }
	hb.identityFn = func(context.Context) (*keys.KeyPair, error) { return keys.NewKeyPair(), nil }
	hb.mu.Unlock()
	hb.ensureE2EEThread(chat)
	var all []MediaItem
	before := int64(0)
	calls := 0
	for {
		calls++
		res, err := hb.MediaHistory(ctx, testPeerID, before, 60, []string{"image", "video"}, maxMediaHistoryPages)
		if err != nil {
			t.Fatal(err)
		}
		for i, item := range res.Items {
			if before > 0 && item.TimestampMS >= before || i > 0 && item.TimestampMS > res.Items[i-1].TimestampMS {
				t.Fatal("items must be newest first and strictly older than before_ms")
			}
		}
		all = append(all, res.Items...)
		if !res.HasMore || len(res.Items) == 0 {
			break
		}
		before = res.Items[len(res.Items)-1].TimestampMS
		if calls > 10 {
			t.Fatal("pagination did not terminate")
		}
	}
	if len(all) != 130 || paged.fetches.Load() > 10 {
		t.Fatalf("expected the whole archive (130 items) in <=10 fetches, got %d items, %d fetches", len(all), paged.fetches.Load())
	}
}

func TestLoadSendFiles(t *testing.T) {
	dir := t.TempDir()
	pngPath := filepath.Join(dir, "pic.png")
	img := image.NewNRGBA(image.Rect(0, 0, 30, 20))
	img.Set(1, 1, color.NRGBA{R: 255, A: 255})
	f, _ := os.Create(pngPath)
	png.Encode(f, img)
	f.Write([]byte("trailing-garbage"))
	f.Close()
	mp4Path := filepath.Join(dir, "clip.mp4")
	os.WriteFile(mp4Path, []byte("\x00\x00\x00\x18ftypmp42\x00\x00\x00\x00mp42isom"), 0o600)
	txtPath := filepath.Join(dir, "notes.txt")
	os.WriteFile(txtPath, []byte("hello"), 0o600)
	files, err := loadSendFiles([]string{pngPath, mp4Path, txtPath})
	if err != nil {
		t.Fatal(err)
	}
	pngData, _ := files[0].read()
	if files[0].Kind != "image" || files[0].Mime != "image/png" || files[0].Width != 30 || files[0].Height != 20 || strings.HasSuffix(string(pngData), "trailing-garbage") {
		t.Fatalf("bad png %+v", files[0].Mime)
	}
	if files[1].Kind != "video" || files[1].Mime != "video/mp4" {
		t.Fatalf("bad mp4 %s %s", files[1].Kind, files[1].Mime)
	}
	if files[2].Kind != "file" {
		t.Fatalf("bad text %s", files[2].Kind)
	}
	big := filepath.Join(dir, "big.bin")
	bf, _ := os.Create(big)
	bf.Truncate(maxSendBytes + 1)
	bf.Close()
	for _, bad := range [][]string{{"relative.png"}, {filepath.Join(dir, "missing.png")}, {big}, {dir}} {
		if _, err := loadSendFiles(bad); toErrorBody(err).Code != CodeBadRequest {
			t.Fatalf("%v: expected bad_request, got %v", bad, err)
		}
	}
}

func TestDemoRound3(t *testing.T) {
	h := newDemoHarness(t)
	h.send(`{"id":1,"cmd":"connect"}`)
	h.waitEvent(EventReady, 3*time.Second)
	h.send(`{"id":2,"cmd":"threads"}`)
	var threads []Thread
	h.okResult(2, &threads)
	withReceipts := 0
	for _, th := range threads {
		if len(th.ReadReceipts) > 0 {
			withReceipts++
		}
	}
	if withReceipts == 0 {
		t.Fatal("demo needs read receipts on some threads")
	}
	threadID := threads[0].ID

	dir := t.TempDir()
	pngPath := filepath.Join(dir, "photo.png")
	f, _ := os.Create(pngPath)
	png.Encode(f, image.NewNRGBA(image.Rect(0, 0, 64, 48)))
	f.Close()
	h.send(fmt.Sprintf(`{"id":3,"cmd":"send_media","params":{"thread_id":%q,"paths":[%q,%q],"caption":"vacances"}}`, threadID, pngPath, pngPath))
	var sent SendMediaResult
	h.okResult(3, &sent)
	if len(sent.MessageIDs) != 2 {
		t.Fatalf("unexpected send_media result %+v", sent)
	}
	var first Message
	for _, raw := range h.events(EventMessage) {
		var m Message
		json.Unmarshal(raw, &m)
		if m.ID == sent.MessageIDs[0] {
			first = m
		}
	}
	if first.Text != "vacances" || len(first.Attachments) != 1 || first.Attachments[0].Kind != "image" || first.Attachments[0].Width != 64 {
		t.Fatalf("bad echo %+v", first)
	}
	h.send(fmt.Sprintf(`{"id":4,"cmd":"media","params":{"thread_id":%q,"message_id":%q,"attachment_id":%q}}`, threadID, first.ID, first.Attachments[0].ID))
	h.okResult(4, nil)
	h.send(fmt.Sprintf(`{"id":5,"cmd":"send_media","params":{"thread_id":%q,"paths":["relative.png"]}}`, threadID))
	if code := h.errCode(5); code != CodeBadRequest {
		t.Fatalf("got %s", code)
	}

	h.send(fmt.Sprintf(`{"id":6,"cmd":"react","params":{"thread_id":%q,"message_id":%q,"emoji":"🔥"}}`, threadID, first.ID))
	h.okResult(6, nil)
	updates := h.events(EventMessageUpdate)
	if len(updates) == 0 || !strings.Contains(string(updates[len(updates)-1]), `"emoji":"🔥","count":1,"mine":true`) {
		t.Fatalf("react must come back as message_updated: %s", updates)
	}
	h.send(fmt.Sprintf(`{"id":7,"cmd":"react","params":{"thread_id":%q,"message_id":%q,"emoji":""}}`, threadID, first.ID))
	h.okResult(7, nil)
	h.send(fmt.Sprintf(`{"id":8,"cmd":"set_typing","params":{"thread_id":%q,"typing":true}}`, threadID), `{"id":9,"cmd":"search","params":{"query":"VACANCES"}}`, `{"id":10,"cmd":"react","params":{"thread_id":"x","message_id":"y"}}`)
	h.okResult(8, nil)
	var hits []SearchHit
	h.okResult(9, &hits)
	if len(hits) == 0 || hits[0].Message.ID != first.ID {
		t.Fatalf("search must find the caption: %+v", hits)
	}
	if code := h.errCode(10); code != CodeBadRequest {
		t.Fatalf("missing emoji must be bad_request, got %s", code)
	}

	media := h.demo.mediaThreadID()
	var all []MediaItem
	before := int64(0)
	for id := 100; id < 140; id++ {
		params := fmt.Sprintf(`{"thread_id":%q,"limit":60}`, media)
		if before > 0 {
			params = fmt.Sprintf(`{"thread_id":%q,"limit":60,"before_ms":%d}`, media, before)
		}
		h.send(fmt.Sprintf(`{"id":%d,"cmd":"media_history","params":%s}`, id, params))
		var res MediaHistoryResult
		h.okResult(int64(id), &res)
		all = append(all, res.Items...)
		if !res.HasMore || len(res.Items) == 0 {
			break
		}
		before = res.Items[len(res.Items)-1].TimestampMS
	}
	if len(all) < 200 {
		t.Fatalf("demo media history too short: %d", len(all))
	}
	span := time.Duration(all[0].TimestampMS-all[len(all)-1].TimestampMS) * time.Millisecond
	if span < 60*24*time.Hour {
		t.Fatalf("demo media history must span months, got %s", span)
	}
	for i := 1; i < len(all); i++ {
		if all[i].TimestampMS > all[i-1].TimestampMS {
			t.Fatal("media history must be newest first across pages")
		}
	}
}
