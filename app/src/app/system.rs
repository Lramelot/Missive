use std::{sync::mpsc::Receiver, time::Duration};

use gpui_kit::*;
use serde_json::json;

use super::{Link, MissiveApp, Screen};
use crate::{
    desktop::{self, TrayAction},
    diagnostics,
    notifications::{REPLY_SEPARATOR, ToastAction},
    settings::{Settings, TextSize, ThemePreference},
    style,
    updates::{self, UpdateState},
};

const DIAGNOSTIC_TIMEOUT: Duration = Duration::from_secs(4);

fn merged_draft(existing: &str, text: &str) -> String {
    let existing = existing.trim_end();
    if existing.trim().is_empty() {
        text.to_string()
    } else {
        format!("{existing}\n\n{text}")
    }
}
const POLL_INTERVAL: Duration = Duration::from_millis(80);
const TOAST_DURATION: Duration = Duration::from_secs(4);
const SLOW_TICK: Duration = Duration::from_secs(1);
const UPDATE_INTERVAL: Duration = Duration::from_secs(6 * 60 * 60);

impl MissiveApp {
    pub(super) fn start_system(
        &mut self,
        clicks: Receiver<String>,
        window: &mut Window,
        cx: &mut Context<Self>,
    ) {
        let weak = cx.entity().downgrade();
        window.on_window_should_close(cx, move |window, cx| {
            let hide = weak
                .read_with(cx, |app, _| {
                    app.settings.close_to_tray && app.tray.is_some()
                })
                .unwrap_or(false);
            if hide {
                let _ = weak.update(cx, |app, cx| app.cancel_voice_recording(cx));
                desktop::hide_window(window);
                false
            } else {
                true
            }
        });

        if desktop::started_in_background() && self.tray.is_some() {
            desktop::hide_window(window);
        }
        cx.on_app_quit(|_, _| async { diagnostics::flush() }).detach();
        if diagnostics::was_restarted() {
            self.show_toast("Missive a redémarré après une erreur", cx);
        }

        self._system_task = Some(cx.spawn_in(window, async move |this, cx| {
            loop {
                cx.background_executor().timer(POLL_INTERVAL).await;
                let mut activations = Vec::new();
                while let Ok(raw) = clicks.try_recv() {
                    activations.extend(ToastAction::parse(&raw));
                }
                let result = this.update_in(cx, |this, window, cx| {
                    let actions = this
                        .tray
                        .as_ref()
                        .map(|tray| tray.poll())
                        .unwrap_or_default();
                    for action in actions {
                        match action {
                            TrayAction::Open => {
                                desktop::show_window(window);
                                cx.notify();
                            }
                            TrayAction::Quit => cx.quit(),
                        }
                    }
                    if !this.media_queue.borrow().is_empty() {
                        this.pump_media(cx);
                    }
                    if !this.gif_queue.borrow().is_empty() {
                        this.pump_gifs(cx);
                    }
                    if this
                        .toast
                        .as_ref()
                        .is_some_and(|(_, at)| at.elapsed() > TOAST_DURATION)
                    {
                        this.toast = None;
                        cx.notify();
                    }
                    if this.last_slow_tick.elapsed() >= SLOW_TICK {
                        this.last_slow_tick = std::time::Instant::now();
                        this.tick_planner(cx);
                        this.persist_drafts(cx);
                    }
                    if this.instance.take_show_request() {
                        desktop::show_window(window);
                        cx.notify();
                    }
                    if this.settings.check_updates
                        && this
                            .last_update_check
                            .is_some_and(|at| at.elapsed() > UPDATE_INTERVAL)
                    {
                        this.check_for_update(cx);
                    }
                    let unread = this.total_unread();
                    if let Some(tray) = this.tray.as_mut() {
                        tray.set_unread(unread);
                    }
                    let mut opened = None;
                    for activation in activations.drain(..) {
                        if let Some(thread_id) = this.handle_toast_action(activation, window, cx) {
                            opened = Some(thread_id);
                        }
                    }
                    if let Some(thread_id) = opened {
                        desktop::show_window(window);
                        cx.notify();
                        if matches!(this.screen, Screen::Main) {
                            this.select_thread(thread_id, window, cx);
                        }
                    }
                });
                if result.is_err() {
                    break;
                }
            }
        }));

        if self.settings.check_updates {
            self.check_for_update(cx);
        }
    }

