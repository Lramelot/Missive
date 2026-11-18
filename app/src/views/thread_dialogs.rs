use gpui_kit::assets::IconName;
use gpui_kit::component::{
    Disableable as _, Icon, Sizable as _,
    button::{Button, ButtonVariants as _},
    h_flex,
    input::Input,
    v_flex,
};
use gpui_kit::{prelude::FluentBuilder as _, *};

use super::widgets::{icon_button, person_avatar};
use crate::{app::MissiveApp, model::UserResult, style::Palette};

fn scrim() -> Div {
    div()
        .absolute()
        .inset_0()
        .flex()
        .items_center()
        .justify_center()
        .bg(hsla(0., 0., 0., 0.45))
        .occlude()
}

fn menu_item(
    id: impl Into<ElementId>,
    icon: IconName,
    label: impl Into<SharedString>,
    danger: bool,
    p: &Palette,
) -> Stateful<Div> {
    let hover = p.hover;
    h_flex()
        .id(id)
        .gap_3()
        .px_3()
        .h(px(36.))
        .rounded(px(10.))
        .cursor_pointer()
        .hover(move |style| style.bg(hover))
        .text_size(px(13.5))
        .text_color(if danger { p.danger } else { p.text })
        .child(Icon::new(icon).size(px(16.)).text_color(if danger {
            p.danger
        } else {
            p.text_muted
        }))
        .child(div().flex_1().child(label.into()))
}

fn menu_divider(p: &Palette) -> Div {
    div().my_1().mx_2().h(px(1.)).bg(p.divider)
}

fn menu_title(text: impl Into<SharedString>, p: &Palette) -> Div {
    div()
        .px_3()
        .pt_1p5()
        .pb_1()
        .text_size(px(12.))
        .font_weight(FontWeight::SEMIBOLD)
        .text_color(p.text_faint)
        .truncate()
        .child(text.into())
}

fn dialog_card(id: &'static str, width: f32, p: &Palette) -> Stateful<Div> {
    v_flex()
        .id(id)
        .w(px(width))
        .max_w(relative(0.92))
        .rounded(px(18.))
        .border_1()
        .border_color(p.divider)
        .bg(p.panel)
        .shadow_lg()
        .on_mouse_down(MouseButton::Left, |_, _, cx| cx.stop_propagation())
}

