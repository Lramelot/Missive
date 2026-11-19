use std::{
    collections::{HashMap, VecDeque},
    io::{BufRead, BufReader, ErrorKind, Read, Write},
    path::PathBuf,
    process::{Child, Command, Stdio},
    sync::{
        Arc, Mutex, Weak,
        atomic::{AtomicU64, Ordering},
        mpsc as std_mpsc,
    },
    thread,
    time::{Duration, Instant},
};

use anyhow::{Context as _, Result, anyhow};
use futures::channel::{mpsc, oneshot};
use serde::Deserialize;
use serde_json::{Value, json};

pub const DEFAULT_TIMEOUT: Duration = Duration::from_secs(60);
pub const LONG_TIMEOUT: Duration = Duration::from_secs(15 * 60);
const LONG_COMMANDS: &[&str] = &[
    "login",
    "connect",
    "send",
    "create_poll",
    "restore_history",
    "export_thread",
    "save_media",
    "media",
    "media_history",
    "avatar",
    "send_media",
    "send_voice",
    "forward",
    "set_thread_image",
    "search",
];
const REAPER_INTERVAL: Duration = Duration::from_millis(500);
const EXIT_WAIT: Duration = Duration::from_secs(3);
const STDERR_DRAIN_WAIT: Duration = Duration::from_secs(2);
pub const STDERR_TAIL_LINES: usize = 20;
pub const EXITED_EVENT: &str = "bridge_exited";

pub const RESTART_DELAYS: [Duration; 5] = [
    Duration::from_secs(1),
    Duration::from_secs(2),
    Duration::from_secs(5),
    Duration::from_secs(10),
    Duration::from_secs(30),
];
pub const RESTART_WINDOW: Duration = Duration::from_secs(5 * 60);

pub fn timeout_for(cmd: &str) -> Duration {
    if LONG_COMMANDS.contains(&cmd) {
        LONG_TIMEOUT
    } else {
        DEFAULT_TIMEOUT
    }
}

#[derive(Debug, Clone, Deserialize)]
pub struct BridgeError {
    pub code: String,
    pub message: String,
    #[serde(default)]
    pub data: Option<Value>,
}

impl BridgeError {
    fn timeout(cmd: &str, after: Duration) -> Self {
        Self {
            code: "timeout".into(),
            message: format!("no answer to `{cmd}` after {} s", after.as_secs()),
            data: None,
        }
    }

    fn stopped(cmd: &str) -> Self {
        Self {
            code: "bridge_stopped".into(),
            message: format!("bridge stopped before answering `{cmd}`"),
            data: None,
        }
    }
}

impl std::fmt::Display for BridgeError {
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        write!(f, "{} ({})", self.message, self.code)
    }
}

impl std::error::Error for BridgeError {}

#[derive(Debug)]
pub struct BridgeEvent {
    pub name: String,
    pub data: Value,
}

#[derive(Debug, Clone, Default, PartialEq, Eq, Deserialize)]
pub struct BridgeExit {
    #[serde(default)]
    pub code: Option<i32>,
    #[serde(default)]
    pub stderr: Vec<String>,
}

#[derive(Deserialize)]
struct IncomingLine {
    id: Option<u64>,
    ok: Option<bool>,
    result: Option<Value>,
    error: Option<BridgeError>,
    event: Option<String>,
    data: Option<Value>,
}

struct PendingEntry {
    cmd: String,
    deadline: Instant,
    timeout: Duration,
    sender: oneshot::Sender<Result<Value, BridgeError>>,
}

#[derive(Default)]
struct PendingTable {
    entries: HashMap<u64, PendingEntry>,
}

impl PendingTable {
    fn insert(
        &mut self,
        id: u64,
        cmd: &str,
        timeout: Duration,
        now: Instant,
    ) -> oneshot::Receiver<Result<Value, BridgeError>> {
        let (sender, receiver) = oneshot::channel();
        self.entries.insert(
            id,
            PendingEntry {
                cmd: cmd.to_string(),
                deadline: now + timeout,
                timeout,
                sender,
            },
        );
        receiver
    }

    fn remove(&mut self, id: u64) -> bool {
        self.entries.remove(&id).is_some()
    }

