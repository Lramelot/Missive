use std::{path::PathBuf, time::Duration};

use chrono::{Local, TimeZone as _};
use gpui_kit::component::input::{InputEvent, InputState};
use gpui_kit::*;
use serde_json::{Value, json};

use super::{Filter, MissiveApp, action_error, library::SearchState};
use crate::{
    model::{Participant, Thread, UserResult},
    settings::Folder,
};

const PEOPLE_DEBOUNCE: Duration = Duration::from_millis(250);
const HOUR_MS: i64 = 3_600_000;
const IMAGE_EXTENSIONS: [&str; 6] = ["png", "jpg", "jpeg", "gif", "webp", "bmp"];
const FOLDER_NAME_MAX: usize = 40;

#[derive(Clone, Copy, PartialEq, Eq)]
pub(crate) enum PeopleMode {
    Direct,
    Group,
    Add,
}

pub(crate) struct PeopleDialog {
    pub(crate) mode: PeopleMode,
    pub(crate) thread_id: Option<String>,
    pub(crate) input: Entity<InputState>,
    pub(crate) name_input: Entity<InputState>,
    pub(crate) query: String,
    pub(crate) results: Vec<UserResult>,
    results_query: String,
    pub(crate) searching: bool,
    pub(crate) selected: Vec<UserResult>,
    pub(crate) busy: bool,
    serial: u64,
    _task: Option<Task<()>>,
    _subscriptions: Vec<Subscription>,
}

impl PeopleDialog {
    pub(crate) fn is_direct(&self) -> bool {
        self.mode == PeopleMode::Direct
    }

    pub(crate) fn is_add(&self) -> bool {
        self.mode == PeopleMode::Add
    }

    pub(crate) fn is_selected(&self, user_id: &str) -> bool {
        self.selected.iter().any(|user| user.id == user_id)
    }
}

pub(crate) struct RenameDialog {
    pub(crate) thread_id: String,
    pub(crate) input: Entity<InputState>,
    _subscription: Subscription,
}

#[derive(Clone, PartialEq, Eq)]
pub(crate) enum ConfirmAction {
    DeleteThread { thread_id: String },
    LeaveGroup { thread_id: String },
    Block { user_id: String, blocked: bool },
    RemoveMember { thread_id: String, user_id: String },
    DeleteFolder { folder_id: String },
}

pub(crate) struct Confirm {
    pub(crate) title: SharedString,
    pub(crate) text: SharedString,
    pub(crate) button: SharedString,
    pub(crate) danger: bool,
    pub(crate) action: ConfirmAction,
}

#[derive(Clone, PartialEq, Eq)]
pub(crate) enum MenuKind {
    Thread { thread_id: String },
    Mute { thread_id: String },
    Member { thread_id: String, user_id: String },
    Folders { thread_id: String },
    FolderTab { folder_id: String },
}

impl MenuKind {
    pub(crate) fn thread_id(&self) -> &str {
        match self {
            MenuKind::Thread { thread_id }
            | MenuKind::Mute { thread_id }
            | MenuKind::Folders { thread_id }
            | MenuKind::Member { thread_id, .. } => thread_id,
            MenuKind::FolderTab { .. } => "",
        }
    }

    pub(crate) fn is_mute(&self) -> bool {
        matches!(self, MenuKind::Mute { .. })
    }

    pub(crate) fn is_folders(&self) -> bool {
        matches!(self, MenuKind::Folders { .. })
    }

    pub(crate) fn folder_tab_id(&self) -> Option<&str> {
        match self {
            MenuKind::FolderTab { folder_id } => Some(folder_id),
            _ => None,
        }
    }

    pub(crate) fn member_id(&self) -> Option<&str> {
        match self {
            MenuKind::Member { user_id, .. } => Some(user_id),
            _ => None,
        }
    }
}

#[derive(Clone, PartialEq, Eq)]
pub(crate) enum FolderDialogMode {
    Create { thread_id: Option<String> },
    Rename { folder_id: String },
}

pub(crate) struct FolderDialog {
    pub(crate) mode: FolderDialogMode,
    pub(crate) input: Entity<InputState>,
    _subscription: Subscription,
}

impl FolderDialog {
    pub(crate) fn is_rename(&self) -> bool {
        matches!(self.mode, FolderDialogMode::Rename { .. })
    }

    pub(crate) fn adds_thread(&self) -> bool {
        matches!(self.mode, FolderDialogMode::Create { thread_id: Some(_) })
    }
}

pub(crate) struct ThreadMenu {
    pub(crate) kind: MenuKind,
    pub(crate) position: Point<Pixels>,
}

#[derive(Default)]
pub(crate) struct ThreadOps {
    pub(crate) people: Option<PeopleDialog>,
    pub(crate) rename: Option<RenameDialog>,
    pub(crate) confirm: Option<Confirm>,
    pub(crate) menu: Option<ThreadMenu>,
    pub(crate) folder: Option<String>,
    pub(crate) folder_dialog: Option<FolderDialog>,
    pub(crate) search: SearchState,
    keep_unread: Option<String>,
    people_serial: u64,
}

impl ThreadOps {
    pub(crate) fn on_thread_changed(&mut self) {
        self.menu = None;
        self.rename = None;
        self.confirm = None;
        self.keep_unread = None;
        if self
            .people
            .as_ref()
            .is_some_and(|dialog| dialog.mode == PeopleMode::Add)
        {
            self.people = None;
        }
    }

    pub(crate) fn reset(&mut self) {
        let people_serial = self.people_serial;
        *self = Self::default();
        self.people_serial = people_serial;
    }
}

fn is_inbox_folder(folder: &str) -> bool {
    !matches!(folder, "requests" | "spam" | "archived")
}

fn is_request(thread: &Thread) -> bool {
    matches!(thread.folder.as_str(), "requests" | "spam")
}

fn thread_label(thread: &Thread) -> String {
    if thread.name.trim().is_empty() {
        t!("thread_ops.conversation").into_owned()
    } else {
        thread.name.clone()
    }
}

fn now_ms() -> i64 {
    chrono::Utc::now().timestamp_millis()
}

fn mute_until_ms(hours: Option<i64>) -> i64 {
    match hours {
        Some(hours) => now_ms() + hours * HOUR_MS,
        None => -1,
    }
}

fn mute_label(thread: &Thread, muted: bool) -> Option<String> {
    if !muted {
        return None;
    }
    match thread.muted_until_ms {
        Some(until) if until > now_ms() => {
            Some(t!("thread_ops.mute.until", until = format_until(until)).into_owned())
        }
        _ => Some(t!("thread_ops.mute.until_unmuted").into_owned()),
    }
}

fn format_until(timestamp_ms: i64) -> String {
    let Some(time) = Local.timestamp_millis_opt(timestamp_ms).single() else {
        return String::new();
    };
    let days = (time.date_naive() - Local::now().date_naive()).num_days();
    let clock = crate::timefmt::clock(&time);
    match days {
        0 => clock,
        1 => t!("thread_ops.until.tomorrow", time = clock).into_owned(),
        _ => t!(
            "thread_ops.until.date",
            date = crate::timefmt::day_month_numeric(&time),
            time = clock
        )
        .into_owned(),
    }
}

