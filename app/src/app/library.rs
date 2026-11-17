use std::{cell::Cell, ops::Range, path::PathBuf, rc::Rc, time::Duration};

use chrono::{Datelike as _, Local, NaiveDate, TimeZone as _};
use gpui_kit::component::input::{InputEvent, InputState};
use gpui_kit::*;
use serde_json::{Value, json};

use super::{Gallery, GalleryTab, Lightbox, Media, MissiveApp, Older, Switcher};
use crate::model::{MediaPage, MediaResult, Message, SearchHit};

pub(crate) const OLDER_PAGE: usize = 60;
const GALLERY_PAGE: usize = 60;
const SEARCH_DEBOUNCE: Duration = Duration::from_millis(280);
const SEARCH_LIMIT: usize = 80;
const ZOOM_STEP: f32 = 1.25;
const MAX_ZOOM_PERCENT: f32 = 4.;
const WHEEL_LINE: f32 = 40.;

pub(crate) struct Viewer {
    pub(crate) zoom: f32,
    pub(crate) pan: (f32, f32),
    pub(crate) drag: Option<((f32, f32), (f32, f32))>,
    pub(crate) bounds: Rc<Cell<Bounds<Pixels>>>,
    pub(crate) busy: bool,
}

impl Default for Viewer {
    fn default() -> Self {
        Self {
            zoom: 1.,
            pan: (0., 0.),
            drag: None,
            bounds: Rc::new(Cell::new(Bounds::default())),
            busy: false,
        }
    }
}

impl Viewer {
    pub(crate) fn reset(&mut self) {
        self.zoom = 1.;
        self.pan = (0., 0.);
        self.drag = None;
    }

    pub(crate) fn is_fit(&self) -> bool {
        (self.zoom - 1.).abs() < 0.001 && self.pan == (0., 0.)
    }
}

pub(crate) struct ViewerLayout {
    pub(crate) container: (f32, f32),
    pub(crate) size: (f32, f32),
    pub(crate) pan: (f32, f32),
    pub(crate) percent: u32,
    pub(crate) fitted: bool,
    pub(crate) limits: (f32, f32),
}

pub(crate) fn fit_scale(natural: Option<(f32, f32)>, container: (f32, f32)) -> f32 {
    match natural {
        Some((w, h)) if w > 0. && h > 0. && container.0 > 0. && container.1 > 0. => {
            (container.0 / w).min(container.1 / h)
        }
        _ => 1.,
    }
}

pub(crate) fn fitted_size(natural: Option<(f32, f32)>, container: (f32, f32)) -> (f32, f32) {
    match natural {
        Some((w, h)) if w > 0. && h > 0. && container.0 > 0. && container.1 > 0. => {
            let scale = fit_scale(natural, container);
            (w * scale, h * scale)
        }
        _ => container,
    }
}

pub(crate) fn zoom_limits(fit: f32) -> (f32, f32) {
    let fit = if fit > 0. { fit } else { 1. };
    ((1. / fit).min(1.), (MAX_ZOOM_PERCENT / fit).max(1.))
}

pub(crate) fn zoom_about(
    zoom: f32,
    pan: (f32, f32),
    factor: f32,
    anchor: (f32, f32),
    limits: (f32, f32),
) -> (f32, (f32, f32)) {
    let next = (zoom * factor).clamp(limits.0, limits.1);
    let ratio = next / zoom;
    (
        next,
        (
            anchor.0 - (anchor.0 - pan.0) * ratio,
            anchor.1 - (anchor.1 - pan.1) * ratio,
        ),
    )
}

pub(crate) fn clamp_pan(pan: (f32, f32), size: (f32, f32), container: (f32, f32)) -> (f32, f32) {
    let max_x = ((size.0 - container.0) / 2.).max(0.);
    let max_y = ((size.1 - container.1) / 2.).max(0.);
    (pan.0.clamp(-max_x, max_x), pan.1.clamp(-max_y, max_y))
}

pub(crate) fn sniff_image_format(bytes: &[u8], path: &std::path::Path) -> Option<ImageFormat> {
    let by_magic = if bytes.starts_with(&[0x89, b'P', b'N', b'G']) {
        Some(ImageFormat::Png)
    } else if bytes.starts_with(&[0xFF, 0xD8, 0xFF]) {
        Some(ImageFormat::Jpeg)
    } else if bytes.starts_with(b"GIF8") {
        Some(ImageFormat::Gif)
    } else if bytes.len() >= 12 && &bytes[0..4] == b"RIFF" && &bytes[8..12] == b"WEBP" {
        Some(ImageFormat::Webp)
    } else if bytes.starts_with(b"BM") {
        Some(ImageFormat::Bmp)
    } else {
        None
    };
    by_magic.or_else(|| {
        let extension = path.extension()?.to_string_lossy().to_lowercase();
        match extension.as_str() {
            "png" => Some(ImageFormat::Png),
            "jpg" | "jpeg" => Some(ImageFormat::Jpeg),
            "gif" => Some(ImageFormat::Gif),
            "webp" => Some(ImageFormat::Webp),
            "bmp" => Some(ImageFormat::Bmp),
            _ => None,
        }
    })
}

pub(crate) fn image_extension(format: ImageFormat) -> &'static str {
    match format {
        ImageFormat::Png => "png",
        ImageFormat::Gif => "gif",
        ImageFormat::Webp => "webp",
        ImageFormat::Bmp => "bmp",
        _ => "jpg",
    }
}

pub(crate) fn with_extension(path: PathBuf, extension: &str) -> PathBuf {
    if path.extension().is_some() || extension.is_empty() {
        path
    } else {
        path.with_extension(extension)
    }
}

pub(crate) struct SearchPanel {
    pub(crate) position: Point<Pixels>,
    pub(crate) person: Entity<InputState>,
    pub(crate) conversation: Entity<InputState>,
    _subscriptions: Vec<Subscription>,
}

pub(crate) struct SearchChip {
    pub(crate) field: SearchField,
    pub(crate) label: String,
    pub(crate) invalid: bool,
    pub(crate) warning: bool,
}

#[derive(Clone, Debug, PartialEq, Eq)]
struct PersonCandidate {
    id: String,
    name: String,
    in_scope: bool,
}

fn rank_people(people: Vec<PersonCandidate>, wanted: &str) -> Vec<PersonCandidate> {
    let wanted = fold(wanted.trim());
    let mut scored: Vec<(bool, u8, PersonCandidate)> = people
        .into_iter()
        .filter_map(|person| {
            let score = if wanted.is_empty() {
                0
            } else {
                match_score(&person.name, &wanted)?
            };
            Some((!person.in_scope, score, person))
        })
        .collect();
    scored.sort_by_key(|(outside, score, _)| (*outside, *score));
    scored.into_iter().map(|(_, _, person)| person).collect()
}

#[derive(Default)]
pub(crate) struct SearchState {
    pub(crate) query: SearchQuery,
    pub(crate) sender: Option<(String, String)>,
    pub(crate) sender_outside: bool,
    pub(crate) thread: Option<(String, String)>,
    pub(crate) problems: Vec<SearchField>,
    pub(crate) loading: bool,
    pub(crate) panel: Option<SearchPanel>,
}

pub(crate) fn today() -> NaiveDate {
    Local::now().date_naive()
}

fn is_me_alias(value: &str) -> bool {
    matches!(fold(value.trim()).as_str(), "moi" | "me" | "vous" | "je")
}

impl MissiveApp {
    pub(crate) fn search_active(&self) -> bool {
        self.thread_ops.search.query.is_active()
    }

    pub(crate) fn listed_threads(&self, cx: &App) -> Vec<usize> {
        let query = &self.thread_ops.search.query;
        if !query.has_filters() && query.text == self.search_query {
            return self.visible_threads(cx);
        }
        let text = if query.has_filters() {
            String::new()
        } else {
            fold(&query.text)
        };
        self.threads
            .iter()
            .enumerate()
            .filter(|(_, thread)| self.thread_matches_filter(thread, self.filter))
            .filter(|(_, thread)| text.is_empty() || fold(&thread.name).contains(&text))
            .map(|(index, _)| index)
            .collect()
    }

    pub(crate) fn search_people(&self, wanted: &str) -> Vec<(String, String)> {
        self.ranked_people(wanted)
            .into_iter()
            .map(|person| (person.id, person.name))
            .collect()
    }

    fn ranked_people(&self, wanted: &str) -> Vec<PersonCandidate> {
        let mut people: Vec<PersonCandidate> = Vec::new();
        let scope = self.thread_ops.search.thread.as_ref().map(|(id, _)| id.as_str());
        let me = self.user.as_ref().map(|user| user.id.as_str());
        for thread in &self.threads {
            let in_scope = Some(thread.id.as_str()) == scope;
            let mut push = |id: &str, name: &str| {
                if id.is_empty() || name.trim().is_empty() || Some(id) == me {
                    return;
                }
                match people.iter_mut().find(|person| person.id == id) {
                    Some(person) => person.in_scope |= in_scope,
                    None => people.push(PersonCandidate {
                        id: id.to_string(),
                        name: name.to_string(),
                        in_scope,
                    }),
                }
            };
            if let (false, Some(peer)) = (thread.is_group, thread.peer_id.as_deref()) {
                push(peer, &thread.name);
            }
            for participant in &thread.participants {
                push(&participant.user_id, &participant.name);
            }
        }
        rank_people(people, wanted)
    }

