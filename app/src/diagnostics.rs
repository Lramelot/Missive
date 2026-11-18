#![cfg_attr(not(windows), allow(dead_code))]

use std::{
    fs::{self, File, OpenOptions},
    io::{self, BufRead, BufReader, Read, Write},
    path::{Path, PathBuf},
    sync::{
        Condvar, Mutex,
        atomic::{AtomicBool, Ordering},
    },
    time::Duration,
};

use chrono::{DateTime, Utc};
use serde_json::Value;

const MAX_BYTES: u64 = 2 * 1024 * 1024;
const KEPT_FILES: usize = 3;
const PANIC_MESSAGE_BYTES: usize = 2048;
const MAX_READ_FAILURES: u32 = 50;
const READ_RETRY_DELAY: Duration = Duration::from_millis(100);
const FLUSH_TIMEOUT: Duration = Duration::from_millis(500);
const FLUSH_MARKER: &[u8] = b"\x01missive-log-flush\n";
pub const REPORT_LINES: usize = 200;

pub fn log_dir() -> Option<PathBuf> {
    dirs::data_local_dir().map(|dir| dir.join("Missive").join("logs"))
}

fn base_name() -> &'static str {
    if crate::bridge::demo_requested() {
        "missive-demo"
    } else {
        "missive"
    }
}

fn rotated_path(dir: &Path, base: &str, index: usize) -> PathBuf {
    if index == 0 {
        dir.join(format!("{base}.log"))
    } else {
        dir.join(format!("{base}.{index}.log"))
    }
}

fn rotate(dir: &Path, base: &str, kept: usize) -> io::Result<()> {
    if kept == 0 {
        return Ok(());
    }
    match fs::remove_file(rotated_path(dir, base, kept - 1)) {
        Ok(()) => {}
        Err(err) if err.kind() == io::ErrorKind::NotFound => {}
        Err(err) => return Err(err),
    }
    for index in (0..kept - 1).rev() {
        let from = rotated_path(dir, base, index);
        if from.exists() {
            fs::rename(&from, rotated_path(dir, base, index + 1))?;
        }
    }
    Ok(())
}

fn stamp(line: &str, now: DateTime<Utc>) -> String {
    format!("{} {line}", now.format("%Y-%m-%dT%H:%M:%S%.3fZ"))
}

fn capped(text: &str, max_bytes: usize) -> &str {
    if text.len() <= max_bytes {
        return text;
    }
    let mut end = max_bytes;
    while !text.is_char_boundary(end) {
        end -= 1;
    }
    &text[..end]
}

fn panic_line(thread: &str, location: &str, message: &str) -> String {
    let message = capped(message, PANIC_MESSAGE_BYTES).replace(['\r', '\n'], " ");
    format!("[panic] thread '{thread}' at {location}: {message}")
}

const RESTART_ARG: &str = "--restarted-after";
const RESTART_COOLDOWN_SECS: u64 = 60;
const RESTART_WAIT: Duration = Duration::from_secs(15);

pub fn was_restarted() -> bool {
    std::env::args().any(|arg| arg == RESTART_ARG)
}

fn crashed_pid(args: impl IntoIterator<Item = String>) -> Option<u32> {
    let mut args = args.into_iter();
    while let Some(arg) = args.next() {
        if arg == RESTART_ARG {
            return args.next().and_then(|pid| pid.parse().ok());
        }
    }
    None
}

fn restart_allowed(last: Option<&str>, now_secs: u64) -> bool {
    last.and_then(|text| text.trim().parse::<u64>().ok())
        .is_none_or(|last| now_secs.saturating_sub(last) >= RESTART_COOLDOWN_SECS)
}

fn relaunch_args(args: impl IntoIterator<Item = String>, pid: u32) -> Vec<String> {
    let mut kept: Vec<String> = args.into_iter().filter(|arg| arg == "--demo").collect();
    kept.push(RESTART_ARG.to_string());
    kept.push(pid.to_string());
    kept
}

fn relaunch_after_crash(marker: &Path) {
    if !cfg!(panic = "abort") {
        return;
    }
    let now = std::time::SystemTime::now()
        .duration_since(std::time::UNIX_EPOCH)
        .map(|elapsed| elapsed.as_secs())
        .unwrap_or(0);
    if !restart_allowed(fs::read_to_string(marker).ok().as_deref(), now) {
        return;
    }
    let _ = fs::write(marker, now.to_string());
    let Ok(exe) = std::env::current_exe() else {
        return;
    };
    let _ = std::process::Command::new(exe)
        .args(relaunch_args(std::env::args().skip(1), std::process::id()))
        .spawn();
}

