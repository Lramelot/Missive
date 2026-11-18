package main

import (
	"encoding/json"
	"errors"
	"fmt"
)

const (
	CodeBadRequest   = "bad_request"
	CodeNotLoggedIn  = "not_logged_in"
	CodeNotConnected = "not_connected"
	CodeAuthFailed   = "auth_failed"
	CodeNotFound     = "not_found"
	CodeUnsupported  = "unsupported"
	CodeBadPIN       = "bad_pin"
	CodePINLocked    = "pin_locked"
	CodeInternal     = "internal"
	CodeCancelled    = "cancelled"
	CodeBusy         = "busy"
	CodeEditRejected = "edit_rejected"
)

type Request struct {
	ID     int64           `json:"id"`
	Cmd    string          `json:"cmd"`
	Params json.RawMessage `json:"params,omitempty"`
}

type ErrorBody struct {
	Code    string         `json:"code"`
	Message string         `json:"message"`
	Data    map[string]any `json:"data,omitempty"`
}

type okResponse struct {
	ID     int64 `json:"id"`
	OK     bool  `json:"ok"`
	Result any   `json:"result"`
}

type errResponse struct {
	ID    int64     `json:"id"`
	OK    bool      `json:"ok"`
	Error ErrorBody `json:"error"`
}

type eventEnvelope struct {
	Event string `json:"event"`
	Data  any    `json:"data"`
}

type BridgeError struct {
	Code    string
	Message string
	Data    map[string]any
}

func (e *BridgeError) Error() string {
	return e.Code + ": " + e.Message
}

func newError(code, format string, args ...any) *BridgeError {
	return &BridgeError{Code: code, Message: fmt.Sprintf(format, args...)}
}

func toErrorBody(err error) ErrorBody {
	var be *BridgeError
	if errors.As(err, &be) {
		return ErrorBody{Code: be.Code, Message: redactText(be.Message), Data: be.Data}
	}
	return ErrorBody{Code: CodeInternal, Message: redactText(err.Error())}
}

type User struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	AvatarURL string `json:"avatar_url,omitempty"`
}

type Thread struct {
	ID               string        `json:"id"`
	Name             string        `json:"name"`
	AvatarURL        string        `json:"avatar_url,omitempty"`
	MemberAvatars    []string      `json:"member_avatars"`
	PeerID           string        `json:"peer_id,omitempty"`
	Snippet          string        `json:"snippet"`
	LastActivityMS   int64         `json:"last_activity_ms"`
	UnreadCount      int           `json:"unread_count"`
	IsGroup          bool          `json:"is_group"`
	E2EE             bool          `json:"e2ee"`
	Muted            bool          `json:"muted"`
	ReadReceipts     []ReadReceipt `json:"read_receipts"`
	Participants     []Participant `json:"participants"`
	PeerOnline       *bool         `json:"peer_online,omitempty"`
	PeerLastActiveMS int64         `json:"peer_last_active_ms,omitempty"`
	Folder           string        `json:"folder"`
	MutedUntilMS     *int64        `json:"muted_until_ms,omitempty"`
	Theme            *ThreadTheme  `json:"theme,omitempty"`
	QuickEmoji       string        `json:"quick_emoji,omitempty"`
	EphemeralTTLMS   int64         `json:"ephemeral_ttl_ms,omitempty"`
	PinnedMessageIDs []string      `json:"pinned_message_ids"`
	CanManage        bool          `json:"can_manage"`
	IsAdmin          bool          `json:"is_admin"`
	Blocked          bool          `json:"blocked"`
}

type ThreadTheme struct {
	Accent   string   `json:"accent"`
	Gradient []string `json:"gradient"`
}

type Participant struct {
	UserID       string `json:"user_id"`
	Name         string `json:"name"`
	AvatarURL    string `json:"avatar_url,omitempty"`
	Online       *bool  `json:"online,omitempty"`
	LastActiveMS int64  `json:"last_active_ms,omitempty"`
	IsAdmin      bool   `json:"is_admin"`
}

type UserResult struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	AvatarURL string `json:"avatar_url,omitempty"`
	ThreadID  string `json:"thread_id,omitempty"`
	IsFriend  bool   `json:"is_friend"`
}

type Poll struct {
	ID         string       `json:"id"`
	Question   string       `json:"question"`
	Options    []PollOption `json:"options"`
	TotalVotes int          `json:"total_votes"`
}