    pub(crate) fn search_conversations(&self, wanted: &str) -> Vec<(String, String)> {
        let wanted = fold(wanted.trim());
        let mut scored: Vec<(u8, (String, String))> = self
            .threads
            .iter()
            .filter(|thread| thread.folder != "spam")
            .filter_map(|thread| {
                let score = if wanted.is_empty() {
                    0
                } else {
                    match_score(&thread.name, &wanted)?
                };
                Some((score, (thread.id.clone(), thread.name.clone())))
            })
            .collect();
        scored.sort_by_key(|(score, _)| *score);
        scored.into_iter().map(|(_, thread)| thread).collect()
    }

    fn resolve_search(&mut self) {
        let query = self.thread_ops.search.query.clone();
        let mut problems: Vec<SearchField> = Vec::new();
        let thread = query.within.as_deref().and_then(|wanted| {
            let found = best_match(
                self.threads
                    .iter()
                    .filter(|thread| thread.folder != "spam")
                    .map(|thread| (thread.name.as_str(), thread)),
                wanted,
            )
            .map(|thread| (thread.id.clone(), thread.name.clone()));
            if found.is_none() {
                problems.push(SearchField::Within);
            }
            found
        });
        self.thread_ops.search.thread = thread;
        let scoped = self.thread_ops.search.thread.is_some();
        let mut sender_outside = false;
        let sender = query.from.as_deref().and_then(|wanted| {
            let found = if is_me_alias(wanted) {
                self.user
                    .as_ref()
                    .map(|user| (user.id.clone(), "Moi".to_string()))
            } else {
                self.ranked_people(wanted).into_iter().next().map(|person| {
                    sender_outside = scoped && !person.in_scope;
                    (person.id, person.name)
                })
            };
            if found.is_none() {
                problems.push(SearchField::From);
            }
            found
        });
        if !query.invalid_kinds.is_empty() {
            problems.push(SearchField::InvalidKinds);
        }
        if query.before.as_ref().is_some_and(|bound| !bound.is_valid()) {
            problems.push(SearchField::Before);
        }
        if query.after.as_ref().is_some_and(|bound| !bound.is_valid()) {
            problems.push(SearchField::After);
        }
        let search = &mut self.thread_ops.search;
        search.sender = sender;
        search.sender_outside = sender_outside;
        search.problems = problems;
    }

    fn search_params(&self) -> Value {
        let search = &self.thread_ops.search;
        let query = &search.query;
        let mut params = json!({ "query": query.text, "limit": SEARCH_LIMIT });
        if let Some((id, _)) = &search.thread {
            params["thread_id"] = json!(id);
        }
        if let Some((id, _)) = &search.sender {
            params["sender_id"] = json!(id);
        }
        if !query.kinds.is_empty() {
            let kinds: Vec<&str> = query.kinds.iter().map(|kind| kind.bridge()).collect();
            params["kinds"] = json!(kinds);
        }
        if let Some(ms) = query.before.as_ref().and_then(DateBound::before_date).and_then(day_start_ms) {
            params["before_ms"] = json!(ms);
        }
        if let Some(ms) = query.after.as_ref().and_then(DateBound::after_date).and_then(day_start_ms) {
            params["after_ms"] = json!(ms);
        }
        params
    }

    pub(crate) fn set_search_query(
        &mut self,
        query: SearchQuery,
        window: &mut Window,
        cx: &mut Context<Self>,
    ) {
        let text = query.to_input();
        self.search
            .update(cx, |input, cx| input.set_value(text, window, cx));
        self.on_search_changed(cx);
        cx.notify();
    }

    pub(crate) fn remove_search_filter(
        &mut self,
        field: SearchField,
        window: &mut Window,
        cx: &mut Context<Self>,
    ) {
        let mut query = self.thread_ops.search.query.clone();
        query.remove(field);
        self.set_search_query(query, window, cx);
    }

    pub(crate) fn clear_search_filters(&mut self, window: &mut Window, cx: &mut Context<Self>) {
        let text = self.thread_ops.search.query.text.clone();
        let query = SearchQuery {
            text,
            ..SearchQuery::default()
        };
        self.set_search_query(query, window, cx);
    }

    pub(crate) fn toggle_search_kind(
        &mut self,
        kind: SearchKind,
        window: &mut Window,
        cx: &mut Context<Self>,
    ) {
        let mut query = self.thread_ops.search.query.clone();
        query.toggle_kind(kind);
        self.set_search_query(query, window, cx);
    }

    pub(crate) fn set_search_after(
        &mut self,
        raw: Option<String>,
        window: &mut Window,
        cx: &mut Context<Self>,
    ) {
        let mut query = self.thread_ops.search.query.clone();
        match raw {
            Some(raw) => query.set_after(raw, today()),
            None => query.after = None,
        }
        query.before = None;
        self.set_search_query(query, window, cx);
    }

    pub(crate) fn set_search_from(
        &mut self,
        value: Option<String>,
        window: &mut Window,
        cx: &mut Context<Self>,
    ) {
        let mut query = self.thread_ops.search.query.clone();
        query.from = value;
        self.set_search_query(query, window, cx);
    }

    pub(crate) fn set_search_within(
        &mut self,
        value: Option<String>,
        window: &mut Window,
        cx: &mut Context<Self>,
    ) {
        let mut query = self.thread_ops.search.query.clone();
        query.within = value;
        self.set_search_query(query, window, cx);
    }

    pub(crate) fn open_search_panel(
        &mut self,
        position: Point<Pixels>,
        window: &mut Window,
        cx: &mut Context<Self>,
    ) {
        if self.thread_ops.search.panel.take().is_some() {
            cx.notify();
            return;
        }
        let person = cx.new(|cx| InputState::new(window, cx).placeholder("Nom d'une personne"));
        let conversation =
            cx.new(|cx| InputState::new(window, cx).placeholder("Nom de la conversation"));
        let subscriptions = vec![
            cx.subscribe_in(&person, window, |this, _, event: &InputEvent, window, cx| {
                match event {
                    InputEvent::Change => cx.notify(),
                    InputEvent::PressEnter { .. } => this.apply_panel_person(window, cx),
                    _ => {}
                }
            }),
            cx.subscribe_in(
                &conversation,
                window,
                |this, _, event: &InputEvent, window, cx| match event {
                    InputEvent::Change => cx.notify(),
                    InputEvent::PressEnter { .. } => this.apply_panel_conversation(window, cx),
                    _ => {}
                },
            ),
        ];
        person.update(cx, |input, cx| input.focus(window, cx));
        self.thread_ops.search.panel = Some(SearchPanel {
            position,
            person,
            conversation,
            _subscriptions: subscriptions,
        });
        cx.notify();
    }

    pub(crate) fn close_search_panel(&mut self, cx: &mut Context<Self>) {
        if self.thread_ops.search.panel.take().is_some() {
            cx.notify();
        }
    }

    pub(crate) fn search_panel_enter(&mut self, window: &mut Window, cx: &mut Context<Self>) {
        let Some(panel) = self.thread_ops.search.panel.as_ref() else {
            return;
        };
        if panel.conversation.read(cx).focus_handle(cx).is_focused(window) {
            self.apply_panel_conversation(window, cx);
        } else if panel.person.read(cx).focus_handle(cx).is_focused(window) {
            self.apply_panel_person(window, cx);
        }
    }

    fn apply_panel_person(&mut self, window: &mut Window, cx: &mut Context<Self>) {
        let Some(panel) = self.thread_ops.search.panel.as_ref() else {
            return;
        };
        let typed = panel.person.read(cx).value().trim().to_string();
        if typed.is_empty() {
            return;
        }
        let input = panel.person.clone();
        let value = if is_me_alias(&typed) {
            "moi".to_string()
        } else {
            self.search_people(&typed)
                .into_iter()
                .next()
                .map(|(_, name)| name)
                .unwrap_or(typed)
        };
        input.update(cx, |input, cx| input.clean(window, cx));
        self.set_search_from(Some(value), window, cx);
    }

    fn apply_panel_conversation(&mut self, window: &mut Window, cx: &mut Context<Self>) {
        let Some(panel) = self.thread_ops.search.panel.as_ref() else {
            return;
        };
        let typed = panel.conversation.read(cx).value().trim().to_string();
        if typed.is_empty() {
            return;
        }
        let input = panel.conversation.clone();
        let value = self
            .search_conversations(&typed)
            .into_iter()
            .next()
            .map(|(_, name)| name)
            .unwrap_or(typed);
        input.update(cx, |input, cx| input.clean(window, cx));
        self.set_search_within(Some(value), window, cx);
    }

    pub(crate) fn search_excerpt(&self, body: &str) -> (String, Vec<Range<usize>>) {
        let ranges = highlight_ranges(body, &self.search_terms());
        excerpt(body, &ranges)
    }

