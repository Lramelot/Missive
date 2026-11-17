use std::{
    collections::HashMap,
    time::{Duration, Instant},
};

use gpui_kit::component::input::{InputEvent, InputState, Position};
use gpui_kit::*;
use serde_json::json;

use super::{MissiveApp, action_error, library::fold};
use crate::model::{Message, Poll, Reactor};

const EDIT_WINDOW_MS: i64 = 15 * 60 * 1000;
const FLASH: Duration = Duration::from_millis(1200);
const JUMP_PAGES: usize = 10;
const JUMP_PAGE: usize = 60;
const JUMP_EMPTY_ROUNDS: usize = 3;
const JUMP_BACKFILL_WAIT: Duration = Duration::from_millis(1500);
const FORWARD_RESULTS: usize = 60;
pub(crate) const MAX_FORWARD: usize = 10;
pub(crate) const MIN_POLL_OPTIONS: usize = 2;
pub(crate) const MAX_POLL_OPTIONS: usize = 12;

#[derive(Clone, Copy, PartialEq, Eq)]
enum Confirm {
    Unsend,
    DeleteForMe,
}

pub(crate) struct MessageMenu {
    pub(crate) message: Message,
    pub(crate) position: Point<Pixels>,
    pub(crate) outgoing: bool,
    pub(crate) reminder_choices: bool,
    confirm: Option<Confirm>,
}

impl MessageMenu {
    pub(crate) fn confirming(&self) -> Option<(&'static str, &'static str)> {
        match self.confirm? {
            Confirm::Unsend => Some(("Retirer ce message pour tout le monde ?", "Retirer")),
            Confirm::DeleteForMe => Some(("Supprimer ce message pour vous ?", "Supprimer")),
        }
    }
}

pub(crate) struct Editing {
    pub(crate) thread_id: String,
    pub(crate) message: Message,
    previous_draft: String,
    window: AnyWindowHandle,
}

pub(crate) struct ForwardDialog {
    id: u64,
    pub(crate) message: Message,
    pub(crate) input: Entity<InputState>,
    pub(crate) selected: Vec<String>,
    pub(crate) busy: bool,
    _subscription: Subscription,
}

pub(crate) struct PollDialog {
    id: u64,
    pub(crate) thread_id: String,
    pub(crate) question: Entity<InputState>,
    pub(crate) options: Vec<Entity<InputState>>,
    pub(crate) busy: bool,
    pub(crate) error: Option<SharedString>,
    _subscriptions: Vec<Subscription>,
}

pub(crate) struct SeenBy {
    pub(crate) thread_id: String,
    pub(crate) message_id: String,
}

#[derive(Default)]
pub(crate) struct MessageOps {
    pub(crate) menu: Option<MessageMenu>,
    pub(crate) editing: Option<Editing>,
    pub(crate) forward: Option<ForwardDialog>,
    pub(crate) poll: Option<PollDialog>,
    pub(crate) seen_by: Option<SeenBy>,
    pub(crate) flash: Option<(String, Instant)>,
    pub(crate) jumping: Option<String>,
    pub(crate) pending_votes: HashMap<String, (u64, Vec<String>)>,
    next_request: u64,
}

impl MessageOps {
    pub(crate) fn on_thread_changed(&mut self) {
        self.menu = None;
        self.editing = None;
        self.forward = None;
        self.poll = None;
        self.seen_by = None;
        self.flash = None;
        self.jumping = None;
    }

    pub(crate) fn reset(&mut self) {
        *self = Self::default();
    }

    pub(crate) fn is_flashing(&self, message_id: &str) -> bool {
        self.flash
            .as_ref()
            .is_some_and(|(id, at)| id == message_id && at.elapsed() < FLASH)
    }

    fn next_id(&mut self) -> u64 {
        self.next_request += 1;
        self.next_request
    }

    pub(crate) fn is_editing(&self, message_id: &str) -> bool {
        self.editing
            .as_ref()
            .is_some_and(|editing| editing.message.id == message_id)
    }
}

enum JumpStep {
    Done,
    Wait,
    Fetch(i64, u64),
    Stop,
}

pub(crate) fn is_local(message: &Message) -> bool {
    message.id.starts_with("local-")
}

pub(crate) fn apply_vote(poll: &mut Poll, selected: &[String], me: &Reactor) {
    let mut total = poll.total_votes;
    for option in poll.options.iter_mut() {
        let chosen = selected.contains(&option.id);
        if chosen && !option.mine {
            option.vote_count += 1;
            total += 1;
            option.voters.retain(|voter| voter.user_id != me.user_id);
            option.voters.insert(0, me.clone());
        } else if !chosen && option.mine {
            option.vote_count = option.vote_count.saturating_sub(1);
            total = total.saturating_sub(1);
            option.voters.retain(|voter| voter.user_id != me.user_id);
        }
        option.mine = chosen;
    }
    poll.total_votes = total;
}

