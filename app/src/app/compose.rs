use std::{
    collections::{HashMap, HashSet},
    path::PathBuf,
    sync::OnceLock,
    time::{Duration, Instant},
};

use gpui_kit::assets::IconName;
use gpui_kit::component::input::{InputEvent, InputState};
use gpui_kit::*;
use serde_json::json;

use super::{Link, Media, MissiveApp, Outgoing, OutgoingRequest, library::fold};
use crate::model::{Attachment, Message, Reaction, Reactor, ReplyPreview, Thread};

const DEFAULT_LIKE: &str = "👍";
const TYPING_REFRESH: Duration = Duration::from_secs(4);
const MAX_PENDING: usize = 10;
const OFFLINE_EXPIRY: Duration = Duration::from_secs(30 * 60);
const OFFLINE_TICK: Duration = Duration::from_secs(10);
fn offline_expired() -> SharedString {
    ts!("compose.offline.expired")
}

fn offline_cancelled() -> SharedString {
    ts!("compose.offline.cancelled")
}
const MAX_RECENT_EMOJIS: usize = 32;
const EMOJI_RESULTS: usize = 160;
const EMOJI_REOPEN_GUARD: Duration = Duration::from_millis(250);

#[derive(Clone, Debug, PartialEq, Eq)]
pub(crate) struct Queued {
    pub(crate) client_id: String,
    pub(crate) thread_id: String,
    pub(crate) seq: u64,
    pub(crate) at: Instant,
}

#[derive(Default)]
pub(crate) struct OfflineQueue {
    waiting: Vec<Queued>,
    in_flight: HashMap<String, Queued>,
    created: HashMap<String, (u64, Instant)>,
    next_seq: u64,
    ticking: bool,
}

impl OfflineQueue {
    pub(crate) fn register(&mut self, client_id: &str, at: Instant) -> (u64, Instant) {
        self.next_seq += 1;
        let origin = (self.next_seq, at);
        self.created.insert(client_id.to_string(), origin);
        origin
    }

    pub(crate) fn origin(&mut self, client_id: &str, now: Instant) -> (u64, Instant) {
        match self.created.get(client_id) {
            Some(origin) => *origin,
            None => self.register(client_id, now),
        }
    }

    pub(crate) fn forget(&mut self, client_id: &str) {
        self.created.remove(client_id);
    }

    pub(crate) fn is_queued(&self, client_id: &str) -> bool {
        self.is_waiting(client_id)
            || self
                .in_flight
                .values()
                .any(|item| item.client_id == client_id)
    }

    pub(crate) fn is_waiting(&self, client_id: &str) -> bool {
        self.waiting.iter().any(|item| item.client_id == client_id)
    }

    pub(crate) fn has_thread(&self, thread_id: &str) -> bool {
        self.in_flight.contains_key(thread_id)
            || self.waiting.iter().any(|item| item.thread_id == thread_id)
    }

    pub(crate) fn is_empty(&self) -> bool {
        self.waiting.is_empty() && self.in_flight.is_empty()
    }

    pub(crate) fn push(&mut self, item: Queued) {
        self.remove(&item.client_id);
        let position = self.waiting.partition_point(|queued| queued.seq <= item.seq);
        self.waiting.insert(position, item);
    }

    pub(crate) fn remove(&mut self, client_id: &str) -> bool {
        let before = self.waiting.len();
        self.waiting.retain(|item| item.client_id != client_id);
        let flying = self
            .in_flight
            .iter()
            .find(|(_, item)| item.client_id == client_id)
            .map(|(thread, _)| thread.clone());
        if let Some(thread) = &flying {
            self.in_flight.remove(thread);
        }
        before != self.waiting.len() || flying.is_some()
    }

    pub(crate) fn take_expired(&mut self, now: Instant) -> Vec<Queued> {
        let (expired, kept): (Vec<Queued>, Vec<Queued>) = std::mem::take(&mut self.waiting)
            .into_iter()
            .partition(|item| now.saturating_duration_since(item.at) >= OFFLINE_EXPIRY);
        self.waiting = kept;
        expired
    }

    pub(crate) fn next_batch(&mut self) -> Vec<Queued> {
        let mut batch = Vec::new();
        let mut taken: HashSet<String> = HashSet::new();
        let mut index = 0;
        while index < self.waiting.len() {
            let thread = self.waiting[index].thread_id.clone();
            if self.in_flight.contains_key(&thread) || taken.contains(&thread) {
                taken.insert(thread);
                index += 1;
                continue;
            }
            let item = self.waiting.remove(index);
            taken.insert(thread.clone());
            self.in_flight.insert(thread, item.clone());
            batch.push(item);
        }
        batch
    }

    pub(crate) fn finish(&mut self, thread_id: &str, client_id: &str) -> bool {
        if self
            .in_flight
            .get(thread_id)
            .is_some_and(|item| item.client_id == client_id)
        {
            self.in_flight.remove(thread_id);
            return true;
        }
        false
    }
}

#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub(crate) enum EmojiTab {
    Recent,
    Group(emojis::Group),
}

pub(crate) const EMOJI_TABS: [EmojiTab; 10] = [
    EmojiTab::Recent,
    EmojiTab::Group(emojis::Group::SmileysAndEmotion),
    EmojiTab::Group(emojis::Group::PeopleAndBody),
    EmojiTab::Group(emojis::Group::AnimalsAndNature),
    EmojiTab::Group(emojis::Group::FoodAndDrink),
    EmojiTab::Group(emojis::Group::Activities),
    EmojiTab::Group(emojis::Group::TravelAndPlaces),
    EmojiTab::Group(emojis::Group::Objects),
    EmojiTab::Group(emojis::Group::Symbols),
    EmojiTab::Group(emojis::Group::Flags),
];

impl EmojiTab {
    pub(crate) fn icon(self) -> IconName {
        match self {
            EmojiTab::Recent => IconName::Clock,
            EmojiTab::Group(group) => match group {
                emojis::Group::SmileysAndEmotion => IconName::FaceSlightlySmiling,
                emojis::Group::PeopleAndBody => IconName::Users,
                emojis::Group::AnimalsAndNature => IconName::PawPrint,
                emojis::Group::FoodAndDrink => IconName::Utensils,
                emojis::Group::Activities => IconName::Trophy,
                emojis::Group::TravelAndPlaces => IconName::Plane,
                emojis::Group::Objects => IconName::Lightbulb,
                emojis::Group::Symbols => IconName::Heart,
                emojis::Group::Flags => IconName::Flag,
            },
        }
    }

