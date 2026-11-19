use gpui_kit::assets::IconName;
use gpui_kit::component::{
    Disableable as _, Icon,
    button::{Button, ButtonVariants as _},
    h_flex,
    input::Input,
    v_flex,
};
use gpui_kit::{prelude::FluentBuilder as _, *};

use super::{
    conversation::column,
    sidebar::display_name,
    widgets::{avatar, format_short, icon_button, thread_avatar},
};
use crate::{
    app::{MissiveApp, describe_attachments},
    model::{Message, ReadReceipt, Thread},
    style::Palette,
};

const MAX_POLL_OPTIONS: usize = 12;

pub(super) fn scrim(id: &'static str) -> Stateful<Div> {
    div()
        .id(id)
        .absolute()
        .occlude()
        .on_mouse_down(MouseButton::Right, |_, _, cx| cx.stop_propagation())
        .inset_0()
        .flex()
        .items_center()
        .justify_center()
        .bg(hsla(0., 0., 0., 0.45))
}

pub(super) fn summary(message: &Message) -> String {
    if message.text.is_empty() {
        describe_attachments(message)
    } else {
        message
            .text
            .split_whitespace()
            .collect::<Vec<_>>()
            .join(" ")
    }
}

pub(super) fn pinned_preview(message: &Message) -> String {
    if message.deleted {
        return t!("message_dialogs.pinned.removed").into_owned();
    }
    let body = summary(message);
    if !body.is_empty() {
        return body;
    }
    match &message.poll {
        Some(poll) if !poll.question.trim().is_empty() => poll
            .question
            .split_whitespace()
            .collect::<Vec<_>>()
            .join(" "),
        _ => t!("message_dialogs.pinned.message").into_owned(),
    }
}

pub(super) fn menu_item(
    id: impl Into<ElementId>,
    icon: IconName,
    label: impl Into<SharedString>,
    danger: bool,
    p: &Palette,
) -> Stateful<Div> {
    let label: SharedString = label.into();
    let hover = p.hover;
    let color = if danger { p.danger } else { p.text };
    h_flex()
        .id(id)
        .h(px(36.))
        .px_3()
        .gap_2p5()
        .rounded(px(10.))
        .cursor_pointer()
        .hover(move |style| style.bg(hover))
        .text_size(px(13.5))
        .text_color(color)
        .child(Icon::new(icon).size(px(16.)).text_color(if danger {
            p.danger
        } else {
            p.text_muted
        }))
        .child(label)
}

pub(super) fn dialog_card(id: &'static str, width: f32, p: &Palette) -> Stateful<Div> {
    v_flex()
        .id(id)
        .w(px(width))
        .max_w(relative(0.92))
        .max_h(relative(0.86))
        .rounded(px(18.))
        .bg(p.panel)
        .shadow_lg()
        .overflow_hidden()
        .on_mouse_down(MouseButton::Left, |_, _, cx| cx.stop_propagation())
}

pub(super) fn dialog_header(
    title: impl Into<SharedString>,
    close: Stateful<Div>,
    p: &Palette,
) -> impl IntoElement {
    let title: SharedString = title.into();
    h_flex()
        .flex_none()
        .px_5()
        .pt_4()
        .pb_3()
        .gap_3()
        .child(
            div()
                .flex_1()
                .text_size(px(17.))
                .font_weight(FontWeight::BOLD)
                .text_color(p.text)
                .child(title),
        )
        .child(close)
}

impl MissiveApp {
    pub(super) fn render_message_dialogs(
        &self,
        p: &Palette,
        cx: &mut Context<Self>,
    ) -> Option<AnyElement> {
        let mut layers: Vec<AnyElement> = Vec::new();
        layers.extend(self.render_poll_dialog(p, cx));
        layers.extend(self.render_forward_dialog(p, cx));
        layers.extend(self.render_seen_by(p, cx));
        layers.extend(self.render_message_menu(p, cx));
        if layers.is_empty() {
            return None;
        }
        Some(
            div()
                .absolute()
                .inset_0()
                .children(layers)
                .into_any_element(),
        )
    }

