use std::{
    collections::HashMap,
    time::{Duration, Instant},
};

use chrono::{
    Datelike as _, Duration as Span, Local, NaiveDate, NaiveDateTime, NaiveTime, TimeZone as _,
    Timelike as _,
};
use gpui_kit::component::input::{InputEvent, InputState, Position};
use gpui_kit::*;
use serde::{Deserialize, Serialize};

use super::{Link, MissiveApp, OutgoingRequest, Screen, describe_attachments, library::fold};
use crate::{
    model::{Mention, Message},
    notifications::{self, Notification},
    settings::Snippet,
    timefmt::{self, Day},
};

const PLANNER_FILE: &str = "planner.json";
const FAVORITES_FILE: &str = "favorites.json";
const MINUTE_MS: i64 = 60_000;
const HOUR_MS: i64 = 60 * MINUTE_MS;
pub(crate) const OVERDUE_GRACE_MS: i64 = 15 * MINUTE_MS;
const MAX_AHEAD_DAYS: i64 = 365;
const FIRED_KEEP_MS: i64 = 24 * HOUR_MS;
const MAX_SNIPPETS: usize = 6;
const MAX_SNIPPET_QUERY: usize = 30;
const JUMP_PATIENCE: Duration = Duration::from_secs(30);
const PREVIEW_CHARS: usize = 160;
const STABLE_ONLINE_MS: i64 = 20_000;
const RESUME_GAP_MS: i64 = 5_000;
const PREFETCH_BACKOFF: Duration = Duration::from_secs(30);

#[derive(Clone, Debug, Default, Serialize, Deserialize)]
#[serde(default)]
pub(crate) struct ScheduledItem {
    pub(crate) id: String,
    pub(crate) thread_id: String,
    pub(crate) thread_name: String,
    pub(crate) text: String,
    pub(crate) mentions: Vec<Mention>,
    pub(crate) reply_to_id: Option<String>,
    pub(crate) send_at_ms: i64,
    pub(crate) created_ms: i64,
    pub(crate) overdue: bool,
    pub(crate) sending: bool,
    pub(crate) failure: Option<String>,
    pub(crate) client_id: Option<String>,
}

impl ScheduledItem {
    pub(crate) fn waiting(&self) -> bool {
        !self.overdue && !self.sending && self.failure.is_none()
    }

    pub(crate) fn needs_decision(&self) -> bool {
        !self.sending && (self.overdue || self.failure.is_some())
    }
}

#[derive(Clone, Debug, Default, Serialize, Deserialize)]
#[serde(default)]
pub(crate) struct Reminder {
    pub(crate) id: String,
    pub(crate) thread_id: String,
    pub(crate) thread_name: String,
    pub(crate) message_id: String,
    pub(crate) preview: String,
    pub(crate) due_ms: i64,
    pub(crate) fired_ms: Option<i64>,
}

#[derive(Clone, Debug, Default, Serialize, Deserialize)]
#[serde(default)]
pub(crate) struct Favorite {
    pub(crate) thread_id: String,
    pub(crate) thread_name: String,
    pub(crate) message_id: String,
    pub(crate) sender_name: String,
    pub(crate) text: String,
    pub(crate) timestamp_ms: i64,
    pub(crate) kind: String,
    pub(crate) added_ms: i64,
}

#[derive(Default, Serialize, Deserialize)]
#[serde(default)]
struct PlannerFile {
    scheduled: Vec<ScheduledItem>,
    reminders: Vec<Reminder>,
}

#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub(crate) enum Due {
    Wait,
    Now,
    Overdue,
}

pub(crate) fn due_state(send_at_ms: i64, now_ms: i64) -> Due {
    if now_ms < send_at_ms {
        Due::Wait
    } else if now_ms - send_at_ms > OVERDUE_GRACE_MS {
        Due::Overdue
    } else {
        Due::Now
    }
}

pub(crate) fn stable_online(
    online: bool,
    since_ms: Option<i64>,
    last_tick_ms: Option<i64>,
    now_ms: i64,
) -> (Option<i64>, bool) {
    if !online {
        return (None, false);
    }
    let resumed = last_tick_ms.is_some_and(|last| now_ms - last > RESUME_GAP_MS);
    let since = match since_ms {
        Some(since) if !resumed => since,
        _ => now_ms,
    };
    (Some(since), now_ms - since >= STABLE_ONLINE_MS)
}

fn hm(hour: u32, minute: u32) -> NaiveTime {
    NaiveTime::from_hms_opt(hour, minute, 0).unwrap_or_default()
}

fn next_monday(today: NaiveDate) -> NaiveDate {
    let days = 7 - i64::from(today.weekday().num_days_from_monday());
    today + Span::days(days)
}

fn tonight(now: NaiveDateTime) -> Option<NaiveDateTime> {
    (now.time() < hm(19, 30)).then(|| now.date().and_time(hm(20, 0)))
}

pub(crate) fn schedule_presets(now: NaiveDateTime) -> Vec<(SharedString, NaiveDateTime)> {
    let today = now.date();
    let mut presets = Vec::new();
    if let Some(at) = tonight(now) {
        presets.push((ts!("planner.preset.tonight"), at));
    }
    presets.push((
        ts!("planner.preset.tomorrow_morning"),
        (today + Span::days(1)).and_time(hm(8, 0)),
    ));
    presets.push((
        ts!("planner.preset.monday"),
        next_monday(today).and_time(hm(9, 0)),
    ));
    presets
}

pub(crate) fn reminder_presets(now: NaiveDateTime) -> Vec<(SharedString, NaiveDateTime)> {
    let base = now
        .with_second(0)
        .and_then(|at| at.with_nanosecond(0))
        .unwrap_or(now);
    let mut presets = vec![(ts!("planner.preset.in_one_hour"), base + Span::hours(1))];
    if let Some(at) = tonight(now) {
        presets.push((ts!("planner.preset.tonight"), at));
    }
    presets.push((
        ts!("planner.preset.tomorrow_nine"),
        (now.date() + Span::days(1)).and_time(hm(9, 0)),
    ));
    presets
}

fn parse_time(text: &str) -> Option<NaiveTime> {
    let lower = text.trim().to_lowercase();
    let (hour, minute) = match lower.split_once([':', 'h']) {
        Some((hour, minute)) => (hour.trim(), minute.trim()),
        None => (lower.as_str(), ""),
    };
    if hour.is_empty() || hour.len() > 2 || !hour.chars().all(|c| c.is_ascii_digit()) {
        return None;
    }
    if minute.len() > 2 || !minute.chars().all(|c| c.is_ascii_digit()) {
        return None;
    }
    let minute = if minute.is_empty() {
        0
    } else {
        minute.parse().ok()?
    };
    NaiveTime::from_hms_opt(hour.parse().ok()?, minute, 0)
}

pub(crate) fn parse_when(
    date: &str,
    time: &str,
    now: NaiveDateTime,
) -> Result<NaiveDateTime, SharedString> {
    let date =
        timefmt::parse_input_date(date, now.date()).ok_or_else(|| ts!("planner.picker.invalid_date"))?;
    let time = parse_time(time).ok_or_else(|| ts!("planner.picker.invalid_time"))?;
    let at = date.and_time(time);
    if at <= now {
        Err(ts!("planner.picker.in_the_past"))
    } else if at > now + Span::days(MAX_AHEAD_DAYS) {
        Err(ts!("planner.picker.too_far"))
    } else {
        Ok(at)
    }
}

