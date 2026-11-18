use std::{collections::HashMap, path::PathBuf};

use serde::{Deserialize, Serialize};

#[derive(Clone, Copy, Debug, Default, PartialEq, Eq, Serialize, Deserialize)]
#[serde(rename_all = "snake_case")]
pub enum ThemePreference {
    #[default]
    System,
    Light,
    Dark,
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
    #[default]
    Normal,
    Large,
    Larger,
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

impl Settings {
    pub fn load() -> Self {
        path()
            .map(|path| crate::storage::load_path(&path))
            .unwrap_or_default()
    }

    pub fn save(&self) {
        if let Some(path) = path() {
            crate::storage::save_path(&path, self);
        }
    }
}

#[cfg(test)]
mod tests {
    use super::{Language, Settings, ThemePreference};

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
