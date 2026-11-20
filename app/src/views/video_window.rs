use std::{
    fs::File,
    io::{Read, Seek, SeekFrom},
    path::{Path, PathBuf},
};

use futures::{StreamExt as _, channel::mpsc};
use gpui_kit::component::{TitleBar, h_flex, v_flex};
use gpui_kit::*;
use gpui_webview::WebView;
use wry::{
    http::{Request, Response, StatusCode, header},
    raw_window_handle::{HandleError, HasWindowHandle, RawWindowHandle, WindowHandle},
};

use crate::{app::MissiveApp, style::Palette};

const SCHEME: &str = "missive";
#[cfg(windows)]
const PLAYER_URL: &str = "http://missive.localhost/";
#[cfg(not(windows))]
const PLAYER_URL: &str = "missive://localhost/";
const VIDEO_PATH: &str = "/video";
const MAX_CHUNK: u64 = 2 << 20;
const MAX_WIDTH: f32 = 1100.;
const MAX_HEIGHT: f32 = 760.;
const MIN_WIDTH: f32 = 420.;
const MIN_HEIGHT: f32 = 300.;
const TITLE_BAR_HEIGHT: f32 = 34.;

const PLAYER_HTML: &str = r#"<!doctype html>
<html><head><meta charset="utf-8">
<style>html,body{margin:0;height:100%;background:#000;overflow:hidden}video{display:block;width:100%;height:100%;object-fit:contain;outline:none}</style>
</head><body>
<video src="/video" controls autoplay playsinline></video>
<script>
const video = document.querySelector('video');
video.addEventListener('error', () => window.ipc.postMessage('unsupported'));
document.addEventListener('keydown', (event) => {
  if (event.key === 'Escape' && !document.fullscreenElement) window.ipc.postMessage('close');
  if (event.key === ' ' && document.activeElement !== video) {
    event.preventDefault();
    video.paused ? video.play() : video.pause();
  }
});
</script>
</body></html>"#;

pub struct VideoWindow {
    webview: Option<Entity<WebView>>,
    _context: Option<wry::WebContext>,
    _task: Task<()>,
}

struct ParentWindow(RawWindowHandle);

impl HasWindowHandle for ParentWindow {
    fn window_handle(&self) -> Result<WindowHandle<'_>, HandleError> {
        Ok(unsafe { WindowHandle::borrow_raw(self.0) })
    }
}

#[derive(Debug, PartialEq, Eq)]
enum ByteRange {
    Partial { start: u64, end: u64 },
    Unsatisfiable,
}

fn parse_range(header: Option<&str>, len: u64) -> ByteRange {
    let unsatisfiable = ByteRange::Unsatisfiable;
    if len == 0 {
        return unsatisfiable;
    }
    let spec = match header {
        None => "0-",
        Some(value) => match value.trim().strip_prefix("bytes=") {
            Some(spec) => spec.split(',').next().unwrap_or("").trim(),
            None => return unsatisfiable,
        },
    };
    let Some((first, last)) = spec.split_once('-') else {
        return unsatisfiable;
    };
    let (start, end) = match (first.trim(), last.trim()) {
        ("", "") => return unsatisfiable,
        ("", suffix) => match suffix.parse::<u64>() {
            Ok(0) | Err(_) => return unsatisfiable,
            Ok(suffix) => (len.saturating_sub(suffix), len - 1),
        },
        (start, end) => {
            let Ok(start) = start.parse::<u64>() else {
                return unsatisfiable;
            };
            let end = match end {
                "" => len - 1,
                end => match end.parse::<u64>() {
                    Ok(end) => end.min(len - 1),
                    Err(_) => return unsatisfiable,
                },
            };
            (start, end)
        }
    };
    if start >= len || start > end {
        return unsatisfiable;
    }
    ByteRange::Partial {
        start,
        end: end.min(start + MAX_CHUNK - 1),
    }
}

fn video_mime(path: &Path) -> &'static str {
    match path
        .extension()
        .map(|ext| ext.to_string_lossy().to_lowercase())
        .as_deref()
    {
        Some("webm") => "video/webm",
        Some("mkv") => "video/x-matroska",
        _ => "video/mp4",
    }
}

