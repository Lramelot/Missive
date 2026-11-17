use chrono::{Datelike as _, Local, TimeZone as _};
use gpui_kit::assets::IconName;
use gpui_kit::component::{
    Icon, h_flex, input::Input, scroll::ScrollableElement as _, tooltip::Tooltip, v_flex,
};
use gpui_kit::{prelude::FluentBuilder as _, *};

use super::{
    sidebar::display_name,
    widgets::{format_short, icon_button, thread_avatar},
};
use crate::{
    app::{GalleryTab, Media, MissiveApp, media_key},
    model::{Attachment, MediaItem, Message},
    style::Palette,
};

const MONTHS_LONG: [&str; 12] = [
    "janvier",
    "février",
    "mars",
    "avril",
    "mai",
    "juin",
    "juillet",
    "août",
    "septembre",
    "octobre",
    "novembre",
    "décembre",
];

fn scrim() -> Div {
    div()
        .absolute()
        .inset_0()
        .flex()
        .items_center()
        .justify_center()
        .bg(hsla(0., 0., 0., 0.45))
}

fn month_label(timestamp_ms: i64) -> String {
    Local
        .timestamp_millis_opt(timestamp_ms)
        .single()
        .map(|time| format!("{} {}", MONTHS_LONG[time.month0() as usize], time.year()))
        .unwrap_or_default()
}