type PollOption struct {
	ID        string    `json:"id"`
	Text      string    `json:"text"`
	VoteCount int       `json:"vote_count"`
	Voters    []Reactor `json:"voters"`
	Mine      bool      `json:"mine"`
}

type Mention struct {
	Offset int    `json:"offset"`
	Length int    `json:"length"`
	UserID string `json:"user_id"`
}

type ReadReceipt struct {
	UserID      string `json:"user_id"`
	Name        string `json:"name"`
	AvatarURL   string `json:"avatar_url,omitempty"`
	ReadMS      int64  `json:"read_ms"`
	DeliveredMS int64  `json:"delivered_ms,omitempty"`
}

type ReplyPreview struct {
	ID         string `json:"id"`
	SenderName string `json:"sender_name"`
	Text       string `json:"text"`
	Kind       string `json:"kind,omitempty"`
}

type MediaItem struct {
	MessageID   string     `json:"message_id"`
	ThreadID    string     `json:"thread_id"`
	TimestampMS int64      `json:"timestamp_ms"`
	SenderName  string     `json:"sender_name"`
	IsOutgoing  bool       `json:"is_outgoing"`
	Attachment  Attachment `json:"attachment"`
}

type SearchHit struct {
	ThreadID string  `json:"thread_id"`
	Message  Message `json:"message"`
}

type MediaHistoryResult struct {
	Items   []MediaItem `json:"items"`
	HasMore bool        `json:"has_more"`
}

type SendMediaResult struct {
	MessageIDs []string `json:"message_ids"`
}

type SendMediaParams struct {
	ThreadID  string   `json:"thread_id"`
	Paths     []string `json:"paths"`
	Caption   string   `json:"caption"`
	ReplyToID string   `json:"reply_to_id"`
	ClientIDs []string `json:"client_ids"`
}

type ReactParams struct {
	ThreadID  string  `json:"thread_id"`
	MessageID string  `json:"message_id"`
	Emoji     *string `json:"emoji"`
}

type TypingParams struct {
	ThreadID string `json:"thread_id"`
	Typing   bool   `json:"typing"`
}

type SearchParams struct {
	Query    string   `json:"query"`
	ThreadID string   `json:"thread_id"`
	Limit    *int     `json:"limit"`
	SenderID string   `json:"sender_id"`
	Kinds    []string `json:"kinds"`
	BeforeMS *int64   `json:"before_ms"`
	AfterMS  *int64   `json:"after_ms"`
}

type MediaHistoryParams struct {
	ThreadID string   `json:"thread_id"`
	BeforeMS *int64   `json:"before_ms"`
	Limit    *int     `json:"limit"`
	Kinds    []string `json:"kinds"`
	MaxPages *int     `json:"max_pages"`
}

type Attachment struct {
	ID           string    `json:"id"`
	Kind         string    `json:"kind"`
	URL          string    `json:"url,omitempty"`
	ThumbnailURL string    `json:"thumbnail_url,omitempty"`
	Title        string    `json:"title,omitempty"`
	Description  string    `json:"description,omitempty"`
	Source       string    `json:"source,omitempty"`
	DurationMS   int64     `json:"duration_ms,omitempty"`
	Size         int64     `json:"size,omitempty"`
	FileName     string    `json:"file_name,omitempty"`
	MimeType     string    `json:"mime_type,omitempty"`
	Width        int       `json:"width,omitempty"`
	Height       int       `json:"height,omitempty"`
	Voice        bool      `json:"voice"`
	Gif          bool      `json:"gif,omitempty"`
	Waveform     []float64 `json:"waveform"`
	Latitude     *float64  `json:"latitude,omitempty"`
	Longitude    *float64  `json:"longitude,omitempty"`
}

type Message struct {
	ID          string        `json:"id"`
	ThreadID    string        `json:"thread_id"`
	SenderID    string        `json:"sender_id"`
	SenderName  string        `json:"sender_name"`
	Reactions   []Reaction    `json:"reactions"`
	Text        string        `json:"text"`
	TimestampMS int64         `json:"timestamp_ms"`
	IsOutgoing  bool          `json:"is_outgoing"`
	Attachments []Attachment  `json:"attachments"`
	ReplyToID   string        `json:"reply_to_id,omitempty"`
	ReplyTo     *ReplyPreview `json:"reply_to,omitempty"`
	ClientID    string        `json:"client_id,omitempty"`
	Mentions    []Mention     `json:"mentions"`
	Edited      bool          `json:"edited"`
	Deleted     bool          `json:"deleted"`
	System      bool          `json:"system"`
	Forwarded   bool          `json:"forwarded"`
	Poll        *Poll         `json:"poll,omitempty"`
}

