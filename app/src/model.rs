use serde::Deserialize;

#[derive(Clone, Debug, Deserialize)]
pub struct User {
    pub id: String,
    pub name: String,
    #[serde(default)]
    pub avatar_url: Option<String>,
}

#[derive(Clone, Debug, Deserialize)]
pub struct Thread {
    pub id: String,
    pub name: String,
    #[serde(default)]
    pub avatar_url: Option<String>,
    #[serde(default)]
    pub snippet: String,
    pub last_activity_ms: i64,
    #[serde(default)]
    pub unread_count: u32,
    #[serde(default)]
    pub is_group: bool,
    #[serde(default)]
    pub e2ee: bool,
    #[serde(default)]
    pub muted: bool,
    #[serde(default)]
    pub member_avatars: Vec<String>,
    #[serde(default)]
    pub peer_id: Option<String>,
    #[serde(default)]
    pub read_receipts: Vec<ReadReceipt>,
    #[serde(default)]
    pub participants: Vec<Participant>,
    #[serde(default)]
    pub peer_online: Option<bool>,
    #[serde(default)]
    pub peer_last_active_ms: Option<i64>,
    #[serde(default = "default_folder")]
    pub folder: String,
    #[serde(default)]
    pub muted_until_ms: Option<i64>,
    #[serde(default)]
    pub theme: Option<ThreadTheme>,
    #[serde(default)]
    pub quick_emoji: Option<String>,
    #[serde(default)]
    pub ephemeral_ttl_ms: Option<i64>,
    #[serde(default)]
    pub pinned_message_ids: Vec<String>,
    #[serde(default)]
    pub can_manage: bool,
    #[serde(default)]
    pub is_admin: bool,
    #[serde(default)]
    pub blocked: bool,
}

fn default_folder() -> String {
    "inbox".to_string()
}

#[derive(Clone, Debug, Default, Deserialize)]
pub struct ThreadTheme {
    pub accent: String,
    #[serde(default)]
    pub gradient: Vec<String>,
}

#[derive(Clone, Debug, Default, Deserialize)]
pub struct UserResult {
    pub id: String,
    #[serde(default)]
    pub name: String,
    #[serde(default)]
    pub avatar_url: Option<String>,
    #[serde(default)]
    pub thread_id: Option<String>,
    #[serde(default)]
    pub is_friend: bool,
}

#[derive(Clone, Debug, Default, Deserialize)]
pub struct Poll {
    pub id: String,
    #[serde(default)]
    pub question: String,
    #[serde(default)]
    pub options: Vec<PollOption>,
    #[serde(default)]
    pub total_votes: u32,
}

#[derive(Clone, Debug, Default, Deserialize)]
pub struct PollOption {
    pub id: String,
    #[serde(default)]
    pub text: String,
    #[serde(default)]
    pub vote_count: u32,
    #[serde(default)]
    pub voters: Vec<Reactor>,
    #[serde(default)]
    pub mine: bool,
}

#[derive(Clone, Debug, Deserialize)]
pub struct ReactionAddedEvent {
    pub thread_id: String,
    pub emoji: String,
    #[serde(default)]
    pub actor_name: String,
    #[serde(default)]
    pub target_preview: String,
}

#[derive(Clone, Debug, Deserialize)]
pub struct MessageRemovedEvent {
    pub thread_id: String,
    pub message_id: String,
}

#[derive(Clone, Debug, Default, Deserialize)]
pub struct Reactor {
    pub user_id: String,
    #[serde(default)]
    pub name: String,
    #[serde(default)]
    pub avatar_url: Option<String>,
}

#[derive(Clone, Debug, Deserialize)]
pub struct Participant {
    pub user_id: String,
    #[serde(default)]
    pub name: String,
    #[serde(default)]
    pub avatar_url: Option<String>,
    #[serde(default)]
    pub online: Option<bool>,
    #[serde(default)]
    pub last_active_ms: Option<i64>,
    #[serde(default)]
    pub is_admin: bool,
}

#[derive(Clone, Debug, Default, Deserialize, serde::Serialize)]
pub struct Mention {
    pub offset: usize,
    pub length: usize,
    pub user_id: String,
}

#[derive(Clone, Debug, Deserialize)]
pub struct ReadReceipt {
    pub user_id: String,
    #[serde(default)]
    pub name: String,
    #[serde(default)]
    pub avatar_url: Option<String>,
    #[serde(default)]
    pub read_ms: i64,
    #[serde(default)]
    pub delivered_ms: Option<i64>,
}

#[derive(Clone, Debug, Default, Deserialize)]
pub struct ReplyPreview {
    pub id: String,
    #[serde(default)]
    pub sender_name: String,
    #[serde(default)]
    pub text: String,
    #[serde(default)]
    pub kind: Option<String>,
}