fn ephemeral_label(ttl_ms: i64) -> String {
    let seconds = (ttl_ms / 1000).max(1);
    if seconds > 86_400 && seconds % 86_400 == 0 {
        tn!("thread_ops.ephemeral.days", seconds / 86_400).into_owned()
    } else if seconds >= 3_600 && seconds % 3_600 == 0 {
        format!("{} h", seconds / 3_600)
    } else if seconds >= 60 {
        format!("{} min", seconds / 60)
    } else {
        format!("{seconds} s")
    }
}

fn is_image_path(path: &std::path::Path) -> bool {
    path.extension()
        .and_then(|ext| ext.to_str())
        .is_some_and(|ext| IMAGE_EXTENSIONS.contains(&ext.to_ascii_lowercase().as_str()))
}

impl MissiveApp {
    pub(crate) fn is_request_thread(&self, thread: &Thread) -> bool {
        is_request(thread)
    }

    pub(crate) fn is_inbox_thread(&self, thread: &Thread) -> bool {
        is_inbox_folder(&thread.folder)
    }

    pub(crate) fn thread_mute_label(&self, thread: &Thread) -> Option<String> {
        mute_label(thread, self.thread_is_muted(thread))
    }

    pub(crate) fn thread_overlay_enter(
        &mut self,
        window: &mut Window,
        cx: &mut Context<Self>,
    ) -> bool {
        if self.thread_ops.menu.is_some() {
            return true;
        }
        if self.thread_ops.search.panel.is_some() {
            self.search_panel_enter(window, cx);
            return true;
        }
        if self.thread_ops.confirm.is_some() {
            self.run_confirm(cx);
            return true;
        }
        if self.thread_ops.rename.is_some() {
            self.submit_rename(cx);
            return true;
        }
        if self.thread_ops.folder_dialog.is_some() {
            self.submit_folder_dialog(cx);
            return true;
        }
        if self.exports.dialog.is_some() {
            self.export_enter(cx);
            return true;
        }
        let Some(dialog) = self.thread_ops.people.as_ref() else {
            return false;
        };
        let in_name = dialog.mode == PeopleMode::Group
            && dialog
                .name_input
                .read(cx)
                .focus_handle(cx)
                .is_focused(window);
        if in_name {
            self.submit_people(window, cx);
        } else {
            self.people_enter(window, cx);
        }
        true
    }

    pub(crate) fn thread_ephemeral_label(&self, thread: &Thread) -> Option<String> {
        thread
            .ephemeral_ttl_ms
            .filter(|ttl| *ttl > 0)
            .map(ephemeral_label)
    }

    pub(crate) fn mute_thread_for(
        &mut self,
        thread_id: &str,
        hours: Option<i64>,
        cx: &mut Context<Self>,
    ) {
        self.mute_thread(thread_id, mute_until_ms(hours), cx);
    }

    pub(crate) fn unmute_thread(&mut self, thread_id: &str, cx: &mut Context<Self>) {
        self.mute_thread(thread_id, 0, cx);
    }

    pub(crate) fn thread_matches_filter(&self, thread: &Thread, filter: Filter) -> bool {
        if let (Filter::All | Filter::Unread | Filter::Groups, Some(folder)) =
            (filter, self.active_folder())
        {
            let inside = thread.folder != "spam" && folder.thread_ids.contains(&thread.id);
            return inside
                && match filter {
                    Filter::Unread => self.has_unread(thread),
                    Filter::Groups => thread.is_group,
                    _ => true,
                };
        }
        let archived = self.is_locally_archived(thread);
        let inbox = is_inbox_folder(&thread.folder) && !archived;
        match filter {
            Filter::All => inbox,
            Filter::Unread => inbox && self.has_unread(thread),
            Filter::Groups => inbox && thread.is_group,
            Filter::Requests => is_request(thread),
            Filter::Archived => thread.folder == "archived" || (archived && !is_request(thread)),
        }
    }

    pub(crate) fn close_thread_dialogs(&mut self, cx: &mut Context<Self>) -> bool {
        let ops = &mut self.thread_ops;
        let closed = ops.menu.take().is_some()
            || ops.search.panel.take().is_some()
            || ops.confirm.take().is_some()
            || ops.folder_dialog.take().is_some()
            || ops.rename.take().is_some()
            || ops.people.take().is_some();
        let closed = closed || {
            let export = self.exports.dialog.is_some();
            if export {
                self.close_export(cx);
            }
            export
        };
        if closed {
            cx.notify();
        }
        closed
    }

    pub(crate) fn is_locally_unread(&self, thread: &Thread) -> bool {
        let viewing = self.selected.as_deref() == Some(thread.id.as_str())
            && self.thread_ops.keep_unread.as_deref() != Some(thread.id.as_str());
        !viewing && self.settings.locally_unread.contains(&thread.id)
    }

    pub(crate) fn is_locally_archived(&self, thread: &Thread) -> bool {
        self.settings
            .locally_archived
            .get(&thread.id)
            .is_some_and(|at| thread.last_activity_ms <= *at)
    }

    pub(crate) fn has_unread(&self, thread: &Thread) -> bool {
        thread.unread_count > 0 || self.is_locally_unread(thread)
    }

    pub(crate) fn folder_count(&self, filter: Filter) -> usize {
        self.threads
            .iter()
            .filter(|thread| self.thread_matches_filter(thread, filter))
            .count()
    }

    pub(crate) fn needs_local_sweep(&self) -> bool {
        let selected_unread = self.selected.as_ref().is_some_and(|id| {
            self.thread_ops.keep_unread.as_ref() != Some(id)
                && self.settings.locally_unread.contains(id)
        });
        selected_unread
            || self.settings.locally_archived.iter().any(|(id, at)| {
                self.threads
                    .iter()
                    .any(|thread| thread.id == *id && thread.last_activity_ms > *at)
            })
    }

    pub(crate) fn sweep_local_state(&mut self, cx: &mut Context<Self>) {
        let before = (
            self.settings.locally_unread.len(),
            self.settings.locally_archived.len(),
        );
        if let Some(selected) = self.selected.clone()
            && self.thread_ops.keep_unread.as_ref() != Some(&selected) {
                self.settings.locally_unread.retain(|id| *id != selected);
            }
        let threads = &self.threads;
        self.settings.locally_archived.retain(|id, at| {
            !threads
                .iter()
                .any(|thread| thread.id == *id && thread.last_activity_ms > *at)
        });
        let after = (
            self.settings.locally_unread.len(),
            self.settings.locally_archived.len(),
        );
        if before != after {
            self.mark_settings_dirty();
            cx.notify();
        }
    }

    fn thread_mut(&mut self, thread_id: &str) -> Option<&mut Thread> {
        self.threads
            .iter_mut()
            .find(|thread| thread.id == thread_id)
    }

    fn thread_by_id(&self, thread_id: &str) -> Option<&Thread> {
        self.threads.iter().find(|thread| thread.id == thread_id)
    }

