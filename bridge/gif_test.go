package main

import (
	"testing"

	"go.mau.fi/mautrix-meta/pkg/messagix/table"
)

func TestLSGifAttachmentsBecomeGifVideos(t *testing.T) {
	cases := []struct {
		name     string
		media    lsMedia
		wantKind string
		wantGif  bool
	}{
		{"animated image with mp4", lsMedia{kind: table.AttachmentTypeAnimatedImage, playable: "https://x/a.mp4", playableMime: "video/mp4", preview: "https://x/a.jpg"}, "video", true},
		{"animated image without playable", lsMedia{kind: table.AttachmentTypeAnimatedImage, preview: "https://x/a.gif", previewMime: "image/gif"}, "image", false},
		{"animated image with gif playable", lsMedia{kind: table.AttachmentTypeAnimatedImage, playable: "https://x/a.gif", playableMime: "image/gif"}, "image", false},
		{"video with gif preview", lsMedia{kind: table.AttachmentTypeVideo, playable: "https://x/v.mp4", playableMime: "video/mp4", preview: "https://x/v.gif", previewMime: "image/gif"}, "video", true},
		{"video named gif", lsMedia{kind: table.AttachmentTypeVideo, playable: "https://x/v.mp4", attMime: "video/mp4", fileName: "giphy.GIF"}, "video", true},
		{"plain video", lsMedia{kind: table.AttachmentTypeVideo, playable: "https://x/v.mp4", playableMime: "video/mp4", preview: "https://x/v.jpg", previewMime: "image/jpeg"}, "video", false},
		{"plain image", lsMedia{kind: table.AttachmentTypeImage, preview: "https://x/i.jpg", previewMime: "image/jpeg"}, "image", false},
	}
	for _, c := range cases {
		att, rec := c.media.convert("a")
		if att.Kind != c.wantKind || att.Gif != c.wantGif || rec.Kind != c.wantKind {
			t.Errorf("%s: kind=%q gif=%v rec=%q, want kind=%q gif=%v", c.name, att.Kind, att.Gif, rec.Kind, c.wantKind, c.wantGif)
		}
	}
}
