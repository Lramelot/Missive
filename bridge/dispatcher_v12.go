package main

import (
	"context"
	"strings"
)

type BackendV12 interface {
	SearchFiltered(ctx context.Context, f SearchFilter, limit int) ([]SearchHit, error)
	ExportThread(ctx context.Context, p ExportThreadParams) (ExportThreadResult, error)
	SaveMedia(ctx context.Context, p SaveMediaParams) (SaveMediaResult, error)
	CancelArchive(ctx context.Context) (CancelArchiveResult, error)
}

func (d *Dispatcher) search(ctx context.Context, p SearchParams) (any, error) {
	f := SearchFilter{
		Query:    strings.TrimSpace(p.Query),
		ThreadID: strings.TrimSpace(p.ThreadID),
		SenderID: strings.TrimSpace(p.SenderID),
		Kinds:    p.Kinds,
	}
	if p.BeforeMS != nil {
		if *p.BeforeMS <= 0 {
			return nil, newError(CodeBadRequest, "before_ms must be positive")
		}
		f.BeforeMS = *p.BeforeMS
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
	limit := 50
	if p.Limit != nil {
		if *p.Limit <= 0 {
			return nil, newError(CodeBadRequest, "limit must be positive")
		}
		limit = *p.Limit
	}
	var hits []SearchHit
	var err error
	if b, ok := d.backend.(BackendV12); ok {
		hits, err = b.SearchFiltered(ctx, f, limit)
	} else if f.SenderID == "" && len(f.Kinds) == 0 && f.BeforeMS == 0 && f.AfterMS == 0 && f.Query != "" {
		hits, err = d.backend.Search(ctx, f.Query, f.ThreadID, limit)
	} else {
		return nil, newError(CodeUnsupported, "search filters are not supported")
	}
	if err != nil {
		return nil, err
	}
	if hits == nil {
		hits = []SearchHit{}
	}
	return hits, nil
}

func (d *Dispatcher) dispatchV12(ctx context.Context, req *Request) (any, bool, error) {
	switch req.Cmd {
	case "export_thread", "save_media", "cancel_archive":
	default:
		return nil, false, nil
	}
	b, ok := d.backend.(BackendV12)
	if !ok {
		return nil, true, newError(CodeUnsupported, "%s is not supported", req.Cmd)
	}
	switch req.Cmd {
	case "cancel_archive":
		if err := decodeParams(req.Params, &struct{}{}); err != nil {
			return nil, true, err
		}
		res, err := b.CancelArchive(ctx)
		return res, true, err
	case "export_thread":
		var p ExportThreadParams
		if err := decodeParams(req.Params, &p); err != nil {
			return nil, true, err
		}
		if err := required(map[string]string{"thread_id": p.ThreadID, "dir": p.Dir}); err != nil {
			return nil, true, err
		}
		switch p.Format {
		case ExportHTML, ExportMarkdown, ExportJSON:
		default:
			return nil, true, newError(CodeBadRequest, "format must be html, markdown or json")
		}
		res, err := b.ExportThread(ctx, p)
		return res, true, err
	default:
		var p SaveMediaParams
		if err := decodeParams(req.Params, &p); err != nil {
			return nil, true, err
		}
		if err := required(map[string]string{"thread_id": p.ThreadID, "dir": p.Dir}); err != nil {
			return nil, true, err
		}
		for _, kind := range p.Kinds {
			switch kind {
			case "image", "video", "file", "audio", "sticker":
			default:
				return nil, true, newError(CodeBadRequest, "unknown kind %q", kind)
			}
		}
		res, err := b.SaveMedia(ctx, p)
		return res, true, err
	}
}
