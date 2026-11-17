use gpui_kit::*;

use super::{MissiveApp, library::fold};
use crate::model::{Mention, Participant};

const MAX_QUERY: usize = 30;
const MAX_SUGGESTIONS: usize = 6;

pub(crate) fn mention_query(text: &str) -> Option<(usize, String)> {
    let at = text.rfind('@')?;
    let before_ok = text[..at].chars().last().is_none_or(char::is_whitespace);
    let query = &text[at + 1..];
    if !before_ok || query.contains('\n') || query.chars().count() > MAX_QUERY {
        return None;
    }
    if query.chars().filter(|c| c.is_whitespace()).count() > 2 {
        return None;
    }
    Some((at, query.to_string()))
}

pub(crate) fn compute_mentions(text: &str, picked: &[(String, String)]) -> Vec<Mention> {
    let mut mentions = Vec::new();
    let mut taken: Vec<(usize, usize)> = Vec::new();
    for (label, user_id) in picked {
        let mut from = 0;
        while let Some(found) = text[from..].find(label.as_str()) {
            let start = from + found;
            let end = start + label.len();
            let boundary_ok = text[end..]
                .chars()
                .next()
                .is_none_or(|c| !c.is_alphanumeric());
            let overlaps = taken.iter().any(|(s, e)| start < *e && end > *s);
            if boundary_ok && !overlaps {
                taken.push((start, end));
                mentions.push(Mention {
                    offset: start,
                    length: label.len(),
                    user_id: user_id.clone(),
                });
                break;
            }
            from = start + label.len();
        }
    }
    mentions.sort_by_key(|mention| mention.offset);
    mentions
}

impl MissiveApp {
    pub(crate) fn refresh_mention_query(&mut self, cx: &mut Context<Self>) {
        let is_group = self.selected_thread().is_some_and(|thread| thread.is_group);
        let text = self.composer.read(cx).value().to_string();
        let query = if is_group {
            mention_query(&text).map(|(_, query)| query)
        } else {
            None
        };
        if query != self.mention_query {
            self.mention_selected = 0;
        }
        self.mention_query = query;
        self.refresh_snippet_query(&text);
        let text_now = text;
        self.pending_mentions
            .retain(|(label, _)| text_now.contains(label.as_str()));
    }

    pub(crate) fn mention_candidates(&self) -> Vec<Participant> {
        let Some(query) = self.mention_query.as_ref() else {
            return Vec::new();
        };
        let Some(thread) = self.selected_thread() else {
            return Vec::new();
        };
        let needle = fold(query);
        let mut matches: Vec<Participant> = thread
            .participants
            .iter()
            .filter(|participant| !participant.name.is_empty())
            .filter(|participant| {
                let name = fold(&participant.name);
                needle.is_empty()
                    || name.starts_with(&needle)
                    || name
                        .split_whitespace()
                        .any(|word| word.starts_with(&needle))
            })
            .cloned()
            .collect();
        matches.sort_by(|a, b| a.name.cmp(&b.name));
        matches.truncate(MAX_SUGGESTIONS);
        matches
    }

    pub(crate) fn mention_list_open(&self) -> bool {
        self.mention_query.is_some() && !self.mention_candidates().is_empty()
    }

    pub(crate) fn mention_popup_open(&self) -> bool {
        !self.planner.blocks_enter() && (self.mention_list_open() || self.snippet_popup_active())
    }

    pub(crate) fn move_mention(&mut self, delta: isize, cx: &mut Context<Self>) {
        let count = if self.mention_list_open() {
            self.mention_candidates().len()
        } else {
            self.snippet_candidates().len()
        };
        if count > 0 {
            self.mention_selected =
                (self.mention_selected as isize + delta).rem_euclid(count as isize) as usize;
            cx.notify();
        }
    }

    pub(crate) fn accept_mention(
        &mut self,
        index: usize,
        window: &mut Window,
        cx: &mut Context<Self>,
    ) {
        if !self.mention_list_open() {
            self.accept_snippet(index, window, cx);
            return;
        }
        let candidates = self.mention_candidates();
        let Some(participant) = candidates.get(index) else {
            return;
        };
        let text = self.composer.read(cx).value().to_string();
        let Some((at, _)) = mention_query(&text) else {
            return;
        };
        let label = format!("@{}", participant.name);
        let new_text = format!("{}{} ", &text[..at], label);
        self.pending_mentions
            .push((label, participant.user_id.clone()));
        self.mention_query = None;
        let line = new_text.matches('\n').count() as u32;
        let character = new_text
            .rsplit('\n')
            .next()
            .map(|last| last.encode_utf16().count() as u32)
            .unwrap_or(0);
        self.composer.update(cx, |input, cx| {
            input.set_value(new_text, window, cx);
            input.set_cursor_position(
                gpui_kit::component::input::Position { line, character },
                window,
                cx,
            );
            input.focus(window, cx);
        });
        cx.notify();
    }

    pub(crate) fn mentions_me(&self, message: &crate::model::Message) -> bool {
        self.user.as_ref().is_some_and(|user| {
            message
                .mentions
                .iter()
                .any(|mention| mention.user_id == user.id)
        })
    }

    pub(crate) fn close_mentions(&mut self, cx: &mut Context<Self>) {
        self.mention_query = None;
        self.planner.snippet_query = None;
        cx.notify();
    }
}

#[cfg(test)]
mod tests {
    use super::{compute_mentions, mention_query};

    #[::core::prelude::v1::test]
    fn detects_query_at_word_start_only() {
        assert_eq!(mention_query("salut @Cam").map(|q| q.1), Some("Cam".into()));
        assert_eq!(mention_query("@").map(|q| q.1), Some(String::new()));
        assert!(mention_query("mail@exemple.fr").is_none());
        assert!(mention_query("pas de mention").is_none());
    }

    #[::core::prelude::v1::test]
    fn computes_byte_offsets_with_accents_and_emoji() {
        let text = "😀 coucou @Élodie Martin et @Théo !";
        let picked = vec![
            ("@Élodie Martin".to_string(), "1".to_string()),
            ("@Théo".to_string(), "2".to_string()),
        ];
        let mentions = compute_mentions(text, &picked);
        assert_eq!(mentions.len(), 2);
        assert_eq!(
            &text[mentions[0].offset..mentions[0].offset + mentions[0].length],
            "@Élodie Martin"
        );
        assert_eq!(
            &text[mentions[1].offset..mentions[1].offset + mentions[1].length],
            "@Théo"
        );
    }

    #[::core::prelude::v1::test]
    fn removed_mentions_are_dropped() {
        let picked = vec![("@Cam".to_string(), "1".to_string())];
        assert!(compute_mentions("plus de mention", &picked).is_empty());
        assert!(compute_mentions("@Camille", &picked).is_empty());
    }
}