impl MissiveApp {
    pub(super) fn render_thread_dialogs(
        &self,
        p: &Palette,
        cx: &mut Context<Self>,
    ) -> Option<AnyElement> {
        let mut layers: Vec<AnyElement> = Vec::new();
        if self.thread_ops.people.is_some() {
            layers.push(self.render_people_dialog(p, cx));
        }
        if self.thread_ops.rename.is_some() {
            layers.push(self.render_rename_dialog(p, cx));
        }
        if self.thread_ops.folder_dialog.is_some() {
            layers.push(self.render_folder_dialog(p, cx));
        }
        if self.exports.dialog.is_some() {
            layers.push(self.render_export_dialog(p, cx));
        }
        if self.thread_ops.confirm.is_some() {
            layers.push(self.render_confirm_dialog(p, cx));
        }
        if self.thread_ops.search.panel.is_some() {
            layers.push(self.render_search_panel(p, cx));
        }
        if self.thread_ops.menu.is_some() {
            layers.push(self.render_thread_menu(p, cx));
        }
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

    fn render_thread_menu(&self, p: &Palette, cx: &mut Context<Self>) -> AnyElement {
        let Some(menu) = &self.thread_ops.menu else {
            return div().into_any_element();
        };
        let thread_id = menu.kind.thread_id().to_string();
        let thread = self.threads.iter().find(|t| t.id == thread_id);
        let items: Vec<AnyElement> = match (menu.kind.folder_tab_id(), thread) {
            (Some(folder_id), _) => self.folder_tab_menu_items(folder_id, p, cx),
            (None, None) => Vec::new(),
            (None, Some(_)) if menu.kind.is_folders() => {
                self.folder_menu_items(&thread_id, p, cx)
            }
            (None, Some(thread)) => {
                if let Some(member_id) = menu.kind.member_id() {
                    self.member_menu_items(&thread_id, member_id, p, cx)
                } else if menu.kind.is_mute() {
                    self.mute_menu_items(&thread_id, self.thread_is_muted(thread), p, cx)
                } else {
                    self.thread_menu_items(&thread_id, p, cx)
                }
            }
        };
        let close = cx.listener(|this, _: &MouseDownEvent, _, cx| this.close_thread_menu(cx));
        let close_right = cx.listener(|this, _: &MouseDownEvent, _, cx| this.close_thread_menu(cx));
        div()
            .absolute()
            .inset_0()
            .occlude()
            .on_mouse_down(MouseButton::Left, close)
            .on_mouse_down(MouseButton::Right, close_right)
            .child(
                anchored()
                    .position(menu.position)
                    .snap_to_window_with_margin(px(8.))
                    .child(
                        v_flex()
                            .id("thread-menu")
                            .min_w(px(250.))
                            .p_1p5()
                            .rounded(px(14.))
                            .bg(p.panel)
                            .border_1()
                            .border_color(p.divider)
                            .shadow_lg()
                            .on_mouse_down(MouseButton::Left, |_, _, cx| cx.stop_propagation())
                            .on_mouse_down(MouseButton::Right, |_, _, cx| cx.stop_propagation())
                            .children(items),
                    ),
            )
            .into_any_element()
    }

    fn thread_menu_items(
        &self,
        thread_id: &str,
        p: &Palette,
        cx: &mut Context<Self>,
    ) -> Vec<AnyElement> {
        let Some(thread) = self.threads.iter().find(|t| t.id == thread_id) else {
            return Vec::new();
        };
        let unread = self.has_unread(thread);
        let request = self.is_request_thread(thread);
        let archived = self.is_locally_archived(thread);
        let can_archive = self.is_inbox_thread(thread);
        let muted = self.thread_is_muted(thread);
        let mut items: Vec<AnyElement> = Vec::new();

        if !request {
            let id = thread_id.to_string();
            items.push(
                menu_item(
                    "menu-unread",
                    if unread {
                        IconName::MailOpen
                    } else {
                        IconName::Mail
                    },
                    if unread {
                        t!("thread_dialogs.menu.mark_read")
                    } else {
                        t!("thread_dialogs.menu.mark_unread")
                    },
                    false,
                    p,
                )
                .on_click(
                    cx.listener(move |this, _, _, cx| this.set_local_unread(&id, !unread, cx)),
                )
                .into_any_element(),
            );
        }
        if request {
            let id = thread_id.to_string();
            items.push(
                menu_item(
                    "menu-accept",
                    IconName::Check,
                    t!("thread_dialogs.menu.accept_request"),
                    false,
                    p,
                )
                .on_click(cx.listener(move |this, _, _, cx| this.accept_request(&id, cx)))
                .into_any_element(),
            );
        }
        if muted {
            let id = thread_id.to_string();
            items.push(
                menu_item(
                    "menu-unmute",
                    IconName::Bell,
                    t!("thread_dialogs.menu.unmute"),
                    false,
                    p,
                )
                .on_click(cx.listener(move |this, _, _, cx| this.unmute_thread(&id, cx)))
                .into_any_element(),
            );
        } else {
            items.push(
                menu_item(
                    "menu-mute",
                    IconName::BellOff,
                    t!("thread_dialogs.menu.mute"),
                    false,
                    p,
                )
                .child(
                    Icon::new(IconName::ChevronRight)
                        .size(px(15.))
                        .text_color(p.text_faint),
                )
                .on_click(cx.listener(|this, _, _, cx| this.show_mute_submenu(cx)))
                .into_any_element(),
            );
        }
        if archived {
            let id = thread_id.to_string();
            items.push(
                menu_item(
                    "menu-unarchive",
                    IconName::ArchiveRestore,
                    t!("thread_dialogs.menu.unarchive"),
                    false,
                    p,
                )
                .on_click(
                    cx.listener(move |this, _, _, cx| this.set_local_archived(&id, false, cx)),
                )
                .into_any_element(),
            );
        } else if can_archive {
            let id = thread_id.to_string();
            items.push(
                menu_item(
                    "menu-archive",
                    IconName::Archive,
                    t!("thread_dialogs.menu.archive"),
                    false,
                    p,
                )
                .on_click(cx.listener(move |this, _, _, cx| this.set_local_archived(&id, true, cx)))
                .into_any_element(),
            );
        }
        if !request {
            items.push(
                menu_item(
                    "menu-folders",
                    IconName::FolderPlus,
                    t!("thread_dialogs.menu.add_to_folder"),
                    false,
                    p,
                )
                .child(
                    Icon::new(IconName::ChevronRight)
                        .size(px(15.))
                        .text_color(p.text_faint),
                )
                .on_click(cx.listener(|this, _, _, cx| this.show_folder_submenu(cx)))
                .into_any_element(),
            );
            if let Some(folder) = self
                .active_folder()
                .filter(|folder| folder.thread_ids.iter().any(|id| id == thread_id))
            {
                let (id, folder_id) = (thread_id.to_string(), folder.id.clone());
                items.push(
                    menu_item(
                        "menu-folder-remove",
                        IconName::FolderMinus,
                        t!("thread_dialogs.menu.remove_from_folder", folder = folder.name),
                        false,
                        p,
                    )
                    .on_click(cx.listener(move |this, _, _, cx| {
                        this.set_thread_in_folder(&id, &folder_id, false, cx)
                    }))
                    .into_any_element(),
                );
            }
            let id = thread_id.to_string();
            let priority = self.is_priority_thread(thread_id);
            items.push(
                menu_item(
                    "menu-priority",
                    IconName::Flag,
                    t!("thread_dialogs.menu.priority"),
                    false,
                    p,
                )
                .when(priority, |el| {
                    el.child(
                        Icon::new(IconName::Check)
                            .size(px(15.))
                            .text_color(p.accent_text),
                    )
                })
                .on_click(cx.listener(move |this, _, _, cx| this.toggle_priority(&id, cx)))
                .into_any_element(),
            );
        }
        items.push(menu_divider(p).into_any_element());
        let id = thread_id.to_string();
        items.push(
            menu_item(
                "menu-delete",
                IconName::Trash,
                t!("thread_dialogs.menu.delete"),
                true,
                p,
            )
            .on_click(cx.listener(move |this, _, _, cx| this.confirm_delete_thread(&id, cx)))
            .into_any_element(),
        );
        items
    }

    fn mute_menu_items(
        &self,
        thread_id: &str,
        muted: bool,
        p: &Palette,
        cx: &mut Context<Self>,
    ) -> Vec<AnyElement> {
        let mut items: Vec<AnyElement> =
            vec![menu_title(t!("thread_dialogs.menu.mute"), p).into_any_element()];
        let choices: [(&'static str, SharedString, Option<i64>); 4] = [
            ("mute-1h", ts!("thread_dialogs.mute.one_hour"), Some(1)),
            ("mute-8h", ts!("thread_dialogs.mute.eight_hours"), Some(8)),
            ("mute-24h", ts!("thread_dialogs.mute.one_day"), Some(24)),
            ("mute-forever", ts!("thread_dialogs.mute.forever"), None),
        ];
        for (id, label, hours) in choices {
            let thread_id = thread_id.to_string();
            items.push(
                menu_item(
                    id,
                    if hours.is_some() {
                        IconName::Clock
                    } else {
                        IconName::BellOff
                    },
                    label,
                    false,
                    p,
                )
                .on_click(
                    cx.listener(move |this, _, _, cx| this.mute_thread_for(&thread_id, hours, cx)),
                )
                .into_any_element(),
            );
        }
        if muted {
            let thread_id = thread_id.to_string();
            items.push(menu_divider(p).into_any_element());
            items.push(
                menu_item(
                    "mute-off",
                    IconName::Bell,
                    t!("thread_dialogs.menu.unmute"),
                    false,
                    p,
                )
                .on_click(cx.listener(move |this, _, _, cx| this.unmute_thread(&thread_id, cx)))
                .into_any_element(),
            );
        }
        items
    }

    fn member_menu_items(
        &self,
        thread_id: &str,
        member_id: &str,
        p: &Palette,
        cx: &mut Context<Self>,
    ) -> Vec<AnyElement> {
        let Some(thread) = self.threads.iter().find(|t| t.id == thread_id) else {
            return Vec::new();
        };
        let Some(member) = thread.participants.iter().find(|m| m.user_id == member_id) else {
            return Vec::new();
        };
        let mut items: Vec<AnyElement> =
            vec![menu_title(member.name.clone(), p).into_any_element()];
        if thread.is_admin {
            let admin = member.is_admin;
            let (thread_id, user_id) = (thread_id.to_string(), member_id.to_string());
            items.push(
                menu_item(
                    "member-admin",
                    if admin {
                        IconName::ShieldOff
                    } else {
                        IconName::ShieldCheck
                    },
                    if admin {
                        t!("thread_dialogs.member.remove_admin")
                    } else {
                        t!("thread_dialogs.member.make_admin")
                    },
                    false,
                    p,
                )
                .on_click(cx.listener(move |this, _, _, cx| {
                    this.set_admin(&thread_id, &user_id, !admin, cx)
                }))
                .into_any_element(),
            );
        }
        let (thread_id, user_id) = (thread_id.to_string(), member_id.to_string());
        items.push(
            menu_item(
                "member-remove",
                IconName::UserMinus,
                t!("thread_dialogs.member.remove"),
                true,
                p,
            )
            .on_click(cx.listener(move |this, _, _, cx| {
                this.confirm_remove_member(&thread_id, &user_id, cx)
            }))
            .into_any_element(),
        );
        items
    }

    fn render_confirm_dialog(&self, p: &Palette, cx: &mut Context<Self>) -> AnyElement {
        let Some(confirm) = &self.thread_ops.confirm else {
            return div().into_any_element();
        };
        let danger = confirm.danger;
        scrim()
            .on_mouse_down(
                MouseButton::Left,
                cx.listener(|this, _: &MouseDownEvent, _, cx| this.cancel_confirm(cx)),
            )
            .child(
                dialog_card("confirm-dialog", 420., p)
                    .gap_3()
                    .p_6()
                    .child(
                        div()
                            .text_size(px(18.))
                            .font_weight(FontWeight::BOLD)
                            .child(confirm.title.clone()),
                    )
                    .child(
                        div()
                            .text_size(px(13.5))
                            .line_height(px(20.))
                            .text_color(p.text_muted)
                            .child(confirm.text.clone()),
                    )
                    .child(
                        h_flex()
                            .pt_2()
                            .justify_end()
                            .gap_2()
                            .child(
                                Button::new("confirm-cancel")
                                    .ghost()
                                    .label(t!("thread_dialogs.button.cancel"))
                                    .on_click(
                                        cx.listener(|this, _, _, cx| this.cancel_confirm(cx)),
                                    ),
                            )
                            .child(
                                Button::new("confirm-ok")
                                    .when(danger, |button| button.danger())
                                    .when(!danger, |button| button.primary())
                                    .label(confirm.button.clone())
                                    .on_click(cx.listener(|this, _, _, cx| this.run_confirm(cx))),
                            ),
                    ),
            )
            .into_any_element()
    }

    fn render_rename_dialog(&self, p: &Palette, cx: &mut Context<Self>) -> AnyElement {
        let Some(rename) = &self.thread_ops.rename else {
            return div().into_any_element();
        };
        scrim()
            .on_mouse_down(
                MouseButton::Left,
                cx.listener(|this, _: &MouseDownEvent, _, cx| this.close_rename(cx)),
            )
            .child(
                dialog_card("rename-dialog", 420., p)
                    .gap_4()
                    .p_6()
                    .child(
                        div()
                            .text_size(px(18.))
                            .font_weight(FontWeight::BOLD)
                            .child(ts!("thread_dialogs.rename.title")),
                    )
                    .child(
                        div()
                            .text_size(px(13.))
                            .text_color(p.text_muted)
                            .child(ts!("thread_dialogs.rename.hint")),
                    )
                    .child(Input::new(&rename.input).large())
                    .child(
                        h_flex()
                            .justify_end()
                            .gap_2()
                            .child(
                                Button::new("rename-cancel")
                                    .ghost()
                                    .label(t!("thread_dialogs.button.cancel"))
                                    .on_click(cx.listener(|this, _, _, cx| this.close_rename(cx))),
                            )
                            .child(
                                Button::new("rename-save")
                                    .primary()
                                    .label(t!("thread_dialogs.button.save"))
                                    .on_click(cx.listener(|this, _, _, cx| this.submit_rename(cx))),
                            ),
                    ),
            )
            .into_any_element()
    }

    fn render_people_dialog(&self, p: &Palette, cx: &mut Context<Self>) -> AnyElement {
        let Some(dialog) = &self.thread_ops.people else {
            return div().into_any_element();
        };
        let direct = dialog.is_direct();
        let add = dialog.is_add();
        let group = !direct && !add;
        let candidates = self.people_candidates();
        let hover = p.hover;

        let mode_chip = |id: &'static str, label: SharedString, active: bool, to_group: bool| {
            div()
                .id(id)
                .h(px(30.))
                .px_3()
                .flex()
                .items_center()
                .rounded_full()
                .text_size(px(13.))
                .cursor_pointer()
                .font_weight(FontWeight::SEMIBOLD)
                .when(active, |el| el.bg(p.accent_soft).text_color(p.accent_text))
                .when(!active, |el| {
                    el.bg(p.chip_bg)
                        .text_color(p.text_muted)
                        .hover(move |style| style.bg(hover))
                })
                .child(label)
                .on_click(cx.listener(move |this, _, _, cx| this.set_people_group(to_group, cx)))
        };
        let modes = h_flex()
            .px_5()
            .pb_3()
            .gap_1p5()
            .child(mode_chip(
                "people-direct",
                ts!("thread_dialogs.people.mode_direct"),
                direct,
                false,
            ))
            .child(mode_chip(
                "people-group",
                ts!("thread_dialogs.people.mode_group"),
                group,
                true,
            ));

        let chips: Vec<AnyElement> =
            dialog
                .selected
                .iter()
                .enumerate()
                .map(|(index, user)| {
                    let user_clone = user.clone();
                    h_flex()
                        .id(("people-chip", index))
                        .h(px(28.))
                        .pl_1()
                        .pr_2()
                        .gap_1p5()
                        .rounded_full()
                        .bg(p.accent_soft)
                        .text_size(px(12.5))
                        .font_weight(FontWeight::SEMIBOLD)
                        .text_color(p.accent_text)
                        .cursor_pointer()
                        .child(person_avatar(
                            &user.name,
                            self.avatar_source(user.avatar_url.as_deref()),
                            20.,
                            false,
                            p.panel,
                            p,
                        ))
                        .child(SharedString::from(user.name.clone()))
                        .child(Icon::new(IconName::X).size(px(12.)))
                        .on_click(cx.listener(move |this, _, _, cx| {
                            this.toggle_person(user_clone.clone(), cx)
                        }))
                        .into_any_element()
                })
                .collect();

        let rows: Vec<AnyElement> = candidates
            .iter()
            .enumerate()
            .map(|(_, user)| self.person_row(user, direct, dialog.is_selected(&user.id), p, cx))
            .collect();

        let status: Option<SharedString> = if dialog.searching && rows.is_empty() {
            Some(ts!("thread_dialogs.people.searching"))
        } else if rows.is_empty() && !dialog.query.is_empty() {
            Some(ts!("thread_dialogs.people.no_results"))
        } else if rows.is_empty() {
            Some(ts!("thread_dialogs.people.prompt"))
        } else {
            None
        };
        let section = if dialog.query.is_empty() {
            ts!("thread_dialogs.people.suggestions")
        } else {
            ts!("thread_dialogs.people.results")
        };
        let count = dialog.selected.len();
        let can_submit = if add { count >= 1 } else { count >= 2 };
        let footer_label = match count {
            0 => ts!("thread_dialogs.people.selected_none"),
            n => SharedString::from(tn!("thread_dialogs.people.selected", n)),
        };

        scrim()
            .items_start()
            .pt(px(90.))
            .on_mouse_down(
                MouseButton::Left,
                cx.listener(|this, _: &MouseDownEvent, _, cx| this.close_people(cx)),
            )
            .child(
                dialog_card("people-dialog", 480., p)
                    .max_h(relative(0.8))
                    .overflow_hidden()
                    .child(
                        h_flex()
                            .flex_none()
                            .px_5()
                            .pt_5()
                            .pb_3()
                            .justify_between()
                            .child(
                                div()
                                    .text_size(px(19.))
                                    .font_weight(FontWeight::EXTRA_BOLD)
                                    .child(if add {
                                        ts!("thread_dialogs.people.add_title")
                                    } else {
                                        ts!("thread_dialogs.people.new_title")
                                    }),
                            )
                            .child(
                                icon_button("close-people", IconName::X, false, p)
                                    .on_click(cx.listener(|this, _, _, cx| this.close_people(cx))),
                            ),
                    )
                    .when(!add, |el| el.child(modes))
                    .child(
                        h_flex()
                            .flex_none()
                            .mx_5()
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
                                div().flex_1().child(
                                    Input::new(&dialog.input).appearance(false).cleanable(true),
                                ),
                            )
                            .when(dialog.searching, |el| {
                                el.child(
                                    Icon::new(IconName::LoaderCircle)
                                        .size(px(15.))
                                        .text_color(p.text_faint),
                                )
                            }),
                    )
                    .when(!chips.is_empty(), |el| {
                        el.child(
                            div()
                                .flex_none()
                                .flex()
                                .flex_wrap()
                                .gap_1p5()
                                .px_5()
                                .pt_3()
                                .children(chips),
                        )
                    })
                    .when(group, |el| {
                        el.child(
                            div()
                                .flex_none()
                                .mx_5()
                                .mt_3()
                                .child(Input::new(&dialog.name_input)),
                        )
                    })
                    .child(
                        div()
                            .flex_none()
                            .px_5()
                            .pt_3()
                            .pb_1()
                            .text_size(px(12.))
                            .font_weight(FontWeight::SEMIBOLD)
                            .text_color(p.text_faint)
                            .child(section),
                    )
                    .child(
                        v_flex()
                            .id("people-results")
                            .flex_1()
                            .min_h(px(120.))
                            .overflow_y_scroll()
                            .px_2()
                            .pb_3()
                            .when(dialog.busy, |el| el.opacity(0.55))
                            .children(rows)
                            .when_some(status, |el, status| {
                                el.child(
                                    div()
                                        .py_6()
                                        .text_center()
                                        .text_size(px(13.))
                                        .text_color(p.text_faint)
                                        .child(status),
                                )
                            }),
                    )
                    .when(!direct, |el| {
                        el.child(
                            h_flex()
                                .flex_none()
                                .px_5()
                                .py_3()
                                .gap_3()
                                .border_t_1()
                                .border_color(p.divider)
                                .child(
                                    div()
                                        .flex_1()
                                        .text_size(px(12.5))
                                        .text_color(p.text_muted)
                                        .child(footer_label),
                                )
                                .child(
                                    Button::new("people-submit")
                                        .primary()
                                        .small()
                                        .label(if add {
                                            t!("thread_dialogs.people.add")
                                        } else {
                                            t!("thread_dialogs.people.create_group")
                                        })
                                        .loading(dialog.busy)
                                        .disabled(!can_submit || dialog.busy)
                                        .on_click(cx.listener(|this, _, window, cx| {
                                            this.submit_people(window, cx)
                                        })),
                                ),
                        )
                    }),
            )
            .into_any_element()
    }

