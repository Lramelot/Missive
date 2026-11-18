use gpui_kit::assets::IconName;
use gpui_kit::component::{
    Icon, Sizable as _, h_flex,
    input::{Input, Textarea},
    tooltip::Tooltip,
    v_flex,
};
use gpui_kit::{prelude::FluentBuilder as _, *};

use super::conversation::{COLUMN_GUTTER, COLUMN_WIDTH};
use super::message::theme_colors;
use super::widgets::icon_button;
use crate::{
    app::MissiveApp,
    model::{Message, Thread},
    style::Palette,
};

fn one_line(text: &str) -> String {
    text.split_whitespace().collect::<Vec<_>>().join(" ")
}

pub(super) fn composer_field(p: &Palette) -> Div {
    h_flex()
        .w_full()
        .items_end()
        .gap(px(4.))
        .pl(px(6.))
        .pr(px(5.))
        .py(px(5.))
        .rounded(px(16.))
        .border_1()
        .border_color(p.border_strong)
        .bg(if p.dark { p.field_bg } else { p.panel })
}

fn context_bar(icon: IconName, title: String, body: String, p: &Palette) -> Div {
    h_flex()
        .w_full()
        .px(px(14.))
        .py(px(8.))
        .gap_2p5()
        .rounded(px(12.))
        .bg(p.chip_bg)
        .child(Icon::new(icon).size(px(15.)).text_color(p.accent_text))
        .child(
            v_flex()
                .flex_1()
                .min_w_0()
                .child(
                    div()
                        .text_size(px(12.))
                        .font_weight(FontWeight::BOLD)
                        .text_color(p.accent_text)
                        .child(SharedString::from(title)),
                )
                .child(
                    div()
                        .text_size(px(12.5))
                        .text_color(p.text_muted)
                        .truncate()
                        .child(SharedString::from(body)),
                ),
        )
}

fn message_summary(message: &Message) -> String {
    if message.text.is_empty() {
        crate::app::describe_attachments(message)
    } else {
        one_line(&message.text)
    }
}

fn square_button(id: &'static str, bg: Hsla, hover: Hsla) -> Stateful<Div> {
    div()
        .id(id)
        .flex_none()
        .size(px(40.))
        .rounded(px(12.))
        .flex()
        .items_center()
        .justify_center()
        .bg(bg)
        .cursor_pointer()
        .hover(move |style| style.bg(hover))
}

