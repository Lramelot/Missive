use gpui_kit::assets::IconName;
use gpui_kit::component::{Icon, h_flex, v_flex};
use gpui_kit::{prelude::FluentBuilder as _, *};

use super::conversation::{COLUMN_GUTTER, COLUMN_WIDTH};
use super::widgets::{avatar, find_urls, format_duration, format_separator};
use crate::{
    app::{MissiveApp, Outgoing, describe_attachments},
    model::{Attachment, Mention, Message, Poll, ReadReceipt, ReplyPreview, Thread},
    style::{Palette, with_alpha},
};

const RUN_GAP_MS: i64 = 3 * 60 * 1000;
const SEPARATOR_GAP_MS: i64 = 60 * 60 * 1000;
pub(super) const BUBBLE_RADIUS: f32 = 18.;
pub(super) const BUBBLE_TAIL: f32 = 6.;
const BUBBLE_MAX_WIDTH: f32 = 440.;
const AVATAR_SIZE: f32 = 36.;
const AVATAR_GAP: f32 = 10.;
const META_HEIGHT: f32 = 20.;

const QUICK_REACTIONS: [&str; 6] = ["👍", "❤️", "😂", "😮", "😢", "😡"];

pub(super) fn parse_hex_color(value: &str) -> Option<Rgba> {
    let hex = value.trim().strip_prefix('#')?;
    if hex.len() != 6 || !hex.chars().all(|c| c.is_ascii_hexdigit()) {
        return None;
    }
    u32::from_str_radix(hex, 16).ok().map(rgb)
}

pub(super) fn theme_colors(thread: &Thread, p: &Palette) -> (Hsla, Hsla, Hsla) {
    match thread
        .theme
        .as_ref()
        .and_then(|theme| parse_hex_color(&theme.accent))
    {
        Some(color) => {
            let luminance = 0.2126 * color.r + 0.7152 * color.g + 0.0722 * color.b;
            let fg = if luminance > 0.72 {
                hsla(0., 0., 0.08, 1.)
            } else {
                white()
            };
            let bg: Hsla = color.into();
            let hover = Hsla {
                l: (bg.l * 0.86).clamp(0., 1.),
                ..bg
            };
            (bg, fg, hover)
        }
        None => (p.accent, p.on_accent, p.accent_strong),
    }
}

fn bubble_colors(thread: &Thread, p: &Palette) -> (Hsla, Hsla) {
    let themed = thread
        .theme
        .as_ref()
        .and_then(|theme| parse_hex_color(&theme.accent))
        .is_some();
    if themed {
        let (bg, fg, _) = theme_colors(thread, p);
        (bg, fg)
    } else {
        (p.bubble_out, p.on_bubble_out)
    }
}

pub(super) fn delivery_label(
    receipts: &[ReadReceipt],
    timestamp_ms: i64,
    is_group: bool,
    members: usize,
    local: bool,
) -> String {
    if local {
        return "Envoyé".into();
    }
    let seen = receipts
        .iter()
        .filter(|receipt| receipt.read_ms > 0 && receipt.read_ms >= timestamp_ms)
        .count();
    if seen > 0 {
        return if !is_group {
            "Vu".into()
        } else if members > 0 && seen >= members {
            "Vu par tout le monde".into()
        } else {
            format!("Vu par {seen}")
        };
    }
    let delivered = receipts.iter().any(|receipt| {
        receipt
            .delivered_ms
            .is_some_and(|delivered| delivered >= timestamp_ms)
    });
    if delivered {
        "Distribué".into()
    } else {
        "Envoyé".into()
    }
}

struct FindHighlight {
    query: String,
    style: HighlightStyle,
}

impl FindHighlight {
    fn ranges(&self, text: &str) -> Vec<(std::ops::Range<usize>, HighlightStyle)> {
        MissiveApp::find_ranges(text, &self.query)
            .into_iter()
            .map(|range| (range, self.style))
            .collect()
    }
}

fn highlighted(text: String, find: Option<&FindHighlight>) -> AnyElement {
    let ranges = find.map(|find| find.ranges(&text)).unwrap_or_default();
    if ranges.is_empty() {
        SharedString::from(text).into_any_element()
    } else {
        StyledText::new(SharedString::from(text))
            .with_highlights(ranges)
            .into_any_element()
    }
}

fn scaled(size: f32, scale: f32) -> Pixels {
    px(size * scale)
}

fn outgoing_status(
    client_id: &str,
    state: Outgoing,
    waiting: bool,
    view: &Entity<MissiveApp>,
    p: &Palette,
) -> AnyElement {
    let row = h_flex().gap_2().text_size(px(12.));
    if waiting {
        let discard_view = view.clone();
        let discard_id = client_id.to_string();
        return row
            .text_color(p.text_faint)
            .child(Icon::new(IconName::Clock).size(px(13.)))
            .child("En attente de connexion")
            .child(
                div()
                    .id(SharedString::from(format!("discard-{client_id}")))
                    .cursor_pointer()
                    .hover(|style| style.underline())
                    .child("Annuler")
                    .on_click(move |_, _, cx| {
                        let id = discard_id.clone();
                        discard_view.update(cx, |this, cx| this.discard_outgoing(id, cx));
                    }),
            )
            .into_any_element();
    }
    match state {
        Outgoing::Sending => row
            .text_color(p.text_faint)
            .child("Envoi…")
            .into_any_element(),
        Outgoing::Failed(label) => {
            let retry_view = view.clone();
            let discard_view = view.clone();
            let retry_id = client_id.to_string();
            let discard_id = client_id.to_string();
            row.text_color(p.danger)
                .child(label)
                .child(
                    div()
                        .id(SharedString::from(format!("retry-{client_id}")))
                        .font_weight(FontWeight::SEMIBOLD)
                        .cursor_pointer()
                        .child("Réessayer")
                        .on_click(move |_, _, cx| {
                            let id = retry_id.clone();
                            retry_view.update(cx, |this, cx| this.retry_outgoing(id, cx));
                        }),
                )
                .child(
                    div()
                        .id(SharedString::from(format!("discard-{client_id}")))
                        .text_color(p.text_faint)
                        .cursor_pointer()
                        .child("Annuler")
                        .on_click(move |_, _, cx| {
                            let id = discard_id.clone();
                            discard_view.update(cx, |this, cx| this.discard_outgoing(id, cx));
                        }),
                )
                .into_any_element()
        }
    }
}

#[derive(Clone, Copy)]
enum Action {
    React,
    Reply,
    Menu,
}

#[derive(Clone, Copy)]
struct QuoteColors {
    bg: Hsla,
    name: Hsla,
    text: Hsla,
}

