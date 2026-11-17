package main

import (
	"context"
	"slices"
	"strings"
	"time"
)

const (
	defaultUserSearchLimit = 20
	maxUserSearchLimit     = 100
	minPollOptions         = 2
	maxPollOptions         = 12
	maxThreadNameLength    = 250
)

type BackendV11 interface {
	EditMessage(ctx context.Context, threadID, messageID, text string) error
	Unsend(ctx context.Context, threadID, messageID string) error
	DeleteForMe(ctx context.Context, threadID, messageID string) error
	Forward(ctx context.Context, fromThreadID, messageID string, toThreadIDs []string) ([]string, error)
	SendVoice(ctx context.Context, threadID, path string, durationMS int64, clientID, replyToID string) (string, error)
	CreatePoll(ctx context.Context, threadID, question string, options []string) error
	VotePoll(ctx context.Context, threadID, pollID string, optionIDs []string) error
	SearchUsers(ctx context.Context, query string, limit int) ([]UserResult, error)
	OpenThread(ctx context.Context, userID string) (Thread, error)
	CreateGroup(ctx context.Context, userIDs []string, name string) (Thread, error)
	AcceptRequest(ctx context.Context, threadID string) error
	DeleteThread(ctx context.Context, threadID string) error
	Mute(ctx context.Context, threadID string, untilMS int64) error
	RenameThread(ctx context.Context, threadID, name string) error
	SetThreadImage(ctx context.Context, threadID, path string) error
	AddParticipants(ctx context.Context, threadID string, userIDs []string) error
	RemoveParticipant(ctx context.Context, threadID, userID string) error
	LeaveThread(ctx context.Context, threadID string) error
	SetAdmin(ctx context.Context, threadID, userID string, admin bool) error
	BlockUser(ctx context.Context, userID string, blocked bool) error
}