impl MissiveApp {
    pub(super) fn render_composer_area(
        &self,
        thread: &Thread,
        p: &Palette,
        cx: &mut Context<Self>,
    ) -> AnyElement {
        let view = cx.entity();
        let editing = self.message_ops.editing.as_ref().map(|e| e.message.clone());
        let has_text =
            !self.composer.read(cx).value().trim().is_empty() || !self.pending_files.is_empty();
        let (send_bg, send_fg, send_hover) = theme_colors(thread, p);
        let hover = p.hover;
        let paste_view = view.clone();
        let mention_popup = self
            .render_mention_popup(p, cx)
            .or_else(|| self.render_snippet_popup(p, cx));
        let scheduled_bar = self.render_scheduled_bar(thread, p, cx);
        let discreet_bar = self.render_discreet_bar(thread, p, cx);
        let schedule_button = (has_text && editing.is_none()).then(|| {
            icon_button(
                "schedule-send",
                IconName::Clock,
                self.planner.schedule_menu.is_some(),
                p,
            )
            .on_click(cx.listener(|this, event: &ClickEvent, _, cx| {
                this.open_schedule_menu(event.position(), cx)
            }))
            .into_any_element()
        });
        let recording_bar = self.render_recording_bar(p, cx);
        let voice_button = if recording_bar.is_none() {
            self.render_voice_button(thread, p, cx)
        } else {
            None
        };
        let like = self.like_emoji(thread);
        let poll_button = (recording_bar.is_none() && editing.is_none() && self.can_create_poll())
            .then(|| {
                icon_button("create-poll", IconName::ChartBar, false, p)
                    .on_click(cx.listener(|this, _, window, cx| this.open_poll_dialog(window, cx)))
                    .into_any_element()
            });
        let emoji_button = recording_bar.is_none().then(|| {
            icon_button(
                "emoji-picker",
                IconName::FaceSlightlySmiling,
                self.find.emoji.is_some(),
                p,
            )
            .tooltip(|window, cx| Tooltip::new(ts!("composer.emoji.tooltip")).build(window, cx))
            .on_click(cx.listener(|this, _, window, cx| this.toggle_emoji_picker(window, cx)))
            .into_any_element()
        });
        let emoji_picker = self.render_emoji_picker(p, cx);
        let scale = self.settings.text_size.scale();

        let context: Option<AnyElement> = if let Some(message) = &editing {
            Some(
                context_bar(
                    IconName::Pencil,
                    t!("composer.context.editing").to_string(),
                    message_summary(message),
                    p,
                )
                .child(
                    icon_button("cancel-edit", IconName::X, false, p).on_click(
                        cx.listener(|this, _, window, cx| this.cancel_edit(Some(window), cx)),
                    ),
                )
                .into_any_element(),
            )
        } else {
            self.reply_to.as_ref().map(|quoted| {
                context_bar(
                    IconName::Reply,
                    if quoted.is_outgoing {
                        t!("composer.context.reply_to_self").to_string()
                    } else {
                        t!("composer.context.reply_to", name = &quoted.sender_name).to_string()
                    },
                    message_summary(quoted),
                    p,
                )
                .child(
                    icon_button("cancel-reply", IconName::X, false, p)
                        .on_click(cx.listener(|this, _, _, cx| this.cancel_reply(cx))),
                )
                .into_any_element()
            })
        };

        let pending: Vec<AnyElement> = if editing.is_none() {
            self.pending_files
                .iter()
                .enumerate()
                .map(|(index, path)| pending_chip(index, path, p, cx))
                .collect()
        } else {
            Vec::new()
        };

        let action_button = if editing.is_some() {
            square_button("save-edit", send_bg, send_hover)
                .on_click(cx.listener(|this, _, window, cx| this.send_current(window, cx)))
                .child(Icon::new(IconName::Check).size(px(18.)).text_color(send_fg))
                .into_any_element()
        } else if has_text {
            square_button("send", send_bg, send_hover)
                .on_click(cx.listener(|this, _, window, cx| this.send_current(window, cx)))
                .on_mouse_down(
                    MouseButton::Right,
                    cx.listener(|this, event: &MouseDownEvent, _, cx| {
                        cx.stop_propagation();
                        this.open_schedule_menu(event.position, cx);
                    }),
                )
                .child(Icon::new(IconName::Send).size(px(18.)).text_color(send_fg))
                .into_any_element()
        } else {
            square_button("like", transparent_black(), hover)
                .text_size(px(22.))
                .on_click(cx.listener(|this, _, _, cx| this.send_like(cx)))
                .child(SharedString::from(like))
                .into_any_element()
        };

        let field = match recording_bar {
            Some(bar) => bar,
            None => composer_field(p)
                .when(editing.is_none(), |el| {
                    el.child(
                        icon_button("attach", IconName::Paperclip, false, p)
                            .on_click(cx.listener(|this, _, _, cx| this.pick_files(cx))),
                    )
                })
                .children(poll_button)
                .children(emoji_button)
                .child(
                    div()
                        .flex_1()
                        .min_w_0()
                        .min_h(px(40.))
                        .flex()
                        .flex_col()
                        .justify_center()
                        .px(px(6.))
                        .py(px(2.))
                        .text_size(px(14.5 * scale))
                        .capture_key_down(cx.listener(|this, event: &KeyDownEvent, window, cx| {
                            let keystroke = &event.keystroke;
                            let modifiers = keystroke.modifiers;
                            let plain = !modifiers.control
                                && !modifiers.alt
                                && !modifiers.shift
                                && !modifiers.platform;
                            if keystroke.key == "up"
                                && plain
                                && this.composer.read(cx).value().is_empty()
                                && this.edit_last_message(window, cx)
                            {
                                cx.stop_propagation();
                            }
                        }))
                        .child(Textarea::new(&self.composer).appearance(false).on_paste(
                            move |item, _, cx| {
                                paste_view.update(cx, |this, cx| this.paste_image(item, cx))
                            },
                        )),
                )
                .when(editing.is_none(), |el| el.children(voice_button))
                .children(schedule_button)
                .child(action_button)
                .into_any_element(),
        };

        div()
            .w_full()
            .flex_none()
            .flex()
            .justify_center()
            .px(px(COLUMN_GUTTER))
            .pt_2()
            .pb(px(18.))
            .child(
                v_flex()
                    .relative()
                    .w_full()
                    .max_w(px(COLUMN_WIDTH))
                    .gap(px(6.))
                    .children(emoji_picker)
                    .children(mention_popup)
                    .children(scheduled_bar)
                    .children(discreet_bar)
                    .children(context)
                    .when(!pending.is_empty(), |el| {
                        el.child(h_flex().pt_1().gap_2().flex_wrap().children(pending))
                    })
                    .child(field),
            )
            .into_any_element()
    }

