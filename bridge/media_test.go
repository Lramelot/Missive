package main

import (
	"bytes"
	"context"
	"image/png"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"go.mau.fi/whatsmeow/proto/waCommon"
	"go.mau.fi/whatsmeow/proto/waConsumerApplication"
	"go.mau.fi/whatsmeow/proto/waMediaTransport"
	waTypes "go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
	"google.golang.org/protobuf/proto"

	"go.mau.fi/mautrix-meta/pkg/messagix/cookies"
	"go.mau.fi/mautrix-meta/pkg/messagix/table"
	"go.mau.fi/mautrix-meta/pkg/messagix/types"
)

func TestLSAttachmentMapping(t *testing.T) {
	b, _, _ := newTestMetaBackend(t)
	b.store.SetSelfID(testSelf)
	b.handleTable(&table.LSTable{
		LSInsertMessage: []*table.LSInsertMessage{{ThreadKey: 5, MessageId: "mid.$m", TimestampMs: 10, SenderId: 7}},
		LSInsertBlobAttachment: []*table.LSInsertBlobAttachment{
			{MessageId: "mid.$m", AttachmentFbid: "111", AttachmentType: table.AttachmentTypeImage, PreviewUrl: "https://cdn/small.jpg", PreviewUrlLarge: "https://cdn/large.jpg", PreviewUrlMimeType: "image/jpeg", PreviewWidth: 800, PreviewHeight: 600, Filesize: 12345, Filename: "photo.jpg"},
			{MessageId: "mid.$m", AttachmentFbid: "222", AttachmentType: table.AttachmentTypeVideo, PlayableUrl: "https://cdn/v.mp4", PlayableUrlMimeType: "video/mp4", PreviewUrl: "https://cdn/v.jpg", PlayableDurationMs: 4200, Filesize: 999},
			{MessageId: "mid.$m", AttachmentFbid: "222", AttachmentType: table.AttachmentTypeAudio, PlayableUrl: "https://cdn/a.mp4", AttachmentMimeType: "audio/mpeg", PlayableDurationMs: 1500},
		},
		LSInsertStickerAttachment: []*table.LSInsertStickerAttachment{{MessageId: "mid.$m", AttachmentFbid: "333", PreviewUrl: "https://cdn/s.png", PreviewUrlMimeType: "image/png", PreviewWidth: 120, PreviewHeight: 120}},
	}, modeLive)
	msg, ok := b.store.FindMessage("mid.$m")
	if !ok || len(msg.Attachments) != 4 {
		t.Fatalf("unexpected message %+v", msg)
	}
	img, vid, aud, st := msg.Attachments[0], msg.Attachments[1], msg.Attachments[2], msg.Attachments[3]
	if img.ID != "111" || img.Kind != "image" || img.URL != "https://cdn/large.jpg" || img.ThumbnailURL != "https://cdn/small.jpg" || img.Width != 800 || img.Size != 12345 || img.MimeType != "image/jpeg" || img.FileName != "photo.jpg" {
		t.Fatalf("bad image %+v", img)
	}
	if vid.ID != "222" || vid.Kind != "video" || vid.URL != "https://cdn/v.mp4" || vid.ThumbnailURL != "https://cdn/v.jpg" || vid.DurationMS != 4200 || vid.MimeType != "video/mp4" {
		t.Fatalf("bad video %+v", vid)
	}
	if aud.ID == "222" || aud.ID == "" || aud.Kind != "audio" || aud.MimeType != "audio/mpeg" || aud.DurationMS != 1500 {
		t.Fatalf("duplicate fbid must get a distinct id: %+v", aud)
	}
	if st.ID != "333" || st.Kind != "sticker" || st.URL != "https://cdn/s.png" {
		t.Fatalf("bad sticker %+v", st)
	}
	rec, ok := b.store.Media("mid.$m", "222")
	if !ok || rec.URL != "https://cdn/v.mp4" {
		t.Fatalf("media record not stored: %+v", rec)
	}
}

func allowTestServer(t *testing.T, srv *httptest.Server) {
	t.Helper()
	host := strings.TrimPrefix(srv.URL, "http://")
	previous := mediaURLAllowed
	mediaURLAllowed = func(u *url.URL) bool { return u.Host == host || previous(u) }
	t.Cleanup(func() { mediaURLAllowed = previous })
}