pub(crate) fn format_when(target: NaiveDateTime, now: NaiveDateTime) -> String {
    let clock = timefmt::clock_compact(&target);
    let label = match timefmt::relative_day(target.date(), now.date()) {
        Day::Today => t!("planner.when.today", clock = clock),
        Day::Tomorrow => t!("planner.when.tomorrow", clock = clock),
        Day::Yesterday => t!("planner.when.yesterday", clock = clock),
        Day::NextWeek(weekday) => t!(
            "planner.when.weekday",
            weekday = timefmt::weekday_name(weekday),
            clock = clock
        ),
        _ if target.year() == now.year() => t!(
            "planner.when.date",
            day = target.day(),
            month = timefmt::month_short(target.month()),
            clock = clock
        ),
        _ => t!(
            "planner.when.date_year",
            day = target.day(),
            month = timefmt::month_short(target.month()),
            year = target.year(),
            clock = clock
        ),
    };
    label.to_string()
}

pub(crate) fn snippet_query(text: &str) -> Option<(usize, String)> {
    let slash = text.rfind('/')?;
    let before_ok = text[..slash].chars().last().is_none_or(char::is_whitespace);
    let query = &text[slash + 1..];
    let valid = !query.is_empty()
        && query.chars().count() <= MAX_SNIPPET_QUERY
        && query
            .chars()
            .all(|c| c.is_alphanumeric() || c == '-' || c == '_');
    (before_ok && valid).then(|| (slash, query.to_string()))
}

fn snippet_key(snippet: &Snippet) -> String {
    fold(snippet.shortcut.trim().trim_start_matches('/'))
}

pub(crate) fn usable_snippets(snippets: &[Snippet]) -> bool {
    snippets
        .iter()
        .any(|snippet| !snippet_key(snippet).is_empty() && !snippet.text.trim().is_empty())
}

pub(crate) fn match_snippets(snippets: &[Snippet], query: &str) -> Vec<Snippet> {
    let needle = fold(query);
    let mut prefix: Vec<(String, &Snippet)> = Vec::new();
    let mut inner: Vec<(String, &Snippet)> = Vec::new();
    for snippet in snippets {
        let key = snippet_key(snippet);
        if key.is_empty() || snippet.text.trim().is_empty() {
            continue;
        }
        if key.starts_with(&needle) {
            prefix.push((key, snippet));
        } else if key.contains(&needle) {
            inner.push((key, snippet));
        }
    }
    let order = |a: &(String, &Snippet), b: &(String, &Snippet)| {
        a.0.len().cmp(&b.0.len()).then_with(|| a.0.cmp(&b.0))
    };
    prefix.sort_by(order);
    inner.sort_by(order);
    prefix
        .into_iter()
        .chain(inner)
        .map(|(_, snippet)| snippet.clone())
        .take(MAX_SNIPPETS)
        .collect()
}

fn one_line(text: &str, max_chars: usize) -> String {
    let flat = text.split_whitespace().collect::<Vec<_>>().join(" ");
    if flat.chars().count() <= max_chars {
        return flat;
    }
    let mut out: String = flat.chars().take(max_chars).collect();
    out.push('…');
    out
}

fn now_ms() -> i64 {
    chrono::Utc::now().timestamp_millis()
}

pub(crate) fn local_now() -> NaiveDateTime {
    Local::now().naive_local()
}

pub(crate) fn local_ms(at: NaiveDateTime) -> i64 {
    Local
        .from_local_datetime(&at)
        .earliest()
        .or_else(|| Local.from_local_datetime(&(at + Span::hours(1))).earliest())
        .map(|time| time.timestamp_millis())
        .unwrap_or_else(|| at.and_utc().timestamp_millis())
}

pub(crate) fn naive_at(ms: i64) -> NaiveDateTime {
    Local
        .timestamp_millis_opt(ms)
        .single()
        .map(|time| time.naive_local())
        .unwrap_or_else(local_now)
}

pub(crate) fn when_label(ms: i64) -> String {
    format_when(naive_at(ms), local_now())
}

#[derive(Clone)]
pub(crate) struct ReminderDraft {
    pub(crate) thread_id: String,
    pub(crate) thread_name: String,
    pub(crate) message_id: String,
    pub(crate) preview: String,
}

#[derive(Clone)]
pub(crate) struct DraftSnapshot {
    thread_id: String,
    thread_name: String,
    text: String,
    mentions: Vec<Mention>,
    reply_to_id: Option<String>,
}

pub(crate) struct ScheduleMenu {
    pub(crate) position: Point<Pixels>,
    draft: DraftSnapshot,
}

#[derive(Clone)]
pub(crate) enum PickerTarget {
    Schedule(DraftSnapshot),
    Remind(ReminderDraft),
    Reschedule(String),
    MoveReminder(String),
}

pub(crate) struct DatePicker {
    pub(crate) target: PickerTarget,
    pub(crate) date: Entity<InputState>,
    pub(crate) time: Entity<InputState>,
    pub(crate) error: Option<SharedString>,
    _subscriptions: Vec<Subscription>,
}

impl DatePicker {
    pub(crate) fn title(&self) -> SharedString {
        match self.target {
            PickerTarget::Schedule(_) => ts!("planner.picker.title_schedule"),
            PickerTarget::Remind(_) => ts!("planner.picker.title_remind"),
            PickerTarget::Reschedule(_) => ts!("planner.picker.title_reschedule"),
            PickerTarget::MoveReminder(_) => ts!("planner.picker.title_move_reminder"),
        }
    }

    pub(crate) fn confirm_label(&self) -> SharedString {
        match self.target {
            PickerTarget::Schedule(_) => ts!("planner.picker.confirm_schedule"),
            PickerTarget::Remind(_) => ts!("planner.picker.confirm_remind"),
            PickerTarget::Reschedule(_) | PickerTarget::MoveReminder(_) => {
                ts!("planner.picker.confirm_save")
            }
        }
    }
}

#[derive(Clone, Copy, PartialEq, Eq)]
pub(crate) enum PlannerTab {
    Scheduled,
    Reminders,
}

#[derive(Default)]
pub(crate) struct FavoritesView {
    pub(crate) input: Option<Entity<InputState>>,
    _subscription: Option<Subscription>,
}

#[derive(Default)]
pub(crate) struct Planner {
    pub(crate) scheduled: Vec<ScheduledItem>,
    pub(crate) reminders: Vec<Reminder>,
    pub(crate) favorites: Vec<Favorite>,
    pub(crate) schedule_menu: Option<ScheduleMenu>,
    pub(crate) picker: Option<DatePicker>,
    pub(crate) list: Option<PlannerTab>,
    pub(crate) favorites_view: Option<FavoritesView>,
    pub(crate) snippet_query: Option<String>,
    pending_jump: Option<(String, String, Instant)>,
    online_since_ms: Option<i64>,
    last_tick_ms: Option<i64>,
    prefetched: HashMap<String, Instant>,
    next_id: u64,
}

