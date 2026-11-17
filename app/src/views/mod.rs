mod composer;
mod conversation;
mod login_window;
mod message;
mod message_dialogs;
mod planner;
mod overlays;
mod popout;
mod settings;
mod sidebar;
mod thread_dialogs;
mod voice;
mod widgets;

use gpui_kit::assets::IconName;
use gpui_kit::component::{
    Icon, Sizable as _, TitleBar,
    button::{Button, ButtonVariants as _},
    h_flex,
    input::Input,
    v_flex,
};
use gpui_kit::{prelude::FluentBuilder as _, *};

use crate::{
    app::{Link, MissiveApp, Screen},
    style::Palette,
};

pub use widgets::{AvatarSpec, link_color, link_label};

impl Render for MissiveApp {
    fn render(&mut self, window: &mut Window, cx: &mut Context<Self>) -> impl IntoElement {
        let p = Palette::get(cx);
        let unread = self.total_unread();
        let title = if unread > 0 {
            format!("({unread}) Missive")
        } else {
            "Missive".to_string()
        };
        if title != self.window_title {
            window.set_window_title(&title);
            self.window_title = title;
        }
        if let Some(tray) = self.tray.as_mut() {
            tray.set_unread(unread);
        }
        crate::desktop::set_taskbar_badge(window, unread);

        let content = match &self.screen {
            Screen::Starting => {
                centered(div().text_color(p.text_muted).child("Démarrage…")).into_any_element()
            }
            Screen::Fatal(message) => centered(
                v_flex()
                    .gap_2()
                    .max_w(px(520.))
                    .child(
                        div()
                            .text_size(px(18.))
                            .font_weight(FontWeight::SEMIBOLD)
                            .child("Impossible de démarrer"),
                    )
                    .child(div().text_color(p.text_muted).child(message.clone())),
            )
            .into_any_element(),
            Screen::Login => self.render_login(&p, cx).into_any_element(),
            Screen::Main => h_flex()
                .flex_1()
                .min_h_0()
                .pr(px(10.))
                .pb(px(10.))
                .child(self.render_rail(&p, cx))
                .child(
                    h_flex()
                        .flex_1()
                        .min_w_0()
                        .h_full()
                        .rounded(px(18.))
                        .border_1()
                        .border_color(p.divider)
                        .bg(p.panel)
                        .overflow_hidden()
                        .child(self.render_sidebar(&p, cx))
                        .child(self.render_conversation(&p, cx))
                        .when(self.info_open && self.selected_thread().is_some(), |el| {
                            el.child(
                                div()
                                    .h_full()
                                    .flex_none()
                                    .border_l_1()
                                    .border_color(p.divider)
                                    .child(self.render_info(&p, cx)),
                            )
                        }),
                )
                .into_any_element(),
        };

        let main = matches!(self.screen, Screen::Main);
        let message_dialogs = if main { self.render_message_dialogs(&p, cx) } else { None };
        let thread_dialogs = if main { self.render_thread_dialogs(&p, cx) } else { None };
        let planner_dialogs = if main { self.render_planner_dialogs(&p, cx) } else { None };
        v_flex()
            .size_full()
            .relative()
            .bg(p.canvas)
            .font_family(crate::style::FONT_FAMILY)
            .text_color(p.text)
            .text_size(px(14.))
            .capture_key_down(cx.listener(|this, event: &KeyDownEvent, window, cx| {
                if this.handle_shortcut(event, window, cx) {
                    cx.stop_propagation();
                }
            }))
            .child(self.render_title_bar(&p, cx))
            .child(content)
            .when(main && self.gallery.is_some(), |el| {
                el.child(self.render_gallery(&p, cx))
            })
            .when(main && self.switcher.is_some(), |el| {
                el.child(self.render_switcher(&p, cx))
            })
            .when(main && self.settings_open, |el| {
                el.child(self.render_settings(&p, cx))
            })
            .children(message_dialogs)
            .children(thread_dialogs)
            .children(planner_dialogs)
            .when(main && self.lightbox.is_some(), |el| {
                el.child(self.render_lightbox(cx))
            })
            .when(self.pin_open, |el| el.child(self.render_pin_dialog(&p, cx)))
            .when_some(self.toast.as_ref().map(|(text, _)| text.clone()), |el, text| {
                el.child(
                    div()
                        .absolute()
                        .bottom(px(28.))
                        .left_0()
                        .right_0()
                        .flex()
                        .justify_center()
                        .child(
                            div()
                                .id("toast")
                                .max_w(px(520.))
                                .px_4()
                                .py_2p5()
                                .rounded(px(14.))
                                .bg(p.text)
                                .text_color(p.panel)
                                .text_size(px(13.))
                                .shadow_lg()
                                .cursor_pointer()
                                .on_click(cx.listener(|this, _, _, cx| {
                                    this.toast = None;
                                    cx.notify();
                                }))
                                .child(text),
                        ),
                )
            })
    }
}

