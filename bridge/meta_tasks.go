package main

import (
	"context"
	"errors"
	"time"

	"go.mau.fi/mautrix-meta/pkg/messagix/socket"
	"go.mau.fi/mautrix-meta/pkg/messagix/table"
)

var (
	editConfirmWait  = 5 * time.Second
	groupConfirmWait = 5 * time.Second
)

const (
	editConfirmPoll   = 100 * time.Millisecond
	editWindow        = 15 * time.Minute
	maxForwardTargets = 10
	maxThreadImage    = 10 << 20
)

var errTaskRejected = errors.New("Meta rejected the request")

func lsTaskFailure(resp *table.LSTable) error {
	if resp == nil {
		return nil
	}
	switch {
	case len(resp.LSHandleFailedTask) > 0:
		return newError(CodeInternal, "%v: %s", errTaskRejected, resp.LSHandleFailedTask[0].Message)
	case len(resp.LSMarkOptimisticMessageFailed) > 0:
		return newError(CodeInternal, "%v: %s", errTaskRejected, resp.LSMarkOptimisticMessageFailed[0].Message)
	case len(resp.LSIssueNewError) > 0:
		return newError(CodeInternal, "%v: %s", errTaskRejected, firstNonEmpty(resp.LSIssueNewError[0].ErrorMessage, resp.LSIssueNewError[0].ErrorTitle))
	}
	for _, completion := range resp.LSMailboxTaskCompletionApiOnTaskCompletion {
		if !completion.Success {
			return newError(CodeInternal, "%v", errTaskRejected)
		}
	}
	return nil
}

func (b *MetaBackend) runTasks(ctx context.Context, tasks ...socket.Task) (*table.LSTable, error) {
	var resp *table.LSTable
	var err error
	if b.taskFn != nil {
		resp, err = b.taskFn(ctx, tasks...)
	} else {
		client, cerr := b.connectedClient(ctx)
		if cerr != nil {
			return nil, cerr
		}
		resp, err = client.ExecuteTasks(ctx, tasks...)
	}
	if err != nil {
		var be *BridgeError
		if errors.As(err, &be) {
			return nil, be
		}
		return nil, newError(CodeInternal, "Meta request failed: %v", err)
	}
	if ferr := lsTaskFailure(resp); ferr != nil {
		return resp, ferr
	}
	if resp != nil {
		b.handleTable(resp, modeLive)
	}
	return resp, nil
}

func (b *MetaBackend) emitThread(id string) {
	if view, ok := b.store.ThreadView(id); ok {
		b.out.Emit(EventThreads, ThreadsEvent{Threads: []Thread{view}})
	}
}

func (b *MetaBackend) loggedThread(threadID string) (threadRec, error) {
	rec, _, err := b.threadAndReply(threadID, "")
	return rec, err
}

func (b *MetaBackend) lsTarget(rec threadRec) (int64, int64, error) {
	key, err := parseThreadKey(rec.ID)
	if err != nil {
		return 0, 0, err
	}
	if rec.E2EE {
		if fbKey, ok := b.store.FBKeyFor(key); ok {
			return fbKey, syncGroupE2EE, nil
		}
	}
	return key, syncGroupInbox, nil
}