impl Planner {
    pub(crate) fn load() -> Self {
        let file: PlannerFile = crate::storage::load_json(PLANNER_FILE);
        let now = now_ms();
        let mut scheduled = file.scheduled;
        let mut flagged = false;
        for item in scheduled.iter_mut().filter(|item| item.sending) {
            item.sending = false;
            item.failure = Some(t!("planner.failure.unconfirmed").to_string());
            flagged = true;
        }
        for item in scheduled.iter_mut().filter(|item| item.waiting()) {
            if due_state(item.send_at_ms, now) == Due::Overdue {
                item.overdue = true;
                flagged = true;
            }
        }
        let mut reminders = file.reminders;
        reminders.retain(|reminder| {
            reminder
                .fired_ms
                .is_none_or(|fired| now - fired < FIRED_KEEP_MS)
        });
        let planner = Self {
            scheduled,
            reminders,
            favorites: crate::storage::load_json(FAVORITES_FILE),
            ..Self::default()
        };
        if flagged {
            planner.save();
        }
        planner
    }

    fn save(&self) {
        crate::storage::save_json(
            PLANNER_FILE,
            &PlannerFile {
                scheduled: self.scheduled.clone(),
                reminders: self.reminders.clone(),
            },
        );
    }

    fn save_favorites(&self) {
        crate::storage::save_json(FAVORITES_FILE, &self.favorites);
    }

    fn new_id(&mut self) -> String {
        self.next_id += 1;
        format!("{}-{}", now_ms(), self.next_id)
    }

    pub(crate) fn blocks_enter(&self) -> bool {
        self.schedule_menu.is_some()
            || self.picker.is_some()
            || self.list.is_some()
            || self.favorites_view.is_some()
    }

    pub(crate) fn pending_reminder(&self, message_id: &str) -> Option<&Reminder> {
        self.reminders
            .iter()
            .find(|reminder| reminder.message_id == message_id && reminder.fired_ms.is_none())
    }

    pub(crate) fn is_favorite(&self, message_id: &str) -> bool {
        self.favorites
            .iter()
            .any(|favorite| favorite.message_id == message_id)
    }

    pub(crate) fn thread_summary(&self, thread_id: &str) -> Option<(usize, usize, i64)> {
        let items: Vec<&ScheduledItem> = self
            .scheduled
            .iter()
            .filter(|item| item.thread_id == thread_id)
            .collect();
        if items.is_empty() {
            return None;
        }
        let attention = items.iter().filter(|item| item.needs_decision()).count();
        let next = items
            .iter()
            .filter(|item| !item.needs_decision())
            .map(|item| item.send_at_ms)
            .min()
            .unwrap_or(0);
        Some((items.len(), attention, next))
    }

    pub(crate) fn sorted_scheduled(&self) -> Vec<ScheduledItem> {
        let mut items = self.scheduled.clone();
        items.sort_by(|a, b| {
            b.needs_decision()
                .cmp(&a.needs_decision())
                .then(a.send_at_ms.cmp(&b.send_at_ms))
        });
        items
    }

    pub(crate) fn sorted_reminders(&self) -> Vec<Reminder> {
        let mut items = self.reminders.clone();
        items.sort_by(|a, b| {
            b.fired_ms
                .is_some()
                .cmp(&a.fired_ms.is_some())
                .then(a.due_ms.cmp(&b.due_ms))
        });
        items
    }
}

impl MissiveApp {
    pub(crate) fn tick_planner(&mut self, cx: &mut Context<Self>) {
        let now = now_ms();
        let mut changed = false;
        let link_up = matches!(self.screen, Screen::Main)
            && self.link == Link::Online
            && self.bridge.is_some();
        let (since, online) = stable_online(
            link_up,
            self.planner.online_since_ms,
            self.planner.last_tick_ms,
            now,
        );
        self.planner.online_since_ms = since;
        self.planner.last_tick_ms = Some(now);
        let mut ready: Vec<String> = Vec::new();
        let mut late = 0;
        for item in self.planner.scheduled.iter_mut().filter(|item| item.waiting()) {
            match due_state(item.send_at_ms, now) {
                Due::Wait => {}
                Due::Overdue => {
                    item.overdue = true;
                    changed = true;
                    late += 1;
                }
                Due::Now if online => ready.push(item.id.clone()),
                Due::Now => {}
            }
        }
        for id in ready {
            changed |= self.dispatch_scheduled(&id, cx);
        }
        if late > 0 {
            self.show_toast(t!("planner.toast.late"), cx);
        }

        let due: Vec<Reminder> = self
            .planner
            .reminders
            .iter()
            .filter(|reminder| reminder.fired_ms.is_none() && reminder.due_ms <= now)
            .cloned()
            .collect();
        for reminder in due {
            if let Some(stored) = self
                .planner
                .reminders
                .iter_mut()
                .find(|stored| stored.id == reminder.id)
            {
                stored.fired_ms = Some(now);
            }
            changed = true;
            self.fire_reminder(&reminder, cx);
        }
        let before = self.planner.reminders.len();
        self.planner.reminders.retain(|reminder| {
            reminder
                .fired_ms
                .is_none_or(|fired| now - fired < FIRED_KEEP_MS)
        });
        changed |= before != self.planner.reminders.len();

        if let Some((thread_id, message_id, at)) = self.planner.pending_jump.clone() {
            if self.selected.as_deref() != Some(thread_id.as_str()) || at.elapsed() > JUMP_PATIENCE
            {
                self.planner.pending_jump = None;
            } else if self.messages.contains_key(&thread_id) {
                self.planner.pending_jump = None;
                self.jump_to_message(thread_id, message_id, cx);
            }
        }

        if changed {
            self.planner.save();
            cx.notify();
        }
    }

    fn dispatch_scheduled(&mut self, id: &str, cx: &mut Context<Self>) -> bool {
        let Some(item) = self
            .planner
            .scheduled
            .iter()
            .find(|item| item.id == id)
            .cloned()
        else {
            return false;
        };
        if !self.threads.is_empty() && !self.threads.iter().any(|t| t.id == item.thread_id) {
            self.fail_scheduled(id, t!("planner.thread_gone").to_string(), cx);
            return true;
        }
        if !self.messages.contains_key(&item.thread_id) {
            let recent = self
                .planner
                .prefetched
                .get(&item.thread_id)
                .is_some_and(|at| at.elapsed() < PREFETCH_BACKOFF);
            if !recent {
                self.planner
                    .prefetched
                    .insert(item.thread_id.clone(), Instant::now());
                self.prefetch(&item.thread_id, cx);
            }
            return false;
        }
        let client_id = self.queue_outgoing(
            OutgoingRequest {
                thread_id: item.thread_id,
                text: item.text,
                reply_to: item.reply_to_id,
                files: Vec::new(),
                mentions: item.mentions,
                mark_read: self.settings.send_read_receipts,
            },
            cx,
        );
        if let Some(stored) = self.planner.scheduled.iter_mut().find(|item| item.id == id) {
            stored.sending = true;
            stored.overdue = false;
            stored.failure = None;
            stored.client_id = Some(client_id);
        }
        true
    }

