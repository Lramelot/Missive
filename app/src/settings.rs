use std::{
    collections::HashMap,
    path::{Path, PathBuf},
};

use serde::{Deserialize, Serialize, de::MapAccess};
use serde_json::Value;

pub const SETTINGS_VERSION: u32 = 1;

static SAVES: crate::storage::SaveSlot = crate::storage::SaveSlot::new();

#[derive(Clone, Copy, Debug, Default, PartialEq, Eq, Serialize, Deserialize)]
#[serde(rename_all = "snake_case")]
pub enum ThemePreference {
    Light,
    Dark,
    #[default]
    #[serde(other)]
    System,
}

#[derive(Clone, Copy, Debug, Default, PartialEq, Eq, Serialize, Deserialize)]
#[serde(rename_all = "snake_case")]
pub enum Language {
    French,
    English,
    #[default]
    #[serde(other)]
    Auto,
}

#[derive(Clone, Copy, Debug, Default, PartialEq, Eq, Serialize, Deserialize)]
#[serde(rename_all = "snake_case")]
pub enum TextSize {
    Small,
    Large,
    Larger,
    #[default]
    #[serde(other)]
    Normal,
}

impl TextSize {
    pub fn scale(self) -> f32 {
        match self {
            TextSize::Small => 0.92,
            TextSize::Normal => 1.0,
            TextSize::Large => 1.12,
            TextSize::Larger => 1.25,
        }
    }
}

#[derive(Clone, Debug, Default, PartialEq, Eq, Serialize, Deserialize)]
#[serde(default)]
pub struct Snippet {
    pub shortcut: String,
    pub text: String,
}

#[derive(Clone, Debug, Default, PartialEq, Eq, Serialize, Deserialize)]
#[serde(default)]
pub struct Folder {
    pub id: String,
    pub name: String,
    pub thread_ids: Vec<String>,
}

#[derive(Clone, Debug, PartialEq, Eq, Serialize, Deserialize)]
#[serde(default)]
pub struct QuietHours {
    pub enabled: bool,
    pub start_minutes: u16,
    pub end_minutes: u16,
}

impl Default for QuietHours {
    fn default() -> Self {
        Self {
            enabled: false,
            start_minutes: 22 * 60,
            end_minutes: 7 * 60,
        }
    }
}

#[derive(Clone, Debug, PartialEq, Eq, Serialize, Deserialize)]
#[serde(default)]
pub struct Settings {
    #[serde(default)]
    pub version: u32,
    pub notifications: bool,
    pub notification_preview: bool,
    pub close_to_tray: bool,
    pub launch_at_login: bool,
    pub theme: ThemePreference,
    pub language: Language,
    pub check_updates: bool,
    pub locally_unread: Vec<String>,
    pub locally_archived: HashMap<String, i64>,
    pub send_read_receipts: bool,
    pub send_typing: bool,
    pub keywords: Vec<String>,
    pub quiet_hours: QuietHours,
    pub priority_threads: Vec<String>,
    pub snippets: Vec<Snippet>,
    pub folders: Vec<Folder>,
    pub text_size: TextSize,
    pub compact: bool,
    pub recent_emojis: Vec<String>,
}

impl Default for Settings {
    fn default() -> Self {
        Self {
            version: SETTINGS_VERSION,
            notifications: true,
            notification_preview: true,
            close_to_tray: true,
            launch_at_login: false,
            theme: ThemePreference::System,
            language: Language::Auto,
            check_updates: true,
            locally_unread: Vec::new(),
            locally_archived: HashMap::new(),
            send_read_receipts: true,
            send_typing: true,
            keywords: Vec::new(),
            quiet_hours: QuietHours::default(),
            priority_threads: Vec::new(),
            snippets: Vec::new(),
            folders: Vec::new(),
            text_size: TextSize::Normal,
            compact: false,
            recent_emojis: Vec::new(),
        }
    }
}

