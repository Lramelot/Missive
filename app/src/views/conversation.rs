use gpui_kit::assets::IconName;
use gpui_kit::component::{
    Icon, Sizable as _,
    button::{Button, ButtonVariants as _},
    h_flex,
    input::Input,
    message_scroller::MessageScroller,
    skeleton::Skeleton,
    tooltip::Tooltip,
    v_flex,
};
use gpui_kit::{prelude::FluentBuilder as _, *};

use super::{
    sidebar::display_name,
    widgets::{avatar, icon_button, thread_avatar, typing_dots},
};
use crate::{
    app::{GalleryTab, MissiveApp},
    model::Thread,
    style::Palette,
};

pub(super) const COLUMN_WIDTH: f32 = 760.;
pub(super) const COLUMN_GUTTER: f32 = 26.;

pub(super) fn column(content: impl IntoElement) -> Div {
    div()
        .w_full()
        .flex_none()
        .flex()
        .justify_center()
        .px(px(COLUMN_GUTTER))
        .child(div().w_full().max_w(px(COLUMN_WIDTH)).child(content))
}

impl MissiveApp {
    pub(super) fn render_conversation(&self, p: &Palette, cx: &mut Context<Self>) -> AnyElement {
        let card = v_flex()
            .flex_1()
            .h_full()
            .min_w_0()
            .bg(p.panel)
            .overflow_hidden();

        let Some(thread) = self.selected_thread() else {
            return card
                .items_center()
                .justify_center()
                .gap_3()
                .child(
                    div()
                        .size(px(72.))
                        .rounded_full()
                        .bg(p.accent_soft)
                        .flex()
                        .items_center()
                        .justify_center()
                        .child(
                            Icon::new(IconName::MessageCircle)
                                .size(px(32.))
                                .text_color(p.accent_text),
                        ),
                )
                .child(
                    div()
                        .text_size(px(20.))
                        .font_weight(FontWeight::EXTRA_BOLD)
                        .child("Vos discussions"),
                )
                .child(
                    div()
                        .text_size(px(13.))
                        .text_color(p.text_muted)
                        .child("Choisissez une conversation dans la liste."),
                )
                .into_any_element();
        };

        let typing = self.typing.get(&thread.id).copied().unwrap_or(false);
        let view = cx.entity();
        let drop_bg = p.accent_soft;
        let header = self.render_header(thread, p, cx);
        let request_banner = self
            .is_request_thread(thread)
            .then(|| self.render_request_banner(thread, p, cx));
        let blocked_banner = (thread.blocked && !thread.is_group)
            .then(|| self.render_blocked_banner(thread, p, cx))
            .flatten();
        let pinned_bar = self.render_pinned_bar(thread, p, cx);
        let find_bar = self.render_find_bar(p, cx);

        card.id("conversation")
            .on_drop(cx.listener(|this, paths: &ExternalPaths, _, cx| {
                this.add_pending(paths.0.iter().cloned(), cx);
            }))
            .drag_over::<ExternalPaths>(move |style, _, _, _| style.bg(drop_bg))
            .child(header)
            .children(request_banner)
            .children(blocked_banner)
            .children(pinned_bar)
            .children(find_bar)
            .child(if self.is_loading_thread(&thread.id) {
                message_skeleton(p).into_any_element()
            } else {
                div()
                    .flex_1()
                    .min_h_0()
                    .child(
                        MessageScroller::new("messages", self.scroller.clone(), {
                            let view = view.clone();
                            let thread_id = thread.id.clone();
                            move |index, _, cx| {
                                view.read(cx).render_message(&thread_id, index, &view, cx)
                            }
                        })
                        .with_jump_button_label("Derniers messages")
                        .with_row_style(StyleRefinement::default().px(px(0.)).pb(px(0.)))
                        .size_full(),
                    )
                    .into_any_element()
            })
            .when(typing, |el| {
                let label = if thread.is_group {
                    "Quelqu'un écrit".to_string()
                } else {
                    format!("{} écrit", display_name(thread))
                };
                el.child(
                    div()
                        .w_full()
                        .flex_none()
                        .flex()
                        .justify_center()
                        .child(
                            h_flex()
                                .w_full()
                                .max_w(px(COLUMN_WIDTH))
                                .px(px(COLUMN_GUTTER))
                                .pt_1()
                                .gap_2()
                                .text_size(px(12.5))
                                .text_color(p.text_muted)
                                .child(typing_dots("typing-conversation", p.text_faint))
                                .child(SharedString::from(label)),
                        ),
                )
            })
            .child(self.render_composer_area(thread, p, cx))
            .into_any_element()
    }

