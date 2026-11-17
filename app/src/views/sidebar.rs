use gpui_kit::assets::IconName;
use gpui_kit::component::{
    Icon, Sizable as _,
    button::{Button, ButtonVariants as _},
    h_flex,
    input::Input,
    tooltip::Tooltip,
    v_flex,
};
use gpui_kit::{prelude::FluentBuilder as _, *};

use super::widgets::{
    AvatarSpec, avatar, format_short, link_color, thread_avatar, typing_dots, unread_pill,
};
use crate::{
    app::{Filter, History, MissiveApp, describe_attachments},
    model::{SearchHit, Thread},
    settings::ThemePreference,
    style::Palette,
};

const ROW_HEIGHT: f32 = 68.;
const ROW_GAP: f32 = 2.;

impl MissiveApp {
    pub(super) fn render_sidebar(&self, p: &Palette, cx: &mut Context<Self>) -> impl IntoElement {
        let view = cx.entity();
        let visible = self.listed_threads(cx);
        let count = visible.len();
        let palette = *p;
        if self.needs_local_sweep() {
            cx.spawn(async move |this, cx| {
                let _ = this.update(cx, |this, cx| this.sweep_local_state(cx));
            })
            .detach();
        }
        let in_folder = self.active_folder().is_some()
            && matches!(self.filter, Filter::All | Filter::Unread | Filter::Groups);
        let empty_label = match self.filter {
            Filter::Requests => "Aucune demande de message",
            Filter::Archived => "Aucune discussion archivée",
            _ if in_folder => "Aucune discussion dans ce dossier. Ajoutez-en par clic droit sur une discussion.",
            Filter::Unread => "Aucune discussion non lue",
            Filter::Groups => "Aucun groupe",
            Filter::All => "Aucune discussion",
        };
        let has_filters = self.thread_ops.search.query.has_filters();
        let searching = self.thread_ops.search.loading;
        let filters_hover = p.hover;

        let title = match self.filter {
            Filter::Requests => "Demandes",
            Filter::Archived => "Archivées",
            Filter::All | Filter::Unread | Filter::Groups => "Discussions",
        };
        let folder = matches!(self.filter, Filter::Requests | Filter::Archived);
        let accent_hover = p.accent_strong;

        v_flex()
            .w(px(330.))
            .h_full()
            .flex_none()
            .border_r_1()
            .border_color(p.divider)
            .child(
                h_flex()
                    .pl(px(20.))
                    .pr(px(14.))
                    .pt(px(20.))
                    .pb(px(12.))
                    .justify_between()
                    .child(
                        div()
                            .text_size(px(24.))
                            .font_weight(FontWeight::EXTRA_BOLD)
                            .child(title),
                    )
                    .child(
                        div()
                            .id("new-message")
                            .size(px(38.))
                            .flex_none()
                            .rounded(px(12.))
                            .bg(p.accent)
                            .flex()
                            .items_center()
                            .justify_center()
                            .cursor_pointer()
                            .hover(move |style| style.bg(accent_hover))
                            .tooltip(|window, cx| Tooltip::new("Nouveau message").build(window, cx))
                            .child(
                                Icon::new(IconName::SquarePen)
                                    .size(px(18.))
                                    .text_color(p.on_accent),
                            )
                            .on_click(
                                cx.listener(|this, _, window, cx| this.open_new_chat(window, cx)),
                            ),
                    ),
            )
            .child(
                h_flex()
                    .mx(px(14.))
                    .h(px(40.))
                    .px_3()
                    .gap(px(10.))
                    .rounded(px(12.))
                    .bg(p.field_bg)
                    .child(
                        Icon::new(IconName::Search)
                            .size(px(16.))
                            .text_color(p.text_muted),
                    )
                    .child(
                        div()
                            .flex_1()
                            .min_w_0()
                            .child(Input::new(&self.search).appearance(false).cleanable(true)),
                    )
                    .when(searching, |el| {
                        el.child(
                            Icon::new(IconName::LoaderCircle)
                                .size(px(14.))
                                .text_color(p.text_faint),
                        )
                    })
                    .when(self.search_query.is_empty(), |el| {
                        el.child(
                            div()
                                .flex_none()
                                .px(px(6.))
                                .py(px(1.))
                                .rounded(px(6.))
                                .border_1()
                                .border_color(p.border_strong)
                                .text_size(px(11.5))
                                .font_weight(FontWeight::SEMIBOLD)
                                .text_color(p.text_muted)
                                .child("Ctrl K"),
                        )
                    })
                    .child(
                        div()
                            .id("search-filters")
                            .size(px(28.))
                            .flex_none()
                            .rounded(px(8.))
                            .flex()
                            .items_center()
                            .justify_center()
                            .cursor_pointer()
                            .when(has_filters, |el| el.bg(p.accent_soft))
                            .when(!has_filters, |el| {
                                el.hover(move |style| style.bg(filters_hover))
                            })
                            .tooltip(|window, cx| {
                                Tooltip::new("Filtres de recherche").build(window, cx)
                            })
                            .child(Icon::new(IconName::SlidersHorizontal).size(px(15.)).text_color(
                                if has_filters {
                                    p.accent_text
                                } else {
                                    p.text_muted
                                },
                            ))
                            .on_click(cx.listener(|this, event: &ClickEvent, window, cx| {
                                this.open_search_panel(event.position(), window, cx)
                            })),
                    ),
            )
            .children(self.render_search_chips(p, cx))
            .child(if folder {
                div()
                    .h(px(12.))
                    .border_b_1()
                    .border_color(p.divider)
                    .into_any_element()
            } else {
                self.render_filter_tabs(p, cx)
            })
            .when(self.history == History::Available, |el| {
                el.child(self.render_history_banner(p, cx))
            })
            .child(if self.search_active() {
                self.render_search_results(&visible, p, cx)
                    .into_any_element()
            } else if count == 0 {
                div()
                    .flex_1()
                    .flex()
                    .items_center()
                    .justify_center()
                    .px_6()
                    .text_center()
                    .text_size(px(13.))
                    .text_color(p.text_faint)
                    .child(empty_label)
                    .into_any_element()
            } else {
                uniform_list("threads", count, move |range, _, cx| {
                    let this = view.read(cx);
                    range
                        .filter_map(|row| {
                            let index = *visible.get(row)?;
                            let thread = this.threads.get(index)?;
                            Some(this.render_thread_row(row, thread, &palette, &view))
                        })
                        .collect::<Vec<_>>()
                })
                .flex_1()
                .pt_2()
                .pb_2()
                .into_any_element()
            })
    }