impl MissiveApp {
    pub(super) fn render_gallery(&self, p: &Palette, cx: &mut Context<Self>) -> impl IntoElement {
        let Some(gallery) = &self.gallery else {
            return div().into_any_element();
        };
        let thread_name = self
            .threads
            .iter()
            .find(|thread| thread.id == gallery.thread_id)
            .map(display_name)
            .unwrap_or_default();

        let tabs: Vec<AnyElement> = [
            ("tab-media", "Photos et vidéos", GalleryTab::Media),
            ("tab-files", "Fichiers", GalleryTab::Files),
            ("tab-links", "Liens", GalleryTab::Links),
        ]
        .into_iter()
        .map(|(id, label, value)| {
            let active = gallery.tab == value;
            let hover = p.hover;
            div()
                .id(id)
                .px_3()
                .h(px(32.))
                .flex()
                .items_center()
                .rounded_full()
                .text_size(px(13.))
                .cursor_pointer()
                .font_weight(FontWeight::SEMIBOLD)
                .when(active, |el| el.bg(p.accent_soft).text_color(p.accent_text))
                .when(!active, |el| {
                    el.text_color(p.text_muted).hover(move |s| s.bg(hover))
                })
                .child(label)
                .on_click(cx.listener(move |this, _, _, cx| this.open_gallery(value, cx)))
                .into_any_element()
        })
        .collect();

        let mut body = v_flex().gap_4().px_6().pb_6();
        let mut current_month = String::new();
        let mut month_items: Vec<&MediaItem> = Vec::new();
        let mut sections: Vec<(String, Vec<&MediaItem>)> = Vec::new();
        for item in &gallery.items {
            let month = month_label(item.timestamp_ms);
            if month != current_month && !month_items.is_empty() {
                sections.push((current_month.clone(), std::mem::take(&mut month_items)));
            }
            current_month = month;
            month_items.push(item);
        }
        if !month_items.is_empty() {
            sections.push((current_month, month_items));
        }

        let media_list: Vec<(Message, Attachment)> = gallery
            .items
            .iter()
            .filter(|item| item.attachment.kind == "image")
            .map(|item| (crate::app::item_message(item), item.attachment.clone()))
            .collect();

        for (month, items) in sections {
            let section = v_flex().gap_2().child(
                div()
                    .text_size(px(13.))
                    .font_weight(FontWeight::SEMIBOLD)
                    .text_color(p.text_muted)
                    .child(SharedString::from(month)),
            );
            let section = match gallery.tab {
                GalleryTab::Media => {
                    let mut tiles = Vec::with_capacity(items.len());
                    for item in items {
                        tiles.push(self.gallery_tile(item, &media_list, p, cx));
                    }
                    section.child(div().grid().grid_cols(6).gap(px(6.)).children(tiles))
                }
                _ => section.child(
                    v_flex()
                        .gap_1()
                        .children(items.into_iter().map(|item| self.gallery_row(item, p))),
                ),
            };
            body = body.child(section);
        }

        let offset = gallery.scroll.offset().y;
        let max = gallery.scroll.max_offset().y;
        let remaining = max + offset;
        let near_bottom = remaining < px(700.);
        let show_fade = gallery.has_more || remaining > px(4.);
        if near_bottom && gallery.has_more && !gallery.loading && gallery.error.is_none() {
            cx.spawn(async move |this, cx| {
                let _ = this.update(cx, |this, cx| this.load_gallery_page(cx));
            })
            .detach();
        }

        let footer = if gallery.loading {
            h_flex()
                .justify_center()
                .py_4()
                .gap_2()
                .text_size(px(13.))
                .text_color(p.text_muted)
                .child(Icon::new(IconName::LoaderCircle).size(px(16.)))
                .child("Chargement de l'historique…")
                .into_any_element()
        } else if let Some(error) = gallery.error.clone() {
            div()
                .py_4()
                .text_center()
                .text_size(px(13.))
                .text_color(p.danger)
                .child(error)
                .into_any_element()
        } else if gallery.has_more {
            h_flex()
                .justify_center()
                .py_3()
                .child(
                    div()
                        .id("gallery-more")
                        .px_4()
                        .h(px(36.))
                        .flex()
                        .items_center()
                        .rounded(px(12.))
                        .bg(p.field_bg)
                        .text_size(px(13.))
                        .font_weight(FontWeight::SEMIBOLD)
                        .cursor_pointer()
                        .child("Charger plus ancien")
                        .on_click(cx.listener(|this, _, _, cx| this.load_gallery_page(cx))),
                )
                .into_any_element()
        } else {
            div()
                .py_4()
                .text_center()
                .text_size(px(12.5))
                .text_color(p.text_faint)
                .child(if gallery.items.is_empty() {
                    "Rien de partagé ici."
                } else {
                    "Début de la conversation atteint."
                })
                .into_any_element()
        };

        scrim()
            .child(
                v_flex()
                    .id("gallery")
                    .w(px(820.))
                    .max_w(relative(0.92))
                    .h(relative(0.86))
                    .rounded(px(18.))
                    .border_1()
                    .border_color(p.divider)
                    .bg(p.panel)
                    .shadow_lg()
                    .overflow_hidden()
                    .on_mouse_down(MouseButton::Left, |_, _, cx| cx.stop_propagation())
                    .child(
                        h_flex()
                            .flex_none()
                            .px_6()
                            .pt_5()
                            .pb_3()
                            .gap_3()
                            .child(
                                v_flex()
                                    .flex_1()
                                    .child(
                                        div()
                                            .text_size(px(19.))
                                            .font_weight(FontWeight::EXTRA_BOLD)
                                            .child("Médias partagés"),
                                    )
                                    .child(
                                        div()
                                            .text_size(px(12.5))
                                            .text_color(p.text_muted)
                                            .child(SharedString::from(thread_name)),
                                    ),
                            )
                            .child(
                                icon_button("close-gallery", IconName::X, false, p)
                                    .on_click(cx.listener(|this, _, _, cx| this.close_gallery(cx))),
                            ),
                    )
                    .child(
                        h_flex()
                            .flex_none()
                            .px_6()
                            .pb_4()
                            .gap_1p5()
                            .children(tabs)
                            .child(div().flex_1())
                            .child(div().text_size(px(12.5)).text_color(p.text_faint).child(
                                SharedString::from(gallery_count_label(
                                    gallery.items.len(),
                                    gallery.has_more,
                                )),
                            )),
                    )
                    .child(
                        div()
                            .relative()
                            .flex_1()
                            .min_h_0()
                            .border_t_1()
                            .border_color(p.divider)
                            .child(
                                div()
                                    .id("gallery-scroll")
                                    .size_full()
                                    .pt_4()
                                    .overflow_y_scroll()
                                    .track_scroll(&gallery.scroll)
                                    .vertical_scrollbar(&gallery.scroll)
                                    .child(body.child(footer)),
                            )
                            .when(show_fade, |el| {
                                el.child(
                                    div()
                                        .absolute()
                                        .left_0()
                                        .right_0()
                                        .bottom_0()
                                        .h(px(56.))
                                        .bg(linear_gradient(
                                            180.,
                                            linear_color_stop(p.panel.opacity(0.), 0.),
                                            linear_color_stop(p.panel, 1.),
                                        )),
                                )
                            }),
                    ),
            )
            .into_any_element()
    }

