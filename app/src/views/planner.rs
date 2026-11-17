use gpui_kit::assets::IconName;
use gpui_kit::component::{
    Icon, Sizable as _,
    button::{Button, ButtonVariants as _},
    h_flex,
    input::Input,
    v_flex,
};
use gpui_kit::{prelude::FluentBuilder as _, *};

use super::message_dialogs::{dialog_card, dialog_header, menu_item, scrim};
use super::widgets::{format_short, icon_button};
use crate::{app::MissiveApp, style::Palette};

fn field_label(text: &'static str, p: &Palette) -> Div {
    div()
        .text_size(px(12.))
        .font_weight(FontWeight::SEMIBOLD)
        .text_color(p.text_muted)
        .child(text)
}

fn empty_state(text: &'static str, p: &Palette) -> AnyElement {
    div()
        .py_8()
        .px_6()
        .text_center()
        .text_size(px(13.))
        .line_height(px(19.))
        .text_color(p.text_faint)
        .child(text)
        .into_any_element()
}

fn tab(id: &'static str, label: String, active: bool, p: &Palette) -> Stateful<Div> {
    let hover = p.hover;
    div()
        .id(id)
        .px_3()
        .h(px(32.))
        .flex()
        .items_center()
        .rounded(px(12.))
        .text_size(px(13.))
        .font_weight(FontWeight::SEMIBOLD)
        .cursor_pointer()
        .when(active, |el| el.bg(p.accent_soft).text_color(p.accent_text))
        .when(!active, |el| {
            el.text_color(p.text_muted).hover(move |s| s.bg(hover))
        })
        .child(SharedString::from(label))
}

