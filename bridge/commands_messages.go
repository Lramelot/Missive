package main

import (
	"context"
	"encoding/json"
	"strings"
)

const (
	minPollOptions = 2
	maxPollOptions = 12
)

func cmdMessages(ctx context.Context, d *Dispatcher, raw json.RawMessage) (any, error) {
	p, err := decode[MessagesParams](raw)
	if err != nil {
		return nil, err
	}
	if err := requireThreadID(p.ThreadID); err != nil {
		return nil, err
	}
	limit, err := optionalLimit(p.Limit, 50)
	if err != nil {
		return nil, err
	}
	before, err := optionalBefore(p.BeforeMS)
	if err != nil {
		return nil, err
	}
	fetchMissing := p.FetchMissing == nil || *p.FetchMissing
	msgs, err := d.backend.Messages(ctx, p.ThreadID, before, limit, fetchMissing)
	if err != nil {
		return nil, err
	}
	out := make([]Message, len(msgs))
	for i, msg := range msgs {
		out[i] = msg.normalized()
	}
	return out, nil
}

func cmdSend(ctx context.Context, d *Dispatcher, raw json.RawMessage) (any, error) {
	p, err := decode[SendParams](raw)
	if err != nil {
		return nil, err
	}
	if err := requireThreadID(p.ThreadID); err != nil {
		return nil, err
	}
	if p.Text == "" {
		return nil, newError(CodeBadRequest, "text is required")
	}
	id, err := d.backend.Send(ctx, p.ThreadID, p.Text, p.ReplyToID, p.ClientID, p.Mentions)
	if err != nil {
		return nil, err
	}
	return SendResult{MessageID: id}, nil
}

func cmdSendMedia(ctx context.Context, d *Dispatcher, raw json.RawMessage) (any, error) {
	p, err := decode[SendMediaParams](raw)
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(p.ThreadID) == "" || len(p.Paths) == 0 {
		return nil, newError(CodeBadRequest, "thread_id and paths are required")
	}
	files, err := loadSendFiles(p.Paths)
	if err != nil {
		return nil, err
	}
	if len(p.ClientIDs) > 0 && len(p.ClientIDs) != len(p.Paths) {
		return nil, newError(CodeBadRequest, "client_ids must have one entry per path")
	}
	ids, err := d.backend.SendMedia(ctx, p.ThreadID, files, p.Caption, p.ReplyToID, p.ClientIDs)
	if err != nil {
		return nil, err
	}
	if ids == nil {
		ids = []string{}
	}
	return SendMediaResult{MessageIDs: ids}, nil
}

func cmdSendVoice(ctx context.Context, d *Dispatcher, raw json.RawMessage) (any, error) {
	p, err := decode[SendVoiceParams](raw)
	if err != nil {
		return nil, err
	}
	if err := required(map[string]string{"thread_id": p.ThreadID, "path": p.Path}); err != nil {
		return nil, err
	}
	if p.DurationMS < 0 {
		return nil, newError(CodeBadRequest, "duration_ms must not be negative")
	}
	id, err := d.backend.SendVoice(ctx, p.ThreadID, p.Path, p.DurationMS, p.ClientID, p.ReplyToID)
	if err != nil {
		return nil, err
	}
	return SendResult{MessageID: id}, nil
}

func cmdReact(ctx context.Context, d *Dispatcher, raw json.RawMessage) (any, error) {
	p, err := decode[ReactParams](raw)
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(p.ThreadID) == "" || strings.TrimSpace(p.MessageID) == "" || p.Emoji == nil {
		return nil, newError(CodeBadRequest, "thread_id, message_id and emoji are required")
	}
	return Empty{}, d.backend.React(ctx, p.ThreadID, p.MessageID, *p.Emoji)
}

func cmdEditMessage(ctx context.Context, d *Dispatcher, raw json.RawMessage) (any, error) {
	p, err := decode[EditMessageParams](raw)
	if err != nil {
		return nil, err
	}
	if err := required(map[string]string{"thread_id": p.ThreadID, "message_id": p.MessageID, "text": p.Text}); err != nil {
		return nil, err
	}
	return Empty{}, d.backend.EditMessage(ctx, p.ThreadID, p.MessageID, p.Text)
}

func messageCommand(run func(b Backend, ctx context.Context, threadID, messageID string) error) commandHandler {
	return func(ctx context.Context, d *Dispatcher, raw json.RawMessage) (any, error) {
		p, err := decode[MessageRefParams](raw)
		if err != nil {
			return nil, err
		}
		if err := required(map[string]string{"thread_id": p.ThreadID, "message_id": p.MessageID}); err != nil {
			return nil, err
		}
		return Empty{}, run(d.backend, ctx, p.ThreadID, p.MessageID)
	}
}

var (
	cmdUnsend      = messageCommand(Backend.Unsend)
	cmdDeleteForMe = messageCommand(Backend.DeleteForMe)
)

func cmdForward(ctx context.Context, d *Dispatcher, raw json.RawMessage) (any, error) {
	p, err := decode[ForwardParams](raw)
	if err != nil {
		return nil, err
	}
	if err := required(map[string]string{"from_thread_id": p.FromThreadID, "message_id": p.MessageID}); err != nil {
		return nil, err
	}
	targets, err := cleanIDs("to_thread_ids", p.ToThreadIDs, 1, maxForwardTargets)
	if err != nil {
		return nil, err
	}
	ids, err := d.backend.Forward(ctx, p.FromThreadID, p.MessageID, targets)
	if err != nil {
		return nil, err
	}
	return ForwardResult{MessageIDs: ids}, nil
}

func cmdCreatePoll(ctx context.Context, d *Dispatcher, raw json.RawMessage) (any, error) {
	p, err := decode[CreatePollParams](raw)
	if err != nil {
		return nil, err
	}
	if err := required(map[string]string{"thread_id": p.ThreadID, "question": p.Question}); err != nil {
		return nil, err
	}
	options, err := cleanPollOptions(p.Options)
	if err != nil {
		return nil, err
	}
	return Empty{}, d.backend.CreatePoll(ctx, p.ThreadID, strings.TrimSpace(p.Question), options)
}

func cmdVotePoll(ctx context.Context, d *Dispatcher, raw json.RawMessage) (any, error) {
	p, err := decode[VotePollParams](raw)
	if err != nil {
		return nil, err
	}
	if err := required(map[string]string{"thread_id": p.ThreadID, "poll_id": p.PollID}); err != nil {
		return nil, err
	}
	options, err := cleanIDs("option_ids", p.OptionIDs, 0, maxPollOptions*4)
	if err != nil {
		return nil, err
	}
	return Empty{}, d.backend.VotePoll(ctx, p.ThreadID, p.PollID, options)
}
