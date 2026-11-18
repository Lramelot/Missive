use std::{collections::HashMap, path::PathBuf};

use futures::StreamExt as _;
use gpui_kit::component::{
    input::{InputEvent, InputState, TextareaState},
    message_scroller::MessageScrollerState,
};
use gpui_kit::*;
use serde_json::json;

use crate::{
    bridge::{Bridge, BridgeError, BridgeEvent, BridgeOptions},
    desktop::Tray,
    model::{
        Attachment, BackfillEvent, ConnectionEvent, HistoryStatus, LoginResult, MediaItem, Message,
        MessageRemovedEvent, Participant, ReactionAddedEvent, PresenceEvent, ReadyEvent, SearchHit, Status, Thread, ThreadRemovedEvent, ThreadsEvent,
        TypingEvent, User,
    },
    notifications::{self, Notification},
    settings::Settings,
    updates::UpdateState,
};

mod compose;
mod export;
mod find;
mod library;
mod media;
mod mentions;
mod message_ops;
mod notify_rules;
mod planner;
mod system;
mod thread_ops;
mod voice;


pub(crate) use media::{MediaJob, thumb_key};

#[derive(Clone)]
pub(crate) enum Outgoing {
    Sending,
    Failed(SharedString),
}

#[derive(Clone)]
pub(crate) struct OutgoingRequest {
    pub(crate) thread_id: String,
    pub(crate) text: String,
    pub(crate) reply_to: Option<String>,
    pub(crate) files: Vec<PathBuf>,
    pub(crate) mentions: Vec<crate::model::Mention>,
    pub(crate) mark_read: bool,
}

pub(crate) use library::item_message;

#[derive(Default)]
pub(crate) struct Older {
    pub(crate) loading: bool,
    pub(crate) exhausted: bool,
}

pub(crate) struct Switcher {
    pub(crate) input: Entity<InputState>,
    pub(crate) selected: usize,
    _subscription: Subscription,
}

#[derive(Clone, Copy, PartialEq, Eq)]
pub(crate) enum GalleryTab {
    Media,
    Files,
    Links,
}

impl GalleryTab {
    pub(crate) fn kinds(self) -> &'static [&'static str] {
        match self {
            GalleryTab::Media => &["image", "video"],
            GalleryTab::Files => &["file", "audio"],
            GalleryTab::Links => &["link"],
        }
    }
}

pub(crate) struct Gallery {
    pub(crate) thread_id: String,
    pub(crate) tab: GalleryTab,
    pub(crate) items: Vec<MediaItem>,
    pub(crate) has_more: bool,
    pub(crate) loading: bool,
    pub(crate) error: Option<SharedString>,
    pub(crate) scroll: ScrollHandle,
    pub(crate) store_pass_done: bool,
}

pub(crate) struct Lightbox {
    pub(crate) items: Vec<(Message, Attachment)>,
    pub(crate) index: usize,
}

#[derive(Clone, Copy, PartialEq, Eq)]
pub(crate) enum Filter {
    All,
    Unread,
    Groups,
    Requests,
    Archived,
}

#[derive(Clone, Copy, PartialEq, Eq)]
pub(crate) enum History {
    Unknown,
    None,
    Available,
    Restored,
}

pub(crate) enum Screen {
    Starting,
    Login,
    Main,
    Fatal(SharedString),
}

#[derive(Clone, Copy, PartialEq, Eq)]
pub(crate) enum Link {
    Connecting,
    Online,
    Reconnecting,
    Error,
}

#[derive(Clone)]
pub(crate) enum Media {
    Loading,
    Ready(PathBuf),
    Failed,
}

pub(crate) enum GifFrames {
    Decoding,
    Ready(ImageSource),
    Failed,
}

pub struct MissiveApp {
    pub(crate) bridge: Option<Bridge>,
    pub(crate) screen: Screen,
    pub(crate) user: Option<User>,
    pub(crate) link: Link,
    pub(crate) link_detail: Option<SharedString>,
    pub(crate) threads: Vec<Thread>,
    pub(crate) selected: Option<String>,
    pub(crate) messages: HashMap<String, Vec<Message>>,
    pub(crate) typing: HashMap<String, bool>,
    pub(crate) media: HashMap<String, Media>,
    pub(crate) scroller: Entity<MessageScrollerState>,
    pub(crate) composer: Entity<TextareaState>,
    pub(crate) search: Entity<InputState>,
    pub(crate) cookie_c_user: Entity<InputState>,
    pub(crate) cookie_xs: Entity<InputState>,
    pub(crate) cookie_datr: Entity<InputState>,
    pub(crate) login_error: Option<SharedString>,
    pub(crate) login_busy: bool,
    pub(crate) history: History,
    pub(crate) pin_input: Entity<InputState>,
    pub(crate) pin_open: bool,
    pub(crate) pin_busy: bool,
    pub(crate) pin_error: Option<SharedString>,
    pub(crate) window_active: bool,
    pub(crate) window_title: String,
    pub(crate) filter: Filter,
    pub(crate) info_open: bool,
    pub(crate) presence: HashMap<String, bool>,
    pub(crate) reply_to: Option<Message>,
    pub(crate) pending_files: Vec<PathBuf>,
    pub(crate) react_picker: Option<String>,
    pub(crate) older: HashMap<String, Older>,
    pub(crate) typing_sent: Option<(String, std::time::Instant)>,
    pub(crate) search_hits: Vec<SearchHit>,
    pub(crate) search_query: String,
    pub(crate) switcher: Option<Switcher>,
    pub(crate) gallery: Option<Gallery>,
    pub(crate) lightbox: Option<Lightbox>,
    pub(crate) sending: usize,
    pub(crate) settings: Settings,
    pub(crate) settings_open: bool,
    pub(crate) manual_login: bool,
    pub(crate) tray: Option<Tray>,
    pub(crate) instance: crate::desktop::Instance,
    pub(crate) drafts: HashMap<String, String>,
    pub(crate) thread_gen: HashMap<String, u64>,
    pub(crate) loading_threads: std::collections::HashSet<String>,
    pub(crate) buffered_live: HashMap<String, Vec<Message>>,
    pub(crate) pending_scroll: Option<(String, String)>,
    pub(crate) popout_active: HashMap<String, bool>,
    pub(crate) media_queue: std::cell::RefCell<Vec<MediaJob>>,
    pub(crate) gifs: HashMap<String, GifFrames>,
    pub(crate) gif_order: Vec<String>,
    pub(crate) gif_queue: std::cell::RefCell<Vec<(String, PathBuf)>>,
    pub(crate) gif_in_flight: usize,
    pub(crate) media_in_flight: usize,
    pub(crate) outgoing: HashMap<String, Outgoing>,
    pub(crate) outgoing_requests: HashMap<String, OutgoingRequest>,
    pub(crate) next_client_id: u64,
    pub(crate) started: std::time::Instant,
    pub(crate) prefetched: bool,
    pub(crate) mention_query: Option<String>,
    pub(crate) mention_selected: usize,
    pub(crate) pending_mentions: Vec<(String, String)>,
    pub(crate) swallow_enter_until: Option<std::time::Instant>,
    pub(crate) reaction_details: Option<String>,
    pub(crate) message_ops: message_ops::MessageOps,
    pub(crate) thread_ops: thread_ops::ThreadOps,
    pub(crate) voice: voice::Voice,
    pub(crate) toast: Option<(SharedString, std::time::Instant)>,
    pub(crate) planner: planner::Planner,
    pub(crate) exports: export::Exports,
    pub(crate) find: find::Find,
    pub(crate) drafts_saved: HashMap<String, String>,
    pub(crate) last_slow_tick: std::time::Instant,
    pub(crate) read_sent: HashMap<String, i64>,
    pub(crate) clicks: std::sync::mpsc::Sender<String>,
    pub(crate) update: Option<UpdateState>,
    pub(crate) last_update_check: Option<std::time::Instant>,
    _system_task: Option<Task<()>>,
    _search_task: Option<Task<()>>,
    _subscriptions: Vec<Subscription>,
    _event_task: Option<Task<()>>,
}