    pub(crate) fn scheduled_send_finished(
        &mut self,
        client_id: &str,
        error: Option<String>,
        cx: &mut Context<Self>,
    ) {
        let Some(index) = self
            .planner
            .scheduled
            .iter()
            .position(|item| item.client_id.as_deref() == Some(client_id))
        else {
            return;
        };
        match error {
            None => {
                let item = self.planner.scheduled.remove(index);
                self.planner.save();
                self.show_toast(t!("planner.toast.sent", name = item.thread_name), cx);
            }
            Some(reason) => {
                let id = self.planner.scheduled[index].id.clone();
                self.fail_scheduled(&id, reason, cx);
            }
        }
        cx.notify();
    }

    fn fail_scheduled(&mut self, id: &str, reason: String, cx: &mut Context<Self>) {
        let Some(item) = self.planner.scheduled.iter_mut().find(|item| item.id == id) else {
            return;
        };
        item.sending = false;
        item.failure = Some(reason);
        let item = item.clone();
        self.planner.save();
        let body = if self.settings.notification_preview {
            t!(
                "planner.notification.body",
                name = item.thread_name,
                text = one_line(&item.text, 120)
            )
            .to_string()
        } else {
            t!("planner.notification.open_to_retry").to_string()
        };
        let notification = Notification {
            title: t!("planner.notification.not_sent_title").to_string(),
            body,
            thread_id: item.thread_id.clone(),
        };
        let clicks = self.clicks.clone();
        cx.background_spawn(async move { notifications::show(notification, clicks) })
            .detach();
        self.show_toast(t!("planner.toast.not_sent", name = item.thread_name), cx);
        cx.notify();
    }

    fn fire_reminder(&mut self, reminder: &Reminder, cx: &mut Context<Self>) {
        let body = if !self.settings.notification_preview {
            t!("planner.reminder.notification_hidden").to_string()
        } else if reminder.preview.is_empty() {
            t!("planner.reminder.notification_in_thread", name = reminder.thread_name).to_string()
        } else {
            t!(
                "planner.notification.body",
                name = reminder.thread_name,
                text = reminder.preview
            )
            .to_string()
        };
        let notification = Notification {
            title: t!("planner.reminder.title").to_string(),
            body,
            thread_id: reminder.thread_id.clone(),
        };
        let clicks = self.clicks.clone();
        cx.background_spawn(async move { notifications::show(notification, clicks) })
            .detach();
        let toast = if reminder.preview.is_empty() {
            t!("planner.reminder.toast", name = reminder.thread_name)
        } else {
            t!(
                "planner.reminder.toast_preview",
                name = reminder.thread_name,
                preview = one_line(&reminder.preview, 90)
            )
        };
        self.show_toast(toast, cx);
        self.remeasure_message(&reminder.thread_id, &reminder.message_id, cx);
    }

    pub(crate) fn open_scheduled(&mut self, cx: &mut Context<Self>) {
        self.planner.schedule_menu = None;
        self.planner.favorites_view = None;
        if self.planner.list.is_none() {
            self.planner.list = Some(PlannerTab::Scheduled);
        }
        cx.notify();
    }

    pub(crate) fn set_planner_tab(&mut self, tab: PlannerTab, cx: &mut Context<Self>) {
        self.planner.list = Some(tab);
        cx.notify();
    }

    pub(crate) fn close_planner_list(&mut self, cx: &mut Context<Self>) {
        self.planner.list = None;
        cx.notify();
    }

    pub(crate) fn open_favorites(&mut self, cx: &mut Context<Self>) {
        self.planner.schedule_menu = None;
        self.planner.list = None;
        if self.planner.favorites_view.is_some() {
            cx.notify();
            return;
        }
        self.planner.favorites_view = Some(FavoritesView::default());
        let handle = cx
            .active_window()
            .or_else(|| cx.windows().first().copied());
        if let Some(handle) = handle {
            let weak = cx.entity().downgrade();
            cx.defer(move |cx| {
                let _ = handle.update(cx, |_, window, cx| {
                    let _ = weak.update(cx, |this, cx| this.attach_favorites_input(window, cx));
                });
            });
        }
        cx.notify();
    }

    fn attach_favorites_input(&mut self, window: &mut Window, cx: &mut Context<Self>) {
        if self
            .planner
            .favorites_view
            .as_ref()
            .is_none_or(|view| view.input.is_some())
        {
            return;
        }
        let input = cx.new(|cx| {
            InputState::new(window, cx).placeholder(t!("planner.favorites.search_placeholder"))
        });
        let subscription = cx.subscribe_in(&input, window, |_, _, event: &InputEvent, _, cx| {
            if let InputEvent::Change = event {
                cx.notify();
            }
        });
        input.update(cx, |input, cx| input.focus(window, cx));
        if let Some(view) = self.planner.favorites_view.as_mut() {
            view.input = Some(input);
            view._subscription = Some(subscription);
        }
        cx.notify();
    }

    pub(crate) fn close_favorites(&mut self, cx: &mut Context<Self>) {
        self.planner.favorites_view = None;
        cx.notify();
    }

    pub(crate) fn close_planner_dialogs(&mut self, cx: &mut Context<Self>) -> bool {
        if self.planner.picker.is_some() {
            self.planner.picker = None;
        } else if self.planner.schedule_menu.is_some() {
            self.planner.schedule_menu = None;
        } else if self.planner.favorites_view.is_some() {
            self.planner.favorites_view = None;
        } else if self.planner.list.is_some() {
            self.planner.list = None;
        } else {
            return false;
        }
        cx.notify();
        true
    }

    pub(crate) fn favorite_results(&self, cx: &App) -> Vec<Favorite> {
        let Some(view) = self.planner.favorites_view.as_ref() else {
            return Vec::new();
        };
        let query = view
            .input
            .as_ref()
            .map(|input| fold(input.read(cx).value().trim()))
            .unwrap_or_default();
        let mut results: Vec<Favorite> = self
            .planner
            .favorites
            .iter()
            .filter(|favorite| {
                query.is_empty()
                    || fold(&favorite.text).contains(&query)
                    || fold(&favorite.thread_name).contains(&query)
                    || fold(&favorite.sender_name).contains(&query)
                    || fold(&favorite.kind).contains(&query)
            })
            .cloned()
            .collect();
        results.sort_by_key(|item| std::cmp::Reverse(item.added_ms));
        results
    }

    pub(crate) fn toggle_favorite(&mut self, message: &Message, cx: &mut Context<Self>) {
        self.message_ops.menu = None;
        if self.planner.is_favorite(&message.id) {
            self.planner
                .favorites
                .retain(|favorite| favorite.message_id != message.id);
            self.show_toast(t!("planner.favorites.removed"), cx);
        } else {
            let thread_name = self
                .threads
                .iter()
                .find(|thread| thread.id == message.thread_id)
                .map(|thread| thread.name.clone())
                .unwrap_or_default();
            let sender_name = if message.is_outgoing {
                t!("planner.favorites.you").to_string()
            } else {
                message.sender_name.clone()
            };
            self.planner.favorites.push(Favorite {
                thread_id: message.thread_id.clone(),
                thread_name,
                message_id: message.id.clone(),
                sender_name,
                text: message.text.clone(),
                timestamp_ms: message.timestamp_ms,
                kind: describe_attachments(message),
                added_ms: now_ms(),
            });
            self.show_toast(t!("planner.favorites.added"), cx);
        }
        self.planner.save_favorites();
        self.remeasure_message(&message.thread_id, &message.id, cx);
        cx.notify();
    }