pub(crate) fn toggle_option(mut selected: Vec<String>, option_id: &str) -> Vec<String> {
    match selected.iter().position(|id| id == option_id) {
        Some(position) => {
            selected.remove(position);
        }
        None => selected.push(option_id.to_string()),
    }
    selected
}

impl MissiveApp {
    pub(crate) fn close_message_dialogs(&mut self, cx: &mut Context<Self>) -> bool {
        if self.message_ops.menu.is_some() {
            self.message_ops.menu = None;
        } else if self.message_ops.seen_by.is_some() {
            self.message_ops.seen_by = None;
        } else if self.message_ops.forward.is_some() {
            self.message_ops.forward = None;
        } else if self.message_ops.poll.is_some() {
            self.message_ops.poll = None;
        } else if self.message_ops.editing.is_some() {
            self.cancel_edit(None, cx);
        } else {
            return false;
        }
        cx.notify();
        true
    }

    fn me_reactor(&self) -> Reactor {
        self.user
            .as_ref()
            .map(|user| Reactor {
                user_id: user.id.clone(),
                name: user.name.clone(),
                avatar_url: user.avatar_url.clone(),
            })
            .unwrap_or_default()
    }

    fn mutate_message(
        &mut self,
        thread_id: &str,
        message_id: &str,
        change: impl FnOnce(&mut Message),
        cx: &mut Context<Self>,
    ) -> Option<Message> {
        let list = self.messages.get_mut(thread_id)?;
        let message = list.iter_mut().find(|m| m.id == message_id)?;
        let original = message.clone();
        change(message);
        self.remeasure_message(thread_id, message_id, cx);
        cx.notify();
        Some(original)
    }

    pub(crate) fn message_overlay_blocks_enter(&self) -> bool {
        self.message_ops.menu.is_some()
    }

    pub(crate) fn message_dialog_open(&self) -> bool {
        self.message_ops.menu.is_some()
            || self.message_ops.forward.is_some()
            || self.message_ops.poll.is_some()
            || self.message_ops.seen_by.is_some()
    }

    pub(crate) fn message_dialog_input_focused(&self, window: &Window, cx: &App) -> bool {
        let forward = self
            .message_ops
            .forward
            .iter()
            .map(|forward| &forward.input);
        let poll = self
            .message_ops
            .poll
            .iter()
            .flat_map(|poll| std::iter::once(&poll.question).chain(poll.options.iter()));
        forward
            .chain(poll)
            .any(|input| input.focus_handle(cx).contains_focused(window, cx))
    }

    pub(crate) fn effective_poll(&self, poll: &Poll) -> Option<Poll> {
        let (_, selected) = self.message_ops.pending_votes.get(&poll.id)?;
        let mut shown = poll.clone();
        apply_vote(&mut shown, selected, &self.me_reactor());
        Some(shown)
    }

    pub(crate) fn last_outgoing_index(&self, thread_id: &str) -> Option<usize> {
        self.messages
            .get(thread_id)?
            .iter()
            .rposition(|m| m.is_outgoing && !m.system)
    }

    pub(crate) fn remeasure_last_outgoing(&mut self, thread_id: &str, cx: &mut Context<Self>) {
        if self.selected.as_deref() != Some(thread_id) {
            return;
        }
        if let Some(index) = self.last_outgoing_index(thread_id) {
            self.scroller.update(cx, |scroller, cx| {
                scroller.remeasure_items(index..index + 1, cx)
            });
        }
    }

    pub(crate) fn can_edit(&self, message: &Message) -> bool {
        message.is_outgoing
            && !message.deleted
            && !message.system
            && message.poll.is_none()
            && !is_local(message)
            && !message.text.trim().is_empty()
            && message.attachments.iter().all(|a| a.kind == "link")
            && chrono::Utc::now().timestamp_millis() - message.timestamp_ms < EDIT_WINDOW_MS
    }

    pub(crate) fn can_unsend(&self, message: &Message) -> bool {
        message.is_outgoing && !message.deleted && !message.system && !is_local(message)
    }

    pub(crate) fn can_forward(&self, message: &Message) -> bool {
        !message.deleted && !message.system && message.poll.is_none() && !is_local(message)
    }

    pub(crate) fn open_message_menu(
        &mut self,
        message: Message,
        position: Point<Pixels>,
        cx: &mut Context<Self>,
    ) {
        self.react_picker = None;
        if let Some(previous) = self.reaction_details.take() {
            let thread_id = message.thread_id.clone();
            self.remeasure_message(&thread_id, &previous, cx);
        }
        let outgoing = message.is_outgoing;
        self.message_ops.menu = Some(MessageMenu {
            message,
            position,
            outgoing,
            reminder_choices: false,
            confirm: None,
        });
        cx.notify();
    }