impl MissiveApp {
    pub(super) fn render_planner_dialogs(
        &self,
        p: &Palette,
        cx: &mut Context<Self>,
    ) -> Option<AnyElement> {
        let mut layers: Vec<AnyElement> = Vec::new();
        layers.extend(self.render_planner_list(p, cx));
        layers.extend(self.render_favorites(p, cx));
        layers.extend(self.render_schedule_menu(p, cx));
        layers.extend(self.render_date_picker(p, cx));
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

    fn render_schedule_menu(&self, p: &Palette, cx: &mut Context<Self>) -> Option<AnyElement> {
        let position = self.planner.schedule_menu.as_ref()?.position;
        let mut items: Vec<AnyElement> = vec![
            div()
                .px_3()
                .pt_1p5()
                .pb_1()
                .text_size(px(11.5))
                .font_weight(FontWeight::BOLD)
                .text_color(p.text_muted)
                .child("Programmer l'envoi")
                .into_any_element(),
        ];
        for (index, (label, at)) in self.schedule_choices().into_iter().enumerate() {
            items.push(
                menu_item(("schedule-preset", index), IconName::Clock, label, false, p)
                    .on_click(cx.listener(move |this, _, window, cx| {
                        this.schedule_from_menu(at, window, cx)
                    }))
                    .into_any_element(),
            );
        }
        items.push(
            menu_item(
                "schedule-pick",
                IconName::CalendarClock,
                "Choisir une date…",
                false,
                p,
            )
            .on_click(cx.listener(|this, _, window, cx| this.open_schedule_picker(window, cx)))
            .into_any_element(),
        );
        Some(
            div()
                .id("schedule-menu-scrim")
                .absolute()
                .inset_0()
                .occlude()
                .on_click(cx.listener(|this, _, _, cx| {
                    cx.stop_propagation();
                    this.close_schedule_menu(cx);
                }))
                .on_mouse_down(
                    MouseButton::Right,
                    cx.listener(|this, _, _, cx| {
                        cx.stop_propagation();
                        this.close_schedule_menu(cx);
                    }),
                )
                .child(
                    anchored()
                        .position(position)
                        .anchor(Anchor::BottomRight)
                        .snap_to_window_with_margin(Edges::all(px(8.)))
                        .child(
                            v_flex()
                                .id("schedule-menu")
                                .w(px(240.))
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

    fn render_date_picker(&self, p: &Palette, cx: &mut Context<Self>) -> Option<AnyElement> {
        let picker = self.planner.picker.as_ref()?;
        Some(
            scrim("date-picker-scrim")
                .on_click(cx.listener(|this, _, _, cx| {
                    cx.stop_propagation();
                    this.close_picker(cx);
                }))
                .child(
                    dialog_card("date-picker", 380., p)
                        .child(dialog_header(
                            picker.title(),
                            icon_button("close-date-picker", IconName::X, false, p)
                                .on_click(cx.listener(|this, _, _, cx| this.close_picker(cx))),
                            p,
                        ))
                        .child(
                            v_flex()
                                .px_5()
                                .pb_4()
                                .gap_3()
                                .child(
                                    h_flex()
                                        .gap_3()
                                        .child(
                                            v_flex()
                                                .flex_1()
                                                .gap_1p5()
                                                .child(field_label("Date", p))
                                                .child(Input::new(&picker.date)),
                                        )
                                        .child(
                                            v_flex()
                                                .w(px(110.))
                                                .gap_1p5()
                                                .child(field_label("Heure", p))
                                                .child(Input::new(&picker.time)),
                                        ),
                                )
                                .child(
                                    div()
                                        .text_size(px(12.))
                                        .text_color(p.text_faint)
                                        .child("Dans l'année qui vient, à l'heure de cet ordinateur."),
                                )
                                .when_some(picker.error.clone(), |el, error| {
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
                                    Button::new("date-picker-cancel")
                                        .ghost()
                                        .label("Annuler")
                                        .on_click(cx.listener(|this, _, _, cx| this.close_picker(cx))),
                                )
                                .child(
                                    Button::new("date-picker-ok")
                                        .primary()
                                        .label(picker.confirm_label())
                                        .on_click(cx.listener(|this, _, window, cx| {
                                            this.confirm_picker(window, cx)
                                        })),
                                ),
                        ),
                )
                .into_any_element(),
        )
    }

    fn render_planner_list(&self, p: &Palette, cx: &mut Context<Self>) -> Option<AnyElement> {
        self.planner.list?;
        let reminders_tab = self.planner_reminders_tab();
        let scheduled_count = self.planner.scheduled.len();
        let reminder_count = self.planner.reminders.len();
        let rows = if reminders_tab {
            self.reminder_rows(p, cx)
        } else {
            self.scheduled_rows(p, cx)
        };
        let label = |base: &str, count: usize| {
            if count > 0 {
                format!("{base} · {count}")
            } else {
                base.to_string()
            }
        };
        Some(
            scrim("planner-scrim")
                .on_click(cx.listener(|this, _, _, cx| {
                    cx.stop_propagation();
                    this.close_planner_list(cx);
                }))
                .child(
                    dialog_card("planner-list", 520., p)
                        .h(px(560.))
                        .child(dialog_header(
                            "Planification",
                            icon_button("close-planner", IconName::X, false, p)
                                .on_click(cx.listener(|this, _, _, cx| this.close_planner_list(cx))),
                            p,
                        ))
                        .child(
                            h_flex()
                                .flex_none()
                                .px_5()
                                .pb_3()
                                .gap_1()
                                .child(
                                    tab(
                                        "planner-tab-scheduled",
                                        label("Messages programmés", scheduled_count),
                                        !reminders_tab,
                                        p,
                                    )
                                    .on_click(cx.listener(|this, _, _, cx| {
                                        this.show_planner_tab(false, cx)
                                    })),
                                )
                                .child(
                                    tab(
                                        "planner-tab-reminders",
                                        label("Rappels", reminder_count),
                                        reminders_tab,
                                        p,
                                    )
                                    .on_click(cx.listener(|this, _, _, cx| {
                                        this.show_planner_tab(true, cx)
                                    })),
                                ),
                        )
                        .child(
                            v_flex()
                                .id("planner-rows")
                                .flex_1()
                                .min_h_0()
                                .px_3()
                                .pb_3()
                                .gap_1()
                                .overflow_y_scroll()
                                .children(rows),
                        ),
                )
                .into_any_element(),
        )
    }

    fn scheduled_rows(&self, p: &Palette, cx: &mut Context<Self>) -> Vec<AnyElement> {
        let items = self.planner.sorted_scheduled();
        if items.is_empty() {
            return vec![empty_state(
                "Aucun message programmé. Écrivez un message puis utilisez l'horloge à côté du bouton d'envoi.",
                p,
            )];
        }
        let hover = p.hover;
        items
            .into_iter()
            .enumerate()
            .map(|(index, item)| {
                let alert = item.needs_decision();
                let when = if item.sending {
                    "Envoi en cours…".to_string()
                } else if item.failure.is_some() {
                    format!("Non envoyé · prévu {}", self.planner_when(item.send_at_ms))
                } else if item.overdue {
                    format!("En retard · prévu {}", self.planner_when(item.send_at_ms))
                } else {
                    self.planner_when_title(item.send_at_ms)
                };
                let open_thread = item.thread_id.clone();
                let send_id = item.id.clone();
                let edit_id = item.id.clone();
                let delete_id = item.id.clone();
                let mut actions = h_flex().gap_1().justify_end();
                if !item.sending {
                    if !item.overdue {
                        actions = actions.child(
                            Button::new(("scheduled-edit", index))
                                .ghost()
                                .small()
                                .label("Modifier l'heure")
                                .on_click(cx.listener(move |this, _, window, cx| {
                                    this.open_reschedule_picker(edit_id.clone(), window, cx)
                                })),
                        );
                    }
                    actions = actions
                        .child(
                            Button::new(("scheduled-delete", index))
                                .ghost()
                                .small()
                                .label("Supprimer")
                                .on_click(cx.listener(move |this, _, _, cx| {
                                    this.delete_scheduled(&delete_id, cx)
                                })),
                        )
                        .child(
                            Button::new(("scheduled-send", index))
                                .when(alert, |b| b.primary())
                                .when(!alert, |b| b.outline())
                                .small()
                                .label(if item.failure.is_some() {
                                    "Réessayer"
                                } else {
                                    "Envoyer maintenant"
                                })
                                .on_click(cx.listener(move |this, _, _, cx| {
                                    this.send_scheduled_now(&send_id, cx)
                                })),
                        );
                }
                v_flex()
                    .id(("scheduled-row", index))
                    .px_3()
                    .py_2p5()
                    .gap_1()
                    .rounded(px(12.))
                    .hover(move |s| s.bg(hover))
                    .child(
                        h_flex()
                            .id(("scheduled-open", index))
                            .gap_2()
                            .cursor_pointer()
                            .on_click(cx.listener(move |this, _, window, cx| {
                                this.open_planner_thread(open_thread.clone(), window, cx)
                            }))
                            .child(
                                div()
                                    .flex_1()
                                    .min_w_0()
                                    .truncate()
                                    .text_size(px(13.5))
                                    .font_weight(FontWeight::SEMIBOLD)
                                    .child(SharedString::from(if item.thread_name.is_empty() {
                                        "Conversation".to_string()
                                    } else {
                                        item.thread_name.clone()
                                    })),
                            )
                            .child(
                                h_flex()
                                    .flex_none()
                                    .gap_1()
                                    .text_size(px(12.))
                                    .font_weight(FontWeight::SEMIBOLD)
                                    .text_color(if alert { p.danger } else { p.accent_text })
                                    .child(
                                        Icon::new(if alert {
                                            IconName::ClockAlert
                                        } else {
                                            IconName::Clock
                                        })
                                        .size(px(13.)),
                                    )
                                    .child(SharedString::from(when)),
                            ),
                    )
                    .child(
                        div()
                            .text_size(px(13.))
                            .line_height(px(18.))
                            .text_color(p.text_muted)
                            .line_clamp(2)
                            .child(SharedString::from(item.text.clone())),
                    )
                    .when_some(item.failure.clone(), |el, reason| {
                        el.child(
                            div()
                                .text_size(px(12.))
                                .text_color(p.danger)
                                .child(SharedString::from(reason)),
                        )
                    })
                    .child(actions)
                    .into_any_element()
            })
            .collect()
    }

    fn reminder_rows(&self, p: &Palette, cx: &mut Context<Self>) -> Vec<AnyElement> {
        let items = self.planner.sorted_reminders();
        if items.is_empty() {
            return vec![empty_state(
                "Aucun rappel. Utilisez « Me le rappeler » dans le menu d'un message.",
                p,
            )];
        }
        let hover = p.hover;
        items
            .into_iter()
            .enumerate()
            .map(|(index, reminder)| {
                let fired = reminder.fired_ms.is_some();
                let when = if fired {
                    format!("Rappelé {}", self.planner_when(reminder.fired_ms.unwrap_or(reminder.due_ms)))
                } else {
                    self.planner_when_title(reminder.due_ms)
                };
                let (thread_id, message_id) =
                    (reminder.thread_id.clone(), reminder.message_id.clone());
                let first_id = reminder.id.clone();
                let second_id = reminder.id.clone();
                let actions = if fired {
                    h_flex()
                        .gap_1()
                        .justify_end()
                        .child(
                            Button::new(("reminder-done", index))
                                .ghost()
                                .small()
                                .label("Terminé")
                                .on_click(cx.listener(move |this, _, _, cx| {
                                    this.delete_reminder(&first_id, cx)
                                })),
                        )
                        .child(
                            Button::new(("reminder-snooze", index))
                                .outline()
                                .small()
                                .label("Reporter d'1 h")
                                .on_click(cx.listener(move |this, _, _, cx| {
                                    this.snooze_reminder(&second_id, cx)
                                })),
                        )
                } else {
                    h_flex()
                        .gap_1()
                        .justify_end()
                        .child(
                            Button::new(("reminder-edit", index))
                                .ghost()
                                .small()
                                .label("Modifier")
                                .on_click(cx.listener(move |this, _, window, cx| {
                                    this.open_move_reminder_picker(first_id.clone(), window, cx)
                                })),
                        )
                        .child(
                            Button::new(("reminder-delete", index))
                                .ghost()
                                .small()
                                .label("Supprimer")
                                .on_click(cx.listener(move |this, _, _, cx| {
                                    this.delete_reminder(&second_id, cx)
                                })),
                        )
                };
                v_flex()
                    .id(("reminder-row", index))
                    .px_3()
                    .py_2p5()
                    .gap_1()
                    .rounded(px(12.))
                    .hover(move |s| s.bg(hover))
                    .child(
                        v_flex()
                            .id(("reminder-open", index))
                            .gap_1()
                            .cursor_pointer()
                            .on_click(cx.listener(move |this, _, window, cx| {
                                this.open_planner_message(
                                    thread_id.clone(),
                                    message_id.clone(),
                                    window,
                                    cx,
                                )
                            }))
                            .child(
                                h_flex()
                                    .gap_2()
                                    .child(
                                        div()
                                            .flex_1()
                                            .min_w_0()
                                            .truncate()
                                            .text_size(px(13.5))
                                            .font_weight(FontWeight::SEMIBOLD)
                                            .child(SharedString::from(
                                                if reminder.thread_name.is_empty() {
                                                    "Conversation".to_string()
                                                } else {
                                                    reminder.thread_name.clone()
                                                },
                                            )),
                                    )
                                    .child(
                                        h_flex()
                                            .flex_none()
                                            .gap_1()
                                            .text_size(px(12.))
                                            .font_weight(FontWeight::SEMIBOLD)
                                            .text_color(if fired {
                                                p.signal_text
                                            } else {
                                                p.accent_text
                                            })
                                            .child(
                                                Icon::new(if fired {
                                                    IconName::BellRing
                                                } else {
                                                    IconName::AlarmClock
                                                })
                                                .size(px(13.)),
                                            )
                                            .child(SharedString::from(when)),
                                    ),
                            )
                            .child(
                                div()
                                    .text_size(px(13.))
                                    .line_height(px(18.))
                                    .text_color(p.text_muted)
                                    .line_clamp(2)
                                    .child(SharedString::from(reminder.preview.clone())),
                            ),
                    )
                    .child(actions)
                    .into_any_element()
            })
            .collect()
    }

    fn render_favorites(&self, p: &Palette, cx: &mut Context<Self>) -> Option<AnyElement> {
        let view = self.planner.favorites_view.as_ref()?;
        let results = self.favorite_results(cx);
        let total = self.planner.favorites.len();
        let hover = p.hover;
        let mut rows: Vec<AnyElement> = Vec::with_capacity(results.len());
        for (index, favorite) in results.into_iter().enumerate() {
            let (thread_id, message_id) = (favorite.thread_id.clone(), favorite.message_id.clone());
            let remove_id = favorite.message_id.clone();
            let body = if favorite.text.is_empty() {
                if favorite.kind.is_empty() {
                    "Message".to_string()
                } else {
                    favorite.kind.clone()
                }
            } else {
                favorite.text.clone()
            };
            let header = if favorite.sender_name.is_empty() {
                favorite.thread_name.clone()
            } else if favorite.thread_name.is_empty() || favorite.thread_name == favorite.sender_name
            {
                favorite.sender_name.clone()
            } else {
                format!("{} · {}", favorite.sender_name, favorite.thread_name)
            };
            rows.push(
                h_flex()
                    .id(("favorite-row", index))
                    .px_3()
                    .py_2p5()
                    .gap_3()
                    .items_start()
                    .rounded(px(12.))
                    .cursor_pointer()
                    .hover(move |s| s.bg(hover))
                    .on_click(cx.listener(move |this, _, window, cx| {
                        this.open_planner_message(thread_id.clone(), message_id.clone(), window, cx)
                    }))
                    .child(
                        Icon::new(IconName::StarFill)
                            .size(px(15.))
                            .mt(px(2.))
                            .text_color(p.signal),
                    )
                    .child(
                        v_flex()
                            .flex_1()
                            .min_w_0()
                            .gap_0p5()
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
                                            .text_color(p.text_muted)
                                            .child(SharedString::from(header)),
                                    )
                                    .child(
                                        div()
                                            .flex_none()
                                            .text_size(px(11.5))
                                            .text_color(p.text_faint)
                                            .child(format_short(favorite.timestamp_ms)),
                                    ),
                            )
                            .child(
                                div()
                                    .text_size(px(13.5))
                                    .line_height(px(19.))
                                    .line_clamp(3)
                                    .child(SharedString::from(body)),
                            ),
                    )
                    .child(
                        icon_button(("favorite-remove", index), IconName::StarOff, false, p)
                            .size(px(30.))
                            .on_click(cx.listener(move |this, _, _, cx| {
                                cx.stop_propagation();
                                this.remove_favorite(&remove_id, cx)
                            })),
                    )
                    .into_any_element(),
            );
        }
        if rows.is_empty() {
            rows.push(empty_state(
                if total == 0 {
                    "Aucun favori. Utilisez « Ajouter aux favoris » dans le menu d'un message ; ils restent sur cet ordinateur."
                } else {
                    "Aucun favori ne correspond à la recherche."
                },
                p,
            ));
        }
        Some(
            scrim("favorites-scrim")
                .on_click(cx.listener(|this, _, _, cx| {
                    cx.stop_propagation();
                    this.close_favorites(cx);
                }))
                .child(
                    dialog_card("favorites", 500., p)
                        .h(px(580.))
                        .child(dialog_header(
                            "Favoris",
                            icon_button("close-favorites", IconName::X, false, p)
                                .on_click(cx.listener(|this, _, _, cx| this.close_favorites(cx))),
                            p,
                        ))
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
                                .child(div().flex_1().children(
                                    view.input
                                        .as_ref()
                                        .map(|input| Input::new(input).appearance(false)),
                                )),
                        )
                        .child(
                            v_flex()
                                .id("favorites-list")
                                .flex_1()
                                .min_h_0()
                                .px_3()
                                .pb_3()
                                .gap_0p5()
                                .overflow_y_scroll()
                                .children(rows),
                        ),
                )
                .into_any_element(),
        )
    }
}