    pub(super) fn render_rail(&self, p: &Palette, cx: &mut Context<Self>) -> AnyElement {
        let user_name = self
            .user
            .as_ref()
            .map(|user| user.name.clone())
            .unwrap_or_default();
        let user_avatar = self.user.as_ref().and_then(|user| user.avatar_url.clone());
        let requests = self.folder_count(Filter::Requests);
        let dark = p.dark;
        let discussions = matches!(self.filter, Filter::All | Filter::Unread | Filter::Groups);

        v_flex()
            .w(px(68.))
            .h_full()
            .flex_none()
            .items_center()
            .gap(px(6.))
            .pt(px(8.))
            .child(
                rail_button("rail-discussions", IconName::MessageCircle, "Discussions", discussions, p)
                    .on_click(cx.listener(|this, _, _, cx| {
                        if matches!(this.filter, Filter::Requests | Filter::Archived) {
                            this.set_filter(Filter::All, cx);
                        }
                    })),
            )
            .child(
                rail_button(
                    "rail-requests",
                    IconName::Inbox,
                    "Demandes de message",
                    self.filter == Filter::Requests,
                    p,
                )
                .relative()
                .when(requests > 0, |el| {
                    el.child(
                        div()
                            .absolute()
                            .top(px(6.))
                            .right(px(5.))
                            .min_w(px(20.))
                            .h(px(20.))
                            .px(px(3.))
                            .rounded(px(10.))
                            .border_2()
                            .border_color(p.canvas)
                            .bg(p.signal)
                            .text_color(p.on_signal)
                            .text_size(px(10.5))
                            .font_weight(FontWeight::EXTRA_BOLD)
                            .flex()
                            .items_center()
                            .justify_center()
                            .child(SharedString::from(if requests > 99 {
                                "99+".to_string()
                            } else {
                                requests.to_string()
                            })),
                    )
                })
                .on_click(cx.listener(|this, _, _, cx| this.set_filter(Filter::Requests, cx))),
            )
            .child(
                rail_button(
                    "rail-archived",
                    IconName::Archive,
                    "Archivées",
                    self.filter == Filter::Archived,
                    p,
                )
                .on_click(cx.listener(|this, _, _, cx| this.set_filter(Filter::Archived, cx))),
            )
            .child(
                rail_button("rail-favorites", IconName::Star, "Favoris", false, p)
                    .on_click(cx.listener(|this, _, _, cx| this.open_favorites(cx))),
            )
            .child(
                rail_button("rail-scheduled", IconName::Clock, "Programmés", false, p)
                    .on_click(cx.listener(|this, _, _, cx| this.open_scheduled(cx))),
            )
            .child(div().flex_1())
            .child(
                rail_button(
                    "rail-theme",
                    if dark { IconName::Sun } else { IconName::Moon },
                    if dark { "Thème clair" } else { "Thème sombre" },
                    false,
                    p,
                )
                .on_click(cx.listener(move |this, _, window, cx| {
                    let next = if dark {
                        ThemePreference::Light
                    } else {
                        ThemePreference::Dark
                    };
                    this.set_theme(next, window, cx);
                })),
            )
            .child(
                rail_button(
                    "rail-settings",
                    IconName::SlidersHorizontal,
                    "Paramètres",
                    self.settings_open,
                    p,
                )
                .on_click(cx.listener(|this, _, _, cx| this.toggle_settings(cx))),
            )
            .child(
                div()
                    .relative()
                    .mt(px(6.))
                    .mb(px(4.))
                    .child(avatar(
                        &user_name,
                        self.avatar_source(user_avatar.as_deref()),
                        38.,
                        p,
                    ))
                    .child(
                        div()
                            .absolute()
                            .right(px(-1.))
                            .bottom(px(-1.))
                            .size(px(12.))
                            .rounded_full()
                            .border_2()
                            .border_color(p.canvas)
                            .bg(link_color(self.link, p)),
                    ),
            )
            .into_any_element()
    }

