# Missive bridge protocol (v1)

Missive is split in two processes:

- `app/` — the desktop UI, pure Rust (GPUI via `gpui-kit`). Owns the window, notifications, local cache.
- `bridge/` — `missive-bridge`, a Go executable built on `go.mau.fi/mautrix-meta/pkg/messagix`
  (and whatsmeow for end-to-end encrypted chats). Owns the Messenger session. No UI, no Matrix.

The app spawns the bridge as a child process and talks to it over **stdin/stdout, one JSON object per
line (UTF-8, `\n`-terminated)**. The bridge writes **only protocol lines to stdout**; all logs go to
**stderr** (zerolog console or JSON, the app just forwards them to its own log).

## Launch

```
missive-bridge --data-dir <absolute path>
```

`--data-dir` holds everything the bridge persists (cookies, messagix state, whatsmeow SQLite device
store). The bridge creates it if missing. The app uses `%APPDATA%\Missive\bridge` on Windows and
`~/Library/Application Support/Missive/bridge` on macOS.

The bridge exits cleanly (disconnecting) when stdin reaches EOF.

### Data at rest

| file in `--data-dir` | content | protection |
|---|---|---|
| `cookies.json` | Meta session cookies (`xs`, `c_user`, `datr`, …) | encrypted |
| `session.json` | logged-in user, WhatsApp device id, secure-history device (enrollment keys) | encrypted |
| `state.json` | messagix connection state (tokens needed to reconnect without reloading messenger.com) | encrypted |
| `cache.json` | message cache (threads, messages, contacts, reactions) | encrypted |
| `whatsmeow.db` (+ `-wal`, `-shm`) | whatsmeow SQLite device store: E2EE identity, Signal sessions, pre-keys, sender keys | **not encrypted** (see below) |
| `media/`, `media/thumbs/` | downloaded attachments and thumbnails | not encrypted (opened directly by the app and the OS) |
| `avatars/` | cached profile pictures | not encrypted |

- Windows: "encrypted" files start with the ASCII header `MISSIVE-DPAPI-1\n`, then a big-endian `uint32`
  length and a random 256-bit key wrapped by DPAPI (`CryptProtectData`, current user scope, no UI, fixed
  application entropy), then a 12-byte nonce and the AES-256-GCM ciphertext (the header is the
  associated data). A new key is drawn at every write; DPAPI only ever wraps the key, so large caches stay
  fast. Only the same Windows user on the same machine can read them; a modified file fails to decrypt. Migration is transparent: a plaintext file found at load is read and
  encrypted into a temporary sibling; the plaintext is renamed to `<file>.migrating`, the encrypted file
  is renamed into place, and only then is `<file>.migrating` overwritten with zeros and removed (stray
  `<file>.*.tmp` leftovers too). If any rename fails (e.g. the file is held by an antivirus), the original
  is put back, the plaintext is used for this run and the migration is retried at the next load. A
  leftover `<file>.migrating` found at load is wiped and removed when `<file>` is a valid encrypted file,
  otherwise it is restored as `<file>`.
- Error messages sent to the app (`error.message`) go through the same text redaction as the logs, and
  download errors never embed the requested URL (only its host). If decryption fails (profile copied to another
  user or machine, corrupt file), the bridge starts **logged out** (never crashes): `status` answers
  `logged_in: false`, an undecryptable `state.json` is deleted, an undecryptable `cache.json` starts empty.
- Other OSes: no DPAPI; these files stay plaintext with `0600` permissions and the data dir is `0700`.
- `whatsmeow.db` is not encrypted at the file level (SQLite is opened by whatsmeow through the pure-Go
  driver, which has no page encryption). It is created by the bridge with `0600` before SQLite opens it
  (SQLite gives `-wal`/`-shm` the same mode); on Windows it relies on the ACL of the user profile
  (`%APPDATA%` is readable by the user, SYSTEM and administrators only).
- `logout` deletes `cookies.json`, `session.json`, `state.json`, `cache.json`, the media and avatar caches,
  and the WhatsApp device from `whatsmeow.db`.

### Logs

Logs (stderr) never contain message text, names, cookies, tokens, the PIN, phone numbers or e-mail
addresses. Every log line goes through a sanitizer: values of sensitive keys (`text`, `name`, `url`,
`body`, `payload`, `frame`, `*cookie*`, `*token*`, `pin`, …) and any structured (object/array) value are
replaced by `[redacted]`; in every remaining string, URL query strings and fragments are dropped
(`?[redacted]`), `key=value` secrets (`xs`, `c_user`, `datr`, `fb_dtsg`, `lsd`, `access_token`, `oh`, `oe`,
…) are redacted, e-mails become `[email]`, `+…` phone numbers and WhatsApp phone JIDs become `[phone]`,
the user's home directory becomes `~`, and values are cut at 600 characters. This also applies to the
logs of messagix / whatsmeow and to the global zerolog and standard `log` loggers. Thread ids, message
ids and command names are kept. The `connection` event `detail` goes through the same text redaction.

