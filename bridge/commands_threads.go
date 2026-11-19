package main

import (
	"context"
	"encoding/json"
	"strings"
	"time"
)

const (
	defaultUserSearchLimit = 20
	maxUserSearchLimit     = 100
	maxThreadNameLength    = 250
)

func cmdThreads(ctx context.Context, d *Dispatcher, raw json.RawMessage) (any, error) {
	p, err := decode[ThreadsParams](raw)
	if err != nil {
		return nil, err
	}
	limit, err := optionalLimit(p.Limit, 100)
	if err != nil {
		return nil, err
	}
	threads, err := d.backend.Threads(ctx, limit)
	if err != nil {
		return nil, err
	}
	if threads == nil {
		threads = []Thread{}
	}
	return threads, nil
}

func cmdMarkRead(ctx context.Context, d *Dispatcher, raw json.RawMessage) (any, error) {
	p, err := decode[MarkReadParams](raw)
	if err != nil {
		return nil, err
	}
	if err := requireThreadID(p.ThreadID); err != nil {
		return nil, err
	}
	return Empty{}, d.backend.MarkRead(ctx, p.ThreadID)
}

func cmdSetTyping(ctx context.Context, d *Dispatcher, raw json.RawMessage) (any, error) {
	p, err := decode[TypingParams](raw)
	if err != nil {
		return nil, err
	}
	if err := requireThreadID(p.ThreadID); err != nil {
		return nil, err
	}
	return Empty{}, d.backend.SetTyping(ctx, p.ThreadID, p.Typing)
}

func cmdSearchUsers(ctx context.Context, d *Dispatcher, raw json.RawMessage) (any, error) {
	p, err := decode[SearchUsersParams](raw)
	if err != nil {
		return nil, err
	}
	if err := required(map[string]string{"query": p.Query}); err != nil {
		return nil, err
	}
	limit := p.Limit
	switch {
	case limit < 0:
		return nil, newError(CodeBadRequest, "limit must be positive")
	case limit == 0:
		limit = defaultUserSearchLimit
	}
	users, err := d.backend.SearchUsers(ctx, strings.TrimSpace(p.Query), min(limit, maxUserSearchLimit))
	if users == nil && err == nil {
		users = []UserResult{}
	}
	return users, err
}

func cmdOpenThread(ctx context.Context, d *Dispatcher, raw json.RawMessage) (any, error) {
	p, err := decode[UserIDParams](raw)
	if err != nil {
		return nil, err
	}
	if err := required(map[string]string{"user_id": p.UserID}); err != nil {
		return nil, err
	}
	thread, err := d.backend.OpenThread(ctx, p.UserID)
	if err != nil {
		return nil, err
	}
	return thread.normalized(), nil
}

func cmdCreateGroup(ctx context.Context, d *Dispatcher, raw json.RawMessage) (any, error) {
	p, err := decode[CreateGroupParams](raw)
	if err != nil {
		return nil, err
	}
	users, err := cleanIDs("user_ids", p.UserIDs, 2, 250)
	if err != nil {
		return nil, err
	}
	if len(p.Name) > maxThreadNameLength {
		return nil, newError(CodeBadRequest, "name is too long")
	}
	thread, err := d.backend.CreateGroup(ctx, users, strings.TrimSpace(p.Name))
	if err != nil {
		return nil, err
	}
	return thread.normalized(), nil
}

func threadCommand(run func(b Backend, ctx context.Context, threadID string) error) commandHandler {
	return func(ctx context.Context, d *Dispatcher, raw json.RawMessage) (any, error) {
		p, err := decode[ThreadIDParams](raw)
		if err != nil {
			return nil, err
		}
		if err := required(map[string]string{"thread_id": p.ThreadID}); err != nil {
			return nil, err
		}
		return Empty{}, run(d.backend, ctx, p.ThreadID)
	}
}