    fn render_message_menu(&self, p: &Palette, cx: &mut Context<Self>) -> Option<AnyElement> {
        let menu = self.message_ops.menu.as_ref()?;
        let message = menu.message.clone();
        let mut items: Vec<AnyElement> = Vec::new();
        if let Some((question, button)) = menu.confirming() {
            items.push(
                v_flex()
                    .gap_3()
                    .p_3()
                    .child(
                        div()
                            .text_size(px(13.5))
                            .line_height(px(19.))
                            .font_weight(FontWeight::SEMIBOLD)
                            .text_color(p.text)
                            .child(question),
                    )
                    .child(
                        h_flex()
                            .justify_end()
                            .gap_2()
                            .child(
                                Button::new("menu-confirm-cancel")
                                    .ghost()
                                    .label(t!("message_dialogs.cancel"))
                                    .on_click(
                                        cx.listener(|this, _, _, cx| this.menu_cancel_confirm(cx)),
                                    ),
                            )
                            .child(
                                Button::new("menu-confirm-ok")
                                    .danger()
                                    .label(button)
                                    .on_click(cx.listener(|this, _, _, cx| this.menu_confirm(cx))),
                            ),
                    )
                    .into_any_element(),
            );
        } else if menu.reminder_choices {
            items.push(
                menu_item(
                    "menu-reminder-back",
                    IconName::ChevronLeft,
                    t!("message_dialogs.menu.remind_me"),
                    false,
                    p,
                )
                .font_weight(FontWeight::SEMIBOLD)
                .on_click(cx.listener(|this, _, _, cx| this.menu_show_reminder_choices(false, cx)))
                .into_any_element(),
            );
            items.push(
                div()
                    .h(px(1.))
                    .mx_2()
                    .my_1()
                    .bg(p.divider)
                    .into_any_element(),
            );
            let draft = self.reminder_draft(&message);
            for (index, (label, due)) in self.reminder_choices().into_iter().enumerate() {
                let draft = draft.clone();
                items.push(
                    menu_item(("menu-reminder", index), IconName::Clock, label, false, p)
                        .on_click(cx.listener(move |this, _, _, cx| {
                            this.remind_message(draft.clone(), due, cx)
                        }))
                        .into_any_element(),
                );
            }
            items.push(
                menu_item(
                    "menu-reminder-pick",
                    IconName::CalendarClock,
                    t!("message_dialogs.menu.pick_time"),
                    false,
                    p,
                )
                .on_click(cx.listener(move |this, _, window, cx| {
                    this.open_remind_picker(draft.clone(), window, cx)
                }))
                .into_any_element(),
            );
        } else {
            let target = message.clone();
            items.push(
                menu_item("menu-reply", IconName::Reply, t!("message_dialogs.menu.reply"), false, p)
                    .on_click(cx.listener(move |this, _, window, cx| {
                        this.message_ops.menu = None;
                        this.start_reply(target.clone(), window, cx);
                    }))
                    .into_any_element(),
            );
            let target = message.id.clone();
            items.push(
                menu_item("menu-react", IconName::Heart, t!("message_dialogs.menu.react"), false, p)
                    .on_click(cx.listener(move |this, _, _, cx| {
                        this.message_ops.menu = None;
                        this.react_picker = None;
                        this.toggle_react_picker(target.clone(), cx);
                    }))
                    .into_any_element(),
            );
            let has_text = !message.text.is_empty()
                || message
                    .attachments
                    .iter()
                    .any(|a| a.kind == "link" && a.url.is_some());
            if has_text {
                let target = message.clone();
                items.push(
                    menu_item(
                        "menu-copy",
                        IconName::Copy,
                        t!("message_dialogs.menu.copy_text"),
                        false,
                        p,
                    )
                        .on_click(
                            cx.listener(move |this, _, _, cx| this.copy_message_text(&target, cx)),
                        )
                        .into_any_element(),
                );
            }
            if self.can_forward(&message) {
                let target = message.clone();
                items.push(
                    menu_item(
                        "menu-forward",
                        IconName::Forward,
                        t!("message_dialogs.menu.forward"),
                        false,
                        p,
                    )
                        .on_click(cx.listener(move |this, _, window, cx| {
                            this.open_forward(target.clone(), window, cx)
                        }))
                        .into_any_element(),
                );
            }
            if self.can_edit(&message) {
                let target = message.clone();
                items.push(
                    menu_item(
                        "menu-edit",
                        IconName::Pencil,
                        t!("message_dialogs.menu.edit"),
                        false,
                        p,
                    )
                        .on_click(cx.listener(move |this, _, window, cx| {
                            this.start_edit(target.clone(), window, cx)
                        }))
                        .into_any_element(),
                );
            }
            if self.can_plan_on(&message) {
                match self.planner.pending_reminder(&message.id) {
                    Some(reminder) => {
                        let target = message.clone();
                        items.push(
                            menu_item(
                                "menu-reminder-cancel",
                                IconName::AlarmClockOff,
                                t!(
                                    "message_dialogs.menu.cancel_reminder",
                                    when = self.planner_when(reminder.due_ms)
                                ),
                                false,
                                p,
                            )
                            .on_click(cx.listener(move |this, _, _, cx| {
                                this.cancel_message_reminder(&target, cx)
                            }))
                            .into_any_element(),
                        );
                    }
                    None => items.push(
                        menu_item(
                            "menu-reminder-open",
                            IconName::AlarmClock,
                            t!("message_dialogs.menu.remind_me"),
                            false,
                            p,
                        )
                        .child(div().flex_1())
                        .child(
                            Icon::new(IconName::ChevronRight)
                                .size(px(15.))
                                .text_color(p.text_faint),
                        )
                        .on_click(cx.listener(|this, _, _, cx| {
                            this.menu_show_reminder_choices(true, cx)
                        }))
                        .into_any_element(),
                    ),
                }
                let favorite = self.planner.is_favorite(&message.id);
                let target = message.clone();
                items.push(
                    menu_item(
                        "menu-favorite",
                        if favorite {
                            IconName::StarOff
                        } else {
                            IconName::Star
                        },
                        if favorite {
                            t!("message_dialogs.menu.remove_favorite")
                        } else {
                            t!("message_dialogs.menu.add_favorite")
                        },
                        false,
                        p,
                    )
                    .on_click(cx.listener(move |this, _, _, cx| this.toggle_favorite(&target, cx)))
                    .into_any_element(),
                );
            }
            let removable = !message.id.starts_with("local-");
            if removable {
                items.push(
                    div()
                        .h(px(1.))
                        .mx_2()
                        .my_1()
                        .bg(p.divider)
                        .into_any_element(),
                );
            }
            if self.can_unsend(&message) {
                items.push(
                    menu_item(
                        "menu-unsend",
                        IconName::Undo2,
                        t!("message_dialogs.menu.unsend"),
                        true,
                        p,
                    )
                    .on_click(cx.listener(|this, _, _, cx| this.menu_request_unsend(cx)))
                    .into_any_element(),
                );
            }
            if removable {
                items.push(
                    menu_item(
                        "menu-delete",
                        IconName::Trash,
                        t!("message_dialogs.menu.delete_for_me"),
                        true,
                        p,
                    )
                    .on_click(cx.listener(|this, _, _, cx| this.menu_request_delete(cx)))
                    .into_any_element(),
                );
            }
        }
        let anchor = if menu.outgoing {
            Anchor::TopRight
        } else {
            Anchor::TopLeft
        };
        Some(
            div()
                .id("message-menu-scrim")
                .absolute()
                .inset_0()
                .occlude()
                .on_click(cx.listener(|this, _, _, cx| {
                    cx.stop_propagation();
                    this.close_message_menu(cx);
                }))
                .on_mouse_down(
                    MouseButton::Right,
                    cx.listener(|this, _, _, cx| {
                        cx.stop_propagation();
                        this.close_message_menu(cx);
                    }),
                )
                .child(
                    anchored()
                        .position(menu.position)
                        .anchor(anchor)
                        .snap_to_window_with_margin(Edges::all(px(8.)))
                        .child(
                            v_flex()
                                .id("message-menu")
                                .w(px(256.))
                                .p_1()
                                .rounded(px(14.))
                                .bg(p.panel)
                                .border_1()
                                .border_color(p.divider)
                                .shadow_lg()
                                .on_mouse_down(MouseButton::Left, |_, _, cx| cx.stop_propagation())
                                .children(items),
                        ),
                )
                .into_any_element(),
        )
    }

