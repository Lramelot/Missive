use std::{
    ops::Range,
    time::{Duration, Instant},
};

use gpui_kit::component::input::{InputEvent, InputState};
use gpui_kit::*;
use serde_json::json;

use super::{
    MissiveApp,
    compose::{EmojiPicker, OfflineQueue},
    library::{OLDER_PAGE, Viewer, fold, highlight_ranges},
};
use crate::{model::Message, settings::TextSize};

const FIND_PAGES: usize = 10;
const FIND_WAIT: Duration = Duration::from_millis(400);
const FIND_FLASH: Duration = Duration::from_millis(1200);
const TEXT_SIZES: [TextSize; 4] = [
    TextSize::Small,
    TextSize::Normal,
    TextSize::Large,
    TextSize::Larger,
];

#[derive(Default)]
pub(crate) struct Find {
    pub(crate) bar: Option<FindBar>,
    pub(crate) emoji: Option<EmojiPicker>,
    pub(crate) emoji_closed_at: Option<Instant>,
    pub(crate) viewer: Viewer,
    pub(crate) offline: OfflineQueue,
}

pub(crate) struct FindBar {
    pub(crate) input: Entity<InputState>,
    pub(crate) query: String,
    pub(crate) current: Option<String>,
    pub(crate) searching: bool,
    pub(crate) exhausted: bool,
    _subscription: Subscription,
}

pub(crate) struct FindStatus {
    pub(crate) position: Option<usize>,
    pub(crate) total: usize,
}

pub(crate) struct FindMark<'a> {
    pub(crate) query: &'a str,
    pub(crate) current: bool,
}

enum FurtherStep {
    Done,
    Stop,
    Wait,
    Fetch(i64, u64),
}

impl Find {
    pub(crate) fn on_thread_changed(&mut self) {
        self.bar = None;
        self.emoji = None;
    }
}

pub(crate) fn normalize_query(raw: &str) -> String {
    fold(raw.trim())
}

pub(crate) fn message_matches(message: &Message, query: &str) -> bool {
    if query.is_empty() || message.system {
        return false;
    }
    fold(&message.text).contains(query)
        || message.attachments.iter().any(|attachment| {
            [attachment.title.as_deref(), attachment.file_name.as_deref()]
                .into_iter()
                .flatten()
                .any(|value| fold(value).contains(query))
        })
}

pub(crate) fn match_indices(messages: &[Message], query: &str) -> Vec<usize> {
    messages
        .iter()
        .enumerate()
        .filter(|(_, message)| message_matches(message, query))
        .map(|(index, _)| index)
        .collect()
}

pub(crate) fn step_match(count: usize, current: Option<usize>, delta: isize) -> Option<usize> {
    if count == 0 {
        return None;
    }
    match current {
        None => Some(count - 1),
        Some(current) => Some((current as isize + delta).rem_euclid(count as isize) as usize),
    }
}

pub(crate) fn stepped_text_size(current: TextSize, step: i32) -> TextSize {
    if step == 0 {
        return TextSize::Normal;
    }
    let index = TEXT_SIZES
        .iter()
        .position(|size| *size == current)
        .unwrap_or(1) as i32;
    TEXT_SIZES[(index + step.signum()).clamp(0, TEXT_SIZES.len() as i32 - 1) as usize]
}

fn text_size_notice(size: TextSize) -> SharedString {
    match size {
        TextSize::Small => ts!("find.text_size.small"),
        TextSize::Normal => ts!("find.text_size.normal"),
        TextSize::Large => ts!("find.text_size.large"),
        TextSize::Larger => ts!("find.text_size.larger"),
    }
}