    fn complete(&mut self, id: u64, result: Result<Value, BridgeError>) -> bool {
        match self.entries.remove(&id) {
            Some(entry) => {
                let _ = entry.sender.send(result);
                true
            }
            None => false,
        }
    }

    fn fail(&mut self, id: u64) -> bool {
        match self.entries.remove(&id) {
            Some(entry) => {
                let _ = entry.sender.send(Err(BridgeError::stopped(&entry.cmd)));
                true
            }
            None => false,
        }
    }

    fn expire(&mut self, now: Instant) -> usize {
        let expired: Vec<u64> = self
            .entries
            .iter()
            .filter(|(_, entry)| entry.deadline <= now)
            .map(|(id, _)| *id)
            .collect();
        for id in &expired {
            if let Some(entry) = self.entries.remove(id) {
                eprintln!("[bridge] `{}` timed out", entry.cmd);
                let _ = entry
                    .sender
                    .send(Err(BridgeError::timeout(&entry.cmd, entry.timeout)));
            }
        }
        expired.len()
    }

    fn fail_all(&mut self) {
        for (_, entry) in self.entries.drain() {
            let _ = entry.sender.send(Err(BridgeError::stopped(&entry.cmd)));
        }
    }

    #[cfg(test)]
    fn len(&self) -> usize {
        self.entries.len()
    }
}

type Pending = Arc<Mutex<PendingTable>>;

fn lock<T>(mutex: &Mutex<T>) -> std::sync::MutexGuard<'_, T> {
    mutex.lock().unwrap_or_else(|poisoned| poisoned.into_inner())
}

#[derive(Default)]
struct StderrTail {
    lines: VecDeque<String>,
}

impl StderrTail {
    fn push(&mut self, line: String) {
        if self.lines.len() == STDERR_TAIL_LINES {
            self.lines.pop_front();
        }
        self.lines.push_back(line);
    }

    fn snapshot(&self) -> Vec<String> {
        self.lines.iter().cloned().collect()
    }
}

#[derive(Default)]
pub struct RestartPolicy {
    recent: VecDeque<Instant>,
}

impl RestartPolicy {
    pub fn next_delay(&mut self, now: Instant) -> Option<Duration> {
        self.recent
            .retain(|at| now.saturating_duration_since(*at) < RESTART_WINDOW);
        let delay = RESTART_DELAYS.get(self.recent.len()).copied()?;
        self.recent.push_back(now);
        Some(delay)
    }

    pub fn reset(&mut self) {
        self.recent.clear();
    }
}

#[derive(Clone)]
pub struct Bridge {
    inner: Arc<Inner>,
}

struct Inner {
    writer: std_mpsc::Sender<(u64, String)>,
    child: Arc<Mutex<Child>>,
    next_id: AtomicU64,
    pending: Pending,
}

impl Drop for Inner {
    fn drop(&mut self) {
        let _ = lock(&self.child).kill();
    }
}

pub struct BridgeOptions {
    pub executable: PathBuf,
    pub data_dir: PathBuf,
    pub cache_dir: PathBuf,
    pub demo: bool,
}

pub fn demo_requested() -> bool {
    std::env::args().any(|arg| arg == "--demo")
        || std::env::var("MISSIVE_DEMO").is_ok_and(|value| value == "1")
}

pub fn data_dir(demo: bool) -> Option<PathBuf> {
    dirs::data_dir().map(|dir| {
        dir.join("Missive")
            .join(if demo { "bridge-demo" } else { "bridge" })
    })
}

pub fn cache_dir(demo: bool) -> Option<PathBuf> {
    dirs::cache_dir().map(|dir| {
        dir.join("Missive")
            .join(if demo { "cache-demo" } else { "cache" })
    })
}

impl BridgeOptions {
    pub fn from_env() -> Result<Self> {
        let demo = demo_requested();
        let data_dir = data_dir(demo).context("no user data directory")?;
        let cache_dir = cache_dir(demo).unwrap_or_else(|| data_dir.clone());
        Ok(Self {
            executable: locate_executable()?,
            data_dir,
            cache_dir,
            demo,
        })
    }

    fn args(&self) -> Vec<std::ffi::OsString> {
        let mut args: Vec<std::ffi::OsString> = vec![
            "--data-dir".into(),
            self.data_dir.clone().into(),
            "--cache-dir".into(),
            self.cache_dir.clone().into(),
        ];
        if self.demo {
            args.push("--demo".into());
        }
        args
    }
}