    pub(crate) fn search_chips(&self) -> Vec<SearchChip> {
        let search = &self.thread_ops.search;
        let query = &search.query;
        let problem = |field: SearchField| search.problems.contains(&field);
        let mut chips: Vec<SearchChip> = Vec::new();
        let mut push = |field: SearchField, label: String, invalid: bool| {
            chips.push(SearchChip {
                field,
                label,
                invalid,
                warning: false,
            })
        };
        if let Some(raw) = &query.from {
            let label = match &search.sender {
                Some((_, name)) if search.sender_outside => {
                    format!("De : {name} · pas dans cette conversation")
                }
                Some((_, name)) => format!("De : {name}"),
                None => format!("De : {raw} · introuvable"),
            };
            push(SearchField::From, label, problem(SearchField::From));
        }
        if let Some(raw) = &query.within {
            let label = match &search.thread {
                Some((_, name)) if !name.trim().is_empty() => format!("Dans : {name}"),
                Some(_) => "Dans : Conversation".to_string(),
                None => format!("Dans : {raw} · introuvable"),
            };
            push(SearchField::Within, label, problem(SearchField::Within));
        }
        for kind in &query.kinds {
            push(SearchField::Kind(*kind), kind.label().to_string(), false);
        }
        if !query.invalid_kinds.is_empty() {
            let label = format!("Type inconnu : {}", query.invalid_kinds.join(", "));
            push(SearchField::InvalidKinds, label, true);
        }
        if let Some(bound) = &query.after {
            let label = format!("Après : {}", bound.raw);
            push(SearchField::After, label, !bound.is_valid());
        }
        if let Some(bound) = &query.before {
            let label = format!("Avant : {}", bound.raw);
            push(SearchField::Before, label, !bound.is_valid());
        }
        if search.sender_outside {
            if let Some(chip) = chips.iter_mut().find(|chip| chip.field == SearchField::From) {
                chip.warning = true;
            }
        }
        chips
    }

    pub(crate) fn search_notice_text(&self) -> Option<String> {
        let search = &self.thread_ops.search;
        if !search.sender_outside {
            return None;
        }
        let (_, person) = search.sender.as_ref()?;
        let (_, thread) = search.thread.as_ref()?;
        Some(format!(
            "{person} ne fait pas partie de « {thread} » : aucun message de cette personne ici."
        ))
    }

    pub(crate) fn search_problem_text(&self) -> Option<String> {
        let search = &self.thread_ops.search;
        let query = &search.query;
        Some(match search.problems.first()? {
            SearchField::From => format!(
                "Aucune personne ne correspond à « {} ».",
                query.from.clone().unwrap_or_default()
            ),
            SearchField::Within => format!(
                "Aucune conversation ne correspond à « {} ».",
                query.within.clone().unwrap_or_default()
            ),
            SearchField::InvalidKinds => {
                "Types reconnus après a: photo, vidéo, lien, fichier, vocal, position.".into()
            }
            SearchField::Before | SearchField::After => {
                "Date non reconnue. Exemples : 12/03/2024, 2024-03-12, hier, mars 2024, 2023."
                    .into()
            }
            SearchField::Kind(_) => return None,
        })
    }

    pub(crate) fn search_kind_options(&self) -> Vec<(SearchKind, bool)> {
        let kinds = &self.thread_ops.search.query.kinds;
        SearchKind::ALL
            .into_iter()
            .map(|kind| (kind, kinds.contains(&kind)))
            .collect()
    }

    pub(crate) fn search_period_options(&self) -> Vec<(&'static str, String, bool)> {
        let today = today();
        let ago = |days: i64| {
            (today - chrono::Duration::days(days))
                .format("%d/%m/%Y")
                .to_string()
        };
        let current = self
            .thread_ops
            .search
            .query
            .after
            .as_ref()
            .and_then(DateBound::after_date);
        [
            ("Aujourd'hui", "hier".to_string()),
            ("7 derniers jours", ago(7)),
            ("30 derniers jours", ago(30)),
            ("Cette année", (today.year() - 1).to_string()),
        ]
        .into_iter()
        .map(|(label, raw)| {
            let start = parse_period(&raw, today).map(|period| period.end);
            let active = current.is_some() && start == current;
            (label, raw, active)
        })
        .collect()
    }

    pub(crate) fn search_terms(&self) -> Vec<String> {
        self.thread_ops
            .search
            .query
            .text
            .split_whitespace()
            .map(fold)
            .filter(|term| term.chars().count() >= 2)
            .collect()
    }

    pub(crate) fn maybe_load_older(&mut self, cx: &mut Context<Self>) {
        let Some(thread_id) = self.selected.clone() else {
            return;
        };
        if self.scroller.read(cx).logical_scroll_top().item_ix > 2 {
            return;
        }
        let Some(bridge) = self.bridge() else { return };
        let Some((oldest, generation)) = self.begin_older_page(&thread_id) else {
            return;
        };
        cx.spawn(async move |this, cx| {
            let result = bridge
                .call::<Vec<Message>>(
                    "messages",
                    json!({ "thread_id": thread_id, "before_ms": oldest, "limit": OLDER_PAGE }),
                )
                .await;
            let _ = this.update(cx, |this, cx| {
                this.merge_older_page(&thread_id, oldest, generation, result, cx);
            });
        })
        .detach();
    }

    pub(crate) fn begin_older_page(&mut self, thread_id: &str) -> Option<(i64, u64)> {
        let oldest = self.messages.get(thread_id)?.first()?.timestamp_ms;
        let generation = self.generation(thread_id);
        let state = self.older.entry(thread_id.to_string()).or_default();
        if state.loading || state.exhausted {
            return None;
        }
        state.loading = true;
        Some((oldest, generation))
    }

    pub(crate) fn merge_older_page(
        &mut self,
        thread_id: &str,
        oldest: i64,
        generation: u64,
        result: anyhow::Result<Vec<Message>>,
        cx: &mut Context<Self>,
    ) -> Option<usize> {
        self.older
            .entry(thread_id.to_string())
            .or_insert_with(Older::default)
            .loading = false;
        if self.generation(thread_id) != generation || !self.messages.contains_key(thread_id) {
            return None;
        }
        let mut older = match result {
            Ok(older) => older,
            Err(err) => {
                self.set_error(err.context("Historique indisponible"));
                cx.notify();
                return None;
            }
        };
        let known: std::collections::HashSet<String> = self
            .messages
            .get(thread_id)
            .map(|list| list.iter().map(|m| m.id.clone()).collect())
            .unwrap_or_default();
        older.retain(|message| message.timestamp_ms < oldest && !known.contains(&message.id));
        older.sort_by_key(|message| message.timestamp_ms);
        let count = older.len();
        if count == 0 {
            self.older
                .entry(thread_id.to_string())
                .or_insert_with(Older::default)
                .exhausted = true;
        } else {
            if let Some(list) = self.messages.get_mut(thread_id) {
                older.extend(list.drain(..));
                *list = older;
            }
            if self.selected.as_deref() == Some(thread_id) {
                self.scroller.update(cx, |scroller, cx| {
                    scroller.prepend(count, cx);
                    scroller.remeasure_items(count..count + 1, cx);
                });
            }
        }
        cx.notify();
        Some(count)
    }

    pub(crate) fn on_search_changed(&mut self, cx: &mut Context<Self>) {
        let query = self.search.read(cx).value().trim().to_string();
        self.search_query = query.clone();
        self.thread_ops.search.query = parse_query(&query, today());
        self.resolve_search();
        let search = &self.thread_ops.search;
        if !search.query.is_active() || !search.problems.is_empty() {
            self.search_hits.clear();
            self.thread_ops.search.loading = false;
            self._search_task = None;
            return;
        }
        let params = self.search_params();
        let Some(bridge) = self.bridge() else { return };
        self.thread_ops.search.loading = true;
        self._search_task = Some(cx.spawn(async move |this, cx| {
            cx.background_executor().timer(SEARCH_DEBOUNCE).await;
            let result = bridge.call::<Vec<SearchHit>>("search", params).await;
            let _ = this.update(cx, |this, cx| {
                if this.search_query == query {
                    this.search_hits = result.unwrap_or_default();
                    this.thread_ops.search.loading = false;
                    cx.notify();
                }
            });
        }));
    }

    pub(crate) fn open_search_hit(
        &mut self,
        hit: SearchHit,
        window: &mut Window,
        cx: &mut Context<Self>,
    ) {
        self.pending_scroll = Some((hit.thread_id.clone(), hit.message.id.clone()));
        self.select_thread(hit.thread_id.clone(), window, cx);
        let index = self
            .messages
            .get(&hit.thread_id)
            .and_then(|list| list.iter().position(|m| m.id == hit.message.id));
        if let Some(index) = index {
            self.pending_scroll = None;
            self.scroller
                .update(cx, |scroller, cx| scroller.scroll_to_item(index, cx));
        }
    }