#[derive(Clone, Debug, Deserialize)]
pub struct MediaItem {
    pub message_id: String,
    pub thread_id: String,
    pub timestamp_ms: i64,
    #[serde(default)]
    pub sender_name: String,
    #[serde(default)]
    pub is_outgoing: bool,
    pub attachment: Attachment,
}

#[derive(Clone, Debug, Deserialize)]
pub struct MediaPage {
    pub items: Vec<MediaItem>,
    #[serde(default)]
    pub has_more: bool,
}

#[derive(Clone, Debug, Deserialize)]
pub struct SearchHit {
    pub thread_id: String,
    pub message: Message,
}

#[derive(Clone, Debug, Default, Deserialize)]
pub struct Reaction {
    pub emoji: String,
    #[serde(default)]
    pub count: u32,
    #[serde(default)]
    pub mine: bool,
    #[serde(default)]
    pub reactors: Vec<Reactor>,
}

#[derive(Clone, Debug, Deserialize)]
pub struct PresenceEvent {
    pub user_id: String,
    pub online: bool,
}

#[derive(Clone, Debug, Deserialize)]
pub struct HistoryStatus {
    pub backup: String,
    #[serde(default)]
    pub restored: bool,
}

#[derive(Clone, Debug, Default, Deserialize)]
pub struct Message {
    pub id: String,
    pub thread_id: String,
    #[serde(default)]
    pub sender_id: String,
    #[serde(default)]
    pub sender_name: String,
    #[serde(default)]
    pub text: String,
    pub timestamp_ms: i64,
    #[serde(default)]
    pub is_outgoing: bool,
    #[serde(default)]
    pub attachments: Vec<Attachment>,
    #[serde(default)]
    pub reply_to_id: Option<String>,
    #[serde(default)]
    pub reply_to: Option<ReplyPreview>,
    #[serde(default)]
    pub client_id: Option<String>,
    #[serde(default)]
    pub mentions: Vec<Mention>,
    #[serde(default)]
    pub edited: bool,
    #[serde(default)]
    pub deleted: bool,
    #[serde(default)]
    pub reactions: Vec<Reaction>,
    #[serde(default)]
    pub system: bool,
    #[serde(default)]
    pub forwarded: bool,
    #[serde(default)]
    pub poll: Option<Poll>,
}

#[derive(Clone, Debug, Default, Deserialize)]
pub struct Attachment {
    #[serde(default)]
    pub id: String,
    pub kind: String,
    #[serde(default)]
    pub url: Option<String>,
    #[serde(default)]
    pub thumbnail_url: Option<String>,
    #[serde(default)]
    pub duration_ms: Option<i64>,
    #[serde(default)]
    pub size: Option<i64>,
    #[serde(default)]
    pub title: Option<String>,
    #[serde(default)]
    pub description: Option<String>,
    #[serde(default)]
    pub source: Option<String>,
    #[serde(default)]
    pub file_name: Option<String>,
    #[serde(default)]
    pub mime_type: Option<String>,
    #[serde(default)]
    pub width: Option<u32>,
    #[serde(default)]
    pub height: Option<u32>,
    #[serde(default)]
    pub voice: bool,
    #[serde(default)]
    pub waveform: Vec<f32>,
    #[serde(default)]
    pub latitude: Option<f64>,
    #[serde(default)]
    pub longitude: Option<f64>,
}

#[derive(Clone, Debug, Deserialize)]
pub struct Status {
    pub logged_in: bool,
    #[serde(default)]
    pub connected: bool,
    #[serde(default)]
    pub user: Option<User>,
}

#[derive(Clone, Debug, Deserialize)]
pub struct ConnectionEvent {
    pub state: String,
    #[serde(default)]
    pub detail: Option<String>,
    #[serde(default)]
    pub permanent: bool,
}

#[derive(Clone, Debug, Deserialize)]
pub struct ThreadsEvent {
    pub threads: Vec<Thread>,
}

#[derive(Clone, Debug, Deserialize)]
pub struct ThreadRemovedEvent {
    pub thread_id: String,
}

#[derive(Clone, Debug, Deserialize)]
pub struct TypingEvent {
    pub thread_id: String,
    pub user_id: String,
    pub typing: bool,
}

#[derive(Clone, Debug, Deserialize)]
pub struct ReadyEvent {
    pub user: User,
}

#[derive(Clone, Debug, Deserialize)]
pub struct LoginResult {
    pub user: User,
}

#[derive(Clone, Debug, Deserialize)]
pub struct MediaResult {
    pub path: String,
}

#[derive(Clone, Debug, Deserialize)]
pub struct BackfillEvent {
    pub thread_id: String,
    #[serde(default)]
    pub messages: Vec<Message>,
}