impl MissiveApp {
    fn handle_shortcut(
        &mut self,
        event: &KeyDownEvent,
        window: &mut Window,
        cx: &mut Context<Self>,
    ) -> bool {
        if !matches!(self.screen, Screen::Main) {
            return false;
        }
        let keystroke = &event.keystroke;
        let key = keystroke.key.as_str();
        let primary = if cfg!(target_os = "macos") {
            keystroke.modifiers.platform
        } else {
            keystroke.modifiers.control
        };
        let alt = keystroke.modifiers.alt;
        let plain = !primary && !alt && !keystroke.modifiers.shift;

        if self.lightbox.is_some() {
            match key {
                "escape" => self.close_lightbox(cx),
                "left" => self.step_lightbox(-1, cx),
                "right" => self.step_lightbox(1, cx),
                "0" => self.reset_lightbox_zoom(cx),
                "=" | "+" => self.zoom_lightbox(1, cx),
                "-" => self.zoom_lightbox(-1, cx),
                "enter" => {}
                _ => return false,
            }
            return true;
        }
        if (self.gallery.is_some()
            || (self.settings_open && !self.settings_input_focused(window, cx)))
            && key == "enter"
        {
            return true;
        }
        if key == "enter" {
            if self.thread_overlay_enter(window, cx) {
                return true;
            }
            if self.message_overlay_blocks_enter()
                || (self.message_dialog_open() && !self.message_dialog_input_focused(window, cx))
            {
                return true;
            }
        }
        if self.mention_popup_open() {
            match key {
                "up" if plain => self.move_mention(-1, cx),
                "down" if plain => self.move_mention(1, cx),
                "enter" | "tab" if !keystroke.modifiers.shift => {
                    let index = self.mention_selected;
                    self.swallow_enter_until =
                        Some(std::time::Instant::now() + std::time::Duration::from_millis(150));
                    self.accept_mention(index, window, cx);
                }
                "escape" => self.close_mentions(cx),
                _ => return false,
            }
            return true;
        }
        if key == "escape"
            && (self.close_planner_dialogs(cx)
                || self.close_message_dialogs(cx)
                || self.close_thread_dialogs(cx)
                || self.close_voice(cx)
                || self.close_find(cx))
        {
            return true;
        }
        if self.switcher.is_some() {
            match key {
                "escape" => self.close_switcher(cx),
                "up" if plain => self.move_switcher(-1, cx),
                "down" if plain => self.move_switcher(1, cx),
                "k" if primary => self.close_switcher(cx),
                _ => return false,
            }
            return true;
        }
        match key {
            "k" if primary => self.open_switcher(window, cx),
            "f" if primary && self.selected.is_some() => self.toggle_find(window, cx),
            "=" | "+" if primary => self.step_text_size(1, cx),
            "-" if primary => self.step_text_size(-1, cx),
            "0" if primary => self.step_text_size(0, cx),
            "up" if alt => self.step_thread(-1, window, cx),
            "down" if alt => self.step_thread(1, window, cx),
            "," if primary => self.toggle_settings(cx),
            "escape" if self.settings_open => self.toggle_settings(cx),
            "escape" if self.gallery.is_some() => self.close_gallery(cx),
            "escape" if self.react_picker.is_some() => {
                self.react_picker = None;
                cx.notify();
            }
            "escape" if self.reaction_details.is_some() => {
                self.reaction_details = None;
                cx.notify();
            }
            "escape" if self.reply_to.is_some() => self.cancel_reply(cx),
            "escape" if self.info_open => self.toggle_info(cx),
            _ => return false,
        }
        true
    }
}