    fn render_seen_by(&self, p: &Palette, cx: &mut Context<Self>) -> Option<AnyElement> {
        let seen = self.message_ops.seen_by.as_ref()?;
        let thread = self.threads.iter().find(|t| t.id == seen.thread_id)?;
        let message = self
            .messages
            .get(&thread.id)?
            .iter()
            .find(|m| m.id == seen.message_id)?;
        let timestamp = message.timestamp_ms;
        let mut read: Vec<&ReadReceipt> = thread
            .read_receipts
            .iter()
            .filter(|r| r.read_ms > 0 && r.read_ms >= timestamp)
            .collect();
        read.sort_by_key(|r| r.read_ms);
        let mut delivered: Vec<&ReadReceipt> = thread
            .read_receipts
            .iter()
            .filter(|r| !(r.read_ms > 0 && r.read_ms >= timestamp))
            .filter(|r| r.delivered_ms.is_some_and(|d| d >= timestamp))
            .collect();
        delivered.sort_by_key(|r| r.delivered_ms.unwrap_or_default());

        let seen_verb = t!("message_dialogs.seen_by.verb_seen");
        let delivered_verb = t!("message_dialogs.seen_by.verb_delivered");
        let row = |receipt: &ReadReceipt, at: i64, verb: &str| {
            h_flex()
                .px_3()
                .py_2()
                .gap_3()
                .child(avatar(
                    &receipt.name,
                    self.avatar_source(receipt.avatar_url.as_deref()),
                    32.,
                    p,
                ))
                .child(
                    div()
                        .flex_1()
                        .min_w_0()
                        .truncate()
                        .text_size(px(13.5))
                        .font_weight(FontWeight::SEMIBOLD)
                        .child(SharedString::from(if receipt.name.is_empty() {
                            t!("message_dialogs.seen_by.participant").into_owned()
                        } else {
                            receipt.name.clone()
                        })),
                )
                .child(
                    div()
                        .text_size(px(12.))
                        .text_color(p.text_faint)
                        .child(SharedString::from(format!("{verb} {}", format_short(at)))),
                )
                .into_any_element()
        };
        let section = |title: String| {
            div()
                .px_3()
                .pt_2()
                .pb_1()
                .text_size(px(11.5))
                .font_weight(FontWeight::SEMIBOLD)
                .text_color(p.text_muted)
                .child(SharedString::from(title))
                .into_any_element()
        };
        let mut rows: Vec<AnyElement> = Vec::new();
        if !read.is_empty() {
            rows.push(section(
                t!("message_dialogs.seen_by.section_seen", count = read.len()).into_owned(),
            ));
            rows.extend(read.iter().map(|r| row(r, r.read_ms, &seen_verb)));
        }
        if !delivered.is_empty() {
            rows.push(section(
                t!(
                    "message_dialogs.seen_by.section_delivered",
                    count = delivered.len()
                )
                .into_owned(),
            ));
            rows.extend(
                delivered
                    .iter()
                    .map(|r| row(r, r.delivered_ms.unwrap_or_default(), &delivered_verb)),
            );
        }
        if rows.is_empty() {
            rows.push(
                div()
                    .py_6()
                    .text_center()
                    .text_size(px(13.))
                    .text_color(p.text_faint)
                    .child(ts!("message_dialogs.seen_by.nobody"))
                    .into_any_element(),
            );
        }
        Some(
            scrim("seen-by-scrim")
                .on_click(cx.listener(|this, _, _, cx| {
                    cx.stop_propagation();
                    this.close_seen_by(cx);
                }))
                .child(
                    dialog_card("seen-by", 380., p)
                        .child(dialog_header(
                            t!("message_dialogs.seen_by.title"),
                            icon_button("close-seen-by", IconName::X, false, p)
                                .on_click(cx.listener(|this, _, _, cx| this.close_seen_by(cx))),
                            p,
                        ))
                        .child(
                            div()
                                .mx_5()
                                .mb_2()
                                .text_size(px(12.5))
                                .text_color(p.text_muted)
                                .line_clamp(2)
                                .child(SharedString::from(summary(message))),
                        )
                        .child(
                            v_flex()
                                .id("seen-by-list")
                                .px_2()
                                .pb_3()
                                .overflow_y_scroll()
                                .children(rows),
                        ),
                )
                .into_any_element(),
        )
    }

