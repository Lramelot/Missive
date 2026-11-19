package main

import (
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"go.mau.fi/mautrix-meta/pkg/messagix/table"
	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/proto/waCommon"
	"go.mau.fi/whatsmeow/proto/waConsumerApplication"
	"go.mau.fi/whatsmeow/proto/waMsgApplication"
	waTypes "go.mau.fi/whatsmeow/types"
	"google.golang.org/protobuf/proto"
)

func TestTrimmedCacheKeepsHasMore(t *testing.T) {
	s := NewStore()
	s.SetSelfID("1")
	s.UpdateThread("5", func(rec *threadRec) {})
	for i := 0; i < 400; i++ {
		s.PutMessage(Message{ID: fmt.Sprintf("m%03d", i), ThreadID: "5", TimestampMS: int64(1000 + i)})
	}
	s.SetHasMoreBefore("5", false)
	path := filepath.Join(t.TempDir(), "cache.json")
	if err := s.Save(path); err != nil {
		t.Fatal(err)
	}
	s2 := NewStore()
	if err := s2.Load(path); err != nil {
		t.Fatal(err)
	}
	if more, known := s2.HasMoreBefore("5"); s2.MessageCount("5") == 400 || !known || !more {
		t.Fatalf("trimmed thread must keep has_more_before=true (known=%v more=%v)", known, more)
	}
	if more, _ := s.HasMoreBefore("5"); more {
		t.Fatal("saving must not change the in-memory flag")
	}
}

func TestPagingNeverSplitsSameTimestamp(t *testing.T) {
	s := NewStore()
	s.UpdateThread("5", func(rec *threadRec) {})
	s.PutMessage(Message{ID: "m1", ThreadID: "5", TimestampMS: 1000, Attachments: []Attachment{{ID: "a", Kind: "image"}, {ID: "b", Kind: "image"}, {ID: "c", Kind: "image"}}})
	s.PutMessage(Message{ID: "m0", ThreadID: "5", TimestampMS: 900, Attachments: []Attachment{{ID: "z", Kind: "image"}}})
	page1, more := cutMediaItems(s.MediaItems("5", 0, []string{"image"}), 2)
	if len(page1) != 3 || !more {
		t.Fatalf("one message's attachments must stay together: %d %v", len(page1), more)
	}
	page2, _ := cutMediaItems(s.MediaItems("5", page1[len(page1)-1].TimestampMS, []string{"image"}), 2)
	if len(page2) != 1 || page2[0].MessageID != "m0" {
		t.Fatalf("lossless continuation failed: %+v", page2)
	}
	for i, ts := range []int64{100, 200, 200, 200, 300} {
		s.PutMessage(Message{ID: fmt.Sprintf("t%d", i), ThreadID: "9", TimestampMS: ts})
	}
	msgs := s.Messages("9", 300, 2)
	if len(msgs) != 3 || msgs[0].TimestampMS != 200 {
		t.Fatalf("messages page must include the whole same-timestamp group: %+v", msgs)
	}
	if older := s.Messages("9", msgs[0].TimestampMS, 10); len(older) != 1 || older[0].TimestampMS != 100 {
		t.Fatalf("lossless older page failed: %+v", older)
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
