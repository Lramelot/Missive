package main

import (
	"context"
	"fmt"
	"testing"
	"time"

	"go.mau.fi/mautrix-meta/pkg/messagix"
	"go.mau.fi/mautrix-meta/pkg/messagix/cookies"
	"go.mau.fi/mautrix-meta/pkg/messagix/securehistory"
	"go.mau.fi/mautrix-meta/pkg/messagix/types"
	"go.mau.fi/whatsmeow/proto/waConsumerApplication"
	waTypes "go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
	"go.mau.fi/whatsmeow/util/keys"
)

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

func TestMetaMediaHistoryMaxPagesZero(t *testing.T) {
	chat := waTypes.NewJID(testPeerID, waTypes.MessengerServer)
	paged := &pagedHistory{}
	for i := 0; i < 30; i++ {
		paged.items = append(paged.items, imageItem(chat, fmt.Sprintf("Z%03d", i), time.Now().Add(-time.Duration(i+1)*time.Hour)))
	}
	hb, _, _ := newHistoryBackend(t, &paged.fakeHistory)
	hb.mu.Lock()
	hb.sess.HistoryDevice = &securehistory.Device{ID: "dev", BackupID: "b"}
	hb.historyFactory = func(*messagix.Client) historyService { return paged }
	hb.identityFn = func(context.Context) (*keys.KeyPair, error) { return keys.NewKeyPair(), nil }
	hb.mu.Unlock()
	hb.ensureE2EEThread(chat)
	hb.store.PutMessage(Message{ID: "local", ThreadID: testPeerID, TimestampMS: time.Now().UnixMilli(), Attachments: []Attachment{{ID: "a", Kind: "image"}}})
	ctx := context.Background()
	res, err := hb.MediaHistory(ctx, testPeerID, 0, 10, []string{"image"}, 0)
	if err != nil || len(res.Items) != 1 || !res.HasMore || paged.fetches.Load() != 0 {
		t.Fatalf("max_pages 0 must answer from the store only: %+v %v fetches=%d", res, err, paged.fetches.Load())
	}
	res, err = hb.MediaHistory(ctx, testPeerID, 0, 10, []string{"image"}, 1)
	if err != nil || len(res.Items) <= 1 || paged.fetches.Load() != 1 {
		t.Fatalf("max_pages 1 must fetch exactly one page: %d items, %v, fetches=%d", len(res.Items), err, paged.fetches.Load())
	}
}