    fn render_emoji_picker(&self, p: &Palette, cx: &mut Context<Self>) -> Option<AnyElement> {
        let picker = self.find.emoji.as_ref()?;
        let searching = !picker.search.read(cx).value().trim().is_empty();
        let results = self.emoji_results(cx);
        let hover = p.hover;
        let tabs: Vec<AnyElement> = MissiveApp::emoji_tabs()
            .iter()
            .enumerate()
            .map(|(index, tab)| {
                let tab = *tab;
                let active = !searching && picker.tab == tab;
                div()
                    .id(("emoji-tab", index))
                    .size(px(30.))
                    .flex_none()
                    .rounded(px(10.))
                    .flex()
                    .items_center()
                    .justify_center()
                    .cursor_pointer()
                    .when(active, |el| el.bg(p.accent_soft))
                    .when(!active, |el| el.hover(move |style| style.bg(hover)))
                    .tooltip(move |window, cx| Tooltip::new(tab.label()).build(window, cx))
                    .child(Icon::new(tab.icon()).size(px(16.)).text_color(if active {
                        p.accent_text
                    } else {
                        p.text_muted
                    }))
                    .on_click(cx.listener(move |this, _, _, cx| this.select_emoji_tab(tab, cx)))
                    .into_any_element()
            })
            .collect();
        let title = if searching {
            ts!("composer.emoji.results")
        } else {
            picker.tab.label()
        };
        let empty = if searching {
            ts!("composer.emoji.none_found")
        } else {
            ts!("composer.emoji.recent_hint")
        };
        let cells: Vec<AnyElement> = results
            .iter()
            .enumerate()
            .map(|(index, emoji)| {
                let emoji: &'static emojis::Emoji = emoji;
                div()
                    .id(("emoji", index))
                    .size(px(38.))
                    .rounded(px(10.))
                    .flex()
                    .items_center()
                    .justify_center()
                    .text_size(px(22.))
                    .cursor_pointer()
                    .hover(move |style| style.bg(hover))
                    .child(emoji.as_str())
                    .on_hover(cx.listener(move |this, hovered: &bool, _, cx| {
                        this.hover_emoji(emoji, *hovered, cx)
                    }))
                    .on_click(cx.listener(move |this, _, window, cx| {
                        this.insert_emoji(emoji.as_str(), window, cx)
                    }))
                    .into_any_element()
            })
            .collect();
        let preview = match picker.hovered {
            Some(emoji) => h_flex()
                .gap_2p5()
                .child(div().text_size(px(26.)).child(emoji.as_str()))
                .child(
                    div()
                        .min_w_0()
                        .truncate()
                        .text_size(px(12.5))
                        .text_color(p.text_muted)
                        .child(SharedString::from(match emoji.shortcode() {
                            Some(code) => format!(":{code}:"),
                            None => emoji.name().to_string(),
                        })),
                )
                .into_any_element(),
            None => div()
                .text_size(px(12.5))
                .text_color(p.text_faint)
                .child(ts!("composer.emoji.hover_hint"))
                .into_any_element(),
        };
        Some(
            v_flex()
                .id("emoji-popover")
                .absolute()
                .left_0()
                .bottom(relative(1.))
                .mb_2()
                .w(px(352.))
                .max_w_full()
                .rounded(px(16.))
                .bg(p.panel)
                .border_1()
                .border_color(p.divider)
                .shadow_lg()
                .occlude()
                .on_mouse_down_out(cx.listener(|this, _: &MouseDownEvent, _, cx| {
                    this.close_emoji_picker(cx);
                }))
                .child(
                    div()
                        .px_3()
                        .pt_3()
                        .child(Input::new(&picker.search).small()),
                )
                .child(
                    h_flex()
                        .px_2()
                        .pt_2()
                        .gap(px(2.))
                        .justify_between()
                        .children(tabs),
                )
                .child(
                    div()
                        .px_3()
                        .pt_2()
                        .pb_1()
                        .text_size(px(11.5))
                        .font_weight(FontWeight::BOLD)
                        .text_color(p.text_muted)
                        .child(title),
                )
                .child(
                    div()
                        .id("emoji-grid")
                        .h(px(220.))
                        .px_2()
                        .overflow_y_scroll()
                        .track_scroll(&picker.scroll)
                        .child(if cells.is_empty() {
                            div()
                                .pt_6()
                                .px_4()
                                .text_center()
                                .text_size(px(12.5))
                                .text_color(p.text_faint)
                                .child(empty)
                                .into_any_element()
                        } else {
                            div()
                                .grid()
                                .grid_cols(8)
                                .gap(px(2.))
                                .children(cells)
                                .into_any_element()
                        }),
                )
                .child(
                    h_flex()
                        .h(px(44.))
                        .px_3()
                        .border_t_1()
                        .border_color(p.divider)
                        .child(preview),
                )
                .into_any_element(),
        )
    }