    pub(crate) fn remove_favorite(&mut self, message_id: &str, cx: &mut Context<Self>) {
        let thread_id = self
            .planner
            .favorites
            .iter()
            .find(|favorite| favorite.message_id == message_id)
            .map(|favorite| favorite.thread_id.clone());
        self.planner
            .favorites
            .retain(|favorite| favorite.message_id != message_id);
        self.planner.save_favorites();
        if let Some(thread_id) = thread_id {
            self.remeasure_message(&thread_id, message_id, cx);
        }
        cx.notify();
    }

    pub(crate) fn open_planner_message(
        &mut self,
        thread_id: String,
        message_id: String,
        window: &mut Window,
        cx: &mut Context<Self>,
    ) {
        if !self.threads.iter().any(|thread| thread.id == thread_id) {
            self.show_toast(t!("planner.thread_gone"), cx);
            return;
        }
        self.planner.favorites_view = None;
        self.planner.list = None;
        if self.selected.as_deref() != Some(thread_id.as_str()) {
            self.pending_scroll = Some((thread_id.clone(), message_id.clone()));
            self.select_thread(thread_id.clone(), window, cx);
        }
        if self.messages.contains_key(&thread_id) {
            self.pending_scroll = None;
            self.jump_to_message(thread_id, message_id, cx);
        } else {
            self.planner.pending_jump = Some((thread_id, message_id, Instant::now()));
        }
        cx.notify();
    }

    pub(crate) fn open_planner_thread(
        &mut self,
        thread_id: String,
        window: &mut Window,
        cx: &mut Context<Self>,
    ) {
        if !self.threads.iter().any(|thread| thread.id == thread_id) {
            self.show_toast(t!("planner.thread_gone"), cx);
            return;
        }
        self.planner.list = None;
        self.select_thread(thread_id, window, cx);
        cx.notify();
    }

    fn draft_snapshot(&mut self, cx: &mut Context<Self>) -> Option<DraftSnapshot> {
        if self.message_ops.editing.is_some() {
            return None;
        }
        let thread_id = self.selected.clone()?;
        if !self.pending_files.is_empty() {
            self.show_toast(t!("planner.draft.no_attachments"), cx);
            return None;
        }
        let text = self.composer.read(cx).value().trim().to_string();
        if text.is_empty() {
            self.show_toast(t!("planner.draft.empty"), cx);
            return None;
        }
        let thread_name = self
            .selected_thread()
            .map(|thread| thread.name.clone())
            .unwrap_or_default();
        Some(DraftSnapshot {
            thread_id,
            thread_name,
            mentions: super::mentions::compute_mentions(&text, &self.pending_mentions),
            reply_to_id: self.reply_to.as_ref().map(|message| message.id.clone()),
            text,
        })
    }

    pub(crate) fn open_schedule_menu(&mut self, position: Point<Pixels>, cx: &mut Context<Self>) {
        let Some(draft) = self.draft_snapshot(cx) else {
            return;
        };
        self.planner.schedule_menu = Some(ScheduleMenu { position, draft });
        cx.notify();
    }

    pub(crate) fn close_schedule_menu(&mut self, cx: &mut Context<Self>) {
        self.planner.schedule_menu = None;
        cx.notify();
    }

    pub(crate) fn schedule_from_menu(
        &mut self,
        send_at_ms: i64,
        window: &mut Window,
        cx: &mut Context<Self>,
    ) {
        let Some(menu) = self.planner.schedule_menu.take() else {
            return;
        };
        self.schedule_draft_at(menu.draft, send_at_ms, window, cx);
    }

    fn schedule_draft_at(
        &mut self,
        draft: DraftSnapshot,
        send_at_ms: i64,
        window: &mut Window,
        cx: &mut Context<Self>,
    ) {
        self.planner.schedule_menu = None;
        let same_thread = self.selected.as_deref() == Some(draft.thread_id.as_str());
        let composer_text = self.composer.read(cx).value().trim().to_string();
        if same_thread && composer_text == draft.text && self.message_ops.editing.is_none() {
            if self.reply_to.as_ref().map(|message| &message.id) == draft.reply_to_id.as_ref() {
                self.reply_to = None;
            }
            self.pending_mentions.clear();
            self.mention_query = None;
            self.planner.snippet_query = None;
            self.stop_typing(cx);
            self.composer
                .update(cx, |input, cx| input.clean(window, cx));
        } else if !same_thread
            && self
                .drafts
                .get(&draft.thread_id)
                .is_some_and(|saved| saved.trim() == draft.text)
        {
            self.drafts.remove(&draft.thread_id);
        }
        let id = self.planner.new_id();
        self.planner.scheduled.push(ScheduledItem {
            id,
            thread_id: draft.thread_id,
            thread_name: draft.thread_name,
            text: draft.text,
            mentions: draft.mentions,
            reply_to_id: draft.reply_to_id,
            send_at_ms,
            created_ms: now_ms(),
            ..ScheduledItem::default()
        });
        self.planner.save();
        self.show_toast(
            t!("planner.toast.scheduled", when = when_label(send_at_ms)),
            cx,
        );
        cx.notify();
    }

    pub(crate) fn send_scheduled_now(&mut self, id: &str, cx: &mut Context<Self>) {
        let Some(item) = self.planner.scheduled.iter_mut().find(|item| item.id == id) else {
            self.show_toast(t!("planner.toast.already_sent"), cx);
            return;
        };
        if item.sending {
            return;
        }
        let online = self.link == Link::Online;
        let live = item.client_id.clone().filter(|client_id| {
            online && item.failure.is_some() && self.outgoing_requests.contains_key(client_id)
        });
        item.overdue = false;
        item.failure = None;
        match live {
            Some(client_id) => {
                item.sending = true;
                self.planner.save();
                self.retry_outgoing(client_id, cx);
            }
            None => {
                let stale = item.client_id.take();
                item.send_at_ms = now_ms();
                self.planner.save();
                if let Some(stale) = stale.filter(|id| self.outgoing_requests.contains_key(id)) {
                    self.discard_outgoing(stale, cx);
                }
                if self.link != Link::Online {
                    self.show_toast(t!("planner.toast.waiting_for_connection"), cx);
                }
                self.tick_planner(cx);
            }
        }
        cx.notify();
    }

    pub(crate) fn delete_scheduled(&mut self, id: &str, cx: &mut Context<Self>) {
        self.planner.scheduled.retain(|item| item.id != id);
        self.planner.save();
        cx.notify();
    }

    pub(crate) fn reminder_draft(&self, message: &Message) -> ReminderDraft {
        let thread_name = self
            .threads
            .iter()
            .find(|thread| thread.id == message.thread_id)
            .map(|thread| thread.name.clone())
            .unwrap_or_default();
        let body = if message.text.is_empty() {
            describe_attachments(message)
        } else {
            message.text.clone()
        };
        let preview = if message.is_outgoing || message.sender_name.is_empty() {
            one_line(&body, PREVIEW_CHARS)
        } else {
            t!(
                "planner.reminder.preview",
                sender = message.sender_name,
                text = one_line(&body, PREVIEW_CHARS)
            )
            .to_string()
        };
        ReminderDraft {
            thread_id: message.thread_id.clone(),
            thread_name,
            message_id: message.id.clone(),
            preview,
        }
    }