fn path() -> Option<PathBuf> {
    let name = if crate::bridge::demo_requested() {
        "settings-demo.json"
    } else {
        "settings.json"
    };
    dirs::data_dir().map(|dir| dir.join("Missive").join(name))
}

struct FieldCollector<'a>(&'a mut Vec<(String, Value)>);

impl<'de> serde::de::Visitor<'de> for FieldCollector<'_> {
    type Value = ();

    fn expecting(&self, formatter: &mut std::fmt::Formatter) -> std::fmt::Result {
        formatter.write_str("an object")
    }

    fn visit_map<A: MapAccess<'de>>(self, mut map: A) -> Result<(), A::Error> {
        while let Some(key) = map.next_key::<String>()? {
            let value = map.next_value::<Value>()?;
            self.0.push((key, value));
        }
        Ok(())
    }
}

fn top_level_fields(text: &str) -> Vec<(String, Value)> {
    let mut fields = Vec::new();
    let mut deserializer = serde_json::Deserializer::from_str(text);
    let _ = serde::Deserializer::deserialize_map(&mut deserializer, FieldCollector(&mut fields));
    fields
}

impl Settings {
    pub fn load() -> Self {
        path()
            .map(|path| Self::load_from(&path))
            .unwrap_or_default()
    }

    pub(crate) fn load_from(path: &Path) -> Self {
        let Ok(bytes) = std::fs::read(path) else {
            return Self::default();
        };
        let mut settings = match serde_json::from_slice::<Settings>(&bytes) {
            Ok(settings) => settings,
            Err(err) => {
                eprintln!(
                    "[settings] unreadable settings ({:?} error, line {}), recovering field by field",
                    err.classify(),
                    err.line()
                );
                crate::storage::keep_corrupt_copy(path);
                let recovered = Self::recover(&String::from_utf8_lossy(&bytes));
                crate::storage::save_path(path, &recovered);
                recovered
            }
        };
        settings.version = SETTINGS_VERSION;
        settings
    }

    pub(crate) fn recover(text: &str) -> Self {
        let Ok(Value::Object(mut current)) = serde_json::to_value(Self::default()) else {
            return Self::default();
        };
        for (key, value) in top_level_fields(text) {
            if !current.contains_key(&key) {
                continue;
            }
            let previous = current.insert(key.clone(), value);
            if serde_json::from_value::<Settings>(Value::Object(current.clone())).is_err() {
                match previous {
                    Some(previous) => current.insert(key, previous),
                    None => current.remove(&key),
                };
            }
        }
        let mut settings: Settings =
            serde_json::from_value(Value::Object(current)).unwrap_or_default();
        settings.version = SETTINGS_VERSION;
        settings
    }

    pub fn save(&self) {
        self.save_ticketed(Self::save_ticket());
    }

    pub fn save_ticket() -> u64 {
        SAVES.ticket()
    }

    pub fn save_ticketed(&self, ticket: u64) {
        if let Some(path) = path() {
            SAVES.save_path(ticket, &path, self);
        }
    }
}

#[cfg(test)]
mod tests {
    use super::{Language, SETTINGS_VERSION, Settings, TextSize, ThemePreference};

    fn temp_dir(name: &str) -> std::path::PathBuf {
        let dir = std::env::temp_dir().join(format!(
            "missive-settings-{name}-{}-{}",
            std::process::id(),
            chrono::Utc::now().timestamp_nanos_opt().unwrap_or_default()
        ));
        std::fs::create_dir_all(&dir).unwrap();
        dir
    }