func required(values map[string]string) error {
	keys := make([]string, 0, len(values))
	for k := range values {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	for _, k := range keys {
		if strings.TrimSpace(values[k]) == "" {
			return newError(CodeBadRequest, "%s is required", k)
		}
	}
	return nil
}

func cleanIDs(field string, ids []string, minCount, maxCount int) ([]string, error) {
	out := make([]string, 0, len(ids))
	for _, id := range ids {
		id = strings.TrimSpace(id)
		if id == "" {
			return nil, newError(CodeBadRequest, "%s must not contain empty ids", field)
		}
		if !slices.Contains(out, id) {
			out = append(out, id)
		}
	}
	if len(out) < minCount {
		return nil, newError(CodeBadRequest, "%s needs at least %d distinct ids", field, minCount)
	}
	if maxCount > 0 && len(out) > maxCount {
		return nil, newError(CodeBadRequest, "%s accepts at most %d ids", field, maxCount)
	}
	return out, nil
}

func cleanPollOptions(options []string) ([]string, error) {
	out := make([]string, 0, len(options))
	for _, o := range options {
		o = strings.TrimSpace(o)
		if o == "" {
			return nil, newError(CodeBadRequest, "poll options must not be empty")
		}
		out = append(out, o)
	}
	if len(out) < minPollOptions || len(out) > maxPollOptions {
		return nil, newError(CodeBadRequest, "a poll needs between %d and %d options", minPollOptions, maxPollOptions)
	}
	return out, nil
}

func validMuteUntil(untilMS *int64, now time.Time) error {
	switch {
	case untilMS == nil:
		return newError(CodeBadRequest, "until_ms is required")
	case *untilMS == 0 || *untilMS == -1:
		return nil
	case *untilMS < 0 || *untilMS <= now.UnixMilli():
		return newError(CodeBadRequest, "until_ms must be 0, -1 or a future timestamp")
	}
	return nil
}

func (d *Dispatcher) dispatchV11(ctx context.Context, req *Request) (any, bool, error) {
	b, ok := d.backend.(BackendV11)
	if !ok {
		return nil, false, nil
	}
	result, err := d.runV11(ctx, b, req)
	if err == errUnknownV11 {
		return nil, false, nil
	}
	return result, true, err
}

var errUnknownV11 = newError(CodeBadRequest, "unknown command")

func (d *Dispatcher) runV11(ctx context.Context, b BackendV11, req *Request) (any, error) {
	switch req.Cmd {
	case "edit_message":
		var p EditMessageParams
		if err := decodeParams(req.Params, &p); err != nil {
			return nil, err
		}
		if err := required(map[string]string{"thread_id": p.ThreadID, "message_id": p.MessageID, "text": p.Text}); err != nil {
			return nil, err
		}
		return Empty{}, b.EditMessage(ctx, p.ThreadID, p.MessageID, p.Text)
	case "unsend", "delete_for_me":
		var p MessageRefParams
		if err := decodeParams(req.Params, &p); err != nil {
			return nil, err
		}
		if err := required(map[string]string{"thread_id": p.ThreadID, "message_id": p.MessageID}); err != nil {
			return nil, err
		}
		if req.Cmd == "unsend" {
			return Empty{}, b.Unsend(ctx, p.ThreadID, p.MessageID)
		}
		return Empty{}, b.DeleteForMe(ctx, p.ThreadID, p.MessageID)
	case "forward":
		var p ForwardParams
		if err := decodeParams(req.Params, &p); err != nil {
			return nil, err
		}
		if err := required(map[string]string{"from_thread_id": p.FromThreadID, "message_id": p.MessageID}); err != nil {
			return nil, err
		}
		targets, err := cleanIDs("to_thread_ids", p.ToThreadIDs, 1, maxForwardTargets)
		if err != nil {
			return nil, err
		}
		ids, err := b.Forward(ctx, p.FromThreadID, p.MessageID, targets)
		if err != nil {
			return nil, err
		}
		return ForwardResult{MessageIDs: ids}, nil
	case "send_voice":
		var p SendVoiceParams
		if err := decodeParams(req.Params, &p); err != nil {
			return nil, err
		}
		if err := required(map[string]string{"thread_id": p.ThreadID, "path": p.Path}); err != nil {
			return nil, err
		}
		if p.DurationMS < 0 {
			return nil, newError(CodeBadRequest, "duration_ms must not be negative")
		}
		id, err := b.SendVoice(ctx, p.ThreadID, p.Path, p.DurationMS, p.ClientID, p.ReplyToID)
		if err != nil {
			return nil, err
		}
		return SendResult{MessageID: id}, nil
	case "create_poll":
		var p CreatePollParams
		if err := decodeParams(req.Params, &p); err != nil {
			return nil, err
		}
		if err := required(map[string]string{"thread_id": p.ThreadID, "question": p.Question}); err != nil {
			return nil, err
		}
		options, err := cleanPollOptions(p.Options)
		if err != nil {
			return nil, err
		}
		return Empty{}, b.CreatePoll(ctx, p.ThreadID, strings.TrimSpace(p.Question), options)
	case "vote_poll":
		var p VotePollParams
		if err := decodeParams(req.Params, &p); err != nil {
			return nil, err
		}
		if err := required(map[string]string{"thread_id": p.ThreadID, "poll_id": p.PollID}); err != nil {
			return nil, err
		}
		options, err := cleanIDs("option_ids", p.OptionIDs, 0, maxPollOptions*4)
		if err != nil {
			return nil, err
		}
		return Empty{}, b.VotePoll(ctx, p.ThreadID, p.PollID, options)
	case "search_users":
		var p SearchUsersParams
		if err := decodeParams(req.Params, &p); err != nil {
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
		users, err := b.SearchUsers(ctx, strings.TrimSpace(p.Query), min(limit, maxUserSearchLimit))
		if users == nil && err == nil {
			users = []UserResult{}
		}
		return users, err
	case "open_thread":
		var p UserIDParams
		if err := decodeParams(req.Params, &p); err != nil {
			return nil, err
		}
		if err := required(map[string]string{"user_id": p.UserID}); err != nil {
			return nil, err
		}
		thread, err := b.OpenThread(ctx, p.UserID)
		if err != nil {
			return nil, err
		}
		return thread.normalized(), nil
	case "create_group":
		var p CreateGroupParams
		if err := decodeParams(req.Params, &p); err != nil {
			return nil, err
		}
		users, err := cleanIDs("user_ids", p.UserIDs, 2, 250)
		if err != nil {
			return nil, err
		}
		if len(p.Name) > maxThreadNameLength {
			return nil, newError(CodeBadRequest, "name is too long")
		}
		thread, err := b.CreateGroup(ctx, users, strings.TrimSpace(p.Name))
		if err != nil {
			return nil, err
		}
		return thread.normalized(), nil
	case "accept_request", "delete_thread", "leave_thread":
		var p ThreadIDParams
		if err := decodeParams(req.Params, &p); err != nil {
			return nil, err
		}
		if err := required(map[string]string{"thread_id": p.ThreadID}); err != nil {
			return nil, err
		}
		switch req.Cmd {
		case "accept_request":
			return Empty{}, b.AcceptRequest(ctx, p.ThreadID)
		case "delete_thread":
			return Empty{}, b.DeleteThread(ctx, p.ThreadID)
		}
		return Empty{}, b.LeaveThread(ctx, p.ThreadID)
	case "mute":
		var p MuteParams
		if err := decodeParams(req.Params, &p); err != nil {
			return nil, err
		}
		if err := required(map[string]string{"thread_id": p.ThreadID}); err != nil {
			return nil, err
		}
		if err := validMuteUntil(p.UntilMS, time.Now()); err != nil {
			return nil, err
		}
		return Empty{}, b.Mute(ctx, p.ThreadID, *p.UntilMS)
	case "rename_thread":
		var p RenameThreadParams
		if err := decodeParams(req.Params, &p); err != nil {
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
		return Empty{}, b.RenameThread(ctx, p.ThreadID, name)
	case "set_thread_image":
		var p ThreadImageParams
		if err := decodeParams(req.Params, &p); err != nil {
			return nil, err
		}
		if err := required(map[string]string{"thread_id": p.ThreadID, "path": p.Path}); err != nil {
			return nil, err
		}
		return Empty{}, b.SetThreadImage(ctx, p.ThreadID, p.Path)
	case "add_participants":
		var p ParticipantsParams
		if err := decodeParams(req.Params, &p); err != nil {
			return nil, err
		}
		if err := required(map[string]string{"thread_id": p.ThreadID}); err != nil {
			return nil, err
		}
		users, err := cleanIDs("user_ids", p.UserIDs, 1, 250)
		if err != nil {
			return nil, err
		}
		return Empty{}, b.AddParticipants(ctx, p.ThreadID, users)
	case "remove_participant":
		var p ParticipantParams
		if err := decodeParams(req.Params, &p); err != nil {
			return nil, err
		}
		if err := required(map[string]string{"thread_id": p.ThreadID, "user_id": p.UserID}); err != nil {
			return nil, err
		}
		return Empty{}, b.RemoveParticipant(ctx, p.ThreadID, p.UserID)
	case "set_admin":
		var p SetAdminParams
		if err := decodeParams(req.Params, &p); err != nil {
			return nil, err
		}
		if err := required(map[string]string{"thread_id": p.ThreadID, "user_id": p.UserID}); err != nil {
			return nil, err
		}
		if p.Admin == nil {
			return nil, newError(CodeBadRequest, "admin is required")
		}
		return Empty{}, b.SetAdmin(ctx, p.ThreadID, p.UserID, *p.Admin)
	case "block_user":
		var p BlockUserParams
		if err := decodeParams(req.Params, &p); err != nil {
			return nil, err
		}
		if err := required(map[string]string{"user_id": p.UserID}); err != nil {
			return nil, err
		}
		if p.Blocked == nil {
			return nil, newError(CodeBadRequest, "blocked is required")
		}
		return Empty{}, b.BlockUser(ctx, p.UserID, *p.Blocked)
	}
	return nil, errUnknownV11
}