fn centered(content: impl IntoElement) -> Div {
    div()
        .flex_1()
        .flex()
        .items_center()
        .justify_center()
        .child(content)
}

fn logo(size: f32) -> Img {
    img("missive/logo.svg").size(px(size)).flex_none()
}

impl MissiveApp {
    fn render_title_bar(&self, p: &Palette, cx: &mut Context<Self>) -> impl IntoElement {
        let ready = matches!(self.update, Some(crate::updates::UpdateState::Ready { .. }));
        let accent_hover = p.accent_strong;
        TitleBar::new().bg(p.canvas).border_color(p.canvas).child(
            h_flex()
                .gap_2()
                .px_2()
                .child(logo(18.))
                .child(
                    div()
                        .text_size(px(13.))
                        .font_weight(FontWeight::BOLD)
                        .child("Missive"),
                )
                .when(
                    matches!(self.screen, Screen::Main) && self.link != Link::Online,
                    |el| {
                        el.child(
                            h_flex()
                                .ml_3()
                                .gap_1p5()
                                .text_size(px(12.))
                                .text_color(p.text_muted)
                                .child(
                                    div()
                                        .size(px(7.))
                                        .rounded_full()
                                        .bg(link_color(self.link, p)),
                                )
                                .child(link_label(self.link, self.link_detail.as_ref())),
                        )
                    },
                )
                .when(ready, |el| {
                    el.child(
                        div()
                            .id("titlebar-update")
                            .ml_3()
                            .px_2p5()
                            .h(px(22.))
                            .flex()
                            .items_center()
                            .rounded_full()
                            .bg(p.accent)
                            .text_color(p.on_accent)
                            .text_size(px(11.5))
                            .font_weight(FontWeight::SEMIBOLD)
                            .cursor_pointer()
                            .hover(move |s| s.bg(accent_hover))
                            .child("Mise à jour prête · Redémarrer")
                            .on_mouse_down(MouseButton::Left, |_, _, cx| cx.stop_propagation())
                            .on_click(cx.listener(|this, _, _, cx| this.restart_to_update(cx))),
                    )
                }),
        )
    }

    fn render_pin_dialog(&self, p: &Palette, cx: &mut Context<Self>) -> impl IntoElement {
        div()
            .absolute()
            .inset_0()
            .flex()
            .items_center()
            .justify_center()
            .bg(hsla(0., 0., 0., 0.4))
            .child(
                v_flex()
                    .id("pin-dialog")
                    .w(px(440.))
                    .gap_4()
                    .p_7()
                    .rounded(px(18.))
                    .border_1()
                    .border_color(p.divider)
                    .bg(p.panel)
                    .shadow_lg()
                    .on_mouse_down(MouseButton::Left, |_, _, cx| cx.stop_propagation())
                    .child(
                        div()
                            .size(px(48.))
                            .rounded(px(14.))
                            .bg(p.accent_soft)
                            .flex()
                            .items_center()
                            .justify_center()
                            .child(
                                Icon::new(IconName::Lock)
                                    .size(px(22.))
                                    .text_color(p.accent_text),
                            ),
                    )
                    .child(
                        div()
                            .text_size(px(19.))
                            .font_weight(FontWeight::EXTRA_BOLD)
                            .child("Restaurer l'historique chiffré"),
                    )
                    .child(
                        div()
                            .text_size(px(13.5))
                            .line_height(px(21.))
                            .text_color(p.text_muted)
                            .child("Tes conversations privées sont chiffrées de bout en bout. Pour retrouver leurs messages, Missive restaure ta sauvegarde Messenger avec ton code PIN, celui que messenger.com demande sur un nouvel appareil."),
                    )
                    .child(Input::new(&self.pin_input).large())
                    .when_some(self.pin_error.clone(), |el, error| {
                        el.child(div().text_size(px(13.)).text_color(p.danger).child(error))
                    })
                    .child(
                        div()
                            .text_size(px(12.))
                            .text_color(p.text_faint)
                            .child("Meta limite le nombre d'essais. En cas de doute, vérifie ton PIN sur messenger.com."),
                    )
                    .child(
                        h_flex()
                            .justify_end()
                            .gap_2()
                            .child(
                                Button::new("pin-later")
                                    .ghost()
                                    .label("Plus tard")
                                    .on_click(cx.listener(|this, _, window, cx| this.close_pin(window, cx))),
                            )
                            .child(
                                Button::new("pin-restore")
                                    .primary()
                                    .label("Restaurer")
                                    .loading(self.pin_busy)
                                    .on_click(cx.listener(|this, _, _, cx| this.restore_history(cx))),
                            ),
                    ),
            )
    }