const DRAFTS_FILE: &str = "drafts.json";

pub(crate) fn media_key(message_id: &str, attachment_id: &str) -> String {
    format!("{message_id}/{attachment_id}")
}

impl MissiveApp {
    pub fn new(
        settings: Settings,
        instance: crate::desktop::Instance,
        window: &mut Window,
        cx: &mut Context<Self>,
    ) -> Self {
        let (clicks, clicks_rx) = std::sync::mpsc::channel::<String>();
        let scroller = cx.new(|cx| MessageScrollerState::new(0, cx));
        let composer = cx.new(|cx| {
            TextareaState::new(window, cx)
                .auto_grow(1, 6)
                .submit_on_enter(true)
                .placeholder("Écrire un message")
        });
        let search = cx.new(|cx| InputState::new(window, cx).placeholder("Rechercher"));
        let cookie_c_user = cx.new(|cx| InputState::new(window, cx).placeholder("c_user"));
        let cookie_xs = cx.new(|cx| InputState::new(window, cx).placeholder("xs"));
        let cookie_datr = cx.new(|cx| InputState::new(window, cx).placeholder("datr"));
        let pin_input = cx.new(|cx| {
            InputState::new(window, cx)
                .masked(true)
                .validate(|text, _| text.len() <= 6 && text.chars().all(|c| c.is_ascii_digit()))
                .placeholder("PIN à 6 chiffres")
        });

        let subscriptions = vec![
            cx.on_app_quit(|this, cx| {
                this.persist_drafts(cx);
                this.rescue_unsent_texts(cx);
                async {}
            }),
            cx.observe(&scroller, |this, _, cx| this.maybe_load_older(cx)),
            cx.observe_window_activation(window, |this, window, cx| {
                this.window_active = window.is_window_active();
                if this.window_active {
                    this.mark_read(cx);
                }
                this.report_activity(cx);
            }),
            cx.subscribe_in(&pin_input, window, |this, _, event: &InputEvent, _, cx| {
                if let InputEvent::PressEnter { .. } = event {
                    this.restore_history(cx);
                }
            }),
            cx.subscribe_in(
                &composer,
                window,
                |this, _, event: &InputEvent, window, cx| match event {
                    InputEvent::PressEnter { shift: false, .. } => {
                        let swallow = this
                            .swallow_enter_until
                            .take()
                            .is_some_and(|until| std::time::Instant::now() <= until);
                        if !swallow {
                            this.send_current(window, cx);
                        }
                    }
                    InputEvent::Change => {
                        this.on_composer_changed(cx);
                        cx.notify();
                    }
                    _ => {}
                },
            ),
            cx.subscribe_in(&search, window, |this, _, event: &InputEvent, _, cx| {
                if let InputEvent::Change = event {
                    this.on_search_changed(cx);
                    cx.notify();
                }
            }),
        ];

        let mut app = Self {
            bridge: None,
            screen: Screen::Starting,
            user: None,
            link: Link::Connecting,
            link_detail: None,
            threads: Vec::new(),
            selected: None,
            messages: HashMap::new(),
            typing: HashMap::new(),
            media: HashMap::new(),
            scroller,
            composer,
            search,
            cookie_c_user,
            cookie_xs,
            cookie_datr,
            login_error: None,
            login_busy: false,
            history: History::Unknown,
            pin_input,
            pin_open: false,
            pin_busy: false,
            pin_error: None,
            window_active: window.is_window_active(),
            window_title: String::new(),
            filter: Filter::All,
            info_open: false,
            presence: HashMap::new(),
            reply_to: None,
            pending_files: Vec::new(),
            react_picker: None,
            older: HashMap::new(),
            typing_sent: None,
            search_hits: Vec::new(),
            search_query: String::new(),
            switcher: None,
            gallery: None,
            lightbox: None,
            sending: 0,
            tray: Tray::create(),
            instance,
            drafts: crate::storage::load_json(DRAFTS_FILE),
            thread_gen: HashMap::new(),
            loading_threads: std::collections::HashSet::new(),
            buffered_live: HashMap::new(),
            pending_scroll: None,
            popout_active: HashMap::new(),
            media_queue: std::cell::RefCell::new(Vec::new()),
            gifs: HashMap::new(),
            gif_order: Vec::new(),
            gif_queue: std::cell::RefCell::new(Vec::new()),
            gif_in_flight: 0,
            media_in_flight: 0,
            outgoing: HashMap::new(),
            outgoing_requests: HashMap::new(),
            next_client_id: 0,
            started: std::time::Instant::now(),
            prefetched: false,
            mention_query: None,
            mention_selected: 0,
            pending_mentions: Vec::new(),
            swallow_enter_until: None,
            reaction_details: None,
            message_ops: Default::default(),
            thread_ops: Default::default(),
            voice: Default::default(),
            toast: None,
            planner: planner::Planner::load(),
            exports: Default::default(),
            find: Default::default(),
            drafts_saved: HashMap::new(),
            last_slow_tick: std::time::Instant::now(),
            read_sent: HashMap::new(),
            settings,
            settings_open: false,
            manual_login: false,
            clicks,
            update: None,
            last_update_check: None,
            _system_task: None,
            _search_task: None,
            _subscriptions: subscriptions,
            _event_task: None,
        };

        match BridgeOptions::from_env().and_then(|options| Bridge::spawn(&options)) {
            Ok((bridge, mut events)) => {
                app.bridge = Some(bridge);
                app._event_task = Some(cx.spawn(async move |this, cx| {
                    while let Some(event) = events.next().await {
                        if this
                            .update(cx, |this, cx| this.handle_event(event, cx))
                            .is_err()
                        {
                            break;
                        }
                    }
                }));
                app.load_status(cx);
            }
            Err(err) => app.screen = Screen::Fatal(format!("{err:#}").into()),
        }
        app.start_system(clicks_rx, window, cx);
        app
    }