    fn render_find_bar(&self, p: &Palette, cx: &mut Context<Self>) -> Option<AnyElement> {
        let bar = self.find.bar.as_ref()?;
        let status = self.find_status()?;
        let hover = p.hover;
        let nav = |id: &'static str, icon: IconName, enabled: bool| {
            div()
                .id(id)
                .size(px(30.))
                .flex_none()
                .rounded(px(10.))
                .flex()
                .items_center()
                .justify_center()
                .when(enabled, |el| {
                    el.cursor_pointer().hover(move |style| style.bg(hover))
                })
                .child(Icon::new(icon).size(px(16.)).text_color(if enabled {
                    p.text_muted
                } else {
                    p.text_faint
                }))
        };
        let label = |text: &'static str| {
            div()
                .flex_none()
                .text_size(px(12.5))
                .text_color(p.text_muted)
                .child(text)
        };
        let info: Option<AnyElement> = if bar.query.is_empty() {
            None
        } else if status.total > 0 {
            Some(
                div()
                    .flex_none()
                    .text_size(px(12.5))
                    .text_color(p.text_muted)
                    .child(SharedString::from(format!(
                        "{} / {}",
                        status.position.unwrap_or(0),
                        status.total
                    )))
                    .into_any_element(),
            )
        } else if bar.searching {
            Some(
                h_flex()
                    .flex_none()
                    .gap_1p5()
                    .child(
                        Icon::new(IconName::LoaderCircle)
                            .size(px(14.))
                            .text_color(p.text_faint),
                    )
                    .child(label("Recherche…"))
                    .into_any_element(),
            )
        } else if bar.exhausted {
            Some(label("Aucun résultat").into_any_element())
        } else {
            Some(
                h_flex()
                    .flex_none()
                    .gap_2()
                    .child(label("Aucun résultat chargé"))
                    .child(
                        div()
                            .id("find-further")
                            .text_size(px(12.5))
                            .font_weight(FontWeight::SEMIBOLD)
                            .text_color(p.accent_text)
                            .cursor_pointer()
                            .hover(|style| style.underline())
                            .child("Chercher plus loin")
                            .on_click(cx.listener(|this, _, _, cx| this.find_further(cx))),
                    )
                    .into_any_element(),
            )
        };
        let navigable = status.total > 0;
        Some(
            div()
                .w_full()
                .flex_none()
                .flex()
                .justify_center()
                .border_b_1()
                .border_color(p.divider)
                .child(
                    h_flex()
                        .w_full()
                        .max_w(px(COLUMN_WIDTH))
                        .h(px(44.))
                        .px(px(COLUMN_GUTTER))
                        .gap_2()
                        .child(
                            Icon::new(IconName::Search)
                                .size(px(15.))
                                .text_color(p.text_faint),
                        )
                        .child(
                            div()
                                .flex_1()
                                .min_w_0()
                                .child(Input::new(&bar.input).appearance(false).small()),
                        )
                        .children(info)
                        .child(
                            nav("find-older", IconName::ChevronUp, navigable)
                                .tooltip(|window, cx| {
                                    Tooltip::new("Plus ancien (Entrée)").build(window, cx)
                                })
                                .on_click(cx.listener(|this, _, _, cx| this.step_find(-1, cx))),
                        )
                        .child(
                            nav("find-newer", IconName::ChevronDown, navigable)
                                .tooltip(|window, cx| {
                                    Tooltip::new("Plus récent (Maj+Entrée)").build(window, cx)
                                })
                                .on_click(cx.listener(|this, _, _, cx| this.step_find(1, cx))),
                        )
                        .child(
                            nav("find-close", IconName::X, true)
                                .tooltip(|window, cx| Tooltip::new("Fermer (Échap)").build(window, cx))
                                .on_click(cx.listener(|this, _, _, cx| {
                                    this.close_find(cx);
                                })),
                        ),
                )
                .into_any_element(),
        )
    }

    fn subtitle(&self, thread: &Thread, p: &Palette) -> AnyElement {
        let line = h_flex()
            .min_w_0()
            .gap(px(7.))
            .text_color(p.text_muted);
        let dot = || {
            div()
                .size(px(8.))
                .flex_none()
                .rounded_full()
                .bg(p.online)
        };
        let members = (thread.is_group && !thread.participants.is_empty())
            .then(|| thread.participants.len());
        let typing = self.typing.get(&thread.id).copied().unwrap_or(false);
        if typing {
            return line
                .text_color(p.accent_text)
                .child("écrit…")
                .into_any_element();
        }
        if self.is_request_thread(thread) {
            return line.child("Demande de message").into_any_element();
        }
        if self.is_online(thread) {
            return line.child(dot()).child("En ligne").into_any_element();
        }
        if let Some(label) = self.activity_label(thread) {
            let text = match members {
                Some(count) => format!("{label}, {count} membres"),
                None => label,
            };
            return line
                .when(thread.is_group, |el| el.child(dot()))
                .child(div().min_w_0().truncate().child(SharedString::from(text)))
                .into_any_element();
        }
        if thread.e2ee {
            return line
                .gap_1()
                .child(Icon::new(IconName::Lock).size(px(12.)))
                .child("Chiffré de bout en bout")
                .into_any_element();
        }
        if thread.is_group {
            return line
                .child(SharedString::from(match members {
                    Some(count) => format!("Groupe, {count} membres"),
                    None => "Groupe".to_string(),
                }))
                .into_any_element();
        }
        div().into_any_element()
    }

    fn member_stack(&self, thread: &Thread, p: &Palette) -> AnyElement {
        if !thread.is_group {
            return thread_avatar(self.avatar_spec(thread), 30., p.panel, p);
        }
        let me = self.user.as_ref().map(|user| user.id.as_str());
        let mut members: Vec<&crate::model::Participant> = thread
            .participants
            .iter()
            .filter(|participant| Some(participant.user_id.as_str()) != me)
            .collect();
        members.sort_by_key(|participant| !self.participant_online(participant));
        h_flex()
            .flex_none()
            .children(members.into_iter().take(3).enumerate().map(|(index, participant)| {
                div()
                    .flex_none()
                    .rounded_full()
                    .border_2()
                    .border_color(p.panel)
                    .when(index > 0, |el| el.ml(px(-8.)))
                    .child(avatar(
                        &participant.name,
                        self.avatar_source(participant.avatar_url.as_deref()),
                        30.,
                        p,
                    ))
            }))
            .into_any_element()
    }

    fn render_header(&self, thread: &Thread, p: &Palette, cx: &mut Context<Self>) -> AnyElement {
        let ephemeral = self.thread_ephemeral_label(thread);
        h_flex()
            .h(px(74.))
            .flex_none()
            .pl(px(COLUMN_GUTTER))
            .pr_4()
            .gap(px(14.))
            .border_b_1()
            .border_color(p.divider)
            .child(
                v_flex()
                    .flex_1()
                    .min_w_0()
                    .gap_0p5()
                    .child(
                        h_flex()
                            .min_w_0()
                            .gap_2()
                            .child(
                                div()
                                    .min_w_0()
                                    .text_size(px(20.))
                                    .font_weight(FontWeight::EXTRA_BOLD)
                                    .truncate()
                                    .child(SharedString::from(display_name(thread))),
                            )
                            .when(self.thread_is_muted(thread), |el| {
                                el.child(
                                    Icon::new(IconName::BellOff)
                                        .size(px(14.))
                                        .text_color(p.text_faint),
                                )
                            })
                            .when_some(ephemeral, |el, label| {
                                el.child(
                                    h_flex()
                                        .flex_none()
                                        .gap_1()
                                        .text_size(px(11.5))
                                        .text_color(p.text_faint)
                                        .child(Icon::new(IconName::Timer).size(px(14.)))
                                        .child(SharedString::from(label)),
                                )
                            }),
                    )
                    .child(div().text_size(px(13.)).child(self.subtitle(thread, p))),
            )
            .child(self.member_stack(thread, p))
            .child(
                h_flex()
                    .flex_none()
                    .child(
                        icon_button("open-popout", IconName::ExternalLink, false, p)
                            .on_click(cx.listener(|this, _, _, cx| this.open_popout(cx))),
                    )
                    .child(
                        icon_button("toggle-info", IconName::Info, self.info_open, p)
                            .on_click(cx.listener(|this, _, _, cx| this.toggle_info(cx))),
                    ),
            )
            .into_any_element()
    }

    fn render_request_banner(
        &self,
        thread: &Thread,
        p: &Palette,
        cx: &mut Context<Self>,
    ) -> AnyElement {
        let name = display_name(thread);
        let accept_id = thread.id.clone();
        let delete_id = thread.id.clone();
        let banner = v_flex()
            .px_4()
            .py(px(14.))
            .gap_3()
            .rounded(px(14.))
            .bg(p.accent_soft)
            .child(
                h_flex()
                    .gap_3()
                    .items_start()
                    .child(
                        Icon::new(IconName::Inbox)
                            .size(px(18.))
                            .text_color(p.accent_text),
                    )
                    .child(
                        v_flex()
                            .flex_1()
                            .min_w_0()
                            .gap_0p5()
                            .child(
                                div()
                                    .text_size(px(14.))
                                    .font_weight(FontWeight::BOLD)
                                    .child(SharedString::from(format!(
                                        "{name} souhaite vous envoyer un message"
                                    ))),
                            )
                            .child(
                                div()
                                    .text_size(px(12.5))
                                    .line_height(px(18.))
                                    .text_color(p.text_muted)
                                    .child("Acceptez pour répondre et déplacer la conversation dans vos discussions. Tant que la demande n'est pas acceptée, l'expéditeur ne sait pas que vous avez vu ses messages."),
                            ),
                    ),
            )
            .child(
                h_flex()
                    .justify_end()
                    .gap_2()
                    .child(
                        Button::new("request-delete")
                            .ghost()
                            .small()
                            .label("Supprimer")
                            .on_click(cx.listener(move |this, _, _, cx| {
                                this.confirm_delete_thread(&delete_id, cx)
                            })),
                    )
                    .child(
                        Button::new("request-accept")
                            .primary()
                            .small()
                            .label("Accepter")
                            .on_click(cx.listener(move |this, _, _, cx| {
                                this.accept_request(&accept_id, cx)
                            })),
                    ),
            );
        column(banner).pt(px(10.)).into_any_element()
    }

    fn render_blocked_banner(
        &self,
        thread: &Thread,
        p: &Palette,
        cx: &mut Context<Self>,
    ) -> Option<AnyElement> {
        let peer = thread.peer_id.clone()?;
        let accent = p.accent_text;
        let banner = h_flex()
            .px(px(14.))
            .py(px(9.))
            .gap_2p5()
            .rounded(px(12.))
            .bg(p.chip_bg)
            .text_size(px(13.))
            .child(Icon::new(IconName::Ban).size(px(16.)).text_color(p.danger))
            .child(
                div()
                    .flex_1()
                    .min_w_0()
                    .text_color(p.text_muted)
                    .child(SharedString::from(format!(
                        "Vous avez bloqué {}. Cette personne ne peut plus vous écrire.",
                        display_name(thread)
                    ))),
            )
            .child(
                div()
                    .id("blocked-unblock")
                    .flex_none()
                    .text_color(accent)
                    .font_weight(FontWeight::BOLD)
                    .cursor_pointer()
                    .child("Débloquer")
                    .on_click(
                        cx.listener(move |this, _, _, cx| this.confirm_block(&peer, false, cx)),
                    ),
            );
        Some(column(banner).pt(px(10.)).into_any_element())
    }

    pub(super) fn render_info(&self, p: &Palette, cx: &mut Context<Self>) -> AnyElement {
        let thread = self.selected_thread().cloned();
        let card = v_flex()
            .id("info-panel")
            .w(px(300.))
            .h_full()
            .flex_none()
            .bg(p.panel)
            .overflow_y_scroll();
        let Some(thread) = thread else {
            return card.into_any_element();
        };

        let shown: Vec<(crate::model::Message, crate::model::Attachment, ImageSource)> = self
            .messages
            .get(&thread.id)
            .map(|messages| {
                messages
                    .iter()
                    .rev()
                    .flat_map(|message| {
                        message
                            .attachments
                            .iter()
                            .filter(|a| a.kind == "image")
                            .filter_map(move |a| {
                                self.thumb_source(message, a)
                                    .map(|source| (message.clone(), a.clone(), source))
                            })
                    })
                    .take(9)
                    .collect()
            })
            .unwrap_or_default();
        let lightbox_items: Vec<(crate::model::Message, crate::model::Attachment)> = shown
            .iter()
            .map(|(message, attachment, _)| (message.clone(), attachment.clone()))
            .collect();
        let images: Vec<ImageSource> = shown.into_iter().map(|(_, _, source)| source).collect();

        let row = |icon: IconName, text: SharedString| {
            h_flex()
                .gap_3()
                .px_3()
                .py_2p5()
                .rounded(px(12.))
                .bg(p.chip_bg)
                .text_size(px(13.5))
                .child(Icon::new(icon).size(px(16.)).text_color(p.text_muted))
                .child(div().flex_1().min_w_0().child(text))
        };

        let mute_text: SharedString = self
            .thread_mute_label(&thread)
            .unwrap_or_else(|| "Notifications actives".to_string())
            .into();
        let mute_thread = thread.id.clone();
        let hover = p.hover;
        let notifications = h_flex()
            .id("info-mute")
            .gap_3()
            .px_3()
            .py_2p5()
            .rounded(px(12.))
            .bg(p.chip_bg)
            .text_size(px(13.5))
            .cursor_pointer()
            .hover(move |style| style.bg(hover))
            .child(
                Icon::new(if self.thread_is_muted(&thread) {
                    IconName::BellOff
                } else {
                    IconName::Bell
                })
                .size(px(16.))
                .text_color(p.text_muted),
            )
            .child(div().flex_1().min_w_0().child(mute_text))
            .child(
                Icon::new(IconName::ChevronRight)
                    .size(px(15.))
                    .text_color(p.text_faint),
            )
            .on_click(cx.listener(move |this, event: &ClickEvent, _, cx| {
                this.open_mute_menu(&mute_thread, event.position(), cx)
            }));
        let ephemeral = self.thread_ephemeral_label(&thread);
        let priority = self.is_priority_thread(&thread.id);
        let priority_thread = thread.id.clone();
        let priority_row = h_flex()
            .id("info-priority")
            .gap_3()
            .px_3()
            .py_2()
            .rounded(px(12.))
            .bg(p.chip_bg)
            .cursor_pointer()
            .hover(move |style| style.bg(hover))
            .child(
                Icon::new(IconName::Flag)
                    .size(px(16.))
                    .text_color(if priority { p.accent_text } else { p.text_muted }),
            )
            .child(
                v_flex()
                    .flex_1()
                    .min_w_0()
                    .child(div().text_size(px(13.5)).child("Conversation prioritaire"))
                    .child(
                        div()
                            .text_size(px(11.5))
                            .text_color(p.text_faint)
                            .child("Passe outre les heures calmes"),
                    ),
            )
            .child(toggle_switch(priority, p))
            .on_click(cx.listener(move |this, _, _, cx| this.toggle_priority(&priority_thread, cx)));

        let media_save = self.media_save_for(&thread.id);
        let media_running = media_save.is_some_and(|save| save.result.is_none());
        let media_done = media_save.is_some_and(|save| matches!(save.result, Some(Ok(_))));
        let media_label: SharedString = match media_save {
            Some(save) if save.result.is_none() => {
                format!("Téléchargement… {}", save.progress_label()).into()
            }
            _ => "Télécharger tous les médias…".into(),
        };
        let save_thread = thread.id.clone();
        let open_thread = thread.id.clone();
        let download_entry = h_flex()
            .id("info-save-media")
            .gap_3()
            .px_3()
            .py_2p5()
            .rounded(px(12.))
            .text_size(px(13.5))
            .when(!media_running, |el| {
                el.cursor_pointer().hover(move |style| style.bg(hover))
            })
            .child(
                Icon::new(if media_running {
                    IconName::LoaderCircle
                } else {
                    IconName::Download
                })
                .size(px(16.))
                .text_color(p.text_muted),
            )
            .child(
                div()
                    .flex_1()
                    .min_w_0()
                    .truncate()
                    .when(media_running, |el| el.text_color(p.text_muted))
                    .child(media_label),
            )
            .when(media_running, |el| {
                let cancelling = self.archive_cancelling();
                el.child(
                    div()
                        .id("info-cancel-media")
                        .flex_none()
                        .text_size(px(12.5))
                        .font_weight(FontWeight::BOLD)
                        .text_color(if cancelling { p.text_faint } else { p.accent_text })
                        .when(!cancelling, |el| el.cursor_pointer())
                        .child(if cancelling { "Annulation…" } else { "Annuler" })
                        .on_click(cx.listener(|this, _, _, cx| {
                            cx.stop_propagation();
                            this.cancel_archive(cx)
                        })),
                )
            })
            .on_click(cx.listener(move |this, _, _, cx| {
                if this.media_save_for(&save_thread).is_none_or(|save| save.result.is_some()) {
                    this.save_all_media(&save_thread, cx)
                }
            }));
        let media_folder_entry = media_done.then(|| {
            action_entry(
                "info-media-folder",
                IconName::FolderOpen,
                "Ouvrir le dossier des médias",
                false,
                p,
            )
            .on_click(cx.listener(move |this, _, _, cx| this.open_media_folder(&open_thread, cx)))
        });
        let export_thread = thread.id.clone();
        let export_running_here = self
            .exports
            .job
            .as_ref()
            .is_some_and(|job| job.thread_id == thread.id && job.result.is_none());
        let export_section = v_flex()
            .px_4()
            .pt_2()
            .gap_1()
            .child(section_title("Exporter", p))
            .child(
                action_entry(
                    "info-export",
                    if export_running_here {
                        IconName::LoaderCircle
                    } else {
                        IconName::FileDown
                    },
                    if export_running_here {
                        "Export en cours…"
                    } else {
                        "Exporter la conversation…"
                    },
                    false,
                    p,
                )
                .on_click(cx.listener(move |this, _, _, cx| this.open_export(&export_thread, cx))),
            );

        let manage = thread.is_group && thread.can_manage;
        let management: Option<AnyElement> =
            manage.then(|| {
                let (rename_id, photo_id, add_id) =
                    (thread.id.clone(), thread.id.clone(), thread.id.clone());
                v_flex()
                    .px_4()
                    .pt_5()
                    .gap_1()
                    .child(section_title("Personnaliser", p))
                    .child(
                        action_entry(
                            "info-rename",
                            IconName::Pencil,
                            "Renommer le groupe",
                            false,
                            p,
                        )
                        .on_click(cx.listener(
                            move |this, _, window, cx| this.open_rename(&rename_id, window, cx),
                        )),
                    )
                    .child(
                        action_entry("info-photo", IconName::Camera, "Changer la photo", false, p)
                            .on_click(cx.listener(move |this, _, _, cx| {
                                this.pick_thread_image(&photo_id, cx)
                            })),
                    )
                    .child(
                        action_entry(
                            "info-add",
                            IconName::UserPlus,
                            "Ajouter des personnes",
                            false,
                            p,
                        )
                        .on_click(cx.listener(
                            move |this, _, window, cx| this.open_add_people(&add_id, window, cx),
                        )),
                    )
                    .into_any_element()
            });

        let mut actions: Vec<AnyElement> = Vec::new();
        if self.is_request_thread(&thread) {
            let id = thread.id.clone();
            actions.push(
                action_entry(
                    "info-accept",
                    IconName::Check,
                    "Accepter la demande",
                    false,
                    p,
                )
                .on_click(cx.listener(move |this, _, _, cx| this.accept_request(&id, cx)))
                .into_any_element(),
            );
        }
        if manage {
            let id = thread.id.clone();
            actions.push(
                action_entry("info-leave", IconName::LogOut, "Quitter le groupe", true, p)
                    .on_click(cx.listener(move |this, _, _, cx| this.confirm_leave_group(&id, cx)))
                    .into_any_element(),
            );
        }
        if let (false, Some(peer)) = (thread.is_group, thread.peer_id.clone()) {
            let blocked = thread.blocked;
            let label = if blocked {
                format!("Débloquer {}", display_name(&thread))
            } else {
                format!("Bloquer {}", display_name(&thread))
            };
            actions.push(
                action_entry("info-block", IconName::Ban, label, !blocked, p)
                    .on_click(
                        cx.listener(move |this, _, _, cx| this.confirm_block(&peer, !blocked, cx)),
                    )
                    .into_any_element(),
            );
        }
        let delete_id = thread.id.clone();
        actions.push(
            action_entry(
                "info-delete",
                IconName::Trash,
                "Supprimer la conversation",
                true,
                p,
            )
            .on_click(cx.listener(move |this, _, _, cx| this.confirm_delete_thread(&delete_id, cx)))
            .into_any_element(),
        );

        let members = self.render_members(&thread, p, cx);

        card.child(
            v_flex()
                .items_center()
                .gap_2()
                .pt_8()
                .pb_5()
                .px_5()
                .child(thread_avatar(self.avatar_spec(&thread), 88., p.panel, p))
                .child(
                    div()
                        .pt_2()
                        .text_size(px(20.))
                        .font_weight(FontWeight::EXTRA_BOLD)
                        .text_center()
                        .child(SharedString::from(display_name(&thread))),
                )
                .child(div().text_size(px(13.)).child(self.subtitle(&thread, p))),
        )
        .child(
            v_flex()
                .px_4()
                .gap_2()
                .when(thread.e2ee, |el| {
                    el.child(row(IconName::Lock, "Chiffré de bout en bout".into()))
                })
                .child(notifications)
                .when(!self.is_request_thread(&thread), |el| el.child(priority_row))
                .when_some(ephemeral, |el, label| {
                    el.child(row(
                        IconName::Timer,
                        format!("Messages éphémères : {label}").into(),
                    ))
                })
                .when(thread.is_group, |el| {
                    el.child(row(IconName::Users, "Discussion de groupe".into()))
                }),
        )
        .children(management)
        .children(members)
        .child(
            v_flex()
                .px_4()
                .pt_5()
                .pb_4()
                .gap_2()
                .child(
                    h_flex()
                        .justify_between()
                        .child(
                            div()
                                .text_size(px(12.))
                                .font_weight(FontWeight::BOLD)
                                .text_color(p.text_muted)
                                .child("Médias partagés"),
                        )
                        .child(
                            div()
                                .id("see-all-media")
                                .text_size(px(12.5))
                                .font_weight(FontWeight::BOLD)
                                .text_color(p.accent_text)
                                .cursor_pointer()
                                .child("Tout voir")
                                .on_click(cx.listener(|this, _, _, cx| {
                                    this.open_gallery(GalleryTab::Media, cx)
                                })),
                        ),
                )
                .child(if images.is_empty() {
                    div()
                        .text_size(px(12.5))
                        .text_color(p.text_faint)
                        .child("Aucune photo dans les messages chargés.")
                        .into_any_element()
                } else {
                    div()
                        .flex()
                        .flex_wrap()
                        .gap(px(6.))
                        .children(images.into_iter().enumerate().map(|(i, source)| {
                            let items = lightbox_items.clone();
                            div()
                                .id(("info-media", i))
                                .size(px(84.))
                                .rounded(px(10.))
                                .overflow_hidden()
                                .bg(p.chip_bg)
                                .cursor_pointer()
                                .on_click(cx.listener(move |this, _, _, cx| {
                                    this.open_lightbox(items.clone(), i, cx);
                                }))
                                .child(
                                    img(source)
                                        .size_full()
                                        .rounded(px(10.))
                                        .object_fit(ObjectFit::Cover),
                                )
                        }))
                        .into_any_element()
                })
                .child(
                    v_flex()
                        .pt_2()
                        .gap_1()
                        .child(gallery_entry(
                            "gallery-media",
                            IconName::Images,
                            "Photos et vidéos",
                            GalleryTab::Media,
                            p,
                            cx,
                        ))
                        .child(gallery_entry(
                            "gallery-files",
                            IconName::FileText,
                            "Fichiers",
                            GalleryTab::Files,
                            p,
                            cx,
                        ))
                        .child(gallery_entry(
                            "gallery-links",
                            IconName::Link,
                            "Liens",
                            GalleryTab::Links,
                            p,
                            cx,
                        ))
                        .child(download_entry)
                        .children(media_folder_entry),
                ),
        )
        .child(export_section)
        .child(
            v_flex()
                .px_4()
                .pt_2()
                .pb_6()
                .gap_1()
                .child(section_title("Confidentialité", p))
                .children(actions),
        )
        .into_any_element()
    }
}

