use std::{collections::HashMap, time::Duration};

use gpui_kit::component::{TitleBar, h_flex, v_flex};
use gpui_kit::*;
use gpui_webview::WebView;

use crate::{app::MissiveApp, style::Palette};

const LOGIN_URL: &str = "https://www.messenger.com/login/";
const COOKIE_URL: &str = "https://www.messenger.com/";
const REQUIRED: [&str; 3] = ["c_user", "xs", "datr"];
const POLL: Duration = Duration::from_millis(800);

pub struct LoginWindow {
    webview: Option<Entity<WebView>>,
    error: Option<SharedString>,
    _context: Box<wry::WebContext>,
    _poll: Option<Task<()>>,
}

fn session_cookies(webview: &WebView) -> Option<HashMap<String, String>> {
    let cookies = webview.raw().cookies_for_url(COOKIE_URL).ok()?;
    let found: HashMap<String, String> = cookies
        .iter()
        .filter(|cookie| !cookie.value().is_empty())
        .map(|cookie| (cookie.name().to_string(), cookie.value().to_string()))
        .collect();
    REQUIRED
        .iter()
        .all(|name| found.contains_key(*name))
        .then_some(found)
}

impl LoginWindow {
    fn new(app: WeakEntity<MissiveApp>, window: &mut Window, cx: &mut Context<Self>) -> Self {
        let data_dir = dirs::data_dir().map(|dir| dir.join("Missive").join("webview"));
        let mut context = Box::new(wry::WebContext::new(data_dir));
        let builder =
            wry::WebViewBuilder::new_with_web_context(&mut context).with_url("about:blank");
        let (webview, error) = match WebView::build(builder, window, cx) {
            Ok(mut webview) => {
                let _ = webview.raw().clear_all_browsing_data();
                webview.load_url(LOGIN_URL);
                (Some(cx.new(|_| webview)), None)
            }
            Err(err) => (
                None,
                Some(format!("Navigateur intégré indisponible : {err}").into()),
            ),
        };

        let poll = webview.clone().map(|webview| {
            cx.spawn_in(window, async move |this, cx| {
                let mut baseline: Option<Option<String>> = None;
                loop {
                    cx.background_executor().timer(POLL).await;
                    let cookies = webview.read_with(cx, |webview, _| session_cookies(webview));
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
            })
        });

        Self {
            webview,
            error,
            _context: context,
            _poll: poll,
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
                .text_color(p.danger)
                .child(error.clone().unwrap_or_default())
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
                                .child("Connexion à Messenger"),
                        ),
                ),
            )
            .child(
                h_flex()
                    .flex_none()
                    .px_4()
                    .py_2()
                    .bg(p.accent_soft)
                    .border_b_1()
                    .border_color(p.divider)
                    .text_size(px(12.5))
                    .text_color(p.text_muted)
                    .child("Connecte-toi à Messenger ici. Missive récupère la session automatiquement ; ton mot de passe reste dans cette page."),
            )
            .child(body)
    }
}

impl MissiveApp {
    pub(crate) fn open_login_window(&mut self, cx: &mut Context<Self>) {
        let app = cx.entity().downgrade();
        let options = WindowOptions {
            window_bounds: Some(WindowBounds::centered(size(px(480.), px(720.)), cx)),
            window_min_size: Some(size(px(380.), px(520.))),
            ..TitleBar::window_options()
        };
        if let Err(err) = gpui_kit::open_window(options, cx, move |window, cx| {
            cx.new(|cx| LoginWindow::new(app, window, cx))
        }) {
            self.login_error = Some(format!("Fenêtre de connexion impossible : {err:#}").into());
            cx.notify();
        }
    }
}