    #[::core::prelude::v1::test]
    fn unknown_enum_values_fall_back_to_defaults() {
        let settings: Settings =
            serde_json::from_str(r#"{"theme":"sepia","text_size":"huge","compact":true}"#)
                .unwrap();
        assert_eq!(settings.theme, ThemePreference::System);
        assert_eq!(settings.text_size, TextSize::Normal);
        assert!(settings.compact);
    }

    #[::core::prelude::v1::test]
    fn version_is_absent_from_old_files_and_current_when_saved() {
        let old: Settings = serde_json::from_str(r#"{"compact":true}"#).unwrap();
        assert_eq!(old.version, 0);
        let saved = serde_json::to_value(Settings::default()).unwrap();
        assert_eq!(saved["version"], SETTINGS_VERSION);
    }

    #[::core::prelude::v1::test]
    fn broken_fields_are_dropped_and_the_rest_is_kept() {
        let settings = Settings::recover(
            r#"{"theme":"dark","notifications":"yes","keywords":["urgent"],"quiet_hours":{"enabled":"x"},"compact":true}"#,
        );
        assert_eq!(settings.theme, ThemePreference::Dark);
        assert!(settings.notifications);
        assert_eq!(settings.keywords, vec!["urgent".to_string()]);
        assert!(!settings.quiet_hours.enabled);
        assert!(settings.compact);
        assert_eq!(settings.version, SETTINGS_VERSION);
    }

    #[::core::prelude::v1::test]
    fn truncated_file_keeps_the_fields_read_before_the_cut() {
        let settings = Settings::recover(r#"{"theme":"light","send_typing":false,"keywords":["a","#);
        assert_eq!(settings.theme, ThemePreference::Light);
        assert!(!settings.send_typing);
        assert!(settings.keywords.is_empty());
    }

    #[::core::prelude::v1::test]
    fn corrupt_file_is_copied_recovered_and_rewritten() {
        let dir = temp_dir("corrupt");
        let path = dir.join("settings.json");
        let broken = r#"{"theme":"dark","close_to_tray":false,"text_size":12,"#;
        std::fs::write(&path, broken).unwrap();

        let settings = Settings::load_from(&path);
        assert_eq!(settings.theme, ThemePreference::Dark);
        assert!(!settings.close_to_tray);
        assert_eq!(settings.text_size, TextSize::Normal);

        let copies: Vec<_> = std::fs::read_dir(&dir)
            .unwrap()
            .filter_map(Result::ok)
            .filter(|entry| entry.file_name().to_string_lossy().contains("corrupt-"))
            .collect();
        assert_eq!(copies.len(), 1);
        assert_eq!(std::fs::read_to_string(copies[0].path()).unwrap(), broken);

        let rewritten: Settings =
            serde_json::from_str(&std::fs::read_to_string(&path).unwrap()).unwrap();
        assert_eq!(rewritten.theme, ThemePreference::Dark);
        assert!(!rewritten.close_to_tray);
        let _ = std::fs::remove_dir_all(dir);
    }

    #[::core::prelude::v1::test]
    fn valid_file_loads_without_copy() {
        let dir = temp_dir("valid");
        let path = dir.join("settings.json");
        std::fs::write(&path, r#"{"theme":"light"}"#).unwrap();
        let settings = Settings::load_from(&path);
        assert_eq!(settings.theme, ThemePreference::Light);
        assert_eq!(settings.version, SETTINGS_VERSION);
        assert_eq!(std::fs::read_dir(&dir).unwrap().count(), 1);
        let _ = std::fs::remove_dir_all(dir);
    }

    #[::core::prelude::v1::test]
    fn missing_fields_fall_back_to_defaults() {
        let settings: Settings = serde_json::from_str(r#"{"theme":"dark"}"#).unwrap();
        assert_eq!(settings.theme, ThemePreference::Dark);
        assert_eq!(settings.language, Language::Auto);
        assert!(settings.notifications);
        assert!(settings.close_to_tray);
        assert!(settings.locally_unread.is_empty());
        assert!(settings.locally_archived.is_empty());
    }

    #[::core::prelude::v1::test]
    fn local_thread_state_round_trips() {
        let mut settings = Settings::default();
        settings.locally_unread.push("t1".into());
        settings.locally_archived.insert("t2".into(), 42);
        let text = serde_json::to_string(&settings).unwrap();
        let back: Settings = serde_json::from_str(&text).unwrap();
        assert_eq!(back, settings);
    }
}