    fn render_forward_dialog(&self, p: &Palette, cx: &mut Context<Self>) -> Option<AnyElement> {
        let forward = self.message_ops.forward.as_ref()?;
        let results = self.forward_results(cx);
        let hover = p.hover;
        let selected_count = forward.selected.len();
        let mut rows: Vec<AnyElement> = Vec::with_capacity(results.len());
        for (row, index) in results.iter().enumerate() {
            let Some(thread) = self.threads.get(*index) else {
                continue;
            };
            let checked = forward.selected.contains(&thread.id);
            let id = thread.id.clone();
            rows.push(
                h_flex()
                    .id(("forward-row", row))
                    .px_3()
                    .h(px(52.))
                    .gap_3()
                    .rounded(px(12.))
                    .cursor_pointer()
                    .when(checked, |el| el.bg(p.accent_soft))
                    .when(!checked, |el| el.hover(move |s| s.bg(hover)))
                    .child(thread_avatar(self.avatar_spec(thread), 34., p.panel, p))
                    .child(
                        div()
                            .flex_1()
                            .min_w_0()
                            .truncate()
                            .font_weight(FontWeight::SEMIBOLD)
                            .child(SharedString::from(display_name(thread))),
                    )
                    .child(
                        Icon::new(if checked {
                            IconName::SquareCheck
                        } else {
                            IconName::Square
                        })
                        .size(px(18.))
                        .text_color(if checked {
                            p.accent_text
                        } else {
                            p.text_faint
                        }),
                    )
                    .on_click(
                        cx.listener(move |this, _, _, cx| {
                            this.toggle_forward_target(id.clone(), cx)
                        }),
                    )
                    .into_any_element(),
            );
        }
        if rows.is_empty() {
            rows.push(
                div()
                    .py_4()
                    .text_center()
                    .text_size(px(13.))
                    .text_color(p.text_faint)
                    .child(ts!("message_dialogs.forward.no_results"))
                    .into_any_element(),
            );
        }
        let sender = if forward.message.is_outgoing {
            t!("message_dialogs.forward.you").into_owned()
        } else {
            forward.message.sender_name.clone()
        };
        Some(
            scrim("forward-scrim")
                .on_click(cx.listener(|this, _, _, cx| {
                    cx.stop_propagation();
                    this.close_forward(cx);
                }))
                .child(
                    dialog_card("forward", 460., p)
                        .h(px(560.))
                        .child(dialog_header(
                            t!("message_dialogs.forward.title"),
                            icon_button("close-forward", IconName::X, false, p)
                                .on_click(cx.listener(|this, _, _, cx| this.close_forward(cx))),
                            p,
                        ))
                        .child(
                            v_flex()
                                .flex_none()
                                .mx_5()
                                .mb_3()
                                .px_3()
                                .py_2()
                                .rounded(px(12.))
                                .bg(p.chip_bg)
                                .border_l_2()
                                .border_color(p.accent_text)
                                .child(
                                    h_flex()
                                        .gap_1()
                                        .text_size(px(11.5))
                                        .text_color(p.text_muted)
                                        .child(Icon::new(IconName::Forward).size(px(11.)))
                                        .child(SharedString::from(sender)),
                                )
                                .child(
                                    div()
                                        .text_size(px(13.))
                                        .line_clamp(2)
                                        .child(SharedString::from(summary(&forward.message))),
                                ),
                        )
                        .child(
                            h_flex()
                                .flex_none()
                                .mx_5()
                                .mb_2()
                                .px_3()
                                .h(px(40.))
                                .gap_2()
                                .rounded(px(12.))
                                .bg(p.field_bg)
                                .child(
                                    Icon::new(IconName::Search)
                                        .size(px(15.))
                                        .text_color(p.text_faint),
                                )
                                .child(
                                    div()
                                        .flex_1()
                                        .child(Input::new(&forward.input).appearance(false)),
                                ),
                        )
                        .child(
                            v_flex()
                                .id("forward-list")
                                .flex_1()
                                .min_h_0()
                                .px_3()
                                .gap_0p5()
                                .overflow_y_scroll()
                                .children(rows),
                        )
                        .child(
                            h_flex()
                                .flex_none()
                                .px_5()
                                .py_3()
                                .gap_2()
                                .border_t_1()
                                .border_color(p.divider)
                                .child(
                                    div()
                                        .flex_1()
                                        .text_size(px(12.5))
                                        .text_color(p.text_muted)
                                        .child(SharedString::from(match selected_count {
                                            0 => t!("message_dialogs.forward.choose").into_owned(),
                                            n => tn!("message_dialogs.forward.selected", n)
                                                .into_owned(),
                                        })),
                                )
                                .child(
                                    Button::new("forward-cancel")
                                        .ghost()
                                        .label(t!("message_dialogs.cancel"))
                                        .on_click(
                                            cx.listener(|this, _, _, cx| this.close_forward(cx)),
                                        ),
                                )
                                .child(
                                    Button::new("forward-send")
                                        .primary()
                                        .label(t!("message_dialogs.forward.send"))
                                        .loading(forward.busy)
                                        .disabled(selected_count == 0)
                                        .on_click(
                                            cx.listener(|this, _, _, cx| this.send_forward(cx)),
                                        ),
                                ),
                        ),
                )
                .into_any_element(),
        )
    }