impl MissiveApp {
    pub(crate) fn toggle_find(&mut self, window: &mut Window, cx: &mut Context<Self>) {
        self.close_emoji_picker(cx);
        if let Some(bar) = &self.find.bar {
            let input = bar.input.clone();
            input.update(cx, |input, cx| {
                input.focus(window, cx);
                input.select_all(window, cx);
            });
            return;
        }
        if self.selected.is_none() {
            return;
        }
        let input = cx.new(|cx| InputState::new(window, cx).placeholder(t!("find.placeholder")));
        let subscription =
            cx.subscribe_in(
                &input,
                window,
                |this, _, event: &InputEvent, _, cx| match event {
                    InputEvent::Change => this.on_find_changed(cx),
                    InputEvent::PressEnter { shift, .. } => {
                        this.step_find(if *shift { 1 } else { -1 }, cx)
                    }
                    _ => {}
                },
            );
        input.update(cx, |input, cx| input.focus(window, cx));
        self.find.bar = Some(FindBar {
            input,
            query: String::new(),
            current: None,
            searching: false,
            exhausted: false,
            _subscription: subscription,
        });
        cx.notify();
    }

    pub(crate) fn close_find(&mut self, cx: &mut Context<Self>) -> bool {
        if self.close_emoji_picker(cx) {
            self.refocus_composer(cx);
            return true;
        }
        let Some(bar) = self.find.bar.take() else {
            return false;
        };
        self.remeasure_find_rows(&bar.query, cx);
        self.refocus_composer(cx);
        cx.notify();
        true
    }

    pub(crate) fn refocus_composer(&self, cx: &mut Context<Self>) {
        let composer = self.composer.clone();
        cx.spawn(async move |_, cx| {
            let Some(window) = cx.update(|cx| cx.active_window()) else {
                return;
            };
            let _ = window.update(cx, |_, window, cx| {
                composer.update(cx, |input, cx| input.focus(window, cx));
            });
        })
        .detach();
    }

    fn selected_messages(&self) -> &[Message] {
        self.selected
            .as_ref()
            .and_then(|thread_id| self.messages.get(thread_id))
            .map(Vec::as_slice)
            .unwrap_or(&[])
    }

    pub(crate) fn find_matches(&self) -> Vec<usize> {
        match &self.find.bar {
            Some(bar) => match_indices(self.selected_messages(), &bar.query),
            None => Vec::new(),
        }
    }

    pub(crate) fn find_status(&self) -> Option<FindStatus> {
        let bar = self.find.bar.as_ref()?;
        let matches = self.find_matches();
        let messages = self.selected_messages();
        let current = bar.current.as_ref().and_then(|id| {
            matches
                .iter()
                .position(|index| messages.get(*index).is_some_and(|m| &m.id == id))
        });
        Some(FindStatus {
            position: current.map(|current| matches.len() - current),
            total: matches.len(),
        })
    }