type Reaction struct {
	Emoji    string    `json:"emoji"`
	Count    int       `json:"count"`
	Mine     bool      `json:"mine"`
	Reactors []Reactor `json:"reactors"`
}

type Reactor struct {
	UserID      string `json:"user_id"`
	Name        string `json:"name"`
	AvatarURL   string `json:"avatar_url,omitempty"`
	TimestampMS int64  `json:"timestamp_ms,omitempty"`
}

func (m Message) normalized() Message {
	if m.Attachments == nil {
		m.Attachments = []Attachment{}
	}
	if m.Reactions == nil {
		m.Reactions = []Reaction{}
	}
	if m.Mentions == nil {
		m.Mentions = []Mention{}
	}
	for i := range m.Attachments {
		if m.Attachments[i].Waveform == nil {
			atts := make([]Attachment, len(m.Attachments))
			copy(atts, m.Attachments)
			for j := range atts {
				if atts[j].Waveform == nil {
					atts[j].Waveform = []float64{}
				}
			}
			m.Attachments = atts
			break
		}
	}
	return m
}

func (t Thread) normalized() Thread {
	if t.MemberAvatars == nil {
		t.MemberAvatars = []string{}
	}
	if t.ReadReceipts == nil {
		t.ReadReceipts = []ReadReceipt{}
	}
	if t.Participants == nil {
		t.Participants = []Participant{}
	}
	if t.PinnedMessageIDs == nil {
		t.PinnedMessageIDs = []string{}
	}
	if t.Folder == "" {
		t.Folder = FolderInbox
	}
	if t.Theme != nil && t.Theme.Gradient == nil {
		theme := *t.Theme
		theme.Gradient = []string{}
		t.Theme = &theme
	}
	return t
}

type StatusResult struct {
	LoggedIn  bool  `json:"logged_in"`
	Connected bool  `json:"connected"`
	User      *User `json:"user,omitempty"`
}

type LoginResult struct {
	User User `json:"user"`
}

type SendResult struct {
	MessageID string `json:"message_id,omitempty"`
}

type Empty struct{}

type LoginParams struct {
	Cookies map[string]string `json:"cookies"`
}

type ThreadsParams struct {
	Limit *int `json:"limit"`
}

type MessagesParams struct {
	ThreadID     string `json:"thread_id"`
	BeforeMS     *int64 `json:"before_ms"`
	Limit        *int   `json:"limit"`
	FetchMissing *bool  `json:"fetch_missing"`
}

type SendParams struct {
	ThreadID  string    `json:"thread_id"`
	Text      string    `json:"text"`
	ReplyToID string    `json:"reply_to_id"`
	ClientID  string    `json:"client_id"`
	Mentions  []Mention `json:"mentions"`
}

type AvatarParams struct {
	URL string `json:"url"`
}

type AvatarResult struct {
	Path string `json:"path"`
}

type MessagesBackfilledEvent struct {
	ThreadID string    `json:"thread_id"`
	Messages []Message `json:"messages"`
}

type MarkReadParams struct {
	ThreadID string `json:"thread_id"`
}

type MediaParams struct {
	ThreadID     string `json:"thread_id"`
	MessageID    string `json:"message_id"`
	AttachmentID string `json:"attachment_id"`
	Variant      string `json:"variant"`
}

const (
	VariantFull  = "full"
	VariantThumb = "thumb"
)

const (
	BackupNone      = "none"
	BackupAvailable = "available"
	BackupUnknown   = "unknown"
)

type HistoryStatusResult struct {
	Backup   string `json:"backup"`
	Restored bool   `json:"restored"`
}

type RestoreHistoryParams struct {
	PIN string `json:"pin"`
}

type MediaResult struct {
	Path     string `json:"path"`
	MimeType string `json:"mime_type,omitempty"`
	Width    int    `json:"width,omitempty"`
	Height   int    `json:"height,omitempty"`
}