    pub(crate) fn open_switcher(&mut self, window: &mut Window, cx: &mut Context<Self>) {
        if self.switcher.is_some() {
            self.close_switcher(cx);
            return;
        }
        let input = cx.new(|cx| InputState::new(window, cx).placeholder("Aller à une discussion…"));
        let subscription = cx.subscribe_in(
            &input,
            window,
            |this, _, event: &InputEvent, window, cx| match event {
                InputEvent::Change => {
                    if let Some(switcher) = this.switcher.as_mut() {
                        switcher.selected = 0;
                    }
                    cx.notify();
                }
                InputEvent::PressEnter { .. } => this.confirm_switcher(window, cx),
                _ => {}
            },
        );
        input.update(cx, |input, cx| input.focus(window, cx));
        self.switcher = Some(Switcher {
            input,
            selected: 0,
            _subscription: subscription,
        });
        cx.notify();
    }

    pub(crate) fn close_switcher(&mut self, cx: &mut Context<Self>) {
        self.switcher = None;
        cx.notify();
    }

    pub(crate) fn switcher_results(&self, cx: &App) -> Vec<usize> {
        let Some(switcher) = &self.switcher else {
            return Vec::new();
        };
        let query = fold(&switcher.input.read(cx).value());
        self.threads
            .iter()
            .enumerate()
            .filter(|(_, thread)| query.is_empty() || fold(&thread.name).contains(&query))
            .map(|(index, _)| index)
            .take(8)
            .collect()
    }

    pub(crate) fn move_switcher(&mut self, delta: isize, cx: &mut Context<Self>) {
        let count = self.switcher_results(cx).len();
        if let Some(switcher) = self.switcher.as_mut() {
            if count > 0 {
                let next = (switcher.selected as isize + delta).rem_euclid(count as isize);
                switcher.selected = next as usize;
            }
        }
        cx.notify();
    }

    pub(crate) fn confirm_switcher(&mut self, window: &mut Window, cx: &mut Context<Self>) {
        let results = self.switcher_results(cx);
        let selected = self.switcher.as_ref().map(|s| s.selected).unwrap_or(0);
        let thread_id = results
            .get(selected)
            .and_then(|index| self.threads.get(*index))
            .map(|thread| thread.id.clone());
        self.switcher = None;
        if let Some(thread_id) = thread_id {
            self.select_thread(thread_id, window, cx);
        }
        cx.notify();
    }

    pub(crate) fn step_thread(
        &mut self,
        delta: isize,
        window: &mut Window,
        cx: &mut Context<Self>,
    ) {
        let visible = self.listed_threads(cx);
        if visible.is_empty() {
            return;
        }
        let current = self.selected.as_ref().and_then(|id| {
            visible
                .iter()
                .position(|index| self.threads.get(*index).is_some_and(|t| &t.id == id))
        });
        let next = match current {
            Some(position) => {
                (position as isize + delta).clamp(0, visible.len() as isize - 1) as usize
            }
            None => 0,
        };
        if let Some(thread) = self.threads.get(visible[next]) {
            let id = thread.id.clone();
            self.select_thread(id, window, cx);
        }
    }

    pub(crate) fn open_gallery(&mut self, tab: GalleryTab, cx: &mut Context<Self>) {
        let Some(thread_id) = self.selected.clone() else {
            return;
        };
        self.gallery = Some(Gallery {
            thread_id,
            tab,
            items: Vec::new(),
            has_more: true,
            loading: false,
            error: None,
            scroll: ScrollHandle::new(),
            store_pass_done: false,
        });
        self.load_gallery_page(cx);
        cx.notify();
    }

    pub(crate) fn close_gallery(&mut self, cx: &mut Context<Self>) {
        self.gallery = None;
        cx.notify();
    }

    pub(crate) fn load_gallery_page(&mut self, cx: &mut Context<Self>) {
        let Some(gallery) = self.gallery.as_mut() else {
            return;
        };
        if gallery.loading || !gallery.has_more {
            return;
        }
        gallery.loading = true;
        gallery.error = None;
        let thread_id = gallery.thread_id.clone();
        let tab = gallery.tab;
        let mut params = json!({
            "thread_id": thread_id,
            "limit": GALLERY_PAGE,
            "kinds": tab.kinds(),
        });
        if let Some(oldest) = gallery.items.last() {
            params["before_ms"] = json!(oldest.timestamp_ms);
        }
        let store_pass = !gallery.store_pass_done;
        params["max_pages"] = json!(if store_pass { 0 } else { 3 });
        let Some(bridge) = self.bridge() else { return };
        cx.spawn(async move |this, cx| {
            let result = bridge.call::<MediaPage>("media_history", params).await;
            let _ = this.update(cx, |this, cx| {
                let Some(gallery) = this.gallery.as_mut() else {
                    return;
                };
                if gallery.thread_id != thread_id || gallery.tab != tab {
                    return;
                }
                gallery.loading = false;
                match result {
                    Ok(page) => {
                        gallery.has_more = if store_pass {
                            gallery.store_pass_done = true;
                            true
                        } else {
                            page.has_more && !page.items.is_empty()
                        };
                        let known: std::collections::HashSet<(String, String)> = gallery
                            .items
                            .iter()
                            .map(|item| (item.message_id.clone(), item.attachment.id.clone()))
                            .collect();
                        let fresh: Vec<_> = page
                            .items
                            .into_iter()
                            .filter(|item| {
                                !known.contains(&(
                                    item.message_id.clone(),
                                    item.attachment.id.clone(),
                                ))
                            })
                            .collect();
                        gallery.items.extend(fresh);
                    }
                    Err(err) => {
                        gallery.error = Some(format!("{err:#}").into());
                        gallery.has_more = false;
                    }
                }
                cx.notify();
            });
        })
        .detach();
    }

    pub(crate) fn open_lightbox(
        &mut self,
        items: Vec<(Message, crate::model::Attachment)>,
        index: usize,
        cx: &mut Context<Self>,
    ) {
        if items.is_empty() {
            return;
        }
        let index = index.min(items.len() - 1);
        self.lightbox = Some(Lightbox { items, index });
        self.find.viewer.reset();
        cx.notify();
    }

    pub(crate) fn step_lightbox(&mut self, delta: isize, cx: &mut Context<Self>) {
        if let Some(lightbox) = self.lightbox.as_mut() {
            let len = lightbox.items.len() as isize;
            let next = (lightbox.index as isize + delta).clamp(0, len - 1) as usize;
            if next != lightbox.index {
                lightbox.index = next;
                self.find.viewer.reset();
            }
            cx.notify();
        }
    }

    pub(crate) fn close_lightbox(&mut self, cx: &mut Context<Self>) {
        self.lightbox = None;
        self.find.viewer.reset();
        cx.notify();
    }

    fn lightbox_natural_size(&self) -> Option<(f32, f32)> {
        let lightbox = self.lightbox.as_ref()?;
        let (_, attachment) = lightbox.items.get(lightbox.index)?;
        match (attachment.width, attachment.height) {
            (Some(w), Some(h)) if w > 0 && h > 0 => Some((w as f32, h as f32)),
            _ => None,
        }
    }

    pub(crate) fn lightbox_layout(&self) -> ViewerLayout {
        let bounds = self.find.viewer.bounds.get();
        let container = (f32::from(bounds.size.width), f32::from(bounds.size.height));
        let natural = self.lightbox_natural_size();
        let fit = fit_scale(natural, container);
        let base = fitted_size(natural, container);
        let viewer = &self.find.viewer;
        ViewerLayout {
            container,
            size: (base.0 * viewer.zoom, base.1 * viewer.zoom),
            pan: viewer.pan,
            percent: (viewer.zoom * fit * 100.).round() as u32,
            fitted: viewer.is_fit(),
            limits: zoom_limits(fit),
        }
    }

    fn apply_zoom(&mut self, factor: f32, anchor: (f32, f32), cx: &mut Context<Self>) {
        let layout = self.lightbox_layout();
        let viewer = &self.find.viewer;
        let (zoom, pan) = zoom_about(viewer.zoom, viewer.pan, factor, anchor, layout.limits);
        self.set_lightbox_zoom(zoom, pan, cx);
    }

    fn set_lightbox_zoom(&mut self, zoom: f32, pan: (f32, f32), cx: &mut Context<Self>) {
        let bounds = self.find.viewer.bounds.get();
        let container = (f32::from(bounds.size.width), f32::from(bounds.size.height));
        let base = fitted_size(self.lightbox_natural_size(), container);
        let viewer = &mut self.find.viewer;
        viewer.zoom = if (zoom - 1.).abs() < 0.001 { 1. } else { zoom };
        viewer.pan = clamp_pan(pan, (base.0 * viewer.zoom, base.1 * viewer.zoom), container);
        cx.notify();
    }

    pub(crate) fn zoom_lightbox(&mut self, step: i32, cx: &mut Context<Self>) {
        if self.lightbox.is_none() {
            return;
        }
        self.apply_zoom(ZOOM_STEP.powi(step), (0., 0.), cx);
    }

    pub(crate) fn reset_lightbox_zoom(&mut self, cx: &mut Context<Self>) {
        self.find.viewer.reset();
        cx.notify();
    }