    fn person_row(
        &self,
        user: &UserResult,
        direct: bool,
        selected: bool,
        p: &Palette,
        cx: &mut Context<Self>,
    ) -> AnyElement {
        let hover = p.hover;
        let hint: Option<SharedString> = if user.is_friend {
            Some(ts!("thread_dialogs.people.friend"))
        } else if user.thread_id.is_some() {
            Some(ts!("thread_dialogs.people.existing"))
        } else {
            None
        };
        let picked = user.id.clone();
        h_flex()
            .id(SharedString::from(format!("person-{}", user.id)))
            .mx_1()
            .px_3()
            .h(px(56.))
            .gap_3()
            .rounded(px(12.))
            .cursor_pointer()
            .hover(move |style| style.bg(hover))
            .child(person_avatar(
                &user.name,
                self.avatar_source(user.avatar_url.as_deref()),
                38.,
                false,
                p.panel,
                p,
            ))
            .child(
                v_flex()
                    .flex_1()
                    .min_w_0()
                    .child(
                        div()
                            .truncate()
                            .text_size(px(14.))
                            .font_weight(FontWeight::SEMIBOLD)
                            .child(SharedString::from(if user.name.is_empty() {
                                t!("thread_dialogs.people.unknown_user").to_string()
                            } else {
                                user.name.clone()
                            })),
                    )
                    .when_some(hint, |el, hint| {
                        el.child(
                            div()
                                .text_size(px(12.))
                                .text_color(p.text_faint)
                                .child(hint),
                        )
                    }),
            )
            .when(!direct, |el| {
                el.child(
                    div()
                        .size(px(22.))
                        .flex_none()
                        .rounded_full()
                        .border_2()
                        .border_color(if selected { p.accent } else { p.border_strong })
                        .when(selected, |el| el.bg(p.accent))
                        .flex()
                        .items_center()
                        .justify_center()
                        .when(selected, |el| {
                            el.child(
                                Icon::new(IconName::Check)
                                    .size(px(13.))
                                    .text_color(p.on_accent),
                            )
                        }),
                )
            })
            .on_click(cx.listener(move |this, _, window, cx| this.pick_person(&picked, window, cx)))
            .into_any_element()
    }