pub fn wait_for_crashed_instance() {
    let Some(pid) = crashed_pid(std::env::args().skip(1)) else {
        return;
    };
    wait_for_exit(pid);
    std::thread::sleep(Duration::from_millis(800));
}

#[cfg(windows)]
fn wait_for_exit(pid: u32) {
    use windows_sys::Win32::Foundation::CloseHandle;
    use windows_sys::Win32::System::Threading::{OpenProcess, PROCESS_SYNCHRONIZE, WaitForSingleObject};
    unsafe {
        let handle = OpenProcess(PROCESS_SYNCHRONIZE, 0, pid);
        if !handle.is_null() {
            WaitForSingleObject(handle, RESTART_WAIT.as_millis() as u32);
            CloseHandle(handle);
        }
    }
}

#[cfg(not(windows))]
fn wait_for_exit(_: u32) {
    std::thread::sleep(Duration::from_secs(2));
}

fn install_panic_hook(path: PathBuf) {
    let marker = path.with_file_name(format!("{}-restart.txt", base_name()));
    let previous = std::panic::take_hook();
    std::panic::set_hook(Box::new(move |info| {
        let thread = std::thread::current();
        let location = info
            .location()
            .map(|location| location.to_string())
            .unwrap_or_else(|| "unknown".to_string());
        let message = info.payload_as_str().unwrap_or("non-string payload");
        let line = stamp(
            &panic_line(thread.name().unwrap_or("unnamed"), &location, message),
            Utc::now(),
        );
        if let Ok(mut file) = OpenOptions::new().create(true).append(true).open(&path) {
            let _ = file
                .write_all(line.as_bytes())
                .and_then(|_| file.write_all(b"\n"))
                .and_then(|_| file.flush());
        }
        previous(info);
        relaunch_after_crash(&marker);
    }));
}

struct RotatingLog {
    dir: PathBuf,
    base: &'static str,
    limit: u64,
    kept: usize,
    file: Option<File>,
    size: u64,
    rotation_failed: bool,
}

impl RotatingLog {
    fn open(dir: PathBuf, base: &'static str, limit: u64, kept: usize) -> Self {
        let mut log = Self {
            dir,
            base,
            limit,
            kept,
            file: None,
            size: 0,
            rotation_failed: false,
        };
        log.reopen();
        log
    }

    fn truncate(&mut self) {
        self.file = None;
        let _ = OpenOptions::new()
            .create(true)
            .write(true)
            .truncate(true)
            .open(rotated_path(&self.dir, self.base, 0));
        self.reopen();
        self.append(&stamp(
            "[log] rotation impossible, journal tronqué",
            Utc::now(),
        ));
    }

    fn append(&mut self, line: &str) {
        if let Some(file) = self.file.as_mut() {
            if file
                .write_all(line.as_bytes())
                .and_then(|_| file.write_all(b"\n"))
                .is_ok()
            {
                self.size += line.len() as u64 + 1;
            }
        }
    }

    fn reopen(&mut self) {
        self.file = fs::create_dir_all(&self.dir)
            .and_then(|_| {
                OpenOptions::new()
                    .create(true)
                    .append(true)
                    .open(rotated_path(&self.dir, self.base, 0))
            })
            .ok();
        self.size = self
            .file
            .as_ref()
            .and_then(|file| file.metadata().ok())
            .map(|meta| meta.len())
            .unwrap_or(0);
    }

    fn write_line(&mut self, line: &str) {
        let bytes = line.len() as u64 + 1;
        if self.size > 0 && self.size + bytes > self.limit {
            if !self.rotation_failed {
                self.file = None;
                if rotate(&self.dir, self.base, self.kept).is_err() {
                    self.rotation_failed = true;
                }
                self.reopen();
            }
            if self.rotation_failed && self.size + bytes > self.limit.saturating_mul(2) {
                self.truncate();
            }
        }
        self.append(line);
    }
}

static REDIRECTED: AtomicBool = AtomicBool::new(false);
static FLUSHED: Mutex<u64> = Mutex::new(0);
static FLUSH_SIGNAL: Condvar = Condvar::new();

fn note_flush() {
    let mut seen = FLUSHED.lock().unwrap_or_else(|err| err.into_inner());
    *seen += 1;
    FLUSH_SIGNAL.notify_all();
}