fn reply_quote(
    quoted: &ReplyPreview,
    owner: &Message,
    me_name: Option<&str>,
    colors: QuoteColors,
    scale: f32,
    view: &Entity<MissiveApp>,
) -> AnyElement {
    let body: String = if !quoted.text.is_empty() {
        quoted.text.split_whitespace().collect::<Vec<_>>().join(" ")
    } else {
        match quoted.kind.as_deref() {
            Some("image") => "Photo".into(),
            Some("video") => "Vidéo".into(),
            Some("audio") => "Message vocal".into(),
            Some("link") => "Lien".into(),
            Some("location") => "Position".into(),
            Some(_) => "Pièce jointe".into(),
            None => "Message".into(),
        }
    };
    let name = if quoted.sender_name.is_empty() {
        "Réponse"
    } else if me_name == Some(quoted.sender_name.as_str()) {
        "Vous"
    } else {
        quoted.sender_name.as_str()
    };
    let display = format!("{name}  {body}");
    let highlight = HighlightStyle {
        color: Some(colors.name),
        font_weight: Some(FontWeight::BOLD),
        ..Default::default()
    };
    let text = StyledText::new(SharedString::from(display))
        .with_highlights(vec![(0..name.len(), highlight)]);
    let target = (!quoted.id.is_empty()).then(|| (owner.thread_id.clone(), quoted.id.clone()));
    let view = view.clone();
    div()
        .id(SharedString::from(format!("quote-{}", owner.id)))
        .px(px(10.))
        .py(px(6.))
        .rounded(px(13.))
        .bg(colors.bg)
        .text_size(scaled(12.5, scale))
        .line_height(scaled(17., scale))
        .text_color(colors.text)
        .line_clamp(2)
        .when_some(target, |el, (thread_id, message_id)| {
            el.cursor_pointer()
                .hover(|style| style.opacity(0.8))
                .on_click(move |_, _, cx| {
                    let thread_id = thread_id.clone();
                    let message_id = message_id.clone();
                    view.update(cx, |this, cx| this.jump_to_message(thread_id, message_id, cx));
                })
        })
        .child(text)
        .into_any_element()
}

fn forwarded_label(p: &Palette) -> impl IntoElement {
    h_flex()
        .gap_1()
        .px_1()
        .text_size(px(11.5))
        .text_color(p.text_faint)
        .child(Icon::new(IconName::Forward).size(px(12.)))
        .child("Transféré")
}

fn date_separator(timestamp_ms: i64, compact: bool, p: &Palette) -> impl IntoElement {
    let gap = if compact { px(6.) } else { px(10.) };
    div()
        .w_full()
        .flex()
        .justify_center()
        .pt(gap)
        .pb(gap)
        .text_size(px(12.))
        .font_weight(FontWeight::BOLD)
        .text_color(p.text_muted)
        .child(format_separator(timestamp_ms))
}

fn row_column() -> Div {
    v_flex()
        .w_full()
        .max_w(px(COLUMN_WIDTH))
        .px(px(COLUMN_GUTTER))
}

fn centered_row(content: Div) -> Div {
    div().w_full().flex().justify_center().child(content)
}

struct RowContext {
    index: usize,
    outgoing: bool,
    first_in_run: bool,
    out_bg: Hsla,
    out_fg: Hsla,
    scale: f32,
    compact: bool,
}

impl MissiveApp {
    pub(super) fn render_message(
        &self,
        thread_id: &str,
        index: usize,
        view: &Entity<Self>,
        cx: &App,
    ) -> AnyElement {
        let p = Palette::get(cx);
        let Some(thread) = self.threads.iter().find(|thread| thread.id == thread_id) else {
            return div().into_any_element();
        };
        let Some(messages) = self.messages.get(&thread.id) else {
            return div().into_any_element();
        };
        let Some(message) = messages.get(index) else {
            return div().into_any_element();
        };
        let previous = index.checked_sub(1).and_then(|i| messages.get(i));
        let next = messages.get(index + 1);
        let is_last = index + 1 == messages.len();

        let separator =
            previous.is_none_or(|prev| message.timestamp_ms - prev.timestamp_ms > SEPARATOR_GAP_MS);
        let scale = self.settings.text_size.scale();
        let compact = self.settings.compact;
        if message.system {
            return render_system_line(message, separator, is_last, scale, compact, &p);
        }
        let first_in_run = separator || previous.is_none_or(|prev| !same_run(prev, message));
        let last_in_run = next.is_none_or(|next| {
            !same_run(message, next) || next.timestamp_ms - message.timestamp_ms > SEPARATOR_GAP_MS
        });
        let (out_bg, out_fg) = bubble_colors(thread, &p);
        let row = RowContext {
            index,
            outgoing: message.is_outgoing,
            first_in_run,
            out_bg,
            out_fg,
            scale,
            compact,
        };
        let find = self.find_mark(message).map(|mark| FindHighlight {
            query: mark.query.to_string(),
            style: if mark.current {
                HighlightStyle {
                    color: Some(p.on_signal),
                    background_color: Some(p.signal),
                    ..Default::default()
                }
            } else {
                HighlightStyle {
                    color: Some(p.accent_text),
                    background_color: Some(p.accent_soft),
                    ..Default::default()
                }
            },
        });
        let waiting = self.find.offline.is_waiting(&message.id);
        let show_name = thread.is_group && !row.outgoing && first_in_run;
        let (avatar_name, avatar_url) = if thread.is_group {
            let url = thread
                .participants
                .iter()
                .find(|participant| participant.user_id == message.sender_id)
                .and_then(|participant| participant.avatar_url.as_deref());
            (message.sender_name.as_str(), self.avatar_source(url))
        } else {
            (
                thread.name.as_str(),
                self.avatar_source(thread.avatar_url.as_deref()),
            )
        };
        let outgoing_state = self.outgoing.get(&message.id).cloned();
        let has_reactions = !message.reactions.is_empty();
        let group: SharedString = format!("message-{index}").into();
        let readers: Vec<&ReadReceipt> = thread
            .read_receipts
            .iter()
            .filter(|receipt| {
                receipt.read_ms > 0
                    && receipt.read_ms >= message.timestamp_ms
                    && next.is_none_or(|next| receipt.read_ms < next.timestamp_ms)
            })
            .collect();
        let picker_open = self.react_picker.as_deref() == Some(message.id.as_str());
        let menu_open = self
            .message_ops
            .menu
            .as_ref()
            .is_some_and(|menu| menu.message.id == message.id);
        let actionable = !message.deleted;
        let flashing = self.message_ops.is_flashing(&message.id);
        let editing = self.message_ops.is_editing(&message.id);
        let is_last_outgoing = message.is_outgoing
            && messages.iter().rposition(|m| m.is_outgoing && !m.system) == Some(index);

        let quoted = message.reply_to.as_ref().filter(|_| !message.deleted);
        let content = v_flex()
            .max_w(px(BUBBLE_MAX_WIDTH))
            .min_w_0()
            .gap_1()
            .when(row.outgoing, |el| el.items_end())
            .when(!row.outgoing, |el| el.items_start())
            .when(editing, |el| el.opacity(0.55))
            .when(waiting && !editing, |el| el.opacity(0.6))
            .when(message.forwarded && !message.deleted, |el| {
                el.child(forwarded_label(&p))
            })
            .children(self.render_message_body(message, &row, quoted, find.as_ref(), view, &p, cx))
            .when(has_reactions, |el| {
                el.child(reaction_pill(message, row.outgoing, view, &p))
            });
        let details_open =
            has_reactions && self.reaction_details.as_deref() == Some(message.id.as_str());

        let react = self.action_button(
            ("react", index),
            IconName::Heart,
            view,
            message,
            Action::React,
            &p,
        );
        let reply = self.action_button(
            ("reply", index),
            IconName::Reply,
            view,
            message,
            Action::Reply,
            &p,
        );
        let more = self.action_button(
            ("more", index),
            IconName::Ellipsis,
            view,
            message,
            Action::Menu,
            &p,
        );
        let buttons: Vec<Stateful<Div>> = if row.outgoing {
            vec![more, reply, react]
        } else {
            vec![react, reply, more]
        };
        let actions = h_flex()
            .flex_none()
            .gap_0p5()
            .invisible()
            .group_hover(group.clone(), |style| style.visible())
            .when(picker_open || menu_open, |el| el.visible())
            .children(buttons);
        let favorite = self.planner.is_favorite(&message.id);
        let reminder = self.planner.pending_reminder(&message.id).is_some();
        let marks = (favorite || reminder).then(|| {
            v_flex()
                .flex_none()
                .gap(px(4.))
                .items_center()
                .when(reminder, |el| {
                    el.child(
                        Icon::new(IconName::AlarmClock)
                            .size(px(13.))
                            .text_color(p.accent_text),
                    )
                })
                .when(favorite, |el| {
                    el.child(
                        Icon::new(IconName::StarFill)
                            .size(px(13.))
                            .text_color(p.signal),
                    )
                })
        });

        let status: Option<AnyElement> = match outgoing_state {
            Some(state) => Some(outgoing_status(&message.id, state, waiting, view, &p)),
            None if is_last_outgoing => Some(self.delivery_status(thread, message, view, &p)),
            None => None,
        };
        let readers_row: Option<AnyElement> = (!readers.is_empty()).then(|| {
            let view = view.clone();
            let thread_id = thread.id.clone();
            let message_id = message.id.clone();
            h_flex()
                .id(SharedString::from(format!("readers-{}", message.id)))
                .gap(px(2.))
                .cursor_pointer()
                .on_click(move |_, _, cx| {
                    let thread_id = thread_id.clone();
                    let message_id = message_id.clone();
                    view.update(cx, |this, cx| this.open_seen_by(thread_id, message_id, cx));
                })
                .children(readers.iter().take(5).map(|receipt| {
                    avatar(
                        &receipt.name,
                        self.avatar_source(receipt.avatar_url.as_deref()),
                        16.,
                        &p,
                    )
                }))
                .when(readers.len() > 5, |el| {
                    el.child(
                        div()
                            .text_size(px(11.))
                            .text_color(p.text_muted)
                            .child(SharedString::from(format!("+{}", readers.len() - 5))),
                    )
                })
                .into_any_element()
        });
        let has_meta = status.is_some() || readers_row.is_some();

        let top = match (first_in_run, compact) {
            (true, false) => px(12.),
            (true, true) => px(6.),
            (false, false) => px(3.),
            (false, true) => px(1.),
        };
        let column = row_column()
            .pt(top)
            .when(has_reactions, |el| el.pb(px(4.)))
            .when(is_last, |el| el.pb_4())
            .when(separator, |el| {
                el.child(date_separator(message.timestamp_ms, compact, &p))
            })
            .when(show_name, |el| {
                el.child(
                    div()
                        .pl(px(AVATAR_SIZE + AVATAR_GAP))
                        .pb(if compact { px(2.) } else { px(4.) })
                        .text_size(scaled(12., scale))
                        .font_weight(FontWeight::SEMIBOLD)
                        .text_color(p.text_muted)
                        .child(SharedString::from(message.sender_name.clone())),
                )
            })
            .when(picker_open, |el| {
                el.child(self.render_react_picker(message, row.outgoing, view, &p))
            })
            .child(
                h_flex()
                    .group(group)
                    .w_full()
                    .items_center()
                    .gap(px(AVATAR_GAP))
                    .when(row.outgoing, |el| el.justify_end())
                    .when(!row.outgoing, |el| {
                        el.child(
                            div()
                                .flex_none()
                                .self_end()
                                .w(px(AVATAR_SIZE))
                                .when(last_in_run, |el| {
                                    el.child(avatar(avatar_name, avatar_url, AVATAR_SIZE, &p))
                                }),
                        )
                    })
                    .map(|el| match (row.outgoing, actionable) {
                        (true, true) => el.child(actions).children(marks).child(content),
                        (false, true) => el.child(content).children(marks).child(actions),
                        (_, false) => el.child(content),
                    }),
            )
            .when(details_open, |el| {
                el.child(
                    h_flex()
                        .w_full()
                        .when(row.outgoing, |el| el.justify_end())
                        .when(!row.outgoing, |el| el.pl(px(AVATAR_SIZE + AVATAR_GAP)))
                        .child(self.render_reaction_details(message, row.outgoing, view, &p)),
                )
            })
            .when(has_meta, |el| {
                el.child(
                    h_flex()
                        .w_full()
                        .h(px(META_HEIGHT))
                        .mt(if compact { px(0.) } else { px(2.) })
                        .justify_end()
                        .items_center()
                        .gap_1()
                        .text_size(px(12.))
                        .text_color(p.text_muted)
                        .children(status)
                        .children(readers_row),
                )
            });
        centered_row(column)
            .when(flashing, |el| el.bg(p.accent_soft))
            .into_any_element()
    }