    fn render_scheduled_bar(
        &self,
        thread: &Thread,
        p: &Palette,
        cx: &mut Context<Self>,
    ) -> Option<AnyElement> {
        let (count, overdue, next) = self.planner.thread_summary(&thread.id)?;
        let text = if overdue > 0 {
            tn!("composer.scheduled.overdue", count, overdue = overdue).to_string()
        } else if count == 1 {
            t!("composer.scheduled.single", when = self.planner_when(next)).to_string()
        } else {
            t!(
                "composer.scheduled.multiple",
                count = count,
                when = self.planner_when(next)
            )
            .to_string()
        };
        let accent = if overdue > 0 { p.danger } else { p.accent_text };
        Some(
            h_flex()
                .id("scheduled-bar")
                .w_full()
                .px(px(14.))
                .py(px(7.))
                .gap_2p5()
                .rounded(px(12.))
                .bg(p.chip_bg)
                .text_size(px(12.5))
                .child(
                    Icon::new(if overdue > 0 {
                        IconName::ClockAlert
                    } else {
                        IconName::Clock
                    })
                    .size(px(15.))
                    .text_color(accent),
                )
                .child(
                    div()
                        .flex_1()
                        .min_w_0()
                        .truncate()
                        .text_color(p.text_muted)
                        .child(SharedString::from(text)),
                )
                .child(
                    div()
                        .id("scheduled-bar-open")
                        .flex_none()
                        .font_weight(FontWeight::SEMIBOLD)
                        .text_color(accent)
                        .cursor_pointer()
                        .hover(|style| style.underline())
                        .child(ts!("composer.scheduled.view"))
                        .on_click(cx.listener(|this, _, _, cx| this.open_scheduled(cx))),
                )
                .into_any_element(),
        )
    }

