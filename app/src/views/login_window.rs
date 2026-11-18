use std::{collections::HashMap, time::Duration};

use gpui_kit::component::{TitleBar, h_flex, v_flex};
use gpui_kit::*;
use gpui_webview::WebView;
use wry::raw_window_handle::{HandleError, HasWindowHandle, RawWindowHandle, WindowHandle};

use crate::{app::MissiveApp, style::Palette};

const LOGIN_URL: &str = "https://www.messenger.com/login/";
const COOKIE_URL: &str = "https://www.messenger.com/";
const REQUIRED: [&str; 3] = ["c_user", "xs", "datr"];
const POLL: Duration = Duration::from_millis(800);
const CLEAR_TIMEOUT: Duration = Duration::from_secs(8);

pub struct LoginWindow {
    webview: Option<Entity<WebView>>,
    error: Option<SharedString>,
    _context: Option<wry::WebContext>,
    _task: Task<()>,
}

struct ParentWindow(RawWindowHandle);

impl HasWindowHandle for ParentWindow {
    fn window_handle(&self) -> Result<WindowHandle<'_>, HandleError> {
        Ok(unsafe { WindowHandle::borrow_raw(self.0) })
    }
}

fn session_from(cookies: impl IntoIterator<Item = (String, String)>) -> Option<HashMap<String, String>> {
    let found: HashMap<String, String> = cookies
        .into_iter()
        .filter(|(_, value)| !value.is_empty())
        .collect();
    REQUIRED
        .iter()
        .all(|name| found.contains_key(*name))
        .then_some(found)
}

#[cfg(windows)]
type CookieSource = webview2_com::Microsoft::Web::WebView2::Win32::ICoreWebView2;

#[cfg(windows)]
fn cookie_source(webview: &WebView) -> CookieSource {
    use wry::WebViewExtWindows as _;
    webview.raw().webview()
}

#[cfg(windows)]
fn read_cookie_list(
    list: &webview2_com::Microsoft::Web::WebView2::Win32::ICoreWebView2CookieList,
) -> webview2_core::Result<Vec<(String, String)>> {
    use webview2_com::take_pwstr;
    use webview2_core::PWSTR;
    let mut out = Vec::new();
    unsafe {
        let mut count = 0u32;
        list.Count(&mut count)?;
        for index in 0..count {
            let cookie = list.GetValueAtIndex(index)?;
            let mut name = PWSTR::null();
            cookie.Name(&mut name)?;
            let mut value = PWSTR::null();
            cookie.Value(&mut value)?;
            out.push((take_pwstr(name), take_pwstr(value)));
        }
    }
    Ok(out)
}

#[cfg(windows)]
async fn session_cookies(source: &CookieSource, _: &mut AsyncWindowContext) -> Option<HashMap<String, String>> {
    use webview2_com::GetCookiesCompletedHandler;
    use webview2_com::Microsoft::Web::WebView2::Win32::ICoreWebView2_2;
    use webview2_core::{HSTRING, Interface as _, PCWSTR};

    let (tx, rx) = futures::channel::oneshot::channel();
    let handler = GetCookiesCompletedHandler::create(Box::new(move |result, list| {
        let cookies = result
            .ok()
            .and(list)
            .and_then(|list| read_cookie_list(&list).ok());
        let _ = tx.send(cookies);
        Ok(())
    }));
    let uri = HSTRING::from(COOKIE_URL);
    unsafe {
        let manager = source.cast::<ICoreWebView2_2>().ok()?.CookieManager().ok()?;
        manager
            .GetCookies(PCWSTR::from_raw(uri.as_ptr()), &handler)
            .ok()?;
    }
    session_from(rx.await.ok()??)
}

#[cfg(windows)]
async fn clear_browsing_data(webview: &wry::WebView, executor: &BackgroundExecutor) {
    use webview2_com::ClearBrowsingDataCompletedHandler;
    use webview2_com::Microsoft::Web::WebView2::Win32::{ICoreWebView2_13, ICoreWebView2Profile2};
    use webview2_core::Interface as _;
    use wry::WebViewExtWindows as _;

    let (tx, rx) = futures::channel::oneshot::channel::<()>();
    let handler = ClearBrowsingDataCompletedHandler::create(Box::new(move |_| {
        let _ = tx.send(());
        Ok(())
    }));
    let started = unsafe {
        webview
            .webview()
            .cast::<ICoreWebView2_13>()
            .and_then(|webview| webview.Profile())
            .and_then(|profile| profile.cast::<ICoreWebView2Profile2>())
            .and_then(|profile| profile.ClearBrowsingDataAll(&handler))
    }
    .is_ok();
    if started {
        let timeout = executor.timer(CLEAR_TIMEOUT);
        let _ = futures::future::select(Box::pin(rx), Box::pin(timeout)).await;
    }
}

#[cfg(not(windows))]
async fn clear_browsing_data(webview: &wry::WebView, _: &BackgroundExecutor) {
    let _ = webview.clear_all_browsing_data();
}