```
missive-bridge --data-dir <path> --demo
```

`--demo` never touches the network: it fakes a logged-in user, ~15 threads (1:1 and groups, some
unread, some e2ee), a few hundred messages with realistic timestamps, answers every command from that
fake store (`send` echoes a `message` event with `is_outgoing: true`), emits `connection` then `ready`
after `connect`, and emits a fake incoming `message` (plus `typing` before it) every 10–20 s in a
random thread. It is what the UI is developed and tested against.

## Envelope

Request (app → bridge):

```json
{"id": 1, "cmd": "threads", "params": {}}
```

`id` is a positive integer chosen by the app, unique per process lifetime. `params` is always an
object (may be empty, may be omitted).

Response (bridge → app), exactly one per request, in any order:

```json
{"id": 1, "ok": true, "result": <value>}
{"id": 1, "ok": false, "error": {"code": "not_logged_in", "message": "human readable"}}
```

Event (bridge → app), unsolicited, no `id`:

```json
{"event": "message", "data": {...}}
```

Error codes: `bad_request`, `not_logged_in`, `not_connected`, `auth_failed`, `not_found`,
`unsupported`, `bad_pin`, `pin_locked`, `internal`, `cancelled`, `busy`, `edit_rejected`. An error may carry an optional `data` object
with machine-readable details.

Live vs history: `message` events are only emitted for messages received live (or sent by us), never
for history/backfill/restore — the app raises desktop notifications on `message` events.

Paging rule (`messages` with `before_ms`, `media_history`): a page never splits a group of items that
share the same `timestamp_ms` — if the limit falls inside such a group, the page is extended to include
the whole group. This keeps "strictly older than `before_ms`" lossless.

Ordering: when a `message` belongs to a thread the app has never seen, the bridge emits the `threads`
upsert for that thread BEFORE the `message` event.

`send_media` errors carry `data: {message_ids: string[], failed_index: int, uncertain: bool}` —
`message_ids` were sent, `paths[failed_index..]` were not (or, when `uncertain`, the item at
`failed_index` may have been sent: Meta did not confirm). Sends to the same thread are serialized in
the order the requests were received.

## Types

All ids are **strings** (Meta ids exceed JS-safe integers; keep them as strings end to end).
Timestamps are **milliseconds since Unix epoch, integer**.

