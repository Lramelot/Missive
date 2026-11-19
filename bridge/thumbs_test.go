package main

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"

	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/proto/waMediaTransport"
	"google.golang.org/protobuf/proto"
)

func inlineTransport(t *testing.T, thumb []byte) []byte {
	t.Helper()
	raw, err := proto.Marshal(&waMediaTransport.WAMediaTransport{
		Integral: &waMediaTransport.WAMediaTransport_Integral{DirectPath: proto.String("/v/t62/x")},
		Ancillary: &waMediaTransport.WAMediaTransport_Ancillary{
			Mimetype:  proto.String("image/jpeg"),
			Thumbnail: &waMediaTransport.WAMediaTransport_Ancillary_Thumbnail{JPEGThumbnail: thumb},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func TestThumbSourcePriority(t *testing.T) {
	var thumbHits, fullHits atomic.Int32
	big := noisyPNG(t, 1600, 1200)
	small := encodedImage(t, 200, 150, true)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/thumb.jpg":
			thumbHits.Add(1)
			w.Header().Set("Content-Type", "image/jpeg")
			w.Write(small)
		default:
			fullHits.Add(1)
			w.Header().Set("Content-Type", "image/png")
			w.Write(big)
		}
	}))
	defer srv.Close()
	allowTestServer(t, srv)
	b, _, dir := loggedInBackend(t)
	ctx := context.Background()
	inline := encodedImage(t, 48, 36, true)
	b.store.PutMessage(Message{ID: "e2ee", ThreadID: "5", TimestampMS: 1, Attachments: []Attachment{{ID: "a", Kind: "image", ThumbnailURL: srv.URL + "/thumb.jpg"}}})
	b.store.PutMedia("e2ee", "a", mediaRec{Kind: "image", WAType: "image", WATransport: inlineTransport(t, inline)})
	b.store.PutMessage(Message{ID: "ls", ThreadID: "5", TimestampMS: 2, Attachments: []Attachment{{ID: "a", Kind: "image", URL: srv.URL + "/full.png", ThumbnailURL: srv.URL + "/thumb.jpg"}}})
	b.store.PutMessage(Message{ID: "full", ThreadID: "5", TimestampMS: 3, Attachments: []Attachment{{ID: "a", Kind: "image", URL: srv.URL + "/full.png"}}})
	b.store.PutMessage(Message{ID: "vid", ThreadID: "5", TimestampMS: 4, Attachments: []Attachment{{ID: "a", Kind: "video", URL: srv.URL + "/full.mp4"}}})
	b.store.PutMessage(Message{ID: "poster", ThreadID: "5", TimestampMS: 5, Attachments: []Attachment{{ID: "a", Kind: "video", URL: srv.URL + "/full.mp4", ThumbnailURL: srv.URL + "/thumb.jpg"}}})

	res, err := b.Media(ctx, "5", "e2ee", "a", VariantThumb)
	if err != nil || res.Width != 48 || res.Height != 36 || thumbHits.Load()+fullHits.Load() != 0 {
		t.Fatalf("inline thumbnail must win without network: %+v %v", res, err)
	}
	if filepath.Dir(res.Path) != filepath.Join(dir, mediaDirName, thumbDirName) || res.MimeType != "image/jpeg" {
		t.Fatalf("unexpected thumb location %+v", res)
	}

	res, err = b.Media(ctx, "5", "ls", "a", VariantThumb)
	if err != nil || res.Width != 200 || thumbHits.Load() != 1 || fullHits.Load() != 0 {
		t.Fatalf("thumbnail_url must come before the full download: %+v %v", res, err)
	}
	if again, _ := b.Media(ctx, "5", "ls", "a", VariantThumb); again != res || thumbHits.Load() != 1 {
		t.Fatal("thumbs must be cached on disk")
	}

	res, err = b.Media(ctx, "5", "full", "a", VariantThumb)
	if err != nil || res.Width != maxThumbSide || res.Height != 480 || fullHits.Load() != 1 {
		t.Fatalf("full image must be downscaled to %d px: %+v %v", maxThumbSide, res, err)
	}
	info, _ := os.Stat(res.Path)
	if info.Size() >= int64(len(big)) {
		t.Fatal("thumb must be smaller than the full image")
	}

	if _, err := b.Media(ctx, "5", "vid", "a", VariantThumb); toErrorBody(err).Code != CodeNotFound {
		t.Fatalf("video without poster must be not_found, got %v", err)
	}
	if fullHits.Load() != 1 {
		t.Fatal("a video must never be downloaded to build a thumb")
	}
	if res, err := b.Media(ctx, "5", "poster", "a", VariantThumb); err != nil || res.Width != 200 {
		t.Fatalf("video poster from thumbnail_url failed: %+v %v", res, err)
	}

	cachePath := filepath.Join(dir, "reload.json")
	if err := b.store.Save(cachePath); err != nil {
		t.Fatal(err)
	}
	b2, _, dir2 := loggedInBackend(t)
	if err := b2.store.Load(cachePath); err != nil {
		t.Fatal(err)
	}
	srv.Close()
	res, err = b2.Media(ctx, "5", "e2ee", "a", VariantThumb)
	if err != nil || res.Width != 48 || filepath.Dir(filepath.Dir(filepath.Dir(res.Path))) != dir2 {
		t.Fatalf("inline thumbnail must survive a store reload: %+v %v", res, err)
	}
}