    pub(crate) fn menu_show_reminder_choices(&mut self, show: bool, cx: &mut Context<Self>) {
        if let Some(menu) = self.message_ops.menu.as_mut() {
            menu.reminder_choices = show;
        }
        cx.notify();
    }

    pub(crate) fn can_plan_on(&self, message: &Message) -> bool {
        !message.deleted && !message.system && !is_local(message)
    }

    pub(crate) fn close_message_menu(&mut self, cx: &mut Context<Self>) {
        self.message_ops.menu = None;
        cx.notify();
    }

    pub(crate) fn menu_request_unsend(&mut self, cx: &mut Context<Self>) {
        if let Some(menu) = self.message_ops.menu.as_mut() {
            menu.confirm = Some(Confirm::Unsend);
        }
        cx.notify();
    }

    pub(crate) fn menu_request_delete(&mut self, cx: &mut Context<Self>) {
        if let Some(menu) = self.message_ops.menu.as_mut() {
            menu.confirm = Some(Confirm::DeleteForMe);
        }
        cx.notify();
    }

    pub(crate) fn menu_cancel_confirm(&mut self, cx: &mut Context<Self>) {
        if let Some(menu) = self.message_ops.menu.as_mut() {
            menu.confirm = None;
        }
        cx.notify();
    }

    pub(crate) fn menu_confirm(&mut self, cx: &mut Context<Self>) {
        let Some(menu) = self.message_ops.menu.take() else {
            return;
        };
        match menu.confirm {
            Some(Confirm::Unsend) => self.unsend(menu.message, cx),
            Some(Confirm::DeleteForMe) => self.delete_for_me(menu.message, cx),
            None => {}
        }
        cx.notify();
    }

    pub(crate) fn copy_message_text(&mut self, message: &Message, cx: &mut Context<Self>) {
        self.message_ops.menu = None;
        let text = if message.text.is_empty() {
            message
                .attachments
                .iter()
                .filter(|a| a.kind == "link")
                .find_map(|a| a.url.clone())
                .unwrap_or_default()
        } else {
            message.text.clone()
        };
        if text.is_empty() {
            self.show_toast("Aucun texte à copier", cx);
            return;
        }
        cx.write_to_clipboard(ClipboardItem::new_string(text));
        self.show_toast("Texte copié", cx);
    }

    pub(crate) fn start_edit(
        &mut self,
        message: Message,
        window: &mut Window,
        cx: &mut Context<Self>,
    ) {
        self.message_ops.menu = None;
        if !self.can_edit(&message) || self.selected.as_deref() != Some(message.thread_id.as_str())
        {
            cx.notify();
            return;
        }
        self.reply_to = None;
        self.react_picker = None;
        self.stop_typing(cx);
        let previous_draft = match self.message_ops.editing.take() {
            Some(editing) => editing.previous_draft,
            None => self.composer.read(cx).value().to_string(),
        };
        let text = message.text.clone();
        self.message_ops.editing = Some(Editing {
            thread_id: message.thread_id.clone(),
            message,
            previous_draft,
            window: window.window_handle(),
        });
        self.mention_query = None;
        self.pending_mentions.clear();
        self.set_composer_text(text, window, cx);
        cx.notify();
    }

    fn set_composer_text(&mut self, text: String, window: &mut Window, cx: &mut Context<Self>) {
        let line = text.matches('\n').count() as u32;
        let character = text
            .rsplit('\n')
            .next()
            .map(|last| last.encode_utf16().count() as u32)
            .unwrap_or(0);
        self.composer.update(cx, |input, cx| {
            input.set_value(text, window, cx);
            input.set_cursor_position(Position { line, character }, window, cx);
            input.focus(window, cx);
        });
    }

    pub(crate) fn cancel_edit(&mut self, window: Option<&mut Window>, cx: &mut Context<Self>) {
        let Some(editing) = self.message_ops.editing.take() else {
            return;
        };
        let draft = editing.previous_draft;
        match window {
            Some(window) => self.set_composer_text(draft, window, cx),
            None => {
                let composer = self.composer.clone();
                let handle = editing.window;
                cx.defer(move |cx| {
                    let _ = handle.update(cx, |_, window, cx| {
                        composer.update(cx, |input, cx| input.set_value(draft, window, cx));
                    });
                });
            }
        }
        cx.notify();
    }

    pub(crate) fn edit_last_message(
        &mut self,
        window: &mut Window,
        cx: &mut Context<Self>,
    ) -> bool {
        if self.message_ops.editing.is_some()
            || self.mention_popup_open()
            || self.message_dialog_open()
        {
            return false;
        }
        let Some(thread_id) = self.selected.clone() else {
            return false;
        };
        let candidate = self.messages.get(&thread_id).and_then(|list| {
            list.iter()
                .rev()
                .find(|m| m.is_outgoing && !m.system)
                .filter(|m| self.can_edit(m))
                .cloned()
        });
        match candidate {
            Some(message) => {
                self.start_edit(message, window, cx);
                true
            }
            None => false,
        }
    }