    pub(crate) fn find_mark(&self, message: &Message) -> Option<FindMark<'_>> {
        let bar = self.find.bar.as_ref()?;
        if self.selected.as_deref() != Some(message.thread_id.as_str())
            || !message_matches(message, &bar.query)
        {
            return None;
        }
        Some(FindMark {
            query: &bar.query,
            current: bar.current.as_deref() == Some(message.id.as_str()),
        })
    }

    pub(crate) fn find_ranges(text: &str, query: &str) -> Vec<Range<usize>> {
        if query.is_empty() {
            return Vec::new();
        }
        highlight_ranges(text, &[query.to_string()])
    }

    fn remeasure_find_rows(&mut self, query: &str, cx: &mut Context<Self>) {
        let rows = match_indices(self.selected_messages(), query);
        if rows.is_empty() {
            return;
        }
        self.scroller.update(cx, |scroller, cx| {
            for index in rows {
                scroller.remeasure_items(index..index + 1, cx);
            }
        });
    }

    fn on_find_changed(&mut self, cx: &mut Context<Self>) {
        let Some(bar) = self.find.bar.as_mut() else {
            return;
        };
        let query = normalize_query(&bar.input.read(cx).value());
        if query == bar.query {
            return;
        }
        let previous = std::mem::replace(&mut bar.query, query.clone());
        bar.current = None;
        bar.searching = false;
        bar.exhausted = false;
        self.remeasure_find_rows(&previous, cx);
        self.remeasure_find_rows(&query, cx);
        if let Some(&newest) = self.find_matches().last() {
            self.focus_find_match(newest, cx);
        }
        cx.notify();
    }

    pub(crate) fn step_find(&mut self, delta: isize, cx: &mut Context<Self>) {
        let matches = self.find_matches();
        let Some(bar) = &self.find.bar else {
            return;
        };
        let messages = self.selected_messages();
        let current = bar.current.as_ref().and_then(|id| {
            matches
                .iter()
                .position(|index| messages.get(*index).is_some_and(|m| &m.id == id))
        });
        if let Some(next) = step_match(matches.len(), current, delta) {
            self.focus_find_match(matches[next], cx);
        }
        cx.notify();
    }

    fn focus_find_match(&mut self, index: usize, cx: &mut Context<Self>) {
        let Some(message_id) = self.selected_messages().get(index).map(|m| m.id.clone()) else {
            return;
        };
        if let Some(bar) = self.find.bar.as_mut() {
            bar.current = Some(message_id.clone());
        }
        self.scroller.update(cx, |scroller, cx| {
            scroller.scroll_to_item(index, cx);
            scroller.remeasure_items(index..index + 1, cx);
        });
        self.message_ops.flash = Some((message_id.clone(), Instant::now()));
        cx.spawn(async move |this, cx| {
            cx.background_executor().timer(FIND_FLASH).await;
            let _ = this.update(cx, |this, cx| {
                if this
                    .message_ops
                    .flash
                    .as_ref()
                    .is_some_and(|(id, _)| *id == message_id)
                {
                    this.message_ops.flash = None;
                    cx.notify();
                }
            });
        })
        .detach();
    }

    fn find_bar_is(&self, thread_id: &str, query: &str) -> bool {
        self.selected.as_deref() == Some(thread_id)
            && self.find.bar.as_ref().is_some_and(|bar| bar.query == query)
    }

    pub(crate) fn find_further(&mut self, cx: &mut Context<Self>) {
        let Some(thread_id) = self.selected.clone() else {
            return;
        };
        let Some(bridge) = self.bridge() else { return };
        let Some(bar) = self.find.bar.as_mut() else {
            return;
        };
        if bar.searching || bar.query.is_empty() {
            return;
        }
        bar.searching = true;
        bar.exhausted = false;
        let query = bar.query.clone();
        cx.notify();
        cx.spawn(async move |this, cx| {
            let mut pages = 0;
            let mut rounds = 0;
            while pages < FIND_PAGES && rounds < FIND_PAGES * 4 {
                rounds += 1;
                let step = this
                    .update(cx, |this, _| this.further_step(&thread_id, &query))
                    .unwrap_or(FurtherStep::Stop);
                let (oldest, generation) = match step {
                    FurtherStep::Done | FurtherStep::Stop => break,
                    FurtherStep::Wait => {
                        cx.background_executor().timer(FIND_WAIT).await;
                        continue;
                    }
                    FurtherStep::Fetch(oldest, generation) => (oldest, generation),
                };
                let result = bridge
                    .call::<Vec<Message>>(
                        "messages",
                        json!({ "thread_id": thread_id, "before_ms": super::library::older_page_before(oldest), "limit": OLDER_PAGE }),
                    )
                    .await;
                let merged = this
                    .update(cx, |this, cx| {
                        this.merge_older_page(&thread_id, oldest, generation, result, cx)
                    })
                    .ok()
                    .flatten();
                match merged {
                    Some(count) if count > 0 => pages += 1,
                    _ => break,
                }
            }
            let _ = this.update(cx, |this, cx| {
                this.finish_find_further(&thread_id, &query, cx)
            });
        })
        .detach();
    }

    fn further_step(&mut self, thread_id: &str, query: &str) -> FurtherStep {
        if !self.find_bar_is(thread_id, query) {
            return FurtherStep::Stop;
        }
        if !self.find_matches().is_empty() {
            return FurtherStep::Done;
        }
        if self.older.get(thread_id).is_some_and(|state| state.loading) {
            return FurtherStep::Wait;
        }
        match self.begin_older_page(thread_id) {
            Some((oldest, generation)) => FurtherStep::Fetch(oldest, generation),
            None => FurtherStep::Stop,
        }
    }

    fn finish_find_further(&mut self, thread_id: &str, query: &str, cx: &mut Context<Self>) {
        if !self.find_bar_is(thread_id, query) {
            return;
        }
        let newest = self.find_matches().last().copied();
        if let Some(bar) = self.find.bar.as_mut() {
            bar.searching = false;
            bar.exhausted = newest.is_none();
        }
        if let Some(index) = newest {
            self.remeasure_find_rows(query, cx);
            self.focus_find_match(index, cx);
        }
        cx.notify();
    }

    pub(crate) fn on_appearance_changed(&mut self, cx: &mut Context<Self>) {
        self.scroller
            .update(cx, |scroller, cx| scroller.remeasure(cx));
        cx.notify();
    }

    pub(crate) fn step_text_size(&mut self, step: i32, cx: &mut Context<Self>) {
        let next = stepped_text_size(self.settings.text_size, step);
        if next != self.settings.text_size {
            self.settings.text_size = next;
            self.mark_settings_dirty();
            self.on_appearance_changed(cx);
        }
        self.show_toast(text_size_notice(next), cx);
    }
}