fn toggle_switch(on: bool, p: &Palette) -> Div {
    div()
        .w(px(34.))
        .h(px(20.))
        .flex_none()
        .rounded_full()
        .p(px(2.))
        .flex()
        .when(on, |el| el.justify_end())
        .bg(if on { p.accent } else { p.border_strong })
        .child(div().size(px(16.)).rounded_full().bg(p.on_accent))
}

fn section_title(text: &'static str, p: &Palette) -> Div {
    div()
        .pb_1()
        .text_size(px(12.))
        .font_weight(FontWeight::BOLD)
        .text_color(p.text_muted)
        .child(text)
}

fn action_entry(
    id: &'static str,
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
        .py_2p5()
        .rounded(px(12.))
        .cursor_pointer()
        .hover(move |style| style.bg(hover))
        .text_size(px(13.5))
        .text_color(if danger { p.danger } else { p.text })
        .child(Icon::new(icon).size(px(16.)).text_color(if danger {
            p.danger
        } else {
            p.text_muted
        }))
        .child(div().flex_1().min_w_0().truncate().child(label.into()))
}

fn gallery_entry(
    id: &'static str,
    icon: IconName,
    label: &'static str,
    tab: GalleryTab,
    p: &Palette,
    cx: &mut Context<MissiveApp>,
) -> AnyElement {
    let hover = p.hover;
    h_flex()
        .id(id)
        .gap_3()
        .px_3()
        .py_2p5()
        .rounded(px(12.))
        .cursor_pointer()
        .hover(move |style| style.bg(hover))
        .text_size(px(13.5))
        .child(Icon::new(icon).size(px(16.)).text_color(p.text_muted))
        .child(div().flex_1().child(label))
        .child(
            Icon::new(IconName::ChevronRight)
                .size(px(15.))
                .text_color(p.text_faint),
        )
        .on_click(cx.listener(move |this, _, _, cx| this.open_gallery(tab, cx)))
        .into_any_element()
}

