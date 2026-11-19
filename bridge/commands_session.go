package main

import (
	"context"
	"encoding/json"
)

func cmdStatus(ctx context.Context, d *Dispatcher, raw json.RawMessage) (any, error) {
	if err := noParams(raw); err != nil {
		return nil, err
	}
	return d.backend.Status(ctx)
}

func cmdLogin(ctx context.Context, d *Dispatcher, raw json.RawMessage) (any, error) {
	p, err := decode[LoginParams](raw)
	if err != nil {
		return nil, err
	}
	if len(p.Cookies) == 0 {
		return nil, newError(CodeBadRequest, "cookies is required")
	}
	user, err := d.backend.Login(ctx, p.Cookies)
	if err != nil {
		return nil, err
	}
	return LoginResult{User: user}, nil
}

func cmdLogout(ctx context.Context, d *Dispatcher, raw json.RawMessage) (any, error) {
	if err := noParams(raw); err != nil {
		return nil, err
	}
	return Empty{}, d.backend.Logout(ctx)
}

func cmdConnect(ctx context.Context, d *Dispatcher, raw json.RawMessage) (any, error) {
	if err := noParams(raw); err != nil {
		return nil, err
	}
	return Empty{}, d.backend.Connect(ctx)
}

func cmdSetActive(ctx context.Context, d *Dispatcher, raw json.RawMessage) (any, error) {
	p, err := decode[struct {
		Active *bool `json:"active"`
	}](raw)
	if err != nil {
		return nil, err
	}
	if p.Active == nil {
		return nil, newError(CodeBadRequest, "active is required")
	}
	return Empty{}, d.backend.SetActive(ctx, *p.Active)
}

func cmdHistoryStatus(ctx context.Context, d *Dispatcher, raw json.RawMessage) (any, error) {
	if err := noParams(raw); err != nil {
		return nil, err
	}
	return d.backend.HistoryStatus(ctx)
}

func cmdRestoreHistory(ctx context.Context, d *Dispatcher, raw json.RawMessage) (any, error) {
	p, err := decode[RestoreHistoryParams](raw)
	if err != nil {
		return nil, newError(CodeBadRequest, "invalid params")
	}
	if !validPIN(p.PIN) {
		return nil, newError(CodeBadRequest, "pin must be 6 digits")
	}
	return Empty{}, d.backend.RestoreHistory(ctx, p.PIN)
}

func cmdDiagnostics(ctx context.Context, d *Dispatcher, raw json.RawMessage) (any, error) {
	if err := noParams(raw); err != nil {
		return nil, err
	}
	return d.diagnostics(ctx)
}