    fn render_poll_dialog(&self, p: &Palette, cx: &mut Context<Self>) -> Option<AnyElement> {
        let poll = self.message_ops.poll.as_ref()?;
        let ready = self.poll_draft(cx).is_some();
        let removable = poll.options.len() > 2;
        let mut options: Vec<AnyElement> = Vec::with_capacity(poll.options.len());
        for (index, input) in poll.options.iter().enumerate() {
            options.push(
                h_flex()
                    .gap_2()
                    .child(div().flex_1().child(Input::new(input)))
                    .when(removable, |el| {
                        el.child(
                            icon_button(("remove-option", index), IconName::X, false, p).on_click(
                                cx.listener(move |this, _, _, cx| {
                                    this.remove_poll_option(index, cx)
                                }),
                            ),
                        )
                    })
                    .into_any_element(),
            );
        }
        let can_add = poll.options.len() < MAX_POLL_OPTIONS;
        let accent = p.accent_text;
        Some(
            scrim("poll-scrim")
                .on_click(cx.listener(|this, _, _, cx| {
                    cx.stop_propagation();
                    this.close_poll_dialog(cx);
                }))
                .child(
                    dialog_card("poll-dialog", 440., p)
                        .child(dialog_header(
                            t!("message_dialogs.poll.title"),
                            icon_button("close-poll", IconName::X, false, p)
                                .on_click(cx.listener(|this, _, _, cx| this.close_poll_dialog(cx))),
                            p,
                        ))
                        .child(
                            v_flex()
                                .id("poll-form")
                                .flex_1()
                                .min_h_0()
                                .px_5()
                                .pb_2()
                                .gap_3()
                                .overflow_y_scroll()
                                .child(
                                    v_flex()
                                        .gap_1p5()
                                        .child(
                                            div()
                                                .text_size(px(12.))
                                                .font_weight(FontWeight::SEMIBOLD)
                                                .text_color(p.text_muted)
                                                .child(ts!("message_dialogs.poll.question")),
                                        )
                                        .child(Input::new(&poll.question)),
                                )
                                .child(
                                    v_flex()
                                        .gap_1p5()
                                        .child(
                                            div()
                                                .text_size(px(12.))
                                                .font_weight(FontWeight::SEMIBOLD)
                                                .text_color(p.text_muted)
                                                .child(ts!("message_dialogs.poll.options")),
                                        )
                                        .children(options),
                                )
                                .when(can_add, |el| {
                                    el.child(
                                        h_flex()
                                            .id("add-poll-option")
                                            .gap_1p5()
                                            .text_size(px(13.))
                                            .font_weight(FontWeight::SEMIBOLD)
                                            .text_color(accent)
                                            .cursor_pointer()
                                            .child(Icon::new(IconName::Plus).size(px(15.)))
                                            .child(ts!("message_dialogs.poll.add_option"))
                                            .on_click(cx.listener(|this, _, window, cx| {
                                                this.add_poll_option(window, cx)
                                            })),
                                    )
                                })
                                .when_some(poll.error.clone(), |el, error| {
                                    el.child(
                                        div().text_size(px(12.5)).text_color(p.danger).child(error),
                                    )
                                }),
                        )
                        .child(
                            h_flex()
                                .flex_none()
                                .justify_end()
                                .px_5()
                                .py_3()
                                .gap_2()
                                .border_t_1()
                                .border_color(p.divider)
                                .child(
                                    Button::new("poll-cancel")
                                        .ghost()
                                        .label(t!("message_dialogs.cancel"))
                                        .on_click(
                                            cx.listener(|this, _, _, cx| {
                                                this.close_poll_dialog(cx)
                                            }),
                                        ),
                                )
                                .child(
                                    Button::new("poll-create")
                                        .primary()
                                        .label(t!("message_dialogs.poll.create"))
                                        .loading(poll.busy)
                                        .disabled(!ready)
                                        .on_click(
                                            cx.listener(|this, _, _, cx| this.submit_poll(cx)),
                                        ),
                                ),
                        ),
                )
                .into_any_element(),
        )
    }