    fn delivery_status(
        &self,
        thread: &Thread,
        message: &Message,
        view: &Entity<Self>,
        p: &Palette,
    ) -> AnyElement {
        let me = self.user.as_ref().map(|user| user.id.as_str());
        let members = if thread.is_group {
            thread
                .participants
                .iter()
                .filter(|participant| Some(participant.user_id.as_str()) != me)
                .count()
        } else {
            1
        };
        let label = delivery_label(
            &thread.read_receipts,
            message.timestamp_ms,
            thread.is_group,
            members,
            message.id.starts_with("local-"),
        );
        let has_details = thread.read_receipts.iter().any(|receipt| {
            (receipt.read_ms > 0 && receipt.read_ms >= message.timestamp_ms)
                || receipt
                    .delivered_ms
                    .is_some_and(|delivered| delivered >= message.timestamp_ms)
        });
        let view = view.clone();
        let thread_id = thread.id.clone();
        let message_id = message.id.clone();
        div()
            .id(SharedString::from(format!("delivery-{}", message.id)))
            .text_size(px(12.))
            .text_color(p.text_muted)
            .when(has_details, |el| {
                el.cursor_pointer()
                    .hover(|style| style.underline())
                    .on_click(move |_, _, cx| {
                        let thread_id = thread_id.clone();
                        let message_id = message_id.clone();
                        view.update(cx, |this, cx| this.open_seen_by(thread_id, message_id, cx));
                    })
            })
            .child(SharedString::from(label))
            .into_any_element()
    }

    fn action_button(
        &self,
        id: impl Into<ElementId>,
        icon: IconName,
        view: &Entity<Self>,
        message: &Message,
        action: Action,
        p: &Palette,
    ) -> Stateful<Div> {
        let view = view.clone();
        let message = message.clone();
        let hover = p.hover;
        div()
            .id(id)
            .size(px(28.))
            .rounded_full()
            .flex()
            .items_center()
            .justify_center()
            .cursor_pointer()
            .hover(move |style| style.bg(hover))
            .child(Icon::new(icon).size(px(15.)).text_color(p.text_faint))
            .on_click(move |event, window, cx| {
                let message = message.clone();
                let position = event.position();
                view.update(cx, |this, cx| match action {
                    Action::React => this.toggle_react_picker(message.id.clone(), cx),
                    Action::Reply => this.start_reply(message, window, cx),
                    Action::Menu => this.open_message_menu(message, position, cx),
                });
            })
    }