    pub(crate) fn label(self) -> SharedString {
        match self {
            EmojiTab::Recent => ts!("compose.emoji_tab.recent"),
            EmojiTab::Group(group) => match group {
                emojis::Group::SmileysAndEmotion => ts!("compose.emoji_tab.smileys"),
                emojis::Group::PeopleAndBody => ts!("compose.emoji_tab.people"),
                emojis::Group::AnimalsAndNature => ts!("compose.emoji_tab.animals"),
                emojis::Group::FoodAndDrink => ts!("compose.emoji_tab.food"),
                emojis::Group::Activities => ts!("compose.emoji_tab.activities"),
                emojis::Group::TravelAndPlaces => ts!("compose.emoji_tab.travel"),
                emojis::Group::Objects => ts!("compose.emoji_tab.objects"),
                emojis::Group::Symbols => ts!("compose.emoji_tab.symbols"),
                emojis::Group::Flags => ts!("compose.emoji_tab.flags"),
            },
        }
    }
}

pub(crate) struct EmojiPicker {
    pub(crate) search: Entity<InputState>,
    pub(crate) tab: EmojiTab,
    pub(crate) hovered: Option<&'static emojis::Emoji>,
    pub(crate) scroll: ScrollHandle,
    _subscription: Subscription,
}

const FRENCH_KEYWORDS: &[(&str, &str)] = &[
    ("😀", "sourire content heureux"),
    ("😃", "sourire joie"),
    ("😄", "sourire rire"),
    ("😁", "sourire dents"),
    ("😆", "rire mdr"),
    ("😅", "rire gene sueur"),
    ("🤣", "rire mdr ptdr"),
    ("😂", "rire larmes mdr"),
    ("🙂", "sourire"),
    ("😉", "clin oeil"),
    ("😊", "sourire timide"),
    ("😇", "ange"),
    ("🥰", "amour coeurs"),
    ("😍", "amour coeur yeux"),
    ("😘", "bisou"),
    ("😋", "miam"),
    ("😛", "langue"),
    ("😜", "langue clin oeil"),
    ("🤔", "reflechir hmm"),
    ("😐", "neutre"),
    ("😏", "malicieux"),
    ("😌", "soulage"),
    ("😔", "decu"),
    ("😕", "confus"),
    ("😴", "dormir sommeil dodo"),
    ("😎", "cool lunettes"),
    ("😢", "triste pleurer larme"),
    ("😭", "pleurer triste"),
    ("😡", "colere enerve"),
    ("😠", "colere"),
    ("😱", "peur cri"),
    ("😳", "gene rougir"),
    ("🥺", "supplier"),
    ("😬", "grimace oups"),
    ("🙄", "yeux leves"),
    ("🤗", "calin"),
    ("🤩", "etoiles waouh"),
    ("🥳", "fete anniversaire"),
    ("😮", "surpris etonne"),
    ("🤯", "choque"),
    ("🤢", "nausee"),
    ("🤒", "malade"),
    ("😷", "masque malade"),
    ("💩", "caca"),
    ("👀", "yeux regarder"),
    ("👍", "pouce ok oui"),
    ("👎", "pouce non"),
    ("👏", "bravo applaudir"),
    ("🙏", "merci priere svp"),
    ("👋", "salut coucou bonjour main"),
    ("💪", "force muscle"),
    ("🤝", "accord poignee"),
    ("👌", "ok parfait"),
    ("✌️", "paix victoire"),
    ("🤞", "croiser doigts chance"),
    ("🤷", "bof sais pas"),
    ("🤦", "consterne"),
    ("❤️", "coeur amour rouge"),
    ("💔", "coeur brise"),
    ("💕", "coeurs amour"),
    ("💖", "coeur brillant"),
    ("🧡", "coeur orange"),
    ("💛", "coeur jaune"),
    ("💚", "coeur vert"),
    ("💙", "coeur bleu"),
    ("💜", "coeur violet"),
    ("🖤", "coeur noir"),
    ("🤍", "coeur blanc"),
    ("💋", "bisou levres"),
    ("🔥", "feu"),
    ("✨", "etincelles"),
    ("🎉", "fete bravo"),
    ("🎂", "gateau anniversaire"),
    ("🎁", "cadeau"),
    ("☕", "cafe"),
    ("🍕", "pizza"),
    ("🍷", "vin"),
    ("🍺", "biere"),
    ("🍾", "champagne"),
    ("🐶", "chien"),
    ("🐱", "chat"),
    ("🌹", "rose fleur"),
    ("☀️", "soleil"),
    ("🌧️", "pluie"),
    ("❄️", "neige"),
    ("⚽", "foot ballon"),
    ("🚗", "voiture"),
    ("✈️", "avion"),
    ("🏠", "maison"),
    ("💯", "cent"),
    ("✅", "valide oui"),
    ("❌", "croix non"),
    ("⚠️", "attention"),
    ("❓", "question"),
];

fn french_keywords() -> &'static HashMap<&'static str, &'static str> {
    static KEYWORDS: OnceLock<HashMap<&'static str, &'static str>> = OnceLock::new();
    KEYWORDS.get_or_init(|| {
        FRENCH_KEYWORDS
            .iter()
            .filter_map(|(emoji, words)| emojis::get(emoji).map(|found| (found.as_str(), *words)))
            .collect()
    })
}

pub(crate) fn emoji_available(emoji: &emojis::Emoji) -> bool {
    emoji.emoji_version() <= emojis::EmojiVersion::new(15, 0)
}

pub(crate) fn search_emojis(query: &str) -> Vec<&'static emojis::Emoji> {
    let query = fold(query.trim());
    if query.is_empty() {
        return Vec::new();
    }
    let keywords = french_keywords();
    let word_starts = |text: &str| {
        fold(text)
            .split(|c: char| !c.is_alphanumeric())
            .any(|word| word.starts_with(&query))
    };
    let mut ranked: Vec<(u8, usize, &'static emojis::Emoji)> = emojis::iter()
        .filter(|emoji| emoji_available(emoji))
        .enumerate()
        .filter_map(|(order, emoji)| {
            let french = keywords.get(emoji.as_str()).copied().unwrap_or("");
            let score = if word_starts(french) || emoji.shortcodes().any(word_starts) {
                0
            } else if word_starts(emoji.name()) {
                1
            } else if fold(emoji.name()).contains(&query)
                || emoji.shortcodes().any(|code| code.contains(&query))
            {
                2
            } else {
                return None;
            };
            Some((score, order, emoji))
        })
        .collect();
    ranked.sort_by_key(|(score, order, _)| (*score, *order));
    ranked
        .into_iter()
        .take(EMOJI_RESULTS)
        .map(|(_, _, emoji)| emoji)
        .collect()
}