    pub(crate) fn bridge(&self) -> Option<Bridge> {
        self.bridge.clone()
    }

    pub(crate) fn show_toast(&mut self, text: impl Into<SharedString>, cx: &mut Context<Self>) {
        self.toast = Some((text.into(), std::time::Instant::now()));
        cx.notify();
    }

    pub(crate) fn set_error(&mut self, err: anyhow::Error) {
        self.link_detail = Some(format!("{err:#}").into());
    }

    fn load_status(&mut self, cx: &mut Context<Self>) {
        let Some(bridge) = self.bridge() else { return };
        cx.spawn(async move |this, cx| {
            let status = bridge.call::<Status>("status", json!({})).await;
            let _ = this.update(cx, |this, cx| {
                eprintln!(
                    "[app] bridge status after {} ms",
                    this.started.elapsed().as_millis()
                );
                match status {
                    Ok(status) if status.logged_in => {
                        this.user = status.user;
                        this.screen = Screen::Main;
                        this.connect(cx);
                    }
                    Ok(_) => this.screen = Screen::Login,
                    Err(err) => this.screen = Screen::Fatal(format!("{err:#}").into()),
                }
                cx.notify();
            });
        })
        .detach();
    }

    fn connect(&mut self, cx: &mut Context<Self>) {
        let Some(bridge) = self.bridge() else { return };
        self.link = Link::Connecting;
        cx.spawn(async move |this, cx| {
            if let Err(err) = bridge.request("connect", json!({})).await {
                let _ = this.update(cx, |this, cx| {
                    this.link = Link::Error;
                    this.set_error(err);
                    cx.notify();
                });
            }
        })
        .detach();
        self.load_threads(cx);
    }

    fn load_threads(&mut self, cx: &mut Context<Self>) {
        let Some(bridge) = self.bridge() else { return };
        cx.spawn(async move |this, cx| {
            let threads = bridge
                .call::<Vec<Thread>>("threads", json!({ "limit": 500 }))
                .await;
            let _ = this.update(cx, |this, cx| {
                match threads {
                    Ok(threads) => {
                        this.threads = threads;
                        this.sort_threads();
                        if !this.prefetched && !this.threads.is_empty() {
                            this.prefetched = true;
                            eprintln!(
                                "[app] threads shown after {} ms",
                                this.started.elapsed().as_millis()
                            );
                            let top: Vec<String> =
                                this.threads.iter().take(5).map(|t| t.id.clone()).collect();
                            for id in top {
                                this.prefetch(&id, cx);
                            }
                        }
                    }
                    Err(err) => this.set_error(err),
                }
                cx.notify();
            });
        })
        .detach();
    }

    pub(crate) fn login(&mut self, cx: &mut Context<Self>) {
        let c_user = self.cookie_c_user.read(cx).value().trim().to_string();
        let xs = self.cookie_xs.read(cx).value().trim().to_string();
        let datr = self.cookie_datr.read(cx).value().trim().to_string();
        if c_user.is_empty() || xs.is_empty() || datr.is_empty() {
            self.login_error = Some("Les trois cookies sont nécessaires.".into());
            cx.notify();
            return;
        }
        let cookies = HashMap::from([
            ("c_user".to_string(), c_user),
            ("xs".to_string(), xs),
            ("datr".to_string(), datr),
        ]);
        self.login_with_cookies(cookies, cx);
    }

    pub(crate) fn login_with_cookies(
        &mut self,
        cookies: HashMap<String, String>,
        cx: &mut Context<Self>,
    ) {
        let Some(bridge) = self.bridge() else { return };
        if self.login_busy {
            return;
        }
        self.login_busy = true;
        self.login_error = None;
        cx.notify();
        cx.spawn(async move |this, cx| {
            let result = bridge
                .call::<LoginResult>("login", json!({ "cookies": cookies }))
                .await;
            let _ = this.update(cx, |this, cx| {
                this.login_busy = false;
                match result {
                    Ok(result) => {
                        this.user = Some(result.user);
                        this.screen = Screen::Main;
                        this.connect(cx);
                    }
                    Err(err) => this.login_error = Some(format!("{err:#}").into()),
                }
                cx.notify();
            });
        })
        .detach();
    }