fn locate_executable() -> Result<PathBuf> {
    let file_name = if cfg!(windows) {
        "missive-bridge.exe"
    } else {
        "missive-bridge"
    };
    if let Ok(path) = std::env::var("MISSIVE_BRIDGE") {
        return Ok(PathBuf::from(path));
    }
    let mut candidates = Vec::new();
    if let Some(dir) = std::env::current_exe()?.parent() {
        candidates.push(dir.join(file_name));
    }
    candidates.push(
        PathBuf::from(env!("CARGO_MANIFEST_DIR"))
            .join("..")
            .join("bridge")
            .join(file_name),
    );
    candidates
        .into_iter()
        .find(|path| path.is_file())
        .ok_or_else(|| anyhow!("{file_name} not found next to the app nor in ./bridge"))
}

fn read_lines(reader: impl Read, mut on_line: impl FnMut(String)) {
    let mut reader = BufReader::new(reader);
    let mut buffer = Vec::new();
    loop {
        buffer.clear();
        match reader.read_until(b'\n', &mut buffer) {
            Ok(0) => break,
            Ok(_) => {
                while matches!(buffer.last(), Some(b'\n' | b'\r')) {
                    buffer.pop();
                }
                on_line(String::from_utf8_lossy(&buffer).into_owned());
            }
            Err(err) if err.kind() == ErrorKind::Interrupted => continue,
            Err(err) => {
                eprintln!("[bridge] pipe read failed: {err}");
                break;
            }
        }
    }
}

fn wait_for_exit(child: &Mutex<Child>, patience: Duration) -> Option<i32> {
    let started = Instant::now();
    loop {
        match lock(child).try_wait() {
            Ok(Some(status)) => return status.code(),
            Ok(None) => {}
            Err(err) => {
                eprintln!("[bridge] exit status unavailable: {err}");
                return None;
            }
        }
        if started.elapsed() >= patience {
            break;
        }
        thread::sleep(Duration::from_millis(50));
    }
    let mut child = lock(child);
    let _ = child.kill();
    child.wait().ok().and_then(|status| status.code())
}

impl Bridge {
    pub fn spawn(options: &BridgeOptions) -> Result<(Self, mpsc::UnboundedReceiver<BridgeEvent>)> {
        std::fs::create_dir_all(&options.data_dir)?;
        std::fs::create_dir_all(&options.cache_dir)?;
        let mut command = Command::new(&options.executable);
        command
            .args(options.args())
            .stdin(Stdio::piped())
            .stdout(Stdio::piped())
            .stderr(Stdio::piped());
        if let Ok(level) = std::env::var("MISSIVE_LOG_LEVEL") {
            command.arg("--log-level").arg(level);
        }
        #[cfg(windows)]
        {
            use std::os::windows::process::CommandExt;
            const CREATE_NO_WINDOW: u32 = 0x0800_0000;
            command.creation_flags(CREATE_NO_WINDOW);
        }
        let mut child = command
            .spawn()
            .with_context(|| format!("failed to start {}", options.executable.display()))?;

        let mut stdin = child.stdin.take().context("bridge stdin")?;
        let stdout = child.stdout.take().context("bridge stdout")?;
        let stderr = child.stderr.take().context("bridge stderr")?;
        let child = Arc::new(Mutex::new(child));

        let pending: Pending = Arc::default();
        let (events_tx, events_rx) = mpsc::unbounded();
        let tail: Arc<Mutex<StderrTail>> = Arc::default();
        let (stderr_done_tx, stderr_done_rx) = std_mpsc::channel::<()>();

        let stderr_tail = tail.clone();
        thread::Builder::new()
            .name("bridge-stderr".into())
            .spawn(move || {
                read_lines(stderr, |line| {
                    eprintln!("[bridge] {line}");
                    lock(&stderr_tail).push(line);
                });
                let _ = stderr_done_tx.send(());
            })?;

        let reader_pending = pending.clone();
        let reader_child = child.clone();
        thread::Builder::new()
            .name("bridge-stdout".into())
            .spawn(move || {
                drain_and_report(stdout, &reader_pending, &events_tx, || {
                    let code = wait_for_exit(&reader_child, EXIT_WAIT);
                    let _ = stderr_done_rx.recv_timeout(STDERR_DRAIN_WAIT);
                    BridgeExit {
                        code,
                        stderr: lock(&tail).snapshot(),
                    }
                });
            })?;

        let (writer, writer_rx) = std_mpsc::channel::<(u64, String)>();
        let writer_pending = pending.clone();
        thread::Builder::new()
            .name("bridge-stdin".into())
            .spawn(move || {
                for (id, line) in writer_rx {
                    let written = writeln!(stdin, "{line}").and_then(|_| stdin.flush());
                    if written.is_err() {
                        lock(&writer_pending).fail(id);
                    }
                }
            })?;

        let reaper_pending: Weak<Mutex<PendingTable>> = Arc::downgrade(&pending);
        thread::Builder::new()
            .name("bridge-timeouts".into())
            .spawn(move || {
                loop {
                    thread::sleep(REAPER_INTERVAL);
                    let Some(pending) = reaper_pending.upgrade() else {
                        break;
                    };
                    lock(&pending).expire(Instant::now());
                }
            })?;

        Ok((
            Self {
                inner: Arc::new(Inner {
                    writer,
                    child,
                    next_id: AtomicU64::new(1),
                    pending,
                }),
            },
            events_rx,
        ))
    }