    pub(crate) fn toggle_lightbox_actual_size(
        &mut self,
        position: Point<Pixels>,
        cx: &mut Context<Self>,
    ) {
        if !self.find.viewer.is_fit() {
            self.reset_lightbox_zoom(cx);
            return;
        }
        let layout = self.lightbox_layout();
        let actual = 100. / layout.percent.max(1) as f32;
        let target = if (actual - 1.).abs() < 0.05 { 2. } else { actual };
        let anchor = self.viewer_anchor(position);
        let viewer = &self.find.viewer;
        let (zoom, pan) =
            zoom_about(viewer.zoom, viewer.pan, target / viewer.zoom, anchor, layout.limits);
        self.set_lightbox_zoom(zoom, pan, cx);
    }

    fn viewer_anchor(&self, position: Point<Pixels>) -> (f32, f32) {
        let center = self.find.viewer.bounds.get().center();
        (
            f32::from(position.x - center.x),
            f32::from(position.y - center.y),
        )
    }

    pub(crate) fn lightbox_wheel(&mut self, event: &ScrollEvent, cx: &mut Context<Self>) {
        if self.lightbox.is_none() {
            return;
        }
        let delta = f32::from(event.delta.pixel_delta(px(WHEEL_LINE)).y);
        if delta.abs() < f32::EPSILON {
            return;
        }
        let anchor = self.viewer_anchor(event.position);
        self.apply_zoom(ZOOM_STEP.powf(delta / WHEEL_LINE), anchor, cx);
    }

    pub(crate) fn start_lightbox_drag(&mut self, position: Point<Pixels>) {
        let viewer = &mut self.find.viewer;
        if viewer.is_fit() {
            return;
        }
        viewer.drag = Some((
            (f32::from(position.x), f32::from(position.y)),
            viewer.pan,
        ));
    }

    pub(crate) fn drag_lightbox(&mut self, event: &MouseMoveEvent, cx: &mut Context<Self>) {
        let Some((start, pan)) = self.find.viewer.drag else {
            return;
        };
        if !event.dragging() {
            self.find.viewer.drag = None;
            return;
        }
        let moved = (
            pan.0 + f32::from(event.position.x) - start.0,
            pan.1 + f32::from(event.position.y) - start.1,
        );
        let zoom = self.find.viewer.zoom;
        self.set_lightbox_zoom(zoom, moved, cx);
    }

    pub(crate) fn end_lightbox_drag(&mut self) {
        self.find.viewer.drag = None;
    }

    fn with_lightbox_file(
        &mut self,
        cx: &mut Context<Self>,
        then: impl FnOnce(&mut Self, PathBuf, &mut Context<Self>) + 'static,
    ) {
        let Some((message, attachment)) = self
            .lightbox
            .as_ref()
            .and_then(|lightbox| lightbox.items.get(lightbox.index))
            .cloned()
        else {
            return;
        };
        let key = super::media_key(&message.id, &attachment.id);
        if let Some(Media::Ready(path)) = self.media.get(&key) {
            let path = path.clone();
            then(self, path, cx);
            return;
        }
        if message.id.starts_with("local-") {
            if let Some(Media::Ready(path)) =
                self.media.get(&super::thumb_key(&message.id, &attachment.id))
            {
                let path = path.clone();
                then(self, path, cx);
            }
            return;
        }
        let Some(bridge) = self.bridge() else { return };
        if self.find.viewer.busy {
            return;
        }
        self.find.viewer.busy = true;
        cx.notify();
        cx.spawn(async move |this, cx| {
            let result = bridge
                .call::<MediaResult>(
                    "media",
                    json!({
                        "thread_id": message.thread_id,
                        "message_id": message.id,
                        "attachment_id": attachment.id,
                    }),
                )
                .await;
            let _ = this.update(cx, |this, cx| {
                this.find.viewer.busy = false;
                match result {
                    Ok(media) => {
                        let path = PathBuf::from(media.path);
                        this.media.insert(key, Media::Ready(path.clone()));
                        then(this, path, cx);
                    }
                    Err(_) => this.show_toast("Image indisponible pour le moment", cx),
                }
                cx.notify();
            });
        })
        .detach();
    }

    pub(crate) fn copy_lightbox_image(&mut self, cx: &mut Context<Self>) {
        self.with_lightbox_file(cx, |this, path, cx| {
            let image = std::fs::read(&path).ok().and_then(|bytes| {
                sniff_image_format(&bytes, &path).map(|format| Image::from_bytes(format, bytes))
            });
            match image {
                Some(image) => {
                    cx.write_to_clipboard(ClipboardItem::new_image(&image));
                    this.show_toast("Image copiée", cx);
                }
                None => this.show_toast("Impossible de copier cette image", cx),
            }
        });
    }

    pub(crate) fn save_lightbox_image(&mut self, cx: &mut Context<Self>) {
        let suggested = self
            .lightbox
            .as_ref()
            .and_then(|lightbox| lightbox.items.get(lightbox.index))
            .and_then(|(_, attachment)| attachment.file_name.clone())
            .filter(|name| !name.trim().is_empty());
        self.with_lightbox_file(cx, move |_, source, cx| {
            let extension = source
                .extension()
                .map(|ext| ext.to_string_lossy().into_owned())
                .or_else(|| {
                    std::fs::read(&source)
                        .ok()
                        .and_then(|bytes| sniff_image_format(&bytes, &source))
                        .map(|format| image_extension(format).to_string())
                })
                .unwrap_or_else(|| "jpg".into());
            let name = match suggested {
                Some(name) => with_extension(PathBuf::from(name), &extension)
                    .to_string_lossy()
                    .into_owned(),
                None => format!(
                    "Missive {}.{extension}",
                    chrono::Local::now().format("%Y-%m-%d %H%M%S")
                ),
            };
            let directory = dirs::picture_dir()
                .or_else(dirs::download_dir)
                .or_else(dirs::home_dir)
                .unwrap_or_default();
            let receiver = cx.prompt_for_new_path(&directory, Some(&name));
            cx.spawn(async move |this, cx| {
                let Ok(Ok(Some(target))) = receiver.await else {
                    return;
                };
                let copied = std::fs::copy(&source, &target);
                let _ = this.update(cx, |this, cx| match copied {
                    Ok(_) => this.show_toast("Image enregistrée", cx),
                    Err(_) => this.show_toast("Impossible d'enregistrer l'image", cx),
                });
            })
            .detach();
        });
    }

    pub(crate) fn open_lightbox_externally(&mut self, cx: &mut Context<Self>) {
        self.with_lightbox_file(cx, |_, path, cx| crate::desktop::open_local(&path, cx));
    }

    pub(crate) fn video_opening(&self, message_id: &str, attachment_id: &str) -> bool {
        matches!(
            self.media.get(&super::media_key(message_id, attachment_id)),
            Some(Media::Loading)
        )
    }

    pub(crate) fn open_video(
        &mut self,
        message: &Message,
        attachment: &crate::model::Attachment,
        cx: &mut Context<Self>,
    ) {
        let key = super::media_key(&message.id, &attachment.id);
        match self.media.get(&key) {
            Some(Media::Ready(path)) => {
                let path = path.clone();
                crate::desktop::open_local(&path, cx);
                return;
            }
            Some(Media::Loading) => return,
            _ => {}
        }
        if attachment.id.is_empty() || message.id.starts_with("local-") {
            if let Some(url) = attachment.url.as_deref() {
                crate::desktop::open_web(url, cx);
            }
            return;
        }
        let Some(bridge) = self.bridge() else { return };
        self.media.insert(key.clone(), Media::Loading);
        cx.notify();
        let params = json!({
            "thread_id": message.thread_id,
            "message_id": message.id,
            "attachment_id": attachment.id,
        });
        cx.spawn(async move |this, cx| {
            let result = bridge.call::<MediaResult>("media", params).await;
            let _ = this.update(cx, |this, cx| {
                match result {
                    Ok(media) => {
                        let path = PathBuf::from(media.path);
                        this.media.insert(key, Media::Ready(path.clone()));
                        crate::desktop::open_local(&path, cx);
                    }
                    Err(err) => {
                        eprintln!("[app] video download failed: {err:#}");
                        this.media.remove(&key);
                        this.show_toast("Impossible d'ouvrir cette vidéo", cx);
                    }
                }
                cx.notify();
            });
        })
        .detach();
    }

    pub(crate) fn thread_images(
        &self,
        thread_id: &str,
    ) -> Vec<(Message, crate::model::Attachment)> {
        self.messages
            .get(thread_id)
            .map(|messages| {
                messages
                    .iter()
                    .flat_map(|message| {
                        message
                            .attachments
                            .iter()
                            .filter(|a| a.kind == "image")
                            .map(move |a| (message.clone(), a.clone()))
                    })
                    .collect()
            })
            .unwrap_or_default()
    }
}

pub(crate) fn item_message(item: &crate::model::MediaItem) -> Message {
    Message {
        id: item.message_id.clone(),
        thread_id: item.thread_id.clone(),
        sender_id: String::new(),
        sender_name: item.sender_name.clone(),
        text: String::new(),
        timestamp_ms: item.timestamp_ms,
        is_outgoing: item.is_outgoing,
        attachments: vec![item.attachment.clone()],
        ..Message::default()
    }
}

