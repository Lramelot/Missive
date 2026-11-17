use std::{
    collections::HashMap,
    io::{BufRead, BufReader, Write},
    path::PathBuf,
    process::{Child, ChildStdin, Command, Stdio},
    sync::{
        Arc, Mutex,
        atomic::{AtomicU64, Ordering},
    },
    thread,
};

use anyhow::{Context as _, Result, anyhow};
use futures::channel::{mpsc, oneshot};
use serde::Deserialize;
use serde_json::{Value, json};

#[derive(Debug, Clone, Deserialize)]
pub struct BridgeError {
    pub code: String,
    pub message: String,
    #[serde(default)]
    pub data: Option<Value>,
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

#[derive(Deserialize)]
struct IncomingLine {
    id: Option<u64>,
    ok: Option<bool>,
    result: Option<Value>,
    error: Option<BridgeError>,
    event: Option<String>,
    data: Option<Value>,
}

type Pending = Arc<Mutex<HashMap<u64, oneshot::Sender<Result<Value, BridgeError>>>>>;

#[derive(Clone)]
pub struct Bridge {
    inner: Arc<Inner>,
}

struct Inner {
    stdin: Mutex<ChildStdin>,
    child: Mutex<Child>,
    next_id: AtomicU64,
    pending: Pending,
}

impl Drop for Inner {
    fn drop(&mut self) {
        if let Ok(mut child) = self.child.lock() {
            let _ = child.kill();
        }
    }
}

pub struct BridgeOptions {
    pub executable: PathBuf,
    pub data_dir: PathBuf,
    pub demo: bool,
}

pub fn demo_requested() -> bool {
    std::env::args().any(|arg| arg == "--demo")
        || std::env::var("MISSIVE_DEMO").is_ok_and(|value| value == "1")
}

impl BridgeOptions {
    pub fn from_env() -> Result<Self> {
        let demo = demo_requested();
        let data_dir = dirs::data_dir()
            .context("no user data directory")?
            .join("Missive")
            .join(if demo { "bridge-demo" } else { "bridge" });
        Ok(Self {
            executable: locate_executable()?,
            data_dir,
            demo,
        })
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

impl Bridge {
    pub fn spawn(options: &BridgeOptions) -> Result<(Self, mpsc::UnboundedReceiver<BridgeEvent>)> {
        std::fs::create_dir_all(&options.data_dir)?;
        let mut command = Command::new(&options.executable);
        command
            .arg("--data-dir")
            .arg(&options.data_dir)
            .stdin(Stdio::piped())
            .stdout(Stdio::piped())
            .stderr(Stdio::piped());
        if options.demo {
            command.arg("--demo");
        }
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

        let stdin = child.stdin.take().context("bridge stdin")?;
        let stdout = child.stdout.take().context("bridge stdout")?;
        let stderr = child.stderr.take().context("bridge stderr")?;

        let pending: Pending = Arc::default();
        let (events_tx, events_rx) = mpsc::unbounded();

        let reader_pending = pending.clone();
        thread::Builder::new()
            .name("bridge-stdout".into())
            .spawn(move || read_stdout(stdout, reader_pending, events_tx))?;

        thread::Builder::new()
            .name("bridge-stderr".into())
            .spawn(move || {
                for line in BufReader::new(stderr).lines().map_while(Result::ok) {
                    eprintln!("[bridge] {line}");
                }
            })?;

        Ok((
            Self {
                inner: Arc::new(Inner {
                    stdin: Mutex::new(stdin),
                    child: Mutex::new(child),
                    next_id: AtomicU64::new(1),
                    pending,
                }),
            },
            events_rx,
        ))
    }

    pub async fn request(&self, cmd: &str, params: Value) -> Result<Value> {
        let id = self.inner.next_id.fetch_add(1, Ordering::Relaxed);
        let (tx, rx) = oneshot::channel();
        self.inner.pending.lock().unwrap().insert(id, tx);

        let line = json!({ "id": id, "cmd": cmd, "params": params }).to_string();
        let write_result = {
            let mut stdin = self.inner.stdin.lock().unwrap();
            writeln!(stdin, "{line}").and_then(|_| stdin.flush())
        };
        if let Err(err) = write_result {
            self.inner.pending.lock().unwrap().remove(&id);
            return Err(anyhow!(err).context("bridge is not running"));
        }

        match rx.await {
            Ok(Ok(value)) => Ok(value),
            Ok(Err(err)) => Err(err.into()),
            Err(_) => Err(anyhow!("bridge stopped before answering `{cmd}`")),
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

fn read_stdout(
    stdout: std::process::ChildStdout,
    pending: Pending,
    events: mpsc::UnboundedSender<BridgeEvent>,
) {
    for line in BufReader::new(stdout).lines().map_while(Result::ok) {
        let incoming: IncomingLine = match serde_json::from_str(&line) {
            Ok(incoming) => incoming,
            Err(err) => {
                eprintln!("[bridge] unparsable line ({err}), {} bytes", line.len());
                continue;
            }
        };
        if let Some(name) = incoming.event {
            let _ = events.unbounded_send(BridgeEvent {
                name,
                data: incoming.data.unwrap_or(Value::Null),
            });
        } else if let Some(id) = incoming.id {
            let Some(sender) = pending.lock().unwrap().remove(&id) else {
                continue;
            };
            let result = if incoming.ok == Some(true) {
                Ok(incoming.result.unwrap_or(Value::Null))
            } else {
                Err(incoming.error.unwrap_or(BridgeError {
                    code: "internal".into(),
                    message: "malformed error response".into(),
                    data: None,
                }))
            };
            let _ = sender.send(result);
        }
    }
    pending.lock().unwrap().clear();
    let _ = events.unbounded_send(BridgeEvent {
        name: "connection".into(),
        data: json!({ "state": "error", "detail": "bridge process exited", "permanent": true }),
    });
}