    fn gallery_tile(
        &self,
        item: &MediaItem,
        media_list: &[(Message, Attachment)],
        p: &Palette,
        cx: &mut Context<Self>,
    ) -> AnyElement {
        let message = crate::app::item_message(item);
        let is_video = item.attachment.kind == "video";
        let source = self.thumb_source(&message, &item.attachment);
        let index = media_list
            .iter()
            .position(|(m, a)| m.id == item.message_id && a.id == item.attachment.id);
        let list = media_list.to_vec();
        let video_url = item.attachment.url.clone();
        div()
            .id(SharedString::from(format!(
                "tile-{}-{}",
                item.message_id, item.attachment.id
            )))
            .relative()
            .w_full()
            .aspect_square()
            .rounded(px(12.))
            .overflow_hidden()
            .bg(p.field_bg)
            .cursor_pointer()
            .when_some(source, |el, source| {
                el.child(
                    img(source)
                        .size_full()
                        .rounded(px(12.))
                        .object_fit(ObjectFit::Cover),
                )
            })
            .when(is_video, |el| {
                el.child(
                    div()
                        .absolute()
                        .inset_0()
                        .flex()
                        .items_center()
                        .justify_center()
                        .child(
                            div()
                                .size(px(36.))
                                .rounded_full()
                                .bg(hsla(0., 0., 0., 0.55))
                                .flex()
                                .items_center()
                                .justify_center()
                                .child(Icon::new(IconName::Play).size(px(16.)).text_color(white())),
                        ),
                )
            })
            .on_click(cx.listener(move |this, _, _, cx| {
                if let Some(index) = index {
                    this.open_lightbox(list.clone(), index, cx);
                } else if let Some(url) = video_url.as_deref() {
                    crate::desktop::open_web(url, cx);
                }
            }))
            .into_any_element()
    }

    fn gallery_row(&self, item: &MediaItem, p: &Palette) -> impl IntoElement {
        let attachment = &item.attachment;
        let (icon, title, subtitle) = match attachment.kind.as_str() {
            "link" => (
                IconName::Link,
                attachment
                    .title
                    .clone()
                    .or_else(|| attachment.url.clone())
                    .unwrap_or_else(|| "Lien".into()),
                attachment.source.clone().unwrap_or_default(),
            ),
            "audio" => (IconName::Mic, "Message vocal".into(), String::new()),
            _ => (
                IconName::FileText,
                attachment
                    .file_name
                    .clone()
                    .unwrap_or_else(|| "Fichier".into()),
                attachment.size.map(human_size).unwrap_or_default(),
            ),
        };
        let url = attachment.url.clone();
        let is_link = attachment.kind == "link";
        let message = crate::app::item_message(item);
        let local = match self.media.get(&media_key(&message.id, &attachment.id)) {
            Some(Media::Ready(path)) => Some(path.clone()),
            _ => None,
        };
        let hover = p.hover;
        h_flex()
            .id(SharedString::from(format!(
                "row-{}-{}",
                item.message_id, attachment.id
            )))
            .gap_3()
            .px_3()
            .py_2p5()
            .rounded(px(12.))
            .cursor_pointer()
            .hover(move |s| s.bg(hover))
            .child(
                div()
                    .size(px(38.))
                    .flex_none()
                    .rounded(px(10.))
                    .bg(p.field_bg)
                    .flex()
                    .items_center()
                    .justify_center()
                    .child(Icon::new(icon).size(px(17.)).text_color(p.text_muted)),
            )
            .child(
                v_flex()
                    .flex_1()
                    .min_w_0()
                    .child(
                        div()
                            .text_size(px(13.5))
                            .font_weight(FontWeight::SEMIBOLD)
                            .truncate()
                            .child(SharedString::from(title)),
                    )
                    .child(
                        div()
                            .text_size(px(12.))
                            .text_color(p.text_faint)
                            .truncate()
                            .child(SharedString::from(
                                [
                                    subtitle,
                                    item.sender_name.clone(),
                                    format_short(item.timestamp_ms).to_string(),
                                ]
                                .into_iter()
                                .filter(|part| !part.is_empty())
                                .collect::<Vec<_>>()
                                .join(" · "),
                            )),
                    ),
            )
            .on_click(move |_, _, cx| {
                if is_link {
                    if let Some(url) = url.as_deref() {
                        crate::desktop::open_web(url, cx);
                    }
                } else if let Some(path) = &local {
                    crate::desktop::open_local(path, cx);
                } else if let Some(url) = url.as_deref() {
                    crate::desktop::open_web(url, cx);
                }
            })
    }