#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub(crate) enum SearchKind {
    Photo,
    Video,
    Link,
    File,
    Audio,
    Location,
}

impl SearchKind {
    pub(crate) const ALL: [SearchKind; 6] = [
        SearchKind::Photo,
        SearchKind::Video,
        SearchKind::Link,
        SearchKind::File,
        SearchKind::Audio,
        SearchKind::Location,
    ];

    fn parse(value: &str) -> Option<Self> {
        match fold(value).as_str() {
            "photo" | "photos" | "image" | "images" => Some(SearchKind::Photo),
            "video" | "videos" => Some(SearchKind::Video),
            "lien" | "liens" | "link" | "links" => Some(SearchKind::Link),
            "fichier" | "fichiers" | "file" | "files" | "document" | "documents" => {
                Some(SearchKind::File)
            }
            "vocal" | "vocaux" | "audio" | "audios" => Some(SearchKind::Audio),
            "position" | "positions" | "lieu" | "lieux" | "location" => {
                Some(SearchKind::Location)
            }
            _ => None,
        }
    }

    pub(crate) fn bridge(self) -> &'static str {
        match self {
            SearchKind::Photo => "image",
            SearchKind::Video => "video",
            SearchKind::Link => "link",
            SearchKind::File => "file",
            SearchKind::Audio => "audio",
            SearchKind::Location => "location",
        }
    }

    fn token(self) -> &'static str {
        match self {
            SearchKind::Photo => "photo",
            SearchKind::Video => "vidéo",
            SearchKind::Link => "lien",
            SearchKind::File => "fichier",
            SearchKind::Audio => "vocal",
            SearchKind::Location => "position",
        }
    }

    pub(crate) fn label(self) -> &'static str {
        match self {
            SearchKind::Photo => "Photos",
            SearchKind::Video => "Vidéos",
            SearchKind::Link => "Liens",
            SearchKind::File => "Fichiers",
            SearchKind::Audio => "Messages vocaux",
            SearchKind::Location => "Positions",
        }
    }
}

#[derive(Clone, Debug, PartialEq, Eq)]
pub(crate) struct DateBound {
    pub(crate) raw: String,
    pub(crate) period: Option<Period>,
}

impl DateBound {
    fn new(raw: String, today: NaiveDate) -> Self {
        let period = parse_period(&raw, today);
        Self { raw, period }
    }

    pub(crate) fn is_valid(&self) -> bool {
        self.period.is_some()
    }

    fn before_date(&self) -> Option<NaiveDate> {
        self.period.map(|period| period.start)
    }

    fn after_date(&self) -> Option<NaiveDate> {
        self.period.map(|period| period.end)
    }
}

#[derive(Clone, Debug, Default, PartialEq, Eq)]
pub(crate) struct SearchQuery {
    pub(crate) text: String,
    pub(crate) from: Option<String>,
    pub(crate) within: Option<String>,
    pub(crate) kinds: Vec<SearchKind>,
    pub(crate) invalid_kinds: Vec<String>,
    pub(crate) before: Option<DateBound>,
    pub(crate) after: Option<DateBound>,
}

#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub(crate) enum SearchField {
    From,
    Within,
    Kind(SearchKind),
    InvalidKinds,
    Before,
    After,
}

impl SearchQuery {
    pub(crate) fn has_filters(&self) -> bool {
        self.from.is_some()
            || self.within.is_some()
            || !self.kinds.is_empty()
            || !self.invalid_kinds.is_empty()
            || self.before.is_some()
            || self.after.is_some()
    }

    pub(crate) fn is_active(&self) -> bool {
        self.has_filters() || self.text.chars().count() >= 2
    }

    pub(crate) fn remove(&mut self, field: SearchField) {
        match field {
            SearchField::From => self.from = None,
            SearchField::Within => self.within = None,
            SearchField::Kind(kind) => self.kinds.retain(|k| *k != kind),
            SearchField::InvalidKinds => self.invalid_kinds.clear(),
            SearchField::Before => self.before = None,
            SearchField::After => self.after = None,
        }
    }

    pub(crate) fn toggle_kind(&mut self, kind: SearchKind) {
        if self.kinds.contains(&kind) {
            self.kinds.retain(|k| *k != kind);
        } else {
            self.kinds.push(kind);
        }
    }

    pub(crate) fn set_after(&mut self, raw: String, today: NaiveDate) {
        self.after = Some(DateBound::new(raw, today));
    }

    pub(crate) fn to_input(&self) -> String {
        let mut parts: Vec<String> = Vec::new();
        if !self.text.is_empty() {
            parts.push(self.text.clone());
        }
        if let Some(from) = &self.from {
            parts.push(format!("de:{}", quote(from)));
        }
        if let Some(within) = &self.within {
            parts.push(format!("dans:{}", quote(within)));
        }
        for kind in &self.kinds {
            parts.push(format!("a:{}", kind.token()));
        }
        for kind in &self.invalid_kinds {
            parts.push(format!("a:{}", quote(kind)));
        }
        if let Some(after) = &self.after {
            parts.push(format!("après:{}", quote(&after.raw)));
        }
        if let Some(before) = &self.before {
            parts.push(format!("avant:{}", quote(&before.raw)));
        }
        parts.join(" ")
    }
}

fn quote(value: &str) -> String {
    if value.chars().any(char::is_whitespace) {
        format!("\"{value}\"")
    } else {
        value.to_string()
    }
}

fn tokenize(input: &str) -> Vec<String> {
    let mut tokens = Vec::new();
    let mut current = String::new();
    let mut quoted = false;
    for c in input.chars() {
        match c {
            '"' | '“' | '”' | '«' | '»' => quoted = !quoted,
            c if c.is_whitespace() && !quoted => {
                if !current.trim().is_empty() {
                    tokens.push(current.trim().to_string());
                }
                current.clear();
            }
            c => current.push(c),
        }
    }
    if !current.trim().is_empty() {
        tokens.push(current.trim().to_string());
    }
    tokens
}

const MONTH_NAMES: [(&str, u32); 21] = [
    ("janvier", 1),
    ("janv", 1),
    ("fevrier", 2),
    ("fevr", 2),
    ("fev", 2),
    ("mars", 3),
    ("avril", 4),
    ("avr", 4),
    ("mai", 5),
    ("juin", 6),
    ("juillet", 7),
    ("juil", 7),
    ("aout", 8),
    ("septembre", 9),
    ("sept", 9),
    ("octobre", 10),
    ("oct", 10),
    ("novembre", 11),
    ("nov", 11),
    ("decembre", 12),
    ("dec", 12),
];

fn month_number(word: &str) -> Option<u32> {
    let word = fold(word);
    let word = word.trim_end_matches('.');
    MONTH_NAMES
        .iter()
        .find(|(name, _)| *name == word)
        .map(|(_, number)| *number)
}

fn parse_year(text: &str) -> Option<i32> {
    if !text.chars().all(|c| c.is_ascii_digit()) {
        return None;
    }
    let year: i32 = text.parse().ok()?;
    match text.len() {
        2 => Some(2000 + year),
        4 if (1970..=2100).contains(&year) => Some(year),
        _ => None,
    }
}

fn most_recent(month: u32, day: u32, today: NaiveDate) -> Option<NaiveDate> {
    (today.year() - 8..=today.year())
        .rev()
        .filter_map(|year| NaiveDate::from_ymd_opt(year, month, day))
        .find(|date| *date <= today)
}

#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub(crate) struct Period {
    pub(crate) start: NaiveDate,
    pub(crate) end: NaiveDate,
}

impl Period {
    fn day(start: NaiveDate) -> Option<Self> {
        Some(Self {
            start,
            end: start.succ_opt()?,
        })
    }

    fn month(start: NaiveDate) -> Option<Self> {
        let (year, month) = if start.month() == 12 {
            (start.year() + 1, 1)
        } else {
            (start.year(), start.month() + 1)
        };
        Some(Self {
            start,
            end: NaiveDate::from_ymd_opt(year, month, 1)?,
        })
    }

    fn year(year: i32) -> Option<Self> {
        Some(Self {
            start: NaiveDate::from_ymd_opt(year, 1, 1)?,
            end: NaiveDate::from_ymd_opt(year + 1, 1, 1)?,
        })
    }
}