func TestMetaMediaDownloadsAndCaches(t *testing.T) {
	var hits atomic.Int32
	payload := []byte("\x89PNG\r\n\x1a\nfake-image-bytes")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		if strings.HasSuffix(r.URL.Path, "/gone") {
			w.WriteHeader(http.StatusForbidden)
			return
		}
		w.Header().Set("Content-Type", "image/png")
		w.Write(payload)
	}))
	defer srv.Close()
	allowTestServer(t, srv)

	b, _, dir := newTestMetaBackend(t)
	b.cookies = &cookies.Cookies{Platform: types.Messenger}
	b.store.SetSelfID(testSelf)
	b.handleTable(&table.LSTable{
		LSInsertMessage: []*table.LSInsertMessage{{ThreadKey: 5, MessageId: "mid.$m", TimestampMs: 10, SenderId: 7}},
		LSInsertBlobAttachment: []*table.LSInsertBlobAttachment{
			{MessageId: "mid.$m", AttachmentFbid: "1", AttachmentType: table.AttachmentTypeImage, PreviewUrl: srv.URL + "/ok", PreviewUrlMimeType: "image/png"},
			{MessageId: "mid.$m", AttachmentFbid: "2", AttachmentType: table.AttachmentTypeImage, PreviewUrl: srv.URL + "/gone"},
		},
	}, modeLive)
	ctx := context.Background()
	res, err := b.Media(ctx, "5", "mid.$m", "1", VariantFull)
	if err != nil {
		t.Fatal(err)
	}
	if !filepath.IsAbs(res.Path) || filepath.Dir(res.Path) != filepath.Join(dir, mediaDirName) || filepath.Ext(res.Path) != ".png" || res.MimeType != "image/png" {
		t.Fatalf("unexpected result %+v", res)
	}
	if data, _ := os.ReadFile(res.Path); !bytes.Equal(data, payload) {
		t.Fatal("downloaded content mismatch")
	}
	again, err := b.Media(ctx, "5", "mid.$m", "1", VariantFull)
	if err != nil || again.Path != res.Path || hits.Load() != 1 {
		t.Fatalf("second call must be served from cache: %+v %v hits=%d", again, err, hits.Load())
	}
	if _, err := b.Media(ctx, "5", "mid.$m", "2", VariantFull); toErrorBody(err).Code != CodeUnsupported {
		t.Fatalf("expired media must be unsupported, got %v", err)
	}
	for _, c := range [][3]string{{"6", "mid.$m", "1"}, {"5", "mid.$x", "1"}, {"5", "mid.$m", "9"}} {
		if _, err := b.Media(ctx, c[0], c[1], c[2], VariantFull); toErrorBody(err).Code != CodeNotFound {
			t.Fatalf("%v: expected not_found, got %v", c, err)
		}
	}
}