    fn render_react_picker(
        &self,
        message: &Message,
        outgoing: bool,
        view: &Entity<Self>,
        p: &Palette,
    ) -> impl IntoElement {
        h_flex()
            .w_full()
            .when(outgoing, |el| el.justify_end())
            .when(!outgoing, |el| el.pl(px(AVATAR_SIZE + AVATAR_GAP)))
            .pb_1()
            .child(
                h_flex()
                    .gap_0p5()
                    .px_1()
                    .py_1()
                    .rounded_full()
                    .bg(p.panel)
                    .border_1()
                    .border_color(p.divider)
                    .shadow_md()
                    .children(QUICK_REACTIONS.iter().enumerate().map(|(i, emoji)| {
                        let view = view.clone();
                        let message = message.clone();
                        let mine = message
                            .reactions
                            .iter()
                            .any(|r| r.mine && r.emoji == *emoji);
                        let hover = p.hover;
                        div()
                            .id(("quick-reaction", i))
                            .size(px(34.))
                            .rounded_full()
                            .flex()
                            .items_center()
                            .justify_center()
                            .text_size(px(20.))
                            .cursor_pointer()
                            .when(mine, |el| el.bg(p.accent_soft))
                            .hover(move |style| style.bg(hover))
                            .child(*emoji)
                            .on_click(move |_, _, cx| {
                                view.update(cx, |this, cx| this.react(&message, emoji, cx));
                            })
                    })),
            )
    }

    fn render_message_body(
        &self,
        message: &Message,
        row: &RowContext,
        quoted: Option<&ReplyPreview>,
        find: Option<&FindHighlight>,
        view: &Entity<Self>,
        p: &Palette,
        cx: &App,
    ) -> Vec<AnyElement> {
        let mut parts = Vec::new();
        let me_name = self.user.as_ref().map(|user| user.name.as_str());
        let mut quote_pending = quoted;
        let standalone_quote = |quoted: &ReplyPreview| {
            reply_quote(
                quoted,
                message,
                me_name,
                QuoteColors {
                    bg: p.bubble_in,
                    name: p.accent_text,
                    text: p.text_muted,
                },
                row.scale,
                view,
            )
        };

        if message.deleted {
            parts.push(
                bubble(row)
                    .border_1()
                    .border_color(p.divider)
                    .text_color(p.text_faint)
                    .italic()
                    .child("Message retiré")
                    .into_any_element(),
            );
            return parts;
        }

        if let Some(poll) = &message.poll {
            let shown = self.effective_poll(poll);
            parts.push(self.render_poll(message, shown.as_ref().unwrap_or(poll), view, p));
        }

        for attachment in &message.attachments {
            parts.push(self.render_attachment(message, attachment, row, find, view, p, cx));
        }

        let text_is_card_link = message.attachments.iter().any(|a| {
            a.kind == "link"
                && a.url.as_deref().is_some_and(|url| {
                    let text = message.text.trim();
                    text == url || find_urls(text).len() == 1 && text.len() < url.len() + 3
                })
        });
        let text_is_poll = message
            .poll
            .as_ref()
            .is_some_and(|poll| message.text.trim() == poll.question.trim());
        let text_bubble = !message.text.is_empty()
            && !text_is_card_link
            && !(message.poll.is_some() && text_is_poll)
            && !is_emoji_only(&message.text);
        if !text_bubble && let Some(quoted) = quote_pending.take() {
            parts.insert(0, standalone_quote(quoted));
        }
        if message.poll.is_some() && (message.text.is_empty() || text_is_poll) {
            return parts;
        }
        if !message.text.is_empty() && !text_is_card_link {
            let only_emoji = is_emoji_only(&message.text);
            if only_emoji {
                parts.push(
                    div()
                        .text_size(scaled(36., row.scale))
                        .line_height(scaled(44., row.scale))
                        .child(SharedString::from(message.text.clone()))
                        .into_any_element(),
                );
            } else {
                let (bg, fg, link, mention) = if row.outgoing {
                    (row.out_bg, row.out_fg, row.out_fg, row.out_fg)
                } else {
                    (p.bubble_in, p.bubble_in_text, p.link, p.accent_text)
                };
                let mentions_me = self.mentions_me(message);
                let text = div()
                    .child(linkified(
                        ("text", row.index),
                        &message.text,
                        link,
                        &message.mentions,
                        mention,
                        find,
                    ))
                    .when(message.edited, |el| {
                        el.child(
                            div()
                                .text_size(scaled(11., row.scale))
                                .opacity(0.7)
                                .child("modifié"),
                        )
                    });
                let quote = quote_pending.take().map(|quoted| {
                    let colors = if row.outgoing {
                        QuoteColors {
                            bg: with_alpha(row.out_fg, 0.16),
                            name: row.out_fg,
                            text: with_alpha(row.out_fg, 0.82),
                        }
                    } else {
                        QuoteColors {
                            bg: p.panel,
                            name: p.accent_text,
                            text: p.text_muted,
                        }
                    };
                    reply_quote(quoted, message, me_name, colors, row.scale, view)
                });
                parts.push(
                    bubble(row)
                        .bg(bg)
                        .text_color(fg)
                        .when(mentions_me && !row.outgoing, |el| {
                            el.border_1().border_color(p.accent_text)
                        })
                        .map(|el| match quote {
                            Some(quote) => el.p(px(5.)).child(quote).child(
                                text.px(px(9.)).pt(px(6.)).pb(px(3.)),
                            ),
                            None => el.child(text),
                        })
                        .into_any_element(),
                );
            }
        } else if message.attachments.is_empty() {
            parts.push(
                bubble(row)
                    .bg(p.bubble_in)
                    .text_color(p.text_muted)
                    .italic()
                    .child("Message non pris en charge")
                    .into_any_element(),
            );
        }
        parts
    }

    fn render_attachment(
        &self,
        message: &Message,
        attachment: &Attachment,
        row: &RowContext,
        find: Option<&FindHighlight>,
        view: &Entity<Self>,
        p: &Palette,
        cx: &App,
    ) -> AnyElement {
        match attachment.kind.as_str() {
            "image" | "sticker" => self.render_image(message, attachment, view, p),
            "link" => self.render_link(message, attachment, row, find, p),
            "audio" => self
                .render_voice_attachment(message, attachment, row.outgoing, view, p, cx)
                .unwrap_or_else(|| render_chip(message, attachment, find, p)),
            "location" => self.render_location(message, attachment, p),
            "story" => self.render_story(message, attachment, p),
            "contact" => render_contact(attachment, p),
            _ => render_chip(message, attachment, find, p),
        }
    }

    fn card_thumbnail(&self, message: &Message, attachment: &Attachment) -> Option<ImageSource> {
        attachment
            .thumbnail_url
            .as_deref()
            .filter(|url| !url.is_empty())
            .and_then(|_| self.thumb_source(message, attachment))
    }