```ts
User {
  id: string,
  name: string,
  avatar_url?: string
}

Thread {
  id: string,              // thread key
  name: string,            // display name: group name, or for 1:1 the nickname set in this thread if any, else the contact name
  avatar_url?: string,
  member_avatars: string[],// groups without their own picture: up to 2 member avatar URLs (most recent senders first), else []
  peer_id?: string,        // 1:1 only: the other participant's user id (used for presence)
  snippet: string,         // last message preview, may be ""
  last_activity_ms: int,
  unread_count: int,       // 0 when read; 1 if only a boolean is known
  is_group: bool,
  e2ee: bool,              // true when the chat is end-to-end encrypted (whatsmeow side)
  muted: bool,             // notifications muted on Meta's side (mute expiry in the future or permanent)
  read_receipts: ReadReceipt[], // other participants' read watermarks, [] when unknown
  participants: Participant[],  // groups: every current member except the logged-in user (nickname rule for name), [] for 1:1
  peer_online?: bool,           // 1:1: whether the other person is currently active (absent when unknown)
  peer_last_active_ms?: int,    // 1:1: when the other person was last active (absent when unknown)
  folder: "inbox" | "requests" | "spam" | "archived", // Meta folder of the thread (LS folder / message-request state); "inbox" when unknown
  muted_until_ms?: int,         // absent = not muted; -1 = muted forever; else epoch ms when the mute ends (in the future)
  theme?: ThreadTheme,          // custom thread theme when one is set and its colors are known
  quick_emoji?: string,         // the thread's custom "like" emoji when set (default 👍 is NOT sent)
  ephemeral_ttl_ms?: int,       // disappearing messages are on, with this lifetime
  pinned_message_ids: string[], // pinned messages of the thread, most recent pin first, [] when none
  can_manage: bool,             // the bridge can run group-management commands on this thread (group, not E2EE-only, we are a member)
  is_admin: bool,               // the logged-in user is an admin of this group
  blocked: bool                 // 1:1: the logged-in user blocked the other person
}

ThreadTheme {
  accent: string,               // "#RRGGBB", the color Messenger uses for the logged-in user's bubbles
  gradient: string[]            // "#RRGGBB" stops when the theme is a gradient, [] otherwise
}

Participant {
  user_id: string,
  name: string,
  avatar_url?: string,
  online?: bool,
  last_active_ms?: int,
  is_admin: bool
}

UserResult {                    // people search result
  id: string,
  name: string,
  avatar_url?: string,
  thread_id?: string,           // existing 1:1 thread with this person, when known
  is_friend: bool
}

Poll {
  id: string,
  question: string,
  options: PollOption[],
  total_votes: int
}

PollOption {
  id: string,
  text: string,
  vote_count: int,
  voters: Reactor[],            // known voters (may be shorter than vote_count)
  mine: bool                    // the logged-in user voted for this option
}

Mention {
  offset: int,             // BYTE offset into the UTF-8 `text` of the message (the bridge converts from/to Meta's UTF-16 offsets)
  length: int,             // byte length of the mention span in `text` (e.g. the bytes of "@Camille Martin")
  user_id: string
}

ReadReceipt {
  user_id: string,
  name: string,            // nickname rule as for sender_name
  avatar_url?: string,
  read_ms: int,            // everything up to this timestamp is read by this user (0 when only delivery is known)
  delivered_ms?: int       // everything up to this timestamp was delivered to this user's devices (LS delivery receipts / E2EE delivery receipts)
}

ReplyPreview {
  id: string,              // the quoted message id
  sender_name: string,
  text: string,            // "" if unknown or media-only
  kind?: string            // first attachment kind of the quoted message, if any
}

MediaItem {
  message_id: string,
  thread_id: string,
  timestamp_ms: int,
  sender_name: string,
  is_outgoing: bool,
  attachment: Attachment
}

SearchHit {
  thread_id: string,
  message: Message
}

Message {
  id: string,
  thread_id: string,
  sender_id: string,
  sender_name: string,     // nickname in this thread if set, else contact name; "" if unknown
  reactions: Reaction[],   // aggregated, [] when none
  text: string,            // plain text; "" for media-only messages
  timestamp_ms: int,
  is_outgoing: bool,       // sent by the logged-in user
  attachments: Attachment[],
  reply_to_id?: string,
  reply_to?: ReplyPreview, // filled whenever reply_to_id is set and the quoted message is known (cache, LS replied-to rows, E2EE quoted context)
  client_id?: string,      // echo of the `client_id` given to `send`/`send_media` for our own outgoing messages (live echo only)
  mentions: Mention[],     // people mentioned in `text`, [] when none (LS mention data and E2EE mentions)
  edited: bool,
  deleted: bool,
  system: bool,            // admin/system line (renames, joins, leaves, theme/emoji/nickname changes, calls "Appel manqué", poll summaries…): `text` is the ready-to-display French sentence, rendered centered by the app; no reactions/reply actions
  forwarded: bool,         // the message was forwarded from another conversation
  poll?: Poll              // the message carries a poll (LS polls)
}

Reaction {
  emoji: string,
  count: int,
  mine: bool,              // the logged-in user is among the reactors
  reactors: Reactor[]      // who reacted with this emoji, most recent first (self included, flagged by user_id == own id); may be shorter than `count` when Meta only sent an aggregate
}

Reactor {
  user_id: string,
  name: string,            // nickname rule
  avatar_url?: string,
  timestamp_ms?: int
}

Attachment {
  id: string,              // unique within its message, stable across restarts
  kind: "image" | "video" | "audio" | "file" | "sticker" | "link" | "location" | "story" | "contact" | "other",
                           // location: url = maps link to open, title = place name or "Position", description = address, thumbnail_url = map image, latitude/longitude when known (live location too)
                           // story: a story reply / story mention / shared story: title = label shown ("A répondu à votre story"), url = story link when known, thumbnail_url = story preview
                           // contact: shared contact card: title = name, description = phone / e-mail / profile line
  url?: string,            // media: directly loadable https URL (non-E2EE CDN), absent for E2EE media.
                           // link: the TARGET URL to open in the browser (never the preview image)
  thumbnail_url?: string,  // smaller preview when Meta provides one; for links the preview image
  title?: string,          // link: preview title (page title, post author, reel caption…)
  description?: string,    // link: preview subtitle / description
  source?: string,         // link: short origin label, e.g. "youtube.com", "Instagram", "Facebook"
  duration_ms?: int,       // audio / video
  size?: int,              // bytes
  file_name?: string,
  mime_type?: string,
  width?: int,
  height?: int,
  voice: bool,             // audio recorded as a voice message (LS voice clip / E2EE PTT)
  gif?: bool,              // video: an animated GIF Meta delivers as a looping MP4 (E2EE gif_playback)
  waveform: float[],       // voice: amplitude samples in 0..1 when Meta provides them, [] otherwise
  latitude?: float,
  longitude?: float
}
```

## Commands

