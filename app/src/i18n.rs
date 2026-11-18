use crate::settings::Language;

pub const FRENCH: &str = "fr";
pub const ENGLISH: &str = "en";

macro_rules! ts {
    ($($all:tt)*) => {
        gpui_kit::SharedString::from(t!($($all)*))
    };
}

macro_rules! tn {
    ($key:literal, $count:expr $(, $name:ident = $value:expr)* $(,)?) => {{
        let count = $count;
        if $crate::i18n::singular(count as i64) {
            t!(concat!($key, ".one"), count = count $(, $name = $value)*)
        } else {
            t!(concat!($key, ".other"), count = count $(, $name = $value)*)
        }
    }};
}

pub fn resolve(language: Language) -> &'static str {
    match language {
        Language::French => FRENCH,
        Language::English => ENGLISH,
        Language::Auto => from_tags(sys_locale::get_locales()),
    }
}

fn from_tags(tags: impl IntoIterator<Item = String>) -> &'static str {
    tags.into_iter()
        .find_map(|tag| {
            let primary = tag
                .split(['-', '_'])
                .next()
                .unwrap_or_default()
                .to_ascii_lowercase();
            match primary.as_str() {
                "fr" => Some(FRENCH),
                "en" => Some(ENGLISH),
                _ => None,
            }
        })
        .unwrap_or(ENGLISH)
}

pub fn apply(language: Language) {
    rust_i18n::set_locale(resolve(language));
}

pub fn current() -> &'static str {
    if &*rust_i18n::locale() == FRENCH {
        FRENCH
    } else {
        ENGLISH
    }
}

pub fn is_french() -> bool {
    current() == FRENCH
}

pub fn singular(count: i64) -> bool {
    let count = count.unsigned_abs();
    if is_french() { count <= 1 } else { count == 1 }
}

#[cfg(test)]
static TEST_LOCK: std::sync::Mutex<()> = std::sync::Mutex::new(());

#[cfg(test)]
pub fn scoped(locale: &str) -> std::sync::MutexGuard<'static, ()> {
    let guard = TEST_LOCK.lock().unwrap_or_else(|poisoned| poisoned.into_inner());
    rust_i18n::set_locale(locale);
    guard
}

#[cfg(test)]
mod tests {
    use std::collections::BTreeSet;
    use std::path::Path;

    use super::{ENGLISH, FRENCH, from_tags, scoped, singular};

    fn keys(locale: &str) -> BTreeSet<String> {
        crate::_rust_i18n_backend()
            .messages_for_locale(locale)
            .unwrap_or_default()
            .into_iter()
            .map(|(key, _)| key.into_owned())
            .collect()
    }

    fn has(locale: &str, key: &str) -> bool {
        crate::_rust_i18n_backend().translate(locale, key).is_some()
    }

    fn sources(dir: &Path, out: &mut Vec<(String, String)>) {
        for entry in std::fs::read_dir(dir).unwrap() {
            let path = entry.unwrap().path();
            if path.is_dir() {
                sources(&path, out);
            } else if path.extension().is_some_and(|ext| ext == "rs") {
                out.push((
                    path.display().to_string(),
                    std::fs::read_to_string(&path).unwrap(),
                ));
            }
        }
    }

    fn literal_keys(text: &str, macro_name: &str) -> Vec<String> {
        let needle = format!("{macro_name}!(\"");
        let mut found = Vec::new();
        let mut rest = text;
        while let Some(start) = rest.find(&needle) {
            let preceded = rest[..start]
                .chars()
                .last()
                .is_some_and(|c| c.is_alphanumeric() || c == '_');
            rest = &rest[start + needle.len()..];
            if preceded {
                continue;
            }
            if let Some(end) = rest.find('"') {
                found.push(rest[..end].to_string());
            }
        }
        found
    }

    #[::core::prelude::v1::test]
    fn every_key_exists_in_both_languages() {
        let english = keys(ENGLISH);
        let french = keys(FRENCH);
        assert!(!english.is_empty());
        let missing_fr: Vec<_> = english.difference(&french).collect();
        let missing_en: Vec<_> = french.difference(&english).collect();
        assert!(missing_fr.is_empty(), "missing in fr: {missing_fr:?}");
        assert!(missing_en.is_empty(), "missing in en: {missing_en:?}");
    }

    #[::core::prelude::v1::test]
    fn every_key_used_in_code_exists() {
        let mut files = Vec::new();
        sources(&Path::new(env!("CARGO_MANIFEST_DIR")).join("src"), &mut files);
        let mut missing = Vec::new();
        for (path, text) in &files {
            for name in ["t", "ts"] {
                for key in literal_keys(text, name) {
                    for locale in [ENGLISH, FRENCH] {
                        if !has(locale, &key) {
                            missing.push(format!("{path}: {locale}.{key}"));
                        }
                    }
                }
            }
            for key in literal_keys(text, "tn") {
                for form in ["one", "other"] {
                    for locale in [ENGLISH, FRENCH] {
                        if !has(locale, &format!("{key}.{form}")) {
                            missing.push(format!("{path}: {locale}.{key}.{form}"));
                        }
                    }
                }
            }
        }
        assert!(missing.is_empty(), "missing keys:\n{}", missing.join("\n"));
    }

    #[::core::prelude::v1::test]
    fn system_tags_pick_a_supported_language() {
        assert_eq!(from_tags(["fr-FR".to_string()]), FRENCH);
        assert_eq!(from_tags(["fr_CA".to_string()]), FRENCH);
        assert_eq!(from_tags(["en-US".to_string()]), ENGLISH);
        assert_eq!(from_tags(["de-DE".to_string(), "fr-BE".to_string()]), FRENCH);
        assert_eq!(from_tags(["de-DE".to_string()]), ENGLISH);
        assert_eq!(from_tags(Vec::<String>::new()), ENGLISH);
    }

    #[::core::prelude::v1::test]
    fn translations_follow_the_current_locale() {
        {
            let _guard = scoped(FRENCH);
            assert_eq!(ts!("common.weekday.mon"), "lundi");
            assert_eq!(t!("common.month_short.feb"), "févr.");
        }
        let _guard = scoped(ENGLISH);
        assert_eq!(ts!("common.weekday.mon"), "Monday");
    }

    #[::core::prelude::v1::test]
    fn plural_rules_follow_the_language() {
        {
            let _guard = scoped(FRENCH);
            assert!(singular(0));
            assert!(singular(1));
            assert!(!singular(2));
        }
        let _guard = scoped(ENGLISH);
        assert!(!singular(0));
        assert!(singular(1));
        assert!(!singular(2));
    }
}