    fn render_location(
        &self,
        message: &Message,
        attachment: &Attachment,
        p: &Palette,
    ) -> AnyElement {
        let url = attachment
            .url
            .clone()
            .filter(|url| !url.is_empty())
            .or_else(|| match (attachment.latitude, attachment.longitude) {
                (Some(lat), Some(lon)) => Some(format!(
                    "https://www.google.com/maps/search/?api=1&query={lat},{lon}"
                )),
                _ => None,
            });
        let title = attachment
            .title
            .clone()
            .filter(|t| !t.is_empty())
            .unwrap_or_else(|| "Position".into());
        let thumbnail = self.card_thumbnail(message, attachment);
        let hover = p.hover;
        v_flex()
            .id(SharedString::from(format!(
                "location-{}-{}",
                message.id, attachment.id
            )))
            .w(px(260.))
            .rounded(px(18.))
            .overflow_hidden()
            .bg(p.bubble_in)
            .border_1()
            .border_color(p.divider)
            .when_some(url, |el, url| {
                el.cursor_pointer()
                    .hover(move |style| style.bg(hover))
                    .on_click(move |_, _, cx| crate::desktop::open_web(&url, cx))
            })
            .child(
                div()
                    .w_full()
                    .h(px(130.))
                    .bg(p.field_bg)
                    .flex()
                    .items_center()
                    .justify_center()
                    .overflow_hidden()
                    .child(match thumbnail {
                        Some(source) => img(source)
                            .size_full()
                            .rounded_t(px(17.))
                            .object_fit(ObjectFit::Cover)
                            .into_any_element(),
                        None => Icon::new(IconName::MapPin)
                            .size(px(30.))
                            .text_color(p.accent_text)
                            .into_any_element(),
                    }),
            )
            .child(
                h_flex()
                    .px_3()
                    .py_2p5()
                    .gap_2p5()
                    .child(
                        Icon::new(IconName::MapPin)
                            .size(px(16.))
                            .text_color(p.accent_text),
                    )
                    .child(
                        v_flex()
                            .flex_1()
                            .min_w_0()
                            .child(
                                div()
                                    .text_size(px(13.5))
                                    .font_weight(FontWeight::SEMIBOLD)
                                    .text_color(p.bubble_in_text)
                                    .truncate()
                                    .child(SharedString::from(title)),
                            )
                            .when_some(
                                attachment.description.clone().filter(|d| !d.is_empty()),
                                |el, description| {
                                    el.child(
                                        div()
                                            .text_size(px(12.))
                                            .line_height(px(16.))
                                            .text_color(p.text_muted)
                                            .line_clamp(2)
                                            .child(SharedString::from(description)),
                                    )
                                },
                            ),
                    ),
            )
            .into_any_element()
    }

    fn render_story(&self, message: &Message, attachment: &Attachment, p: &Palette) -> AnyElement {
        let url = attachment.url.clone().filter(|url| {
            url.starts_with("https://") || url.starts_with("http://")
        });
        let title = attachment
            .title
            .clone()
            .filter(|t| !t.is_empty())
            .unwrap_or_else(|| "Story".into());
        let thumbnail = self.card_thumbnail(message, attachment);
        let hover = p.hover;
        v_flex()
            .id(SharedString::from(format!(
                "story-{}-{}",
                message.id, attachment.id
            )))
            .w(px(180.))
            .rounded(px(18.))
            .overflow_hidden()
            .bg(p.bubble_in)
            .border_1()
            .border_color(p.divider)
            .when_some(url, |el, url| {
                el.cursor_pointer()
                    .hover(move |style| style.bg(hover))
                    .on_click(move |_, _, cx| crate::desktop::open_web(&url, cx))
            })
            .map(|el| match thumbnail {
                Some(source) => el.child(
                    div().w_full().h(px(240.)).bg(p.field_bg).overflow_hidden().child(
                        img(source)
                            .size_full()
                            .rounded_t(px(17.))
                            .object_fit(ObjectFit::Cover),
                    ),
                ),
                None => el.child(
                    div()
                        .w_full()
                        .h(px(96.))
                        .bg(p.field_bg)
                        .flex()
                        .items_center()
                        .justify_center()
                        .child(
                            Icon::new(IconName::CircleDot)
                                .size(px(26.))
                                .text_color(p.text_faint),
                        ),
                ),
            })
            .child(
                h_flex()
                    .px_3()
                    .py_2()
                    .gap_2()
                    .child(
                        Icon::new(IconName::CircleDot)
                            .size(px(13.))
                            .text_color(p.accent_text),
                    )
                    .child(
                        div()
                            .flex_1()
                            .min_w_0()
                            .text_size(px(12.5))
                            .line_height(px(16.))
                            .text_color(p.text_muted)
                            .line_clamp(2)
                            .child(SharedString::from(title)),
                    ),
            )
            .into_any_element()
    }

    fn render_poll(
        &self,
        message: &Message,
        poll: &Poll,
        view: &Entity<Self>,
        p: &Palette,
    ) -> AnyElement {
        let total = poll.total_votes.max(1) as f32;
        let votable = !message.id.starts_with("local-");
        let hover_border = p.border_strong;
        let mine_fill = with_alpha(p.accent, if p.dark { 0.22 } else { 0.12 });
        let other_fill = with_alpha(p.text_faint, 0.15);
        let mut options: Vec<AnyElement> = Vec::with_capacity(poll.options.len());
        for option in &poll.options {
            let fraction = (option.vote_count as f32 / total).clamp(0., 1.);
            let full = fraction >= 0.99;
            let voters: Vec<AnyElement> = option
                .voters
                .iter()
                .take(3)
                .map(|voter| {
                    avatar(
                        &voter.name,
                        self.avatar_source(voter.avatar_url.as_deref()),
                        16.,
                        p,
                    )
                    .into_any_element()
                })
                .collect();
            let view = view.clone();
            let target = message.clone();
            let option_id = option.id.clone();
            options.push(
                h_flex()
                    .id(SharedString::from(format!(
                        "poll-{}-{}",
                        message.id, option.id
                    )))
                    .relative()
                    .min_h(px(38.))
                    .px_3()
                    .py(px(7.))
                    .gap_2()
                    .rounded(px(10.))
                    .overflow_hidden()
                    .bg(p.bubble_in)
                    .border_2()
                    .border_color(if option.mine {
                        p.accent_text
                    } else {
                        transparent_black()
                    })
                    .when(votable, |el| {
                        el.cursor_pointer()
                            .when(!option.mine, |el| {
                                el.hover(move |style| style.border_color(hover_border))
                            })
                            .on_click(move |_, _, cx| {
                                view.update(cx, |this, cx| this.vote_poll(&target, &option_id, cx));
                            })
                    })
                    .child(
                        div()
                            .absolute()
                            .left_0()
                            .top_0()
                            .bottom_0()
                            .w(relative(fraction))
                            .rounded_l(px(8.))
                            .when(full, |el| el.rounded_r(px(8.)))
                            .bg(if option.mine { mine_fill } else { other_fill }),
                    )
                    .child(
                        div()
                            .flex_1()
                            .min_w_0()
                            .text_size(px(14.))
                            .line_height(px(19.))
                            .line_clamp(2)
                            .when(option.mine, |el| el.font_weight(FontWeight::BOLD))
                            .child(SharedString::from(option.text.clone())),
                    )
                    .child(h_flex().flex_none().gap(px(2.)).children(voters))
                    .child(
                        div()
                            .flex_none()
                            .min_w(px(10.))
                            .text_size(px(14.))
                            .text_color(if option.mine {
                                p.accent_text
                            } else {
                                p.text_muted
                            })
                            .when(option.mine, |el| el.font_weight(FontWeight::BOLD))
                            .child(SharedString::from(option.vote_count.to_string())),
                    )
                    .into_any_element(),
            );
        }
        let footer = match poll.total_votes {
            0 => "Aucun vote".to_string(),
            1 => "1 vote".to_string(),
            n => format!("{n} votes"),
        };
        v_flex()
            .w(px(350.))
            .px_4()
            .py(px(14.))
            .rounded(px(16.))
            .bg(p.raised)
            .text_color(p.text)
            .border_1()
            .border_color(p.divider)
            .child(
                div()
                    .mb(px(12.))
                    .text_size(px(15.))
                    .line_height(px(20.))
                    .font_weight(FontWeight::BOLD)
                    .child(SharedString::from(poll.question.clone())),
            )
            .child(v_flex().gap_2().children(options))
            .child(
                div()
                    .mt(px(10.))
                    .text_size(px(12.5))
                    .text_color(p.text_muted)
                    .child(SharedString::from(footer)),
            )
            .into_any_element()
    }