    pub async fn request(&self, cmd: &str, params: Value) -> Result<Value> {
        let id = self.inner.next_id.fetch_add(1, Ordering::Relaxed);
        let receiver = register(&self.inner.pending, id, cmd, Instant::now());

        let line = json!({ "id": id, "cmd": cmd, "params": params }).to_string();
        if self.inner.writer.send((id, line)).is_err() {
            lock(&self.inner.pending).remove(id);
            return Err(BridgeError::stopped(cmd).into());
        }

        match receiver.await {
            Ok(Ok(value)) => Ok(value),
            Ok(Err(err)) => Err(err.into()),
            Err(_) => Err(BridgeError::stopped(cmd).into()),
        }
    }

    pub async fn call<T: serde::de::DeserializeOwned>(
        &self,
        cmd: &str,
        params: Value,
    ) -> Result<T> {
        let value = self.request(cmd, params).await?;
        serde_json::from_value(value).with_context(|| format!("unexpected `{cmd}` result"))
    }
}

fn register(
    pending: &Mutex<PendingTable>,
    id: u64,
    cmd: &str,
    now: Instant,
) -> oneshot::Receiver<Result<Value, BridgeError>> {
    lock(pending).insert(id, cmd, timeout_for(cmd), now)
}

fn drain_and_report(
    stdout: impl Read,
    pending: &Mutex<PendingTable>,
    events: &mpsc::UnboundedSender<BridgeEvent>,
    exit: impl FnOnce() -> BridgeExit,
) {
    read_stdout(stdout, pending, events);
    lock(pending).fail_all();
    let exit = exit();
    eprintln!("[bridge] process exited with code {:?}", exit.code);
    let _ = events.unbounded_send(BridgeEvent {
        name: EXITED_EVENT.into(),
        data: json!({ "code": exit.code, "stderr": exit.stderr }),
    });
}

fn read_stdout(
    stdout: impl Read,
    pending: &Mutex<PendingTable>,
    events: &mpsc::UnboundedSender<BridgeEvent>,
) {
    read_lines(stdout, |line| {
        if line.trim().is_empty() {
            return;
        }
        let incoming: IncomingLine = match serde_json::from_str(&line) {
            Ok(incoming) => incoming,
            Err(err) => {
                eprintln!(
                    "[bridge] unparsable line ({:?} at column {}), {} bytes",
                    err.classify(),
                    err.column(),
                    line.len()
                );
                return;
            }
        };
        if let Some(name) = incoming.event {
            let _ = events.unbounded_send(BridgeEvent {
                name,
                data: incoming.data.unwrap_or(Value::Null),
            });
        } else if let Some(id) = incoming.id {
            let result = if incoming.ok == Some(true) {
                Ok(incoming.result.unwrap_or(Value::Null))
            } else {
                Err(incoming.error.unwrap_or(BridgeError {
                    code: "internal".into(),
                    message: "malformed error response".into(),
                    data: None,
                }))
            };
            lock(pending).complete(id, result);
        }
    });
}

#[cfg(test)]
mod tests {
    use super::*;