func TestE2EEMediaMessageIsRecorded(t *testing.T) {
	b, buf, _ := newTestMetaBackend(t)
	b.store.SetSelfID(testSelf)
	image := &waConsumerApplication.ConsumerApplication_ImageMessage{Caption: &waCommon.MessageText{Text: proto.String("look")}}
	err := image.Set(&waMediaTransport.ImageTransport{
		Integral: &waMediaTransport.ImageTransport_Integral{Transport: &waMediaTransport.WAMediaTransport{
			Integral: &waMediaTransport.WAMediaTransport_Integral{
				MediaKey:   []byte("0123456789abcdef0123456789abcdef"),
				DirectPath: proto.String("/v/t62/fake"),
			},
			Ancillary: &waMediaTransport.WAMediaTransport_Ancillary{
				FileLength: proto.Uint64(4096),
				Mimetype:   proto.String("image/jpeg"),
				ObjectID:   proto.String("obj-1"),
			},
		}},
		Ancillary: &waMediaTransport.ImageTransport_Ancillary{Width: proto.Uint32(640), Height: proto.Uint32(480)},
	})
	if err != nil {
		t.Fatal(err)
	}
	chat := waTypes.NewJID(testPeerID, waTypes.MessengerServer)
	recentTS := time.Now().Add(-time.Minute).UnixMilli()
	b.handleFBMessage(&events.FBMessage{
		Info: waTypes.MessageInfo{
			MessageSource: waTypes.MessageSource{Chat: chat, Sender: chat},
			ID:            "WAMSG1",
			Timestamp:     time.UnixMilli(recentTS),
		},
		Message: &waConsumerApplication.ConsumerApplication{Payload: &waConsumerApplication.ConsumerApplication_Payload{
			Payload: &waConsumerApplication.ConsumerApplication_Payload_Content{Content: &waConsumerApplication.ConsumerApplication_Content{
				Content: &waConsumerApplication.ConsumerApplication_Content_ImageMessage{ImageMessage: image},
			}},
		}},
	})
	msg, ok := b.store.MessageInThread(testPeerID, "WAMSG1")
	if !ok || msg.Text != "look" || len(msg.Attachments) != 1 {
		t.Fatalf("unexpected message %+v", msg)
	}
	att := msg.Attachments[0]
	if att.ID != "obj-1" || att.Kind != "image" || att.URL != "" || att.MimeType != "image/jpeg" || att.Size != 4096 || att.Width != 640 || att.Height != 480 {
		t.Fatalf("bad e2ee attachment %+v", att)
	}
	rec, ok := b.store.Media("WAMSG1", "obj-1")
	if !ok || rec.WAType == "" || len(rec.WATransport) == 0 {
		t.Fatalf("download params not kept: %+v", rec)
	}
	var transport waMediaTransport.WAMediaTransport
	if err := proto.Unmarshal(rec.WATransport, &transport); err != nil || transport.GetIntegral().GetDirectPath() != "/v/t62/fake" {
		t.Fatalf("persisted transport unusable: %v", err)
	}
	th, ok := b.store.ThreadView(testPeerID)
	if !ok || !th.E2EE || th.LastActivityMS != recentTS || th.Snippet != "look" {
		t.Fatalf("thread not bumped by e2ee message: %+v", th)
	}
	if len(eventsOf(t, buf, EventMessage)) != 1 {
		t.Fatal("e2ee message not emitted")
	}

	path := filepath.Join(t.TempDir(), "cache.json")
	if err := b.store.Save(path); err != nil {
		t.Fatal(err)
	}
	reloaded := NewStore()
	if err := reloaded.Load(path); err != nil {
		t.Fatal(err)
	}
	if again, ok := reloaded.Media("WAMSG1", "obj-1"); !ok || !bytes.Equal(again.WATransport, rec.WATransport) {
		t.Fatal("media record not persisted")
	}
}

func TestDemoMediaGeneratesPNG(t *testing.T) {
	h := newDemoHarness(t)
	var target Message
	var att Attachment
	for _, th := range h.demo.store.ListThreads(0) {
		for _, m := range h.demo.store.Messages(th.ID, 0, 0) {
			for _, a := range m.Attachments {
				if a.Kind == "image" && target.ID == "" {
					target, att = m, a
				}
			}
		}
	}
	if target.ID == "" {
		t.Fatal("demo has no image attachments")
	}
	h.send(`{"id":1,"cmd":"media","params":{"thread_id":"` + target.ThreadID + `","message_id":"` + target.ID + `","attachment_id":"` + att.ID + `"}}`)
	var res MediaResult
	h.okResult(1, &res)
	f, err := os.Open(res.Path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	cfg, err := png.DecodeConfig(f)
	if err != nil {
		t.Fatalf("not a PNG: %v", err)
	}
	if res.MimeType != "image/png" || cfg.Width == 0 || max(cfg.Width, cfg.Height) > demoMaxImageSide {
		t.Fatalf("unexpected image %+v %dx%d", res, cfg.Width, cfg.Height)
	}
	if (att.Width > att.Height) != (cfg.Width > cfg.Height) {
		t.Fatalf("aspect ratio lost: %dx%d vs %dx%d", att.Width, att.Height, cfg.Width, cfg.Height)
	}
	h.send(`{"id":2,"cmd":"media","params":{"thread_id":"`+target.ThreadID+`","message_id":"`+target.ID+`","attachment_id":"nope"}}`, `{"id":3,"cmd":"media","params":{"thread_id":"x"}}`)
	if code := h.errCode(2); code != CodeNotFound {
		t.Fatalf("got %s", code)
	}
	if code := h.errCode(3); code != CodeBadRequest {
		t.Fatalf("got %s", code)
	}
}