    fn render_image(
        &self,
        message: &Message,
        attachment: &Attachment,
        view: &Entity<Self>,
        p: &Palette,
    ) -> AnyElement {
        let is_sticker = attachment.kind == "sticker";
        let open = {
            let view = view.clone();
            let thread_id = message.thread_id.clone();
            let message_id = message.id.clone();
            let attachment_id = attachment.id.clone();
            move |_: &ClickEvent, _: &mut Window, cx: &mut App| {
                view.update(cx, |this, cx| {
                    let items = this.thread_images(&thread_id);
                    let index = items
                        .iter()
                        .position(|(m, a)| m.id == message_id && a.id == attachment_id)
                        .unwrap_or(0);
                    this.open_lightbox(items, index, cx);
                });
            }
        };
        let (width, height) = if is_sticker {
            (128., 128.)
        } else {
            fit_image(attachment.width, attachment.height)
        };
        let failed = self.thumb_failed(message, attachment);
        let frame = div()
            .id(SharedString::from(format!(
                "img-{}-{}",
                message.id, attachment.id
            )))
            .w(px(width))
            .h(px(height))
            .when(!is_sticker, |el| el.cursor_pointer().on_click(open))
            .rounded(px(if is_sticker { 0. } else { 18. }))
            .overflow_hidden()
            .when(!is_sticker, |el| el.bg(p.field_bg));
        match self.thumb_source(message, attachment) {
            Some(source) => frame
                .child(
                    img(source)
                        .size_full()
                        .when(!is_sticker, |el| el.rounded(px(18.)))
                        .object_fit(if is_sticker {
                            ObjectFit::Contain
                        } else {
                            ObjectFit::Cover
                        }),
                )
                .into_any_element(),
            None => frame
                .flex()
                .items_center()
                .justify_center()
                .text_size(px(12.))
                .text_color(p.text_faint)
                .child(if failed {
                    div().child("Image indisponible").into_any_element()
                } else {
                    Icon::new(IconName::Image)
                        .size(px(26.))
                        .text_color(p.text_faint)
                        .into_any_element()
                })
                .into_any_element(),
        }
    }

    fn render_link(
        &self,
        message: &Message,
        attachment: &Attachment,
        row: &RowContext,
        find: Option<&FindHighlight>,
        p: &Palette,
    ) -> AnyElement {
        let url = attachment.url.clone().unwrap_or_default();
        let source = attachment
            .source
            .clone()
            .filter(|s| !s.is_empty())
            .unwrap_or_else(|| host_of(&url));
        let title = attachment
            .title
            .clone()
            .filter(|t| !t.is_empty())
            .unwrap_or_else(|| url.clone());
        let thumbnail = attachment
            .thumbnail_url
            .clone()
            .filter(|t| !t.is_empty())
            .map(|t| ImageSource::from(SharedString::from(t)))
            .or_else(|| self.thumb_source(message, attachment));
        let hover = if row.outgoing { p.accent_soft } else { p.hover };
        let open_url = url.clone();

        v_flex()
            .id(SharedString::from(format!(
                "link-{}-{}",
                message.id, attachment.id
            )))
            .w(px(300.))
            .rounded(px(18.))
            .overflow_hidden()
            .bg(p.bubble_in)
            .border_1()
            .border_color(p.divider)
            .when(!url.is_empty(), |el| {
                el.cursor_pointer()
                    .hover(move |style| style.bg(hover))
                    .on_click(move |_, _, cx| {
                        if open_url.starts_with("https://") || open_url.starts_with("http://") {
                            cx.open_url(&open_url);
                        }
                    })
            })
            .when_some(thumbnail, |el, source| {
                el.child(
                    div()
                        .w_full()
                        .h(px(158.))
                        .overflow_hidden()
                        .bg(p.field_bg)
                        .child(
                            img(source)
                                .size_full()
                                .rounded_t(px(17.))
                                .object_fit(ObjectFit::Cover),
                        ),
                )
            })
            .child(
                v_flex()
                    .px_3p5()
                    .py_3()
                    .gap_1()
                    .child(
                        h_flex()
                            .gap_1p5()
                            .text_size(px(11.5))
                            .text_color(p.text_faint)
                            .child(Icon::new(IconName::Link).size(px(12.)))
                            .child(SharedString::from(source)),
                    )
                    .child(
                        div()
                            .text_size(scaled(14., row.scale))
                            .line_height(scaled(19., row.scale))
                            .font_weight(FontWeight::SEMIBOLD)
                            .text_color(p.bubble_in_text)
                            .line_clamp(2)
                            .child(highlighted(title, find)),
                    )
                    .when_some(
                        attachment.description.clone().filter(|d| !d.is_empty()),
                        |el, description| {
                            el.child(
                                div()
                                    .text_size(px(12.5))
                                    .line_height(px(17.))
                                    .text_color(p.text_muted)
                                    .line_clamp(2)
                                    .child(SharedString::from(description)),
                            )
                        },
                    ),
            )
            .into_any_element()
    }
}

fn render_chip(
    message: &Message,
    attachment: &Attachment,
    find: Option<&FindHighlight>,
    p: &Palette,
) -> AnyElement {
    let icon = match attachment.kind.as_str() {
        "audio" => IconName::Mic,
        "video" => IconName::Play,
        "file" => IconName::FileText,
        _ => IconName::Share2,
    };
    let label = match attachment.kind.as_str() {
        "audio" => match attachment.duration_ms {
            Some(ms) => format!("Message vocal · {}", format_duration(ms)),
            None => "Message vocal".into(),
        },
        "video" => match attachment.duration_ms {
            Some(ms) => format!("Vidéo · {}", format_duration(ms)),
            None => "Vidéo".into(),
        },
        "file" => attachment
            .file_name
            .clone()
            .unwrap_or_else(|| describe_attachments(message)),
        _ => attachment
            .title
            .clone()
            .filter(|t| !t.is_empty())
            .unwrap_or_else(|| "Contenu partagé".into()),
    };
    h_flex()
        .gap_2p5()
        .pl_2()
        .pr_4()
        .py_2()
        .rounded(px(18.))
        .bg(p.bubble_in)
        .text_color(p.bubble_in_text)
        .child(
            div()
                .size(px(30.))
                .rounded_full()
                .bg(p.panel)
                .flex()
                .items_center()
                .justify_center()
                .child(Icon::new(icon).size(px(15.)).text_color(p.text_muted)),
        )
        .child(div().text_size(px(13.5)).child(highlighted(label, find)))
        .into_any_element()
}

fn render_contact(attachment: &Attachment, p: &Palette) -> AnyElement {
    let title = attachment
        .title
        .clone()
        .filter(|t| !t.is_empty())
        .unwrap_or_else(|| "Contact".into());
    h_flex()
        .max_w(px(300.))
        .gap_3()
        .pl_2()
        .pr_4()
        .py_2()
        .rounded(px(18.))
        .bg(p.bubble_in)
        .text_color(p.bubble_in_text)
        .border_1()
        .border_color(p.divider)
        .child(
            div()
                .size(px(38.))
                .flex_none()
                .rounded_full()
                .bg(p.accent_soft)
                .flex()
                .items_center()
                .justify_center()
                .child(Icon::new(IconName::Contact).size(px(18.)).text_color(p.accent_text)),
        )
        .child(
            v_flex()
                .min_w_0()
                .child(
                    div()
                        .text_size(px(13.5))
                        .font_weight(FontWeight::SEMIBOLD)
                        .truncate()
                        .child(SharedString::from(title)),
                )
                .when_some(
                    attachment.description.clone().filter(|d| !d.is_empty()),
                    |el, description| {
                        el.child(
                            div()
                                .text_size(px(12.))
                                .text_color(p.text_muted)
                                .truncate()
                                .child(SharedString::from(description)),
                        )
                    },
                ),
        )
        .into_any_element()
}