    fn handle_event(&mut self, event: BridgeEvent, cx: &mut Context<Self>) {
        match event.name.as_str() {
            "connection" => {
                if let Ok(conn) = serde_json::from_value::<ConnectionEvent>(event.data) {
                    let was_online = self.link == Link::Online;
                    self.on_connection(conn);
                    if !was_online && self.link == Link::Online {
                        self.flush_offline_queue(cx);
                    }
                }
            }
            "ready" => {
                if let Ok(ready) = serde_json::from_value::<ReadyEvent>(event.data) {
                    self.user = Some(ready.user);
                }
                eprintln!(
                    "[app] ready after {} ms",
                    self.started.elapsed().as_millis()
                );
                self.link = Link::Online;
                self.link_detail = None;
                self.flush_offline_queue(cx);
                self.forget_failed_media();
                self.load_threads(cx);
                self.load_history_status(cx);
            }
            "threads" => {
                if let Ok(update) = serde_json::from_value::<ThreadsEvent>(event.data) {
                    let selected = self.selected.clone();
                    let mut receipts_changed = false;
                    for thread in update.threads {
                        if selected.as_deref() == Some(thread.id.as_str()) {
                            receipts_changed = self.selected_thread().is_none_or(|current| {
                                current.read_receipts.len() != thread.read_receipts.len()
                                    || current.read_receipts.iter().zip(&thread.read_receipts).any(
                                        |(a, b)| {
                                            a.user_id != b.user_id
                                                || a.read_ms != b.read_ms
                                                || a.delivered_ms != b.delivered_ms
                                        },
                                    )
                            });
                        }
                        self.upsert_thread(thread);
                    }
                    self.sort_threads();
                    if receipts_changed {
                        self.scroller.update(cx, |scroller, cx| scroller.remeasure(cx));
                    }
                }
            }
            "thread_removed" => {
                if let Ok(removed) = serde_json::from_value::<ThreadRemovedEvent>(event.data) {
                    self.threads.retain(|thread| thread.id != removed.thread_id);
                    self.messages.remove(&removed.thread_id);
                    if self.selected.as_deref() == Some(removed.thread_id.as_str()) {
                        self.clear_selection(cx);
                    }
                }
            }
            "message" => {
                if let Ok(message) = serde_json::from_value::<Message>(event.data) {
                    self.on_message(message, cx);
                }
            }
            "message_updated" => {
                if let Ok(message) = serde_json::from_value::<Message>(event.data) {
                    self.on_message_updated(message, cx);
                }
            }
            "export_progress" | "save_media_progress" => {
                let name = event.name.clone();
                self.on_export_event(&name, event.data, cx);
            }
            "reaction_added" => {
                if let Ok(reaction) = serde_json::from_value::<ReactionAddedEvent>(event.data) {
                    self.notify_reaction(reaction, cx);
                }
            }
            "message_removed" => {
                if let Ok(removed) = serde_json::from_value::<MessageRemovedEvent>(event.data) {
                    self.on_message_removed(&removed.thread_id, &removed.message_id, cx);
                }
            }
            "messages_backfilled" => {
                if let Ok(backfill) = serde_json::from_value::<BackfillEvent>(event.data) {
                    self.on_backfill(backfill, cx);
                }
            }
            "presence" => {
                if let Ok(presence) = serde_json::from_value::<PresenceEvent>(event.data) {
                    self.presence.insert(presence.user_id, presence.online);
                }
            }
            "typing" => {
                if let Ok(typing) = serde_json::from_value::<TypingEvent>(event.data) {
                    let me = self.user.as_ref().map(|user| user.id.as_str());
                    if me != Some(typing.user_id.as_str()) {
                        self.typing.insert(typing.thread_id, typing.typing);
                    }
                }
            }
            _ => {}
        }
        cx.notify();
    }

    fn on_connection(&mut self, conn: ConnectionEvent) {
        self.link = match conn.state.as_str() {
            "connecting" => Link::Connecting,
            "connected" => Link::Online,
            "disconnected" => Link::Reconnecting,
            _ => Link::Error,
        };
        if self.link == Link::Online {
            self.forget_failed_media();
        }
        self.link_detail = conn.detail.clone().map(Into::into);
        if conn.permanent {
            if conn.detail.as_deref() == Some("bridge process exited") {
                self.screen = Screen::Fatal("Le bridge Messenger s'est arrêté.".into());
            } else {
                self.reset_account_state();
                self.screen = Screen::Login;
                self.login_error = Some(
                    format!(
                        "Session expirée ou refusée par Meta : {}",
                        conn.detail.unwrap_or_default()
                    )
                    .into(),
                );
            }
        }
    }

    pub(crate) fn report_activity(&self, cx: &mut Context<Self>) {
        let active = self.window_active || self.popout_active.values().any(|active| *active);
        let Some(bridge) = self.bridge() else { return };
        cx.background_spawn(async move {
            let _ = bridge
                .request("set_active", json!({ "active": active }))
                .await;
        })
        .detach();
    }

    pub(crate) fn set_popout_active(
        &mut self,
        thread_id: &str,
        active: Option<bool>,
        cx: &mut Context<Self>,
    ) {
        match active {
            Some(active) => {
                self.popout_active.insert(thread_id.to_string(), active);
                if active {
                    self.auto_mark_read(thread_id, cx);
                }
            }
            None => {
                self.popout_active.remove(thread_id);
            }
        }
        self.report_activity(cx);
    }

    pub(crate) fn forget_failed_media(&mut self) {
        self.media
            .retain(|_, state| !matches!(state, Media::Failed));
    }

    pub(crate) fn erase_local_account_data(&mut self) {
        self.drafts.clear();
        crate::storage::save_json(DRAFTS_FILE, &self.drafts);
        self.drafts_saved.clear();
        for file in ["planner.json", "favorites.json"] {
            if let Some(path) = crate::storage::data_file(file) {
                let _ = std::fs::remove_file(path);
            }
        }
        self.planner = planner::Planner::default();
        self.settings.locally_unread.clear();
        self.settings.locally_archived.clear();
        self.settings.priority_threads.clear();
        for folder in &mut self.settings.folders {
            folder.thread_ids.clear();
        }
        self.settings.save();
    }

    pub(crate) fn reset_account_state(&mut self) {
        self.user = None;
        self.threads.clear();
        self.messages.clear();
        self.selected = None;
        self.media.clear();
        self.typing.clear();
        self.presence.clear();
        self.history = History::Unknown;
        self.gallery = None;
        self.lightbox = None;
        self.switcher = None;
        self.reply_to = None;
        self.pending_files.clear();
        self.react_picker = None;
        self.reaction_details = None;
        self.message_ops.reset();
        self.thread_ops.reset();
        self.voice.reset();
        self.older.clear();
        self.exports = Default::default();
        self.read_sent.clear();
        self.outgoing.clear();
        self.outgoing_requests.clear();
        self.find = find::Find::default();
        self.search_hits.clear();
        self.search_query.clear();
        self.buffered_live.clear();
        self.loading_threads.clear();
        self.pending_scroll = None;
        self.typing_sent = None;
        self.info_open = false;
        self.settings_open = false;
        self.popout_active.clear();
        for generation in self.thread_gen.values_mut() {
            *generation += 1;
        }
    }