pub(crate) fn push_recent_emoji(recents: &mut Vec<String>, emoji: &str) {
    recents.retain(|existing| existing != emoji);
    recents.insert(0, emoji.to_string());
    recents.truncate(MAX_RECENT_EMOJIS);
}

pub(crate) fn merge_draft(existing: &str, text: &str) -> Option<String> {
    let text = text.trim();
    if text.is_empty() || existing.contains(text) {
        return None;
    }
    if existing.trim().is_empty() {
        Some(text.to_string())
    } else {
        Some(format!("{}\n\n{text}", existing.trim_end()))
    }
}

fn not_connected(err: &anyhow::Error) -> bool {
    err.downcast_ref::<crate::bridge::BridgeError>()
        .is_some_and(|e| e.code == "not_connected")
}

fn send_uncertain(err: &anyhow::Error) -> bool {
    let Some(error) = err.downcast_ref::<crate::bridge::BridgeError>() else {
        return false;
    };
    matches!(error.code.as_str(), "timeout" | "bridge_stopped")
        || error
            .data
            .as_ref()
            .and_then(|data| data.get("uncertain"))
            .and_then(|v| v.as_bool())
            .unwrap_or(false)
}

fn save_rescued_drafts(
    slot: &crate::storage::SaveSlot,
    path: &std::path::Path,
    drafts: &std::collections::HashMap<String, String>,
) -> bool {
    slot.save_path(slot.ticket(), path, drafts)
}

impl MissiveApp {
    pub(crate) fn start_reply(
        &mut self,
        message: Message,
        window: &mut Window,
        cx: &mut Context<Self>,
    ) {
        self.react_picker = None;
        self.message_ops.menu = None;
        if self.selected.as_deref() != Some(message.thread_id.as_str()) || message.system {
            cx.notify();
            return;
        }
        if self.message_ops.editing.is_some() {
            self.cancel_edit(Some(window), cx);
        }
        self.reply_to = Some(message);
        self.composer
            .update(cx, |input, cx| input.focus(window, cx));
        cx.notify();
    }

    pub(crate) fn cancel_reply(&mut self, cx: &mut Context<Self>) {
        self.reply_to = None;
        cx.notify();
    }

    pub(crate) fn toggle_react_picker(&mut self, message_id: String, cx: &mut Context<Self>) {
        if self.react_picker.as_deref() == Some(message_id.as_str()) {
            self.react_picker = None;
        } else {
            self.react_picker = Some(message_id);
            self.reaction_details = None;
            self.message_ops.menu = None;
        }
        cx.notify();
    }

    pub(crate) fn toggle_reaction_details(&mut self, message_id: String, cx: &mut Context<Self>) {
        let previous = if self.reaction_details.as_deref() == Some(message_id.as_str()) {
            self.reaction_details.take()
        } else {
            self.react_picker = None;
            self.reaction_details.replace(message_id.clone())
        };
        if let Some(thread_id) = self.selected.clone() {
            if let Some(previous) = previous.filter(|previous| *previous != message_id) {
                self.remeasure_message(&thread_id, &previous, cx);
            }
            self.remeasure_message(&thread_id, &message_id, cx);
        }
        cx.notify();
    }

    pub(crate) fn react(&mut self, message: &Message, emoji: &str, cx: &mut Context<Self>) {
        self.react_picker = None;
        let already_mine = message
            .reactions
            .iter()
            .any(|reaction| reaction.mine && reaction.emoji == emoji);
        let emoji = if already_mine { "" } else { emoji };
        self.apply_local_reaction(&message.thread_id, &message.id, emoji, cx);
        let Some(bridge) = self.bridge() else { return };
        let params = json!({
            "thread_id": message.thread_id,
            "message_id": message.id,
            "emoji": emoji,
        });
        cx.spawn(async move |this, cx| {
            if let Err(err) = bridge.request("react", params).await {
                let _ = this.update(cx, |this, cx| {
                    this.set_error(err.context(t!("compose.error.reaction")));
                    cx.notify();
                });
            }
        })
        .detach();
        cx.notify();
    }

    fn apply_local_reaction(
        &mut self,
        thread_id: &str,
        message_id: &str,
        emoji: &str,
        cx: &mut Context<Self>,
    ) {
        let is_selected = self.selected.as_deref() == Some(thread_id);
        let me = self
            .user
            .as_ref()
            .map(|user| Reactor {
                user_id: user.id.clone(),
                name: user.name.clone(),
                avatar_url: user.avatar_url.clone(),
            })
            .unwrap_or_default();
        let Some(list) = self.messages.get_mut(thread_id) else {
            return;
        };
        let Some(index) = list.iter().position(|m| m.id == message_id) else {
            return;
        };
        let reactions = &mut list[index].reactions;
        for reaction in reactions.iter_mut().filter(|r| r.mine) {
            reaction.mine = false;
            reaction.count = reaction.count.saturating_sub(1);
            reaction.reactors.retain(|r| r.user_id != me.user_id);
        }
        reactions.retain(|r| r.count > 0);
        if !emoji.is_empty() {
            match reactions.iter_mut().find(|r| r.emoji == emoji) {
                Some(reaction) => {
                    reaction.count += 1;
                    reaction.mine = true;
                    reaction.reactors.insert(0, me);
                }
                None => reactions.push(Reaction {
                    emoji: emoji.to_string(),
                    count: 1,
                    mine: true,
                    reactors: vec![me],
                }),
            }
        }
        if is_selected {
            self.scroller.update(cx, |scroller, cx| {
                scroller.remeasure_items(index..index + 1, cx)
            });
        }
    }