    fn thread_command(
        &mut self,
        cmd: &'static str,
        params: Value,
        success: Option<SharedString>,
        rollback: impl FnOnce(&mut Self) + 'static,
        cx: &mut Context<Self>,
    ) {
        let Some(bridge) = self.bridge() else {
            rollback(self);
            cx.notify();
            return;
        };
        cx.notify();
        cx.spawn(async move |this, cx| {
            let result = bridge.request(cmd, params).await;
            let _ = this.update(cx, |this, cx| match result {
                Ok(_) => {
                    if let Some(text) = success {
                        this.show_toast(text, cx);
                    }
                }
                Err(err) => {
                    rollback(this);
                    this.show_toast(action_error(&err), cx);
                }
            });
        })
        .detach();
    }

    pub(crate) fn open_thread_context_menu(
        &mut self,
        thread_id: &str,
        position: Point<Pixels>,
        cx: &mut Context<Self>,
    ) {
        let kind = MenuKind::Thread {
            thread_id: thread_id.to_string(),
        };
        self.open_thread_menu(kind, position, cx);
    }

    pub(crate) fn open_mute_menu(
        &mut self,
        thread_id: &str,
        position: Point<Pixels>,
        cx: &mut Context<Self>,
    ) {
        let kind = MenuKind::Mute {
            thread_id: thread_id.to_string(),
        };
        self.open_thread_menu(kind, position, cx);
    }

    pub(crate) fn open_member_menu(
        &mut self,
        thread_id: &str,
        user_id: &str,
        position: Point<Pixels>,
        cx: &mut Context<Self>,
    ) {
        let kind = MenuKind::Member {
            thread_id: thread_id.to_string(),
            user_id: user_id.to_string(),
        };
        self.open_thread_menu(kind, position, cx);
    }

    pub(crate) fn show_mute_submenu(&mut self, cx: &mut Context<Self>) {
        if let Some(menu) = self.thread_ops.menu.as_mut() {
            menu.kind = MenuKind::Mute {
                thread_id: menu.kind.thread_id().to_string(),
            };
            cx.notify();
        }
    }

    fn open_thread_menu(
        &mut self,
        kind: MenuKind,
        position: Point<Pixels>,
        cx: &mut Context<Self>,
    ) {
        self.thread_ops.menu = Some(ThreadMenu { kind, position });
        cx.notify();
    }

    pub(crate) fn close_thread_menu(&mut self, cx: &mut Context<Self>) {
        if self.thread_ops.menu.take().is_some() {
            cx.notify();
        }
    }

    pub(crate) fn set_local_unread(
        &mut self,
        thread_id: &str,
        unread: bool,
        cx: &mut Context<Self>,
    ) {
        self.thread_ops.menu = None;
        if unread {
            if !self
                .settings
                .locally_unread
                .iter()
                .any(|id| id == thread_id)
            {
                self.settings.locally_unread.push(thread_id.to_string());
            }
            if self.selected.as_deref() == Some(thread_id) {
                self.thread_ops.keep_unread = Some(thread_id.to_string());
            }
        } else {
            self.settings.locally_unread.retain(|id| id != thread_id);
            if self.thread_ops.keep_unread.as_deref() == Some(thread_id) {
                self.thread_ops.keep_unread = None;
            }
            let request = self.thread_by_id(thread_id).is_some_and(is_request);
            if !request {
                self.mark_thread_read(thread_id, cx);
            }
        }
        self.mark_settings_dirty();
        cx.notify();
    }

    pub(crate) fn set_local_archived(
        &mut self,
        thread_id: &str,
        archived: bool,
        cx: &mut Context<Self>,
    ) {
        self.thread_ops.menu = None;
        if archived {
            let at = self
                .thread_by_id(thread_id)
                .map(|thread| thread.last_activity_ms)
                .unwrap_or_else(now_ms);
            self.settings
                .locally_archived
                .insert(thread_id.to_string(), at);
            self.show_toast(t!("thread_ops.toast.archived"), cx);
        } else {
            self.settings.locally_archived.remove(thread_id);
            self.show_toast(t!("thread_ops.toast.unarchived"), cx);
        }
        self.mark_settings_dirty();
        if self.filter == Filter::Archived && self.folder_count(Filter::Archived) == 0 {
            self.filter = Filter::All;
        }
        cx.notify();
    }

    pub(crate) fn unarchive_all_local(&mut self, cx: &mut Context<Self>) {
        if self.settings.locally_archived.is_empty() {
            return;
        }
        self.settings.locally_archived.clear();
        self.mark_settings_dirty();
        if self.filter == Filter::Archived && self.folder_count(Filter::Archived) == 0 {
            self.filter = Filter::All;
        }
        cx.notify();
    }

    pub(crate) fn locally_archived_count(&self) -> usize {
        self.threads
            .iter()
            .filter(|thread| self.is_locally_archived(thread))
            .count()
    }

    pub(crate) fn mute_thread(&mut self, thread_id: &str, until_ms: i64, cx: &mut Context<Self>) {
        self.thread_ops.menu = None;
        let Some(thread) = self.thread_mut(thread_id) else {
            return;
        };
        let previous = (thread.muted, thread.muted_until_ms);
        thread.muted = until_ms != 0;
        thread.muted_until_ms = (until_ms != 0).then_some(until_ms);
        let id = thread_id.to_string();
        let success = if until_ms == 0 {
            ts!("thread_ops.toast.unmuted")
        } else {
            ts!("thread_ops.toast.muted")
        };
        self.thread_command(
            "mute",
            json!({ "thread_id": id, "until_ms": until_ms }),
            Some(success),
            move |this| {
                if let Some(thread) = this.thread_mut(&id) {
                    thread.muted = previous.0;
                    thread.muted_until_ms = previous.1;
                }
            },
            cx,
        );
    }

    pub(crate) fn accept_request(&mut self, thread_id: &str, cx: &mut Context<Self>) {
        self.thread_ops.menu = None;
        let Some(thread) = self.thread_mut(thread_id) else {
            return;
        };
        let previous = std::mem::replace(&mut thread.folder, "inbox".to_string());
        if self.filter == Filter::Requests && self.folder_count(Filter::Requests) == 0 {
            self.filter = Filter::All;
        }
        let id = thread_id.to_string();
        self.thread_command(
            "accept_request",
            json!({ "thread_id": id }),
            Some(ts!("thread_ops.toast.request_accepted")),
            move |this| {
                if let Some(thread) = this.thread_mut(&id) {
                    thread.folder = previous;
                }
            },
            cx,
        );
    }

    pub(crate) fn confirm_delete_thread(&mut self, thread_id: &str, cx: &mut Context<Self>) {
        let thread_id = thread_id.to_string();
        self.ask_confirm(ConfirmAction::DeleteThread { thread_id }, cx);
    }

    pub(crate) fn confirm_leave_group(&mut self, thread_id: &str, cx: &mut Context<Self>) {
        let thread_id = thread_id.to_string();
        self.ask_confirm(ConfirmAction::LeaveGroup { thread_id }, cx);
    }

    pub(crate) fn confirm_block(&mut self, user_id: &str, blocked: bool, cx: &mut Context<Self>) {
        let user_id = user_id.to_string();
        self.ask_confirm(ConfirmAction::Block { user_id, blocked }, cx);
    }

