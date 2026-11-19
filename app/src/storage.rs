use std::{
    path::{Path, PathBuf},
    sync::{
        Mutex, PoisonError,
        atomic::{AtomicU64, Ordering},
    },
    time::{Duration, SystemTime},
};

use serde::{Serialize, de::DeserializeOwned};

pub fn data_file(name: &str) -> Option<PathBuf> {
    let name = if crate::bridge::demo_requested() {
        match name.rsplit_once('.') {
            Some((stem, ext)) => format!("{stem}-demo.{ext}"),
            None => format!("{name}-demo"),
        }
    } else {
        name.to_string()
    };
    dirs::data_dir().map(|dir| dir.join("Missive").join(name))
}

fn file_label(path: &std::path::Path) -> String {
    path.file_name()
        .map(|name| name.to_string_lossy().into_owned())
        .unwrap_or_default()
}

pub fn load_json<T: DeserializeOwned + Default>(name: &str) -> T {
    data_file(name).map(|path| load_path(&path)).unwrap_or_default()
}

pub fn load_path<T: DeserializeOwned + Default>(path: &std::path::Path) -> T {
    let Ok(text) = std::fs::read_to_string(path) else {
        return T::default();
    };
    match serde_json::from_str(&text) {
        Ok(value) => value,
        Err(err) => {
            eprintln!("[storage] unreadable {}: {err}", file_label(path));
            let stamp = chrono::Utc::now().format("%Y%m%d-%H%M%S");
            let _ = std::fs::rename(path, path.with_extension(format!("corrupt-{stamp}")));
            T::default()
        }
    }
}

pub fn keep_corrupt_copy(path: &std::path::Path) -> Option<PathBuf> {
    let stamp = chrono::Utc::now().format("%Y%m%d-%H%M%S");
    let copy = path.with_extension(format!("corrupt-{stamp}"));
    match std::fs::copy(path, &copy) {
        Ok(_) => Some(copy),
        Err(err) => {
            eprintln!("[storage] cannot keep a copy of {}: {err}", file_label(path));
            None
        }
    }
}

pub fn save_json<T: Serialize>(name: &str, value: &T) {
    let Some(path) = data_file(name) else { return };
    save_path(&path, value);
}

pub struct SaveSlot {
    issued: AtomicU64,
    written: Mutex<u64>,
}

impl SaveSlot {
    pub const fn new() -> Self {
        Self {
            issued: AtomicU64::new(0),
            written: Mutex::new(0),
        }
    }

    pub fn ticket(&self) -> u64 {
        self.issued.fetch_add(1, Ordering::SeqCst) + 1
    }

    pub fn save_path<T: Serialize>(&self, ticket: u64, path: &std::path::Path, value: &T) -> bool {
        let mut written = self.written.lock().unwrap_or_else(PoisonError::into_inner);
        if ticket <= *written {
            return false;
        }
        save_path(path, value);
        *written = ticket;
        true
    }

    pub fn save_json<T: Serialize>(&self, ticket: u64, name: &str, value: &T) -> bool {
        match data_file(name) {
            Some(path) => self.save_path(ticket, &path, value),
            None => false,
        }
    }
}

pub fn remove_files_older_than(dir: &Path, max_age: Duration, now: SystemTime) -> usize {
    let Ok(entries) = std::fs::read_dir(dir) else {
        return 0;
    };
    let mut removed = 0;
    for entry in entries.flatten() {
        let Ok(metadata) = entry.metadata() else {
            continue;
        };
        if !metadata.is_file() {
            continue;
        }
        let expired = metadata
            .modified()
            .ok()
            .and_then(|modified| now.duration_since(modified).ok())
            .is_some_and(|age| age > max_age);
        if expired && std::fs::remove_file(entry.path()).is_ok() {
            removed += 1;
        }
    }
    removed
}

pub fn save_path<T: Serialize>(path: &std::path::Path, value: &T) {
    let name = file_label(path);
    if let Some(dir) = path.parent() {
        let _ = std::fs::create_dir_all(dir);
    }
    let Ok(text) = serde_json::to_string_pretty(value) else {
        eprintln!("[storage] cannot serialize {name}");
        return;
    };
    let temp = path.with_extension("tmp");
    match std::fs::write(&temp, text) {
        Ok(()) => {
            if let Err(err) = std::fs::rename(&temp, path) {
                eprintln!("[storage] cannot save {name}: {err}");
                let _ = std::fs::remove_file(&temp);
            }
        }
        Err(err) => {
            eprintln!("[storage] cannot write {name}: {err}");
            let _ = std::fs::remove_file(&temp);
        }
    }
}

#[cfg(test)]
mod tests {
    use super::{SaveSlot, keep_corrupt_copy, remove_files_older_than};
    use std::time::{Duration, SystemTime};

    fn temp_dir(name: &str) -> std::path::PathBuf {
        let dir = std::env::temp_dir().join(format!(
            "missive-storage-{name}-{}-{}",
            std::process::id(),
            chrono::Utc::now().timestamp_nanos_opt().unwrap_or_default()
        ));
        std::fs::create_dir_all(&dir).unwrap();
        dir
    }

    #[::core::prelude::v1::test]
    fn an_older_save_never_overwrites_a_newer_one() {
        let dir = temp_dir("slot");
        let path = dir.join("value.json");
        let slot = SaveSlot::new();
        let first = slot.ticket();
        let second = slot.ticket();
        assert!(slot.save_path(second, &path, &"new"));
        assert!(!slot.save_path(first, &path, &"old"));
        assert_eq!(std::fs::read_to_string(&path).unwrap(), "\"new\"");
        let third = slot.ticket();
        assert!(slot.save_path(third, &path, &"newest"));
        assert_eq!(std::fs::read_to_string(&path).unwrap(), "\"newest\"");
        let _ = std::fs::remove_dir_all(dir);
    }

    #[::core::prelude::v1::test]
    fn a_corrupt_file_is_copied_and_left_in_place() {
        let dir = temp_dir("corrupt");
        let path = dir.join("settings.json");
        std::fs::write(&path, b"{ broken").unwrap();
        let copy = keep_corrupt_copy(&path).unwrap();
        assert_eq!(std::fs::read(&path).unwrap(), b"{ broken");
        assert_eq!(std::fs::read(&copy).unwrap(), b"{ broken");
        assert!(copy
            .extension()
            .is_some_and(|ext| ext.to_string_lossy().starts_with("corrupt-")));
        let _ = std::fs::remove_dir_all(dir);
    }

    #[::core::prelude::v1::test]
    fn only_files_older_than_the_limit_are_removed() {
        let dir = temp_dir("prune");
        let now = SystemTime::now();
        let old = dir.join("old.png");
        let fresh = dir.join("fresh.png");
        std::fs::write(&old, b"x").unwrap();
        std::fs::write(&fresh, b"y").unwrap();
        std::fs::create_dir_all(dir.join("nested")).unwrap();
        std::fs::File::options()
            .write(true)
            .open(&old)
            .unwrap()
            .set_modified(now - Duration::from_secs(8 * 24 * 3600))
            .unwrap();
        std::fs::File::options()
            .write(true)
            .open(&fresh)
            .unwrap()
            .set_modified(now - Duration::from_secs(6 * 24 * 3600))
            .unwrap();
        let removed = remove_files_older_than(&dir, Duration::from_secs(7 * 24 * 3600), now);
        assert_eq!(removed, 1);
        assert!(!old.exists());
        assert!(fresh.exists());
        assert!(dir.join("nested").exists());
        let _ = std::fs::remove_dir_all(dir);
    }
}
