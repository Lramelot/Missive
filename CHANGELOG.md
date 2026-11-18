# Changelog

[Français](CHANGELOG.fr.md)

All notable changes to Missive are listed here. Versions follow [Semantic Versioning](https://semver.org);
the format is based on [Keep a Changelog](https://keepachangelog.com).

## [Unreleased]

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

[Unreleased]: https://github.com/Lramelot/Missive/compare/v0.1.18...HEAD
[0.1.18]: https://github.com/Lramelot/Missive/releases/tag/v0.1.18