    pub(crate) fn pick_files(&mut self, cx: &mut Context<Self>) {
        let receiver = cx.prompt_for_paths(PathPromptOptions {
            files: true,
            directories: false,
            multiple: true,
            prompt: Some(ts!("compose.attach.prompt")),
        });
        cx.spawn(async move |this, cx| {
            if let Ok(Ok(Some(paths))) = receiver.await {
                let _ = this.update(cx, |this, cx| this.add_pending(paths, cx));
            }
        })
        .detach();
    }

    pub(crate) fn add_pending(
        &mut self,
        paths: impl IntoIterator<Item = PathBuf>,
        cx: &mut Context<Self>,
    ) {
        if self.selected.is_none() {
            return;
        }
        for path in paths {
            if path.is_file()
                && !self.pending_files.contains(&path)
                && self.pending_files.len() < MAX_PENDING
            {
                self.pending_files.push(path);
            }
        }
        cx.notify();
    }

    pub(crate) fn remove_pending(&mut self, index: usize, cx: &mut Context<Self>) {
        if index < self.pending_files.len() {
            self.pending_files.remove(index);
            cx.notify();
        }
    }

    pub(crate) fn paste_image(&mut self, item: &ClipboardItem, cx: &mut Context<Self>) -> bool {
        let Some(image) = item.entries().iter().find_map(|entry| match entry {
            ClipboardEntry::Image(image) => Some(image),
            _ => None,
        }) else {
            return false;
        };
        let extension = match image.format {
            ImageFormat::Png => "png",
            ImageFormat::Jpeg => "jpg",
            ImageFormat::Gif => "gif",
            ImageFormat::Webp => "webp",
            ImageFormat::Bmp => "bmp",
            _ => "png",
        };
        let Some(dir) = super::outgoing_dir() else {
            return false;
        };
        let stamp = chrono::Local::now().format("%Y%m%d-%H%M%S-%3f");
        let path = dir.join(format!("capture-{stamp}.{extension}"));
        let bytes = image.bytes.clone();
        let target = path.clone();
        cx.spawn(async move |this, cx| {
            let written = cx
                .background_executor()
                .spawn(async move {
                    std::fs::create_dir_all(&dir)?;
                    std::fs::write(&target, bytes)
                })
                .await;
            match written {
                Ok(()) => {
                    let _ = this.update(cx, |this, cx| this.add_pending([path], cx));
                }
                Err(err) => eprintln!("[app] pasted image not saved: {:?}", err.kind()),
            }
        })
        .detach();
        true
    }

    pub(crate) fn send_current(&mut self, window: &mut Window, cx: &mut Context<Self>) {
        if self.planner.blocks_enter() {
            return;
        }
        if self.message_ops.editing.is_some() {
            self.submit_edit(window, cx);
            return;
        }
        let text = self.composer.read(cx).value().trim().to_string();
        let files = std::mem::take(&mut self.pending_files);
        if text.is_empty() && files.is_empty() {
            return;
        }
        let Some(thread_id) = self.selected.clone() else {
            return;
        };
        self.composer
            .update(cx, |input, cx| input.clean(window, cx));
        let reply_to = self.reply_to.take().map(|message| message.id);
        let mentions = super::mentions::compute_mentions(&text, &self.pending_mentions);
        self.pending_mentions.clear();
        self.mention_query = None;
        self.planner.snippet_query = None;
        self.stop_typing(cx);
        self.mark_thread_read(&thread_id, cx);
        self.scroller
            .update(cx, |scroller, cx| scroller.scroll_to_end(cx));
        if files.is_empty() {
            self.queue_outgoing(
                OutgoingRequest {
                    thread_id,
                    text,
                    reply_to,
                    files: Vec::new(),
                    mentions,
                    mark_read: true,
                },
                cx,
            );
        } else {
            let mut caption = Some(text).filter(|text| !text.is_empty());
            let mut reply_to = reply_to;
            let mut mentions = Some(mentions);
            for file in files {
                let text = caption.take().unwrap_or_default();
                let mentions = if text.is_empty() {
                    Vec::new()
                } else {
                    mentions.take().unwrap_or_default()
                };
                self.queue_outgoing(
                    OutgoingRequest {
                        thread_id: thread_id.clone(),
                        text,
                        reply_to: reply_to.take(),
                        files: vec![file],
                        mentions,
                        mark_read: true,
                    },
                    cx,
                );
            }
        }
        cx.notify();
    }

    pub(crate) fn like_emoji(&self, thread: &Thread) -> String {
        thread
            .quick_emoji
            .as_deref()
            .map(str::trim)
            .filter(|emoji| !emoji.is_empty())
            .unwrap_or(DEFAULT_LIKE)
            .to_string()
    }

    pub(crate) fn send_like(&mut self, cx: &mut Context<Self>) {
        if self.message_ops.editing.is_some() {
            return;
        }
        let Some(thread) = self.selected_thread() else {
            return;
        };
        let thread_id = thread.id.clone();
        let emoji = self.like_emoji(thread);
        let reply_to = self.reply_to.take().map(|message| message.id);
        self.scroller
            .update(cx, |scroller, cx| scroller.scroll_to_end(cx));
        self.send_text_to(thread_id, emoji, reply_to, cx);
        cx.notify();
    }

    pub(crate) fn send_text_to(
        &mut self,
        thread_id: String,
        text: String,
        reply_to: Option<String>,
        cx: &mut Context<Self>,
    ) {
        self.mark_thread_read(&thread_id, cx);
        self.queue_outgoing(
            OutgoingRequest {
                thread_id,
                text,
                reply_to,
                files: Vec::new(),
                mentions: Vec::new(),
                mark_read: true,
            },
            cx,
        );
    }

    pub(crate) fn queue_outgoing(
        &mut self,
        request: OutgoingRequest,
        cx: &mut Context<Self>,
    ) -> String {
        self.next_client_id += 1;
        let client_id = format!(
            "local-{}-{}",
            chrono::Utc::now().timestamp_millis(),
            self.next_client_id
        );
        let local = self.local_message(&client_id, &request);
        self.insert_local_message(local, cx);
        self.outgoing.insert(client_id.clone(), Outgoing::Sending);
        let thread_id = request.thread_id.clone();
        self.outgoing_requests.insert(client_id.clone(), request);
        self.find.offline.register(&client_id, Instant::now());
        if self.link == Link::Online && !self.find.offline.has_thread(&thread_id) {
            self.dispatch_outgoing(client_id.clone(), cx);
        } else {
            self.queue_offline(client_id.clone(), thread_id, cx);
            self.flush_offline_queue(cx);
        }
        client_id
    }