    pub(crate) fn confirm_remove_member(
        &mut self,
        thread_id: &str,
        user_id: &str,
        cx: &mut Context<Self>,
    ) {
        let (thread_id, user_id) = (thread_id.to_string(), user_id.to_string());
        self.ask_confirm(ConfirmAction::RemoveMember { thread_id, user_id }, cx);
    }

    fn ask_confirm(&mut self, action: ConfirmAction, cx: &mut Context<Self>) {
        self.thread_ops.menu = None;
        let (title, text, button, danger) = match &action {
            ConfirmAction::DeleteThread { thread_id } => {
                let name = self
                    .thread_by_id(thread_id)
                    .map(thread_label)
                    .unwrap_or_default();
                (
                    t!("thread_ops.confirm.delete_thread.title"),
                    t!("thread_ops.confirm.delete_thread.text", name = name),
                    t!("thread_ops.confirm.delete_thread.button"),
                    true,
                )
            }
            ConfirmAction::LeaveGroup { thread_id } => {
                let name = self
                    .thread_by_id(thread_id)
                    .map(thread_label)
                    .unwrap_or_default();
                (
                    t!("thread_ops.confirm.leave_group.title"),
                    t!("thread_ops.confirm.leave_group.text", name = name),
                    t!("thread_ops.confirm.leave_group.button"),
                    true,
                )
            }
            ConfirmAction::Block { user_id, blocked } => {
                let name = self
                    .threads
                    .iter()
                    .find(|thread| !thread.is_group && thread.peer_id.as_deref() == Some(user_id))
                    .map(thread_label)
                    .unwrap_or_else(|| t!("thread_ops.confirm.block.fallback_name").into_owned());
                if *blocked {
                    (
                        t!("thread_ops.confirm.block.title", name = name),
                        t!("thread_ops.confirm.block.text"),
                        t!("thread_ops.confirm.block.button"),
                        true,
                    )
                } else {
                    (
                        t!("thread_ops.confirm.unblock.title", name = name),
                        t!("thread_ops.confirm.unblock.text"),
                        t!("thread_ops.confirm.unblock.button"),
                        false,
                    )
                }
            }
            ConfirmAction::DeleteFolder { folder_id } => {
                let name = self
                    .settings
                    .folders
                    .iter()
                    .find(|folder| folder.id == *folder_id)
                    .map(|folder| folder.name.clone())
                    .unwrap_or_default();
                (
                    t!("thread_ops.confirm.delete_folder.title", name = name),
                    t!("thread_ops.confirm.delete_folder.text"),
                    t!("thread_ops.confirm.delete_folder.button"),
                    true,
                )
            }
            ConfirmAction::RemoveMember { thread_id, user_id } => {
                let thread = self.thread_by_id(thread_id);
                let group = thread.map(thread_label).unwrap_or_default();
                let member = thread
                    .and_then(|thread| {
                        thread
                            .participants
                            .iter()
                            .find(|participant| participant.user_id == *user_id)
                    })
                    .map(|participant| participant.name.clone())
                    .filter(|name| !name.is_empty())
                    .unwrap_or_else(|| t!("thread_ops.confirm.remove_member.fallback_name").into_owned());
                (
                    t!("thread_ops.confirm.remove_member.title", member = member),
                    t!("thread_ops.confirm.remove_member.text", member = member, group = group),
                    t!("thread_ops.confirm.remove_member.button"),
                    true,
                )
            }
        };
        self.thread_ops.confirm = Some(Confirm {
            title: title.into(),
            text: text.into(),
            button: button.into(),
            danger,
            action,
        });
        cx.notify();
    }

    pub(crate) fn cancel_confirm(&mut self, cx: &mut Context<Self>) {
        if self.thread_ops.confirm.take().is_some() {
            cx.notify();
        }
    }

    pub(crate) fn run_confirm(&mut self, cx: &mut Context<Self>) {
        let Some(confirm) = self.thread_ops.confirm.take() else {
            return;
        };
        match confirm.action {
            ConfirmAction::DeleteThread { thread_id } => self.delete_thread(thread_id, cx),
            ConfirmAction::LeaveGroup { thread_id } => self.leave_thread(thread_id, cx),
            ConfirmAction::Block { user_id, blocked } => self.block_user(user_id, blocked, cx),
            ConfirmAction::RemoveMember { thread_id, user_id } => {
                self.remove_member(thread_id, user_id, cx)
            }
            ConfirmAction::DeleteFolder { folder_id } => self.delete_folder(&folder_id, cx),
        }
        cx.notify();
    }

    fn delete_thread(&mut self, thread_id: String, cx: &mut Context<Self>) {
        let Some(bridge) = self.bridge() else { return };
        cx.spawn(async move |this, cx| {
            let result = bridge
                .request("delete_thread", json!({ "thread_id": thread_id }))
                .await;
            let _ = this.update(cx, |this, cx| match result {
                Ok(_) => {
                    this.forget_thread(&thread_id, cx);
                    this.show_toast(t!("thread_ops.toast.thread_deleted"), cx);
                }
                Err(err) => this.show_toast(action_error(&err), cx),
            });
        })
        .detach();
    }

    fn forget_thread(&mut self, thread_id: &str, cx: &mut Context<Self>) {
        if self.selected.as_deref() == Some(thread_id) {
            self.clear_selection(cx);
        }
        self.threads.retain(|thread| thread.id != thread_id);
        self.messages.remove(thread_id);
        let before = (
            self.settings.locally_unread.len(),
            self.settings.locally_archived.len(),
        );
        self.settings.locally_unread.retain(|id| id != thread_id);
        self.settings.locally_archived.remove(thread_id);
        let mut changed = false;
        for folder in self.settings.folders.iter_mut() {
            let count = folder.thread_ids.len();
            folder.thread_ids.retain(|id| id != thread_id);
            changed |= folder.thread_ids.len() != count;
        }
        if changed
            || before
                != (
                    self.settings.locally_unread.len(),
                    self.settings.locally_archived.len(),
                )
        {
            self.mark_settings_dirty();
        }
    }

    fn leave_thread(&mut self, thread_id: String, cx: &mut Context<Self>) {
        self.thread_command(
            "leave_thread",
            json!({ "thread_id": thread_id }),
            Some(ts!("thread_ops.toast.left_group")),
            |_| {},
            cx,
        );
    }

    fn block_user(&mut self, user_id: String, blocked: bool, cx: &mut Context<Self>) {
        let mut changed = Vec::new();
        for thread in self.threads.iter_mut() {
            if !thread.is_group && thread.peer_id.as_deref() == Some(user_id.as_str()) {
                changed.push((thread.id.clone(), thread.blocked));
                thread.blocked = blocked;
            }
        }
        self.thread_command(
            "block_user",
            json!({ "user_id": user_id, "blocked": blocked }),
            Some(if blocked {
                ts!("thread_ops.toast.blocked")
            } else {
                ts!("thread_ops.toast.unblocked")
            }),
            move |this| {
                for (id, previous) in changed {
                    if let Some(thread) = this.thread_mut(&id) {
                        thread.blocked = previous;
                    }
                }
            },
            cx,
        );
    }