    fn upsert_thread(&mut self, thread: Thread) {
        match self
            .threads
            .iter_mut()
            .find(|existing| existing.id == thread.id)
        {
            Some(existing) => *existing = thread,
            None => self.threads.push(thread),
        }
    }

    pub(crate) fn sort_threads(&mut self) {
        self.threads
            .sort_by(|a, b| b.last_activity_ms.cmp(&a.last_activity_ms));
    }

    fn notify_message(&self, message: &Message, is_selected: bool, cx: &mut Context<Self>) {
        if !self.settings.notifications
            || message.is_outgoing
            || self.is_thread_in_view(&message.thread_id, is_selected)
        {
            return;
        }
        let thread = self.threads.iter().find(|t| t.id == message.thread_id);
        if !self.should_notify(message, thread) {
            return;
        }
        let (title, is_group) = match thread {
            Some(thread) => (thread.name.clone(), thread.is_group),
            None if !message.sender_name.is_empty() => (message.sender_name.clone(), false),
            None => ("Nouveau message".to_string(), false),
        };
        let text = if !self.settings.notification_preview {
            "Nouveau message".to_string()
        } else if message.text.is_empty() {
            describe_attachments(message)
        } else {
            message.text.clone()
        };
        let body = if is_group && !message.sender_name.is_empty() {
            format!("{} : {text}", message.sender_name)
        } else {
            text
        };
        let notification = Notification {
            title,
            body,
            thread_id: message.thread_id.clone(),
        };
        let clicks = self.clicks.clone();
        cx.background_spawn(async move { notifications::show(notification, clicks) })
            .detach();
    }

    fn notify_reaction(&self, reaction: ReactionAddedEvent, cx: &mut Context<Self>) {
        let is_selected = self.selected.as_deref() == Some(reaction.thread_id.as_str());
        if !self.settings.notifications || self.is_thread_in_view(&reaction.thread_id, is_selected) {
            return;
        }
        let thread = self.threads.iter().find(|t| t.id == reaction.thread_id);
        let probe = Message {
            thread_id: reaction.thread_id.clone(),
            ..Message::default()
        };
        if !self.should_notify(&probe, thread) {
            return;
        }
        let actor = if reaction.actor_name.trim().is_empty() {
            "Quelqu'un".to_string()
        } else {
            reaction.actor_name.clone()
        };
        let title = match thread {
            Some(thread) if thread.is_group => thread.name.clone(),
            _ => actor.clone(),
        };
        let body = if !self.settings.notification_preview {
            "Nouvelle réaction à votre message".to_string()
        } else {
            let preview = reaction.target_preview.trim();
            let mut body = format!("{actor} a réagi {} à votre message", reaction.emoji);
            if !preview.is_empty() {
                body.push_str(&format!(" : « {preview} »"));
            }
            body
        };
        let notification = Notification {
            title,
            body,
            thread_id: reaction.thread_id,
        };
        let clicks = self.clicks.clone();
        cx.background_spawn(async move { notifications::show(notification, clicks) })
            .detach();
    }

    pub(crate) fn is_thread_in_view(&self, thread_id: &str, is_selected: bool) -> bool {
        (self.window_active && is_selected)
            || self.popout_active.get(thread_id).copied().unwrap_or(false)
    }

    fn on_backfill(&mut self, backfill: BackfillEvent, cx: &mut Context<Self>) {
        let thread_id = backfill.thread_id;
        if self.loading_threads.contains(&thread_id) {
            self.buffered_live
                .entry(thread_id)
                .or_default()
                .extend(backfill.messages);
            return;
        }
        let is_selected = self.selected.as_deref() == Some(thread_id.as_str());
        let Some(list) = self.messages.get_mut(&thread_id) else {
            return;
        };
        let known: std::collections::HashSet<String> = list.iter().map(|m| m.id.clone()).collect();
        let oldest = list.first().map(|m| m.timestamp_ms).unwrap_or(i64::MAX);
        let (mut older, mut newer): (Vec<Message>, Vec<Message>) = backfill
            .messages
            .into_iter()
            .filter(|m| !known.contains(&m.id))
            .partition(|m| m.timestamp_ms <= oldest);
        if !newer.is_empty() {
            newer.sort_by_key(|m| m.timestamp_ms);
            let newest = newer.last().cloned();
            for message in newer {
                self.insert_message_sorted(message, is_selected, cx);
            }
            let is_last = newest.as_ref().is_some_and(|newest| {
                self.messages
                    .get(&thread_id)
                    .and_then(|list| list.last())
                    .is_some_and(|last| last.id == newest.id)
            });
            if let (true, Some(newest)) = (is_last, newest) {
                if let Some(thread) = self.threads.iter_mut().find(|t| t.id == thread_id) {
                    thread.snippet = preview(&newest);
                    thread.last_activity_ms = thread.last_activity_ms.max(newest.timestamp_ms);
                }
                self.sort_threads();
            }
            if self.is_thread_in_view(&thread_id, is_selected) {
                self.auto_mark_read(&thread_id, cx);
            }
        }
        if older.is_empty() {
            return;
        }
        let Some(list) = self.messages.get_mut(&thread_id) else {
            return;
        };
        older.sort_by_key(|m| m.timestamp_ms);
        let count = older.len();
        older.extend(list.drain(..));
        *list = older;
        if is_selected {
            self.scroller.update(cx, |scroller, cx| {
                if scroller.item_count() == 0 {
                    scroller.reset(count, cx);
                    scroller.scroll_to_end(cx);
                } else {
                    scroller.prepend(count, cx);
                    scroller.remeasure_items(count..count + 1, cx);
                }
            });
        }
        if let Some(state) = self.older.get_mut(&thread_id) {
            state.exhausted = false;
        }
    }