    fn queue_offline(&mut self, client_id: String, thread_id: String, cx: &mut Context<Self>) {
        self.outgoing.insert(client_id.clone(), Outgoing::Sending);
        let (seq, at) = self.find.offline.origin(&client_id, Instant::now());
        self.find.offline.push(Queued {
            client_id: client_id.clone(),
            thread_id: thread_id.clone(),
            seq,
            at,
        });
        self.remeasure_message(&thread_id, &client_id, cx);
        self.ensure_offline_ticker(cx);
        cx.notify();
    }

    fn ensure_offline_ticker(&mut self, cx: &mut Context<Self>) {
        if self.find.offline.ticking {
            return;
        }
        self.find.offline.ticking = true;
        cx.spawn(async move |this, cx| {
            loop {
                cx.background_executor().timer(OFFLINE_TICK).await;
                let keep = this
                    .update(cx, |this, cx| {
                        this.flush_offline_queue(cx);
                        let keep = !this.find.offline.is_empty();
                        this.find.offline.ticking = keep;
                        keep
                    })
                    .unwrap_or(false);
                if !keep {
                    break;
                }
            }
        })
        .detach();
    }

    fn expire_offline(&mut self, cx: &mut Context<Self>) {
        for item in self.find.offline.take_expired(Instant::now()) {
            self.find.offline.forget(&item.client_id);
            self.scheduled_send_finished(&item.client_id, Some(offline_expired().to_string()), cx);
            if self.outgoing.contains_key(&item.client_id) {
                self.outgoing
                    .insert(item.client_id.clone(), Outgoing::Failed(offline_expired()));
                self.remeasure_message(&item.thread_id, &item.client_id, cx);
            }
            cx.notify();
        }
    }

    pub(crate) fn flush_offline_queue(&mut self, cx: &mut Context<Self>) {
        self.expire_offline(cx);
        if self.link != Link::Online {
            return;
        }
        loop {
            let batch = self.find.offline.next_batch();
            if batch.is_empty() {
                break;
            }
            let mut stale = false;
            for item in batch {
                if !self.outgoing_requests.contains_key(&item.client_id)
                    || !self.outgoing.contains_key(&item.client_id)
                {
                    self.find.offline.finish(&item.thread_id, &item.client_id);
                    stale = true;
                    continue;
                }
                self.outgoing
                    .insert(item.client_id.clone(), Outgoing::Sending);
                self.remeasure_message(&item.thread_id, &item.client_id, cx);
                self.dispatch_outgoing(item.client_id, cx);
            }
            if !stale {
                break;
            }
        }
        cx.notify();
    }

    fn is_scheduled_client(&self, client_id: &str) -> bool {
        self.planner
            .scheduled
            .iter()
            .any(|item| item.client_id.as_deref() == Some(client_id))
    }

    fn local_message(&mut self, client_id: &str, request: &OutgoingRequest) -> Message {
        let (sender_id, sender_name) = self
            .user
            .as_ref()
            .map(|user| (user.id.clone(), user.name.clone()))
            .unwrap_or_default();
        let reply_preview = request.reply_to.as_ref().and_then(|id| {
            self.messages
                .get(&request.thread_id)
                .and_then(|list| list.iter().find(|m| &m.id == id))
                .map(|quoted| ReplyPreview {
                    id: quoted.id.clone(),
                    sender_name: quoted.sender_name.clone(),
                    text: quoted.text.clone(),
                    kind: quoted.attachments.first().map(|a| a.kind.clone()),
                })
        });
        let attachments: Vec<Attachment> = request
            .files
            .iter()
            .enumerate()
            .map(|(index, path)| {
                let extension = path
                    .extension()
                    .map(|ext| ext.to_string_lossy().to_lowercase())
                    .unwrap_or_default();
                let kind = match extension.as_str() {
                    "png" | "jpg" | "jpeg" | "gif" | "webp" | "bmp" => "image",
                    "mp4" | "mov" | "webm" | "mkv" => "video",
                    "mp3" | "m4a" | "ogg" | "wav" => "audio",
                    _ => "file",
                };
                let id = format!("local{index}");
                if kind == "image" {
                    self.media
                        .insert(super::thumb_key(client_id, &id), Media::Ready(path.clone()));
                }
                Attachment {
                    id,
                    kind: kind.to_string(),
                    file_name: path
                        .file_name()
                        .map(|name| name.to_string_lossy().into_owned()),
                    ..Attachment::default()
                }
            })
            .collect();
        Message {
            id: client_id.to_string(),
            client_id: Some(client_id.to_string()),
            mentions: if request.files.is_empty() {
                request.mentions.clone()
            } else {
                Vec::new()
            },
            thread_id: request.thread_id.clone(),
            sender_id,
            sender_name,
            text: request.text.clone(),
            timestamp_ms: chrono::Utc::now().timestamp_millis(),
            is_outgoing: true,
            attachments,
            reply_to: reply_preview,
            ..Message::default()
        }
    }

    fn insert_local_message(&mut self, message: Message, cx: &mut Context<Self>) {
        let thread_id = message.thread_id.clone();
        if let Some(thread) = self.threads.iter_mut().find(|t| t.id == thread_id) {
            thread.snippet = super::preview(&message);
            thread.last_activity_ms = thread.last_activity_ms.max(message.timestamp_ms);
        }
        self.sort_threads();
        let is_selected = self.selected.as_deref() == Some(thread_id.as_str());
        self.remeasure_last_outgoing(&thread_id, cx);
        if let Some(list) = self.messages.get_mut(&thread_id) {
            list.push(message);
            if is_selected {
                self.scroller.update(cx, |scroller, cx| {
                    scroller.append(1, cx);
                    scroller.scroll_to_end(cx);
                });
            }
        }
        cx.notify();
    }

    pub(crate) fn retry_outgoing(&mut self, client_id: String, cx: &mut Context<Self>) {
        let Some(thread_id) = self
            .outgoing_requests
            .get(&client_id)
            .map(|request| request.thread_id.clone())
        else {
            return;
        };
        self.find.offline.remove(&client_id);
        self.find.offline.register(&client_id, Instant::now());
        if self.link == Link::Online && !self.find.offline.has_thread(&thread_id) {
            self.outgoing.insert(client_id.clone(), Outgoing::Sending);
            self.dispatch_outgoing(client_id, cx);
        } else {
            self.queue_offline(client_id, thread_id, cx);
            self.flush_offline_queue(cx);
        }
        cx.notify();
    }

