package main

import "context"

type Backend interface {
	Status(ctx context.Context) (StatusResult, error)
	Login(ctx context.Context, cookies map[string]string) (User, error)
	Logout(ctx context.Context) error
	Connect(ctx context.Context) error
	SetActive(ctx context.Context, active bool) error
	HistoryStatus(ctx context.Context) (HistoryStatusResult, error)
	RestoreHistory(ctx context.Context, pin string) error
	Close()

	Threads(ctx context.Context, limit int) ([]Thread, error)
	Messages(ctx context.Context, threadID string, beforeMS int64, limit int, fetchMissing bool) ([]Message, error)
	MarkRead(ctx context.Context, threadID string) error
	SetTyping(ctx context.Context, threadID string, typing bool) error

	Send(ctx context.Context, threadID, text, replyToID, clientID string, mentions []Mention) (string, error)
	SendMedia(ctx context.Context, threadID string, files []sendFile, caption, replyToID string, clientIDs []string) ([]string, error)
	SendVoice(ctx context.Context, threadID, path string, durationMS int64, clientID, replyToID string) (string, error)
	React(ctx context.Context, threadID, messageID, emoji string) error
	EditMessage(ctx context.Context, threadID, messageID, text string) error
	Unsend(ctx context.Context, threadID, messageID string) error
	DeleteForMe(ctx context.Context, threadID, messageID string) error
	Forward(ctx context.Context, fromThreadID, messageID string, toThreadIDs []string) ([]string, error)
	CreatePoll(ctx context.Context, threadID, question string, options []string) error
	VotePoll(ctx context.Context, threadID, pollID string, optionIDs []string) error

	Media(ctx context.Context, threadID, messageID, attachmentID, variant string) (MediaResult, error)
	Avatar(ctx context.Context, url string) (string, error)
	MediaHistory(ctx context.Context, threadID string, beforeMS int64, limit int, kinds []string, maxPages int) (MediaHistoryResult, error)

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

	SearchFiltered(ctx context.Context, f SearchFilter, limit int) ([]SearchHit, error)
	ExportThread(ctx context.Context, p ExportThreadParams) (ExportThreadResult, error)
	SaveMedia(ctx context.Context, p SaveMediaParams) (SaveMediaResult, error)
	CancelArchive(ctx context.Context) (CancelArchiveResult, error)
}

var (
	_ Backend = (*MetaBackend)(nil)
	_ Backend = (*DemoBackend)(nil)
)