    fn folder_menu_items(
        &self,
        thread_id: &str,
        p: &Palette,
        cx: &mut Context<Self>,
    ) -> Vec<AnyElement> {
        let mut items: Vec<AnyElement> =
            vec![menu_title(t!("thread_dialogs.menu.add_to_folder"), p).into_any_element()];
        for (index, folder) in self.settings.folders.iter().enumerate() {
            let inside = folder.thread_ids.iter().any(|id| id == thread_id);
            let (id, folder_id) = (thread_id.to_string(), folder.id.clone());
            items.push(
                menu_item(
                    ("menu-folder", index),
                    IconName::Folder,
                    folder.name.clone(),
                    false,
                    p,
                )
                .when(inside, |el| {
                    el.child(
                        Icon::new(IconName::Check)
                            .size(px(15.))
                            .text_color(p.accent_text),
                    )
                })
                .on_click(cx.listener(move |this, _, _, cx| {
                    this.set_thread_in_folder(&id, &folder_id, !inside, cx)
                }))
                .into_any_element(),
            );
        }
        if !self.settings.folders.is_empty() {
            items.push(menu_divider(p).into_any_element());
        }
        let id = thread_id.to_string();
        items.push(
            menu_item(
                "menu-folder-new",
                IconName::FolderPlus,
                t!("thread_dialogs.folder.new_item"),
                false,
                p,
            )
            .on_click(cx.listener(move |this, _, window, cx| {
                this.open_new_folder(Some(id.clone()), window, cx)
            }))
            .into_any_element(),
        );
        items
    }