    fn remove_member(&mut self, thread_id: String, user_id: String, cx: &mut Context<Self>) {
        let removed: Option<(usize, Participant)> =
            self.thread_mut(&thread_id).and_then(|thread| {
                let index = thread
                    .participants
                    .iter()
                    .position(|participant| participant.user_id == user_id)?;
                Some((index, thread.participants.remove(index)))
            });
        let id = thread_id.clone();
        self.thread_command(
            "remove_participant",
            json!({ "thread_id": thread_id, "user_id": user_id }),
            Some(ts!("thread_ops.toast.member_removed")),
            move |this| {
                if let (Some((index, participant)), Some(thread)) = (removed, this.thread_mut(&id))
                {
                    let index = index.min(thread.participants.len());
                    thread.participants.insert(index, participant);
                }
            },
            cx,
        );
    }

    pub(crate) fn set_admin(
        &mut self,
        thread_id: &str,
        user_id: &str,
        admin: bool,
        cx: &mut Context<Self>,
    ) {
        self.thread_ops.menu = None;
        let previous = self.thread_mut(thread_id).and_then(|thread| {
            let participant = thread
                .participants
                .iter_mut()
                .find(|participant| participant.user_id == user_id)?;
            Some(std::mem::replace(&mut participant.is_admin, admin))
        });
        let (id, member) = (thread_id.to_string(), user_id.to_string());
        self.thread_command(
            "set_admin",
            json!({ "thread_id": thread_id, "user_id": user_id, "admin": admin }),
            Some(if admin {
                ts!("thread_ops.toast.admin_added")
            } else {
                ts!("thread_ops.toast.admin_removed")
            }),
            move |this| {
                let Some(previous) = previous else { return };
                if let Some(participant) = this.thread_mut(&id).and_then(|thread| {
                    thread
                        .participants
                        .iter_mut()
                        .find(|participant| participant.user_id == member)
                }) {
                    participant.is_admin = previous;
                }
            },
            cx,
        );
    }

    pub(crate) fn open_rename(
        &mut self,
        thread_id: &str,
        window: &mut Window,
        cx: &mut Context<Self>,
    ) {
        let name = self
            .thread_by_id(thread_id)
            .map(|thread| thread.name.clone())
            .unwrap_or_default();
        let input = cx.new(|cx| {
            InputState::new(window, cx)
                .placeholder(ts!("thread_ops.rename.placeholder"))
                .default_value(name)
        });
        let subscription = cx.subscribe_in(&input, window, |this, _, event: &InputEvent, _, cx| {
            if let InputEvent::PressEnter { .. } = event {
                this.submit_rename(cx);
            }
        });
        input.update(cx, |input, cx| input.focus(window, cx));
        self.thread_ops.menu = None;
        self.thread_ops.rename = Some(RenameDialog {
            thread_id: thread_id.to_string(),
            input,
            _subscription: subscription,
        });
        cx.notify();
    }

    pub(crate) fn close_rename(&mut self, cx: &mut Context<Self>) {
        if self.thread_ops.rename.take().is_some() {
            cx.notify();
        }
    }

    pub(crate) fn submit_rename(&mut self, cx: &mut Context<Self>) {
        let Some(dialog) = self.thread_ops.rename.take() else {
            return;
        };
        let name = dialog.input.read(cx).value().trim().to_string();
        let thread_id = dialog.thread_id;
        let Some(thread) = self.thread_mut(&thread_id) else {
            cx.notify();
            return;
        };
        if thread.name == name {
            cx.notify();
            return;
        }
        let previous =
            (!name.is_empty()).then(|| std::mem::replace(&mut thread.name, name.clone()));
        let id = thread_id.clone();
        self.thread_command(
            "rename_thread",
            json!({ "thread_id": thread_id, "name": name }),
            Some(ts!("thread_ops.toast.group_renamed")),
            move |this| {
                if let (Some(previous), Some(thread)) = (previous, this.thread_mut(&id)) {
                    thread.name = previous;
                }
            },
            cx,
        );
    }

    pub(crate) fn pick_thread_image(&mut self, thread_id: &str, cx: &mut Context<Self>) {
        let receiver = cx.prompt_for_paths(PathPromptOptions {
            files: true,
            directories: false,
            multiple: false,
            prompt: Some(ts!("thread_ops.pick_image.prompt")),
        });
        let thread_id = thread_id.to_string();
        cx.spawn(async move |this, cx| {
            let Ok(Ok(Some(paths))) = receiver.await else {
                return;
            };
            let Some(path) = paths.into_iter().next() else {
                return;
            };
            let _ = this.update(cx, |this, cx| this.set_thread_image(thread_id, path, cx));
        })
        .detach();
    }

    fn set_thread_image(&mut self, thread_id: String, path: PathBuf, cx: &mut Context<Self>) {
        if !is_image_path(&path) {
            self.show_toast(t!("thread_ops.toast.not_an_image"), cx);
            return;
        }
        self.thread_command(
            "set_thread_image",
            json!({ "thread_id": thread_id, "path": path.to_string_lossy() }),
            Some(ts!("thread_ops.toast.group_photo_updated")),
            |_| {},
            cx,
        );
    }

    pub(crate) fn open_new_chat(&mut self, window: &mut Window, cx: &mut Context<Self>) {
        self.open_people(PeopleMode::Direct, None, window, cx);
    }

    pub(crate) fn open_add_people(
        &mut self,
        thread_id: &str,
        window: &mut Window,
        cx: &mut Context<Self>,
    ) {
        self.open_people(PeopleMode::Add, Some(thread_id.to_string()), window, cx);
    }

    fn open_people(
        &mut self,
        mode: PeopleMode,
        thread_id: Option<String>,
        window: &mut Window,
        cx: &mut Context<Self>,
    ) {
        let input =
            cx.new(|cx| InputState::new(window, cx).placeholder(ts!("thread_ops.people.search_placeholder")));
        let name_input =
            cx.new(|cx| InputState::new(window, cx).placeholder(ts!("thread_ops.people.name_placeholder")));
        let subscriptions =
            vec![
                cx.subscribe_in(&input, window, |this, _, event: &InputEvent, window, cx| {
                    match event {
                        InputEvent::Change => this.on_people_query(cx),
                        InputEvent::PressEnter { .. } => this.people_enter(window, cx),
                        _ => {}
                    }
                }),
                cx.subscribe_in(
                    &name_input,
                    window,
                    |this, _, event: &InputEvent, window, cx| {
                        if let InputEvent::PressEnter { .. } = event {
                            this.submit_people(window, cx);
                        }
                    },
                ),
            ];
        input.update(cx, |input, cx| input.focus(window, cx));
        self.thread_ops.menu = None;
        self.thread_ops.people_serial += 1;
        self.thread_ops.people = Some(PeopleDialog {
            mode,
            thread_id,
            input,
            name_input,
            query: String::new(),
            results: Vec::new(),
            results_query: String::new(),
            searching: false,
            selected: Vec::new(),
            busy: false,
            serial: self.thread_ops.people_serial,
            _task: None,
            _subscriptions: subscriptions,
        });
        cx.notify();
    }