func downloadableTransport(t *testing.T, inline []byte) []byte {
	t.Helper()
	raw, err := proto.Marshal(&waMediaTransport.WAMediaTransport{
		Integral: &waMediaTransport.WAMediaTransport_Integral{DirectPath: proto.String("/v/t62/full"), MediaKey: []byte("full-key")},
		Ancillary: &waMediaTransport.WAMediaTransport_Ancillary{
			Thumbnail: &waMediaTransport.WAMediaTransport_Ancillary_Thumbnail{
				JPEGThumbnail: inline,
				DownloadableThumbnail: &waMediaTransport.WAMediaTransport_Ancillary_Thumbnail_DownloadableThumbnail{
					DirectPath:    proto.String("/v/t62/thumb"),
					FileSHA256:    []byte("sha"),
					FileEncSHA256: []byte("enc"),
				},
			},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func TestDownloadableE2EEThumb(t *testing.T) {
	b, _, dir := loggedInBackend(t)
	ctx := context.Background()
	fullPath := filepath.Join(dir, "full.png")
	os.WriteFile(fullPath, encodedImage(t, 1600, 1200, false), 0o600)
	var refs []waThumbRef
	fail := false
	b.waThumbFn = func(ctx context.Context, ref waThumbRef) ([]byte, error) {
		refs = append(refs, ref)
		if fail {
			return nil, fmt.Errorf("boom")
		}
		return encodedImage(t, 300, 200, true), nil
	}
	put := func(id, kind, waType string, inline []byte) {
		b.store.PutMessage(Message{ID: id, ThreadID: "5", TimestampMS: 1, Attachments: []Attachment{{ID: "a", Kind: kind}}})
		b.store.PutMedia(id, "a", mediaRec{Kind: kind, WAType: waType, WATransport: downloadableTransport(t, inline)})
		b.store.SetMediaPath(id, "a", fullPath, "image/png")
	}
	put("img", "image", string(whatsmeow.MediaImage), nil)
	put("vid", "video", string(whatsmeow.MediaVideo), nil)
	put("both", "image", "image", encodedImage(t, 40, 30, true))
	put("broken", "image", "image", nil)
	put("brokenvid", "video", string(whatsmeow.MediaVideo), nil)

	res, err := b.Media(ctx, "5", "img", "a", VariantThumb)
	if err != nil || res.Width != 300 || len(refs) != 1 {
		t.Fatalf("downloadable thumbnail must be preferred over the full file: %+v %v", res, err)
	}
	if r := refs[0]; r.DirectPath != "/v/t62/thumb" || string(r.MediaKey) != "full-key" || string(r.EncSHA256) != "enc" || string(r.SHA256) != "sha" || r.MediaType != "WhatsApp Image Keys" || r.MMSType != "thumbnail-image" {
		t.Fatalf("unexpected thumbnail download parameters %+v", r)
	}
	res, err = b.Media(ctx, "5", "vid", "a", VariantThumb)
	if err != nil || res.Width != 300 || refs[1].MMSType != "thumbnail-video" || refs[1].MediaType != "WhatsApp Video Keys" {
		t.Fatalf("video poster must come from the downloadable thumbnail: %+v %v %+v", res, err, refs)
	}
	if res, err := b.Media(ctx, "5", "both", "a", VariantThumb); err != nil || res.Width != 40 || len(refs) != 2 {
		t.Fatalf("inline bytes must win over the thumbnail download: %+v %v", res, err)
	}

	fail = true
	res, err = b.Media(ctx, "5", "broken", "a", VariantThumb)
	if err != nil || res.Width != maxThumbSide || len(refs) != 3 {
		t.Fatalf("a failed thumbnail download must fall back to the full image: %+v %v", res, err)
	}
	if _, err := b.Media(ctx, "5", "brokenvid", "a", VariantThumb); toErrorBody(err).Code != CodeNotFound {
		t.Fatalf("a video whose thumbnail fails must be not_found, got %v", err)
	}

	cachePath := filepath.Join(dir, "reload.json")
	if err := b.store.Save(cachePath); err != nil {
		t.Fatal(err)
	}
	reloaded := NewStore()
	if err := reloaded.Load(cachePath); err != nil {
		t.Fatal(err)
	}
	rec, _ := reloaded.Media("img", "a")
	if ref, ok := downloadableThumbnail(rec); !ok || ref.DirectPath != "/v/t62/thumb" {
		t.Fatal("thumbnail download details must survive a store reload")
	}
}
