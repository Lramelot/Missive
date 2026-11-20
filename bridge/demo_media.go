package main

import (
	"bytes"
	"context"
	"fmt"
	"hash/fnv"
	"image"
	"image/color"
	"image/png"
	"net/url"
	"os"
	"strings"
)

const (
	demoMaxImageSide   = 800
	demoThumbSide      = 320
	demoAvatarSide     = 160
	demoAvatarHost     = "demo.fbcdn.net"
	demoAvatarPathBase = "/avatars/"
)

func demoAvatarURL(id string) string {
	return "https://" + demoAvatarHost + demoAvatarPathBase + id + ".png"
}

func demoAvatarFetcher(ctx context.Context, rawURL string, f *os.File) (string, error) {
	parsed, err := url.Parse(rawURL)
	if err != nil || parsed.Host != demoAvatarHost || !strings.HasPrefix(parsed.Path, demoAvatarPathBase) {
		return "", fmt.Errorf("%w: unknown demo avatar", errMediaExpired)
	}
	data, err := demoPNG("avatar/"+strings.TrimPrefix(parsed.Path, demoAvatarPathBase), demoAvatarSide, demoAvatarSide)
	if err != nil {
		return "", err
	}
	if _, err := f.Write(data); err != nil {
		return "", err
	}
	return "image/png", nil
}

func (d *DemoBackend) Avatar(ctx context.Context, rawURL string) (string, error) {
	d.mu.Lock()
	err := d.requireLogin()
	d.mu.Unlock()
	if err != nil {
		return "", err
	}
	return d.avatars.Get(ctx, rawURL)
}

func (d *DemoBackend) mediaThumb(messageID string, att Attachment) (MediaResult, error) {
	switch att.Kind {
	case "image", "sticker", "link", "video":
	default:
		return MediaResult{}, newError(CodeNotFound, "attachment %s has no thumbnail", att.ID)
	}
	path := thumbPath(d.cacheDir, messageID, att.ID)
	if res, ok := readThumbResult(path); ok {
		return res, nil
	}
	w, h := att.Width, att.Height
	if w <= 0 || h <= 0 {
		w, h = 240, 240
	}
	if longest := max(w, h); longest > demoThumbSide {
		w, h = max(1, w*demoThumbSide/longest), max(1, h*demoThumbSide/longest)
	}
	data, err := demoPNG(messageID+"/"+att.ID, w, h)
	if err != nil {
		return MediaResult{}, newError(CodeInternal, "failed to render demo thumbnail: %v", err)
	}
	res, err := thumbFromBytes(path, data)
	if err != nil {
		return MediaResult{}, newError(CodeInternal, "%v", err)
	}
	return res, nil
}

func (d *DemoBackend) Media(ctx context.Context, threadID, messageID, attachmentID, variant string) (MediaResult, error) {
	d.mu.Lock()
	if err := d.requireLogin(); err != nil {
		d.mu.Unlock()
		return MediaResult{}, err
	}
	msg, ok := d.store.MessageInThread(threadID, messageID)
	d.mu.Unlock()
	if !ok {
		return MediaResult{}, newError(CodeNotFound, "unknown message %s in thread %s", messageID, threadID)
	}
	att, ok := findAttachment(msg, attachmentID)
	if !ok {
		return MediaResult{}, newError(CodeNotFound, "unknown attachment %s", attachmentID)
	}
	if variant == VariantThumb {
		return d.mediaThumb(messageID, att)
	}
	if rec, ok := d.store.Media(messageID, attachmentID); ok && fileExists(rec.Path) {
		return MediaResult{Path: rec.Path, MimeType: rec.MimeType}, nil
	}
	data, mimeType, err := demoMediaData(messageID, att)
	if err != nil {
		return MediaResult{}, err
	}
	path, mimeType, err := saveMediaFile(d.cacheDir, data, mimeType, att.FileName)
	if err != nil {
		return MediaResult{}, newError(CodeInternal, "%v", err)
	}
	d.store.SetMediaPath(messageID, attachmentID, path, mimeType)
	return MediaResult{Path: path, MimeType: mimeType}, nil
}

func demoMediaData(messageID string, att Attachment) ([]byte, string, error) {
	if att.Kind == "video" {
		if sample := os.Getenv("MISSIVE_DEMO_VIDEO"); sample != "" {
			if data, err := os.ReadFile(sample); err == nil {
				return data, "video/mp4", nil
			}
		}
	}
	switch att.Kind {
	case "image", "sticker", "link", "video":
		data, err := demoPNG(messageID+"/"+att.ID, att.Width, att.Height)
		if err != nil {
			return nil, "", newError(CodeInternal, "failed to render demo image: %v", err)
		}
		return data, "image/png", nil
	case "file":
		return []byte(fmt.Sprintf("Missive demo file\nmessage: %s\nattachment: %s\n", messageID, att.ID)), "text/plain", nil
	}
	return nil, "", newError(CodeUnsupported, "demo has no media for %s attachments", att.Kind)
}

var demoPalette = []color.NRGBA{
	{0xE7, 0x6F, 0x51, 0xFF},
	{0x2A, 0x9D, 0x8F, 0xFF},
	{0xE9, 0xC4, 0x6A, 0xFF},
	{0x26, 0x46, 0x53, 0xFF},
	{0x8E, 0x7D, 0xBE, 0xFF},
	{0xF4, 0xA2, 0x61, 0xFF},
	{0x4D, 0x90, 0x8E, 0xFF},
	{0xD6, 0x28, 0x28, 0xFF},
}

func demoPNG(seed string, width, height int) ([]byte, error) {
	if width <= 0 || height <= 0 {
		width, height = 240, 240
	}
	if longest := max(width, height); longest > demoMaxImageSide {
		width = width * demoMaxImageSide / longest
		height = height * demoMaxImageSide / longest
	}
	h := fnv.New64a()
	h.Write([]byte(seed))
	sum := h.Sum64()
	from := demoPalette[sum%uint64(len(demoPalette))]
	to := demoPalette[(sum>>8)%uint64(len(demoPalette))]
	if from == to {
		to = demoPalette[(sum>>8+1)%uint64(len(demoPalette))]
	}
	cx := int(sum>>16%uint64(width)) + 1
	cy := int(sum>>32%uint64(height)) + 1
	radius := min(width, height) / 4
	img := image.NewNRGBA(image.Rect(0, 0, width, height))
	for y := 0; y < height; y++ {
		for x := 0; x < width; x++ {
			t := float64(x+y) / float64(width+height)
			c := color.NRGBA{
				R: lerp(from.R, to.R, t),
				G: lerp(from.G, to.G, t),
				B: lerp(from.B, to.B, t),
				A: 0xFF,
			}
			dx, dy := x-cx, y-cy
			if dx*dx+dy*dy < radius*radius {
				c = color.NRGBA{R: 0xFF - c.R/3, G: 0xFF - c.G/3, B: 0xFF - c.B/3, A: 0xFF}
			}
			img.SetNRGBA(x, y, c)
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func lerp(a, b uint8, t float64) uint8 {
	return uint8(float64(a) + (float64(b)-float64(a))*t)
}
