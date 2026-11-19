#![cfg_attr(not(debug_assertions), windows_subsystem = "windows")]

#[macro_use]
extern crate rust_i18n;

rust_i18n::i18n!("locales", fallback = "en");

#[macro_use]
mod i18n;
mod app;
mod audio;
mod bridge;
mod desktop;
mod diagnostics;
mod gif_video;
mod logo;
mod model;
mod notifications;
mod settings;
mod storage;
mod style;
mod timefmt;
mod updates;
mod views;

use std::{borrow::Cow, sync::Arc};

use gpui_kit::component::TitleBar;
use gpui_kit::*;
use reqwest_client::ReqwestClient;

fn main() {
    let mut velopack = velopack::VelopackApp::build();
    #[cfg(windows)]
    {
        velopack = velopack
            .set_app_user_model_id(notifications::APP_ID)
            .on_before_uninstall_fast_callback(|_| {
                let _ = desktop::set_launch_at_login(false);
                notifications::unregister();
            });
    }
    velopack.run();
    diagnostics::init();
    diagnostics::wait_for_crashed_instance();
    let preferences = settings::Settings::load();
    i18n::apply(preferences.language);
    let Some(instance) = desktop::Instance::acquire() else {
        return;
    };
    notifications::register();
    let http_client = ReqwestClient::user_agent("Missive").expect("failed to create HTTP client");
    gpui_kit::application()
        .with_http_client(Arc::new(http_client))
        .with_assets(Assets)
        .run(|cx| {
            gpui_kit::init(cx);
            style::register_fonts(cx);
            style::apply_theme(preferences.theme, None, cx);

            let options = WindowOptions {
                window_bounds: Some(WindowBounds::centered(size(px(1280.), px(820.)), cx)),
                window_min_size: Some(size(px(820.), px(520.))),
                ..TitleBar::window_options()
            };
            let (main_window, _) = gpui_kit::open_window(options, cx, move |window, cx| {
                desktop::set_window_icon(window);
                cx.new(|cx| app::MissiveApp::new(preferences, instance, window, cx))
            })
            .expect("failed to open the main window");

            let main_id = main_window.window_id();
            cx.on_window_closed(move |cx, closed| {
                if closed == main_id {
                    cx.quit();
                }
            })
            .detach();
            if !desktop::started_in_background() {
                cx.activate(true);
            }
        });
}

struct Assets;

const LOGO_PATH: &str = "missive/logo.svg";

impl AssetSource for Assets {
    fn load(&self, path: &str) -> anyhow::Result<Option<Cow<'static, [u8]>>> {
        if path == LOGO_PATH {
            return Ok(Some(Cow::Borrowed(include_bytes!("../assets/logo.svg"))));
        }
        gpui_kit::assets::AllAssets.load(path)
    }

    fn list(&self, path: &str) -> anyhow::Result<Vec<SharedString>> {
        gpui_kit::assets::AllAssets.list(path)
    }
}