    #[::core::prelude::v1::test]
    fn long_commands_get_the_long_timeout() {
        let expected = [
            "login",
            "connect",
            "send",
            "create_poll",
            "restore_history",
            "export_thread",
            "save_media",
            "media",
            "media_history",
            "avatar",
            "send_media",
            "send_voice",
            "forward",
            "set_thread_image",
            "search",
        ];
        for cmd in expected {
            assert_eq!(timeout_for(cmd), LONG_TIMEOUT, "{cmd}");
        }
        assert_eq!(LONG_COMMANDS.len(), expected.len());
        for cmd in ["status", "threads", "messages", "react", "mark_read", "diagnostics"] {
            assert_eq!(timeout_for(cmd), DEFAULT_TIMEOUT, "{cmd}");
        }
    }

    #[::core::prelude::v1::test]
    fn registered_requests_get_the_deadline_of_their_command() {
        let pending = Mutex::new(PendingTable::default());
        let now = Instant::now();
        let _export = register(&pending, 1, "export_thread", now);
        let _status = register(&pending, 2, "status", now);
        let table = lock(&pending);
        assert_eq!(table.entries[&1].deadline, now + Duration::from_secs(900));
        assert_eq!(table.entries[&2].deadline, now + Duration::from_secs(60));
    }

    #[::core::prelude::v1::test]
    fn a_closed_stdout_fails_pending_requests_then_reports_the_exit() {
        let pending = Mutex::new(PendingTable::default());
        let mut waiting = lock(&pending).insert(9, "export_thread", LONG_TIMEOUT, Instant::now());
        let (events_tx, mut events_rx) = mpsc::unbounded();
        let input = b"{\"event\":\"typing\",\"data\":{}}\n".to_vec();
        drain_and_report(&input[..], &pending, &events_tx, || {
            assert_eq!(
                lock(&pending).len(),
                0,
                "pending requests must fail before waiting for the exit"
            );
            BridgeExit {
                code: Some(2),
                stderr: vec!["panic: boom".into()],
            }
        });
        let error = waiting.try_recv().unwrap().unwrap().unwrap_err();
        assert_eq!(error.code, "bridge_stopped");
        assert_eq!(events_rx.try_recv().unwrap().name, "typing");
        let exited = events_rx.try_recv().unwrap();
        assert_eq!(exited.name, EXITED_EVENT);
        assert_eq!(
            serde_json::from_value::<BridgeExit>(exited.data).unwrap(),
            BridgeExit {
                code: Some(2),
                stderr: vec!["panic: boom".into()],
            }
        );
    }

    #[::core::prelude::v1::test]
    fn expired_requests_leave_the_table_with_a_timeout_error() {
        let mut table = PendingTable::default();
        let now = Instant::now();
        let mut slow = table.insert(1, "status", Duration::from_secs(60), now);
        let mut long = table.insert(2, "media", Duration::from_secs(900), now);

        assert_eq!(table.expire(now + Duration::from_secs(59)), 0);
        assert_eq!(table.len(), 2);

        assert_eq!(table.expire(now + Duration::from_secs(60)), 1);
        assert_eq!(table.len(), 1);
        let error = slow.try_recv().unwrap().unwrap().unwrap_err();
        assert_eq!(error.code, "timeout");
        assert!(long.try_recv().unwrap().is_none());

        assert!(!table.complete(1, Ok(Value::Null)));
        assert!(table.complete(2, Ok(json!(7))));
        assert_eq!(long.try_recv().unwrap().unwrap().unwrap(), json!(7));
        assert_eq!(table.len(), 0);
    }

    #[::core::prelude::v1::test]
    fn stopping_the_bridge_fails_every_pending_request() {
        let mut table = PendingTable::default();
        let now = Instant::now();
        let mut first = table.insert(1, "threads", DEFAULT_TIMEOUT, now);
        let mut second = table.insert(2, "media", LONG_TIMEOUT, now);
        let mut third = table.insert(3, "send", DEFAULT_TIMEOUT, now);
        assert!(table.fail(3));
        table.fail_all();
        assert_eq!(table.len(), 0);
        for receiver in [&mut first, &mut second, &mut third] {
            let error = receiver.try_recv().unwrap().unwrap().unwrap_err();
            assert_eq!(error.code, "bridge_stopped");
        }
    }