    fn render_search_results(
        &self,
        visible: &[usize],
        p: &Palette,
        cx: &mut Context<Self>,
    ) -> impl IntoElement {
        let view = cx.entity();
        let header = |label: &'static str| {
            div()
                .px_5()
                .pt_3()
                .pb_1()
                .text_size(px(12.))
                .font_weight(FontWeight::SEMIBOLD)
                .text_color(p.text_faint)
                .child(label)
        };
        let search = &self.thread_ops.search;
        let threads: Vec<AnyElement> = if search.query.has_filters() {
            Vec::new()
        } else {
            visible
                .iter()
                .take(6)
                .enumerate()
                .filter_map(|(row, index)| {
                    let thread = self.threads.get(*index)?;
                    Some(self.render_thread_row(row, thread, p, &view))
                })
                .collect()
        };
        let mut groups: Vec<(&str, Vec<&SearchHit>)> = Vec::new();
        for hit in &self.search_hits {
            match groups.iter_mut().find(|(id, _)| *id == hit.thread_id) {
                Some((_, hits)) => hits.push(hit),
                None => groups.push((hit.thread_id.as_str(), vec![hit])),
            }
        }
        let mut row = 0usize;
        let mut hit_rows: Vec<AnyElement> = Vec::new();
        for (group_index, (thread_id, hits)) in groups.into_iter().enumerate() {
            let thread = self.threads.iter().find(|t| t.id == thread_id);
            hit_rows.push(self.render_hit_group_header(
                group_index,
                thread_id,
                thread,
                hits.len(),
                p,
                cx,
            ));
            for hit in hits {
                hit_rows.push(self.render_hit_row(row, hit, p, cx));
                row += 1;
            }
        }
        let status: SharedString = if let Some(problem) = self.search_problem_text() {
            problem.into()
        } else if search.loading {
            "Recherche…".into()
        } else if let Some(notice) = self.search_notice_text() {
            notice.into()
        } else {
            "Aucun résultat".into()
        };
        let empty = threads.is_empty() && hit_rows.is_empty();