fn render_system_line(
    message: &Message,
    separator: bool,
    last: bool,
    scale: f32,
    compact: bool,
    p: &Palette,
) -> AnyElement {
    centered_row(
        row_column()
            .pt(if compact { px(4.) } else { px(8.) })
            .pb(if compact { px(3.) } else { px(6.) })
            .when(last, |el| el.pb_4())
            .when(separator, |el| {
                el.child(date_separator(message.timestamp_ms, compact, p))
            })
            .child(
                div().w_full().flex().justify_center().px_8().child(
                    div()
                        .max_w(px(480.))
                        .text_center()
                        .text_size(scaled(13., scale))
                        .line_height(scaled(18., scale))
                        .text_color(p.text_muted)
                        .child(SharedString::from(message.text.clone())),
                ),
            ),
    )
    .into_any_element()
}

impl MissiveApp {
    fn render_reaction_details(
        &self,
        message: &Message,
        outgoing: bool,
        view: &Entity<Self>,
        p: &Palette,
    ) -> impl IntoElement {
        let me = self.user.as_ref().map(|user| user.id.clone()).unwrap_or_default();
        let hover = p.hover;
        let mut rows: Vec<AnyElement> = Vec::new();
        for reaction in &message.reactions {
            for reactor in &reaction.reactors {
                let is_me = reactor.user_id == me;
                let label = if is_me {
                    "Vous · cliquer pour retirer".to_string()
                } else {
                    reactor.name.clone()
                };
                let view = view.clone();
                let target = message.clone();
                let emoji = reaction.emoji.clone();
                rows.push(
                    h_flex()
                        .id(SharedString::from(format!(
                            "reactor-{}-{}-{}",
                            message.id, reaction.emoji, reactor.user_id
                        )))
                        .gap_2p5()
                        .px_2()
                        .py_1p5()
                        .rounded(px(10.))
                        .when(is_me, |el| {
                            el.cursor_pointer()
                                .hover(move |s| s.bg(hover))
                                .on_click(move |_, _, cx| {
                                    let target = target.clone();
                                    let emoji = emoji.clone();
                                    view.update(cx, |this, cx| {
                                        this.reaction_details = None;
                                        this.react(&target, &emoji, cx);
                                    });
                                })
                        })
                        .child(avatar(
                            &reactor.name,
                            self.avatar_source(reactor.avatar_url.as_deref()),
                            24.,
                            p,
                        ))
                        .child(
                            div()
                                .flex_1()
                                .min_w_0()
                                .truncate()
                                .text_size(px(13.))
                                .when(is_me, |el| el.text_color(p.text_muted))
                                .child(SharedString::from(label)),
                        )
                        .child(div().text_size(px(16.)).child(SharedString::from(reaction.emoji.clone())))
                        .into_any_element(),
                );
            }
            let unknown = (reaction.count as usize).saturating_sub(reaction.reactors.len());
            if unknown > 0 {
                rows.push(
                    h_flex()
                        .gap_2p5()
                        .px_2()
                        .py_1p5()
                        .text_size(px(12.5))
                        .text_color(p.text_faint)
                        .child(div().flex_1().child(SharedString::from(if unknown == 1 {
                            "1 autre personne".to_string()
                        } else {
                            format!("{unknown} autres personnes")
                        })))
                        .child(div().text_size(px(16.)).child(SharedString::from(reaction.emoji.clone())))
                        .into_any_element(),
                );
            }
        }
        v_flex()
            .mt_1()
            .w(px(260.))
            .max_h(px(280.))
            .p_1()
            .when(outgoing, |el| el.self_end())
            .when(!outgoing, |el| el.self_start())
            .rounded(px(14.))
            .bg(p.panel)
            .border_1()
            .border_color(p.divider)
            .shadow_md()
            .id(SharedString::from(format!("reaction-details-{}", message.id)))
            .overflow_y_scroll()
            .children(rows)
    }
}

fn reaction_pill(
    message: &Message,
    outgoing: bool,
    view: &Entity<MissiveApp>,
    p: &Palette,
) -> impl IntoElement {
    let total: u32 = message.reactions.iter().map(|r| r.count.max(1)).sum();
    let mine = message.reactions.iter().any(|r| r.mine);
    let emojis: String = message
        .reactions
        .iter()
        .take(3)
        .map(|r| r.emoji.as_str())
        .collect();
    let view = view.clone();
    let message_id = message.id.clone();
    h_flex()
        .id(SharedString::from(format!("reactions-{}", message.id)))
        .cursor_pointer()
        .on_click(move |_, _, cx| {
            let message_id = message_id.clone();
            view.update(cx, |this, cx| this.toggle_reaction_details(message_id, cx));
        })
        .flex_none()
        .mt(px(-6.))
        .when(outgoing, |el| el.mr(px(10.)).self_end())
        .when(!outgoing, |el| el.ml(px(10.)).self_start())
        .gap_1()
        .px(px(7.))
        .h(px(22.))
        .rounded(px(11.))
        .map(|el| {
            if p.dark {
                el.bg(p.border_strong)
                    .border_2()
                    .border_color(if mine { p.accent_text } else { p.panel })
            } else {
                el.bg(p.panel)
                    .border_1()
                    .border_color(if mine { p.accent_text } else { p.divider })
            }
        })
        .text_size(px(12.))
        .line_height(px(18.))
        .child(SharedString::from(emojis))
        .when(total > 1, |el| {
            el.child(
                div()
                    .text_color(p.text_muted)
                    .child(SharedString::from(total.to_string())),
            )
        })
}

fn shorten_url(url: &str) -> String {
    const MAX: usize = 48;
    let bare = url
        .trim_start_matches("https://")
        .trim_start_matches("http://")
        .trim_start_matches("www.");
    if bare.chars().count() <= MAX {
        return bare.to_string();
    }
    let mut out: String = bare.chars().take(MAX - 1).collect();
    out.push('…');
    out
}

fn valid_mentions(text: &str, mentions: &[Mention]) -> Vec<std::ops::Range<usize>> {
    let mut ranges: Vec<std::ops::Range<usize>> = mentions
        .iter()
        .map(|mention| mention.offset..mention.offset + mention.length)
        .filter(|range| {
            range.start < range.end
                && range.end <= text.len()
                && text.is_char_boundary(range.start)
                && text.is_char_boundary(range.end)
        })
        .collect();
    ranges.sort_by_key(|range| range.start);
    ranges.dedup_by(|b, a| b.start < a.end);
    ranges
}