    fn folder_tab_menu_items(
        &self,
        folder_id: &str,
        p: &Palette,
        cx: &mut Context<Self>,
    ) -> Vec<AnyElement> {
        let Some(folder) = self.settings.folders.iter().find(|f| f.id == folder_id) else {
            return Vec::new();
        };
        let (rename_id, delete_id) = (folder.id.clone(), folder.id.clone());
        vec![
            menu_title(folder.name.clone(), p).into_any_element(),
            menu_item(
                "folder-rename",
                IconName::Pencil,
                t!("thread_dialogs.folder.rename_item"),
                false,
                p,
            )
                .on_click(cx.listener(move |this, _, window, cx| {
                    this.open_rename_folder(&rename_id, window, cx)
                }))
                .into_any_element(),
            menu_item(
                "folder-delete",
                IconName::Trash,
                t!("thread_dialogs.folder.delete_item"),
                true,
                p,
            )
            .on_click(cx.listener(move |this, _, _, cx| {
                this.confirm_delete_folder(&delete_id, cx)
            }))
            .into_any_element(),
        ]
    }

    fn render_folder_dialog(&self, p: &Palette, cx: &mut Context<Self>) -> AnyElement {
        let Some(dialog) = &self.thread_ops.folder_dialog else {
            return div().into_any_element();
        };
        let rename = dialog.is_rename();
        let (title, text, button) = if rename {
            (
                ts!("thread_dialogs.folder.rename_title"),
                ts!("thread_dialogs.folder.rename_hint"),
                ts!("thread_dialogs.button.save"),
            )
        } else if dialog.adds_thread() {
            (
                ts!("thread_dialogs.folder.new_title"),
                ts!("thread_dialogs.folder.new_with_thread_hint"),
                ts!("thread_dialogs.button.create"),
            )
        } else {
            (
                ts!("thread_dialogs.folder.new_title"),
                ts!("thread_dialogs.folder.new_hint"),
                ts!("thread_dialogs.button.create"),
            )
        };
        scrim()
            .on_mouse_down(
                MouseButton::Left,
                cx.listener(|this, _: &MouseDownEvent, _, cx| this.close_folder_dialog(cx)),
            )
            .child(
                dialog_card("folder-dialog", 420., p)
                    .gap_4()
                    .p_6()
                    .child(
                        div()
                            .text_size(px(18.))
                            .font_weight(FontWeight::BOLD)
                            .child(title),
                    )
                    .child(
                        div()
                            .text_size(px(13.))
                            .line_height(px(19.))
                            .text_color(p.text_muted)
                            .child(text),
                    )
                    .child(Input::new(&dialog.input).large())
                    .child(
                        h_flex()
                            .justify_end()
                            .gap_2()
                            .child(
                                Button::new("folder-cancel")
                                    .ghost()
                                    .label(t!("thread_dialogs.button.cancel"))
                                    .on_click(
                                        cx.listener(|this, _, _, cx| this.close_folder_dialog(cx)),
                                    ),
                            )
                            .child(
                                Button::new("folder-save")
                                    .primary()
                                    .label(button)
                                    .on_click(
                                        cx.listener(|this, _, _, cx| this.submit_folder_dialog(cx)),
                                    ),
                            ),
                    ),
            )
            .into_any_element()
    }