#[cfg(test)]
mod tests {
    use super::{match_indices, message_matches, normalize_query, step_match, stepped_text_size};
    use crate::{
        app::MissiveApp,
        model::{Attachment, Message},
        settings::TextSize,
    };

    fn message(id: &str, text: &str) -> Message {
        Message {
            id: id.into(),
            text: text.into(),
            ..Message::default()
        }
    }

    #[::core::prelude::v1::test]
    fn matching_ignores_case_and_accents() {
        let query = normalize_query("  Élève ");
        assert_eq!(query, "eleve");
        assert!(message_matches(&message("a", "Un ELEVE studieux"), &query));
        assert!(message_matches(&message("b", "l'élève"), &query));
        assert!(!message_matches(&message("c", "rien"), &query));
        assert!(!message_matches(&message("d", "élève"), ""));
    }

    #[::core::prelude::v1::test]
    fn matching_covers_link_titles_and_file_names_but_not_system_lines() {
        let mut link = message("a", "");
        link.attachments.push(Attachment {
            kind: "link".into(),
            title: Some("Recette de crêpes".into()),
            ..Attachment::default()
        });
        let mut file = message("b", "");
        file.attachments.push(Attachment {
            kind: "file".into(),
            file_name: Some("Facture-Crêpes.pdf".into()),
            ..Attachment::default()
        });
        let system = Message {
            system: true,
            ..message("c", "crêpes")
        };
        let deleted = Message {
            deleted: true,
            ..message("d", "crêpes")
        };
        let list = vec![link, file, system, deleted, message("e", "CREPES !")];
        assert_eq!(match_indices(&list, &normalize_query("crepes")), [0, 1, 3, 4]);
    }

    #[::core::prelude::v1::test]
    fn highlight_ranges_point_at_the_original_text() {
        let text = "Café puis CAFÉ";
        let ranges = MissiveApp::find_ranges(text, &normalize_query("cafe"));
        assert_eq!(ranges.len(), 2);
        assert_eq!(&text[ranges[0].clone()], "Café");
        assert_eq!(&text[ranges[1].clone()], "CAFÉ");
        assert!(MissiveApp::find_ranges(text, "").is_empty());
    }

    #[::core::prelude::v1::test]
    fn navigation_starts_at_newest_and_wraps() {
        assert_eq!(step_match(0, None, -1), None);
        assert_eq!(step_match(3, None, -1), Some(2));
        assert_eq!(step_match(3, Some(2), -1), Some(1));
        assert_eq!(step_match(3, Some(0), -1), Some(2));
        assert_eq!(step_match(3, Some(2), 1), Some(0));
    }

    #[::core::prelude::v1::test]
    fn text_size_steps_are_clamped_and_reset() {
        assert_eq!(stepped_text_size(TextSize::Normal, 1), TextSize::Large);
        assert_eq!(stepped_text_size(TextSize::Larger, 1), TextSize::Larger);
        assert_eq!(stepped_text_size(TextSize::Small, -1), TextSize::Small);
        assert_eq!(stepped_text_size(TextSize::Large, -3), TextSize::Normal);
        assert_eq!(stepped_text_size(TextSize::Larger, 0), TextSize::Normal);
    }
}