fn pump(reader: impl Read, log: &mut RotatingLog, mut echo: impl FnMut(&[u8])) {
    let mut reader = BufReader::new(reader);
    let mut buffer = Vec::new();
    let mut failures = 0;
    loop {
        buffer.clear();
        match reader.read_until(b'\n', &mut buffer) {
            Ok(0) => return,
            Ok(_) => failures = 0,
            Err(err) if err.kind() == io::ErrorKind::Interrupted => continue,
            Err(_) => {
                failures += 1;
                if failures >= MAX_READ_FAILURES {
                    return;
                }
                std::thread::sleep(READ_RETRY_DELAY);
                continue;
            }
        }
        if buffer == FLUSH_MARKER {
            note_flush();
            continue;
        }
        echo(&buffer);
        let text = String::from_utf8_lossy(&buffer);
        log.write_line(&stamp(text.trim_end_matches(['\r', '\n']), Utc::now()));
    }
}

pub fn flush() {
    if !REDIRECTED.load(Ordering::Acquire) {
        return;
    }
    let before = *FLUSHED.lock().unwrap_or_else(|err| err.into_inner());
    if io::stderr().write_all(FLUSH_MARKER).is_err() {
        return;
    }
    let seen = FLUSHED.lock().unwrap_or_else(|err| err.into_inner());
    let _ = FLUSH_SIGNAL.wait_timeout_while(seen, FLUSH_TIMEOUT, |seen| *seen <= before);
}

#[cfg(windows)]
pub fn init() {
    use std::mem::ManuallyDrop;
    use std::os::windows::io::{FromRawHandle, IntoRawHandle, RawHandle};
    use windows_sys::Win32::Foundation::INVALID_HANDLE_VALUE;
    use windows_sys::Win32::System::Console::{GetStdHandle, STD_ERROR_HANDLE, SetStdHandle};

    let Some(dir) = log_dir() else { return };
    install_panic_hook(rotated_path(&dir, base_name(), 0));
    let Ok((mut reader, writer)) = io::pipe() else {
        return;
    };
    let previous = unsafe { GetStdHandle(STD_ERROR_HANDLE) };
    let original = previous as isize;
    let is_console = !previous.is_null()
        && previous != INVALID_HANDLE_VALUE
        && unsafe { windows_sys::Win32::Storage::FileSystem::GetFileType(previous) }
            == windows_sys::Win32::Storage::FileSystem::FILE_TYPE_CHAR;
    let echo_handle = is_console.then_some(original);
    let mut log = RotatingLog::open(dir, base_name(), MAX_BYTES, KEPT_FILES);
    let spawned = std::thread::Builder::new()
        .name("log-writer".into())
        .spawn(move || {
            pump(&mut reader, &mut log, |bytes| {
                if let Some(handle) = echo_handle {
                    let mut out =
                        ManuallyDrop::new(unsafe { File::from_raw_handle(handle as RawHandle) });
                    let _ = out.write_all(bytes);
                }
            });
            unsafe { SetStdHandle(STD_ERROR_HANDLE, original as _) };
            std::mem::forget(reader);
            REDIRECTED.store(false, Ordering::Release);
            note_flush();
            log.append(&stamp("[log] capture de stderr arrêtée", Utc::now()));
        });
    if spawned.is_err() {
        return;
    }
    let handle = writer.into_raw_handle();
    if unsafe { SetStdHandle(STD_ERROR_HANDLE, handle as _) } != 0 {
        REDIRECTED.store(true, Ordering::Release);
    }
}

#[cfg(not(windows))]
pub fn init() {}

fn last_lines(newest_first: &[String], limit: usize) -> Vec<String> {
    let mut lines: Vec<String> = Vec::new();
    for text in newest_first {
        let mut chunk: Vec<String> = text.lines().map(str::to_string).collect();
        chunk.append(&mut lines);
        lines = chunk;
        if lines.len() >= limit {
            break;
        }
    }
    let skip = lines.len().saturating_sub(limit);
    lines.split_off(skip)
}

pub fn recent_lines(limit: usize) -> Vec<String> {
    let Some(dir) = log_dir() else {
        return Vec::new();
    };
    let texts: Vec<String> = (0..KEPT_FILES)
        .filter_map(|index| fs::read(rotated_path(&dir, base_name(), index)).ok())
        .map(|bytes| String::from_utf8_lossy(&bytes).into_owned())
        .collect();
    last_lines(&texts, limit)
}

