use std::{
    collections::{HashMap, HashSet},
    ops::{Deref, Range},
};

use crate::model::Message;

pub(crate) const KEPT_RECENT_THREADS: usize = 8;
pub(crate) const COLD_THREAD_MESSAGES: usize = 80;

#[derive(Default)]
pub(crate) struct MessageStore {
    lists: HashMap<String, Vec<Message>>,
    revisions: HashMap<String, u64>,
    next_revision: u64,
}

impl Deref for MessageStore {
    type Target = HashMap<String, Vec<Message>>;

    fn deref(&self) -> &Self::Target {
        &self.lists
    }
}

impl MessageStore {
    pub(crate) fn revision(&self, thread_id: &str) -> u64 {
        self.revisions.get(thread_id).copied().unwrap_or(0)
    }

    pub(crate) fn touch(&mut self, thread_id: &str) {
        self.next_revision += 1;
        match self.revisions.get_mut(thread_id) {
            Some(revision) => *revision = self.next_revision,
            None => {
                self.revisions
                    .insert(thread_id.to_string(), self.next_revision);
            }
        }
    }

    pub(crate) fn get_mut(&mut self, thread_id: &str) -> Option<&mut Vec<Message>> {
        if !self.lists.contains_key(thread_id) {
            return None;
        }
        self.touch(thread_id);
        self.lists.get_mut(thread_id)
    }

    pub(crate) fn insert(&mut self, thread_id: String, list: Vec<Message>) -> Option<Vec<Message>> {
        self.touch(&thread_id);
        self.lists.insert(thread_id, list)
    }

    pub(crate) fn remove(&mut self, thread_id: &str) -> Option<Vec<Message>> {
        self.touch(thread_id);
        self.lists.remove(thread_id)
    }

    pub(crate) fn clear(&mut self) {
        let ids: Vec<String> = self.lists.keys().cloned().collect();
        for id in ids {
            self.touch(&id);
        }
        self.lists.clear();
    }

    pub(crate) fn trim_to_latest(&mut self, thread_id: &str, keep: usize) -> Vec<Message> {
        let Some(list) = self.lists.get_mut(thread_id) else {
            return Vec::new();
        };
        if list.len() <= keep {
            return Vec::new();
        }
        let evicted: Vec<Message> = list.drain(..list.len() - keep).collect();
        self.touch(thread_id);
        evicted
    }

    pub(crate) fn cold_threads(&self, keep: &HashSet<&str>, limit: usize) -> Vec<String> {
        self.lists
            .iter()
            .filter(|(id, list)| list.len() > limit && !keep.contains(id.as_str()))
            .map(|(id, _)| id.clone())
            .collect()
    }
}

pub(crate) fn remember_recent(recent: &mut Vec<String>, thread_id: &str, capacity: usize) {
    recent.retain(|id| id != thread_id);
    recent.insert(0, thread_id.to_string());
    recent.truncate(capacity);
}

pub(crate) fn list_change(old: &[String], new: &[String]) -> Option<(Range<usize>, usize)> {
    if old == new {
        return None;
    }
    let prefix = old.iter().zip(new).take_while(|(a, b)| a == b).count();
    let room = old.len().min(new.len()) - prefix;
    let suffix = old
        .iter()
        .rev()
        .zip(new.iter().rev())
        .take(room)
        .take_while(|(a, b)| a == b)
        .count();
    Some((prefix..old.len() - suffix, new.len() - suffix - prefix))
}

#[cfg(test)]
mod tests {
    use super::*;

    fn message(id: &str, timestamp_ms: i64) -> Message {
        Message {
            id: id.into(),
            thread_id: "t".into(),
            timestamp_ms,
            ..Message::default()
        }
    }

    fn ids(values: &[&str]) -> Vec<String> {
        values.iter().map(|value| value.to_string()).collect()
    }

    #[::core::prelude::v1::test]
    fn every_mutation_moves_the_revision_forward() {
        let mut store = MessageStore::default();
        assert_eq!(store.revision("t"), 0);
        store.insert("t".into(), vec![message("a", 1)]);
        let inserted = store.revision("t");
        assert!(inserted > 0);
        assert!(store.get_mut("missing").is_none());
        assert_eq!(store.revision("missing"), 0);
        store.get_mut("t").unwrap().push(message("b", 2));
        let mutated = store.revision("t");
        assert!(mutated > inserted);
        store.touch("t");
        assert!(store.revision("t") > mutated);
        let before_clear = store.revision("t");
        store.clear();
        assert!(store.revision("t") > before_clear);
        assert!(store.get("t").is_none());
    }

    #[::core::prelude::v1::test]
    fn trimming_keeps_the_latest_messages_and_returns_the_rest() {
        let mut store = MessageStore::default();
        let list: Vec<Message> = (0..100).map(|i| message(&format!("m{i}"), i)).collect();
        store.insert("t".into(), list);
        let before = store.revision("t");
        let evicted = store.trim_to_latest("t", 80);
        assert_eq!(evicted.len(), 20);
        assert_eq!(evicted.first().unwrap().id, "m0");
        assert_eq!(evicted.last().unwrap().id, "m19");
        assert_eq!(store.get("t").unwrap().len(), 80);
        assert_eq!(store.get("t").unwrap()[0].id, "m20");
        assert!(store.revision("t") > before);
        let unchanged = store.revision("t");
        assert!(store.trim_to_latest("t", 80).is_empty());
        assert_eq!(store.revision("t"), unchanged);
    }

    #[::core::prelude::v1::test]
    fn only_long_threads_outside_the_kept_set_are_cold() {
        let mut store = MessageStore::default();
        let long = |prefix: &str| -> Vec<Message> {
            (0..81).map(|i| message(&format!("{prefix}{i}"), i)).collect()
        };
        store.insert("selected".into(), long("s"));
        store.insert("recent".into(), long("r"));
        store.insert("cold".into(), long("c"));
        store.insert("short".into(), vec![message("x", 1)]);
        let keep: HashSet<&str> = ["selected", "recent"].into_iter().collect();
        assert_eq!(store.cold_threads(&keep, 80), vec!["cold".to_string()]);
    }

    #[::core::prelude::v1::test]
    fn recent_threads_are_most_recent_first_and_bounded() {
        let mut recent = Vec::new();
        for id in ["a", "b", "c", "a"] {
            remember_recent(&mut recent, id, 3);
        }
        assert_eq!(recent, ids(&["a", "c", "b"]));
        remember_recent(&mut recent, "d", 3);
        assert_eq!(recent, ids(&["d", "a", "c"]));
    }

    #[::core::prelude::v1::test]
    fn list_changes_are_located_precisely() {
        assert_eq!(list_change(&ids(&["a", "b"]), &ids(&["a", "b"])), None);
        assert_eq!(
            list_change(&ids(&["a", "c"]), &ids(&["a", "b", "c"])),
            Some((1..1, 1))
        );
        assert_eq!(
            list_change(&ids(&["a", "b"]), &ids(&["a", "b", "c", "d"])),
            Some((2..2, 2))
        );
        assert_eq!(
            list_change(&ids(&["c", "d"]), &ids(&["a", "b", "c", "d"])),
            Some((0..0, 2))
        );
        assert_eq!(
            list_change(&ids(&["a", "b", "c"]), &ids(&["a", "c"])),
            Some((1..2, 0))
        );
        assert_eq!(
            list_change(&ids(&["a", "a"]), &ids(&["a", "a", "a"])),
            Some((2..2, 1))
        );
        assert_eq!(list_change(&ids(&["a"]), &ids(&["b"])), Some((0..1, 1)));
    }
}