    fn on_message(&mut self, message: Message, cx: &mut Context<Self>) {
        let is_selected = self.selected.as_deref() == Some(message.thread_id.as_str());
        let in_view = self.is_thread_in_view(&message.thread_id, is_selected);
        self.typing.remove(&message.thread_id);
        self.notify_message(&message, is_selected, cx);
        let receipts = self.settings.send_read_receipts;

        if let Some(thread) = self.threads.iter_mut().find(|t| t.id == message.thread_id) {
            thread.snippet = preview(&message);
            thread.last_activity_ms = thread.last_activity_ms.max(message.timestamp_ms);
            if !message.is_outgoing && (!in_view || !receipts) {
                thread.unread_count += 1;
            }
        }
        self.sort_threads();

        if self.confirm_echo(&message, cx) {
            return;
        }
        if self.loading_threads.contains(&message.thread_id) {
            self.buffered_live
                .entry(message.thread_id.clone())
                .or_default()
                .push(message);
            return;
        }
        let message_thread = message.thread_id.clone();
        if self.insert_message_sorted(message, is_selected, cx) && in_view {
            self.auto_mark_read(&message_thread, cx);
        }
    }

    fn insert_message_sorted(
        &mut self,
        message: Message,
        is_selected: bool,
        cx: &mut Context<Self>,
    ) -> bool {
        let Some(list) = self.messages.get_mut(&message.thread_id) else {
            return false;
        };
        if list.iter().any(|existing| existing.id == message.id) {
            return false;
        }
        let position =
            list.partition_point(|existing| existing.timestamp_ms <= message.timestamp_ms);
        let appended = position == list.len();
        list.insert(position, message);
        if is_selected {
            self.scroller.update(cx, |scroller, cx| {
                if appended {
                    scroller.append(1, cx);
                    if position > 0 {
                        scroller.remeasure_items(position - 1..position, cx);
                    }
                } else {
                    scroller.splice(position..position, 1, cx);
                }
            });
        }
        true
    }

    fn load_history_status(&mut self, cx: &mut Context<Self>) {
        let Some(bridge) = self.bridge() else { return };
        cx.spawn(async move |this, cx| {
            let status = bridge
                .call::<HistoryStatus>("history_status", json!({}))
                .await;
            let _ = this.update(cx, |this, cx| {
                this.history = match status {
                    Ok(status) if status.restored => History::Restored,
                    Ok(status) if status.backup == "available" => History::Available,
                    Ok(status) if status.backup == "none" => History::None,
                    _ => History::Unknown,
                };
                cx.notify();
            });
        })
        .detach();
    }

    pub(crate) fn open_pin(&mut self, window: &mut Window, cx: &mut Context<Self>) {
        self.pin_open = true;
        self.pin_error = None;
        self.pin_input
            .update(cx, |input, cx| input.focus(window, cx));
        cx.notify();
    }

    pub(crate) fn close_pin(&mut self, window: &mut Window, cx: &mut Context<Self>) {
        self.pin_open = false;
        self.pin_input
            .update(cx, |input, cx| input.clean(window, cx));
        cx.notify();
    }

    pub(crate) fn restore_history(&mut self, cx: &mut Context<Self>) {
        if self.pin_busy {
            return;
        }
        let Some(bridge) = self.bridge() else { return };
        let pin = self.pin_input.read(cx).value().trim().to_string();
        if pin.len() != 6 || !pin.chars().all(|c| c.is_ascii_digit()) {
            self.pin_error = Some("Le PIN compte exactement 6 chiffres.".into());
            cx.notify();
            return;
        }
        self.pin_busy = true;
        self.pin_error = None;
        cx.notify();
        cx.spawn(async move |this, cx| {
            let result = bridge
                .request("restore_history", json!({ "pin": pin }))
                .await;
            let _ = this.update(cx, |this, cx| {
                this.pin_busy = false;
                match result {
                    Ok(_) => {
                        this.history = History::Restored;
                        this.pin_open = false;
                        this.messages.clear();
                        this.older.clear();
                        this.forget_failed_media();
                        for generation in this.thread_gen.values_mut() {
                            *generation += 1;
                        }
                        if let Some(thread_id) = this.selected.clone() {
                            this.scroller
                                .update(cx, |scroller, cx| scroller.reset(0, cx));
                            this.load_messages(thread_id, cx);
                        }
                        this.load_threads(cx);
                    }
                    Err(err) => this.pin_error = Some(pin_error_message(&err).into()),
                }
                cx.notify();
            });
        })
        .detach();
    }

    pub(crate) fn total_unread(&self) -> u32 {
        self.threads
            .iter()
            .filter(|thread| !self.thread_is_muted(thread) && !self.is_request_thread(thread))
            .map(|thread| match thread.unread_count {
                0 if self.has_unread(thread) => 1,
                count => count,
            })
            .sum()
    }

    fn on_message_updated(&mut self, message: Message, cx: &mut Context<Self>) {
        let is_selected = self.selected.as_deref() == Some(message.thread_id.as_str());
        let Some(list) = self.messages.get_mut(&message.thread_id) else {
            return;
        };
        if let Some(index) = list.iter().position(|existing| existing.id == message.id) {
            list[index] = message;
            if is_selected {
                self.scroller.update(cx, |scroller, cx| {
                    scroller.remeasure_items(index..index + 1, cx)
                });
            }
        }
    }

    pub(crate) fn on_message_removed(
        &mut self,
        thread_id: &str,
        message_id: &str,
        cx: &mut Context<Self>,
    ) {
        let is_selected = self.selected.as_deref() == Some(thread_id);
        let Some(list) = self.messages.get_mut(thread_id) else {
            return;
        };
        let Some(index) = list.iter().position(|m| m.id == message_id) else {
            return;
        };
        list.remove(index);
        if self.reply_to.as_ref().is_some_and(|m| m.id == message_id) {
            self.reply_to = None;
        }
        if is_selected {
            self.scroller
                .update(cx, |scroller, cx| scroller.splice(index..index + 1, 0, cx));
            self.remeasure_last_outgoing(thread_id, cx);
        }
        cx.notify();
    }

