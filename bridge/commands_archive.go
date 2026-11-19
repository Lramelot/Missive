package main

import (
	"context"
	"encoding/json"
	"strings"
)

func cmdSearch(ctx context.Context, d *Dispatcher, raw json.RawMessage) (any, error) {
	p, err := decode[SearchParams](raw)
	if err != nil {
		return nil, err
	}
	f := SearchFilter{
		Query:    strings.TrimSpace(p.Query),
		ThreadID: strings.TrimSpace(p.ThreadID),
		SenderID: strings.TrimSpace(p.SenderID),
		Kinds:    p.Kinds,
	}
	if f.BeforeMS, err = optionalBefore(p.BeforeMS); err != nil {
		return nil, err
	}
	if p.AfterMS != nil {
		if *p.AfterMS < 0 {
			return nil, newError(CodeBadRequest, "after_ms must not be negative")
		}
		f.AfterMS = *p.AfterMS
	}
	if kind, ok := validSearchKinds(f.Kinds); !ok {
		return nil, newError(CodeBadRequest, "unknown kind %q", kind)
	}
	if f.Query == "" && !f.filtered() {
		return nil, newError(CodeBadRequest, "query is required when no filter is set")
	}
	limit, err := optionalLimit(p.Limit, 50)
	if err != nil {
		return nil, err
	}
	hits, err := d.backend.SearchFiltered(ctx, f, limit)
	if err != nil {
		return nil, err
	}
	if hits == nil {
		hits = []SearchHit{}
	}
	return hits, nil
}

func cmdExportThread(ctx context.Context, d *Dispatcher, raw json.RawMessage) (any, error) {
	p, err := decode[ExportThreadParams](raw)
	if err != nil {
		return nil, err
	}
	if err := required(map[string]string{"thread_id": p.ThreadID, "dir": p.Dir}); err != nil {
		return nil, err
	}
	switch p.Format {
	case ExportHTML, ExportMarkdown, ExportJSON:
	default:
		return nil, newError(CodeBadRequest, "format must be html, markdown or json")
	}
	return d.backend.ExportThread(ctx, p)
}

func cmdSaveMedia(ctx context.Context, d *Dispatcher, raw json.RawMessage) (any, error) {
	p, err := decode[SaveMediaParams](raw)
	if err != nil {
		return nil, err
	}
	if err := required(map[string]string{"thread_id": p.ThreadID, "dir": p.Dir}); err != nil {
		return nil, err
	}
	for _, kind := range p.Kinds {
		switch kind {
		case "image", "video", "file", "audio", "sticker":
		default:
			return nil, newError(CodeBadRequest, "unknown kind %q", kind)
		}
	}
	return d.backend.SaveMedia(ctx, p)
}

func cmdCancelArchive(ctx context.Context, d *Dispatcher, raw json.RawMessage) (any, error) {
	if err := noParams(raw); err != nil {
		return nil, err
	}
	return d.backend.CancelArchive(ctx)
}
