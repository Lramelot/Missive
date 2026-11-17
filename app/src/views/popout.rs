use gpui_kit::assets::IconName;
use gpui_kit::component::{
    Icon, TitleBar, h_flex,
    input::{InputEvent, Textarea, TextareaState},
    message_scroller::{MessageScroller, MessageScrollerState},
    v_flex,
};
use gpui_kit::{prelude::FluentBuilder as _, *};

use super::{sidebar::display_name, widgets::thread_avatar};
use crate::{app::MissiveApp, style::Palette};

#[derive(Clone, PartialEq, Eq)]
struct Snapshot {
    exists: bool,
    count: usize,
    first: Option<String>,
    last: Option<String>,
}

pub struct Popout {
    app: Entity<MissiveApp>,
    thread_id: String,
    scroller: Entity<MessageScrollerState>,
    composer: Entity<TextareaState>,
    last_seen: Snapshot,
    _subscriptions: Vec<Subscription>,
}

impl Popout {
    fn new(
        app: Entity<MissiveApp>,
        thread_id: String,
        window: &mut Window,
        cx: &mut Context<Self>,
    ) -> Self {
        let count = app
            .read(cx)
            .messages
            .get(&thread_id)
            .map(Vec::len)
            .unwrap_or(0);
        let scroller = cx.new(|cx| MessageScrollerState::new(count, cx));
        let composer = cx.new(|cx| {
            TextareaState::new(window, cx)
                .auto_grow(1, 6)
                .submit_on_enter(true)
                .placeholder("Écrire un message")
        });
        let subscriptions = vec![
            cx.observe(&app, |this, _, cx| this.sync(cx)),
            cx.observe_window_activation(window, |this, window, cx| {
                let active = window.is_window_active();
                let thread_id = this.thread_id.clone();
                this.app.update(cx, |app, cx| {
                    app.set_popout_active(&thread_id, Some(active), cx)
                });
            }),
            cx.on_release({
                let app = app.clone();
                let thread_id = thread_id.clone();
                move |_, cx| {
                    app.update(cx, |app, cx| app.set_popout_active(&thread_id, None, cx));
                }
            }),
            cx.subscribe_in(
                &composer,
                window,
                |this, _, event: &InputEvent, window, cx| match event {
                    InputEvent::PressEnter { shift: false, .. } => this.send(window, cx),
                    InputEvent::Change => cx.notify(),
                    _ => {}
                },
            ),
        ];
        let snapshot = Self::snapshot(&app, &thread_id, cx);
        if count == 0 {
            let id = thread_id.clone();
            app.update(cx, |app, cx| app.load_messages(id, cx));
        }
        composer.update(cx, |input, cx| input.focus(window, cx));
        Self {
            app,
            thread_id,
            scroller,
            composer,
            last_seen: snapshot,
            _subscriptions: subscriptions,
        }
    }

    fn snapshot(app: &Entity<MissiveApp>, thread_id: &str, cx: &App) -> Snapshot {
        let list = app.read(cx).messages.get(thread_id);
        Snapshot {
            exists: list.is_some(),
            count: list.map(Vec::len).unwrap_or(0),
            first: list.and_then(|l| l.first().map(|m| m.id.clone())),
            last: list.and_then(|l| l.last().map(|m| m.id.clone())),
        }
    }

    fn sync(&mut self, cx: &mut Context<Self>) {
        let now = Self::snapshot(&self.app, &self.thread_id, cx);
        if now == self.last_seen {
            cx.notify();
            return;
        }
        let before = std::mem::replace(&mut self.last_seen, now.clone());
        if !now.exists {
            let thread_id = self.thread_id.clone();
            let loading = self.app.read(cx).loading_threads.contains(&thread_id);
            if !loading {
                self.app
                    .update(cx, |app, cx| app.load_messages(thread_id, cx));
            }
        }
        let grew = now.count.saturating_sub(before.count);
        let in_sync = self.scroller.read(cx).item_count() == before.count && before.count > 0;
        self.scroller.update(cx, |scroller, cx| {
            if in_sync && grew > 0 && now.first == before.first {
                scroller.append(grew, cx);
            } else if in_sync && grew > 0 && now.last == before.last {
                scroller.prepend(grew, cx);
            } else {
                scroller.reset(now.count, cx);
                scroller.scroll_to_end(cx);
            }
        });
        cx.notify();
    }

    fn send(&mut self, window: &mut Window, cx: &mut Context<Self>) {
        let text = self.composer.read(cx).value().trim().to_string();
        if text.is_empty() {
            return;
        }
        self.composer
            .update(cx, |input, cx| input.clean(window, cx));
        let thread_id = self.thread_id.clone();
        self.app
            .update(cx, |app, cx| app.send_text_to(thread_id, text, None, cx));
        self.scroller
            .update(cx, |scroller, cx| scroller.scroll_to_end(cx));
    }
}