    pub(crate) fn submit_edit(&mut self, window: &mut Window, cx: &mut Context<Self>) {
        let text = self.composer.read(cx).value().trim().to_string();
        let Some(editing) = self.message_ops.editing.take() else {
            return;
        };
        let thread_id = editing.thread_id.clone();
        let message_id = editing.message.id.clone();
        let unchanged = text.is_empty() || text == editing.message.text.trim();
        self.set_composer_text(editing.previous_draft, window, cx);
        self.stop_typing(cx);
        if unchanged {
            cx.notify();
            return;
        }
        let Some(original) = self.mutate_message(
            &thread_id,
            &message_id,
            |message| {
                message.text = text.clone();
                message.edited = true;
                message.mentions.clear();
            },
            cx,
        ) else {
            cx.notify();
            return;
        };
        let Some(bridge) = self.bridge() else { return };
        let params = json!({ "thread_id": thread_id, "message_id": message_id, "text": text });
        cx.spawn(async move |this, cx| {
            if let Err(err) = bridge.request("edit_message", params).await {
                let _ = this.update(cx, |this, cx| {
                    let optimistic = text;
                    this.mutate_message(
                        &original.thread_id,
                        &original.id,
                        |message| {
                            if message.text == optimistic && !message.deleted {
                                message.text = original.text;
                                message.edited = original.edited;
                                message.mentions = original.mentions;
                            }
                        },
                        cx,
                    );
                    this.show_toast(
                        format!("Modification impossible : {}", action_error(&err)),
                        cx,
                    );
                });
            }
        })
        .detach();
    }

    pub(crate) fn unsend(&mut self, message: Message, cx: &mut Context<Self>) {
        if !self.can_unsend(&message) {
            return;
        }
        if self.message_ops.is_editing(&message.id) {
            self.cancel_edit(None, cx);
        }
        let Some(original) = self.mutate_message(
            &message.thread_id,
            &message.id,
            |message| {
                message.deleted = true;
                message.text.clear();
                message.attachments.clear();
                message.mentions.clear();
                message.poll = None;
            },
            cx,
        ) else {
            return;
        };
        if self.reply_to.as_ref().is_some_and(|m| m.id == message.id) {
            self.reply_to = None;
        }
        let Some(bridge) = self.bridge() else { return };
        let params = json!({ "thread_id": message.thread_id, "message_id": message.id });
        cx.spawn(async move |this, cx| {
            if let Err(err) = bridge.request("unsend", params).await {
                let _ = this.update(cx, |this, cx| {
                    this.mutate_message(
                        &original.thread_id,
                        &original.id,
                        |message| {
                            if message.deleted && message.text.is_empty() {
                                message.deleted = original.deleted;
                                message.text = original.text;
                                message.attachments = original.attachments;
                                message.mentions = original.mentions;
                                message.poll = original.poll;
                            }
                        },
                        cx,
                    );
                    this.show_toast(format!("Retrait impossible : {}", action_error(&err)), cx);
                });
            }
        })
        .detach();
    }

    pub(crate) fn delete_for_me(&mut self, message: Message, cx: &mut Context<Self>) {
        if is_local(&message) || message.system {
            return;
        }
        if self.message_ops.is_editing(&message.id) {
            self.cancel_edit(None, cx);
        }
        let thread_id = message.thread_id.clone();
        self.on_message_removed(&thread_id, &message.id, cx);
        self.remeasure_last_outgoing(&thread_id, cx);
        let Some(bridge) = self.bridge() else { return };
        let params = json!({ "thread_id": thread_id, "message_id": message.id });
        cx.spawn(async move |this, cx| {
            if let Err(err) = bridge.request("delete_for_me", params).await {
                let _ = this.update(cx, |this, cx| {
                    this.reinsert_message(message, cx);
                    this.show_toast(
                        format!("Suppression impossible : {}", action_error(&err)),
                        cx,
                    );
                });
            }
        })
        .detach();
    }

    fn reinsert_message(&mut self, message: Message, cx: &mut Context<Self>) {
        let thread_id = message.thread_id.clone();
        let is_selected = self.selected.as_deref() == Some(thread_id.as_str());
        let Some(list) = self.messages.get_mut(&thread_id) else {
            return;
        };
        if list.iter().any(|m| m.id == message.id) {
            return;
        }
        let position = list.partition_point(|m| m.timestamp_ms <= message.timestamp_ms);
        list.insert(position, message);
        if is_selected {
            self.scroller.update(cx, |scroller, cx| {
                scroller.splice(position..position, 1, cx)
            });
        }
        self.remeasure_last_outgoing(&thread_id, cx);
        cx.notify();
    }