    fn render_discreet_bar(
        &self,
        thread: &Thread,
        p: &Palette,
        cx: &mut Context<Self>,
    ) -> Option<AnyElement> {
        if self.settings.send_read_receipts
            || thread.unread_count == 0
            || self.is_request_thread(thread)
        {
            return None;
        }
        let thread_id = thread.id.clone();
        Some(
            h_flex()
                .w_full()
                .px(px(14.))
                .py(px(5.))
                .gap_2p5()
                .rounded(px(12.))
                .bg(p.signal_soft)
                .text_size(px(12.5))
                .text_color(p.signal_text)
                .child(Icon::new(IconName::EyeOff).size(px(14.)).text_color(p.signal))
                .child(
                    div()
                        .flex_1()
                        .min_w_0()
                        .truncate()
                        .child(ts!("composer.discreet.notice")),
                )
                .child(
                    div()
                        .id("discreet-mark-read")
                        .flex_none()
                        .font_weight(FontWeight::SEMIBOLD)
                        .cursor_pointer()
                        .hover(|style| style.underline())
                        .child(ts!("composer.discreet.mark_read"))
                        .on_click(cx.listener(move |this, _, _, cx| {
                            this.mark_thread_read(&thread_id, cx);
                            cx.notify();
                        })),
                )
                .into_any_element(),
        )
    }

    fn render_snippet_popup(&self, p: &Palette, cx: &mut Context<Self>) -> Option<AnyElement> {
        self.planner.snippet_query.as_ref()?;
        let card = v_flex()
            .p_1()
            .w(px(360.))
            .max_w_full()
            .rounded(px(14.))
            .bg(p.panel)
            .border_1()
            .border_color(p.divider)
            .shadow_md();
        if self.snippet_hint_visible() {
            let hover = p.hover;
            return Some(
                card.child(
                    h_flex()
                        .id("snippet-hint")
                        .px_3()
                        .py_2()
                        .gap_2p5()
                        .rounded(px(10.))
                        .cursor_pointer()
                        .hover(move |s| s.bg(hover))
                        .text_size(px(13.))
                        .text_color(p.text_muted)
                        .child(Icon::new(IconName::Zap).size(px(15.)).text_color(p.accent_text))
                        .child(ts!("composer.snippets.add_hint"))
                        .on_click(cx.listener(|this, _, _, cx| {
                            this.close_mentions(cx);
                            this.settings_open = true;
                            cx.notify();
                        })),
                )
                .into_any_element(),
            );
        }
        let candidates = self.snippet_candidates();
        if candidates.is_empty() {
            return None;
        }
        let selected = self.mention_selected.min(candidates.len() - 1);
        let hover = p.hover;
        let rows: Vec<AnyElement> = candidates
            .iter()
            .enumerate()
            .map(|(index, snippet)| {
                let shortcut = format!("/{}", snippet.shortcut.trim().trim_start_matches('/'));
                h_flex()
                    .id(("snippet", index))
                    .px_3()
                    .py_2()
                    .gap_3()
                    .rounded(px(10.))
                    .cursor_pointer()
                    .when(index == selected, |el| el.bg(p.accent_soft))
                    .when(index != selected, |el| el.hover(move |s| s.bg(hover)))
                    .child(
                        div()
                            .flex_none()
                            .max_w(px(130.))
                            .truncate()
                            .text_size(px(13.))
                            .font_weight(FontWeight::SEMIBOLD)
                            .text_color(p.accent_text)
                            .child(SharedString::from(shortcut)),
                    )
                    .child(
                        div()
                            .flex_1()
                            .min_w_0()
                            .truncate()
                            .text_size(px(13.))
                            .text_color(p.text_muted)
                            .child(SharedString::from(one_line(&snippet.text))),
                    )
                    .on_click(cx.listener(move |this, _, window, cx| {
                        this.accept_snippet(index, window, cx)
                    }))
                    .into_any_element()
            })
            .collect();
        Some(
            card.child(
                div()
                    .px_3()
                    .pt_1()
                    .pb_1()
                    .text_size(px(11.5))
                    .font_weight(FontWeight::BOLD)
                    .text_color(p.text_muted)
                    .child(ts!("composer.snippets.title")),
            )
            .children(rows)
            .into_any_element(),
        )
    }

