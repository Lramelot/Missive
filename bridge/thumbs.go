package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"image"
	_ "image/gif"
	"image/jpeg"
	_ "image/png"
	"io"
	"os"
	"path/filepath"
	"time"

	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/proto/waMediaTransport"
	"golang.org/x/image/draw"
	_ "golang.org/x/image/webp"
	"google.golang.org/protobuf/proto"
)

const (
	thumbDirName         = "thumbs"
	maxThumbSide         = 640
	thumbJPEGQuality     = 82
	maxThumbSourcePixels = 64 << 20
	fastScaleThreshold   = 2048
	waThumbTimeout       = 30 * time.Second
)

type waThumbRef struct {
	DirectPath string
	EncSHA256  []byte
	SHA256     []byte
	MediaKey   []byte
	MediaType  whatsmeow.MediaType
	MMSType    string
}

func thumbPath(dataDir, messageID, attachmentID string) string {
	sum := sha256.Sum256([]byte(messageID + "\x00" + attachmentID))
	return filepath.Join(dataDir, mediaDirName, thumbDirName, hex.EncodeToString(sum[:])+".jpg")
}

func readThumbResult(path string) (MediaResult, bool) {
	f, err := os.Open(path)
	if err != nil {
		return MediaResult{}, false
	}
	defer f.Close()
	cfg, err := jpeg.DecodeConfig(f)
	if err != nil {
		return MediaResult{}, false
	}
	return MediaResult{Path: path, MimeType: "image/jpeg", Width: cfg.Width, Height: cfg.Height}, true
}

func decodeImageLimited(r io.ReadSeeker) (image.Image, error) {
	cfg, _, err := image.DecodeConfig(r)
	if err != nil {
		return nil, err
	}
	if cfg.Width <= 0 || cfg.Height <= 0 || int64(cfg.Width)*int64(cfg.Height) > maxThumbSourcePixels {
		return nil, fmt.Errorf("image dimensions %dx%d out of range", cfg.Width, cfg.Height)
	}
	if _, err := r.Seek(0, io.SeekStart); err != nil {
		return nil, err
	}
	img, _, err := image.Decode(r)
	return img, err
}

func thumbSize(w, h int) (int, int) {
	if w <= maxThumbSide && h <= maxThumbSide {
		return w, h
	}
	if w >= h {
		return maxThumbSide, max(1, h*maxThumbSide/w)
	}
	return max(1, w*maxThumbSide/h), maxThumbSide
}

func scaleImage(src image.Image) *image.RGBA {
	bounds := src.Bounds()
	w, h := thumbSize(bounds.Dx(), bounds.Dy())
	dst := image.NewRGBA(image.Rect(0, 0, w, h))
	draw.Draw(dst, dst.Bounds(), image.White, image.Point{}, draw.Src)
	var scaler draw.Scaler = draw.CatmullRom
	if max(bounds.Dx(), bounds.Dy()) > fastScaleThreshold {
		scaler = draw.ApproxBiLinear
	}
	scaler.Scale(dst, dst.Bounds(), src, bounds, draw.Over, nil)
	return dst
}

func writeThumb(path string, src image.Image) (MediaResult, error) {
	img := scaleImage(src)
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, img, &jpeg.Options{Quality: thumbJPEGQuality}); err != nil {
		return MediaResult{}, err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return MediaResult{}, err
	}
	if err := writeFileAtomic(path, buf.Bytes()); err != nil {
		return MediaResult{}, err
	}
	return MediaResult{Path: path, MimeType: "image/jpeg", Width: img.Rect.Dx(), Height: img.Rect.Dy()}, nil
}

func thumbFromBytes(path string, data []byte) (MediaResult, error) {
	img, err := decodeImageLimited(bytes.NewReader(data))
	if err != nil {
		return MediaResult{}, err
	}
	return writeThumb(path, img)
}

func thumbFromFile(path, source string) (MediaResult, error) {
	f, err := os.Open(source)
	if err != nil {
		return MediaResult{}, err
	}
	img, err := decodeImageLimited(f)
	_ = f.Close()
	if err != nil {
		return MediaResult{}, err
	}
	return writeThumb(path, img)
}

func inlineThumbnail(rec mediaRec) []byte {
	if rec.WATransport == nil {
		return nil
	}
	var transport waMediaTransport.WAMediaTransport
	if err := proto.Unmarshal(rec.WATransport, &transport); err != nil {
		return nil
	}
	return transport.GetAncillary().GetThumbnail().GetJPEGThumbnail()
}