| cmd | params | result |
|---|---|---|
| `status` | — | `{logged_in: bool, connected: bool, user?: User}` |
| `login` | `{cookies: {name: value, ...}}` — at least `xs`, `c_user`, `datr` | `{user: User}`; cookies are persisted in the data dir |
| `logout` | — | `{}`; disconnects and deletes persisted session |
| `connect` | — | `{}`; starts the connection loop (returns immediately, progress via `connection` events). Idempotent. |
| `threads` | `{limit?: int}` (default 100) | `Thread[]`, newest activity first |
| `messages` | `{thread_id: string, before_ms?: int, limit?: int, fetch_missing?: bool}` (default 50, `fetch_missing` default true) | `Message[]`, **oldest first**. **Always answers immediately from the local store** (never waits on Meta). Without `before_ms`: the latest stored messages. With it: stored messages strictly older. When the store has fewer than `limit` matching messages and `fetch_missing` is true, the bridge fetches from Meta **in the background** and delivers what it finds through a `messages_backfilled` event (and returns what it has right away, possibly `[]`). |
| `send` | `{thread_id: string, text: string, reply_to_id?: string, client_id?: string, mentions?: Mention[]}` | `{message_id?: string}`; the live echo `message` carries the same `client_id`. `mentions` use byte offsets into `text`; each span must lie inside `text` and `user_id` must be a participant of the thread, else `bad_request`. LS: mention data on the send task (as mautrix does); E2EE: Messenger/WhatsApp mention metadata (as mautrix does). |
| `mark_read` | `{thread_id: string}` | `{}` |
| `send_media` | `{thread_id: string, paths: string[], caption?: string, reply_to_id?: string, client_ids?: string[]}` (one client id per path; the caption message, if separate, reuses the first) — absolute local file paths (images, videos, audio, any file) | `{message_ids: string[]}`; echoes `message` events like `send`. Images/videos are sent as media, everything else as files. E2EE: upload + encrypt through whatsmeow; LS: messagix upload. Errors: `bad_request` (missing/unreadable file, > 100 MB), `unsupported`. |
| `react` | `{thread_id: string, message_id: string, emoji: string}` — `""` removes my reaction | `{}`; the change comes back as `message_updated` |
| `set_typing` | `{thread_id: string, typing: bool}` | `{}` — best effort, fire and forget |
| `set_active` | `{active: bool}` | `{}` — the app window gained/lost focus. The bridge only advertises E2EE presence "available" while active (and sends "unavailable" when inactive); it never forces "available" on connect. |
| `search` | `{query: string, thread_id?: string, limit?: int}` (default 50) | `SearchHit[]`, newest first. Case/accent-insensitive substring search over message text (and link titles, file names) of everything the bridge has stored locally (live, backfill, archive). |
| `media_history` | `{thread_id: string, before_ms?: int, limit?: int, kinds?: string[], max_pages?: int}` (`max_pages` = how many Meta pages this call may fetch, default 10; `0` = answer from the store only, instantly) (default limit 60, kinds `["image","video"]`; also `"file"`, `"link"`, `"audio"`) | `{items: MediaItem[], has_more: bool}` newest first, strictly older than `before_ms`. Must reach the WHOLE history of the thread, not just the cache: when the store runs out it pages older history from Meta (LS FetchMessages / archive pages for E2EE, or a dedicated shared-media query if Meta has one) until `limit` items are found or history is exhausted. Fetched messages are stored as history (no `message` events). |
| `history_status` | — | `{backup: "none" \| "available" \| "unknown", restored: bool}` — whether the account has an encrypted history backup (secure storage) and whether this bridge already restored it. Requires a connected session. |
| `restore_history` | `{pin: string}` (6 digits) | `{}` once the backup is restored and its content applied to the store (threads/messages events are emitted for everything it changes). Errors: `bad_pin` (message carries the attempts Meta reports remaining, `data.attempts_remaining: int` when known), `pin_locked` (no attempts left), `unsupported` (no backup). The PIN is never logged nor persisted. |
| `avatar` | `{url: string}` (a `User.avatar_url`, `Thread.avatar_url`, `member_avatars[]` or `ReadReceipt.avatar_url`) | `{path: string}` — local file, cached on disk under `<data-dir>/avatars/<sha256>.<ext>`; served from disk immediately when present (refreshed in the background if older than 7 days); same host allow-list and limits as media downloads. |
| `media` (variant) | same params + `variant?: "full" \| "thumb"` (default `"full"`) | `"thumb"`: a small image (longest side ≤ 640 px, JPEG) suitable for bubbles and grids, cached under `<data-dir>/media/thumbs/`. Sources, fastest first: the inline JPEG thumbnail embedded in E2EE image/video/sticker/link messages (no network at all), the attachment's `thumbnail_url`, else download the full media and downscale it (pure Go decoders: jpeg, png, gif, webp). Works for `video` attachments (returns the poster image). Result also carries `width`, `height` of the returned file. |
| `media` | `{thread_id: string, message_id: string, attachment_id: string}` | `{path: string, mime_type?: string}` — absolute path of the decrypted file, downloaded on first call into `<data-dir>/media/` and served from there afterwards. Works for E2EE (whatsmeow download + decrypt) and non-E2EE (plain download of `url`). `not_found` if the attachment is unknown, `unsupported` if the media expired on Meta's side. |