impl Render for Popout {
    fn render(&mut self, window: &mut Window, cx: &mut Context<Self>) -> impl IntoElement {
        let p = Palette::get(cx);
        let app = self.app.clone();
        if !matches!(app.read(cx).screen, crate::app::Screen::Main) {
            window.remove_window();
        }
        let thread = app
            .read(cx)
            .threads
            .iter()
            .find(|thread| thread.id == self.thread_id)
            .cloned();
        let title = thread
            .as_ref()
            .map(display_name)
            .unwrap_or_else(|| "Missive".into());
        window.set_window_title(&title);
        let has_text = !self.composer.read(cx).value().trim().is_empty();
        let thread_id = self.thread_id.clone();
        let accent_hover = p.accent_strong;
        let header_avatar = thread
            .as_ref()
            .map(|thread| thread_avatar(app.read(cx).avatar_spec(thread), 38., p.panel, &p));

        v_flex()
            .size_full()
            .bg(p.canvas)
            .font_family(crate::style::FONT_FAMILY)
            .text_color(p.text)
            .text_size(px(14.))
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
                                .child("Missive"),
                        ),
                ),
            )
            .child(
                v_flex()
                    .flex_1()
                    .min_h_0()
                    .mx(px(10.))
                    .mb(px(10.))
                    .rounded(px(18.))
                    .border_1()
                    .border_color(p.divider)
                    .bg(p.panel)
                    .overflow_hidden()
                    .when_some(thread, |el, thread| {
                        el.child(
                            h_flex()
                                .h(px(64.))
                                .flex_none()
                                .px_5()
                                .gap_3()
                                .border_b_1()
                                .border_color(p.divider)
                                .children(header_avatar)
                                .child(
                                    div()
                                        .text_size(px(17.))
                                        .font_weight(FontWeight::EXTRA_BOLD)
                                        .truncate()
                                        .child(SharedString::from(display_name(&thread))),
                                ),
                        )
                    })
                    .child(
                        div().flex_1().min_h_0().child(
                            MessageScroller::new(
                                "popout-messages",
                                self.scroller.clone(),
                                move |index, _, cx| {
                                    app.read(cx).render_message(&thread_id, index, &app, cx)
                                },
                            )
                            .with_jump_button_label("Derniers messages")
                            .with_row_style(StyleRefinement::default().px(px(0.)).pb(px(0.)))
                            .size_full(),
                        ),
                    )
                    .child(
                        div().flex_none().px_4().pt_2().pb(px(14.)).child(
                            h_flex()
                                .items_end()
                                .gap_1()
                                .p(px(5.))
                                .pl(px(14.))
                                .rounded(px(16.))
                                .border_1()
                                .border_color(p.border_strong)
                                .bg(if p.dark { p.field_bg } else { p.panel })
                                .child(
                                    div()
                                        .flex_1()
                                        .min_w_0()
                                        .min_h(px(40.))
                                        .py(px(9.))
                                        .child(Textarea::new(&self.composer).appearance(false)),
                                )
                                .child(
                                    div()
                                        .id("popout-send")
                                        .flex_none()
                                        .size(px(40.))
                                        .rounded(px(12.))
                                        .flex()
                                        .items_center()
                                        .justify_center()
                                        .bg(if has_text { p.accent } else { p.hover })
                                        .when(has_text, |el| {
                                            el.cursor_pointer().hover(move |s| s.bg(accent_hover))
                                        })
                                        .child(
                                            Icon::new(IconName::SendHorizontal)
                                                .size(px(18.))
                                                .text_color(if has_text {
                                                    p.on_accent
                                                } else {
                                                    p.text_faint
                                                }),
                                        )
                                        .on_click(
                                            cx.listener(|this, _, window, cx| this.send(window, cx)),
                                        ),
                                ),
                        ),
                    ),
            )
    }
}

impl MissiveApp {
    pub(crate) fn open_popout(&mut self, cx: &mut Context<Self>) {
        let Some(thread_id) = self.selected.clone() else {
            return;
        };
        let app = cx.entity();
        cx.defer(move |cx| {
            let options = WindowOptions {
                window_bounds: Some(WindowBounds::centered(size(px(520.), px(720.)), cx)),
                window_min_size: Some(size(px(380.), px(420.))),
                ..TitleBar::window_options()
            };
            let owner = app.clone();
            let result = gpui_kit::open_window(options, cx, move |window, cx| {
                cx.new(|cx| Popout::new(owner, thread_id, window, cx))
            });
            if let Err(err) = result {
                app.update(cx, |this, cx| {
                    this.set_error(err.context("Fenêtre impossible à ouvrir"));
                    cx.notify();
                });
            }
        });
    }
}
