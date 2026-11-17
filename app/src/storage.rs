use std::path::PathBuf;

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

pub fn save_json<T: Serialize>(name: &str, value: &T) {
    let Some(path) = data_file(name) else { return };
    save_path(&path, value);
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