    pub(crate) fn select_thread(
        &mut self,
        thread_id: String,
        window: &mut Window,
        cx: &mut Context<Self>,
    ) {
        if self.selected.as_deref() == Some(thread_id.as_str()) {
            return;
        }
        self.close_planner_dialogs(cx);
        self.stop_typing(cx);
        self.cancel_edit(Some(window), cx);
        let draft = self.composer.read(cx).value().to_string();
        if let Some(previous) = self.selected.clone() {
            if draft.trim().is_empty() {
                self.drafts.remove(&previous);
            } else {
                self.drafts.insert(previous, draft);
            }
        }
        let next_draft = self.drafts.remove(&thread_id).unwrap_or_default();
        self.composer
            .update(cx, |input, cx| input.set_value(next_draft, window, cx));
        self.selected = Some(thread_id.clone());
        self.reply_to = None;
        self.pending_files.clear();
        self.react_picker = None;
        self.reaction_details = None;
        self.message_ops.on_thread_changed();
        self.thread_ops.on_thread_changed();
        self.voice.on_thread_changed();
        self.find.on_thread_changed();
        if self
            .gallery
            .as_ref()
            .is_some_and(|g| g.thread_id != thread_id)
        {
            self.gallery = None;
        }
        let cached = self.messages.get(&thread_id).map(Vec::len);
        self.scroller
            .update(cx, |scroller, cx| scroller.reset(cached.unwrap_or(0), cx));
        if cached.is_none() {
            self.load_messages(thread_id, cx);
        }
        self.mark_read(cx);
        self.composer
            .update(cx, |input, cx| input.focus(window, cx));
        cx.notify();
    }

    pub(crate) fn generation(&self, thread_id: &str) -> u64 {
        self.thread_gen.get(thread_id).copied().unwrap_or(0)
    }

    pub(crate) fn load_messages(&mut self, thread_id: String, cx: &mut Context<Self>) {
        self.load_messages_inner(thread_id, true, cx);
    }

    pub(crate) fn is_loading_thread(&self, thread_id: &str) -> bool {
        self.loading_threads.contains(thread_id) && !self.messages.contains_key(thread_id)
    }

    pub(crate) fn prefetch(&mut self, thread_id: &str, cx: &mut Context<Self>) {
        if self.messages.contains_key(thread_id) || self.loading_threads.contains(thread_id) {
            return;
        }
        self.load_messages_inner(thread_id.to_string(), false, cx);
    }

    fn load_messages_inner(
        &mut self,
        thread_id: String,
        fetch_missing: bool,
        cx: &mut Context<Self>,
    ) {
        let Some(bridge) = self.bridge() else { return };
        if !self.loading_threads.insert(thread_id.clone()) {
            return;
        }
        let generation = {
            let entry = self.thread_gen.entry(thread_id.clone()).or_insert(0);
            *entry += 1;
            *entry
        };
        cx.spawn(async move |this, cx| {
            let result = bridge
                .call::<Vec<Message>>(
                    "messages",
                    json!({ "thread_id": thread_id, "limit": 80, "fetch_missing": fetch_missing }),
                )
                .await;
            let _ = this.update(cx, |this, cx| {
                this.loading_threads.remove(&thread_id);
                let buffered = this.buffered_live.remove(&thread_id).unwrap_or_default();
                if this.generation(&thread_id) != generation {
                    return;
                }
                match result {
                    Ok(mut messages) => {
                        for live in buffered {
                            if !messages.iter().any(|m| m.id == live.id) {
                                messages.push(live);
                            }
                        }
                        messages.sort_by_key(|message| message.timestamp_ms);
                        let count = messages.len();
                        let target = this
                            .pending_scroll
                            .as_ref()
                            .filter(|(thread, _)| *thread == thread_id)
                            .and_then(|(_, message_id)| {
                                messages.iter().position(|m| &m.id == message_id)
                            });
                        this.messages.insert(thread_id.clone(), messages);
                        if this.selected.as_deref() == Some(thread_id.as_str()) {
                            this.scroller.update(cx, |scroller, cx| {
                                scroller.reset(count, cx);
                                match target {
                                    Some(index) => {
                                        scroller.scroll_to_item(index, cx);
                                    }
                                    None => scroller.scroll_to_end(cx),
                                }
                            });
                            this.pending_scroll = None;
                            this.auto_mark_read(&thread_id, cx);
                        }
                    }
                    Err(err) => this.set_error(err),
                }
                cx.notify();
            });
        })
        .detach();
    }

    pub(crate) fn mark_read(&mut self, cx: &mut Context<Self>) {
        if let Some(thread_id) = self.selected.clone() {
            self.auto_mark_read(&thread_id, cx);
        }
    }

    pub(crate) fn auto_mark_read(&mut self, thread_id: &str, cx: &mut Context<Self>) {
        if self.settings.send_read_receipts {
            self.mark_thread_read(thread_id, cx);
        }
    }

    pub(crate) fn persist_drafts(&mut self, cx: &App) {
        if self.message_ops.editing.is_some() {
            return;
        }
        let mut current = self.drafts.clone();
        if let Some(thread_id) = self.selected.as_ref() {
            let text = self.composer.read(cx).value().to_string();
            if text.trim().is_empty() {
                current.remove(thread_id);
            } else {
                current.insert(thread_id.clone(), text);
            }
        }
        if current != self.drafts_saved {
            crate::storage::save_json(DRAFTS_FILE, &current);
            self.drafts_saved = current;
        }
    }

    pub(crate) fn mark_thread_read(&mut self, thread_id: &str, cx: &mut Context<Self>) {
        let thread_id = thread_id.to_string();
        if self
            .threads
            .iter()
            .any(|t| t.id == thread_id && self.is_request_thread(t))
        {
            return;
        }
        let Some(thread) = self.threads.iter_mut().find(|t| t.id == thread_id) else {
            return;
        };
        let activity = self
            .messages
            .get(&thread_id)
            .and_then(|list| list.iter().rev().find(|m| !m.is_outgoing))
            .map(|m| m.timestamp_ms)
            .unwrap_or(thread.last_activity_ms);
        let already_sent = self
            .read_sent
            .get(&thread_id)
            .is_some_and(|sent| *sent >= activity);
        if thread.unread_count == 0 && already_sent {
            return;
        }
        self.read_sent.insert(thread_id.clone(), activity);
        thread.unread_count = 0;
        let Some(bridge) = self.bridge() else { return };
        cx.background_spawn(async move {
            let _ = bridge
                .request("mark_read", json!({ "thread_id": thread_id }))
                .await;
        })
        .detach();
    }