    fn render_login(&self, p: &Palette, cx: &mut Context<Self>) -> impl IntoElement {
        let field = |label: &'static str, input: &Entity<_>| {
            v_flex()
                .gap_1p5()
                .child(
                    div()
                        .text_size(px(12.))
                        .font_weight(FontWeight::SEMIBOLD)
                        .text_color(p.text_muted)
                        .child(label),
                )
                .child(Input::new(input))
        };

        centered(
            v_flex()
                .w(px(420.))
                .gap_6()
                .p_8()
                .rounded(px(18.))
                .border_1()
                .border_color(p.divider)
                .bg(p.panel)
                .shadow_lg()
                .child(
                    v_flex()
                        .gap_4()
                        .child(logo(52.))
                        .child(
                            v_flex()
                                .gap_1()
                                .child(
                                    div()
                                        .text_size(px(26.))
                                        .font_weight(FontWeight::EXTRA_BOLD)
                                        .child("Bienvenue sur Missive"),
                                )
                                .child(
                                    div()
                                        .text_size(px(14.))
                                        .text_color(p.text_muted)
                                        .child("Messenger, en application native."),
                                ),
                        ),
                )
                .child(
                    Button::new("login-web")
                        .primary()
                        .large()
                        .w_full()
                        .label("Se connecter avec Messenger")
                        .loading(self.login_busy && !self.manual_login)
                        .on_click(cx.listener(|this, _, _, cx| this.open_login_window(cx))),
                )
                .when_some(self.login_error.clone(), |el, error| {
                    el.child(div().text_size(px(13.)).text_color(p.danger).child(error))
                })
                .child(
                    div()
                        .id("toggle-manual")
                        .text_size(px(12.5))
                        .font_weight(FontWeight::SEMIBOLD)
                        .text_color(p.accent_text)
                        .cursor_pointer()
                        .child(if self.manual_login {
                            "Masquer la saisie manuelle"
                        } else {
                            "Utiliser des cookies à la place"
                        })
                        .on_click(cx.listener(|this, _, _, cx| {
                            this.manual_login = !this.manual_login;
                            cx.notify();
                        })),
                )
                .when(self.manual_login, |el| {
                    el.child(
                        div()
                            .text_size(px(13.))
                            .line_height(px(20.))
                            .text_color(p.text_muted)
                            .child("Connecte-toi sur messenger.com dans ton navigateur, puis F12 → Application → Cookies, et copie ces trois valeurs."),
                    )
                    .child(
                        v_flex()
                            .gap_3()
                            .child(field("c_user", &self.cookie_c_user))
                            .child(field("xs", &self.cookie_xs))
                            .child(field("datr", &self.cookie_datr)),
                    )
                    .child(
                        Button::new("login")
                            .outline()
                            .w_full()
                            .label("Se connecter avec ces cookies")
                            .loading(self.login_busy && self.manual_login)
                            .on_click(cx.listener(|this, _, _, cx| this.login(cx))),
                    )
                })
                .child(
                    div()
                        .text_size(px(12.))
                        .text_color(p.text_faint)
                        .child("Client non officiel. Meta peut demander une vérification du compte."),
                ),
        )
    }
}
