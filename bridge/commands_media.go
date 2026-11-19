package main

import (
	"context"
	"encoding/json"
	"strings"
)

func cmdMedia(ctx context.Context, d *Dispatcher, raw json.RawMessage) (any, error) {
	p, err := decode[MediaParams](raw)
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(p.ThreadID) == "" || strings.TrimSpace(p.MessageID) == "" || strings.TrimSpace(p.AttachmentID) == "" {
		return nil, newError(CodeBadRequest, "thread_id, message_id and attachment_id are required")
	}
	switch p.Variant {
	case "":
		p.Variant = VariantFull
	case VariantFull, VariantThumb:
	default:
		return nil, newError(CodeBadRequest, "unknown variant %q", p.Variant)
	}
	return d.backend.Media(ctx, p.ThreadID, p.MessageID, p.AttachmentID, p.Variant)
}

func cmdAvatar(ctx context.Context, d *Dispatcher, raw json.RawMessage) (any, error) {
	p, err := decode[AvatarParams](raw)
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(p.URL) == "" {
		return nil, newError(CodeBadRequest, "url is required")
	}
	path, err := d.backend.Avatar(ctx, p.URL)
	if err != nil {
		return nil, err
	}
	return AvatarResult{Path: path}, nil
}

func cmdMediaHistory(ctx context.Context, d *Dispatcher, raw json.RawMessage) (any, error) {
	p, err := decode[MediaHistoryParams](raw)
	if err != nil {
		return nil, err
	}
	if err := requireThreadID(p.ThreadID); err != nil {
		return nil, err
	}
	limit, err := optionalLimit(p.Limit, 60)
	if err != nil {
		return nil, err
	}
	before, err := optionalBefore(p.BeforeMS)
	if err != nil {
		return nil, err
	}
	kinds := p.Kinds
	if len(kinds) == 0 {
		kinds = []string{"image", "video"}
	}
	for _, kind := range kinds {
		switch kind {
		case "image", "video", "file", "link", "audio", "sticker", "other":
		default:
			return nil, newError(CodeBadRequest, "unknown kind %q", kind)
		}
	}
	maxPages := maxMediaHistoryPages
	if p.MaxPages != nil {
		if *p.MaxPages < 0 {
			return nil, newError(CodeBadRequest, "max_pages must not be negative")
		}
		maxPages = min(*p.MaxPages, maxMediaHistoryPages)
	}
	res, err := d.backend.MediaHistory(ctx, p.ThreadID, before, limit, kinds, maxPages)
	if err != nil {
		return nil, err
	}
	if res.Items == nil {
		res.Items = []MediaItem{}
	}
	for i := range res.Items {
		if res.Items[i].Attachment.Waveform == nil {
			res.Items[i].Attachment.Waveform = []float64{}
		}
	}
	return res, nil
}