fn read_range(path: &Path, start: u64, end: u64) -> std::io::Result<Vec<u8>> {
    let mut file = File::open(path)?;
    file.seek(SeekFrom::Start(start))?;
    let mut body = Vec::with_capacity((end - start + 1) as usize);
    file.take(end - start + 1).read_to_end(&mut body)?;
    Ok(body)
}

fn respond(path: &Path, request: &Request<Vec<u8>>) -> Response<Vec<u8>> {
    let plain = |status: StatusCode| {
        Response::builder()
            .status(status)
            .body(Vec::new())
            .unwrap_or_default()
    };
    match request.uri().path() {
        "/" => Response::builder()
            .header(header::CONTENT_TYPE, "text/html; charset=utf-8")
            .body(PLAYER_HTML.as_bytes().to_vec())
            .unwrap_or_default(),
        VIDEO_PATH => {
            let Ok(len) = std::fs::metadata(path).map(|meta| meta.len()) else {
                return plain(StatusCode::NOT_FOUND);
            };
            let range = request
                .headers()
                .get(header::RANGE)
                .and_then(|value| value.to_str().ok());
            match parse_range(range, len) {
                ByteRange::Unsatisfiable => Response::builder()
                    .status(StatusCode::RANGE_NOT_SATISFIABLE)
                    .header(header::CONTENT_RANGE, format!("bytes */{len}"))
                    .body(Vec::new())
                    .unwrap_or_default(),
                ByteRange::Partial { start, end } => match read_range(path, start, end) {
                    Ok(body) => Response::builder()
                        .status(StatusCode::PARTIAL_CONTENT)
                        .header(header::CONTENT_TYPE, video_mime(path))
                        .header(header::ACCEPT_RANGES, "bytes")
                        .header(header::CONTENT_LENGTH, body.len())
                        .header(
                            header::CONTENT_RANGE,
                            format!("bytes {start}-{}/{len}", start + body.len() as u64 - 1),
                        )
                        .body(body)
                        .unwrap_or_default(),
                    Err(_) => plain(StatusCode::INTERNAL_SERVER_ERROR),
                },
            }
        }
        _ => plain(StatusCode::NOT_FOUND),
    }
}

fn window_size(width: Option<u32>, height: Option<u32>) -> Size<Pixels> {
    let (width, height) = match (width, height) {
        (Some(width), Some(height)) if width > 0 && height > 0 => (width as f32, height as f32),
        _ => (16., 9.),
    };
    let scale = (MAX_WIDTH / width).min(MAX_HEIGHT / height);
    let video_width = (width * scale).max(MIN_WIDTH);
    let video_height = (height * scale).max(MIN_HEIGHT);
    size(px(video_width), px(video_height + TITLE_BAR_HEIGHT))
}

enum PlayerEvent {
    Close,
    Unsupported,
}

impl VideoWindow {
    fn new(
        app: WeakEntity<MissiveApp>,
        path: PathBuf,
        window: &mut Window,
        cx: &mut Context<Self>,
    ) -> Self {
        let parent = match HasWindowHandle::window_handle(window) {
            Ok(handle) => ParentWindow(handle.as_raw()),
            Err(err) => {
                eprintln!("[video] no window handle: {err}");
                return Self::fallback(app, path, window, cx);
            }
        };
        let (events, mut received) = mpsc::unbounded::<PlayerEvent>();
        let task = cx.spawn_in(window, async move |this, cx| {
            let data_dir = dirs::data_dir().map(|dir| dir.join("Missive").join("webview"));
            let mut context = wry::WebContext::new(data_dir);
            let served = path.clone();
            let built = wry::WebViewBuilder::new_with_web_context(&mut context)
                .with_asynchronous_custom_protocol(SCHEME.into(), move |_, request, responder| {
                    let served = served.clone();
                    std::thread::spawn(move || responder.respond(respond(&served, &request)));
                })
                .with_ipc_handler(move |request| {
                    let event = match request.body().as_str() {
                        "close" => PlayerEvent::Close,
                        "unsupported" => PlayerEvent::Unsupported,
                        _ => return,
                    };
                    let _ = events.unbounded_send(event);
                })
                .with_url(PLAYER_URL)
                .build_as_child_async(&parent)
                .await;
            let shown = this
                .update_in(cx, |this, window, cx| match built {
                    Ok(raw) => {
                        let webview = WebView::new(raw, window, cx);
                        this.webview = Some(cx.new(|_| webview));
                        this._context = Some(context);
                        cx.notify();
                        true
                    }
                    Err(err) => {
                        eprintln!("[video] webview unavailable: {err}");
                        false
                    }
                })
                .unwrap_or(false);
            if !shown {
                let _ = this.update_in(cx, |_, window, cx| {
                    Self::play_elsewhere(&app, &path, window, cx)
                });
                return;
            }
            while let Some(event) = received.next().await {
                let _ = this.update_in(cx, |_, window, cx| match event {
                    PlayerEvent::Close => window.remove_window(),
                    PlayerEvent::Unsupported => Self::play_elsewhere(&app, &path, window, cx),
                });
            }
        });
        Self {
            webview: None,
            _context: None,
            _task: task,
        }
    }