    pub(crate) fn discard_outgoing(&mut self, client_id: String, cx: &mut Context<Self>) {
        let Some(request) = self.outgoing_requests.remove(&client_id) else {
            return;
        };
        self.outgoing.remove(&client_id);
        self.find.offline.forget(&client_id);
        if self.find.offline.remove(&client_id) {
            self.scheduled_send_finished(&client_id, Some(offline_cancelled().to_string()), cx);
        }
        let is_selected = self.selected.as_deref() == Some(request.thread_id.as_str());
        if let Some(list) = self.messages.get_mut(&request.thread_id)
            && let Some(index) = list.iter().position(|m| m.id == client_id) {
                list.remove(index);
                if is_selected {
                    self.scroller
                        .update(cx, |scroller, cx| scroller.splice(index..index + 1, 0, cx));
                }
            }
        self.remeasure_last_outgoing(&request.thread_id, cx);
        cx.notify();
    }

    fn dispatch_outgoing(&mut self, client_id: String, cx: &mut Context<Self>) {
        let Some(request) = self.outgoing_requests.get(&client_id).cloned() else {
            return;
        };
        let Some(bridge) = self.bridge() else { return };
        let (cmd, mut params) = if request.files.is_empty() {
            let mut params = json!({ "thread_id": request.thread_id, "text": request.text, "client_id": client_id });
            if !request.mentions.is_empty() {
                params["mentions"] = json!(request.mentions);
            }
            if !request.mark_read {
                params["mark_read"] = json!(false);
            }
            ("send", params)
        } else {
            let paths: Vec<String> = request
                .files
                .iter()
                .map(|path| path.to_string_lossy().into_owned())
                .collect();
            let mut params = json!({
                "thread_id": request.thread_id,
                "paths": paths,
                "client_ids": [client_id],
            });
            if !request.text.is_empty() {
                params["caption"] = json!(request.text);
            }
            ("send_media", params)
        };
        if let Some(reply_to) = &request.reply_to {
            params["reply_to_id"] = json!(reply_to);
        }
        if cmd == "send_media" {
            self.sending += 1;
        }
        cx.spawn(async move |this, cx| {
            let result = bridge.request(cmd, params).await;
            let _ = this.update(cx, |this, cx| {
                if cmd == "send_media" {
                    this.sending = this.sending.saturating_sub(1);
                }
                this.find.offline.finish(&request.thread_id, &client_id);
                let mut requeued = false;
                match result {
                    Ok(_) => {
                        this.find.offline.forget(&client_id);
                        this.outgoing.remove(&client_id);
                        this.remeasure_message(&request.thread_id, &client_id, cx);
                        this.scheduled_send_finished(&client_id, None, cx);
                    }
                    Err(err)
                        if not_connected(&err)
                            && !send_uncertain(&err)
                            && !this.is_scheduled_client(&client_id)
                            && this.outgoing.contains_key(&client_id)
                            && this.outgoing_requests.contains_key(&client_id) =>
                    {
                        this.queue_offline(client_id.clone(), request.thread_id.clone(), cx);
                        requeued = true;
                    }
                    Err(err) => {
                        let label: SharedString = if send_uncertain(&err) {
                            ts!("compose.error.send_unconfirmed")
                        } else {
                            ts!("compose.error.send_failed")
                        };
                        this.scheduled_send_finished(&client_id, Some(label.to_string()), cx);
                        if this.outgoing.contains_key(&client_id) {
                            this.outgoing
                                .insert(client_id.clone(), Outgoing::Failed(label));
                        }
                    }
                }
                if !requeued && !this.find.offline.is_empty() {
                    this.flush_offline_queue(cx);
                }
                cx.notify();
            });
        })
        .detach();
    }

    pub(crate) fn confirm_echo(&mut self, echo: &Message, cx: &mut Context<Self>) -> bool {
        if echo.is_outgoing && !echo.system {
            let known = self.messages.get(&echo.thread_id).is_some_and(|list| {
                list.iter()
                    .any(|m| m.id == echo.id || echo.client_id.as_deref() == Some(m.id.as_str()))
            });
            if !known {
                self.remeasure_last_outgoing(&echo.thread_id, cx);
            }
        }
        let Some(client_id) = echo.client_id.clone() else {
            return false;
        };
        self.outgoing.remove(&client_id);
        self.outgoing_requests.remove(&client_id);
        self.find.offline.forget(&client_id);
        self.find.offline.remove(&client_id);
        let is_selected = self.selected.as_deref() == Some(echo.thread_id.as_str());
        let Some(list) = self.messages.get_mut(&echo.thread_id) else {
            return false;
        };
        let Some(index) = list.iter().position(|m| m.id == client_id) else {
            return false;
        };
        if list.iter().any(|m| m.id == echo.id) {
            list.remove(index);
            if is_selected {
                self.scroller
                    .update(cx, |scroller, cx| scroller.splice(index..index + 1, 0, cx));
            }
            return true;
        }
        for attachment in &echo.attachments {
            let local = list[index]
                .attachments
                .iter()
                .position(|a| a.kind == attachment.kind)
                .map(|i| format!("local{i}"));
            if let Some(local) = local
                && let Some(Media::Ready(path)) = self
                    .media
                    .get(&super::thumb_key(&client_id, &local))
                    .cloned()
                {
                    self.media
                        .entry(super::thumb_key(&echo.id, &attachment.id))
                        .or_insert(Media::Ready(path));
                }
        }
        let Some(list) = self.messages.get_mut(&echo.thread_id) else {
            return false;
        };
        list[index] = echo.clone();
        if is_selected {
            self.scroller.update(cx, |scroller, cx| {
                scroller.remeasure_items(index..index + 1, cx)
            });
        }
        true
    }

    pub(crate) fn on_composer_changed(&mut self, cx: &mut Context<Self>) {
        self.refresh_mention_query(cx);
        if self.message_ops.editing.is_some() {
            return;
        }
        let Some(thread_id) = self.selected.clone() else {
            return;
        };
        if !self.settings.send_typing {
            self.stop_typing(cx);
            return;
        }
        let empty = self.composer.read(cx).value().trim().is_empty();
        if empty {
            self.stop_typing(cx);
            return;
        }
        let fresh = self
            .typing_sent
            .as_ref()
            .is_some_and(|(id, at)| *id == thread_id && at.elapsed() < TYPING_REFRESH);
        if fresh {
            return;
        }
        self.typing_sent = Some((thread_id.clone(), Instant::now()));
        self.send_typing(thread_id, true, cx);
    }