pub(crate) fn parse_period(raw: &str, today: NaiveDate) -> Option<Period> {
    let value = fold(raw.trim()).replace(['’', '`'], "'");
    match value.as_str() {
        "aujourd'hui" | "aujourdhui" | "auj" => return Period::day(today),
        "hier" => return Period::day(today.pred_opt()?),
        _ => {}
    }
    if value.len() == 4 && value.chars().all(|c| c.is_ascii_digit()) {
        return Period::year(parse_year(&value)?);
    }
    if value.contains('-') {
        let parts: Vec<&str> = value.split('-').collect();
        if let [year, month, day] = parts.as_slice() {
            if year.len() != 4 {
                return None;
            }
            return Period::day(NaiveDate::from_ymd_opt(
                parse_year(year)?,
                month.parse().ok()?,
                day.parse().ok()?,
            )?);
        }
        return None;
    }
    if value.contains('/') {
        let parts: Vec<&str> = value.split('/').collect();
        let date = match parts.as_slice() {
            [day, month] => most_recent(month.parse().ok()?, day.parse().ok()?, today),
            [day, month, year] => NaiveDate::from_ymd_opt(
                parse_year(year)?,
                month.parse().ok()?,
                day.parse().ok()?,
            ),
            _ => None,
        };
        return Period::day(date?);
    }
    let words: Vec<&str> = value.split_whitespace().collect();
    let start = match words.as_slice() {
        [month] => most_recent(month_number(month)?, 1, today),
        [month, year] => NaiveDate::from_ymd_opt(parse_year(year)?, month_number(month)?, 1),
        _ => None,
    };
    Period::month(start?)
}

pub(crate) fn parse_query(input: &str, today: NaiveDate) -> SearchQuery {
    let tokens = tokenize(input);
    let mut query = SearchQuery::default();
    let mut text: Vec<String> = Vec::new();
    let mut index = 0;
    while index < tokens.len() {
        let token = &tokens[index];
        index += 1;
        let Some((key, value)) = token.split_once(':') else {
            text.push(token.clone());
            continue;
        };
        let key = fold(key);
        if !matches!(key.as_str(), "de" | "dans" | "a" | "avant" | "apres") {
            text.push(token.clone());
            continue;
        }
        let value = value.trim().to_string();
        if value.is_empty() {
            continue;
        }
        match key.as_str() {
            "de" => query.from = Some(value),
            "dans" => query.within = Some(value),
            "a" => match SearchKind::parse(&value) {
                Some(kind) if !query.kinds.contains(&kind) => query.kinds.push(kind),
                Some(_) => {}
                None => query.invalid_kinds.push(value),
            },
            _ => {
                let mut raw = value;
                if month_number(&raw).is_some()
                    && tokens.get(index).is_some_and(|next| {
                        next.len() == 4 && next.chars().all(|c| c.is_ascii_digit())
                    })
                {
                    raw = format!("{raw} {}", tokens[index]);
                    index += 1;
                }
                let bound = Some(DateBound::new(raw, today));
                if key == "avant" {
                    query.before = bound;
                } else {
                    query.after = bound;
                }
            }
        }
    }
    query.text = text.join(" ");
    query
}

pub(crate) fn day_start_ms(date: NaiveDate) -> Option<i64> {
    let start = date.and_hms_opt(0, 0, 0)?;
    Local
        .from_local_datetime(&start)
        .earliest()
        .map(|time| time.timestamp_millis())
}

fn match_score(candidate: &str, wanted: &str) -> Option<u8> {
    let candidate = fold(candidate);
    if candidate.is_empty() {
        return None;
    }
    if candidate == wanted {
        Some(0)
    } else if candidate.starts_with(wanted) {
        Some(1)
    } else if candidate
        .split(|c: char| !c.is_alphanumeric())
        .any(|word| word.starts_with(wanted))
    {
        Some(2)
    } else if candidate.contains(wanted) {
        Some(3)
    } else {
        None
    }
}

pub(crate) fn best_match<'a, T>(
    candidates: impl IntoIterator<Item = (&'a str, T)>,
    wanted: &str,
) -> Option<T> {
    let wanted = fold(wanted.trim());
    if wanted.is_empty() {
        return None;
    }
    let mut best: Option<(u8, T)> = None;
    for (name, value) in candidates {
        if let Some(score) = match_score(name, &wanted) {
            if best.as_ref().is_none_or(|(current, _)| score < *current) {
                best = Some((score, value));
            }
        }
    }
    best.map(|(_, value)| value)
}

pub(crate) fn highlight_ranges(text: &str, terms: &[String]) -> Vec<Range<usize>> {
    let chars: Vec<(usize, char)> = text
        .char_indices()
        .map(|(offset, c)| (offset, fold(&c.to_string()).chars().next().unwrap_or(c)))
        .collect();
    let mut ranges: Vec<Range<usize>> = Vec::new();
    for term in terms {
        let needle: Vec<char> = term.chars().collect();
        if needle.is_empty() || needle.len() > chars.len() {
            continue;
        }
        let mut start = 0;
        while start + needle.len() <= chars.len() {
            let hit = chars[start..start + needle.len()]
                .iter()
                .zip(&needle)
                .all(|((_, a), b)| a == b);
            if hit {
                let end_char = start + needle.len();
                let end = chars.get(end_char).map(|(o, _)| *o).unwrap_or(text.len());
                ranges.push(chars[start].0..end);
                start = end_char;
            } else {
                start += 1;
            }
        }
    }
    ranges.sort_by_key(|range| range.start);
    let mut merged: Vec<Range<usize>> = Vec::new();
    for range in ranges {
        match merged.last_mut() {
            Some(last) if range.start <= last.end => last.end = last.end.max(range.end),
            _ => merged.push(range),
        }
    }
    merged
}

pub(crate) fn excerpt(text: &str, ranges: &[Range<usize>]) -> (String, Vec<Range<usize>>) {
    let Some(first) = ranges.first() else {
        return (text.to_string(), Vec::new());
    };
    let lead = text[..first.start].chars().count();
    if lead <= 48 {
        return (text.to_string(), ranges.to_vec());
    }
    let cut = text[..first.start]
        .char_indices()
        .nth(lead - 32)
        .map(|(offset, _)| offset)
        .unwrap_or(0);
    let prefix = "…";
    let shown = format!("{prefix}{}", &text[cut..]);
    let shift = prefix.len() as isize - cut as isize;
    let moved = ranges
        .iter()
        .map(|range| {
            ((range.start as isize + shift) as usize)..((range.end as isize + shift) as usize)
        })
        .collect();
    (shown, moved)
}

pub(crate) fn fold(text: &str) -> String {
    text.chars()
        .flat_map(char::to_lowercase)
        .map(|c| match c {
            'à' | 'â' | 'ä' | 'á' | 'ã' => 'a',
            'é' | 'è' | 'ê' | 'ë' => 'e',
            'î' | 'ï' | 'í' => 'i',
            'ô' | 'ö' | 'ó' | 'õ' => 'o',
            'ù' | 'û' | 'ü' | 'ú' => 'u',
            'ç' => 'c',
            'ñ' => 'n',
            other => other,
        })
        .collect()
}

#[cfg(test)]
mod tests {
    use chrono::NaiveDate;

    use super::{
        PersonCandidate, SearchKind, best_match, clamp_pan, excerpt, fit_scale, fitted_size, fold,
        highlight_ranges, image_extension, parse_period, parse_query, rank_people,
        sniff_image_format, with_extension, zoom_about, zoom_limits,
    };

    fn day(y: i32, m: u32, d: u32) -> NaiveDate {
        NaiveDate::from_ymd_opt(y, m, d).unwrap()
    }

    fn close(a: f32, b: f32) -> bool {
        (a - b).abs() < 1e-3
    }

    #[::core::prelude::v1::test]
    fn fit_scale_contains_the_image() {
        assert!(close(fit_scale(Some((2000., 1000.)), (1000., 800.)), 0.5));
        assert!(close(fit_scale(Some((100., 400.)), (1000., 800.)), 2.));
        assert!(close(fit_scale(None, (1000., 800.)), 1.));
        assert_eq!(fitted_size(Some((2000., 1000.)), (1000., 800.)), (1000., 500.));
        assert_eq!(fitted_size(None, (1000., 800.)), (1000., 800.));
    }

    #[::core::prelude::v1::test]
    fn zoom_limits_span_fit_to_four_hundred_percent() {
        let (min, max) = zoom_limits(0.5);
        assert!(close(min, 1.) && close(max, 8.));
        let (min, max) = zoom_limits(2.);
        assert!(close(min, 0.5) && close(max, 2.));
        let (min, max) = zoom_limits(10.);
        assert!(close(min, 0.1) && close(max, 1.));
    }

    #[::core::prelude::v1::test]
    fn zoom_keeps_the_anchor_still() {
        let (zoom, pan) = zoom_about(1., (0., 0.), 2., (100., -50.), (1., 8.));
        assert!(close(zoom, 2.));
        assert!(close(pan.0, -100.) && close(pan.1, 50.));
        let (zoom, pan) = zoom_about(2., pan, 0.5, (100., -50.), (1., 8.));
        assert!(close(zoom, 1.) && close(pan.0, 0.) && close(pan.1, 0.));
        let (zoom, _) = zoom_about(7., (0., 0.), 4., (0., 0.), (1., 8.));
        assert!(close(zoom, 8.));
        let (zoom, _) = zoom_about(1., (0., 0.), 0.2, (0., 0.), (1., 8.));
        assert!(close(zoom, 1.));
    }

    #[::core::prelude::v1::test]
    fn pan_stays_inside_the_zoomed_image() {
        assert_eq!(clamp_pan((500., -500.), (2000., 1000.), (1000., 800.)), (500., -100.));
        assert_eq!(clamp_pan((30., 30.), (800., 600.), (1000., 800.)), (0., 0.));
    }