    fn render_export_dialog(&self, p: &Palette, cx: &mut Context<Self>) -> AnyElement {
        let Some(dialog) = &self.exports.dialog else {
            return div().into_any_element();
        };
        let thread_name = self
            .threads
            .iter()
            .find(|thread| thread.id == dialog.thread_id)
            .map(super::sidebar::display_name)
            .unwrap_or_default();
        let job = self
            .exports
            .job
            .as_ref()
            .filter(|job| job.thread_id == dialog.thread_id);
        let other_running = self.archive_busy() && job.is_none();
        let cancelling = self.archive_cancelling();

        let header = h_flex()
            .justify_between()
            .gap_3()
            .child(
                v_flex()
                    .min_w_0()
                    .child(
                        div()
                            .text_size(px(18.))
                            .font_weight(FontWeight::BOLD)
                            .child(ts!("thread_dialogs.export.title")),
                    )
                    .child(
                        div()
                            .truncate()
                            .text_size(px(13.))
                            .text_color(p.text_muted)
                            .child(SharedString::from(thread_name)),
                    ),
            )
            .child(
                icon_button("close-export", IconName::X, false, p)
                    .on_click(cx.listener(|this, _, _, cx| this.close_export(cx))),
            );

        let body: AnyElement = match job {
            Some(job) => {
                let progress = job.progress_label();
                match &job.result {
                    None => v_flex()
                        .gap_3()
                        .child(
                            h_flex()
                                .gap_2p5()
                                .child(
                                    Icon::new(IconName::LoaderCircle)
                                        .size(px(18.))
                                        .text_color(p.accent_text),
                                )
                                .child(
                                    div()
                                        .text_size(px(14.))
                                        .font_weight(FontWeight::SEMIBOLD)
                                        .child(SharedString::from(format!("{progress}…"))),
                                ),
                        )
                        .child(
                            div()
                                .text_size(px(12.5))
                                .line_height(px(18.))
                                .text_color(p.text_muted)
                                .child(ts!("thread_dialogs.export.running_hint")),
                        )
                        .child(
                            h_flex()
                                .justify_end()
                                .gap_2()
                                .child(
                                    Button::new("export-cancel-job")
                                        .ghost()
                                        .label(if cancelling {
                                            t!("thread_dialogs.export.cancelling")
                                        } else {
                                            t!("thread_dialogs.export.cancel_job")
                                        })
                                        .disabled(cancelling)
                                        .on_click(
                                            cx.listener(|this, _, _, cx| this.cancel_archive(cx)),
                                        ),
                                )
                                .child(
                                    Button::new("export-hide")
                                        .primary()
                                        .label(t!("thread_dialogs.export.hide"))
                                        .on_click(
                                            cx.listener(|this, _, _, cx| this.close_export(cx)),
                                        ),
                                ),
                        )
                        .into_any_element(),
                    Some(Ok(outcome)) => {
                        let summary = outcome.summary_label();
                        v_flex()
                            .gap_3()
                            .child(
                                h_flex()
                                    .gap_2p5()
                                    .child(
                                        Icon::new(IconName::CircleCheck)
                                            .size(px(18.))
                                            .text_color(p.online),
                                    )
                                    .child(
                                        div()
                                            .text_size(px(14.))
                                            .font_weight(FontWeight::SEMIBOLD)
                                            .child(ts!("thread_dialogs.export.done")),
                                    ),
                            )
                            .child(
                                div()
                                    .text_size(px(13.))
                                    .text_color(p.text_muted)
                                    .child(SharedString::from(summary)),
                            )
                            .child(
                                div()
                                    .px_3()
                                    .py_2()
                                    .rounded(px(12.))
                                    .bg(p.chip_bg)
                                    .text_size(px(12.))
                                    .text_color(p.text_muted)
                                    .truncate()
                                    .child(SharedString::from(outcome.path.clone())),
                            )
                            .child(
                                h_flex()
                                    .justify_end()
                                    .gap_2()
                                    .child(
                                        Button::new("export-close")
                                            .ghost()
                                            .label(t!("thread_dialogs.button.close"))
                                            .on_click(
                                                cx.listener(|this, _, _, cx| this.close_export(cx)),
                                            ),
                                    )
                                    .child(
                                        Button::new("export-open")
                                            .primary()
                                            .label(t!("thread_dialogs.export.open_folder"))
                                            .on_click(cx.listener(|this, _, _, cx| {
                                                this.open_export_folder(cx)
                                            })),
                                    ),
                            )
                            .into_any_element()
                    }
                    Some(Err(error)) => v_flex()
                        .gap_3()
                        .child(
                            div()
                                .text_size(px(13.5))
                                .line_height(px(19.))
                                .text_color(p.danger)
                                .child(error.clone()),
                        )
                        .child(
                            h_flex()
                                .justify_end()
                                .gap_2()
                                .child(
                                    Button::new("export-close")
                                        .ghost()
                                        .label(t!("thread_dialogs.button.close"))
                                        .on_click(
                                            cx.listener(|this, _, _, cx| this.close_export(cx)),
                                        ),
                                )
                                .child(
                                    Button::new("export-retry")
                                        .primary()
                                        .label(t!("thread_dialogs.export.retry"))
                                        .on_click(cx.listener(|this, _, _, cx| {
                                            this.exports.job = None;
                                            this.start_export(cx);
                                        })),
                                ),
                        )
                        .into_any_element(),
                }
            }
            None => {
                let hover = p.hover;
                let formats: Vec<AnyElement> = self
                    .export_format_options()
                    .into_iter()
                    .enumerate()
                    .map(|(index, (format, active))| {
                        h_flex()
                            .id(("export-format", index))
                            .gap_3()
                            .px_3()
                            .py_2()
                            .rounded(px(12.))
                            .cursor_pointer()
                            .when(active, |el| el.bg(p.accent_soft))
                            .when(!active, |el| el.hover(move |s| s.bg(hover)))
                            .child(radio_dot(active, p))
                            .child(
                                v_flex()
                                    .min_w_0()
                                    .child(
                                        div()
                                            .text_size(px(13.5))
                                            .font_weight(FontWeight::SEMIBOLD)
                                            .child(format.label()),
                                    )
                                    .child(
                                        div()
                                            .text_size(px(12.))
                                            .text_color(p.text_muted)
                                            .child(format.hint()),
                                    ),
                            )
                            .on_click(cx.listener(move |this, _, _, cx| {
                                this.set_export_format(format, cx)
                            }))
                            .into_any_element()
                    })
                    .collect();
                let dir_label: SharedString = dialog
                    .dir
                    .as_ref()
                    .map(|dir| dir.to_string_lossy().to_string())
                    .unwrap_or_else(|| t!("thread_dialogs.export.no_dir").to_string())
                    .into();
                v_flex()
                    .gap_4()
                    .child(
                        v_flex()
                            .gap_1()
                            .child(field_label(t!("thread_dialogs.export.format"), p))
                            .children(formats),
                    )
                    .child(
                        h_flex()
                            .id("export-media")
                            .gap_3()
                            .px_3()
                            .py_2()
                            .rounded(px(12.))
                            .cursor_pointer()
                            .hover(move |s| s.bg(hover))
                            .child(check_box(dialog.include_media, p))
                            .child(
                                v_flex()
                                    .min_w_0()
                                    .child(
                                        div()
                                            .text_size(px(13.5))
                                            .font_weight(FontWeight::SEMIBOLD)
                                            .child(ts!("thread_dialogs.export.include_media")),
                                    )
                                    .child(
                                        div()
                                            .text_size(px(12.))
                                            .text_color(p.text_muted)
                                            .child(ts!("thread_dialogs.export.media_hint")),
                                    ),
                            )
                            .on_click(cx.listener(|this, _, _, cx| this.toggle_export_media(cx))),
                    )
                    .child(
                        v_flex()
                            .gap_1p5()
                            .child(field_label(t!("thread_dialogs.export.destination"), p))
                            .child(
                                h_flex()
                                    .gap_2()
                                    .child(
                                        div()
                                            .flex_1()
                                            .min_w_0()
                                            .px_3()
                                            .py_2()
                                            .rounded(px(12.))
                                            .bg(p.field_bg)
                                            .text_size(px(12.5))
                                            .text_color(p.text_muted)
                                            .truncate()
                                            .child(dir_label),
                                    )
                                    .child(
                                        Button::new("export-dir")
                                            .outline()
                                            .small()
                                            .label(t!("thread_dialogs.export.choose"))
                                            .on_click(cx.listener(|this, _, _, cx| {
                                                this.pick_export_dir(cx)
                                            })),
                                    ),
                            ),
                    )
                    .when(other_running, |el| {
                        el.child(
                            div()
                                .text_size(px(12.5))
                                .text_color(p.text_muted)
                                .child(ts!("thread_dialogs.export.busy")),
                        )
                    })
                    .child(
                        h_flex()
                            .justify_end()
                            .gap_2()
                            .child(
                                Button::new("export-cancel")
                                    .ghost()
                                    .label(t!("thread_dialogs.button.cancel"))
                                    .on_click(cx.listener(|this, _, _, cx| this.close_export(cx))),
                            )
                            .child(
                                Button::new("export-start")
                                    .primary()
                                    .label(t!("thread_dialogs.export.start"))
                                    .disabled(other_running || dialog.dir.is_none())
                                    .on_click(cx.listener(|this, _, _, cx| this.start_export(cx))),
                            ),
                    )
                    .into_any_element()
            }
        };

        scrim()
            .on_mouse_down(
                MouseButton::Left,
                cx.listener(|this, _: &MouseDownEvent, _, cx| this.close_export(cx)),
            )
            .child(
                dialog_card("export-dialog", 480., p)
                    .gap_4()
                    .p_6()
                    .child(header)
                    .child(body),
            )
            .into_any_element()
    }

