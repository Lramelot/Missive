package main

import (
	"bytes"
	"context"
	"encoding/json"
	"slices"
	"strings"
	"time"
)

type commandHandler func(ctx context.Context, d *Dispatcher, raw json.RawMessage) (any, error)

var commands = map[string]commandHandler{
	"status":          cmdStatus,
	"login":           cmdLogin,
	"logout":          cmdLogout,
	"connect":         cmdConnect,
	"set_active":      cmdSetActive,
	"history_status":  cmdHistoryStatus,
	"restore_history": cmdRestoreHistory,
	"diagnostics":     cmdDiagnostics,

	"threads":    cmdThreads,
	"messages":   cmdMessages,
	"mark_read":  cmdMarkRead,
	"set_typing": cmdSetTyping,

	"send":          cmdSend,
	"send_media":    cmdSendMedia,
	"send_voice":    cmdSendVoice,
	"react":         cmdReact,
	"edit_message":  cmdEditMessage,
	"unsend":        cmdUnsend,
	"delete_for_me": cmdDeleteForMe,
	"forward":       cmdForward,
	"create_poll":   cmdCreatePoll,
	"vote_poll":     cmdVotePoll,

	"media":         cmdMedia,
	"avatar":        cmdAvatar,
	"media_history": cmdMediaHistory,

	"search_users":       cmdSearchUsers,
	"open_thread":        cmdOpenThread,
	"create_group":       cmdCreateGroup,
	"accept_request":     cmdAcceptRequest,
	"delete_thread":      cmdDeleteThread,
	"leave_thread":       cmdLeaveThread,
	"mute":               cmdMute,
	"rename_thread":      cmdRenameThread,
	"set_thread_image":   cmdSetThreadImage,
	"add_participants":   cmdAddParticipants,
	"remove_participant": cmdRemoveParticipant,
	"set_admin":          cmdSetAdmin,
	"block_user":         cmdBlockUser,

	"search":         cmdSearch,
	"export_thread":  cmdExportThread,
	"save_media":     cmdSaveMedia,
	"cancel_archive": cmdCancelArchive,
}

func (d *Dispatcher) dispatch(ctx context.Context, req *Request) (any, error) {
	ctx = sendReadContext(ctx, req)
	if req.Cmd == "" {
		return nil, newError(CodeBadRequest, "cmd is required")
	}
	handler, ok := commands[req.Cmd]
	if !ok {
		return nil, newError(CodeBadRequest, "unknown command %q", req.Cmd)
	}
	return handler(ctx, d, req.Params)
}

func decodeParams(raw json.RawMessage, into any) error {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) {
		return nil
	}
	if trimmed[0] != '{' {
		return newError(CodeBadRequest, "params must be an object")
	}
	if err := json.Unmarshal(trimmed, into); err != nil {
		return newError(CodeBadRequest, "invalid params: %v", err)
	}
	return nil
}

func decode[P any](raw json.RawMessage) (P, error) {
	var p P
	err := decodeParams(raw, &p)
	return p, err
}

func noParams(raw json.RawMessage) error {
	return decodeParams(raw, &struct{}{})
}

func capLimit(limit int) int {
	return min(limit, maxListLimit)
}

func optionalLimit(limit *int, fallback int) (int, error) {
	if limit == nil {
		return fallback, nil
	}
	if *limit <= 0 {
		return 0, newError(CodeBadRequest, "limit must be positive")
	}
	return capLimit(*limit), nil
}

func optionalBefore(beforeMS *int64) (int64, error) {
	if beforeMS == nil {
		return 0, nil
	}
	if *beforeMS <= 0 {
		return 0, newError(CodeBadRequest, "before_ms must be positive")
	}
	return *beforeMS, nil
}

func requireThreadID(threadID string) error {
	if strings.TrimSpace(threadID) == "" {
		return newError(CodeBadRequest, "thread_id is required")
	}
	return nil
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

func validPIN(pin string) bool {
	if len(pin) != 6 {
		return false
	}
	for _, c := range pin {
		if c < '0' || c > '9' {
			return false
		}
	}
	return true
}