    fn render_mention_popup(&self, p: &Palette, cx: &mut Context<Self>) -> Option<AnyElement> {
        let candidates = self.mention_candidates();
        if self.mention_query.is_none() || candidates.is_empty() {
            return None;
        }
        let selected = self.mention_selected.min(candidates.len() - 1);
        let hover = p.hover;
        let mut rows = Vec::with_capacity(candidates.len());
        for (index, participant) in candidates.iter().enumerate() {
            let avatar_source = self.avatar_source(participant.avatar_url.as_deref());
            let online = self.participant_online(participant);
            let status = self.participant_status(participant);
            let ring = if index == selected {
                p.accent_soft
            } else {
                p.panel
            };
            rows.push(
                h_flex()
                    .id(("mention", index))
                    .px_3()
                    .py_2()
                    .gap_3()
                    .rounded(px(10.))
                    .cursor_pointer()
                    .when(index == selected, |el| el.bg(p.accent_soft))
                    .when(index != selected, |el| el.hover(move |s| s.bg(hover)))
                    .child(super::widgets::person_avatar(
                        &participant.name,
                        avatar_source,
                        28.,
                        online,
                        ring,
                        p,
                    ))
                    .child(
                        v_flex()
                            .min_w_0()
                            .child(
                                div()
                                    .text_size(px(13.5))
                                    .font_weight(FontWeight::SEMIBOLD)
                                    .truncate()
                                    .child(SharedString::from(participant.name.clone())),
                            )
                            .when_some(status, |el, status| {
                                el.child(
                                    div()
                                        .text_size(px(11.5))
                                        .text_color(if online { p.online } else { p.text_faint })
                                        .child(SharedString::from(status)),
                                )
                            }),
                    )
                    .on_click(cx.listener(move |this, _, window, cx| {
                        this.accept_mention(index, window, cx)
                    }))
                    .into_any_element(),
            );
        }
        Some(
            v_flex()
                .p_1()
                .w(px(320.))
                .max_w_full()
                .rounded(px(14.))
                .bg(p.panel)
                .border_1()
                .border_color(p.divider)
                .shadow_md()
                .child(
                    div()
                        .px_3()
                        .pt_1()
                        .pb_1()
                        .text_size(px(11.5))
                        .font_weight(FontWeight::BOLD)
                        .text_color(p.text_muted)
                        .child(ts!("composer.mentions.title")),
                )
                .children(rows)
                .into_any_element(),
        )
    }
}

fn pending_chip(
    index: usize,
    path: &std::path::Path,
    p: &Palette,
    cx: &mut Context<MissiveApp>,
) -> AnyElement {
    let name = path
        .file_name()
        .map(|name| name.to_string_lossy().into_owned())
        .unwrap_or_default();
    let is_image = matches!(
        path.extension()
            .map(|ext| ext.to_string_lossy().to_lowercase())
            .as_deref(),
        Some("png" | "jpg" | "jpeg" | "gif" | "webp" | "bmp")
    );
    let preview = if is_image {
        div()
            .size(px(56.))
            .rounded(px(12.))
            .overflow_hidden()
            .bg(p.chip_bg)
            .child(
                img(path.to_path_buf())
                    .size_full()
                    .rounded(px(12.))
                    .object_fit(ObjectFit::Cover),
            )
            .into_any_element()
    } else {
        h_flex()
            .h(px(56.))
            .max_w(px(220.))
            .px_3()
            .gap_2()
            .rounded(px(12.))
            .bg(p.chip_bg)
            .child(
                Icon::new(IconName::FileText)
                    .size(px(16.))
                    .text_color(p.text_muted),
            )
            .child(
                div()
                    .text_size(px(12.5))
                    .truncate()
                    .child(SharedString::from(name)),
            )
            .into_any_element()
    };
    div()
        .relative()
        .child(preview)
        .child(
            div()
                .id(("remove-pending", index))
                .absolute()
                .top(px(-6.))
                .right(px(-6.))
                .size(px(20.))
                .rounded_full()
                .bg(p.text)
                .flex()
                .items_center()
                .justify_center()
                .cursor_pointer()
                .child(Icon::new(IconName::X).size(px(12.)).text_color(p.panel))
                .on_click(cx.listener(move |this, _, _, cx| this.remove_pending(index, cx))),
        )
        .into_any_element()
}