    pub(crate) fn check_for_update(&mut self, cx: &mut Context<Self>) {
        self.last_update_check = Some(std::time::Instant::now());
        if matches!(self.update, Some(UpdateState::Downloading { .. })) {
            return;
        }
        if let Some(version) = updates::pending_restart() {
            if self.update.is_none() {
                eprintln!("[app] update {version} already downloaded, applied at next start");
            }
        }
        cx.spawn(async move |this, cx| {
            let found = cx
                .background_executor()
                .spawn(async move { updates::check() })
                .await;
            let info = match found {
                Ok(Some(info)) => info,
                Ok(None) => return,
                Err(err) => {
                    eprintln!("[app] update check failed: {err:#}");
                    return;
                }
            };
            let version = info.TargetFullRelease.Version.clone();
            let already_ready = this
                .update(cx, |this, cx| {
                    let ready = matches!(&this.update, Some(UpdateState::Ready { version: v, .. }) if *v == version);
                    if !ready {
                        this.update = Some(UpdateState::Downloading {
                            version: version.clone(),
                        });
                        cx.notify();
                    }
                    ready
                })
                .unwrap_or(true);
            if already_ready {
                return;
            }
            let download_info = info.clone();
            let downloaded = cx
                .background_executor()
                .spawn(async move { updates::download(&download_info) })
                .await;
            let _ = this.update(cx, |this, cx| {
                this.update = match downloaded {
                    Ok(()) => Some(UpdateState::Ready {
                        version,
                        info: Box::new(info),
                    }),
                    Err(err) => {
                        eprintln!("[app] update download failed: {err:#}");
                        None
                    }
                };
                cx.notify();
            });
        })
        .detach();
    }

    pub(crate) fn restart_to_update(&mut self, cx: &mut Context<Self>) {
        let Some(UpdateState::Ready { info, .. }) = self.update.clone() else {
            return;
        };
        if let Err(err) = updates::apply_and_restart(&info) {
            self.set_error(err.context("Mise à jour impossible"));
            cx.notify();
        }
    }

    pub(crate) fn update_settings(
        &mut self,
        window: &mut Window,
        cx: &mut Context<Self>,
        change: impl FnOnce(&mut Settings),
    ) {
        let before = self.settings.clone();
        change(&mut self.settings);
        if self.settings == before {
            return;
        }
        if self.settings.launch_at_login != before.launch_at_login {
            if let Err(err) = desktop::set_launch_at_login(self.settings.launch_at_login) {
                self.settings.launch_at_login = before.launch_at_login;
                self.set_error(err.context("Lancement au démarrage impossible"));
            }
        }
        if self.settings.theme != before.theme {
            style::apply_theme(self.settings.theme, Some(window), cx);
        }
        if self.settings.check_updates && !before.check_updates {
            self.check_for_update(cx);
        }
        self.settings.save();
        cx.notify();
    }

    pub(crate) fn set_theme(
        &mut self,
        theme: ThemePreference,
        window: &mut Window,
        cx: &mut Context<Self>,
    ) {
        self.update_settings(window, cx, |settings| settings.theme = theme);
    }

    pub(crate) fn toggle_settings(&mut self, cx: &mut Context<Self>) {
        self.settings_open = !self.settings_open;
        cx.notify();
    }

    fn handle_toast_action(
        &mut self,
        action: ToastAction,
        window: &mut Window,
        cx: &mut Context<Self>,
    ) -> Option<String> {
        match action {
            ToastAction::Open(thread_id) => Some(thread_id),
            ToastAction::Read(thread_id) => {
                if matches!(self.screen, Screen::Main)
                    && self.threads.iter().any(|thread| thread.id == thread_id)
                {
                    self.set_local_unread(&thread_id, false, cx);
                }
                None
            }
            ToastAction::Reply { thread_id, text } => {
                let main = matches!(self.screen, Screen::Main);
                let request = match self.threads.iter().find(|thread| thread.id == thread_id) {
                    Some(thread) => Some(self.is_request_thread(thread)),
                    None => None,
                };
                if main && request == Some(false) {
                    self.send_text_to(thread_id, text, None, cx);
                    cx.notify();
                    return None;
                }
                self.keep_reply_as_draft(&thread_id, &text, window, cx);
                (main && request == Some(true)).then_some(thread_id)
            }
        }
    }

    fn keep_reply_as_draft(
        &mut self,
        thread_id: &str,
        text: &str,
        window: &mut Window,
        cx: &mut Context<Self>,
    ) {
        if self.selected.as_deref() == Some(thread_id) {
            if self.message_ops.editing.is_some() {
                let _ = self
                    .clicks
                    .send(format!("reply:{thread_id}{REPLY_SEPARATOR}{text}"));
                return;
            }
            let current = self.composer.read(cx).value().to_string();
            let merged = merged_draft(&current, text);
            self.composer
                .update(cx, |input, cx| input.set_value(merged, window, cx));
        } else {
            let merged = merged_draft(
                self.drafts.get(thread_id).map(String::as_str).unwrap_or(""),
                text,
            );
            self.drafts.insert(thread_id.to_string(), merged);
        }
        self.persist_drafts(cx);
        self.show_toast("Réponse gardée en brouillon", cx);
    }