fn linkified(
    id: impl Into<ElementId>,
    text: &str,
    link: Hsla,
    mentions: &[Mention],
    mention_color: Hsla,
    find: Option<&FindHighlight>,
) -> AnyElement {
    let urls_found = find_urls(text);
    let mention_ranges = valid_mentions(text, mentions);
    if urls_found.is_empty() && mention_ranges.is_empty() {
        return div()
            .child(highlighted(text.to_string(), find))
            .into_any_element();
    }
    let mut spans: Vec<(std::ops::Range<usize>, bool)> = urls_found
        .iter()
        .filter(|url| {
            !mention_ranges
                .iter()
                .any(|m| url.start < m.end && url.end > m.start)
        })
        .map(|range| (range.clone(), true))
        .chain(mention_ranges.iter().map(|range| (range.clone(), false)))
        .collect();
    spans.sort_by_key(|(range, _)| range.start);

    let mut display = String::with_capacity(text.len());
    let mut link_ranges = Vec::new();
    let mut urls = Vec::new();
    let mut mention_display = Vec::new();
    let mut cursor = 0;
    for (range, is_url) in &spans {
        display.push_str(&text[cursor..range.start]);
        let start = display.len();
        if *is_url {
            display.push_str(&shorten_url(&text[range.clone()]));
            link_ranges.push(start..display.len());
            urls.push(text[range.clone()].to_string());
        } else {
            display.push_str(&text[range.clone()]);
            mention_display.push(start..display.len());
        }
        cursor = range.end;
    }
    display.push_str(&text[cursor..]);

    let link_style = HighlightStyle {
        color: Some(link),
        underline: Some(UnderlineStyle {
            thickness: px(1.),
            color: Some(link),
            wavy: false,
        }),
        ..Default::default()
    };
    let mention_style = HighlightStyle {
        color: Some(mention_color),
        font_weight: Some(FontWeight::SEMIBOLD),
        ..Default::default()
    };
    let mut highlights: Vec<(std::ops::Range<usize>, HighlightStyle)> = link_ranges
        .iter()
        .map(|range| (range.clone(), link_style))
        .chain(
            mention_display
                .iter()
                .map(|range| (range.clone(), mention_style)),
        )
        .collect();
    highlights.sort_by_key(|(range, _)| range.start);
    let found = find.map(|find| find.ranges(&display)).unwrap_or_default();
    let highlights: Vec<(std::ops::Range<usize>, HighlightStyle)> = if found.is_empty() {
        highlights
    } else {
        combine_highlights(highlights, found).collect()
    };
    let styled = StyledText::new(SharedString::from(display)).with_highlights(highlights);
    if link_ranges.is_empty() {
        return styled.into_any_element();
    }
    InteractiveText::new(id, styled)
        .on_click(link_ranges, move |index, _, cx| {
            if let Some(url) = urls.get(index) {
                crate::desktop::open_web(url, cx);
            }
        })
        .into_any_element()
}

fn bubble(row: &RowContext) -> Div {
    let round = px(BUBBLE_RADIUS);
    let tail = px(BUBBLE_TAIL);
    let near_first = if row.first_in_run { round } else { tail };
    let (tl, tr, bl, br) = if row.outgoing {
        (round, near_first, round, tail)
    } else {
        (near_first, round, tail, round)
    };
    v_flex()
        .px(if row.compact { px(12.) } else { px(14.) })
        .py(if row.compact { px(6.) } else { px(9.) })
        .rounded_tl(tl)
        .rounded_tr(tr)
        .rounded_bl(bl)
        .rounded_br(br)
        .text_size(scaled(14.5, row.scale))
        .line_height(scaled(21., row.scale))
}

fn same_run(a: &Message, b: &Message) -> bool {
    !a.system
        && !b.system
        && a.is_outgoing == b.is_outgoing
        && a.sender_id == b.sender_id
        && (b.timestamp_ms - a.timestamp_ms).abs() <= RUN_GAP_MS
}

fn fit_image(width: Option<u32>, height: Option<u32>) -> (f32, f32) {
    const MAX_W: f32 = 320.;
    const MAX_H: f32 = 340.;
    match (width, height) {
        (Some(w), Some(h)) if w > 0 && h > 0 => {
            let (w, h) = (w as f32, h as f32);
            let scale = (MAX_W / w).min(MAX_H / h).min(1.);
            ((w * scale).max(90.), (h * scale).max(70.))
        }
        _ => (260., 195.),
    }
}

fn host_of(url: &str) -> String {
    url.split("://")
        .nth(1)
        .and_then(|rest| rest.split(['/', '?', '#']).next())
        .map(|host| host.trim_start_matches("www.").to_string())
        .unwrap_or_default()
}

fn is_emoji_only(text: &str) -> bool {
    let trimmed = text.trim();
    let count = trimmed.chars().filter(|c| !c.is_whitespace()).count();
    count > 0
        && count <= 8
        && trimmed.chars().all(|c| {
            c.is_whitespace()
                || (c as u32) >= 0x1F000
                || (0x2600..=0x27BF).contains(&(c as u32))
                || c == '\u{200D}'
                || c == '\u{FE0F}'
                || (0x1F3FB..=0x1F3FF).contains(&(c as u32))
        })
}

#[cfg(test)]
mod tests {
    use super::{delivery_label, host_of, is_emoji_only, parse_hex_color, same_run, shorten_url};
    use crate::model::{Message, ReadReceipt};

    fn receipt(read_ms: i64, delivered_ms: Option<i64>) -> ReadReceipt {
        ReadReceipt {
            user_id: "u".into(),
            name: "U".into(),
            avatar_url: None,
            read_ms,
            delivered_ms,
        }
    }

    #[::core::prelude::v1::test]
    fn delivery_states() {
        assert_eq!(delivery_label(&[], 100, false, 1, true), "Envoyé");
        assert_eq!(delivery_label(&[receipt(0, None)], 100, false, 1, false), "Envoyé");
        assert_eq!(
            delivery_label(&[receipt(0, Some(120))], 100, false, 1, false),
            "Distribué"
        );
        assert_eq!(delivery_label(&[receipt(150, Some(120))], 100, false, 1, false), "Vu");
        assert_eq!(
            delivery_label(&[receipt(150, None), receipt(90, Some(100))], 100, true, 3, false),
            "Vu par 1"
        );
        assert_eq!(
            delivery_label(&[receipt(150, None), receipt(100, None)], 100, true, 2, false),
            "Vu par tout le monde"
        );
    }

    #[::core::prelude::v1::test]
    fn theme_hex_parsing() {
        let color = parse_hex_color("#FF0080").expect("valid color");
        assert!((color.r - 1.).abs() < 1e-6 && color.g.abs() < 1e-6);
        assert!(parse_hex_color("FF0080").is_none());
        assert!(parse_hex_color("#GG0000").is_none());
        assert!(parse_hex_color("#FFF").is_none());
    }

    #[::core::prelude::v1::test]
    fn system_lines_break_runs() {
        let a = Message {
            id: "a".into(),
            sender_id: "x".into(),
            timestamp_ms: 0,
            ..Message::default()
        };
        let b = Message {
            id: "b".into(),
            sender_id: "x".into(),
            timestamp_ms: 1000,
            ..Message::default()
        };
        assert!(same_run(&a, &b));
        let system = Message {
            system: true,
            ..b.clone()
        };
        assert!(!same_run(&a, &system));
    }

    #[::core::prelude::v1::test]
    fn long_urls_are_shortened() {
        assert_eq!(shorten_url("https://www.exemple.fr/a"), "exemple.fr/a");
        let long = format!("https://amazon.com.be/{}", "x".repeat(200));
        let short = shorten_url(&long);
        assert_eq!(short.chars().count(), 48);
        assert!(short.ends_with('…'));
    }

    #[test]
    fn host_strips_scheme_and_www() {
        assert_eq!(host_of("https://www.youtube.com/watch?v=x"), "youtube.com");
        assert_eq!(host_of("nope"), "");
    }

    #[test]
    fn emoji_only_detection() {
        assert!(is_emoji_only("👍"));
        assert!(is_emoji_only("😂😂"));
        assert!(!is_emoji_only("ok 👍"));
        assert!(!is_emoji_only(""));
    }
}