    pub(super) fn render_lightbox(&self, cx: &mut Context<Self>) -> impl IntoElement {
        let Some(lightbox) = &self.lightbox else {
            return div().into_any_element();
        };
        let Some((message, attachment)) = lightbox.items.get(lightbox.index) else {
            return div().into_any_element();
        };
        let source = self
            .full_source(message, attachment)
            .or_else(|| self.thumb_source(message, attachment));
        let count = lightbox.items.len();
        let index = lightbox.index;
        let caption = format!(
            "{}{}{} / {}",
            if message.sender_name.is_empty() {
                ""
            } else {
                &message.sender_name
            },
            if message.sender_name.is_empty() {
                ""
            } else {
                " · "
            },
            index + 1,
            count
        );
        let control = |id: &'static str, icon: IconName| {
            div()
                .id(id)
                .size(px(44.))
                .flex_none()
                .rounded_full()
                .bg(hsla(0., 0., 1., 0.12))
                .flex()
                .items_center()
                .justify_center()
                .cursor_pointer()
                .hover(|s| s.bg(hsla(0., 0., 1., 0.22)))
                .on_mouse_down(MouseButton::Left, |_, _, cx| cx.stop_propagation())
                .child(Icon::new(icon).size(px(20.)).text_color(white()))
        };
        let tool = |id: &'static str, icon: IconName, label: &'static str| {
            div()
                .id(id)
                .size(px(36.))
                .flex_none()
                .rounded_full()
                .flex()
                .items_center()
                .justify_center()
                .cursor_pointer()
                .hover(|s| s.bg(hsla(0., 0., 1., 0.16)))
                .tooltip(move |window, cx| Tooltip::new(label).build(window, cx))
                .on_mouse_down(MouseButton::Left, |_, _, cx| cx.stop_propagation())
                .child(Icon::new(icon).size(px(18.)).text_color(white()))
        };
        let ratio = match (attachment.width, attachment.height) {
            (Some(w), Some(h)) if w > 0 && h > 0 => Some(w as f32 / h as f32),
            _ => None,
        };
        let layout = self.lightbox_layout();
        let busy = self.find.viewer.busy;
        let image = match source {
            Some(source) => {
                let frame = div()
                    .id("lightbox-image")
                    .on_mouse_down(
                        MouseButton::Left,
                        cx.listener(|this, event: &MouseDownEvent, _, cx| {
                            cx.stop_propagation();
                            this.start_lightbox_drag(event.position);
                        }),
                    )
                    .on_click(cx.listener(|this, event: &ClickEvent, _, cx| {
                        if event.click_count() == 2 {
                            this.toggle_lightbox_actual_size(event.position(), cx);
                        }
                    }))
                    .when(!layout.fitted, |el| el.cursor(CursorStyle::OpenHand))
                    .child(img(source).size_full().object_fit(ObjectFit::Contain));
                if layout.fitted {
                    match ratio {
                        Some(ratio) => frame.h_full().max_w_full().max_h_full().aspect_ratio(ratio),
                        None => frame.size_full(),
                    }
                    .into_any_element()
                } else {
                    let (cw, ch) = layout.container;
                    let (w, h) = layout.size;
                    frame
                        .absolute()
                        .left(px((cw - w) / 2. + layout.pan.0))
                        .top(px((ch - h) / 2. + layout.pan.1))
                        .w(px(w))
                        .h(px(h))
                        .into_any_element()
                }
            }
            None => div()
                .text_color(hsla(0., 0., 1., 0.6))
                .child("Chargement…")
                .into_any_element(),
        };
        let bounds = self.find.viewer.bounds.clone();
        let stage = div()
            .id("lightbox-stage")
            .relative()
            .flex_1()
            .h_full()
            .overflow_hidden()
            .flex()
            .items_center()
            .justify_center()
            .on_scroll(cx.listener(|this, event: &ScrollEvent, _, cx| {
                this.lightbox_wheel(event, cx)
            }))
            .on_mouse_move(cx.listener(|this, event: &MouseMoveEvent, _, cx| {
                this.drag_lightbox(event, cx)
            }))
            .on_mouse_up(
                MouseButton::Left,
                cx.listener(|this, _: &MouseUpEvent, _, _| this.end_lightbox_drag()),
            )
            .child(
                canvas(move |area, _, _| bounds.set(area), |_, _, _, _| {})
                    .absolute()
                    .inset_0(),
            )
            .child(image);
        let toolbar = h_flex()
            .flex_none()
            .gap_1()
            .px_1()
            .rounded_full()
            .bg(hsla(0., 0., 1., 0.08))
            .on_mouse_down(MouseButton::Left, |_, _, cx| cx.stop_propagation())
            .child(
                tool("lightbox-zoom-out", IconName::ZoomOut, "Zoom arrière (−)")
                    .on_click(cx.listener(|this, _, _, cx| this.zoom_lightbox(-1, cx))),
            )
            .child(
                div()
                    .id("lightbox-zoom-level")
                    .min_w(px(52.))
                    .text_center()
                    .text_size(px(12.5))
                    .text_color(hsla(0., 0., 1., 0.85))
                    .cursor_pointer()
                    .tooltip(|window, cx| Tooltip::new("Ajuster à la fenêtre (0)").build(window, cx))
                    .on_mouse_down(MouseButton::Left, |_, _, cx| cx.stop_propagation())
                    .on_click(cx.listener(|this, _, _, cx| this.reset_lightbox_zoom(cx)))
                    .child(SharedString::from(format!("{} %", layout.percent))),
            )
            .child(
                tool("lightbox-zoom-in", IconName::ZoomIn, "Zoom avant (+)")
                    .on_click(cx.listener(|this, _, _, cx| this.zoom_lightbox(1, cx))),
            )
            .child(
                tool("lightbox-fit", IconName::Scan, "Ajuster à la fenêtre (0)")
                    .on_click(cx.listener(|this, _, _, cx| this.reset_lightbox_zoom(cx))),
            );
        let actions = h_flex()
            .flex_none()
            .gap_1()
            .px_1()
            .rounded_full()
            .bg(hsla(0., 0., 1., 0.08))
            .on_mouse_down(MouseButton::Left, |_, _, cx| cx.stop_propagation())
            .when(busy, |el| {
                el.child(
                    div()
                        .size(px(36.))
                        .flex()
                        .items_center()
                        .justify_center()
                        .child(
                            Icon::new(IconName::LoaderCircle)
                                .size(px(16.))
                                .text_color(hsla(0., 0., 1., 0.7)),
                        ),
                )
            })
            .child(
                tool("lightbox-copy", IconName::Copy, "Copier l'image")
                    .on_click(cx.listener(|this, _, _, cx| this.copy_lightbox_image(cx))),
            )
            .child(
                tool("lightbox-save", IconName::Save, "Enregistrer sous…")
                    .on_click(cx.listener(|this, _, _, cx| this.save_lightbox_image(cx))),
            )
            .child(
                tool(
                    "lightbox-open",
                    IconName::ExternalLink,
                    "Ouvrir avec l'application par défaut",
                )
                .on_click(cx.listener(|this, _, _, cx| this.open_lightbox_externally(cx))),
            );

