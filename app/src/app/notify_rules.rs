use chrono::Timelike as _;

use super::MissiveApp;
use crate::model::{Message, Thread};

#[derive(Clone, Copy, Default)]
struct NotifyFacts {
    request: bool,
    muted: bool,
    mentioned: bool,
    keyword: bool,
    priority: bool,
    quiet: bool,
}

fn decide(facts: NotifyFacts) -> bool {
    if facts.request {
        return false;
    }
    let breaks_through = facts.mentioned || facts.keyword;
    if facts.muted && !breaks_through {
        return false;
    }
    if facts.quiet {
        return facts.priority || breaks_through;
    }
    true
}

pub(crate) fn in_quiet_window(start: u16, end: u16, now: u16) -> bool {
    if start == end {
        return false;
    }
    if start < end {
        (start..end).contains(&now)
    } else {
        now >= start || now < end
    }
}

pub(crate) fn fold(text: &str) -> String {
    let mut out = String::with_capacity(text.len());
    for c in text.chars() {
        for lower in c.to_lowercase() {
            match lower {
                'à' | 'á' | 'â' | 'ã' | 'ä' | 'å' | 'ā' | 'ă' | 'ą' => out.push('a'),
                'ç' | 'ć' | 'č' => out.push('c'),
                'ď' => out.push('d'),
                'è' | 'é' | 'ê' | 'ë' | 'ē' | 'ė' | 'ę' | 'ě' => out.push('e'),
                'ì' | 'í' | 'î' | 'ï' | 'ī' | 'į' => out.push('i'),
                'ł' => out.push('l'),
                'ñ' | 'ń' | 'ň' => out.push('n'),
                'ò' | 'ó' | 'ô' | 'õ' | 'ö' | 'ø' | 'ō' | 'ő' => out.push('o'),
                'ř' => out.push('r'),
                'ś' | 'š' | 'ş' => out.push('s'),
                'ť' | 'ţ' => out.push('t'),
                'ù' | 'ú' | 'û' | 'ü' | 'ū' | 'ů' | 'ű' => out.push('u'),
                'ý' | 'ÿ' => out.push('y'),
                'ź' | 'ż' | 'ž' => out.push('z'),
                'œ' => out.push_str("oe"),
                'æ' => out.push_str("ae"),
                'ß' => out.push_str("ss"),
                other => out.push(other),
            }
        }
    }
    out
}

fn words(text: &str) -> Vec<String> {
    fold(text)
        .split(|c: char| !c.is_alphanumeric())
        .filter(|word| !word.is_empty())
        .map(str::to_string)
        .collect()
}

pub(crate) fn keyword_matches(keywords: &[String], text: &str) -> bool {
    if keywords.is_empty() || text.trim().is_empty() {
        return false;
    }
    let haystack = words(text);
    keywords.iter().any(|keyword| {
        let needle = words(keyword);
        !needle.is_empty()
            && haystack
                .windows(needle.len())
                .any(|window| window == needle.as_slice())
    })
}

impl MissiveApp {
    pub(crate) fn keywords_equivalent(a: &str, b: &str) -> bool {
        let left = words(a);
        !left.is_empty() && left == words(b)
    }

    pub(crate) fn should_notify(&self, message: &Message, thread: Option<&Thread>) -> bool {
        let quiet = &self.settings.quiet_hours;
        let now = chrono::Local::now();
        let minutes = (now.hour() * 60 + now.minute()) as u16;
        let mut text = message.text.clone();
        if let Some(poll) = &message.poll {
            text.push(' ');
            text.push_str(&poll.question);
        }
        decide(NotifyFacts {
            request: thread.is_none_or(|thread| self.is_request_thread(thread)),
            muted: thread.is_some_and(|thread| self.thread_is_muted(thread)),
            mentioned: self.mentions_me(message),
            keyword: keyword_matches(&self.settings.keywords, &text),
            priority: self
                .settings
                .priority_threads
                .iter()
                .any(|id| *id == message.thread_id),
            quiet: quiet.enabled
                && in_quiet_window(quiet.start_minutes, quiet.end_minutes, minutes),
        })
    }
}

#[cfg(test)]
mod tests {
    use super::{NotifyFacts, decide, fold, in_quiet_window, keyword_matches};

    fn kw(list: &[&str]) -> Vec<String> {
        list.iter().map(|s| s.to_string()).collect()
    }