    pub(super) fn render_pinned_bar(
        &self,
        thread: &Thread,
        p: &Palette,
        cx: &mut Context<Self>,
    ) -> Option<AnyElement> {
        let pinned_id = thread.pinned_message_ids.first()?.clone();
        let count = thread.pinned_message_ids.len();
        let pinned = self
            .messages
            .get(&thread.id)
            .and_then(|list| list.iter().find(|m| m.id == pinned_id));
        let text: Option<String> = pinned.map(pinned_preview);
        let loaded = text.is_some();
        let thread_id = thread.id.clone();
        let bar = h_flex()
            .id("pinned-bar")
            .w_full()
            .px(px(14.))
            .py(px(8.))
            .gap_2p5()
            .rounded(px(12.))
            .bg(p.signal_soft)
            .text_size(px(13.))
            .text_color(p.signal_text)
            .cursor_pointer()
            .hover(|style| style.opacity(0.88))
            .child(Icon::new(IconName::Pin).size(px(15.)).text_color(p.signal))
            .child(
                div()
                    .flex_none()
                    .font_weight(FontWeight::BOLD)
                    .child(if loaded {
                        ts!("message_dialogs.pinned.label_short")
                    } else {
                        ts!("message_dialogs.pinned.label_long")
                    }),
            )
            .child(
                div()
                    .flex_1()
                    .min_w_0()
                    .truncate()
                    .when(!loaded, |el| el.opacity(0.75))
                    .child(SharedString::from(
                        text.unwrap_or_else(|| t!("message_dialogs.pinned.show").into_owned()),
                    )),
            )
            .when(count > 1, |el| {
                el.child(
                    div()
                        .flex_none()
                        .text_size(px(12.))
                        .font_weight(FontWeight::SEMIBOLD)
                        .opacity(0.75)
                        .child(ts!("message_dialogs.pinned.count", count = count)),
                )
            })
            .on_click(cx.listener(move |this, _, _, cx| {
                this.jump_to_message(thread_id.clone(), pinned_id.clone(), cx)
            }));
        Some(column(bar).pt(px(10.)).into_any_element())
    }
}

#[cfg(test)]
mod tests {
    use super::pinned_preview;
    use crate::model::{Attachment, Message, Poll};

    #[::core::prelude::v1::test]
    fn pinned_bar_previews_the_pinned_message() {
        let _guard = crate::i18n::scoped("fr");
        let text = Message {
            text: "Rendez-vous
 à 8h".into(),
            ..Message::default()
        };
        assert_eq!(pinned_preview(&text), "Rendez-vous à 8h");
        let photo = Message {
            attachments: vec![Attachment {
                kind: "image".into(),
                ..Attachment::default()
            }],
            ..Message::default()
        };
        assert_eq!(pinned_preview(&photo), "Photo");
        let poll = Message {
            poll: Some(Poll {
                question: "Où dîner ?".into(),
                ..Poll::default()
            }),
            ..Message::default()
        };
        assert_eq!(pinned_preview(&poll), "Où dîner ?");
        let deleted = Message {
            deleted: true,
            ..text.clone()
        };
        assert_eq!(pinned_preview(&deleted), "Message retiré");
    }
}