    pub(crate) fn stop_typing(&mut self, cx: &mut Context<Self>) {
        if let Some((thread_id, _)) = self.typing_sent.take() {
            self.send_typing(thread_id, false, cx);
        }
    }

    fn send_typing(&self, thread_id: String, typing: bool, cx: &mut Context<Self>) {
        if typing && !self.settings.send_typing {
            return;
        }
        let request = self
            .threads
            .iter()
            .find(|thread| thread.id == thread_id)
            .is_some_and(|thread| thread.folder == "requests" || thread.folder == "spam");
        if request {
            return;
        }
        let Some(bridge) = self.bridge() else { return };
        cx.background_spawn(async move {
            let _ = bridge
                .request(
                    "set_typing",
                    json!({ "thread_id": thread_id, "typing": typing }),
                )
                .await;
        })
        .detach();
    }

    pub(crate) fn rescue_unsent_texts(&mut self, cx: &App) {
        let mut rescued: Vec<(String, String)> = Vec::new();
        for (thread_id, list) in self.messages.iter() {
            for message in list {
                let Some(request) = self.outgoing_requests.get(&message.id) else {
                    continue;
                };
                let unsent = self.find.offline.is_queued(&message.id)
                    || matches!(self.outgoing.get(&message.id), Some(Outgoing::Failed(_)));
                if unsent
                    && request.files.is_empty()
                    && !request.text.trim().is_empty()
                    && !self.is_scheduled_client(&message.id)
                {
                    rescued.push((thread_id.clone(), request.text.clone()));
                }
            }
        }
        if rescued.is_empty() {
            return;
        }
        let mut drafts = self.drafts.clone();
        if let Some(thread_id) = &self.selected {
            let current = self.composer.read(cx).value().to_string();
            if !current.trim().is_empty() {
                drafts.insert(thread_id.clone(), current);
            }
        }
        for (thread_id, text) in rescued {
            let existing = drafts.get(&thread_id).map(String::as_str).unwrap_or("");
            if let Some(merged) = merge_draft(existing, &text) {
                drafts.insert(thread_id, merged);
            }
        }
        if let Some(path) = crate::storage::data_file(super::DRAFTS_FILE) {
            save_rescued_drafts(&super::DRAFT_SAVES, &path, &drafts);
        }
        self.drafts_saved = drafts.clone();
        self.drafts = drafts;
    }

    pub(crate) fn emoji_tabs() -> &'static [EmojiTab] {
        &EMOJI_TABS
    }

    pub(crate) fn toggle_emoji_picker(&mut self, window: &mut Window, cx: &mut Context<Self>) {
        if self.close_emoji_picker(cx) {
            return;
        }
        if self
            .find
            .emoji_closed_at
            .is_some_and(|at| at.elapsed() < EMOJI_REOPEN_GUARD)
        {
            return;
        }
        let search = cx.new(|cx| InputState::new(window, cx).placeholder(t!("compose.emoji.search_placeholder")));
        let subscription = cx.subscribe_in(
            &search,
            window,
            |this, _, event: &InputEvent, window, cx| match event {
                InputEvent::Change => {
                    if let Some(picker) = this.find.emoji.as_mut() {
                        picker.hovered = None;
                        picker.scroll.set_offset(point(px(0.), px(0.)));
                    }
                    cx.notify();
                }
                InputEvent::PressEnter { .. } => {
                    if let Some(first) = this.emoji_results(cx).first() {
                        this.insert_emoji(first.as_str(), window, cx);
                    }
                }
                _ => {}
            },
        );
        let tab = if self.settings.recent_emojis.is_empty() {
            EMOJI_TABS[1]
        } else {
            EmojiTab::Recent
        };
        self.find.emoji = Some(EmojiPicker {
            search,
            tab,
            hovered: None,
            scroll: ScrollHandle::new(),
            _subscription: subscription,
        });
        cx.notify();
    }

    pub(crate) fn close_emoji_picker(&mut self, cx: &mut Context<Self>) -> bool {
        if self.find.emoji.take().is_none() {
            return false;
        }
        self.find.emoji_closed_at = Some(Instant::now());
        cx.notify();
        true
    }

    pub(crate) fn select_emoji_tab(&mut self, tab: EmojiTab, cx: &mut Context<Self>) {
        if let Some(picker) = self.find.emoji.as_mut() {
            picker.tab = tab;
            picker.hovered = None;
            picker.scroll.set_offset(point(px(0.), px(0.)));
            cx.notify();
        }
    }

    pub(crate) fn hover_emoji(
        &mut self,
        emoji: &'static emojis::Emoji,
        hovered: bool,
        cx: &mut Context<Self>,
    ) {
        let Some(picker) = self.find.emoji.as_mut() else {
            return;
        };
        if hovered {
            picker.hovered = Some(emoji);
        } else if picker
            .hovered
            .is_some_and(|current| std::ptr::eq(current, emoji))
        {
            picker.hovered = None;
        } else {
            return;
        }
        cx.notify();
    }

    pub(crate) fn emoji_results(&self, cx: &App) -> Vec<&'static emojis::Emoji> {
        let Some(picker) = &self.find.emoji else {
            return Vec::new();
        };
        let query = picker.search.read(cx).value().to_string();
        if !query.trim().is_empty() {
            return search_emojis(&query);
        }
        match picker.tab {
            EmojiTab::Recent => self
                .settings
                .recent_emojis
                .iter()
                .filter_map(|emoji| emojis::get(emoji))
                .collect(),
            EmojiTab::Group(group) => group.emojis().filter(|e| emoji_available(e)).collect(),
        }
    }

    pub(crate) fn insert_emoji(&mut self, emoji: &str, window: &mut Window, cx: &mut Context<Self>) {
        self.composer.update(cx, |input, cx| {
            input.replace(emoji.to_string(), window, cx);
            input.focus(window, cx);
        });
        push_recent_emoji(&mut self.settings.recent_emojis, emoji);
        self.mark_settings_dirty();
        self.on_composer_changed(cx);
        cx.notify();
    }
}