    pub(crate) fn remind_message(
        &mut self,
        draft: ReminderDraft,
        due_ms: i64,
        cx: &mut Context<Self>,
    ) {
        self.message_ops.menu = None;
        self.planner
            .reminders
            .retain(|reminder| reminder.message_id != draft.message_id);
        let id = self.planner.new_id();
        let (thread_id, message_id) = (draft.thread_id.clone(), draft.message_id.clone());
        self.planner.reminders.push(Reminder {
            id,
            thread_id: draft.thread_id,
            thread_name: draft.thread_name,
            message_id: draft.message_id,
            preview: draft.preview,
            due_ms,
            fired_ms: None,
        });
        self.planner.save();
        self.remeasure_message(&thread_id, &message_id, cx);
        self.show_toast(t!("planner.reminder.set", when = when_label(due_ms)), cx);
        cx.notify();
    }

    pub(crate) fn cancel_message_reminder(&mut self, message: &Message, cx: &mut Context<Self>) {
        self.message_ops.menu = None;
        self.planner
            .reminders
            .retain(|reminder| reminder.message_id != message.id);
        self.planner.save();
        self.remeasure_message(&message.thread_id, &message.id, cx);
        self.show_toast(t!("planner.reminder.cancelled"), cx);
        cx.notify();
    }

    pub(crate) fn snooze_reminder(&mut self, id: &str, cx: &mut Context<Self>) {
        let mut target = None;
        if let Some(reminder) = self.planner.reminders.iter_mut().find(|r| r.id == id) {
            reminder.due_ms = now_ms() + HOUR_MS;
            reminder.fired_ms = None;
            target = Some((reminder.thread_id.clone(), reminder.message_id.clone()));
        }
        self.planner.save();
        if let Some((thread_id, message_id)) = target {
            self.remeasure_message(&thread_id, &message_id, cx);
        }
        cx.notify();
    }

    pub(crate) fn delete_reminder(&mut self, id: &str, cx: &mut Context<Self>) {
        let target = self
            .planner
            .reminders
            .iter()
            .find(|r| r.id == id)
            .map(|r| (r.thread_id.clone(), r.message_id.clone()));
        self.planner.reminders.retain(|reminder| reminder.id != id);
        self.planner.save();
        if let Some((thread_id, message_id)) = target {
            self.remeasure_message(&thread_id, &message_id, cx);
        }
        cx.notify();
    }

    pub(crate) fn planner_when(&self, ms: i64) -> String {
        when_label(ms)
    }

    pub(crate) fn planner_when_title(&self, ms: i64) -> String {
        timefmt::capitalized(&when_label(ms))
    }

    pub(crate) fn schedule_choices(&self) -> Vec<(SharedString, i64)> {
        schedule_presets(local_now())
            .into_iter()
            .map(|(label, at)| (label, local_ms(at)))
            .collect()
    }

    pub(crate) fn reminder_choices(&self) -> Vec<(SharedString, i64)> {
        reminder_presets(local_now())
            .into_iter()
            .map(|(label, at)| (label, local_ms(at)))
            .collect()
    }

    pub(crate) fn planner_reminders_tab(&self) -> bool {
        self.planner.list == Some(PlannerTab::Reminders)
    }

    pub(crate) fn show_planner_tab(&mut self, reminders: bool, cx: &mut Context<Self>) {
        self.set_planner_tab(
            if reminders {
                PlannerTab::Reminders
            } else {
                PlannerTab::Scheduled
            },
            cx,
        );
    }

    pub(crate) fn open_schedule_picker(&mut self, window: &mut Window, cx: &mut Context<Self>) {
        let draft = match self.planner.schedule_menu.take() {
            Some(menu) => Some(menu.draft),
            None => self.draft_snapshot(cx),
        };
        match draft {
            Some(draft) => self.open_picker(PickerTarget::Schedule(draft), window, cx),
            None => cx.notify(),
        }
    }

    pub(crate) fn open_remind_picker(
        &mut self,
        draft: ReminderDraft,
        window: &mut Window,
        cx: &mut Context<Self>,
    ) {
        self.open_picker(PickerTarget::Remind(draft), window, cx);
    }

    pub(crate) fn open_reschedule_picker(
        &mut self,
        id: String,
        window: &mut Window,
        cx: &mut Context<Self>,
    ) {
        self.open_picker(PickerTarget::Reschedule(id), window, cx);
    }

    pub(crate) fn open_move_reminder_picker(
        &mut self,
        id: String,
        window: &mut Window,
        cx: &mut Context<Self>,
    ) {
        self.open_picker(PickerTarget::MoveReminder(id), window, cx);
    }

    fn open_picker(
        &mut self,
        target: PickerTarget,
        window: &mut Window,
        cx: &mut Context<Self>,
    ) {
        self.planner.schedule_menu = None;
        self.message_ops.menu = None;
        if let PickerTarget::Reschedule(id) = &target {
            match self.planner.scheduled.iter().find(|item| item.id == *id) {
                None => {
                    self.show_toast(t!("planner.toast.already_sent"), cx);
                    return;
                }
                Some(item) if item.sending => {
                    self.show_toast(t!("planner.toast.sending"), cx);
                    return;
                }
                Some(_) => {}
            }
        }
        let initial = match &target {
            PickerTarget::Reschedule(id) => self
                .planner
                .scheduled
                .iter()
                .find(|item| item.id == *id)
                .map(|item| naive_at(item.send_at_ms).max(local_now())),
            PickerTarget::MoveReminder(id) => self
                .planner
                .reminders
                .iter()
                .find(|reminder| reminder.id == *id)
                .map(|reminder| naive_at(reminder.due_ms).max(local_now())),
            _ => None,
        }
        .unwrap_or_else(|| (local_now().date() + Span::days(1)).and_time(hm(9, 0)));
        let date_value = timefmt::input_date(initial.date());
        let time_value = timefmt::input_clock(&initial);
        let date = cx.new(|cx| {
            InputState::new(window, cx)
                .placeholder(t!("planner.picker.date_placeholder"))
                .default_value(date_value)
        });
        let time = cx.new(|cx| {
            InputState::new(window, cx)
                .placeholder(t!("planner.picker.time_placeholder"))
                .default_value(time_value)
        });
        let subscriptions = [&date, &time]
            .into_iter()
            .map(|input| {
                cx.subscribe_in(
                    input,
                    window,
                    |this, _, event: &InputEvent, window, cx| match event {
                        InputEvent::PressEnter { .. } => this.confirm_picker(window, cx),
                        InputEvent::Change => {
                            if let Some(picker) = this.planner.picker.as_mut() {
                                picker.error = None;
                            }
                            cx.notify();
                        }
                        _ => {}
                    },
                )
            })
            .collect();
        date.update(cx, |input, cx| input.focus(window, cx));
        self.planner.picker = Some(DatePicker {
            target,
            date,
            time,
            error: None,
            _subscriptions: subscriptions,
        });
        cx.notify();
    }