#[cfg(windows)]
pub fn os_description() -> String {
    use winreg::{RegKey, enums::HKEY_LOCAL_MACHINE};
    let key = RegKey::predef(HKEY_LOCAL_MACHINE)
        .open_subkey(r"SOFTWARE\Microsoft\Windows NT\CurrentVersion")
        .ok();
    let text = |name: &str| {
        key.as_ref()
            .and_then(|key| key.get_value::<String, _>(name).ok())
            .filter(|value| !value.trim().is_empty())
    };
    let ubr = key
        .as_ref()
        .and_then(|key| key.get_value::<u32, _>("UBR").ok());
    windows_label(
        text("CurrentBuildNumber").or_else(|| text("CurrentBuild")),
        ubr,
        text("DisplayVersion"),
        text("EditionID"),
    )
}

#[cfg(not(windows))]
pub fn os_description() -> String {
    format!("{} {}", std::env::consts::OS, std::env::consts::ARCH)
}

fn windows_label(
    build: Option<String>,
    ubr: Option<u32>,
    display: Option<String>,
    edition: Option<String>,
) -> String {
    let number = build.as_deref().and_then(|b| b.parse::<u32>().ok());
    let family = match number {
        Some(n) if n >= 22000 => "Windows 11",
        Some(_) => "Windows 10",
        None => "Windows",
    };
    let mut label = family.to_string();
    for part in [edition, display].into_iter().flatten() {
        label.push(' ');
        label.push_str(&part);
    }
    if let Some(build) = build {
        label.push_str(&format!(" (build {build}"));
        if let Some(ubr) = ubr {
            label.push_str(&format!(".{ubr}"));
        }
        label.push(')');
    }
    label.push(' ');
    label.push_str(std::env::consts::ARCH);
    label
}

pub struct ReportInput {
    pub version: String,
    pub installed: bool,
    pub demo: bool,
    pub os: String,
    pub link: String,
    pub bridge: Result<Value, String>,
    pub lines: Vec<String>,
}

pub fn compose_report(input: &ReportInput, now: DateTime<Utc>) -> String {
    let demo = if input.demo {
        t!("diagnostics.report.yes")
    } else {
        t!("diagnostics.report.no")
    };
    let bridge = match &input.bridge {
        Ok(value) => serde_json::to_string_pretty(value).unwrap_or_else(|_| value.to_string()),
        Err(reason) => t!("diagnostics.report.bridge_unavailable", reason = reason).into_owned(),
    };
    let installation = if input.installed {
        String::new()
    } else {
        format!(" {}", t!("diagnostics.report.not_installed"))
    };
    let mut report = format!(
        "{}\n\n{}\n{bridge}\n\n{}\n",
        t!(
            "diagnostics.report.header",
            generated = now.format("%Y-%m-%dT%H:%M:%SZ"),
            version = input.version,
            installation = installation,
            system = input.os,
            demo = demo,
            link = input.link,
        ),
        t!("diagnostics.report.bridge_title"),
        t!("diagnostics.report.log_title", count = input.lines.len()),
    );
    for line in &input.lines {
        report.push_str(line);
        report.push('\n');
    }
    report
}

#[cfg(test)]
mod tests {
    use super::{
        FLUSH_MARKER, ReportInput, RotatingLog, compose_report, last_lines, panic_line, pump,
        rotate, rotated_path, stamp, windows_label,
    };
    use std::io::{self, Read};

    struct Flaky {
        failures: u32,
        data: &'static [u8],
    }

    impl Read for Flaky {
        fn read(&mut self, buf: &mut [u8]) -> io::Result<usize> {
            if self.failures > 0 {
                self.failures -= 1;
                return Err(io::Error::other("transient"));
            }
            let n = self.data.len().min(buf.len());
            buf[..n].copy_from_slice(&self.data[..n]);
            self.data = &self.data[n..];
            Ok(n)
        }
    }