var (
	cmdAcceptRequest = threadCommand(Backend.AcceptRequest)
	cmdDeleteThread  = threadCommand(Backend.DeleteThread)
	cmdLeaveThread   = threadCommand(Backend.LeaveThread)
)

func cmdMute(ctx context.Context, d *Dispatcher, raw json.RawMessage) (any, error) {
	p, err := decode[MuteParams](raw)
	if err != nil {
		return nil, err
	}
	if err := required(map[string]string{"thread_id": p.ThreadID}); err != nil {
		return nil, err
	}
	if err := validMuteUntil(p.UntilMS, time.Now()); err != nil {
		return nil, err
	}
	return Empty{}, d.backend.Mute(ctx, p.ThreadID, *p.UntilMS)
}

func cmdRenameThread(ctx context.Context, d *Dispatcher, raw json.RawMessage) (any, error) {
	p, err := decode[RenameThreadParams](raw)
	if err != nil {
		return nil, err
	}
	if err := required(map[string]string{"thread_id": p.ThreadID}); err != nil {
		return nil, err
	}
	if p.Name == nil {
		return nil, newError(CodeBadRequest, "name is required")
	}
	name := strings.TrimSpace(*p.Name)
	if len(name) > maxThreadNameLength {
		return nil, newError(CodeBadRequest, "name is too long")
	}
	return Empty{}, d.backend.RenameThread(ctx, p.ThreadID, name)
}

func cmdSetThreadImage(ctx context.Context, d *Dispatcher, raw json.RawMessage) (any, error) {
	p, err := decode[ThreadImageParams](raw)
	if err != nil {
		return nil, err
	}
	if err := required(map[string]string{"thread_id": p.ThreadID, "path": p.Path}); err != nil {
		return nil, err
	}
	return Empty{}, d.backend.SetThreadImage(ctx, p.ThreadID, p.Path)
}

func cmdAddParticipants(ctx context.Context, d *Dispatcher, raw json.RawMessage) (any, error) {
	p, err := decode[ParticipantsParams](raw)
	if err != nil {
		return nil, err
	}
	if err := required(map[string]string{"thread_id": p.ThreadID}); err != nil {
		return nil, err
	}
	users, err := cleanIDs("user_ids", p.UserIDs, 1, 250)
	if err != nil {
		return nil, err
	}
	return Empty{}, d.backend.AddParticipants(ctx, p.ThreadID, users)
}

func cmdRemoveParticipant(ctx context.Context, d *Dispatcher, raw json.RawMessage) (any, error) {
	p, err := decode[ParticipantParams](raw)
	if err != nil {
		return nil, err
	}
	if err := required(map[string]string{"thread_id": p.ThreadID, "user_id": p.UserID}); err != nil {
		return nil, err
	}
	return Empty{}, d.backend.RemoveParticipant(ctx, p.ThreadID, p.UserID)
}

func cmdSetAdmin(ctx context.Context, d *Dispatcher, raw json.RawMessage) (any, error) {
	p, err := decode[SetAdminParams](raw)
	if err != nil {
		return nil, err
	}
	if err := required(map[string]string{"thread_id": p.ThreadID, "user_id": p.UserID}); err != nil {
		return nil, err
	}
	if p.Admin == nil {
		return nil, newError(CodeBadRequest, "admin is required")
	}
	return Empty{}, d.backend.SetAdmin(ctx, p.ThreadID, p.UserID, *p.Admin)
}

func cmdBlockUser(ctx context.Context, d *Dispatcher, raw json.RawMessage) (any, error) {
	p, err := decode[BlockUserParams](raw)
	if err != nil {
		return nil, err
	}
	if err := required(map[string]string{"user_id": p.UserID}); err != nil {
		return nil, err
	}
	if p.Blocked == nil {
		return nil, newError(CodeBadRequest, "blocked is required")
	}
	return Empty{}, d.backend.BlockUser(ctx, p.UserID, *p.Blocked)
}