    pub(crate) fn close_picker(&mut self, cx: &mut Context<Self>) {
        self.planner.picker = None;
        cx.notify();
    }

    pub(crate) fn confirm_picker(&mut self, window: &mut Window, cx: &mut Context<Self>) {
        let Some(picker) = self.planner.picker.as_mut() else {
            return;
        };
        let date = picker.date.read(cx).value().to_string();
        let time = picker.time.read(cx).value().to_string();
        let at = match parse_when(&date, &time, local_now()) {
            Ok(at) => local_ms(at),
            Err(error) => {
                picker.error = Some(error);
                cx.notify();
                return;
            }
        };
        let target = picker.target.clone();
        self.planner.picker = None;
        match target {
            PickerTarget::Schedule(draft) => self.schedule_draft_at(draft, at, window, cx),
            PickerTarget::Remind(draft) => self.remind_message(draft, at, cx),
            PickerTarget::Reschedule(id) => {
                match self.planner.scheduled.iter_mut().find(|item| item.id == id) {
                    Some(item) if !item.sending => {
                        item.send_at_ms = at;
                        item.overdue = false;
                        item.failure = None;
                        let stale = item.client_id.take();
                        self.planner.save();
                        if let Some(stale) =
                            stale.filter(|id| self.outgoing_requests.contains_key(id))
                        {
                            self.discard_outgoing(stale, cx);
                        }
                    }
                    Some(_) => self.show_toast(t!("planner.toast.sending"), cx),
                    None => self.show_toast(t!("planner.toast.already_sent"), cx),
                }
            }
            PickerTarget::MoveReminder(id) => {
                let mut target = None;
                if let Some(reminder) = self.planner.reminders.iter_mut().find(|r| r.id == id) {
                    reminder.due_ms = at;
                    reminder.fired_ms = None;
                    target = Some((reminder.thread_id.clone(), reminder.message_id.clone()));
                }
                self.planner.save();
                if let Some((thread_id, message_id)) = target {
                    self.remeasure_message(&thread_id, &message_id, cx);
                }
            }
        }
        cx.notify();
    }

    pub(crate) fn refresh_snippet_query(&mut self, text: &str) {
        let query = if self.mention_query.is_some() {
            None
        } else {
            snippet_query(text).map(|(_, query)| query)
        };
        if query.is_some() && query != self.planner.snippet_query {
            self.mention_selected = 0;
        }
        self.planner.snippet_query = query;
    }

    pub(crate) fn snippet_candidates(&self) -> Vec<Snippet> {
        match self.planner.snippet_query.as_ref() {
            Some(query) => match_snippets(&self.settings.snippets, query),
            None => Vec::new(),
        }
    }

    pub(crate) fn snippet_popup_active(&self) -> bool {
        self.planner.snippet_query.is_some() && !self.snippet_candidates().is_empty()
    }

    pub(crate) fn snippet_hint_visible(&self) -> bool {
        self.planner.snippet_query.is_some() && !usable_snippets(&self.settings.snippets)
    }

    pub(crate) fn accept_snippet(&mut self, index: usize, window: &mut Window, cx: &mut Context<Self>) {
        let candidates = self.snippet_candidates();
        let Some(snippet) = candidates.get(index) else {
            return;
        };
        let text = self.composer.read(cx).value().to_string();
        let Some((slash, _)) = snippet_query(&text) else {
            return;
        };
        let new_text = format!("{}{}", &text[..slash], snippet.text);
        self.planner.snippet_query = None;
        let line = new_text.matches('\n').count() as u32;
        let character = new_text
            .rsplit('\n')
            .next()
            .map(|last| last.encode_utf16().count() as u32)
            .unwrap_or(0);
        self.composer.update(cx, |input, cx| {
            input.set_value(new_text, window, cx);
            input.set_cursor_position(Position { line, character }, window, cx);
            input.focus(window, cx);
        });
        cx.notify();
    }
}

#[cfg(test)]
mod tests {
    use chrono::{NaiveDate, NaiveDateTime};
    use gpui_kit::SharedString;

    use super::{
        Due, OVERDUE_GRACE_MS, due_state, format_when, match_snippets, parse_when,
        reminder_presets, schedule_presets, snippet_query, stable_online,
    };
    use crate::settings::Snippet;

    fn at(y: i32, m: u32, d: u32, h: u32, min: u32) -> NaiveDateTime {
        NaiveDate::from_ymd_opt(y, m, d)
            .unwrap()
            .and_hms_opt(h, min, 0)
            .unwrap()
    }

    fn snippet(shortcut: &str, text: &str) -> Snippet {
        Snippet {
            shortcut: shortcut.into(),
            text: text.into(),
        }
    }

    #[::core::prelude::v1::test]
    fn schedule_presets_follow_the_clock() {
        let _guard = crate::i18n::scoped("fr");
        let wednesday_morning = at(2026, 11, 18, 10, 0);
        let presets = schedule_presets(wednesday_morning);
        assert_eq!(
            presets,
            vec![
                (SharedString::from("Ce soir à 20:00"), at(2026, 11, 18, 20, 0)),
                (SharedString::from("Demain à 8:00"), at(2026, 11, 19, 8, 0)),
                (SharedString::from("Lundi à 9:00"), at(2026, 11, 23, 9, 0)),
            ]
        );
        let evening = at(2026, 11, 18, 19, 30);
        assert_eq!(schedule_presets(evening)[0].0, "Demain à 8:00");
        let monday = at(2026, 11, 23, 7, 0);
        assert_eq!(schedule_presets(monday)[2].1, at(2026, 11, 30, 9, 0));
        let sunday = at(2026, 11, 22, 21, 0);
        assert_eq!(schedule_presets(sunday)[1].1, at(2026, 11, 23, 9, 0));
    }

    #[::core::prelude::v1::test]
    fn reminder_presets_round_to_the_minute() {
        let _guard = crate::i18n::scoped("fr");
        let now = NaiveDate::from_ymd_opt(2026, 11, 18)
            .unwrap()
            .and_hms_opt(19, 45, 31)
            .unwrap();
        let presets = reminder_presets(now);
        assert_eq!(
            presets,
            vec![
                (SharedString::from("Dans 1 h"), at(2026, 11, 18, 20, 45)),
                (SharedString::from("Demain à 9:00"), at(2026, 11, 19, 9, 0)),
            ]
        );
    }

    #[::core::prelude::v1::test]
    fn presets_speak_english() {
        let _guard = crate::i18n::scoped("en");
        let presets = schedule_presets(at(2026, 11, 18, 10, 0));
        assert_eq!(presets[0].0, "Tonight at 20:00");
        assert_eq!(presets[1].0, "Tomorrow at 8:00");
        assert_eq!(presets[2].0, "Monday at 9:00");
        assert_eq!(reminder_presets(at(2026, 11, 18, 21, 0))[0].0, "In 1 hour");
    }