    #[::core::prelude::v1::test]
    fn pump_survives_read_errors_and_skips_flush_marker() {
        let dir = temp_dir("flaky");
        let mut log = RotatingLog::open(dir.clone(), "missive", 1024 * 1024, 3);
        let data: &'static [u8] = Box::leak(
            [b"[app] avant\n".as_slice(), FLUSH_MARKER, b"[app] apres\n".as_slice()]
                .concat()
                .into_boxed_slice(),
        );
        pump(Flaky { failures: 2, data }, &mut log, |_| {});
        drop(log);
        let text = fs::read_to_string(rotated_path(&dir, "missive", 0)).unwrap();
        let lines: Vec<&str> = text.lines().collect();
        assert_eq!(lines.len(), 2);
        assert!(lines[0].ends_with("[app] avant"));
        assert!(lines[1].ends_with("[app] apres"));
        let _ = fs::remove_dir_all(dir);
    }

    #[::core::prelude::v1::test]
    fn failed_rotation_keeps_history_and_bounds_the_file() {
        let dir = temp_dir("stuck");
        fs::create_dir_all(rotated_path(&dir, "missive", 2)).unwrap();
        fs::write(rotated_path(&dir, "missive", 1), "history").unwrap();
        let mut log = RotatingLog::open(dir.clone(), "missive", 64, 3);
        for index in 0..40 {
            log.write_line(&format!("line {index:02} ............"));
        }
        drop(log);
        let current = fs::read_to_string(rotated_path(&dir, "missive", 0)).unwrap();
        assert!(current.len() as u64 <= 128);
        assert!(current.contains("journal tronqué"));
        assert!(current.ends_with("line 39 ............\n"));
        assert_eq!(fs::read_to_string(rotated_path(&dir, "missive", 1)).unwrap(), "history");
        assert!(rotated_path(&dir, "missive", 2).is_dir());
        let _ = fs::remove_dir_all(dir);
    }

    #[::core::prelude::v1::test]
    fn restart_arguments_round_trip_and_keep_only_demo() {
        let args = super::relaunch_args(
            ["--background".to_string(), "--demo".to_string(), "--restarted-after".into(), "9".into()],
            42,
        );
        assert_eq!(args, ["--demo", "--restarted-after", "42"]);
        assert_eq!(super::crashed_pid(args), Some(42));
        assert_eq!(super::crashed_pid(["--demo".to_string()]), None);
        assert_eq!(super::crashed_pid(["--restarted-after".to_string(), "x".into()]), None);
    }

    #[::core::prelude::v1::test]
    fn restart_is_throttled() {
        assert!(super::restart_allowed(None, 1_000));
        assert!(super::restart_allowed(Some("garbage"), 1_000));
        assert!(!super::restart_allowed(Some("990"), 1_000));
        assert!(!super::restart_allowed(Some("941"), 1_000));
        assert!(super::restart_allowed(Some("940\n"), 1_000));
    }

    #[::core::prelude::v1::test]
    fn panic_line_is_capped_on_one_line() {
        let line = panic_line("main", "src/x.rs:1:2", "boom\nsecond");
        assert_eq!(line, "[panic] thread 'main' at src/x.rs:1:2: boom second");
        let long = "é".repeat(3000);
        let line = panic_line("t", "l", &long);
        let message = line.split_once(": ").unwrap().1;
        assert!(message.len() <= 2048);
        assert!(message.chars().all(|c| c == 'é'));
    }
    use chrono::{TimeZone, Utc};
    use serde_json::json;
    use std::{fs, path::PathBuf};

    fn temp_dir(name: &str) -> PathBuf {
        let dir = std::env::temp_dir().join(format!(
            "missive-diag-{name}-{}-{}",
            std::process::id(),
            Utc::now().timestamp_nanos_opt().unwrap_or_default()
        ));
        fs::create_dir_all(&dir).unwrap();
        dir
    }

    #[::core::prelude::v1::test]
    fn rotated_names() {
        let dir = PathBuf::from("logs");
        assert_eq!(rotated_path(&dir, "missive", 0), dir.join("missive.log"));
        assert_eq!(rotated_path(&dir, "missive", 1), dir.join("missive.1.log"));
        assert_eq!(rotated_path(&dir, "missive-demo", 2), dir.join("missive-demo.2.log"));
    }

    #[::core::prelude::v1::test]
    fn rotation_shifts_and_drops_oldest() {
        let dir = temp_dir("rotate");
        for (index, content) in ["current", "older", "oldest"].iter().enumerate() {
            fs::write(rotated_path(&dir, "missive", index), content).unwrap();
        }
        rotate(&dir, "missive", 3).unwrap();
        assert!(!rotated_path(&dir, "missive", 0).exists());
        assert_eq!(fs::read_to_string(rotated_path(&dir, "missive", 1)).unwrap(), "current");
        assert_eq!(fs::read_to_string(rotated_path(&dir, "missive", 2)).unwrap(), "older");
        assert!(!rotated_path(&dir, "missive", 3).exists());
        let _ = fs::remove_dir_all(dir);
    }

    #[::core::prelude::v1::test]
    fn log_rotates_past_limit_and_keeps_three_files() {
        let dir = temp_dir("limit");
        let mut log = RotatingLog::open(dir.clone(), "missive", 64, 3);
        for index in 0..40 {
            log.write_line(&format!("line {index:02} ............"));
        }
        drop(log);
        let current = fs::read_to_string(rotated_path(&dir, "missive", 0)).unwrap();
        assert!(current.len() as u64 <= 64);
        assert!(current.ends_with("line 39 ............\n"));
        assert!(rotated_path(&dir, "missive", 1).exists());
        assert!(rotated_path(&dir, "missive", 2).exists());
        assert!(!rotated_path(&dir, "missive", 3).exists());
        let _ = fs::remove_dir_all(dir);
    }

    #[::core::prelude::v1::test]
    fn pump_prefixes_each_line_with_a_timestamp() {
        let dir = temp_dir("pump");
        let mut log = RotatingLog::open(dir.clone(), "missive", 1024 * 1024, 3);
        let mut echoed = Vec::new();
        pump(
            &b"[app] one\r\n[bridge] two\npartial"[..],
            &mut log,
            |bytes| echoed.extend_from_slice(bytes),
        );
        drop(log);
        let text = fs::read_to_string(rotated_path(&dir, "missive", 0)).unwrap();
        let lines: Vec<&str> = text.lines().collect();
        assert_eq!(lines.len(), 3);
        assert!(lines[0].ends_with("Z [app] one"));
        assert!(lines[1].ends_with("Z [bridge] two"));
        assert!(lines[2].ends_with("Z partial"));
        assert_eq!(echoed, b"[app] one\r\n[bridge] two\npartial");
        let _ = fs::remove_dir_all(dir);
    }

    #[::core::prelude::v1::test]
    fn stamp_is_utc_iso() {
        let at = Utc.with_ymd_and_hms(2026, 11, 17, 8, 5, 3).unwrap();
        assert_eq!(stamp("x", at), "2026-11-17T08:05:03.000Z x");
    }

    #[::core::prelude::v1::test]
    fn last_lines_spans_rotated_files() {
        let newest = "c1\nc2\n".to_string();
        let older = "b1\nb2\nb3\n".to_string();
        let oldest = "a1\n".to_string();
        let texts = vec![newest, older, oldest];
        assert_eq!(last_lines(&texts, 3), vec!["b3", "c1", "c2"]);
        assert_eq!(last_lines(&texts, 10), vec!["a1", "b1", "b2", "b3", "c1", "c2"]);
        assert!(last_lines(&[], 5).is_empty());
    }

    #[::core::prelude::v1::test]
    fn windows_label_names_the_release() {
        let label = windows_label(
            Some("26300".into()),
            Some(7171),
            Some("25H2".into()),
            Some("Professional".into()),
        );
        assert!(label.starts_with("Windows 11 Professional 25H2 (build 26300.7171) "));
        assert!(windows_label(Some("19045".into()), None, None, None).starts_with("Windows 10 (build 19045) "));
    }

    #[::core::prelude::v1::test]
    fn report_handles_missing_bridge_command() {
        let _guard = crate::i18n::scoped("fr");
        let at = Utc.with_ymd_and_hms(2026, 11, 17, 8, 0, 0).unwrap();
        let mut input = ReportInput {
            version: "0.1.6".into(),
            installed: true,
            demo: false,
            os: "Windows 11".into(),
            link: "en ligne".into(),
            bridge: Err("unknown command (unknown_command)".into()),
            lines: vec!["l1".into(), "l2".into()],
        };
        let report = compose_report(&input, at);
        assert!(report.contains("Version : 0.1.6\n"));
        assert!(report.contains("Pont :\nindisponible (unknown command (unknown_command))"));
        assert!(report.ends_with("Journal (2 dernières lignes) :\nl1\nl2\n"));
        input.bridge = Ok(json!({ "connected": true }));
        input.installed = false;
        let report = compose_report(&input, at);
        assert!(report.contains("Version : 0.1.6 (hors installation)"));
        assert!(report.contains("\"connected\": true"));
    }
}