        v_flex()
            .id("search-results")
            .flex_1()
            .min_h_0()
            .overflow_y_scroll()
            .pb_2()
            .when(!threads.is_empty(), |el| {
                el.child(header("Discussions")).children(threads)
            })
            .when(!hit_rows.is_empty(), |el| {
                el.child(header("Messages")).children(hit_rows)
            })
            .when(empty, |el| {
                el.child(
                    div()
                        .py_8()
                        .px_6()
                        .text_center()
                        .text_size(px(13.))
                        .text_color(p.text_faint)
                        .child(status),
                )
            })
    }

    fn render_hit_group_header(
        &self,
        index: usize,
        thread_id: &str,
        thread: Option<&Thread>,
        count: usize,
        p: &Palette,
        cx: &mut Context<Self>,
    ) -> AnyElement {
        let hover = p.hover;
        let name = thread.map(display_name).unwrap_or_else(|| "Conversation".into());
        let id = thread_id.to_string();
        h_flex()
            .id(("search-group", index))
            .mx_2()
            .mt_1()
            .px_3()
            .py_1p5()
            .gap_2p5()
            .rounded(px(12.))
            .cursor_pointer()
            .hover(move |s| s.bg(hover))
            .child(thread_avatar(
                match thread {
                    Some(thread) => self.avatar_spec(thread),
                    None => AvatarSpec {
                        name: "",
                        own: None,
                        has_own: false,
                        members: Vec::new(),
                        online: false,
                    },
                },
                28.,
                p.panel,
                p,
            ))
            .child(
                div()
                    .flex_1()
                    .min_w_0()
                    .truncate()
                    .text_size(px(13.5))
                    .font_weight(FontWeight::BOLD)
                    .child(SharedString::from(name)),
            )
            .child(
                div()
                    .flex_none()
                    .text_size(px(11.5))
                    .text_color(p.text_faint)
                    .child(SharedString::from(match count {
                        1 => "1 résultat".to_string(),
                        n => format!("{n} résultats"),
                    })),
            )
            .on_click(cx.listener(move |this, _, window, cx| {
                this.select_thread(id.clone(), window, cx)
            }))
            .into_any_element()
    }

    fn render_hit_row(
        &self,
        row: usize,
        hit: &SearchHit,
        p: &Palette,
        cx: &mut Context<Self>,
    ) -> AnyElement {
        let hover = p.hover;
        let sender = if hit.message.is_outgoing {
            "Vous".to_string()
        } else {
            hit.message.sender_name.clone()
        };
        let body = if hit.message.text.trim().is_empty() {
            describe_attachments(&hit.message)
        } else {
            hit.message.text.split_whitespace().collect::<Vec<_>>().join(" ")
        };
        let (shown, ranges) = self.search_excerpt(&body);
        let highlight = HighlightStyle {
            color: Some(p.text),
            font_weight: Some(FontWeight::BOLD),
            background_color: Some(p.signal_soft),
            ..Default::default()
        };
        let styled = StyledText::new(SharedString::from(shown))
            .with_highlights(ranges.into_iter().map(|range| (range, highlight)));
        let hit_clone = hit.clone();
        v_flex()
            .id(("search-hit", row))
            .mx_2()
            .ml(px(48.))
            .px_3()
            .py_1p5()
            .gap_0p5()
            .rounded(px(12.))
            .cursor_pointer()
            .hover(move |s| s.bg(hover))
            .child(
                h_flex()
                    .gap_2()
                    .child(
                        div()
                            .flex_1()
                            .min_w_0()
                            .truncate()
                            .text_size(px(12.5))
                            .font_weight(FontWeight::SEMIBOLD)
                            .child(SharedString::from(sender)),
                    )
                    .child(
                        div()
                            .flex_none()
                            .text_size(px(11.5))
                            .text_color(p.text_faint)
                            .child(format_short(hit.message.timestamp_ms)),
                    ),
            )
            .child(
                div()
                    .text_size(px(12.5))
                    .line_height(px(17.))
                    .text_color(p.text_muted)
                    .line_clamp(2)
                    .child(styled),
            )
            .on_click(cx.listener(move |this, _, window, cx| {
                this.open_search_hit(hit_clone.clone(), window, cx);
            }))
            .into_any_element()
    }

    fn render_search_chips(&self, p: &Palette, cx: &mut Context<Self>) -> Option<AnyElement> {
        let chips = self.search_chips();
        if chips.is_empty() {
            return None;
        }
        let elements: Vec<AnyElement> = chips
            .into_iter()
            .enumerate()
            .map(|(index, chip)| {
                let (field, label, invalid, warning) =
                    (chip.field, chip.label, chip.invalid, chip.warning);
                h_flex()
                    .id(("search-chip", index))
                    .h(px(26.))
                    .max_w(px(290.))
                    .pl_2p5()
                    .pr_2()
                    .gap_1p5()
                    .rounded_full()
                    .text_size(px(12.))
                    .font_weight(FontWeight::SEMIBOLD)
                    .cursor_pointer()
                    .when(invalid, |el| el.bg(p.chip_bg).text_color(p.danger))
                    .when(!invalid && warning, |el| {
                        el.bg(p.signal_soft).text_color(p.signal_text)
                    })
                    .when(!invalid && !warning, |el| {
                        el.bg(p.accent_soft).text_color(p.accent_text)
                    })
                    .child(div().min_w_0().truncate().child(SharedString::from(label)))
                    .child(Icon::new(IconName::X).size(px(12.)))
                    .tooltip(|window, cx| Tooltip::new("Retirer ce filtre").build(window, cx))
                    .on_click(cx.listener(move |this, _, window, cx| {
                        this.remove_search_filter(field, window, cx)
                    }))
                    .into_any_element()
            })
            .collect();
        Some(
            div()
                .flex()
                .flex_wrap()
                .gap_1p5()
                .mx(px(14.))
                .pt_2()
                .children(elements)
                .into_any_element(),
        )
    }

    fn render_filter_tabs(&self, p: &Palette, cx: &mut Context<Self>) -> AnyElement {
        let text = p.text;
        let active_folder = self.thread_ops.folder.clone();
        let folder_tabs: Vec<AnyElement> = self
            .settings
            .folders
            .iter()
            .enumerate()
            .map(|(index, folder)| {
                let active = active_folder.as_deref() == Some(folder.id.as_str());
                let (open_id, menu_id) = (folder.id.clone(), folder.id.clone());
                tab_label(("folder-tab", index), active, p)
                    .max_w(px(140.))
                    .truncate()
                    .when(!active, |el| el.hover(move |style| style.text_color(text)))
                    .on_click(cx.listener(move |this, _, _, cx| this.open_folder(&open_id, cx)))
                    .on_mouse_down(
                        MouseButton::Right,
                        cx.listener(move |this, event: &MouseDownEvent, _, cx| {
                            cx.stop_propagation();
                            this.open_folder_menu(&menu_id, event.position, cx)
                        }),
                    )
                    .child(SharedString::from(folder.name.clone()))
                    .into_any_element()
            })
            .collect();
        h_flex()
            .id("filter-tabs")
            .px(px(20.))
            .pt(px(14.))
            .gap(px(18.))
            .border_b_1()
            .border_color(p.divider)
            .overflow_x_scroll()
            .child(self.filter_tab("Toutes", Filter::All, p, cx))
            .child(self.filter_tab("Non lues", Filter::Unread, p, cx))
            .child(self.filter_tab("Groupes", Filter::Groups, p, cx))
            .children(folder_tabs)
            .child(
                div()
                    .id("new-folder")
                    .flex_none()
                    .pb(px(8.))
                    .cursor_pointer()
                    .text_color(p.text_muted)
                    .hover(move |style| style.text_color(text))
                    .tooltip(|window, cx| Tooltip::new("Nouveau dossier").build(window, cx))
                    .child(Icon::new(IconName::Plus).size(px(16.)))
                    .on_click(cx.listener(|this, _, window, cx| {
                        this.open_new_folder(None, window, cx)
                    })),
            )
            .into_any_element()
    }

    fn filter_tab(
        &self,
        label: &'static str,
        filter: Filter,
        p: &Palette,
        cx: &mut Context<Self>,
    ) -> AnyElement {
        let active = self.filter == filter && self.active_folder().is_none();
        let text = p.text;
        tab_label(label, active, p)
            .when(!active, |el| el.hover(move |style| style.text_color(text)))
            .on_click(cx.listener(move |this, _, _, cx| this.select_filter_tab(filter, cx)))
            .child(label)
            .into_any_element()
    }

    fn render_history_banner(&self, p: &Palette, cx: &mut Context<Self>) -> impl IntoElement {
        v_flex()
            .mx(px(14.))
            .mt_2()
            .p_3()
            .gap_2()
            .rounded(px(14.))
            .bg(p.accent_soft)
            .child(
                h_flex()
                    .gap_2()
                    .child(Icon::new(IconName::Lock).size(px(14.)).text_color(p.accent_text))
                    .child(
                        div()
                            .text_size(px(13.))
                            .font_weight(FontWeight::SEMIBOLD)
                            .child("Historique chiffré non restauré"),
                    ),
            )
            .child(
                div()
                    .text_size(px(12.))
                    .line_height(px(17.))
                    .text_color(p.text_muted)
                    .child("Vos conversations privées n'affichent pas encore leurs messages."),
            )
            .child(
                Button::new("open-pin")
                    .primary()
                    .small()
                    .label("Restaurer avec mon PIN")
                    .on_click(cx.listener(|this, _, window, cx| this.open_pin(window, cx))),
            )
    }

    fn render_thread_row(
        &self,
        row: usize,
        thread: &Thread,
        p: &Palette,
        view: &Entity<Self>,
    ) -> AnyElement {
        let selected = self.selected.as_deref() == Some(thread.id.as_str());
        let local_unread = self.is_locally_unread(thread);
        let muted = self.thread_is_muted(thread);
        let unread = thread.unread_count > 0 || local_unread;
        let typing = self.typing.get(&thread.id).copied().unwrap_or(false);
        let thread_id = thread.id.clone();
        let view = view.clone();
        let hover = p.hover;

        let snippet = if typing {
            h_flex()
                .gap_1p5()
                .text_color(p.accent_text)
                .child(typing_dots(("typing-row", row), p.accent_text))
                .child("écrit")
                .into_any_element()
        } else {
            div()
                .truncate()
                .text_color(if unread { p.text } else { p.text_muted })
                .when(unread, |el| el.font_weight(FontWeight::SEMIBOLD))
                .child(SharedString::from(
                    thread
                        .snippet
                        .split_whitespace()
                        .collect::<Vec<_>>()
                        .join(" "),
                ))
                .into_any_element()
        };

        div()
            .w_full()
            .px_2()
            .pb(px(ROW_GAP))
            .child(
                h_flex()
                    .id(("thread", row))
                    .w_full()
                    .h(px(ROW_HEIGHT))
                    .px_3()
                    .gap_3()
                    .rounded(px(12.))
                    .cursor_pointer()
                    .when(selected, |el| el.bg(p.accent_soft))
                    .when(!selected, |el| el.hover(move |style| style.bg(hover)))
                    .on_hover({
                        let view = view.clone();
                        let thread_id = thread_id.clone();
                        move |hovered, _, cx| {
                            if *hovered {
                                view.update(cx, |this, cx| this.prefetch(&thread_id, cx));
                            }
                        }
                    })
                    .on_mouse_down(MouseButton::Right, {
                        let view = view.clone();
                        let thread_id = thread_id.clone();
                        move |event: &MouseDownEvent, _, cx| {
                            cx.stop_propagation();
                            let position = event.position;
                            view.update(cx, |this, cx| {
                                this.open_thread_context_menu(&thread_id, position, cx)
                            });
                        }
                    })
                    .on_click(move |_, window, cx| {
                        let thread_id = thread_id.clone();
                        view.update(cx, |this, cx| this.select_thread(thread_id, window, cx));
                    })
                    .child(thread_avatar(
                        self.avatar_spec(thread),
                        48.,
                        if selected { p.accent_soft } else { p.panel },
                        p,
                    ))
                    .child(
                        v_flex()
                            .flex_1()
                            .min_w_0()
                            .gap(px(2.))
                            .child(
                                h_flex()
                                    .gap_2()
                                    .child(
                                        div()
                                            .flex_1()
                                            .min_w_0()
                                            .truncate()
                                            .text_size(px(15.))
                                            .font_weight(if unread {
                                                FontWeight::BOLD
                                            } else {
                                                FontWeight::SEMIBOLD
                                            })
                                            .child(SharedString::from(display_name(thread))),
                                    )
                                    .child(
                                        div()
                                            .flex_none()
                                            .text_size(px(12.))
                                            .text_color(if unread {
                                                p.accent_text
                                            } else {
                                                p.text_muted
                                            })
                                            .when(unread, |el| el.font_weight(FontWeight::BOLD))
                                            .child(format_short(thread.last_activity_ms)),
                                    ),
                            )
                            .child(
                                h_flex()
                                    .gap_2()
                                    .text_size(px(13.5))
                                    .child(div().flex_1().min_w_0().child(snippet))
                                    .when(muted, |el| {
                                        el.child(
                                            Icon::new(IconName::BellOff)
                                                .size(px(15.))
                                                .text_color(p.text_faint),
                                        )
                                    })
                                    .when(thread.unread_count > 0, |el| {
                                        el.child(unread_pill(thread.unread_count, muted, p))
                                    })
                                    .when(thread.unread_count == 0 && local_unread, |el| {
                                        el.child(
                                            div()
                                                .flex_none()
                                                .size(px(11.))
                                                .rounded_full()
                                                .bg(if muted { p.text_faint } else { p.accent }),
                                        )
                                    }),
                            ),
                    ),
            )
            .into_any_element()
    }
}