    pub(crate) fn close_people(&mut self, cx: &mut Context<Self>) {
        if self.thread_ops.people.take().is_some() {
            cx.notify();
        }
    }

    pub(crate) fn set_people_group(&mut self, group: bool, cx: &mut Context<Self>) {
        let Some(dialog) = self.thread_ops.people.as_mut() else {
            return;
        };
        if dialog.mode == PeopleMode::Add {
            return;
        }
        dialog.mode = if group {
            PeopleMode::Group
        } else {
            PeopleMode::Direct
        };
        if !group {
            dialog.selected.clear();
        }
        cx.notify();
    }

    fn on_people_query(&mut self, cx: &mut Context<Self>) {
        let bridge = self.bridge();
        let Some(dialog) = self.thread_ops.people.as_mut() else {
            return;
        };
        let query = dialog.input.read(cx).value().trim().to_string();
        if query == dialog.query {
            return;
        }
        dialog.query = query.clone();
        dialog.results.clear();
        dialog.results_query.clear();
        if query.is_empty() {
            dialog.searching = false;
            dialog._task = None;
            cx.notify();
            return;
        }
        let Some(bridge) = bridge else { return };
        dialog.searching = true;
        let serial = dialog.serial;
        dialog._task = Some(cx.spawn(async move |this, cx| {
            cx.background_executor().timer(PEOPLE_DEBOUNCE).await;
            let result = bridge
                .call::<Vec<UserResult>>("search_users", json!({ "query": query, "limit": 20 }))
                .await;
            let _ = this.update(cx, |this, cx| {
                let me = this.user.as_ref().map(|user| user.id.clone());
                let Some(dialog) = this
                    .thread_ops
                    .people
                    .as_mut()
                    .filter(|dialog| dialog.serial == serial)
                else {
                    return;
                };
                if dialog.query != query {
                    return;
                }
                dialog.searching = false;
                let error = match result {
                    Ok(results) => {
                        dialog.results = results
                            .into_iter()
                            .filter(|result| Some(&result.id) != me.as_ref())
                            .collect();
                        dialog.results_query = query.clone();
                        None
                    }
                    Err(err) => {
                        dialog.results.clear();
                        dialog.results_query.clear();
                        Some(action_error(&err))
                    }
                };
                match error {
                    Some(error) => this.show_toast(error, cx),
                    None => cx.notify(),
                }
            });
        }));
        cx.notify();
    }

    pub(crate) fn people_candidates(&self) -> Vec<UserResult> {
        let Some(dialog) = self.thread_ops.people.as_ref() else {
            return Vec::new();
        };
        let excluded: Vec<&str> = dialog
            .thread_id
            .as_deref()
            .and_then(|id| self.thread_by_id(id))
            .map(|thread| {
                thread
                    .participants
                    .iter()
                    .map(|participant| participant.user_id.as_str())
                    .collect()
            })
            .unwrap_or_default();
        let me = self.user.as_ref().map(|user| user.id.as_str());
        let keep = |user: &UserResult| {
            Some(user.id.as_str()) != me && !excluded.contains(&user.id.as_str())
        };
        if !dialog.query.is_empty() {
            if dialog.results_query != dialog.query {
                return Vec::new();
            }
            return dialog
                .results
                .iter()
                .filter(|user| keep(user))
                .cloned()
                .collect();
        }
        self.threads
            .iter()
            .filter(|thread| !thread.is_group && is_inbox_folder(&thread.folder))
            .filter_map(|thread| {
                Some(UserResult {
                    id: thread.peer_id.clone()?,
                    name: thread_label(thread),
                    avatar_url: thread.avatar_url.clone(),
                    thread_id: Some(thread.id.clone()),
                    is_friend: false,
                })
            })
            .filter(|user| keep(user))
            .take(8)
            .collect()
    }

    fn people_enter(&mut self, window: &mut Window, cx: &mut Context<Self>) {
        let Some(dialog) = self.thread_ops.people.as_ref() else {
            return;
        };
        if dialog.busy || dialog.searching {
            return;
        }
        let mode = dialog.mode;
        if let Some(first) = self.people_candidates().into_iter().next() {
            match mode {
                PeopleMode::Direct => self.open_direct(first, window, cx),
                PeopleMode::Group | PeopleMode::Add => self.toggle_person(first, cx),
            }
        }
    }

    pub(crate) fn pick_person(
        &mut self,
        user_id: &str,
        window: &mut Window,
        cx: &mut Context<Self>,
    ) {
        let Some(mode) = self.thread_ops.people.as_ref().map(|dialog| dialog.mode) else {
            return;
        };
        let Some(user) = self
            .people_candidates()
            .into_iter()
            .find(|user| user.id == user_id)
        else {
            return;
        };
        match mode {
            PeopleMode::Direct => self.open_direct(user, window, cx),
            PeopleMode::Group | PeopleMode::Add => self.toggle_person(user, cx),
        }
    }

    pub(crate) fn toggle_person(&mut self, user: UserResult, cx: &mut Context<Self>) {
        let Some(dialog) = self.thread_ops.people.as_mut() else {
            return;
        };
        match dialog
            .selected
            .iter()
            .position(|selected| selected.id == user.id)
        {
            Some(index) => {
                dialog.selected.remove(index);
            }
            None => dialog.selected.push(user),
        }
        cx.notify();
    }

    fn open_direct(&mut self, user: UserResult, window: &mut Window, cx: &mut Context<Self>) {
        let known = user
            .thread_id
            .clone()
            .filter(|id| self.threads.iter().any(|thread| thread.id == *id));
        if let Some(thread_id) = known {
            self.thread_ops.people = None;
            self.reveal_thread(&thread_id);
            self.select_thread(thread_id, window, cx);
            return;
        }
        let Some(bridge) = self.bridge() else { return };
        let Some(dialog) = self.thread_ops.people.as_mut() else {
            return;
        };
        if dialog.busy {
            return;
        }
        dialog.busy = true;
        let serial = dialog.serial;
        cx.notify();
        cx.spawn_in(window, async move |this, cx| {
            let result = bridge
                .call::<Thread>("open_thread", json!({ "user_id": user.id }))
                .await;
            let _ = this.update_in(cx, |this, window, cx| {
                let current = this.finish_people_request(serial);
                match result {
                    Ok(thread) if current => {
                        this.thread_ops.people = None;
                        this.adopt_and_select(thread, window, cx);
                    }
                    Ok(thread) => this.adopt_thread(thread),
                    Err(err) => this.show_toast(action_error(&err), cx),
                }
                cx.notify();
            });
        })
        .detach();
    }