    #[::core::prelude::v1::test]
    fn image_formats_are_sniffed_from_bytes_then_extension() {
        use gpui_kit::ImageFormat;
        let path = std::path::Path::new("media/abc");
        assert_eq!(sniff_image_format(&[0x89, b'P', b'N', b'G', 0], path), Some(ImageFormat::Png));
        assert_eq!(sniff_image_format(&[0xFF, 0xD8, 0xFF, 0xE0], path), Some(ImageFormat::Jpeg));
        assert_eq!(sniff_image_format(b"RIFF\0\0\0\0WEBPVP8 ", path), Some(ImageFormat::Webp));
        assert_eq!(sniff_image_format(b"????", std::path::Path::new("a.GIF")), Some(ImageFormat::Gif));
        assert_eq!(sniff_image_format(b"????", path), None);
        assert_eq!(image_extension(ImageFormat::Jpeg), "jpg");
        assert_eq!(
            with_extension("C:/x/photo".into(), "png"),
            std::path::PathBuf::from("C:/x/photo.png")
        );
        assert_eq!(
            with_extension("C:/x/photo.jpeg".into(), "png"),
            std::path::PathBuf::from("C:/x/photo.jpeg")
        );
    }

    #[::core::prelude::v1::test]
    fn fold_removes_case_and_accents() {
        assert_eq!(fold("Élodie Ça Va"), "elodie ca va");
    }

    #[::core::prelude::v1::test]
    fn plain_text_has_no_filters() {
        let query = parse_query("  bonjour   toi ", day(2026, 11, 17));
        assert_eq!(query.text, "bonjour toi");
        assert!(!query.has_filters());
        assert!(query.is_active());
        assert!(!parse_query("a", day(2026, 11, 17)).is_active());
    }

    #[::core::prelude::v1::test]
    fn operators_are_extracted_with_quotes_and_accents() {
        let query = parse_query(
            "resto de:\"Jean Dupont\" DANS:Coloc à:photos a:vidéo après:hier",
            day(2026, 11, 17),
        );
        assert_eq!(query.text, "resto");
        assert_eq!(query.from.as_deref(), Some("Jean Dupont"));
        assert_eq!(query.within.as_deref(), Some("Coloc"));
        assert_eq!(query.kinds, vec![SearchKind::Photo, SearchKind::Video]);
        assert_eq!(query.after.unwrap().after_date(), Some(day(2026, 11, 17)));
        assert!(query.before.is_none());
    }

    #[::core::prelude::v1::test]
    fn empty_or_unknown_operators_are_handled() {
        let query = parse_query("de: http://exemple.fr a:gif", day(2026, 11, 17));
        assert_eq!(query.text, "http://exemple.fr");
        assert!(query.from.is_none());
        assert_eq!(query.invalid_kinds, vec!["gif".to_string()]);
        assert!(query.has_filters());
    }

    #[::core::prelude::v1::test]
    fn month_and_year_are_joined() {
        let query = parse_query("avant:mars 2024 apres:2023", day(2026, 11, 17));
        let before = query.before.unwrap();
        assert_eq!(before.raw, "mars 2024");
        assert_eq!(before.before_date(), Some(day(2024, 3, 1)));
        assert_eq!(query.after.unwrap().after_date(), Some(day(2024, 1, 1)));
        assert!(query.text.is_empty());
    }

    fn start(raw: &str, today: NaiveDate) -> Option<NaiveDate> {
        parse_period(raw, today).map(|period| period.start)
    }

    #[::core::prelude::v1::test]
    fn french_dates_are_parsed() {
        let today = day(2026, 11, 17);
        assert_eq!(start("12/03/2024", today), Some(day(2024, 3, 12)));
        assert_eq!(start("12/03/24", today), Some(day(2024, 3, 12)));
        assert_eq!(start("2025-01-31", today), Some(day(2025, 1, 31)));
        assert_eq!(start("05/11", today), Some(day(2026, 11, 5)));
        assert_eq!(start("25/12", today), Some(day(2025, 12, 25)));
        assert_eq!(start("Aujourd’hui", today), Some(today));
        assert_eq!(start("aujourd'hui", today), Some(today));
        assert_eq!(start("hier", today), Some(day(2026, 11, 16)));
        assert_eq!(start("février", today), Some(day(2026, 2, 1)));
        assert_eq!(start("décembre", today), Some(day(2025, 12, 1)));
        assert_eq!(start("Août 2021", today), Some(day(2021, 8, 1)));
        assert_eq!(start("2022", today), Some(day(2022, 1, 1)));
        assert_eq!(start("31/02/2024", today), None);
        assert_eq!(start("31/04", today), None);
        assert_eq!(start("demain", today), None);
    }

    #[::core::prelude::v1::test]
    fn leap_day_without_year_is_the_most_recent_one() {
        assert_eq!(start("29/02", day(2026, 11, 17)), Some(day(2024, 2, 29)));
        assert_eq!(start("29/02", day(2028, 3, 1)), Some(day(2028, 2, 29)));
        assert_eq!(start("29/02", day(2028, 2, 28)), Some(day(2024, 2, 29)));
    }

    fn bound(input: &str, today: NaiveDate) -> (Option<NaiveDate>, Option<NaiveDate>) {
        let query = parse_query(input, today);
        (
            query.after.and_then(|bound| bound.after_date()),
            query.before.and_then(|bound| bound.before_date()),
        )
    }

    #[::core::prelude::v1::test]
    fn apres_starts_after_the_end_of_the_period() {
        let today = day(2026, 11, 17);
        assert_eq!(bound("après:hier", today).0, Some(today));
        assert_eq!(bound("après:aujourd'hui", today).0, Some(day(2026, 11, 18)));
        assert_eq!(bound("après:2023", today).0, Some(day(2024, 1, 1)));
        assert_eq!(bound("après:mars 2025", today).0, Some(day(2025, 4, 1)));
        assert_eq!(bound("après:décembre 2025", today).0, Some(day(2026, 1, 1)));
        assert_eq!(bound("après:15/03/2025", today).0, Some(day(2025, 3, 16)));
        assert_eq!(bound("apres:2025-12-31", today).0, Some(day(2026, 1, 1)));
    }

    #[::core::prelude::v1::test]
    fn avant_ends_before_the_start_of_the_period() {
        let today = day(2026, 11, 17);
        assert_eq!(bound("avant:hier", today).1, Some(day(2026, 11, 16)));
        assert_eq!(bound("avant:2023", today).1, Some(day(2023, 1, 1)));
        assert_eq!(bound("avant:mars 2025", today).1, Some(day(2025, 3, 1)));
        assert_eq!(bound("avant:15/03/2025", today).1, Some(day(2025, 3, 15)));
    }

    fn person(id: &str, name: &str, in_scope: bool) -> PersonCandidate {
        PersonCandidate {
            id: id.into(),
            name: name.into(),
            in_scope,
        }
    }

    #[::core::prelude::v1::test]
    fn people_of_the_scoped_conversation_win() {
        let people = vec![
            person("1", "Marie", false),
            person("2", "Marie-Claire Dubois", true),
            person("3", "Marion", false),
        ];
        let ranked = rank_people(people.clone(), "marie");
        assert_eq!(ranked[0].id, "2");
        assert_eq!(ranked[1].id, "1");
        assert_eq!(ranked.len(), 2);
        let unscoped: Vec<_> = people
            .into_iter()
            .map(|mut p| {
                p.in_scope = false;
                p
            })
            .collect();
        assert_eq!(rank_people(unscoped, "marie")[0].id, "1");
    }

    #[::core::prelude::v1::test]
    fn query_round_trips_through_input_text() {
        let today = day(2026, 11, 17);
        let query = parse_query("menu de:\"Jean Dupont\" a:lien avant:\"mars 2024\"", today);
        let again = parse_query(&query.to_input(), today);
        assert_eq!(again, query);
    }

    #[::core::prelude::v1::test]
    fn best_match_prefers_exact_then_prefix() {
        let names = [("Jeanne Martin", 1), ("Jean", 2), ("Marc Jeanson", 3)];
        assert_eq!(best_match(names, "jean"), Some(2));
        assert_eq!(best_match(names, "mart"), Some(1));
        assert_eq!(best_match(names, "élodie"), None);
    }

    #[::core::prelude::v1::test]
    fn highlights_ignore_case_and_accents() {
        let text = "On se voit à l'Église demain ? église";
        let ranges = highlight_ranges(text, &["eglise".to_string()]);
        let found: Vec<&str> = ranges.iter().map(|r| &text[r.clone()]).collect();
        assert_eq!(found, vec!["Église", "église"]);
    }

    #[::core::prelude::v1::test]
    fn excerpt_keeps_the_match_visible() {
        let text = format!("{} cible", "mot ".repeat(30));
        let ranges = highlight_ranges(&text, &["cible".to_string()]);
        let (shown, moved) = excerpt(&text, &ranges);
        assert!(shown.starts_with('…'));
        assert_eq!(&shown[moved[0].clone()], "cible");
    }
}