fn tab_label(id: impl Into<ElementId>, active: bool, p: &Palette) -> Stateful<Div> {
    div()
        .id(id)
        .flex_none()
        .pb(px(8.))
        .mb(px(-1.))
        .border_b_2()
        .border_color(if active { p.accent_text } else { transparent_black() })
        .text_size(px(13.5))
        .font_weight(FontWeight::SEMIBOLD)
        .text_color(if active { p.text } else { p.text_muted })
        .cursor_pointer()
}

fn rail_button(
    id: &'static str,
    icon: IconName,
    label: &'static str,
    active: bool,
    p: &Palette,
) -> Stateful<Div> {
    let hover = p.divider;
    div()
        .id(id)
        .size(px(44.))
        .flex_none()
        .rounded(px(14.))
        .flex()
        .items_center()
        .justify_center()
        .cursor_pointer()
        .when(active, |el| el.bg(p.accent_soft))
        .when(!active, |el| el.hover(move |style| style.bg(hover)))
        .tooltip(move |window, cx| Tooltip::new(label).build(window, cx))
        .child(Icon::new(icon).size(px(19.)).text_color(if active {
            p.accent_text
        } else {
            p.text_muted
        }))
}

pub(super) fn display_name(thread: &Thread) -> String {
    if thread.name.trim().is_empty() {
        "Conversation".into()
    } else {
        thread.name.clone()
    }
}