func downloadableThumbnail(rec mediaRec) (waThumbRef, bool) {
	if rec.WATransport == nil {
		return waThumbRef{}, false
	}
	var transport waMediaTransport.WAMediaTransport
	if err := proto.Unmarshal(rec.WATransport, &transport); err != nil {
		return waThumbRef{}, false
	}
	dt := transport.GetAncillary().GetThumbnail().GetDownloadableThumbnail()
	if dt.GetDirectPath() == "" {
		return waThumbRef{}, false
	}
	mediaType := whatsmeow.MediaType(rec.WAType)
	mmsType := "thumbnail-image"
	switch mediaType {
	case whatsmeow.MediaVideo:
		mmsType = "thumbnail-video"
	case whatsmeow.MediaDocument:
		mmsType = "thumbnail-document"
	case whatsmeow.MediaImage:
	default:
		mediaType = whatsmeow.MediaImage
	}
	mediaKey := dt.GetMediaKey()
	if len(mediaKey) == 0 {
		mediaKey = transport.GetIntegral().GetMediaKey()
	}
	return waThumbRef{
		DirectPath: dt.GetDirectPath(),
		EncSHA256:  dt.GetFileEncSHA256(),
		SHA256:     dt.GetFileSHA256(),
		MediaKey:   mediaKey,
		MediaType:  mediaType,
		MMSType:    mmsType,
	}, true
}

func (b *MetaBackend) downloadWAThumb(ctx context.Context, ref waThumbRef) ([]byte, error) {
	if b.waThumbFn != nil {
		return b.waThumbFn(ctx, ref)
	}
	ctx, cancel := context.WithTimeout(ctx, waThumbTimeout)
	defer cancel()
	cli, err := b.waitE2EE(ctx)
	if err != nil {
		return nil, err
	}
	data, err := cli.DownloadMediaWithPath(ctx, ref.DirectPath, ref.EncSHA256, ref.SHA256, ref.MediaKey, ref.MediaType, ref.MMSType, false)
	if err == nil && len(data) > maxPreviewBytes {
		return nil, fmt.Errorf("thumbnail larger than %d bytes", maxPreviewBytes)
	}
	return data, err
}

func thumbnailable(kind string) bool {
	switch kind {
	case "image", "sticker", "link", "other":
		return true
	}
	return false
}

func (b *MetaBackend) thumbFromURL(ctx context.Context, path, rawURL string) (MediaResult, error) {
	f, err := newMediaTemp(b.cacheDir)
	if err != nil {
		return MediaResult{}, err
	}
	defer discardMediaTemp(f)
	if _, err := httpDownloadToFile(ctx, rawURL, maxPreviewBytes, f); err != nil {
		return MediaResult{}, err
	}
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return MediaResult{}, err
	}
	img, err := decodeImageLimited(f)
	if err != nil {
		return MediaResult{}, err
	}
	return writeThumb(path, img)
}

func (b *MetaBackend) mediaThumb(ctx context.Context, messageID string, att Attachment) (MediaResult, error) {
	path := thumbPath(b.cacheDir, messageID, att.ID)
	unlock := b.lockMediaKey("thumb|" + mediaKey(messageID, att.ID))
	defer unlock()
	if res, ok := readThumbResult(path); ok {
		return res, nil
	}
	rec, _ := b.store.Media(messageID, att.ID)
	if data := inlineThumbnail(rec); len(data) > 0 {
		res, err := thumbFromBytes(path, data)
		if err == nil {
			return res, nil
		}
		b.log.Debug().Err(err).Msg("Inline thumbnail unusable")
	}
	if ref, ok := downloadableThumbnail(rec); ok {
		data, err := b.downloadWAThumb(ctx, ref)
		if errors.Is(err, whatsmeow.ErrInvalidMediaHMAC) && ref.MediaType != whatsmeow.MediaImage {
			ref.MediaType = whatsmeow.MediaImage
			data, err = b.downloadWAThumb(ctx, ref)
		}
		if err == nil {
			res, err := thumbFromBytes(path, data)
			if err == nil {
				return res, nil
			}
			b.log.Debug().Err(err).Msg("Downloaded thumbnail unusable")
		} else {
			b.log.Debug().Err(err).Msg("Thumbnail download failed")
		}
	}
	if att.ThumbnailURL != "" {
		res, err := b.thumbFromURL(ctx, path, att.ThumbnailURL)
		if err == nil {
			return res, nil
		}
		b.log.Debug().Err(err).Msg("Thumbnail URL unusable")
	}
	if !thumbnailable(att.Kind) {
		return MediaResult{}, newError(CodeNotFound, "attachment %s has no thumbnail", att.ID)
	}
	full, err := b.fullMedia(ctx, messageID, att)
	if err != nil {
		return MediaResult{}, err
	}
	res, err := thumbFromFile(path, full.Path)
	if err != nil {
		return MediaResult{}, newError(CodeUnsupported, "cannot build a thumbnail: %v", err)
	}
	return res, nil
}