    pub(crate) fn open_forward(
        &mut self,
        message: Message,
        window: &mut Window,
        cx: &mut Context<Self>,
    ) {
        self.message_ops.menu = None;
        if !self.can_forward(&message) {
            cx.notify();
            return;
        }
        let input =
            cx.new(|cx| InputState::new(window, cx).placeholder("Rechercher une discussion"));
        let subscription =
            cx.subscribe_in(
                &input,
                window,
                |this, _, event: &InputEvent, _, cx| match event {
                    InputEvent::Change => cx.notify(),
                    InputEvent::PressEnter { .. } => this.send_forward(cx),
                    _ => {}
                },
            );
        input.update(cx, |input, cx| input.focus(window, cx));
        let id = self.message_ops.next_id();
        self.message_ops.forward = Some(ForwardDialog {
            id,
            message,
            input,
            selected: Vec::new(),
            busy: false,
            _subscription: subscription,
        });
        cx.notify();
    }

    pub(crate) fn close_forward(&mut self, cx: &mut Context<Self>) {
        self.message_ops.forward = None;
        cx.notify();
    }

    pub(crate) fn forward_results(&self, cx: &App) -> Vec<usize> {
        let Some(forward) = &self.message_ops.forward else {
            return Vec::new();
        };
        let query = fold(forward.input.read(cx).value().trim());
        self.threads
            .iter()
            .enumerate()
            .filter(|(_, thread)| thread.folder == "inbox" || thread.folder == "archived")
            .filter(|(_, thread)| query.is_empty() || fold(&thread.name).contains(&query))
            .map(|(index, _)| index)
            .take(FORWARD_RESULTS)
            .collect()
    }

    pub(crate) fn toggle_forward_target(&mut self, thread_id: String, cx: &mut Context<Self>) {
        let Some(forward) = self.message_ops.forward.as_mut() else {
            return;
        };
        if forward.busy {
            return;
        }
        match forward.selected.iter().position(|id| *id == thread_id) {
            Some(position) => {
                forward.selected.remove(position);
            }
            None if forward.selected.len() < MAX_FORWARD => forward.selected.push(thread_id),
            None => {
                self.show_toast(format!("{MAX_FORWARD} conversations au maximum"), cx);
                return;
            }
        }
        cx.notify();
    }

    pub(crate) fn send_forward(&mut self, cx: &mut Context<Self>) {
        let Some(bridge) = self.bridge() else { return };
        let Some(forward) = self.message_ops.forward.as_mut() else {
            return;
        };
        if forward.busy || forward.selected.is_empty() {
            return;
        }
        forward.busy = true;
        let dialog = forward.id;
        let count = forward.selected.len();
        let params = json!({
            "from_thread_id": forward.message.thread_id,
            "message_id": forward.message.id,
            "to_thread_ids": forward.selected,
        });
        cx.notify();
        cx.spawn(async move |this, cx| {
            let result = bridge.request("forward", params).await;
            let _ = this.update(cx, |this, cx| match result {
                Ok(_) => {
                    if this
                        .message_ops
                        .forward
                        .as_ref()
                        .is_some_and(|f| f.id == dialog)
                    {
                        this.message_ops.forward = None;
                    }
                    this.show_toast(
                        if count > 1 {
                            format!("Message transféré dans {count} conversations")
                        } else {
                            "Message transféré".to_string()
                        },
                        cx,
                    );
                }
                Err(err) => {
                    if let Some(forward) = this.message_ops.forward.as_mut()
                        && forward.id == dialog
                    {
                        forward.busy = false;
                    }
                    this.show_toast(format!("Transfert impossible : {}", action_error(&err)), cx);
                }
            });
        })
        .detach();
    }

    pub(crate) fn open_seen_by(
        &mut self,
        thread_id: String,
        message_id: String,
        cx: &mut Context<Self>,
    ) {
        self.message_ops.menu = None;
        self.message_ops.seen_by = Some(SeenBy {
            thread_id,
            message_id,
        });
        cx.notify();
    }

    pub(crate) fn close_seen_by(&mut self, cx: &mut Context<Self>) {
        self.message_ops.seen_by = None;
        cx.notify();
    }

    pub(crate) fn can_create_poll(&self) -> bool {
        self.selected_thread()
            .is_some_and(|thread| thread.is_group && thread.can_manage && !thread.e2ee)
    }

    fn poll_option_input(
        &mut self,
        window: &mut Window,
        cx: &mut Context<Self>,
    ) -> (Entity<InputState>, Subscription) {
        let input = cx.new(|cx| InputState::new(window, cx).placeholder("Option"));
        let subscription = cx.subscribe_in(
            &input,
            window,
            |this, input, event: &InputEvent, window, cx| match event {
                InputEvent::Change => cx.notify(),
                InputEvent::PressEnter { .. } => this.poll_enter(Some(input), window, cx),
                _ => {}
            },
        );
        (input, subscription)
    }

