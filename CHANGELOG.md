# Changelog

[Français](CHANGELOG.fr.md)

All notable changes to Missive are listed here. Versions follow [Semantic Versioning](https://semver.org);
the format is based on [Keep a Changelog](https://keepachangelog.com).

## [Unreleased]

### Fixed

- Voice messages in end-to-end encrypted conversations play in Missive. They use the Opus format,
  which the built-in player could not decode: Missive now converts them when they are downloaded.
- Videos and files larger than 64 MB open, up to 1 GB. Before, their download was refused.
- Videos in encrypted conversations show their thumbnail instead of a black frame.

## [0.3.0] - 2026-11-19

### Added

- A message deleted by its sender stays readable, with "This message was deleted" above it. A message
  that arrives already deleted still shows "Message removed". Exports follow the same rule.
- If the bridge stops unexpectedly, Missive restarts it on its own. After repeated failures, an
  error screen offers a Retry button and shows the end of the technical log.

### Fixed

- In end-to-end encrypted one-to-one conversations, a message deleted or edited by the other person
  is now actually deleted or edited. Before, the change was ignored.
- A bridge that stopped answering no longer blocks the app: every request now has a time limit, and
  images and avatars keep loading after an incident.
- After a long network outage, Missive keeps trying to reconnect (up to every 5 minutes) instead of
  giving up after 10 attempts. Encrypted conversations also keep retrying.
- An unknown value in the settings file (after going back to an older version, for example) no
  longer resets every setting: valid settings are kept, and a copy of the file is saved.
- Messages sharing the same millisecond are no longer lost when loading older history.
- The "typing…" indicator disappears on its own if the end event gets lost.
- Detached conversation windows update after an edit, a reaction or a poll vote.

### Changed

- Downloaded media, thumbnails and profile pictures move to a local cache
  (`%LOCALAPPDATA%\Missive\cache`), outside the roaming profile. Thumbnails and avatars are capped at
  500 MB.
- Dates and times follow the interface language. The search shortcut shows ⌘K on macOS.
- Consecutive messages from the same person are grouped when they are less than 5 minutes apart.
- The pinned message bar shows a preview of the pinned message.
- Less disk and CPU work: settings and drafts are written only when they change and never on the
  interface thread; conversations you have not opened in a while release their old messages from
  memory.

## [0.2.0] - 2026-11-18

### Added

- The interface is available in English and French. It follows the system language, and a
  Language setting changes it without a restart.
- Search accepts English keywords (`from:`, `in:`, `has:`, `before:`, `after:`) alongside the
  French ones, in both languages.
- Conversation exports and bridge error messages follow the app's language.

### Fixed

- Messages received while the computer was asleep now appear in the open conversation, not only in
  the sidebar preview.
- After waking up or reconnecting, recent conversations whose last activity is newer than their
  latest known message fetch the missing messages.
- Opening a conversation fetches messages newer than the local cache.
- The sidebar preview follows the newest stored message, even when Meta bumps a conversation's
  activity without sending the message.
- The media gallery no longer fails with "invalid type: null" on some conversations.

### Changed

- The macOS release builds on Intel runners and can be rerun on its own for an existing tag.
- Continuous integration no longer builds tags, which the release workflow already covers.

## [0.1.18] - 2026-11-17

First public release.

### Added

- Native Messenger client for Windows and macOS: Rust interface (GPUI) and a Go bridge based on
  mautrix-meta.
- End-to-end encrypted conversations, with history restored from your Messenger PIN.
- Replies, reactions, mentions, edits and unsends, forwarding, polls, pinned messages, link
  previews, photos, videos, files, locations, voice messages and emoji.
- Scheduled sending, reminders, advanced search, favourites, folders, quick replies, quiet hours,
  priority conversations, discreet reading, export and offline sending.
- Windows notifications with inline reply, unread badge, detachable window, launch at startup,
  automatic updates, light and dark themes.
- GIFs play inline in the conversation. Videos show a thumbnail with their duration and open in the
  system player.
- Clicking the avatar of a one-to-one conversation opens the person's Facebook profile in the
  default browser.
- The Missive icon is embedded in the executable (taskbar, shortcuts).
- After an unexpected error, Missive restarts on its own, at most once a minute.

### Fixed

- Logging back in after a logout no longer crashes. The first login attempt no longer fails.
- The login window is wider, so Meta's cookie banners fit.
- The Messenger PIN field accepts at most 6 digits.
- Scrolling over Settings, the PIN dialog, the gallery or the image viewer no longer scrolls the
  conversation behind them.
- The scrollbar is always visible. Settings keep their close button at the top while their content
  scrolls.
- Opening the detached window no longer crashes the app.

[Unreleased]: https://github.com/Lramelot/Missive/compare/v0.3.0...HEAD
[0.3.0]: https://github.com/Lramelot/Missive/compare/v0.2.0...v0.3.0
[0.2.0]: https://github.com/Lramelot/Missive/compare/v0.1.18...v0.2.0
[0.1.18]: https://github.com/Lramelot/Missive/releases/tag/v0.1.18