    pub(crate) fn set_text_size(
        &mut self,
        size: TextSize,
        window: &mut Window,
        cx: &mut Context<Self>,
    ) {
        if self.settings.text_size == size {
            return;
        }
        self.update_settings(window, cx, |settings| settings.text_size = size);
        self.on_appearance_changed(cx);
    }

    pub(crate) fn set_compact(&mut self, compact: bool, window: &mut Window, cx: &mut Context<Self>) {
        if self.settings.compact == compact {
            return;
        }
        self.update_settings(window, cx, |settings| settings.compact = compact);
        self.on_appearance_changed(cx);
    }

    pub(crate) fn open_logs_folder(&mut self, cx: &mut Context<Self>) {
        let Some(dir) = diagnostics::log_dir() else {
            return;
        };
        if let Err(err) = std::fs::create_dir_all(&dir) {
            eprintln!("[app] log folder unavailable: {err}");
            return;
        }
        cx.open_with_system(&dir);
    }

    pub(crate) fn copy_diagnostic(&mut self, cx: &mut Context<Self>) {
        let bridge = self.bridge();
        let link = match self.link {
            Link::Connecting => "connexion en cours",
            Link::Online => "en ligne",
            Link::Reconnecting => "reconnexion",
            Link::Error => "erreur",
        };
        cx.spawn(async move |this, cx| {
            let bridge_result = match bridge {
                Some(bridge) => {
                    let request = Box::pin(bridge.request("diagnostics", json!({})));
                    let timeout = Box::pin(cx.background_executor().timer(DIAGNOSTIC_TIMEOUT));
                    match futures::future::select(request, timeout).await {
                        futures::future::Either::Left((Ok(value), _)) => Ok(value),
                        futures::future::Either::Left((Err(err), _)) => Err(format!("{err:#}")),
                        futures::future::Either::Right(_) => Err("pas de réponse".to_string()),
                    }
                }
                None => Err("pont non démarré".to_string()),
            };
            let report = cx
                .background_executor()
                .spawn(async move {
                    let input = diagnostics::ReportInput {
                        version: updates::current_version(),
                        installed: updates::is_managed(),
                        demo: crate::bridge::demo_requested(),
                        os: diagnostics::os_description(),
                        link,
                        bridge: bridge_result,
                        lines: diagnostics::recent_lines(diagnostics::REPORT_LINES),
                    };
                    diagnostics::compose_report(&input, chrono::Utc::now())
                })
                .await;
            let _ = this.update(cx, |this, cx| {
                cx.write_to_clipboard(ClipboardItem::new_string(report));
                this.show_toast("Diagnostic copié", cx);
            });
        })
        .detach();
    }

    pub(crate) fn logout(&mut self, window: &mut Window, cx: &mut Context<Self>) {
        let Some(bridge) = self.bridge() else { return };
        cx.spawn_in(window, async move |this, cx| {
            let result = bridge.request("logout", json!({})).await;
            let _ = this.update_in(cx, |this, window, cx| {
                match result {
                    Ok(_) => {
                        this.reset_account_state();
                        this.erase_local_account_data();
                        this.search.update(cx, |input, cx| input.clean(window, cx));
                        this.composer
                            .update(cx, |input, cx| input.clean(window, cx));
                        this.cookie_c_user
                            .update(cx, |input, cx| input.clean(window, cx));
                        this.cookie_xs
                            .update(cx, |input, cx| input.clean(window, cx));
                        this.cookie_datr
                            .update(cx, |input, cx| input.clean(window, cx));
                        if let Some(dir) =
                            dirs::data_dir().map(|dir| dir.join("Missive").join("webview"))
                        {
                            let _ = std::fs::remove_dir_all(dir);
                        }
                        this.screen = Screen::Login;
                    }
                    Err(err) => this.set_error(err.context("Déconnexion impossible")),
                }
                cx.notify();
            });
        })
        .detach();
    }
}

#[cfg(test)]
mod tests {
    use super::merged_draft;

    #[::core::prelude::v1::test]
    fn reply_is_appended_to_existing_draft() {
        assert_eq!(merged_draft("", "Salut"), "Salut");
        assert_eq!(merged_draft("  \n", "Salut"), "Salut");
        assert_eq!(merged_draft("Brouillon\n", "Salut"), "Brouillon\n\nSalut");
    }
}