    fn poll_enter(
        &mut self,
        input: Option<&Entity<InputState>>,
        window: &mut Window,
        cx: &mut Context<Self>,
    ) {
        let Some(poll) = self.message_ops.poll.as_ref() else {
            return;
        };
        let next = match input {
            None => 0,
            Some(input) => match poll.options.iter().position(|option| option == input) {
                Some(index) => index + 1,
                None => return,
            },
        };
        if let Some(target) = poll.options.get(next).cloned() {
            target.update(cx, |input, cx| input.focus(window, cx));
        } else if self.poll_draft(cx).is_some() {
            self.submit_poll(cx);
        } else {
            self.add_poll_option(window, cx);
        }
    }

    pub(crate) fn open_poll_dialog(&mut self, window: &mut Window, cx: &mut Context<Self>) {
        let Some(thread_id) = self.selected.clone() else {
            return;
        };
        if !self.can_create_poll() {
            return;
        }
        let question = cx.new(|cx| InputState::new(window, cx).placeholder("Posez une question"));
        let mut subscriptions = vec![cx.subscribe_in(
            &question,
            window,
            |this, _, event: &InputEvent, window, cx| match event {
                InputEvent::Change => cx.notify(),
                InputEvent::PressEnter { .. } => this.poll_enter(None, window, cx),
                _ => {}
            },
        )];
        let mut options = Vec::new();
        for _ in 0..MIN_POLL_OPTIONS {
            let (input, subscription) = self.poll_option_input(window, cx);
            options.push(input);
            subscriptions.push(subscription);
        }
        question.update(cx, |input, cx| input.focus(window, cx));
        let id = self.message_ops.next_id();
        self.message_ops.poll = Some(PollDialog {
            id,
            thread_id,
            question,
            options,
            busy: false,
            error: None,
            _subscriptions: subscriptions,
        });
        cx.notify();
    }

    pub(crate) fn close_poll_dialog(&mut self, cx: &mut Context<Self>) {
        self.message_ops.poll = None;
        cx.notify();
    }

    pub(crate) fn add_poll_option(&mut self, window: &mut Window, cx: &mut Context<Self>) {
        if self
            .message_ops
            .poll
            .as_ref()
            .is_none_or(|poll| poll.options.len() >= MAX_POLL_OPTIONS)
        {
            return;
        }
        let (input, subscription) = self.poll_option_input(window, cx);
        input.update(cx, |input, cx| input.focus(window, cx));
        if let Some(poll) = self.message_ops.poll.as_mut() {
            poll.options.push(input);
            poll._subscriptions.push(subscription);
        }
        cx.notify();
    }

    pub(crate) fn remove_poll_option(&mut self, index: usize, cx: &mut Context<Self>) {
        if let Some(poll) = self.message_ops.poll.as_mut()
            && poll.options.len() > MIN_POLL_OPTIONS
            && index < poll.options.len()
        {
            poll.options.remove(index);
        }
        cx.notify();
    }

    pub(crate) fn poll_draft(&self, cx: &App) -> Option<(String, Vec<String>)> {
        let poll = self.message_ops.poll.as_ref()?;
        let question = poll.question.read(cx).value().trim().to_string();
        let options: Vec<String> = poll
            .options
            .iter()
            .map(|input| input.read(cx).value().trim().to_string())
            .filter(|text| !text.is_empty())
            .collect();
        (!question.is_empty() && options.len() >= MIN_POLL_OPTIONS).then_some((question, options))
    }

    pub(crate) fn submit_poll(&mut self, cx: &mut Context<Self>) {
        let Some((question, options)) = self.poll_draft(cx) else {
            if let Some(poll) = self.message_ops.poll.as_mut() {
                poll.error = Some("Une question et au moins deux options sont nécessaires.".into());
            }
            cx.notify();
            return;
        };
        let Some(poll) = self.message_ops.poll.as_mut() else {
            return;
        };
        if poll.busy {
            return;
        }
        let Some(bridge) = self.bridge.clone() else {
            return;
        };
        poll.busy = true;
        poll.error = None;
        let dialog = poll.id;
        let thread_id = poll.thread_id.clone();
        let params = json!({ "thread_id": thread_id, "question": question, "options": options });
        cx.notify();
        cx.spawn(async move |this, cx| {
            let result = bridge.request("create_poll", params).await;
            let _ = this.update(cx, |this, cx| {
                match result {
                    Ok(_) => {
                        if this
                            .message_ops
                            .poll
                            .as_ref()
                            .is_some_and(|p| p.id == dialog)
                        {
                            this.message_ops.poll = None;
                        }
                        this.show_toast("Sondage créé", cx);
                    }
                    Err(err) => {
                        if let Some(poll) = this.message_ops.poll.as_mut()
                            && poll.id == dialog
                        {
                            poll.busy = false;
                            poll.error = Some(action_error(&err).into());
                        }
                    }
                }
                cx.notify();
            });
        })
        .detach();
    }