    fn fallback(
        app: WeakEntity<MissiveApp>,
        path: PathBuf,
        window: &mut Window,
        cx: &mut Context<Self>,
    ) -> Self {
        Self::play_elsewhere(&app, &path, window, cx);
        Self {
            webview: None,
            _context: None,
            _task: Task::ready(()),
        }
    }

    fn play_elsewhere(
        app: &WeakEntity<MissiveApp>,
        path: &Path,
        window: &mut Window,
        cx: &mut Context<Self>,
    ) {
        let _ = app.update(cx, |app, cx| {
            app.show_toast(t!("video_window.fallback"), cx);
            crate::desktop::open_local(path, cx);
        });
        cx.defer_in(window, |_, window, _| window.remove_window());
    }
}

impl Render for VideoWindow {
    fn render(&mut self, _: &mut Window, cx: &mut Context<Self>) -> impl IntoElement {
        let p = Palette::get(cx);
        let body = match &self.webview {
            Some(webview) => div()
                .flex_1()
                .min_h_0()
                .child(webview.clone())
                .into_any_element(),
            None => div()
                .flex_1()
                .flex()
                .items_center()
                .justify_center()
                .bg(gpui_kit::black())
                .text_color(gpui_kit::white())
                .child(ts!("video_window.opening"))
                .into_any_element(),
        };
        v_flex()
            .size_full()
            .bg(gpui_kit::black())
            .font_family(crate::style::FONT_FAMILY)
            .text_color(p.text)
            .child(
                TitleBar::new().bg(p.canvas).border_color(p.canvas).child(
                    h_flex()
                        .gap_2()
                        .px_2()
                        .child(img("missive/logo.svg").size(px(18.)).flex_none())
                        .child(
                            div()
                                .text_size(px(13.))
                                .font_weight(FontWeight::BOLD)
                                .child(ts!("video_window.title")),
                        ),
                ),
            )
            .child(body)
    }
}

impl MissiveApp {
    pub(crate) fn open_video_window(
        &mut self,
        path: PathBuf,
        width: Option<u32>,
        height: Option<u32>,
        cx: &mut Context<Self>,
    ) {
        let owner = cx.entity();
        cx.defer(move |cx| {
            let app = owner.downgrade();
            let options = WindowOptions {
                window_bounds: Some(WindowBounds::centered(window_size(width, height), cx)),
                window_min_size: Some(size(px(MIN_WIDTH), px(MIN_HEIGHT))),
                ..TitleBar::window_options()
            };
            let fallback = path.clone();
            if let Err(err) = gpui_kit::open_window(options, cx, move |window, cx| {
                cx.new(|cx| VideoWindow::new(app, path, window, cx))
            }) {
                eprintln!("[video] window failed: {err:#}");
                crate::desktop::open_local(&fallback, cx);
            }
        });
    }
}

#[cfg(test)]
mod tests {
    use gpui_kit::px;
    use wry::http::{Request, StatusCode, header};

    use super::{
        ByteRange, MAX_CHUNK, MAX_HEIGHT, MAX_WIDTH, MIN_WIDTH, PLAYER_URL, TITLE_BAR_HEIGHT,
        VIDEO_PATH, parse_range, respond, window_size,
    };