#[cfg(test)]
mod tests {
    use std::time::{Duration, Instant};

    use super::{
        OFFLINE_EXPIRY, OfflineQueue, Queued, merge_draft, push_recent_emoji, save_rescued_drafts,
        search_emojis, send_uncertain,
    };
    use crate::bridge::BridgeError;

    fn bridge_error(code: &str, data: Option<serde_json::Value>) -> anyhow::Error {
        anyhow::Error::new(BridgeError {
            code: code.into(),
            message: String::new(),
            data,
        })
    }

    #[::core::prelude::v1::test]
    fn sends_without_an_answer_are_unconfirmed_not_failed() {
        assert!(send_uncertain(&bridge_error("timeout", None)));
        assert!(send_uncertain(&bridge_error("bridge_stopped", None)));
        assert!(send_uncertain(&bridge_error(
            "send_failed",
            Some(serde_json::json!({ "uncertain": true }))
        )));
        assert!(!send_uncertain(&bridge_error("not_connected", None)));
        assert!(!send_uncertain(&bridge_error("bad_request", None)));
        assert!(!send_uncertain(&anyhow::anyhow!("timeout")));
    }

    #[::core::prelude::v1::test]
    fn an_older_background_draft_save_cannot_overwrite_rescued_texts() {
        let dir = std::env::temp_dir().join(format!(
            "missive-rescue-{}-{}",
            std::process::id(),
            chrono::Utc::now().timestamp_nanos_opt().unwrap_or_default()
        ));
        let path = dir.join("drafts.json");
        let slot = crate::storage::SaveSlot::new();
        let background = slot.ticket();
        let rescued: std::collections::HashMap<String, String> =
            [("t1".to_string(), "unsent".to_string())].into();
        assert!(save_rescued_drafts(&slot, &path, &rescued));
        let empty: std::collections::HashMap<String, String> = Default::default();
        assert!(!slot.save_path(background, &path, &empty));
        let stored: std::collections::HashMap<String, String> =
            serde_json::from_str(&std::fs::read_to_string(&path).unwrap()).unwrap();
        assert_eq!(stored, rescued);
        let _ = std::fs::remove_dir_all(dir);
    }

    fn enqueue(queue: &mut OfflineQueue, client: &str, thread: &str, now: Instant) {
        let (seq, at) = queue.origin(client, now);
        queue.push(Queued {
            client_id: client.into(),
            thread_id: thread.into(),
            seq,
            at,
        });
    }

    fn ids(batch: Vec<Queued>) -> Vec<String> {
        batch.into_iter().map(|q| q.client_id).collect()
    }

    #[::core::prelude::v1::test]
    fn offline_items_expire_thirty_minutes_after_creation() {
        let start = Instant::now();
        let mut queue = OfflineQueue::default();
        queue.register("a", start);
        queue.register("b", start + Duration::from_secs(60));
        enqueue(&mut queue, "a", "t1", start + Duration::from_secs(600));
        enqueue(&mut queue, "b", "t1", start + Duration::from_secs(600));
        assert!(queue.take_expired(start + OFFLINE_EXPIRY - Duration::from_secs(1)).is_empty());
        let expired = queue.take_expired(start + OFFLINE_EXPIRY);
        assert_eq!(ids(expired), ["a"]);
        assert!(queue.is_waiting("b"));
        assert!(!queue.is_waiting("a"));
    }

    #[::core::prelude::v1::test]
    fn offline_flush_is_ordered_per_thread() {
        let start = Instant::now();
        let mut queue = OfflineQueue::default();
        queue.register("a", start);
        queue.register("c", start);
        queue.register("b", start);
        enqueue(&mut queue, "b", "t1", start);
        enqueue(&mut queue, "a", "t1", start);
        enqueue(&mut queue, "c", "t2", start);
        assert_eq!(ids(queue.next_batch()), ["a", "c"]);
        assert!(queue.next_batch().is_empty());
        assert!(queue.is_queued("a") && !queue.is_waiting("a"));
        assert!(queue.finish("t1", "a"));
        assert!(!queue.finish("t1", "a"));
        assert_eq!(ids(queue.next_batch()), ["b"]);
        assert!(queue.has_thread("t1"));
        assert!(queue.remove("b"));
        assert!(queue.finish("t2", "c"));
        assert!(queue.is_empty());
    }

    #[::core::prelude::v1::test]
    fn requeued_direct_send_goes_back_before_later_messages() {
        let start = Instant::now();
        let mut queue = OfflineQueue::default();
        queue.register("a", start);
        queue.register("b", start);
        enqueue(&mut queue, "b", "t1", start);
        enqueue(&mut queue, "a", "t1", start + Duration::from_secs(30));
        assert_eq!(ids(queue.next_batch()), ["a"]);
    }

    #[::core::prelude::v1::test]
    fn unsent_texts_are_appended_to_drafts_once() {
        assert_eq!(merge_draft("", "salut"), Some("salut".into()));
        assert_eq!(merge_draft("brouillon\n", "salut"), Some("brouillon\n\nsalut".into()));
        assert_eq!(merge_draft("brouillon\n\nsalut", "salut"), None);
        assert_eq!(merge_draft("brouillon", "   "), None);
    }

    #[::core::prelude::v1::test]
    fn recent_emojis_are_deduplicated_and_capped() {
        let mut recents: Vec<String> = (0..32).map(|i| i.to_string()).collect();
        push_recent_emoji(&mut recents, "5");
        assert_eq!(recents[0], "5");
        assert_eq!(recents.len(), 32);
        assert_eq!(recents.iter().filter(|e| *e == "5").count(), 1);
        push_recent_emoji(&mut recents, "👍");
        assert_eq!(recents[0], "👍");
        assert_eq!(recents.len(), 32);
        assert_eq!(recents.last().map(String::as_str), Some("30"));
    }

    #[::core::prelude::v1::test]
    fn emoji_search_understands_french_and_shortcodes() {
        let found = |query: &str| -> Vec<&str> {
            search_emojis(query).into_iter().map(|e| e.as_str()).collect()
        };
        assert!(found("coeur").contains(&"❤️"));
        assert!(found("Fête").contains(&"🎉"));
        assert!(found("rire").contains(&"😂"));
        assert!(found("thumbsup").contains(&"👍"));
        assert!(found("pizza").contains(&"🍕"));
        assert!(found("   ").is_empty());
    }
}