    fn render_search_panel(&self, p: &Palette, cx: &mut Context<Self>) -> AnyElement {
        let Some(panel) = &self.thread_ops.search.panel else {
            return div().into_any_element();
        };
        let search = &self.thread_ops.search;
        let has_filters = search.query.has_filters();
        let kinds: Vec<AnyElement> = self
            .search_kind_options()
            .into_iter()
            .enumerate()
            .map(|(index, (kind, active))| {
                pill(("panel-kind", index), kind.label(), active, p)
                    .on_click(cx.listener(move |this, _, window, cx| {
                        this.toggle_search_kind(kind, window, cx)
                    }))
                    .into_any_element()
            })
            .collect();
        let periods: Vec<AnyElement> = self
            .search_period_options()
            .into_iter()
            .enumerate()
            .map(|(index, (label, raw, active))| {
                pill(("panel-period", index), label, active, p)
                    .on_click(cx.listener(move |this, _, window, cx| {
                        let value = (!active).then(|| raw.clone());
                        this.set_search_after(value, window, cx)
                    }))
                    .into_any_element()
            })
            .collect();

        let me_active = search
            .sender
            .as_ref()
            .is_some_and(|(id, _)| self.user.as_ref().is_some_and(|user| &user.id == id));
        let typed_person = panel.person.read(cx).value().trim().to_string();
        let mut people: Vec<AnyElement> = vec![
            pill("panel-me", t!("library.search.me"), me_active, p)
                .on_click(cx.listener(move |this, _, window, cx| {
                    this.set_search_from_me(!me_active, window, cx)
                }))
                .into_any_element(),
        ];
        if !typed_person.is_empty() {
            for (index, (_, name)) in self.search_people(&typed_person).into_iter().take(4).enumerate() {
                people.push(
                    pill(("panel-person", index), name.clone(), false, p)
                        .on_click(cx.listener(move |this, _, window, cx| {
                            this.set_search_from(Some(name.clone()), window, cx)
                        }))
                        .into_any_element(),
                );
            }
        }

        let typed_conversation = panel.conversation.read(cx).value().trim().to_string();
        let mut conversations: Vec<AnyElement> = Vec::new();
        if let Some(current) = self.selected_thread().map(|thread| thread.name.clone()) {
            let active = search
                .thread
                .as_ref()
                .is_some_and(|(id, _)| self.selected.as_deref() == Some(id.as_str()));
            conversations.push(
                pill(
                    "panel-current",
                    t!("thread_dialogs.search.this_conversation"),
                    active,
                    p,
                )
                    .on_click(cx.listener(move |this, _, window, cx| {
                        let value = (!active).then(|| current.clone());
                        this.set_search_within(value, window, cx)
                    }))
                    .into_any_element(),
            );
        }
        if !typed_conversation.is_empty() {
            for (index, (_, name)) in self
                .search_conversations(&typed_conversation)
                .into_iter()
                .filter(|(_, name)| !name.trim().is_empty())
                .take(4)
                .enumerate()
            {
                conversations.push(
                    pill(("panel-conversation", index), name.clone(), false, p)
                        .on_click(cx.listener(move |this, _, window, cx| {
                            this.set_search_within(Some(name.clone()), window, cx)
                        }))
                        .into_any_element(),
                );
            }
        }

        let field = |input: &Entity<gpui_kit::component::input::InputState>| {
            div()
                .h(px(34.))
                .px_3()
                .flex()
                .items_center()
                .rounded(px(10.))
                .bg(p.field_bg)
                .child(div().flex_1().child(Input::new(input).appearance(false).small()))
        };
        let wrap = |children: Vec<AnyElement>| div().flex().flex_wrap().gap_1p5().children(children);
        let accent = p.accent_text;

        let close = cx.listener(|this, _: &MouseDownEvent, _, cx| this.close_search_panel(cx));
        div()
            .absolute()
            .inset_0()
            .occlude()
            .on_mouse_down(MouseButton::Left, close)
            .child(
                anchored()
                    .position(panel.position)
                    .snap_to_window_with_margin(px(8.))
                    .child(
                        v_flex()
                            .id("search-panel")
                            .w(px(340.))
                            .p_4()
                            .gap_3()
                            .rounded(px(16.))
                            .bg(p.panel)
                            .border_1()
                            .border_color(p.divider)
                            .shadow_lg()
                            .on_mouse_down(MouseButton::Left, |_, _, cx| cx.stop_propagation())
                            .child(
                                h_flex()
                                    .justify_between()
                                    .child(
                                        div()
                                            .text_size(px(15.))
                                            .font_weight(FontWeight::BOLD)
                                            .child(ts!("thread_dialogs.search.title")),
                                    )
                                    .when(has_filters, |el| {
                                        el.child(
                                            div()
                                                .id("panel-clear")
                                                .text_size(px(12.5))
                                                .font_weight(FontWeight::SEMIBOLD)
                                                .text_color(accent)
                                                .cursor_pointer()
                                                .child(ts!("thread_dialogs.search.clear"))
                                                .on_click(cx.listener(|this, _, window, cx| {
                                                    this.clear_search_filters(window, cx)
                                                })),
                                        )
                                    }),
                            )
                            .child(
                                v_flex()
                                    .gap_1p5()
                                    .child(field_label(t!("thread_dialogs.search.containing"), p))
                                    .child(wrap(kinds)),
                            )
                            .child(
                                v_flex()
                                    .gap_1p5()
                                    .child(field_label(t!("thread_dialogs.search.period"), p))
                                    .child(wrap(periods)),
                            )
                            .child(
                                v_flex()
                                    .gap_1p5()
                                    .child(field_label(t!("thread_dialogs.search.sent_by"), p))
                                    .child(field(&panel.person))
                                    .child(wrap(people)),
                            )
                            .child(
                                v_flex()
                                    .gap_1p5()
                                    .child(field_label(
                                        t!("thread_dialogs.search.in_conversation"),
                                        p,
                                    ))
                                    .child(field(&panel.conversation))
                                    .when(!conversations.is_empty(), |el| {
                                        el.child(wrap(conversations))
                                    }),
                            )
                            .child(
                                div()
                                    .pt_1()
                                    .text_size(px(11.5))
                                    .line_height(px(16.))
                                    .text_color(p.text_faint)
                                    .child(ts!("thread_dialogs.search.help")),
                            ),
                    ),
            )
            .into_any_element()
    }
}