    #[test]
    fn ranges_follow_the_http_rules_and_stay_chunked() {
        let len = 10 * MAX_CHUNK;
        let partial = |start, end| ByteRange::Partial { start, end };
        assert_eq!(parse_range(Some("bytes=0-99"), len), partial(0, 99));
        assert_eq!(
            parse_range(Some("bytes=100-"), len),
            partial(100, 100 + MAX_CHUNK - 1)
        );
        assert_eq!(parse_range(None, len), partial(0, MAX_CHUNK - 1));
        assert_eq!(
            parse_range(Some("bytes=-500"), len),
            partial(len - 500, len - 1)
        );
        assert_eq!(
            parse_range(Some("bytes=5-3"), len),
            ByteRange::Unsatisfiable
        );
        assert_eq!(parse_range(Some("bytes=-0"), len), ByteRange::Unsatisfiable);
        assert_eq!(
            parse_range(Some(&format!("bytes={len}-")), len),
            ByteRange::Unsatisfiable
        );
        assert_eq!(
            parse_range(Some("items=0-1"), len),
            ByteRange::Unsatisfiable
        );
        assert_eq!(parse_range(Some("bytes=0-99"), 0), ByteRange::Unsatisfiable);
        assert_eq!(
            parse_range(Some("bytes=10-999999999999"), 50),
            partial(10, 49)
        );
        assert_eq!(parse_range(Some("bytes=0-1, 5-9"), len), partial(0, 1));
    }

    fn request(path: &str, range: Option<&str>) -> Request<Vec<u8>> {
        let mut builder = Request::builder().uri(format!("{PLAYER_URL}{}", &path[1..]));
        if let Some(range) = range {
            builder = builder.header(header::RANGE, range);
        }
        builder.body(Vec::new()).unwrap()
    }

    #[test]
    fn the_protocol_serves_the_page_and_byte_ranges_of_the_video() {
        let dir = std::env::temp_dir().join(format!("missive-video-{}", std::process::id()));
        std::fs::create_dir_all(&dir).unwrap();
        let path = dir.join("clip.mp4");
        let data: Vec<u8> = (0..=255u8).cycle().take(5000).collect();
        std::fs::write(&path, &data).unwrap();

        let page = respond(&path, &request("/", None));
        assert_eq!(page.status(), StatusCode::OK);
        assert!(String::from_utf8_lossy(page.body()).contains("<video src=\"/video\""));

        let part = respond(&path, &request(VIDEO_PATH, Some("bytes=1000-1999")));
        assert_eq!(part.status(), StatusCode::PARTIAL_CONTENT);
        assert_eq!(part.body().as_slice(), &data[1000..2000]);
        assert_eq!(
            part.headers()[header::CONTENT_RANGE],
            "bytes 1000-1999/5000"
        );
        assert_eq!(part.headers()[header::CONTENT_TYPE], "video/mp4");

        let tail = respond(&path, &request(VIDEO_PATH, Some("bytes=4990-")));
        assert_eq!(tail.body().as_slice(), &data[4990..]);
        assert_eq!(
            tail.headers()[header::CONTENT_RANGE],
            "bytes 4990-4999/5000"
        );

        let beyond = respond(&path, &request(VIDEO_PATH, Some("bytes=6000-")));
        assert_eq!(beyond.status(), StatusCode::RANGE_NOT_SATISFIABLE);
        assert_eq!(
            respond(&path, &request("/other", None)).status(),
            StatusCode::NOT_FOUND
        );
        assert_eq!(
            respond(&dir.join("missing.mp4"), &request(VIDEO_PATH, None)).status(),
            StatusCode::NOT_FOUND
        );
        let _ = std::fs::remove_dir_all(&dir);
    }

    #[test]
    fn windows_follow_the_video_shape_within_bounds() {
        let portrait = window_size(Some(1080), Some(1920));
        assert!(portrait.height <= px(MAX_HEIGHT + TITLE_BAR_HEIGHT));
        assert!(portrait.width >= px(MIN_WIDTH));
        let landscape = window_size(Some(1920), Some(1080));
        assert!(landscape.width <= px(MAX_WIDTH));
        assert!(landscape.width > landscape.height);
        let unknown = window_size(None, Some(0));
        assert!(unknown.width > unknown.height);
    }
}