### Commands added in v1.1

All of them answer `not_found` for an unknown thread/message, `bad_request` for invalid params,
`unsupported` when Meta offers no way to do it for that thread (e.g. LS-only feature on an E2EE
thread), and never touch the account outside the requested action. Each one must be implemented the
way mautrix-meta does it when mautrix-meta supports it (LS task and/or whatsmeow / Messenger
ConsumerApplication message).

| cmd | params | result |
|---|---|---|
| `edit_message` | `{thread_id, message_id, text}` — own, non-deleted, non-system text message; `text` non-empty | `{}`; the change comes back as `message_updated` with `edited: true`. LS `EditMessageTask`; E2EE edit message. Meta refuses edits older than 15 min: `bad_request` with a clear message. |
| `unsend` | `{thread_id, message_id}` — own message | `{}`; `message_updated` with `deleted: true`, `text: ""`, `attachments: []`. LS `DeleteMessageTask`; E2EE revoke. |
| `delete_for_me` | `{thread_id, message_id}` | `{}`; removed from the store, then a `message_removed` event. LS `DeleteMessageMeOnlyTask`; E2EE: local only. |
| `forward` | `{from_thread_id, message_id, to_thread_ids: string[]}` (1–10 targets) | `{message_ids: string[]}` (one per target, `""` when unknown). LS→LS: `SendMessageTask` with `ForwardedMsgId`. Any E2EE side: re-send the text and re-upload the media (download from source first). Targets receive normal `message` echoes. |
| `send_voice` | `{thread_id, path, duration_ms, client_id?, reply_to_id?}` — `path` = WAV, PCM 16-bit, mono, 16 or 48 kHz, recorded by the app | `{message_id?: string}`; sent as a voice message (LS voice clip / E2EE PTT audio), transcoded by the bridge to the format Messenger plays (pure Go, no ffmpeg); echo like `send` with `client_id`. |
| `create_poll` | `{thread_id, question, options: string[]}` (2–12 non-empty options) | `{}`; LS groups only (`CreatePollTask`), else `unsupported`. The poll arrives as a `message` with `poll`. |
| `vote_poll` | `{thread_id, poll_id, option_ids: string[]}` — the complete set of options I select (`[]` = remove my votes) | `{}`; `UpdatePollTask`; the updated poll arrives as `message_updated`. |
| `search_users` | `{query, limit?}` (default 20) | `UserResult[]` — Meta people search (`SearchUserTask`, as mautrix-meta's resolve/search) merged with matching cached contacts, contacts first. |
| `open_thread` | `{user_id}` | `Thread` — the existing 1:1 thread with that person, else creates it (as mautrix-meta's "start chat": `CreateThreadTask`, or the E2EE path when Meta requires it). Emits a `threads` upsert too. |
| `create_group` | `{user_ids: string[] (≥ 2), name?}` | `Thread` — `CreateGroupTask`, then `RenameThreadTask` when `name` is given. |
| `accept_request` | `{thread_id}` | `{}`; `AcceptMessageRequestTask`; `threads` upsert with `folder: "inbox"`. |
| `delete_thread` | `{thread_id}` | `{}`; `DeleteThreadTask` (LS) then `thread_removed`; E2EE-only threads: removed locally only (still `thread_removed`). |
| `mute` | `{thread_id, until_ms}` — `0` unmute, `-1` forever, else epoch ms in the future | `{}`; `MuteThreadTask`; `threads` upsert. |
| `rename_thread` | `{thread_id, name}` (group, `""` clears) | `{}`; `RenameThreadTask`. |
| `set_thread_image` | `{thread_id, path}` (local image) | `{}`; upload the image then `SetThreadImageTask`. |
| `add_participants` | `{thread_id, user_ids: string[]}` | `{}`; `AddParticipantsTask`. |
| `remove_participant` | `{thread_id, user_id}` | `{}`; `RemoveParticipantTask`. |
| `leave_thread` | `{thread_id}` | `{}`; `RemoveParticipantTask` with my own id. |
| `set_admin` | `{thread_id, user_id, admin: bool}` | `{}`; `UpdateAdminTask`. |
| `block_user` | `{user_id, blocked: bool}` | `{}`; `SetUserBlockStatusTask` (as mautrix-meta when it uses it); `threads` upsert of the 1:1 with `blocked`. |

Group-management commands require `can_manage: true` on the thread, else `unsupported`.

Bridge clarifications (v1.1, behaviour as implemented):

- `folder`: LS folders `pending`, `other`, `e2ee_cutover_pending`, `e2ee_cutover_other`, `restricted`, `salsa_restricted`, `messenger_marketing_message`, `page_background` → `"requests"`; `spam`, `blocked` → `"spam"` (spam threads are listed instead of dropped); `archived`, `e2ee_cutover_archived`, `hidden`, `disabled` → `"archived"`; anything else → `"inbox"`. Message-request rows follow mautrix-meta: status 1 moves the thread to `"requests"`, any other status moves a `"requests"` thread back to `"inbox"`; spam threads are left alone. Rows keyed by the Facebook key of a hybrid encrypted thread apply to that thread. `accept_request` moves it back to `"inbox"`.
- `theme` / `quick_emoji`: from the LS thread row (outgoing bubble color, theme id + gradient rows, custom emoji). Encrypted-only threads carry none.
- `ephemeral_ttl_ms`: derived from the ephemeral duration of the newest LS message of the thread (Meta gives no thread-level value); not available for encrypted-only threads.
- `can_manage` is `false` for encrypted (E2EE) groups: group management of encrypted groups is not implemented, so those commands answer `unsupported`. `create_poll` / `vote_poll` are LS-only.
- `set_admin` additionally requires `is_admin: true`. After `leave_thread` (or `remove_participant` with my own id) the group stays listed with its history and `can_manage: false`, even when Meta's answer or a later sync reports my removal (mautrix-meta deletes the room instead); being added back makes it manageable again. Commands never re-create a thread that Meta's answer removed.
- `delete_for_me` on an encrypted message is local only. An LS delete-for-me row coming from Meta (another device) also produces `message_removed`; unsend for everyone keeps the message with `deleted: true`. An encrypted unsend is also uploaded to the secure history backup (as mautrix-meta does); edits are not.
- `edit_message` (LS): when Meta answers with a different text and does not correct it within 5 s, Meta's version is kept and the command fails with `edit_rejected` (the app shows its own translated message; the bridge's `message` is English, for logs only).
- Incoming encrypted edits and revokes only apply to a message of the same conversation sent by the editor (message key resolved like mautrix-meta: chat + sender + id); in groups an admin may also revoke someone else's message.
- System lines: LS admin messages (`IsAdminMessage`) and call logs become `system: true` messages; common English admin sentences are translated to French, others are passed through. Call logs carry no attachment. Poll vote events are folded into the poll (`message_updated` of the poll message); when Meta attaches a sentence to the vote it is also kept as a system line.
- `forward`: LS→LS uses `SendMessageTask` with `ForwardedMsgId` (plus the read-watermark task mautrix-meta sends with every message); any other combination re-sends the text and re-uploads image/video/audio/file attachments (links, stickers-by-id, polls and system lines are not forwardable → `bad_request`). A target whose text went out but whose attachment failed reports the text message id; a target where nothing went out yields `""`; the command fails only when every target failed. Forwards are serialized with the other sends to the same target threads.
- `open_thread` with a known group thread id → `bad_request`. `create_group`: when Meta does not return the new thread key, the bridge looks for the new group in the following sync for 5 s; otherwise it fails with `internal` and `data.uncertain: true` (the group may have been created).
- `send_voice`: the WAV is downmixed to mono, resampled to 24 kHz (linear interpolation) and transcoded in pure Go to MP3 (MPEG-2 Layer III, 24 kHz mono, 128 kbit/s; shine port — the only rates it encodes correctly are 22.05/24 kHz) and sent as `audio/mpeg`: LS as a voice clip upload (`voice_clip` + waveform), E2EE as an `AudioMessage` with PTT and waveform bytes. Accepted WAV: PCM 16-bit, mono or stereo (downmixed), 8/11.025/12/16/22.05/24/32/44.1/48 kHz, at most 10 minutes. The echo carries `voice: true`, `waveform` and `duration_ms`.
- `open_thread`: LS `CreateThreadTask` only (the encrypted `CreateWhatsAppThreadTask` that mautrix-meta also issues is not sent); the returned thread is the LS 1:1.
- `mute` / `accept_request` / `delete_thread` on encrypted threads use the Facebook thread key in sync group 95 when it is known, else the thread id (WA JID) in sync group 1, as mautrix-meta does.
- `set_thread_image` accepts JPEG/PNG/GIF/WebP up to 10 MB.
- `search_users`: cached contacts first (`is_friend` = an existing 1:1 conversation), then Meta results (`is_friend` = Meta contact).

### Commands added in v1.2

| cmd | params | result |
|---|---|---|
| `search` (extended) | `{query?: string, thread_id?, sender_id?, kinds?: string[], before_ms?: int, after_ms?: int, limit?: int}` — `query` may be empty or absent when at least one filter is set; `kinds` keeps only messages having an attachment of one of these kinds (`"image"`, `"video"`, `"audio"`, `"file"`, `"link"`, `"location"`, …); `sender_id` = author (the logged-in user's id for "me"); `before_ms` / `after_ms` bound `timestamp_ms` (exclusive / inclusive) | `SearchHit[]`, newest first, same local-store scope as before. All filters combine with AND; the text match stays case/accent-insensitive over text, link titles and file names. |
| `export_thread` | `{thread_id, dir, format: "html" \| "markdown" \| "json", include_media: bool, locale?: "fr" \| "en"}` — `dir` = an existing local folder chosen by the user; `locale` = language of the texts written in the HTML/Markdown file (see below) | `{path: string, messages: int, media: int, media_failed: int}` — writes ONE export folder `<dir>/<safe thread name> <YYYY-MM-DD>/` containing `conversation.html` (self-contained, readable offline, inline CSS, local relative links to media) or `conversation.md` or `conversation.json` (array of protocol `Message`), plus `media/` when `include_media`. Covers the WHOLE history: pages older messages from Meta like `media_history` (LS FetchMessages / E2EE archive) before writing. Emits `export_progress {thread_id, messages, media}` events while running. Never overwrites: adds " (2)" etc. Errors: `bad_request` (dir missing / not writable), `not_found`. |
| `save_media` | `{thread_id, dir, kinds?: string[], locale?: "fr" \| "en"}` (default `["image","video"]`) | `{saved: int, failed: int, dir: string}` — downloads every media of the thread's whole history (same source as `media_history`, all pages) into `<dir>/<safe thread name> - médias/` (`- media/` when `locale` is `"en"`), file names `YYYY-MM-DD HHMMSS <original name or id>.<ext>`, skipping files already present. Emits `save_media_progress {thread_id, saved, total?}`. |

Clarifications (v1.2 implementation):

- `search` `kinds` accepts `"image"`, `"video"`, `"audio"`, `"voice"` (voice messages only), `"file"`, `"link"` (link previews, and also messages whose text contains an `http(s)://` URL), `"location"`, `"sticker"`, `"contact"`, `"story"`, `"other"`; any other value → `bad_request`. `thread_id` counts as a filter (empty query + `thread_id` = the thread's messages). With an empty query, `system: true` messages are left out. `sender_id` equal to the logged-in user's id also matches outgoing messages.
- `export_thread`: in `conversation.json` each entry is a protocol `Message` plus, when its media were copied, `local_files: {<attachment id>: "media/<file name>"}`. Copied kinds: image, video, audio, file, sticker (links, locations, contacts are rendered as text/links). `media/` is omitted when nothing was copied. Media file names follow the `save_media` pattern; names that already exist get " (2)" etc. The progress event is throttled (≤ 4/s, plus first and last).
- `save_media` `kinds` accepts `"image"`, `"video"`, `"audio"`, `"file"`, `"sticker"`. The result also carries `skipped: int` (files already present, not counted in `saved`); `save_media_progress` also carries `skipped` and `failed`, and `total` once the history is loaded. The folder is reused across runs. A file is skipped only when a file with the same content (same size, then same SHA-256; or, before downloading, the same name and the attachment's known size) is already there; a different file with the same name gets " (2)", " (3)"…. Files are never overwritten.
- `locale` (`export_thread`, `save_media`): the app sends the language of its interface (`"fr"` or `"en"`). `"en"`, or any tag starting with `en-` / `en_`, selects English; anything else, or no `locale`, keeps French (the output before this field existed). It sets the texts written by the bridge: in HTML/Markdown exports the `<html lang>`, the summary line (`N messages · exported on <date> at <HH:MM> from Missive`), day headings, "You" / "Unknown", attachment labels, "Forwarded", "edited", "Message removed", reply and poll lines, "Reactions:", the footer, and number grouping (`1,234` in English, narrow no-break space in French); for `save_media`, the folder suffix (` - media` / ` - médias`). JSON exports and the bridge's error messages are not localized: the app translates error codes.
- Archive jobs (`export_thread`, `save_media`): one at a time per account — a second one while one runs → `busy`. Meta history pages are fetched at most every 1.5–2.5 s (random jitter); paging stops after 2 pages without progress; media downloading stops after 8 consecutive failures (the rest are counted in `media_failed` / `failed`). Media are downloaded straight into the target folder (temporary `.missive-*.part` file, then renamed); nothing is added to the bridge's private cache (already cached media are copied from it). On Windows every written file (media and `conversation.*`) gets a `Zone.Identifier` stream (`ZoneId=3`, best effort).
- `cancel_archive` `{}` → `{cancelled: bool}` (`true` when a job was running). The running `export_thread` / `save_media` then fails with the new error code `cancelled` ("Export annulé"); a cancelled export removes its partial folder; a cancelled `save_media` keeps the files already completed.

### Commands added in v1.3

| cmd | params | result |
|---|---|---|
| `diagnostics` | — | `{version: string, go_version: string, os: string, connected: bool, e2ee_ready: bool, threads: int, messages: int, data_dir_size_bytes: int, last_errors: string[]}` |

- `version`: the bridge version (`main.bridgeVersion`, `"dev"` unless set at build time with
  `-ldflags "-X main.bridgeVersion=<x.y.z>"`), plus `+<12-char git revision>` (and `.dirty`) when the
  binary carries VCS info. `go_version`: e.g. `go1.27.4`. `os`: `GOOS/GOARCH`, e.g. `windows/amd64`.
- `connected`: same as `status.connected`. `e2ee_ready`: the WhatsApp (E2EE) socket is connected and
  logged in (demo: `true` once connected).
- `threads` / `messages`: counts in the local store (messages loaded in memory, all threads).
- `data_dir_size_bytes`: total size of the regular files under `--data-dir` (media and avatars included).
- `last_errors`: the last 20 log lines of level warn or above (oldest first), already redacted like the
  logs, formatted `<RFC3339 time> <level> <component>: <message>: <error>`. Requests that fail with
  `internal` are logged at warn and therefore appear here. Always an array (possibly empty).
- Works logged out and in `--demo`. Never touches the network.

Read receipts: the bridge never sends a read receipt (LS mark-read task, E2EE read receipt) except through `mark_read` — and when the user sends a message into the thread (`send`, `send_media`, `send_voice`, `forward`), which marks it read on Meta's side like the official clients, unless the command carries `"mark_read": false` (optional, default `true`). With `mark_read: false`: on an LS thread the `SendMessageTask` is sent ALONE (no `ThreadMarkReadTask` bundled; tasks are independent LS tasks with their own label/queue, so the send is unaffected — not verified live; Meta may still advance the sender's own read state server-side); on an encrypted thread no WhatsApp receipt and no sync-group-95 task is sent. Receiving messages, opening a thread (`messages`), `set_active`, `search`, `media_history`, `export_thread` and `save_media` never mark anything as read on Meta's side. The mark-read after an encrypted send runs in the background (5 s timeout), ordered per thread, after the send result is returned.

How `mark_read` marks read (mirrors mautrix-meta's `HandleMatrixReadReceipt`): LS thread → `ThreadMarkReadTask{thread key, watermark = max(last activity, newest stored message), sync_group 1}`. Encrypted thread → WhatsApp read receipts (grouped by sender in groups) for the stored incoming messages newer than the read watermark **Meta** last reported for this account (never a local or self-written value), each id at most once per bridge run (a message delivered late, older than the newest one, still gets its receipt; capped to the 100 newest), plus `ThreadMarkReadTask` on the thread's Facebook key in sync group 95 — the most recently seen hybrid mapping key, including when it equals the JID (common for 1:1), or the peer id for a 1:1 chat without a known mapping; the watermark is the newest STORED message (+999 ms for second-precision WA timestamps), never the thread's activity timestamp. The task is skipped when the read point does not advance (already marked up to the newest stored message and the thread is not unread); a thread that Meta reports unread again (lower read watermark or unread count) is re-marked.

On startup, if a session is persisted, the bridge loads it but **does not connect** until it receives
`connect`.

## Events

| event | data |
|---|---|
| `connection` | `{state: "connecting" \| "connected" \| "disconnected" \| "error", detail?: string, permanent?: bool}` — `permanent: true` means the session is invalid and the user must log in again |
| `threads` | `{threads: Thread[]}` — upsert: new or changed threads |
| `thread_removed` | `{thread_id: string}` |
| `message` | `Message` — new incoming or outgoing message (including echo of our own sends) |
| `message_updated` | `Message` — edit or unsend of an existing message |
| `typing` | `{thread_id: string, user_id: string, typing: bool}` |
| `read` | `{thread_id: string, user_id: string, timestamp_ms: int}` |
| `ready` | `{user: User}` — initial sync done; `threads` can be called and is meaningful |
| `presence` | `{user_id: string, online: bool, last_seen_ms?: int}` — best effort, for 1:1 peers |
| `export_progress` | `{thread_id: string, messages: int, media: int}` — while `export_thread` runs |
| `save_media_progress` | `{thread_id: string, saved: int, total?: int}` — while `save_media` runs |
| `reaction_added` | `{thread_id, message_id, emoji, actor_id, actor_name, target_preview}` — emitted only for a LIVE reaction (not initial sync, backfill, history restore or archive paging) added by SOMEONE ELSE to one of MY messages (outgoing), LS and E2EE; not emitted for a removal or for my own reactions; `target_preview` = the first ~80 characters of the target message text, or a short French description of its first attachment ("Photo", "Vidéo", "Message vocal", "Fichier", "Lien", "Autocollant", "Pièce jointe") when it has no text; `actor_name` follows the nickname rule (may be `""` when the contact is unknown). Skipped when the target message is not in the local store, and for aggregate-only LS rows that carry no actor. The usual `message_updated` is still emitted. |
| `message_removed` | `{thread_id: string, message_id: string}` — the message disappears for me (`delete_for_me`, or Meta deleted it for me) |
| `messages_backfilled` | `{thread_id: string, messages: Message[]}` — older messages fetched in the background after a `messages` call (history: no notifications), oldest first; the app merges them by id |

`message_updated` is also emitted when a message's reactions change.

A `threads` upsert is emitted when a thread's `read_receipts` change (live read events).