fn message_skeleton(p: &Palette) -> impl IntoElement {
    let rows: [(bool, f32, f32); 7] = [
        (false, 220., 38.),
        (false, 160., 38.),
        (true, 260., 38.),
        (false, 300., 58.),
        (true, 180., 38.),
        (true, 120., 38.),
        (false, 240., 38.),
    ];
    h_flex()
        .flex_1()
        .min_h_0()
        .justify_center()
        .bg(p.panel)
        .child(
            v_flex()
                .h_full()
                .w_full()
                .max_w(px(COLUMN_WIDTH))
                .justify_end()
                .gap(px(6.))
                .px(px(COLUMN_GUTTER))
                .pb_4()
                .children(rows.into_iter().map(|(outgoing, width, height)| {
                    h_flex()
                        .w_full()
                        .items_end()
                        .gap_2p5()
                        .when(outgoing, |el| el.justify_end())
                        .when(!outgoing, |el| {
                            el.child(Skeleton::new().size(px(36.)).rounded_full())
                        })
                        .child(
                            Skeleton::new()
                                .w(px(width))
                                .h(px(height))
                                .rounded(px(18.))
                                .when(outgoing, |el| el.secondary()),
                        )
                })),
        )
}

impl MissiveApp {
    fn render_members(
        &self,
        thread: &Thread,
        p: &Palette,
        cx: &mut Context<Self>,
    ) -> Option<AnyElement> {
        if !thread.is_group || thread.participants.is_empty() {
            return None;
        }
        let me = self.user.as_ref().map(|user| user.id.clone());
        let mut members: Vec<&crate::model::Participant> = thread.participants.iter().collect();
        members.sort_by_key(|participant| {
            (
                Some(&participant.user_id) == me.as_ref(),
                !participant.is_admin,
                !self.participant_online(participant),
                std::cmp::Reverse(participant.last_active_ms.unwrap_or(0)),
                participant.name.to_lowercase(),
            )
        });
        let manage = thread.can_manage;
        let rows: Vec<AnyElement> = members
            .into_iter()
            .enumerate()
            .map(|(index, participant)| {
                let is_me = Some(&participant.user_id) == me.as_ref();
                let online = !is_me && self.participant_online(participant);
                let status = if is_me {
                    Some("Vous".to_string())
                } else {
                    self.participant_status(participant)
                };
                let (thread_id, user_id) = (thread.id.clone(), participant.user_id.clone());
                h_flex()
                    .gap_3()
                    .px_2()
                    .py_1p5()
                    .child(super::widgets::person_avatar(
                        &participant.name,
                        self.avatar_source(participant.avatar_url.as_deref()),
                        34.,
                        online,
                        p.panel,
                        p,
                    ))
                    .child(
                        v_flex()
                            .min_w_0()
                            .flex_1()
                            .child(
                                h_flex()
                                    .min_w_0()
                                    .gap_1p5()
                                    .child(
                                        div()
                                            .min_w_0()
                                            .text_size(px(13.5))
                                            .font_weight(FontWeight::SEMIBOLD)
                                            .truncate()
                                            .child(SharedString::from(participant.name.clone())),
                                    )
                                    .when(participant.is_admin, |el| {
                                        el.child(
                                            div()
                                                .flex_none()
                                                .px_1p5()
                                                .rounded(px(6.))
                                                .bg(p.accent_soft)
                                                .text_size(px(10.5))
                                                .font_weight(FontWeight::BOLD)
                                                .text_color(p.accent_text)
                                                .child("Admin"),
                                        )
                                    }),
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
                    .when(manage && !is_me, |el| {
                        el.child(
                            icon_button(("member-menu", index), IconName::Ellipsis, false, p)
                                .on_click(cx.listener(move |this, event: &ClickEvent, _, cx| {
                                    this.open_member_menu(
                                        &thread_id,
                                        &user_id,
                                        event.position(),
                                        cx,
                                    )
                                })),
                        )
                    })
                    .into_any_element()
            })
            .collect();
        Some(
            v_flex()
                .px_4()
                .pt_5()
                .gap_1()
                .child(
                    div()
                        .pb_1()
                        .text_size(px(12.))
                        .font_weight(FontWeight::BOLD)
                        .text_color(p.text_muted)
                        .child(SharedString::from(format!(
                            "Membres · {}",
                            thread.participants.len()
                        ))),
                )
                .children(rows)
                .into_any_element(),
        )
    }
}