    pub(crate) fn thread_is_muted(&self, thread: &Thread) -> bool {
        if !thread.muted {
            return false;
        }
        match thread.muted_until_ms {
            None | Some(-1) => true,
            Some(until) => until > chrono::Utc::now().timestamp_millis(),
        }
    }

    pub(crate) fn clear_selection(&mut self, cx: &mut Context<Self>) {
        self.stop_typing(cx);
        self.cancel_voice_recording(cx);
        self.selected = None;
        self.reply_to = None;
        self.pending_files.clear();
        self.react_picker = None;
        self.reaction_details = None;
        self.message_ops.on_thread_changed();
        self.thread_ops.on_thread_changed();
        self.voice.on_thread_changed();
        self.scroller.update(cx, |scroller, cx| scroller.reset(0, cx));
        cx.notify();
    }

    pub(crate) fn selected_thread(&self) -> Option<&Thread> {
        let id = self.selected.as_deref()?;
        self.threads.iter().find(|thread| thread.id == id)
    }

    pub(crate) fn visible_threads(&self, cx: &App) -> Vec<usize> {
        let query = self.search.read(cx).value().trim().to_lowercase();
        self.threads
            .iter()
            .enumerate()
            .filter(|(_, thread)| self.thread_matches_filter(thread, self.filter))
            .filter(|(_, thread)| query.is_empty() || thread.name.to_lowercase().contains(&query))
            .map(|(index, _)| index)
            .collect()
    }

    pub(crate) fn set_filter(&mut self, filter: Filter, cx: &mut Context<Self>) {
        self.filter = filter;
        cx.notify();
    }

    pub(crate) fn toggle_info(&mut self, cx: &mut Context<Self>) {
        self.info_open = !self.info_open;
        cx.notify();
    }

    pub(crate) fn is_online(&self, thread: &Thread) -> bool {
        if thread.is_group {
            return false;
        }
        thread
            .peer_id
            .as_ref()
            .and_then(|peer| self.presence.get(peer))
            .copied()
            .or(thread.peer_online)
            .unwrap_or(false)
    }

    pub(crate) fn activity_label(&self, thread: &Thread) -> Option<String> {
        if thread.is_group {
            let me = self.user.as_ref().map(|user| user.id.as_str());
            let online = thread
                .participants
                .iter()
                .filter(|p| Some(p.user_id.as_str()) != me && self.participant_online(p))
                .count();
            return (online > 0).then(|| match online {
                1 => "1 personne en ligne".to_string(),
                n => format!("{n} personnes en ligne"),
            });
        }
        if self.is_online(thread) {
            return Some("En ligne".to_string());
        }
        thread.peer_last_active_ms.map(last_active_label)
    }

    pub(crate) fn participant_online(&self, participant: &Participant) -> bool {
        self.presence
            .get(&participant.user_id)
            .copied()
            .or(participant.online)
            .unwrap_or(false)
    }

    pub(crate) fn participant_status(&self, participant: &Participant) -> Option<String> {
        if self.participant_online(participant) {
            return Some("En ligne".to_string());
        }
        participant.last_active_ms.map(last_active_label)
    }
}

pub(crate) fn action_error(err: &anyhow::Error) -> String {
    match err.downcast_ref::<BridgeError>() {
        Some(bridge_error) if bridge_error.code == "unsupported" => {
            "Messenger ne permet pas cette action dans cette conversation.".into()
        }
        Some(bridge_error) if bridge_error.code == "not_found" => {
            "Élément introuvable, il a peut-être été supprimé.".into()
        }
        Some(bridge_error) => bridge_error.message.clone(),
        None => format!("{err:#}"),
    }
}

fn pin_error_message(err: &anyhow::Error) -> String {
    let Some(bridge_error) = err.downcast_ref::<BridgeError>() else {
        return format!("{err:#}");
    };
    match bridge_error.code.as_str() {
        "bad_pin" => {
            let remaining = bridge_error
                .data
                .as_ref()
                .and_then(|data| data.get("attempts_remaining"))
                .and_then(|value| value.as_i64());
            match remaining {
                Some(n) => format!("PIN incorrect. Il reste {n} essai(s) avant blocage par Meta."),
                None => "PIN incorrect.".into(),
            }
        }
        "pin_locked" => {
            "Trop d'essais : Meta a bloqué la restauration par PIN pour ce compte.".into()
        }
        "unsupported" => "Aucune sauvegarde chiffrée n'existe pour ce compte.".into(),
        _ => bridge_error.message.clone(),
    }
}

pub(crate) fn last_active_label(timestamp_ms: i64) -> String {
    let elapsed_min = (chrono::Utc::now().timestamp_millis() - timestamp_ms).max(0) / 60_000;
    match elapsed_min {
        0 => "Actif à l'instant".to_string(),
        1..=59 => format!("Actif il y a {elapsed_min} min"),
        60..=1439 => format!("Actif il y a {} h", elapsed_min / 60),
        1440..=2879 => "Actif hier".to_string(),
        _ => format!("Actif il y a {} j", elapsed_min / 1440),
    }
}

pub(crate) fn preview(message: &Message) -> String {
    let body = if message.text.is_empty() {
        describe_attachments(message)
    } else {
        message.text.clone()
    };
    if message.is_outgoing {
        format!("Vous : {body}")
    } else {
        body
    }
}

pub(crate) fn describe_attachments(message: &Message) -> String {
    match message.attachments.first().map(|a| a.kind.as_str()) {
        Some("image") => "Photo".into(),
        Some("video") if message.attachments[0].gif => "GIF".into(),
        Some("video") => "Vidéo".into(),
        Some("audio") => "Message vocal".into(),
        Some("sticker") => "Sticker".into(),
        Some("link") => message.attachments[0]
            .title
            .clone()
            .unwrap_or_else(|| "Lien".into()),
        Some("file") => message.attachments[0]
            .file_name
            .clone()
            .unwrap_or_else(|| "Fichier".into()),
        Some(_) => "Pièce jointe".into(),
        None => String::new(),
    }
}