    #[::core::prelude::v1::test]
    fn overdue_rule_has_fifteen_minutes_of_grace() {
        let send_at = 1_000_000_000;
        assert_eq!(due_state(send_at, send_at - 1), Due::Wait);
        assert_eq!(due_state(send_at, send_at), Due::Now);
        assert_eq!(due_state(send_at, send_at + OVERDUE_GRACE_MS), Due::Now);
        assert_eq!(due_state(send_at, send_at + OVERDUE_GRACE_MS + 1), Due::Overdue);
    }

    #[::core::prelude::v1::test]
    fn sending_waits_for_a_stable_link() {
        assert_eq!(stable_online(false, Some(0), Some(0), 60_000), (None, false));
        assert_eq!(stable_online(true, None, None, 1_000), (Some(1_000), false));
        assert_eq!(stable_online(true, Some(1_000), Some(20_000), 21_000), (Some(1_000), true));
        assert_eq!(stable_online(true, Some(1_000), Some(20_000), 60_000), (Some(60_000), false));
        assert_eq!(stable_online(true, Some(1_000), Some(10_000), 11_000), (Some(1_000), false));
    }

    #[::core::prelude::v1::test]
    fn picker_validates_future_and_one_year() {
        let _guard = crate::i18n::scoped("fr");
        let now = at(2026, 11, 18, 10, 0);
        assert_eq!(parse_when("19/11/2026", "08:30", now), Ok(at(2026, 11, 19, 8, 30)));
        assert_eq!(parse_when("19/11/26", "8h", now), Ok(at(2026, 11, 19, 8, 0)));
        assert_eq!(parse_when("02/01", "9h15", now), Ok(at(2027, 1, 2, 9, 15)));
        assert_eq!(parse_when("demain", "7", now), Ok(at(2026, 11, 19, 7, 0)));
        assert!(parse_when("18/11/2026", "09:59", now).is_err());
        assert!(parse_when("19/11/2027", "09:00", now).is_err());
        assert!(parse_when("31/02/2027", "09:00", now).is_err());
        assert!(parse_when("19/11/2026", "25:00", now).is_err());
        assert!(parse_when("bientôt", "09:00", now).is_err());
    }

    #[::core::prelude::v1::test]
    fn picker_accepts_english_and_french_words_in_english() {
        let _guard = crate::i18n::scoped("en");
        let now = at(2026, 11, 18, 10, 0);
        assert_eq!(parse_when("today", "11:00", now), Ok(at(2026, 11, 18, 11, 0)));
        assert_eq!(parse_when("Tomorrow", "7", now), Ok(at(2026, 11, 19, 7, 0)));
        assert_eq!(parse_when("demain", "7", now), Ok(at(2026, 11, 19, 7, 0)));
        assert_eq!(parse_when("aujourd’hui", "11:00", now), Ok(at(2026, 11, 18, 11, 0)));
        assert_eq!(parse_when("02/01", "9:15", now), Ok(at(2027, 2, 1, 9, 15)));
        assert_eq!(parse_when("12/25/2026", "9:15", now), Ok(at(2026, 12, 25, 9, 15)));
        assert!(parse_when("25/12/2026", "9:15", now).is_err());
        assert_eq!(
            parse_when("soon", "09:00", now),
            Err(SharedString::from("Invalid date: use MM/DD/YYYY."))
        );
        assert_eq!(
            parse_when("today", "09:00", now),
            Err(SharedString::from("Pick a time in the future."))
        );
    }

    #[::core::prelude::v1::test]
    fn picker_accepts_english_words_in_french() {
        let _guard = crate::i18n::scoped("fr");
        let now = at(2026, 11, 18, 10, 0);
        assert_eq!(parse_when("demain", "7", now), Ok(at(2026, 11, 19, 7, 0)));
        assert_eq!(parse_when("tomorrow", "7", now), Ok(at(2026, 11, 19, 7, 0)));
        assert_eq!(parse_when("today", "11h", now), Ok(at(2026, 11, 18, 11, 0)));
        assert_eq!(
            parse_when("bientôt", "09:00", now),
            Err(SharedString::from("Date invalide : utilisez JJ/MM/AAAA."))
        );
        assert_eq!(
            parse_when("19/11/2026", "25:00", now),
            Err(SharedString::from("Heure invalide : utilisez HH:MM."))
        );
    }

    #[::core::prelude::v1::test]
    fn relative_labels() {
        let _guard = crate::i18n::scoped("fr");
        let now = at(2026, 11, 18, 10, 0);
        assert_eq!(format_when(at(2026, 11, 18, 20, 0), now), "aujourd'hui à 20:00");
        assert_eq!(format_when(at(2026, 11, 19, 8, 0), now), "demain à 8:00");
        assert_eq!(format_when(at(2026, 11, 17, 9, 5), now), "hier à 9:05");
        assert_eq!(format_when(at(2026, 11, 23, 9, 5), now), "lundi à 9:05");
        assert_eq!(format_when(at(2026, 12, 2, 9, 0), now), "le 2 déc. à 9:00");
        assert_eq!(format_when(at(2027, 1, 2, 9, 0), now), "le 2 janv. 2027 à 9:00");
    }

    #[::core::prelude::v1::test]
    fn relative_labels_in_english() {
        let _guard = crate::i18n::scoped("en");
        let now = at(2026, 11, 18, 10, 0);
        assert_eq!(format_when(at(2026, 11, 18, 9, 5), now), "today at 9:05 AM");
        assert_eq!(format_when(at(2026, 11, 19, 21, 5), now), "tomorrow at 9:05 PM");
        assert_eq!(format_when(at(2026, 11, 17, 9, 5), now), "yesterday at 9:05 AM");
        assert_eq!(format_when(at(2026, 11, 23, 9, 5), now), "Monday at 9:05 AM");
        assert_eq!(format_when(at(2026, 12, 2, 9, 0), now), "Dec 2 at 9:00 AM");
        assert_eq!(format_when(at(2027, 11, 5, 9, 5), now), "Nov 5, 2027 at 9:05 AM");
    }

    #[::core::prelude::v1::test]
    fn snippet_query_needs_a_word_start() {
        assert_eq!(snippet_query("/mer").map(|q| q.1), Some("mer".into()));
        assert_eq!(snippet_query("ok /adr").map(|q| (q.0, q.1)), Some((3, "adr".into())));
        assert!(snippet_query("/").is_none());
        assert!(snippet_query("et/ou").is_none());
        assert!(snippet_query("/mer ci").is_none());
        assert!(snippet_query("https://exemple.fr/page").is_none());
    }

    #[::core::prelude::v1::test]
    fn snippets_match_prefix_first_and_ignore_accents() {
        let snippets = vec![
            snippet("adresse", "12 rue des Lilas"),
            snippet("/merci", "Merci beaucoup !"),
            snippet("remerciements", "Encore merci"),
            snippet("vide", "   "),
            snippet("", "sans raccourci"),
            snippet("Écoute", "Je t'écoute"),
        ];
        let found: Vec<String> = match_snippets(&snippets, "mer")
            .into_iter()
            .map(|s| s.shortcut)
            .collect();
        assert_eq!(found, vec!["/merci".to_string(), "remerciements".to_string()]);
        assert_eq!(match_snippets(&snippets, "ecou")[0].text, "Je t'écoute");
        assert!(match_snippets(&snippets, "vide").is_empty());
        assert!(match_snippets(&snippets, "zzz").is_empty());
    }
}