        div()
            .id("lightbox")
            .absolute()
            .inset_0()
            .bg(hsla(0., 0., 0.04, 0.94))
            .flex()
            .flex_col()
            .cursor_pointer()
            .on_mouse_down(
                MouseButton::Left,
                cx.listener(|this, _, _, cx| {
                    cx.stop_propagation();
                    this.close_lightbox(cx);
                }),
            )
            .child(
                h_flex()
                    .flex_none()
                    .px_5()
                    .py_3()
                    .gap_3()
                    .child(
                        div()
                            .flex_1()
                            .min_w_0()
                            .truncate()
                            .text_size(px(13.))
                            .text_color(hsla(0., 0., 1., 0.8))
                            .child(SharedString::from(caption)),
                    )
                    .child(toolbar)
                    .child(actions)
                    .child(
                        control("lightbox-close", IconName::X)
                            .on_click(cx.listener(|this, _, _, cx| this.close_lightbox(cx))),
                    ),
            )
            .child(
                h_flex()
                    .flex_1()
                    .min_h_0()
                    .px_4()
                    .pb_6()
                    .gap_4()
                    .child(
                        control("lightbox-prev", IconName::ChevronLeft)
                            .when(index == 0, |el| el.invisible())
                            .on_click(cx.listener(|this, _, _, cx| this.step_lightbox(-1, cx))),
                    )
                    .child(stage)
                    .child(
                        control("lightbox-next", IconName::ChevronRight)
                            .when(index + 1 >= count, |el| el.invisible())
                            .on_click(cx.listener(|this, _, _, cx| this.step_lightbox(1, cx))),
                    ),
            )
            .into_any_element()
    }

    pub(super) fn render_switcher(&self, p: &Palette, cx: &mut Context<Self>) -> impl IntoElement {
        let Some(switcher) = &self.switcher else {
            return div().into_any_element();
        };
        let results = self.switcher_results(cx);
        let selected = switcher.selected.min(results.len().saturating_sub(1));
        let hover = p.hover;

        scrim()
            .items_start()
            .pt(px(110.))
            .on_mouse_down(
                MouseButton::Left,
                cx.listener(|this, _, _, cx| this.close_switcher(cx)),
            )
            .child(
                v_flex()
                    .id("switcher")
                    .w(px(520.))
                    .rounded(px(18.))
                    .border_1()
                    .border_color(p.divider)
                    .bg(p.panel)
                    .shadow_lg()
                    .overflow_hidden()
                    .on_mouse_down(MouseButton::Left, |_, _, cx| cx.stop_propagation())
                    .child(
                        h_flex()
                            .px_4()
                            .h(px(52.))
                            .gap_3()
                            .border_b_1()
                            .border_color(p.divider)
                            .child(
                                Icon::new(IconName::Search)
                                    .size(px(17.))
                                    .text_color(p.text_muted),
                            )
                            .child(
                                div()
                                    .flex_1()
                                    .child(Input::new(&switcher.input).appearance(false)),
                            )
                            .child(
                                div()
                                    .px(px(6.))
                                    .py(px(1.))
                                    .rounded(px(6.))
                                    .border_1()
                                    .border_color(p.border_strong)
                                    .text_size(px(11.5))
                                    .font_weight(FontWeight::SEMIBOLD)
                                    .text_color(p.text_muted)
                                    .child("Échap"),
                            ),
                    )
                    .child(
                        v_flex()
                            .p_2()
                            .gap_0p5()
                            .when(results.is_empty(), |el| {
                                el.child(
                                    div()
                                        .py_4()
                                        .text_center()
                                        .text_size(px(13.))
                                        .text_color(p.text_faint)
                                        .child("Aucune discussion"),
                                )
                            })
                            .children(results.iter().enumerate().filter_map(|(row, index)| {
                                let thread = self.threads.get(*index)?;
                                let id = thread.id.clone();
                                Some(
                                    h_flex()
                                        .id(("switcher-row", row))
                                        .px_3()
                                        .h(px(52.))
                                        .gap_3()
                                        .rounded(px(12.))
                                        .cursor_pointer()
                                        .when(row == selected, |el| el.bg(p.accent_soft))
                                        .when(row != selected, |el| el.hover(move |s| s.bg(hover)))
                                        .child(thread_avatar(
                                            self.avatar_spec(&thread),
                                            34.,
                                            p.panel,
                                            p,
                                        ))
                                        .child(
                                            div()
                                                .flex_1()
                                                .min_w_0()
                                                .truncate()
                                                .font_weight(FontWeight::SEMIBOLD)
                                                .child(SharedString::from(display_name(thread))),
                                        )
                                        .child(
                                            div()
                                                .text_size(px(12.))
                                                .text_color(p.text_faint)
                                                .child(format_short(thread.last_activity_ms)),
                                        )
                                        .on_click(cx.listener(move |this, _, window, cx| {
                                            this.switcher = None;
                                            this.select_thread(id.clone(), window, cx);
                                        })),
                                )
                            })),
                    )
                    .child(
                        h_flex()
                            .px_4()
                            .py_2()
                            .gap_4()
                            .border_t_1()
                            .border_color(p.divider)
                            .text_size(px(11.5))
                            .text_color(p.text_faint)
                            .child("↑ ↓ naviguer")
                            .child("Entrée ouvrir")
                            .child("Alt+↑/↓ discussion précédente/suivante"),
                    ),
            )
            .into_any_element()
    }
}

fn human_size(bytes: i64) -> String {
    let bytes = bytes.max(0) as f64;
    if bytes < 1024. {
        format!("{bytes:.0} o")
    } else if bytes < 1024. * 1024. {
        format!("{:.0} Ko", bytes / 1024.)
    } else {
        format!("{:.1} Mo", bytes / 1024. / 1024.)
    }
}

fn gallery_count_label(count: usize, has_more: bool) -> String {
    let base = match count {
        0 => "Aucun élément".to_string(),
        1 => "1 élément".to_string(),
        n => format!("{n} éléments"),
    };
    if has_more && count > 0 {
        format!("{base} · défile pour en voir plus")
    } else {
        base
    }
}