    pub(crate) fn vote_poll(&mut self, message: &Message, option_id: &str, cx: &mut Context<Self>) {
        if message.deleted || is_local(message) {
            return;
        }
        let Some(current) = self
            .messages
            .get(&message.thread_id)
            .and_then(|list| list.iter().find(|m| m.id == message.id))
            .and_then(|m| m.poll.clone())
            .or_else(|| message.poll.clone())
        else {
            return;
        };
        let base: Vec<String> = match self.message_ops.pending_votes.get(&current.id) {
            Some((_, pending)) => pending.clone(),
            None => current
                .options
                .iter()
                .filter(|option| option.mine)
                .map(|option| option.id.clone())
                .collect(),
        };
        let selected = toggle_option(base, option_id);
        let token = self.message_ops.next_id();
        let poll_id = current.id.clone();
        self.message_ops
            .pending_votes
            .insert(poll_id.clone(), (token, selected.clone()));
        let thread_id = message.thread_id.clone();
        let message_id = message.id.clone();
        self.remeasure_message(&thread_id, &message_id, cx);
        cx.notify();
        let Some(bridge) = self.bridge() else {
            self.message_ops.pending_votes.remove(&poll_id);
            return;
        };
        let params = json!({
            "thread_id": thread_id,
            "poll_id": poll_id,
            "option_ids": selected,
        });
        cx.spawn(async move |this, cx| {
            let result = bridge.request("vote_poll", params).await;
            let _ = this.update(cx, |this, cx| {
                let latest = this
                    .message_ops
                    .pending_votes
                    .get(&poll_id)
                    .is_some_and(|(pending, _)| *pending == token);
                if latest {
                    this.message_ops.pending_votes.remove(&poll_id);
                    if result.is_ok() {
                        let me = this.me_reactor();
                        this.mutate_message(
                            &thread_id,
                            &message_id,
                            |message| {
                                if let Some(poll) = message.poll.as_mut()
                                    && poll.id == poll_id
                                {
                                    apply_vote(poll, &selected, &me);
                                }
                            },
                            cx,
                        );
                    }
                }
                if let Err(err) = result {
                    this.remeasure_message(&thread_id, &message_id, cx);
                    this.show_toast(format!("Vote impossible : {}", action_error(&err)), cx);
                }
                cx.notify();
            });
        })
        .detach();
    }

    fn scroll_to_message(
        &mut self,
        thread_id: &str,
        message_id: &str,
        cx: &mut Context<Self>,
    ) -> bool {
        if self.selected.as_deref() != Some(thread_id) {
            return false;
        }
        let Some(index) = self
            .messages
            .get(thread_id)
            .and_then(|list| list.iter().position(|m| m.id == message_id))
        else {
            return false;
        };
        self.scroller
            .update(cx, |scroller, cx| scroller.scroll_to_item(index, cx));
        self.message_ops.flash = Some((message_id.to_string(), Instant::now()));
        let flashed = message_id.to_string();
        cx.spawn(async move |this, cx| {
            cx.background_executor().timer(FLASH).await;
            let _ = this.update(cx, |this, cx| {
                if this
                    .message_ops
                    .flash
                    .as_ref()
                    .is_some_and(|(id, _)| *id == flashed)
                {
                    this.message_ops.flash = None;
                    cx.notify();
                }
            });
        })
        .detach();
        cx.notify();
        true
    }

    pub(crate) fn jump_to_message(
        &mut self,
        thread_id: String,
        message_id: String,
        cx: &mut Context<Self>,
    ) {
        if self.selected.as_deref() != Some(thread_id.as_str()) {
            return;
        }
        self.message_ops.menu = None;
        if self.scroll_to_message(&thread_id, &message_id, cx) {
            return;
        }
        if self.message_ops.jumping.is_some() {
            return;
        }
        let Some(bridge) = self.bridge() else { return };
        self.message_ops.jumping = Some(message_id.clone());
        self.show_toast("Recherche du message d'origine…", cx);
        cx.spawn(async move |this, cx| {
            let mut empty_rounds = 0;
            let mut found = false;
            for _ in 0..JUMP_PAGES * 2 {
                let step = this
                    .update(cx, |this, cx| this.jump_step(&thread_id, &message_id, cx))
                    .unwrap_or(JumpStep::Stop);
                let (oldest, generation) = match step {
                    JumpStep::Done => {
                        found = true;
                        break;
                    }
                    JumpStep::Stop => break,
                    JumpStep::Wait => {
                        cx.background_executor()
                            .timer(Duration::from_millis(400))
                            .await;
                        continue;
                    }
                    JumpStep::Fetch(oldest, generation) => (oldest, generation),
                };
                let result = bridge
                    .call::<Vec<Message>>(
                        "messages",
                        json!({ "thread_id": thread_id, "before_ms": oldest, "limit": JUMP_PAGE }),
                    )
                    .await;
                let merged = this
                    .update(cx, |this, cx| {
                        this.merge_jump_page(&thread_id, oldest, generation, result, cx)
                    })
                    .ok()
                    .flatten();
                match merged {
                    None => break,
                    Some(0) => {
                        empty_rounds += 1;
                        if empty_rounds >= JUMP_EMPTY_ROUNDS {
                            break;
                        }
                        cx.background_executor().timer(JUMP_BACKFILL_WAIT).await;
                    }
                    Some(_) => empty_rounds = 0,
                }
            }
            let _ = this.update(cx, |this, cx| {
                this.message_ops.jumping = None;
                let shown = found || this.scroll_to_message(&thread_id, &message_id, cx);
                if shown {
                    this.toast = None;
                } else if this.selected.as_deref() == Some(thread_id.as_str()) {
                    this.show_toast("Message d'origine introuvable", cx);
                }
                cx.notify();
            });
        })
        .detach();
    }