    #[::core::prelude::v1::test]
    fn invalid_utf8_does_not_stop_the_reader() {
        let mut input = b"first\n".to_vec();
        input.extend_from_slice(&[0xff, 0xfe, b'x', b'\n']);
        input.extend_from_slice(b"third\r\nlast");
        let mut lines = Vec::new();
        read_lines(&input[..], |line| lines.push(line));
        assert_eq!(lines.len(), 4);
        assert_eq!(lines[0], "first");
        assert!(lines[1].ends_with('x'));
        assert_eq!(lines[2], "third");
        assert_eq!(lines[3], "last");
    }

    #[::core::prelude::v1::test]
    fn responses_reach_their_request_and_events_are_forwarded() {
        let pending = Mutex::new(PendingTable::default());
        let mut answer = lock(&pending).insert(4, "status", DEFAULT_TIMEOUT, Instant::now());
        let (events_tx, mut events_rx) = mpsc::unbounded();
        let mut input = b"{\"id\":4,\"ok\":true,\"result\":{\"a\":1}}\n".to_vec();
        input.extend_from_slice(&[0xff, b'\n']);
        input.extend_from_slice(b"{\"event\":\"typing\",\"data\":{\"x\":2}}\n");
        read_stdout(&input[..], &pending, &events_tx);
        assert_eq!(answer.try_recv().unwrap().unwrap().unwrap(), json!({"a": 1}));
        let event = events_rx.try_recv().unwrap();
        assert_eq!(event.name, "typing");
        assert_eq!(event.data, json!({"x": 2}));
    }

    #[::core::prelude::v1::test]
    fn stderr_tail_keeps_the_last_twenty_lines() {
        let mut tail = StderrTail::default();
        for index in 0..25 {
            tail.push(format!("line {index}"));
        }
        let lines = tail.snapshot();
        assert_eq!(lines.len(), STDERR_TAIL_LINES);
        assert_eq!(lines.first().map(String::as_str), Some("line 5"));
        assert_eq!(lines.last().map(String::as_str), Some("line 24"));
    }

    #[::core::prelude::v1::test]
    fn exit_code_is_collected() {
        let mut command = if cfg!(windows) {
            let mut command = Command::new("cmd");
            command.args(["/C", "exit 3"]);
            command
        } else {
            let mut command = Command::new("sh");
            command.args(["-c", "exit 3"]);
            command
        };
        let child = Mutex::new(command.spawn().unwrap());
        assert_eq!(wait_for_exit(&child, Duration::from_secs(10)), Some(3));
    }

    #[::core::prelude::v1::test]
    fn restarts_back_off_then_give_up_after_five_in_five_minutes() {
        let mut policy = RestartPolicy::default();
        let start = Instant::now();
        let delays: Vec<_> = (0..5)
            .map(|index| policy.next_delay(start + Duration::from_secs(index * 10)))
            .collect();
        assert_eq!(
            delays,
            RESTART_DELAYS.iter().copied().map(Some).collect::<Vec<_>>()
        );
        assert_eq!(policy.next_delay(start + Duration::from_secs(60)), None);
        assert_eq!(
            policy.next_delay(start + RESTART_WINDOW + Duration::from_secs(41)),
            Some(RESTART_DELAYS[0])
        );
        policy.reset();
        assert_eq!(policy.next_delay(start), Some(RESTART_DELAYS[0]));
    }

    #[::core::prelude::v1::test]
    fn bridge_gets_a_separate_cache_dir() {
        let options = BridgeOptions {
            executable: PathBuf::from("bridge"),
            data_dir: PathBuf::from("data"),
            cache_dir: PathBuf::from("cache"),
            demo: true,
        };
        let args: Vec<String> = options
            .args()
            .into_iter()
            .map(|arg| arg.to_string_lossy().into_owned())
            .collect();
        assert_eq!(args, ["--data-dir", "data", "--cache-dir", "cache", "--demo"]);
        if let (Some(normal), Some(demo)) = (cache_dir(false), cache_dir(true)) {
            assert!(normal.ends_with(PathBuf::from("Missive").join("cache")));
            assert!(demo.ends_with(PathBuf::from("Missive").join("cache-demo")));
            assert_ne!(Some(normal), data_dir(false));
        }
    }
}