#[cfg(not(windows))]
type CookieSource = Entity<WebView>;

#[cfg(not(windows))]
async fn session_cookies(source: &CookieSource, cx: &mut AsyncWindowContext) -> Option<HashMap<String, String>> {
    let cookies = source.read_with(cx, |webview, _| webview.raw().cookies_for_url(COOKIE_URL).ok())?;
    session_from(
        cookies
            .iter()
            .map(|cookie| (cookie.name().to_string(), cookie.value().to_string())),
    )
}

impl LoginWindow {
    fn new(app: WeakEntity<MissiveApp>, window: &mut Window, cx: &mut Context<Self>) -> Self {
        let parent = match HasWindowHandle::window_handle(window) {
            Ok(handle) => ParentWindow(handle.as_raw()),
            Err(err) => {
                return Self {
                    webview: None,
                    error: Some(ts!("login.webview_unavailable", error = err)),
                    _context: None,
                    _task: Task::ready(()),
                };
            }
        };
        let task = cx.spawn_in(window, async move |this, cx| {
            let data_dir = dirs::data_dir().map(|dir| dir.join("Missive").join("webview"));
            let mut context = wry::WebContext::new(data_dir);
            let built = wry::WebViewBuilder::new_with_web_context(&mut context)
                .with_url("about:blank")
                .build_as_child_async(&parent)
                .await;
            if let Ok(raw) = &built {
                clear_browsing_data(raw, cx.background_executor()).await;
            }
            let source = this
                .update_in(cx, |this, window, cx| match built {
                    Ok(raw) => {
                        let mut webview = WebView::new(raw, window, cx);
                        webview.load_url(LOGIN_URL);
                        #[cfg(windows)]
                        let source = cookie_source(&webview);
                        let entity = cx.new(|_| webview);
                        #[cfg(not(windows))]
                        let source = entity.clone();
                        this.webview = Some(entity);
                        this._context = Some(context);
                        cx.notify();
                        Some(source)
                    }
                    Err(err) => {
                        this.error = Some(ts!("login.webview_unavailable", error = err));
                        cx.notify();
                        None
                    }
                })
                .ok()
                .flatten();
            let Some(source) = source else {
                return;
            };

            let mut baseline: Option<Option<String>> = None;
            loop {
                cx.background_executor().timer(POLL).await;
                let cookies = session_cookies(&source, cx).await;
                let current_xs = cookies.as_ref().and_then(|c| c.get("xs").cloned());
                let Some(initial) = baseline.as_ref() else {
                    baseline = Some(current_xs);
                    continue;
                };
                if current_xs.is_none() || current_xs == *initial {
                    continue;
                }
                if let Some(cookies) = cookies {
                    let _ = app.update(cx, |app, cx| app.login_with_cookies(cookies, cx));
                    let _ = this.update_in(cx, |_, window, _| window.remove_window());
                    break;
                }
            }
        });

        Self {
            webview: None,
            error: None,
            _context: None,
            _task: task,
        }
    }
}

impl Render for LoginWindow {
    fn render(&mut self, _: &mut Window, cx: &mut Context<Self>) -> impl IntoElement {
        let p = Palette::get(cx);
        let body = match (&self.webview, &self.error) {
            (Some(webview), _) => div()
                .flex_1()
                .min_h_0()
                .child(webview.clone())
                .into_any_element(),
            (None, error) => div()
                .flex_1()
                .flex()
                .items_center()
                .justify_center()
                .p_6()
                .text_color(if error.is_some() { p.danger } else { p.text_muted })
                .child(
                    error
                        .clone()
                        .unwrap_or_else(|| ts!("login.opening")),
                )
                .into_any_element(),
        };
        v_flex()
            .size_full()
            .bg(p.panel)
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
                                .child(ts!("login.title")),
                        ),
                ),
            )
            .child(
                div()
                    .flex_none()
                    .w_full()
                    .px_4()
                    .py_2()
                    .bg(p.accent_soft)
                    .border_b_1()
                    .border_color(p.divider)
                    .text_size(px(12.5))
                    .text_color(p.text_muted)
                    .child(ts!("login.instructions")),
            )
            .child(body)
    }
}

impl MissiveApp {
    pub(crate) fn open_login_window(&mut self, cx: &mut Context<Self>) {
        let owner = cx.entity();
        cx.defer(move |cx| {
            let app = owner.downgrade();
            let options = WindowOptions {
                window_bounds: Some(WindowBounds::centered(size(px(980.), px(820.)), cx)),
                window_min_size: Some(size(px(560.), px(600.))),
                ..TitleBar::window_options()
            };
            if let Err(err) = gpui_kit::open_window(options, cx, move |window, cx| {
                cx.new(|cx| LoginWindow::new(app, window, cx))
            }) {
                owner.update(cx, |this, cx| {
                    this.login_error = Some(ts!("login.window_failed", error = format!("{err:#}")));
                    cx.notify();
                });
            }
        });
    }
}