    #[::core::prelude::v1::test]
    fn requests_never_notify() {
        let facts = NotifyFacts {
            request: true,
            mentioned: true,
            keyword: true,
            priority: true,
            ..Default::default()
        };
        assert!(!decide(facts));
    }

    #[::core::prelude::v1::test]
    fn plain_message_notifies() {
        assert!(decide(NotifyFacts::default()));
    }

    #[::core::prelude::v1::test]
    fn muted_only_breaks_through_on_mention_or_keyword() {
        let muted = NotifyFacts {
            muted: true,
            ..Default::default()
        };
        assert!(!decide(muted));
        assert!(!decide(NotifyFacts {
            priority: true,
            ..muted
        }));
        assert!(decide(NotifyFacts {
            mentioned: true,
            ..muted
        }));
        assert!(decide(NotifyFacts {
            keyword: true,
            ..muted
        }));
    }

    #[::core::prelude::v1::test]
    fn quiet_hours_allow_priority_mention_keyword() {
        let quiet = NotifyFacts {
            quiet: true,
            ..Default::default()
        };
        assert!(!decide(quiet));
        assert!(decide(NotifyFacts {
            priority: true,
            ..quiet
        }));
        assert!(decide(NotifyFacts {
            mentioned: true,
            ..quiet
        }));
        assert!(decide(NotifyFacts {
            keyword: true,
            ..quiet
        }));
        assert!(!decide(NotifyFacts {
            priority: true,
            muted: true,
            ..quiet
        }));
    }

    #[::core::prelude::v1::test]
    fn quiet_window_wraps_past_midnight() {
        let (start, end) = (22 * 60, 7 * 60);
        assert!(in_quiet_window(start, end, 22 * 60));
        assert!(in_quiet_window(start, end, 23 * 60 + 59));
        assert!(in_quiet_window(start, end, 0));
        assert!(in_quiet_window(start, end, 6 * 60 + 59));
        assert!(!in_quiet_window(start, end, 7 * 60));
        assert!(!in_quiet_window(start, end, 12 * 60));
        assert!(!in_quiet_window(start, end, 21 * 60 + 59));
    }

    #[::core::prelude::v1::test]
    fn quiet_window_same_day_and_empty() {
        assert!(in_quiet_window(13 * 60, 14 * 60, 13 * 60 + 30));
        assert!(!in_quiet_window(13 * 60, 14 * 60, 14 * 60));
        assert!(!in_quiet_window(13 * 60, 14 * 60, 12 * 60));
        assert!(!in_quiet_window(600, 600, 600));
    }

    #[::core::prelude::v1::test]
    fn fold_removes_case_and_accents() {
        assert_eq!(fold("ÉTÉ Garçon Œuvre"), "ete garcon oeuvre");
    }

    #[::core::prelude::v1::test]
    fn keyword_is_case_and_accent_insensitive() {
        assert!(keyword_matches(&kw(&["urgent"]), "C'est URGENT !"));
        assert!(keyword_matches(&kw(&["réunion"]), "la reunion est déplacée"));
        assert!(keyword_matches(&kw(&["reunion"]), "La Réunion de demain"));
    }

    #[::core::prelude::v1::test]
    fn keyword_matches_whole_words_only() {
        assert!(!keyword_matches(&kw(&["art"]), "le départ est à 8h"));
        assert!(!keyword_matches(&kw(&["chat"]), "les chatons"));
        assert!(keyword_matches(&kw(&["chat"]), "mon chat, malade"));
    }

    #[::core::prelude::v1::test]
    fn keyword_phrase_matches_consecutive_words() {
        let list = kw(&["appel d'offres"]);
        assert!(keyword_matches(&list, "Nouvel Appel d’offres publié"));
        assert!(!keyword_matches(&list, "appel pour des offres"));
    }

    #[::core::prelude::v1::test]
    fn equivalent_keywords_ignore_case_accents_and_spacing() {
        assert!(super::MissiveApp::keywords_equivalent("Réunion  d'équipe", "reunion d’EQUIPE"));
        assert!(!super::MissiveApp::keywords_equivalent("réunion", "réunions"));
        assert!(!super::MissiveApp::keywords_equivalent("!!", "??"));
    }

    #[::core::prelude::v1::test]
    fn empty_keywords_never_match() {
        assert!(!keyword_matches(&[], "urgent"));
        assert!(!keyword_matches(&kw(&["", "  ", "!!"]), "urgent !!"));
        assert!(!keyword_matches(&kw(&["urgent"]), ""));
    }
}