const (
	EventConnection    = "connection"
	EventThreads       = "threads"
	EventThreadRemoved = "thread_removed"
	EventMessage       = "message"
	EventMessageUpdate = "message_updated"
	EventTyping        = "typing"
	EventRead          = "read"
	EventReady         = "ready"
	EventPresence      = "presence"
	EventBackfilled    = "messages_backfilled"
)

type PresenceEvent struct {
	UserID     string `json:"user_id"`
	Online     bool   `json:"online"`
	LastSeenMS int64  `json:"last_seen_ms,omitempty"`
}

const (
	StateConnecting   = "connecting"
	StateConnected    = "connected"
	StateDisconnected = "disconnected"
	StateError        = "error"
)

type ConnectionEvent struct {
	State     string `json:"state"`
	Detail    string `json:"detail,omitempty"`
	Permanent bool   `json:"permanent,omitempty"`
}

type ThreadsEvent struct {
	Threads []Thread `json:"threads"`
}

type ThreadRemovedEvent struct {
	ThreadID string `json:"thread_id"`
}

type TypingEvent struct {
	ThreadID string `json:"thread_id"`
	UserID   string `json:"user_id"`
	Typing   bool   `json:"typing"`
}

type ReadEvent struct {
	ThreadID    string `json:"thread_id"`
	UserID      string `json:"user_id"`
	TimestampMS int64  `json:"timestamp_ms"`
}

type ReadyEvent struct {
	User User `json:"user"`
}

const (
	FolderInbox    = "inbox"
	FolderRequests = "requests"
	FolderSpam     = "spam"
	FolderArchived = "archived"

	EventMessageRemoved = "message_removed"
)

type MessageRemovedEvent struct {
	ThreadID  string `json:"thread_id"`
	MessageID string `json:"message_id"`
}

type MessageRefParams struct {
	ThreadID  string `json:"thread_id"`
	MessageID string `json:"message_id"`
}

type EditMessageParams struct {
	ThreadID  string `json:"thread_id"`
	MessageID string `json:"message_id"`
	Text      string `json:"text"`
}

type ForwardParams struct {
	FromThreadID string   `json:"from_thread_id"`
	MessageID    string   `json:"message_id"`
	ToThreadIDs  []string `json:"to_thread_ids"`
}

type ForwardResult struct {
	MessageIDs []string `json:"message_ids"`
}

type SendVoiceParams struct {
	ThreadID   string `json:"thread_id"`
	Path       string `json:"path"`
	DurationMS int64  `json:"duration_ms"`
	ClientID   string `json:"client_id"`
	ReplyToID  string `json:"reply_to_id"`
}

type CreatePollParams struct {
	ThreadID string   `json:"thread_id"`
	Question string   `json:"question"`
	Options  []string `json:"options"`
}

type VotePollParams struct {
	ThreadID  string   `json:"thread_id"`
	PollID    string   `json:"poll_id"`
	OptionIDs []string `json:"option_ids"`
}

type SearchUsersParams struct {
	Query string `json:"query"`
	Limit int    `json:"limit"`
}

type UserIDParams struct {
	UserID string `json:"user_id"`
}

type CreateGroupParams struct {
	UserIDs []string `json:"user_ids"`
	Name    string   `json:"name"`
}

type ThreadIDParams struct {
	ThreadID string `json:"thread_id"`
}

type MuteParams struct {
	ThreadID string `json:"thread_id"`
	UntilMS  *int64 `json:"until_ms"`
}

type RenameThreadParams struct {
	ThreadID string  `json:"thread_id"`
	Name     *string `json:"name"`
}

type ThreadImageParams struct {
	ThreadID string `json:"thread_id"`
	Path     string `json:"path"`
}

type ParticipantsParams struct {
	ThreadID string   `json:"thread_id"`
	UserIDs  []string `json:"user_ids"`
}

type ParticipantParams struct {
	ThreadID string `json:"thread_id"`
	UserID   string `json:"user_id"`
}

type SetAdminParams struct {
	ThreadID string `json:"thread_id"`
	UserID   string `json:"user_id"`
	Admin    *bool  `json:"admin"`
}

type BlockUserParams struct {
	UserID  string `json:"user_id"`
	Blocked *bool  `json:"blocked"`
}