    fn jump_step(&mut self, thread_id: &str, message_id: &str, cx: &mut Context<Self>) -> JumpStep {
        if self.selected.as_deref() != Some(thread_id)
            || self.message_ops.jumping.as_deref() != Some(message_id)
        {
            return JumpStep::Stop;
        }
        if self.scroll_to_message(thread_id, message_id, cx) {
            return JumpStep::Done;
        }
        let Some(oldest) = self
            .messages
            .get(thread_id)
            .and_then(|list| list.first())
            .map(|m| m.timestamp_ms)
        else {
            return JumpStep::Wait;
        };
        let generation = self.generation(thread_id);
        let state = self.older.entry(thread_id.to_string()).or_default();
        if state.loading {
            return JumpStep::Wait;
        }
        state.loading = true;
        JumpStep::Fetch(oldest, generation)
    }

    fn merge_jump_page(
        &mut self,
        thread_id: &str,
        oldest: i64,
        generation: u64,
        result: anyhow::Result<Vec<Message>>,
        cx: &mut Context<Self>,
    ) -> Option<usize> {
        self.older.entry(thread_id.to_string()).or_default().loading = false;
        if self.generation(thread_id) != generation || !self.messages.contains_key(thread_id) {
            return None;
        }
        let mut older = result.ok()?;
        let known: std::collections::HashSet<String> = self
            .messages
            .get(thread_id)
            .map(|list| list.iter().map(|m| m.id.clone()).collect())
            .unwrap_or_default();
        older.retain(|message| message.timestamp_ms < oldest && !known.contains(&message.id));
        older.sort_by_key(|message| message.timestamp_ms);
        let count = older.len();
        if count == 0 {
            return Some(0);
        }
        if let Some(list) = self.messages.get_mut(thread_id) {
            older.append(list);
            *list = older;
        }
        if self.selected.as_deref() == Some(thread_id) {
            self.scroller.update(cx, |scroller, cx| {
                scroller.prepend(count, cx);
                scroller.remeasure_items(count..count + 1, cx);
            });
        }
        cx.notify();
        Some(count)
    }
}

#[cfg(test)]
mod tests {
    use super::{apply_vote, toggle_option};
    use crate::model::{Poll, PollOption, Reactor};

    fn poll() -> Poll {
        Poll {
            id: "p".into(),
            question: "Où ?".into(),
            options: vec![
                PollOption {
                    id: "a".into(),
                    text: "Ici".into(),
                    vote_count: 2,
                    voters: Vec::new(),
                    mine: true,
                },
                PollOption {
                    id: "b".into(),
                    text: "Là".into(),
                    vote_count: 1,
                    voters: Vec::new(),
                    mine: false,
                },
            ],
            total_votes: 3,
        }
    }

    #[::core::prelude::v1::test]
    fn toggling_switches_membership() {
        let poll = poll();
        let mine: Vec<String> = poll
            .options
            .iter()
            .filter(|option| option.mine)
            .map(|option| option.id.clone())
            .collect();
        assert_eq!(toggle_option(mine.clone(), "a"), Vec::<String>::new());
        assert_eq!(
            toggle_option(mine, "b"),
            vec!["a".to_string(), "b".to_string()]
        );
    }

    #[::core::prelude::v1::test]
    fn vote_updates_counts_and_voters() {
        let mut poll = poll();
        let me = Reactor {
            user_id: "me".into(),
            name: "Moi".into(),
            avatar_url: None,
        };
        apply_vote(&mut poll, &["b".to_string()], &me);
        assert!(!poll.options[0].mine);
        assert_eq!(poll.options[0].vote_count, 1);
        assert!(poll.options[1].mine);
        assert_eq!(poll.options[1].vote_count, 2);
        assert_eq!(poll.options[1].voters[0].user_id, "me");
        assert_eq!(poll.total_votes, 3);
        apply_vote(&mut poll, &[], &me);
        assert_eq!(poll.total_votes, 2);
        assert!(poll.options.iter().all(|option| !option.mine));
    }
}