    pub(crate) fn submit_people(&mut self, window: &mut Window, cx: &mut Context<Self>) {
        let Some(dialog) = self.thread_ops.people.as_ref() else {
            return;
        };
        if dialog.busy {
            return;
        }
        let ids: Vec<String> = dialog.selected.iter().map(|user| user.id.clone()).collect();
        match dialog.mode {
            PeopleMode::Direct => {}
            PeopleMode::Group => {
                if ids.len() < 2 {
                    self.show_toast(t!("thread_ops.toast.pick_two_people"), cx);
                    return;
                }
                let name = dialog.name_input.read(cx).value().trim().to_string();
                let mut params = json!({ "user_ids": ids });
                if !name.is_empty() {
                    params["name"] = json!(name);
                }
                let Some(bridge) = self.bridge() else { return };
                let serial = dialog.serial;
                self.set_people_busy(true);
                cx.notify();
                cx.spawn_in(window, async move |this, cx| {
                    let result = bridge.call::<Thread>("create_group", params).await;
                    let _ = this.update_in(cx, |this, window, cx| {
                        let current = this.finish_people_request(serial);
                        match result {
                            Ok(thread) => {
                                if current {
                                    this.thread_ops.people = None;
                                    this.adopt_and_select(thread, window, cx);
                                } else {
                                    this.adopt_thread(thread);
                                }
                                this.show_toast(t!("thread_ops.toast.group_created"), cx);
                            }
                            Err(err) => this.show_toast(action_error(&err), cx),
                        }
                        cx.notify();
                    });
                })
                .detach();
            }
            PeopleMode::Add => {
                let Some(thread_id) = dialog.thread_id.clone() else {
                    return;
                };
                if ids.is_empty() {
                    return;
                }
                let Some(bridge) = self.bridge() else { return };
                let serial = dialog.serial;
                self.set_people_busy(true);
                cx.notify();
                cx.spawn(async move |this, cx| {
                    let result = bridge
                        .request(
                            "add_participants",
                            json!({ "thread_id": thread_id, "user_ids": ids }),
                        )
                        .await;
                    let _ = this.update(cx, |this, cx| {
                        let current = this.finish_people_request(serial);
                        match result {
                            Ok(_) => {
                                if current {
                                    this.thread_ops.people = None;
                                }
                                this.show_toast(
                                    if ids.len() > 1 {
                                        t!("thread_ops.toast.people_added")
                                    } else {
                                        t!("thread_ops.toast.person_added")
                                    },
                                    cx,
                                );
                            }
                            Err(err) => this.show_toast(action_error(&err), cx),
                        }
                        cx.notify();
                    });
                })
                .detach();
            }
        }
    }

    fn set_people_busy(&mut self, busy: bool) {
        if let Some(dialog) = self.thread_ops.people.as_mut() {
            dialog.busy = busy;
        }
    }

    fn finish_people_request(&mut self, serial: u64) -> bool {
        match self.thread_ops.people.as_mut() {
            Some(dialog) if dialog.serial == serial => {
                dialog.busy = false;
                true
            }
            _ => false,
        }
    }

    fn adopt_thread(&mut self, thread: Thread) {
        let thread_id = thread.id.clone();
        match self.thread_mut(&thread_id) {
            Some(existing) => *existing = thread,
            None => self.threads.push(thread),
        }
        self.sort_threads();
    }

    fn adopt_and_select(&mut self, thread: Thread, window: &mut Window, cx: &mut Context<Self>) {
        let thread_id = thread.id.clone();
        self.adopt_thread(thread);
        self.reveal_thread(&thread_id);
        self.select_thread(thread_id, window, cx);
    }

    fn reveal_thread(&mut self, thread_id: &str) {
        if self.settings.locally_archived.remove(thread_id).is_some() {
            self.mark_settings_dirty();
        }
        let Some(thread) = self.thread_by_id(thread_id) else {
            return;
        };
        if self.thread_matches_filter(thread, self.filter) {
            return;
        }
        if self.thread_ops.folder.take().is_some()
            && self
                .thread_by_id(thread_id)
                .is_some_and(|thread| self.thread_matches_filter(thread, self.filter))
        {
            return;
        }
        let Some(thread) = self.thread_by_id(thread_id) else {
            return;
        };
        self.filter = if is_request(thread) {
            Filter::Requests
        } else if thread.folder == "archived" {
            Filter::Archived
        } else {
            Filter::All
        };
    }
}

impl MissiveApp {
    pub(crate) fn active_folder(&self) -> Option<&Folder> {
        let id = self.thread_ops.folder.as_deref()?;
        self.settings.folders.iter().find(|folder| folder.id == id)
    }

    pub(crate) fn select_filter_tab(&mut self, filter: Filter, cx: &mut Context<Self>) {
        self.thread_ops.folder = None;
        self.set_filter(filter, cx);
    }

    pub(crate) fn open_folder(&mut self, folder_id: &str, cx: &mut Context<Self>) {
        self.thread_ops.folder = Some(folder_id.to_string());
        self.set_filter(Filter::All, cx);
    }

    pub(crate) fn is_priority_thread(&self, thread_id: &str) -> bool {
        self.settings.priority_threads.iter().any(|id| id == thread_id)
    }

    pub(crate) fn toggle_priority(&mut self, thread_id: &str, cx: &mut Context<Self>) {
        self.thread_ops.menu = None;
        if self.is_priority_thread(thread_id) {
            self.settings.priority_threads.retain(|id| id != thread_id);
            self.show_toast(t!("thread_ops.toast.priority_removed"), cx);
        } else {
            self.settings.priority_threads.push(thread_id.to_string());
            self.show_toast(t!("thread_ops.toast.priority_added"), cx);
        }
        self.mark_settings_dirty();
        cx.notify();
    }

    pub(crate) fn show_folder_submenu(&mut self, cx: &mut Context<Self>) {
        if let Some(menu) = self.thread_ops.menu.as_mut() {
            menu.kind = MenuKind::Folders {
                thread_id: menu.kind.thread_id().to_string(),
            };
            cx.notify();
        }
    }

    pub(crate) fn open_folder_menu(
        &mut self,
        folder_id: &str,
        position: Point<Pixels>,
        cx: &mut Context<Self>,
    ) {
        let kind = MenuKind::FolderTab {
            folder_id: folder_id.to_string(),
        };
        self.open_thread_menu(kind, position, cx);
    }

    pub(crate) fn set_thread_in_folder(
        &mut self,
        thread_id: &str,
        folder_id: &str,
        inside: bool,
        cx: &mut Context<Self>,
    ) {
        self.thread_ops.menu = None;
        let Some(folder) = self
            .settings
            .folders
            .iter_mut()
            .find(|folder| folder.id == folder_id)
        else {
            return;
        };
        let present = folder.thread_ids.iter().any(|id| id == thread_id);
        let name = folder.name.clone();
        if inside && !present {
            folder.thread_ids.push(thread_id.to_string());
            self.mark_settings_dirty();
            self.show_toast(t!("thread_ops.toast.added_to_folder", name = name), cx);
        } else if !inside && present {
            folder.thread_ids.retain(|id| id != thread_id);
            self.mark_settings_dirty();
            self.show_toast(t!("thread_ops.toast.removed_from_folder", name = name), cx);
        } else {
            cx.notify();
        }
    }

    pub(crate) fn open_new_folder(
        &mut self,
        thread_id: Option<String>,
        window: &mut Window,
        cx: &mut Context<Self>,
    ) {
        let mode = FolderDialogMode::Create { thread_id };
        self.open_folder_dialog(mode, String::new(), window, cx);
    }