fn field_label(text: impl Into<SharedString>, p: &Palette) -> Div {
    div()
        .text_size(px(12.))
        .font_weight(FontWeight::BOLD)
        .text_color(p.text_muted)
        .child(text.into())
}

fn pill(
    id: impl Into<ElementId>,
    label: impl Into<SharedString>,
    active: bool,
    p: &Palette,
) -> Stateful<Div> {
    let hover = p.hover;
    div()
        .id(id)
        .h(px(28.))
        .px_2p5()
        .flex()
        .items_center()
        .rounded_full()
        .text_size(px(12.5))
        .font_weight(FontWeight::SEMIBOLD)
        .cursor_pointer()
        .when(active, |el| el.bg(p.accent_soft).text_color(p.accent_text))
        .when(!active, |el| {
            el.bg(p.chip_bg)
                .text_color(p.text_muted)
                .hover(move |style| style.bg(hover))
        })
        .child(label.into())
}

fn radio_dot(active: bool, p: &Palette) -> Div {
    div()
        .size(px(18.))
        .flex_none()
        .rounded_full()
        .border_2()
        .border_color(if active { p.accent } else { p.border_strong })
        .flex()
        .items_center()
        .justify_center()
        .when(active, |el| el.child(div().size(px(8.)).rounded_full().bg(p.accent)))
}

fn check_box(checked: bool, p: &Palette) -> Div {
    div()
        .size(px(18.))
        .flex_none()
        .rounded(px(5.))
        .border_2()
        .border_color(if checked { p.accent } else { p.border_strong })
        .when(checked, |el| el.bg(p.accent))
        .flex()
        .items_center()
        .justify_center()
        .when(checked, |el| {
            el.child(Icon::new(IconName::Check).size(px(12.)).text_color(p.on_accent))
        })
}