    pub(crate) fn open_rename_folder(
        &mut self,
        folder_id: &str,
        window: &mut Window,
        cx: &mut Context<Self>,
    ) {
        let Some(name) = self
            .settings
            .folders
            .iter()
            .find(|folder| folder.id == folder_id)
            .map(|folder| folder.name.clone())
        else {
            return;
        };
        let mode = FolderDialogMode::Rename {
            folder_id: folder_id.to_string(),
        };
        self.open_folder_dialog(mode, name, window, cx);
    }

    fn open_folder_dialog(
        &mut self,
        mode: FolderDialogMode,
        name: String,
        window: &mut Window,
        cx: &mut Context<Self>,
    ) {
        let input = cx.new(|cx| {
            InputState::new(window, cx)
                .placeholder(ts!("thread_ops.folder.name_placeholder"))
                .default_value(name)
        });
        let subscription = cx.subscribe_in(&input, window, |this, _, event: &InputEvent, _, cx| {
            if let InputEvent::PressEnter { .. } = event {
                this.submit_folder_dialog(cx);
            }
        });
        input.update(cx, |input, cx| input.focus(window, cx));
        self.thread_ops.menu = None;
        self.thread_ops.folder_dialog = Some(FolderDialog {
            mode,
            input,
            _subscription: subscription,
        });
        cx.notify();
    }

    pub(crate) fn close_folder_dialog(&mut self, cx: &mut Context<Self>) {
        if self.thread_ops.folder_dialog.take().is_some() {
            cx.notify();
        }
    }

    pub(crate) fn submit_folder_dialog(&mut self, cx: &mut Context<Self>) {
        let Some(dialog) = self.thread_ops.folder_dialog.as_ref() else {
            return;
        };
        let name = dialog.input.read(cx).value().trim().to_string();
        let editing = match &dialog.mode {
            FolderDialogMode::Rename { folder_id } => Some(folder_id.clone()),
            FolderDialogMode::Create { .. } => None,
        };
        if let Some(problem) = folder_name_problem(&name, editing.as_deref(), &self.settings.folders)
        {
            self.show_toast(problem, cx);
            return;
        }
        let Some(dialog) = self.thread_ops.folder_dialog.take() else {
            return;
        };
        match dialog.mode {
            FolderDialogMode::Rename { folder_id } => {
                if let Some(folder) = self
                    .settings
                    .folders
                    .iter_mut()
                    .find(|folder| folder.id == folder_id)
                {
                    folder.name = name;
                }
                self.mark_settings_dirty();
                self.show_toast(t!("thread_ops.toast.folder_renamed"), cx);
            }
            FolderDialogMode::Create { thread_id } => {
                let id = new_folder_id(&self.settings.folders, now_ms());
                self.settings.folders.push(Folder {
                    id: id.clone(),
                    name: name.clone(),
                    thread_ids: thread_id.iter().cloned().collect(),
                });
                self.mark_settings_dirty();
                if thread_id.is_some() {
                    self.show_toast(t!("thread_ops.toast.added_to_new_folder", name = name), cx);
                } else {
                    self.open_folder(&id, cx);
                    self.show_toast(t!("thread_ops.toast.folder_created"), cx);
                }
            }
        }
    }

    pub(crate) fn confirm_delete_folder(&mut self, folder_id: &str, cx: &mut Context<Self>) {
        let folder_id = folder_id.to_string();
        self.ask_confirm(ConfirmAction::DeleteFolder { folder_id }, cx);
    }

    fn delete_folder(&mut self, folder_id: &str, cx: &mut Context<Self>) {
        let count = self.settings.folders.len();
        self.settings.folders.retain(|folder| folder.id != folder_id);
        if self.settings.folders.len() == count {
            return;
        }
        if self.thread_ops.folder.as_deref() == Some(folder_id) {
            self.thread_ops.folder = None;
        }
        self.mark_settings_dirty();
        self.show_toast(t!("thread_ops.toast.folder_deleted"), cx);
    }
}

fn folder_name_problem(name: &str, editing: Option<&str>, folders: &[Folder]) -> Option<String> {
    if name.is_empty() {
        return Some(t!("thread_ops.folder.name_required").into_owned());
    }
    if name.chars().count() > FOLDER_NAME_MAX {
        return Some(t!("thread_ops.folder.name_too_long", max = FOLDER_NAME_MAX).into_owned());
    }
    let wanted = super::library::fold(name);
    let taken = folders.iter().any(|folder| {
        Some(folder.id.as_str()) != editing && super::library::fold(&folder.name) == wanted
    });
    taken.then(|| t!("thread_ops.folder.name_taken").into_owned())
}

fn new_folder_id(folders: &[Folder], mut stamp: i64) -> String {
    loop {
        let id = format!("f{stamp:x}");
        if !folders.iter().any(|folder| folder.id == id) {
            return id;
        }
        stamp += 1;
    }
}

#[cfg(test)]
mod tests {
    use super::{
        ephemeral_label, folder_name_problem, is_image_path, is_inbox_folder, new_folder_id,
    };
    use crate::settings::Folder;

    fn folder(id: &str, name: &str) -> Folder {
        Folder {
            id: id.into(),
            name: name.into(),
            thread_ids: Vec::new(),
        }
    }

    #[::core::prelude::v1::test]
    fn folder_ids_are_unique() {
        let existing = vec![folder("f10", "Travail")];
        assert_eq!(new_folder_id(&existing, 16), "f11");
        assert_eq!(new_folder_id(&[], 16), "f10");
    }

    #[::core::prelude::v1::test]
    fn folder_names_are_validated() {
        let folders = vec![folder("a", "Famille")];
        assert!(folder_name_problem("", None, &folders).is_some());
        assert!(folder_name_problem("famille", None, &folders).is_some());
        assert!(folder_name_problem("Famille", Some("a"), &folders).is_none());
        assert!(folder_name_problem("Travail", None, &folders).is_none());
        assert!(folder_name_problem(&"x".repeat(41), None, &folders).is_some());
    }

    #[::core::prelude::v1::test]
    fn ephemeral_labels_are_readable() {
        let _guard = crate::i18n::scoped("fr");
        assert_eq!(ephemeral_label(86_400_000), "24 h");
        assert_eq!(ephemeral_label(3_600_000), "1 h");
        assert_eq!(ephemeral_label(7 * 86_400_000), "7 jours");
        assert_eq!(ephemeral_label(300_000), "5 min");
        assert_eq!(ephemeral_label(5_000), "5 s");
    }

    #[::core::prelude::v1::test]
    fn folders_outside_requests_and_archive_count_as_inbox() {
        assert!(is_inbox_folder("inbox"));
        assert!(is_inbox_folder("other"));
        assert!(!is_inbox_folder("requests"));
        assert!(!is_inbox_folder("spam"));
        assert!(!is_inbox_folder("archived"));
    }

    #[::core::prelude::v1::test]
    fn only_images_are_accepted_as_group_photo() {
        assert!(is_image_path(std::path::Path::new("C:/a/photo.JPG")));
        assert!(!is_image_path(std::path::Path::new("C:/a/doc.pdf")));
    }
}
