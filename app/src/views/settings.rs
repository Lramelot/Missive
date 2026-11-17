use gpui_kit::assets::IconName;
use gpui_kit::component::{
    Icon, Sizable as _,
    button::{Button, ButtonVariants as _},
    h_flex,
    input::{Input, InputEvent, InputState, Textarea, TextareaState},
    switch::Switch,
    v_flex,
};
use gpui_kit::{prelude::FluentBuilder as _, *};

use super::widgets::icon_button;
use crate::{
    app::{History, MissiveApp},
    settings::{Settings, Snippet, TextSize, ThemePreference},
    style::Palette,
    updates::UpdateState,
};

type Toggle = fn(&mut Settings, bool);

const KEYWORD_MAX_CHARS: usize = 60;
const SHORTCUT_MAX_CHARS: usize = 24;

#[derive(Clone, Copy, PartialEq, Eq)]
enum QuietField {
    Start,
    End,
}

#[derive(Clone, Copy, PartialEq, Eq)]
enum Form {
    Keyword,
    Quiet,
    Snippet,
}

struct Field {
    input: Entity<InputState>,
    _subscription: Subscription,
}

struct SnippetEditor {
    index: Option<usize>,
    shortcut: Entity<InputState>,
    text: Entity<TextareaState>,
    _subscription: Subscription,
}

#[derive(Default)]
struct SettingsForms {
    keyword: Option<Field>,
    quiet: Option<(QuietField, Field)>,
    snippet: Option<SnippetEditor>,
    error: Option<(Form, SharedString)>,
}

impl Global for SettingsForms {}

impl SettingsForms {
    fn set_error(&mut self, form: Form, message: impl Into<SharedString>) {
        self.error = Some((form, message.into()));
    }

    fn clear_error(&mut self, form: Form) {
        if self.error.as_ref().is_some_and(|(f, _)| *f == form) {
            self.error = None;
        }
    }
}

#[derive(Clone, Default)]
struct FormsView {
    keyword: Option<Entity<InputState>>,
    quiet: Option<(QuietField, Entity<InputState>)>,
    snippet: Option<(Option<usize>, Entity<InputState>, Entity<TextareaState>)>,
    error: Option<(Form, SharedString)>,
}

impl FormsView {
    fn read(cx: &App) -> Self {
        let Some(forms) = cx.try_global::<SettingsForms>() else {
            return Self::default();
        };
        Self {
            keyword: forms.keyword.as_ref().map(|field| field.input.clone()),
            quiet: forms
                .quiet
                .as_ref()
                .map(|(which, field)| (*which, field.input.clone())),
            snippet: forms
                .snippet
                .as_ref()
                .map(|editor| (editor.index, editor.shortcut.clone(), editor.text.clone())),
            error: forms.error.clone(),
        }
    }

    fn error_for(&self, form: Form) -> Option<SharedString> {
        self.error
            .as_ref()
            .filter(|(f, _)| *f == form)
            .map(|(_, message)| message.clone())
    }
}

fn parse_time(raw: &str) -> Option<u16> {
    let text = raw.trim().to_lowercase().replace(['h', '.'], ":");
    let (hours, minutes) = match text.split_once(':') {
        Some((hours, minutes)) => (hours.trim(), minutes.trim()),
        None => (text.as_str(), ""),
    };
    if hours.is_empty() || hours.len() > 2 || !hours.chars().all(|c| c.is_ascii_digit()) {
        return None;
    }
    let minutes_ok = minutes.is_empty()
        || (minutes.len() == 2 && minutes.chars().all(|c| c.is_ascii_digit()));
    if !minutes_ok {
        return None;
    }
    let hours: u16 = hours.parse().ok()?;
    let minutes: u16 = if minutes.is_empty() {
        0
    } else {
        minutes.parse().ok()?
    };
    (hours < 24 && minutes < 60).then_some(hours * 60 + minutes)
}

fn format_time(minutes: u16) -> String {
    format!("{:02}:{:02}", (minutes / 60) % 24, minutes % 60)
}

fn normalize_keyword(raw: &str) -> Result<String, &'static str> {
    let keyword = raw.split_whitespace().collect::<Vec<_>>().join(" ");
    if keyword.is_empty() {
        return Err("Saisissez un mot ou une expression.");
    }
    if !keyword.chars().any(char::is_alphanumeric) {
        return Err("Le mot-clé doit contenir au moins une lettre ou un chiffre.");
    }
    if keyword.chars().count() > KEYWORD_MAX_CHARS {
        return Err("60 caractères au maximum.");
    }
    Ok(keyword)
}

fn validate_snippet(
    shortcut: &str,
    text: &str,
    existing: &[Snippet],
    index: Option<usize>,
) -> Result<Snippet, &'static str> {
    let shortcut = shortcut.trim();
    let shortcut = shortcut.strip_prefix('/').unwrap_or(shortcut);
    if shortcut.is_empty() {
        return Err("Indiquez un raccourci.");
    }
    if shortcut.chars().any(char::is_whitespace) {
        return Err("Le raccourci ne doit pas contenir d'espace.");
    }
    if shortcut.contains('/') {
        return Err("Le raccourci ne doit pas contenir de « / ».");
    }
    if shortcut.chars().count() > SHORTCUT_MAX_CHARS {
        return Err("24 caractères au maximum pour le raccourci.");
    }
    let taken = existing.iter().enumerate().any(|(i, snippet)| {
        Some(i) != index && snippet.shortcut.to_lowercase() == shortcut.to_lowercase()
    });
    if taken {
        return Err("Ce raccourci existe déjà.");
    }
    let text = text.trim();
    if text.is_empty() {
        return Err("Saisissez le texte à insérer.");
    }
    Ok(Snippet {
        shortcut: shortcut.to_string(),
        text: text.to_string(),
    })
}

fn snippet_preview(text: &str) -> String {
    let joined = text
        .lines()
        .map(str::trim)
        .filter(|line| !line.is_empty())
        .collect::<Vec<_>>()
        .join(" · ");
    if joined.chars().count() > 90 {
        let cut: String = joined.chars().take(89).collect();
        format!("{}…", cut.trim_end())
    } else {
        joined
    }
}

fn field_text(title: &'static str, description: &'static str, p: &Palette) -> Div {
    v_flex()
        .flex_1()
        .gap_0p5()
        .child(
            div()
                .text_size(px(14.))
                .font_weight(FontWeight::SEMIBOLD)
                .child(title),
        )
        .child(
            div()
                .text_size(px(12.5))
                .text_color(p.text_muted)
                .child(description),
        )
}

fn error_line(message: Option<SharedString>, p: &Palette) -> Option<Div> {
    message.map(|message| {
        div()
            .text_size(px(12.5))
            .text_color(p.danger)
            .child(message)
    })
}

impl MissiveApp {
    pub(super) fn settings_input_focused(&self, window: &Window, cx: &App) -> bool {
        let Some(forms) = cx.try_global::<SettingsForms>() else {
            return false;
        };
        let mut inputs = forms
            .keyword
            .iter()
            .map(|field| &field.input)
            .chain(forms.quiet.iter().map(|(_, field)| &field.input))
            .chain(forms.snippet.iter().map(|editor| &editor.shortcut));
        inputs
            .any(|input| input.read(cx).focus_handle(cx).is_focused(window))
            || forms
                .snippet
                .as_ref()
                .is_some_and(|editor| editor.text.read(cx).focus_handle(cx).is_focused(window))
    }

    fn open_keyword_field(&mut self, window: &mut Window, cx: &mut Context<Self>) {
        let existing = cx
            .try_global::<SettingsForms>()
            .and_then(|forms| forms.keyword.as_ref())
            .map(|field| field.input.clone());
        let input = match existing {
            Some(input) => input,
            None => {
                let input = cx.new(|cx| {
                    InputState::new(window, cx).placeholder("Mot ou expression")
                });
                let subscription =
                    cx.subscribe_in(&input, window, |this, _, event: &InputEvent, window, cx| {
                        if let InputEvent::PressEnter { .. } = event {
                            this.submit_keyword(window, cx);
                        }
                    });
                let forms = cx.default_global::<SettingsForms>();
                forms.keyword = Some(Field {
                    input: input.clone(),
                    _subscription: subscription,
                });
                forms.clear_error(Form::Keyword);
                input
            }
        };
        input.update(cx, |input, cx| input.focus(window, cx));
        cx.notify();
    }

    fn close_keyword_field(&mut self, cx: &mut Context<Self>) {
        let forms = cx.default_global::<SettingsForms>();
        forms.keyword = None;
        forms.clear_error(Form::Keyword);
        cx.notify();
    }

    fn submit_keyword(&mut self, window: &mut Window, cx: &mut Context<Self>) {
        let Some(input) = FormsView::read(cx).keyword else {
            return;
        };
        let raw = input.read(cx).value().to_string();
        let outcome = normalize_keyword(&raw).and_then(|keyword| {
            if self
                .settings
                .keywords
                .iter()
                .any(|existing| MissiveApp::keywords_equivalent(existing, &keyword))
            {
                Err("Ce mot-clé existe déjà.")
            } else {
                Ok(keyword)
            }
        });
        match outcome {
            Ok(keyword) => {
                self.update_settings(window, cx, |settings| settings.keywords.push(keyword));
                input.update(cx, |input, cx| input.set_value("", window, cx));
                cx.default_global::<SettingsForms>()
                    .clear_error(Form::Keyword);
            }
            Err(message) => cx
                .default_global::<SettingsForms>()
                .set_error(Form::Keyword, message),
        }
        cx.notify();
    }

    fn remove_keyword(&mut self, index: usize, window: &mut Window, cx: &mut Context<Self>) {
        self.update_settings(window, cx, |settings| {
            if index < settings.keywords.len() {
                settings.keywords.remove(index);
            }
        });
    }

    fn open_quiet_field(&mut self, which: QuietField, window: &mut Window, cx: &mut Context<Self>) {
        let current = match which {
            QuietField::Start => self.settings.quiet_hours.start_minutes,
            QuietField::End => self.settings.quiet_hours.end_minutes,
        };
        let input = cx.new(|cx| {
            InputState::new(window, cx)
                .placeholder("HH:MM")
                .default_value(format_time(current))
        });
        let subscription =
            cx.subscribe_in(&input, window, |this, _, event: &InputEvent, window, cx| {
                if let InputEvent::PressEnter { .. } = event {
                    this.submit_quiet(window, cx);
                }
            });
        input.update(cx, |input, cx| input.focus(window, cx));
        let forms = cx.default_global::<SettingsForms>();
        forms.quiet = Some((
            which,
            Field {
                input,
                _subscription: subscription,
            },
        ));
        forms.clear_error(Form::Quiet);
        cx.notify();
    }

    fn close_quiet_field(&mut self, cx: &mut Context<Self>) {
        let forms = cx.default_global::<SettingsForms>();
        forms.quiet = None;
        forms.clear_error(Form::Quiet);
        cx.notify();
    }

    fn submit_quiet(&mut self, window: &mut Window, cx: &mut Context<Self>) {
        let Some((which, input)) = FormsView::read(cx).quiet else {
            return;
        };
        let Some(minutes) = parse_time(&input.read(cx).value()) else {
            cx.default_global::<SettingsForms>()
                .set_error(Form::Quiet, "Heure invalide, par exemple 22:30.");
            cx.notify();
            return;
        };
        let quiet = &self.settings.quiet_hours;
        let other = match which {
            QuietField::Start => quiet.end_minutes,
            QuietField::End => quiet.start_minutes,
        };
        if minutes == other {
            cx.default_global::<SettingsForms>()
                .set_error(Form::Quiet, "Le début et la fin doivent être différents.");
            cx.notify();
            return;
        }
        self.update_settings(window, cx, |settings| match which {
            QuietField::Start => settings.quiet_hours.start_minutes = minutes,
            QuietField::End => settings.quiet_hours.end_minutes = minutes,
        });
        self.close_quiet_field(cx);
    }

    fn remove_priority_thread(&mut self, thread_id: &str, window: &mut Window, cx: &mut Context<Self>) {
        self.update_settings(window, cx, |settings| {
            settings.priority_threads.retain(|id| id != thread_id)
        });
    }

    fn open_snippet_editor(
        &mut self,
        index: Option<usize>,
        window: &mut Window,
        cx: &mut Context<Self>,
    ) {
        let current = index
            .and_then(|i| self.settings.snippets.get(i))
            .cloned()
            .unwrap_or_default();
        let shortcut = cx.new(|cx| {
            InputState::new(window, cx)
                .placeholder("raccourci, par exemple adresse")
                .default_value(current.shortcut.clone())
        });
        let text = cx.new(|cx| {
            TextareaState::new(window, cx)
                .auto_grow(3, 8)
                .placeholder("Texte inséré dans le message")
                .default_value(current.text.clone())
        });
        let subscription =
            cx.subscribe_in(&shortcut, window, |this, _, event: &InputEvent, window, cx| {
                if let InputEvent::PressEnter { .. } = event {
                    this.submit_snippet(window, cx);
                }
            });
        shortcut.update(cx, |input, cx| input.focus(window, cx));
        let forms = cx.default_global::<SettingsForms>();
        forms.snippet = Some(SnippetEditor {
            index: index.filter(|i| *i < self.settings.snippets.len()),
            shortcut,
            text,
            _subscription: subscription,
        });
        forms.clear_error(Form::Snippet);
        cx.notify();
    }

    fn close_snippet_editor(&mut self, cx: &mut Context<Self>) {
        let forms = cx.default_global::<SettingsForms>();
        forms.snippet = None;
        forms.clear_error(Form::Snippet);
        cx.notify();
    }

    fn submit_snippet(&mut self, window: &mut Window, cx: &mut Context<Self>) {
        let Some((index, shortcut, text)) = FormsView::read(cx).snippet else {
            return;
        };
        let index = index.filter(|i| *i < self.settings.snippets.len());
        let shortcut = shortcut.read(cx).value().to_string();
        let text = text.read(cx).value().to_string();
        match validate_snippet(&shortcut, &text, &self.settings.snippets, index) {
            Ok(snippet) => {
                self.update_settings(window, cx, |settings| match index {
                    Some(i) => settings.snippets[i] = snippet,
                    None => settings.snippets.push(snippet),
                });
                self.close_snippet_editor(cx);
            }
            Err(message) => {
                cx.default_global::<SettingsForms>()
                    .set_error(Form::Snippet, message);
                cx.notify();
            }
        }
    }

    fn delete_snippet(&mut self, index: usize, window: &mut Window, cx: &mut Context<Self>) {
        if cx
            .try_global::<SettingsForms>()
            .is_some_and(|forms| forms.snippet.is_some())
        {
            self.close_snippet_editor(cx);
        }
        self.update_settings(window, cx, |settings| {
            if index < settings.snippets.len() {
                settings.snippets.remove(index);
            }
        });
    }

    fn render_keywords(&self, forms: &FormsView, p: &Palette, cx: &mut Context<Self>) -> AnyElement {
        let hover = p.hover;
        let chips: Vec<AnyElement> = self
            .settings
            .keywords
            .iter()
            .enumerate()
            .map(|(index, keyword)| {
                h_flex()
                    .gap_1()
                    .pl_3()
                    .pr_1()
                    .h(px(30.))
                    .rounded(px(14.))
                    .bg(p.chip_bg)
                    .text_size(px(13.))
                    .text_color(p.text)
                    .child(SharedString::from(keyword.clone()))
                    .child(
                        div()
                            .id(("keyword-remove", index))
                            .size(px(22.))
                            .rounded(px(11.))
                            .flex()
                            .items_center()
                            .justify_center()
                            .cursor_pointer()
                            .hover(move |style| style.bg(hover))
                            .child(Icon::new(IconName::X).size(px(13.)).text_color(p.text_muted))
                            .on_click(cx.listener(move |this, _, window, cx| {
                                this.remove_keyword(index, window, cx)
                            })),
                    )
                    .into_any_element()
            })
            .collect();
        let editor: AnyElement = match &forms.keyword {
            Some(input) => v_flex()
                .gap_1p5()
                .child(
                    h_flex()
                        .gap_2()
                        .child(div().flex_1().child(Input::new(input).small()))
                        .child(
                            Button::new("keyword-submit")
                                .primary()
                                .small()
                                .label("Ajouter")
                                .on_click(cx.listener(|this, _, window, cx| {
                                    this.submit_keyword(window, cx)
                                })),
                        )
                        .child(
                            Button::new("keyword-close")
                                .ghost()
                                .small()
                                .label("Fermer")
                                .on_click(cx.listener(|this, _, _, cx| this.close_keyword_field(cx))),
                        ),
                )
                .children(error_line(forms.error_for(Form::Keyword), p))
                .into_any_element(),
            None => Button::new("keyword-open")
                .ghost()
                .small()
                .icon(IconName::Plus)
                .label("Ajouter un mot-clé")
                .on_click(cx.listener(|this, _, window, cx| this.open_keyword_field(window, cx)))
                .into_any_element(),
        };
        v_flex()
            .py_2()
            .gap_2()
            .child(field_text(
                "Mots-clés qui passent outre",
                "Un message contenant l'un de ces mots vous notifie même en sourdine ou pendant les heures calmes. Majuscules et accents sont ignorés.",
                p,
            ))
            .when(!chips.is_empty(), |el| {
                el.child(h_flex().flex_wrap().gap_1p5().children(chips))
            })
            .child(h_flex().child(editor))
            .into_any_element()
    }

    fn quiet_time(
        &self,
        which: QuietField,
        forms: &FormsView,
        p: &Palette,
        cx: &mut Context<Self>,
    ) -> AnyElement {
        let (id, minutes) = match which {
            QuietField::Start => ("quiet-start", self.settings.quiet_hours.start_minutes),
            QuietField::End => ("quiet-end", self.settings.quiet_hours.end_minutes),
        };
        if let Some((_, input)) = forms.quiet.as_ref().filter(|(w, _)| *w == which) {
            return h_flex()
                .gap_1p5()
                .child(div().w(px(84.)).child(Input::new(input).small()))
                .child(
                    Button::new("quiet-submit")
                        .primary()
                        .small()
                        .label("OK")
                        .on_click(cx.listener(|this, _, window, cx| this.submit_quiet(window, cx))),
                )
                .child(
                    Button::new("quiet-cancel")
                        .ghost()
                        .small()
                        .label("Annuler")
                        .on_click(cx.listener(|this, _, _, cx| this.close_quiet_field(cx))),
                )
                .into_any_element();
        }
        let hover = p.hover;
        div()
            .id(id)
            .px_3()
            .h(px(32.))
            .flex()
            .items_center()
            .rounded(px(12.))
            .bg(p.field_bg)
            .border_1()
            .border_color(p.divider)
            .text_size(px(13.5))
            .font_weight(FontWeight::SEMIBOLD)
            .text_color(p.text)
            .cursor_pointer()
            .hover(move |style| style.bg(hover))
            .child(SharedString::from(format_time(minutes)))
            .on_click(cx.listener(move |this, _, window, cx| {
                this.open_quiet_field(which, window, cx)
            }))
            .into_any_element()
    }

    fn render_quiet_hours(&self, forms: &FormsView, p: &Palette, cx: &mut Context<Self>) -> AnyElement {
        let enabled = self.settings.quiet_hours.enabled;
        let start = self.quiet_time(QuietField::Start, forms, p, cx);
        let end = self.quiet_time(QuietField::End, forms, p, cx);
        v_flex()
            .child(self.setting_row(
                "quiet-hours",
                "Heures calmes",
                "Aucune notification sur cette plage, sauf conversations prioritaires, mentions et mots-clés.",
                enabled,
                |s, v| s.quiet_hours.enabled = v,
                p,
                cx,
            ))
            .when(enabled, |el| {
                el.child(
                    v_flex()
                        .pb_2()
                        .gap_1p5()
                        .child(
                            h_flex()
                                .gap_2()
                                .text_size(px(13.))
                                .text_color(p.text_muted)
                                .child("De")
                                .child(start)
                                .child("à")
                                .child(end)
                                .child(div().text_color(p.text_faint).child("heure locale")),
                        )
                        .children(error_line(forms.error_for(Form::Quiet), p)),
                )
            })
            .into_any_element()
    }

    fn render_priority_threads(&self, p: &Palette, cx: &mut Context<Self>) -> AnyElement {
        let rows: Vec<AnyElement> = self
            .settings
            .priority_threads
            .iter()
            .enumerate()
            .map(|(index, thread_id)| {
                let name = self
                    .threads
                    .iter()
                    .find(|thread| thread.id == *thread_id)
                    .map(|thread| thread.name.clone())
                    .filter(|name| !name.trim().is_empty())
                    .unwrap_or_else(|| "Conversation indisponible".to_string());
                let id = thread_id.clone();
                h_flex()
                    .gap_3()
                    .child(
                        div()
                            .flex_1()
                            .min_w_0()
                            .truncate()
                            .text_size(px(13.5))
                            .child(SharedString::from(name)),
                    )
                    .child(
                        icon_button(("priority-remove", index), IconName::X, false, p).on_click(
                            cx.listener(move |this, _, window, cx| {
                                this.remove_priority_thread(&id, window, cx)
                            }),
                        ),
                    )
                    .into_any_element()
            })
            .collect();
        v_flex()
            .py_2()
            .gap_1()
            .child(field_text(
                "Conversations prioritaires",
                "Elles vous notifient même pendant les heures calmes. Ajoutez-en depuis le menu d'une conversation.",
                p,
            ))
            .when(rows.is_empty(), |el| {
                el.child(
                    div()
                        .pt_1()
                        .text_size(px(12.5))
                        .text_color(p.text_faint)
                        .child("Aucune conversation prioritaire."),
                )
            })
            .children(rows)
            .into_any_element()
    }

    fn render_snippets(&self, forms: &FormsView, p: &Palette, cx: &mut Context<Self>) -> AnyElement {
        let editing_index = forms.snippet.as_ref().and_then(|(index, _, _)| *index);
        let rows: Vec<AnyElement> = self
            .settings
            .snippets
            .iter()
            .enumerate()
            .filter(|(index, _)| Some(*index) != editing_index)
            .map(|(index, snippet)| {
                h_flex()
                    .py_1()
                    .gap_2()
                    .child(
                        v_flex()
                            .flex_1()
                            .min_w_0()
                            .gap_0p5()
                            .child(
                                div()
                                    .text_size(px(14.))
                                    .font_weight(FontWeight::SEMIBOLD)
                                    .text_color(p.accent_text)
                                    .truncate()
                                    .child(SharedString::from(format!("/{}", snippet.shortcut))),
                            )
                            .child(
                                div()
                                    .text_size(px(12.5))
                                    .text_color(p.text_muted)
                                    .truncate()
                                    .child(SharedString::from(snippet_preview(&snippet.text))),
                            ),
                    )
                    .child(
                        icon_button(("snippet-edit", index), IconName::Pencil, false, p).on_click(
                            cx.listener(move |this, _, window, cx| {
                                this.open_snippet_editor(Some(index), window, cx)
                            }),
                        ),
                    )
                    .child(
                        icon_button(("snippet-delete", index), IconName::Trash, false, p).on_click(
                            cx.listener(move |this, _, window, cx| {
                                this.delete_snippet(index, window, cx)
                            }),
                        ),
                    )
                    .into_any_element()
            })
            .collect();
        let editor: AnyElement = match &forms.snippet {
            Some((index, shortcut, text)) => v_flex()
                .mt_1()
                .gap_2()
                .p_4()
                .rounded(px(14.))
                .border_1()
                .border_color(p.divider)
                .bg(p.raised)
                .child(
                    div()
                        .text_size(px(14.))
                        .font_weight(FontWeight::SEMIBOLD)
                        .child(if index.is_some() {
                            "Modifier la réponse rapide"
                        } else {
                            "Nouvelle réponse rapide"
                        }),
                )
                .child(
                    div()
                        .text_size(px(12.5))
                        .text_color(p.text_muted)
                        .child("Raccourci, sans espace"),
                )
                .child(
                    h_flex()
                        .gap_2()
                        .child(
                            div()
                                .text_size(px(15.))
                                .font_weight(FontWeight::SEMIBOLD)
                                .text_color(p.text_muted)
                                .child("/"),
                        )
                        .child(div().flex_1().child(Input::new(shortcut).small())),
                )
                .child(
                    div()
                        .text_size(px(12.5))
                        .text_color(p.text_muted)
                        .child("Texte, sur plusieurs lignes si besoin"),
                )
                .child(Textarea::new(text))
                .children(error_line(forms.error_for(Form::Snippet), p))
                .child(
                    h_flex()
                        .pt_1()
                        .justify_end()
                        .gap_2()
                        .child(
                            Button::new("snippet-cancel")
                                .ghost()
                                .small()
                                .label("Annuler")
                                .on_click(cx.listener(|this, _, _, cx| this.close_snippet_editor(cx))),
                        )
                        .child(
                            Button::new("snippet-save")
                                .primary()
                                .small()
                                .label("Enregistrer")
                                .on_click(cx.listener(|this, _, window, cx| {
                                    this.submit_snippet(window, cx)
                                })),
                        ),
                )
                .into_any_element(),
            None => h_flex()
                .pt_1()
                .child(
                    Button::new("snippet-add")
                        .ghost()
                        .small()
                        .icon(IconName::Plus)
                        .label("Ajouter une réponse rapide")
                        .on_click(cx.listener(|this, _, window, cx| {
                            this.open_snippet_editor(None, window, cx)
                        })),
                )
                .into_any_element(),
        };
        v_flex()
            .gap_1()
            .child(
                div()
                    .pb_1()
                    .text_size(px(12.5))
                    .text_color(p.text_muted)
                    .child("Tapez « / » suivi du raccourci dans un message pour insérer le texte."),
            )
            .when(rows.is_empty() && forms.snippet.is_none(), |el| {
                el.child(
                    div()
                        .text_size(px(12.5))
                        .text_color(p.text_faint)
                        .child("Aucune réponse rapide pour le moment."),
                )
            })
            .children(rows)
            .child(editor)
            .into_any_element()
    }

    fn render_privacy(&self, p: &Palette, cx: &mut Context<Self>) -> AnyElement {
        v_flex()
            .child(self.setting_row(
                "read-receipts",
                "Envoyer les confirmations de lecture",
                "Désactivé, vous lisez en toute discrétion : l'expéditeur ne voit pas que vous avez lu. Répondre ou choisir « Marquer comme lu » le lui indique.",
                self.settings.send_read_receipts,
                |s, v| s.send_read_receipts = v,
                p,
                cx,
            ))
            .child(self.setting_row(
                "send-typing",
                "Indiquer quand j'écris",
                "Vos contacts voient que vous êtes en train d'écrire.",
                self.settings.send_typing,
                |s, v| s.send_typing = v,
                p,
                cx,
            ))
            .into_any_element()
    }

    fn render_appearance(&self, p: &Palette, cx: &mut Context<Self>) -> AnyElement {
        let sizes: Vec<AnyElement> = [
            ("text-small", "Petit", TextSize::Small),
            ("text-normal", "Normal", TextSize::Normal),
            ("text-large", "Grand", TextSize::Large),
            ("text-larger", "Très grand", TextSize::Larger),
        ]
        .into_iter()
        .map(|(id, label, value)| {
            let active = self.settings.text_size == value;
            let hover = p.hover;
            div()
                .id(id)
                .flex_1()
                .h(px(36.))
                .flex()
                .items_center()
                .justify_center()
                .rounded(px(10.))
                .text_size(px(13. * value.scale()))
                .cursor_pointer()
                .when(active, |el| {
                    el.bg(p.raised)
                        .border_1()
                        .border_color(p.divider)
                        .font_weight(FontWeight::SEMIBOLD)
                        .text_color(p.text)
                })
                .when(!active, |el| {
                    el.text_color(p.text_muted).hover(move |s| s.bg(hover))
                })
                .child(label)
                .on_click(cx.listener(move |this, _, window, cx| {
                    this.set_text_size(value, window, cx)
                }))
                .into_any_element()
        })
        .collect();
        let view = cx.entity();
        v_flex()
            .child(
                v_flex()
                    .pt_3()
                    .pb_2()
                    .gap_2()
                    .child(field_text(
                        "Taille du texte",
                        "Messages, noms et zone de saisie.",
                        p,
                    ))
                    .child(
                        h_flex()
                            .p_1()
                            .gap_1()
                            .rounded(px(12.))
                            .bg(p.field_bg)
                            .children(sizes),
                    ),
            )
            .child(
                h_flex()
                    .py_2()
                    .gap_4()
                    .child(field_text(
                        "Affichage compact",
                        "Resserre l'espacement entre les messages.",
                        p,
                    ))
                    .child(
                        Switch::new("compact")
                            .checked(self.settings.compact)
                            .on_click(move |value, window, cx| {
                                let value = *value;
                                view.update(cx, |this, cx| this.set_compact(value, window, cx));
                            }),
                    ),
            )
            .into_any_element()
    }

    fn render_diagnostic(&self, p: &Palette, cx: &mut Context<Self>) -> AnyElement {
        v_flex()
            .py_2()
            .gap_3()
            .child(field_text(
                "Journal de diagnostic",
                "Version, système et journal récent de l'application, sans le contenu de vos messages. Utile pour signaler un problème.",
                p,
            ))
            .child(
                h_flex()
                    .flex_wrap()
                    .gap_2()
                    .child(
                        Button::new("copy-diagnostic")
                            .outline()
                            .small()
                            .icon(IconName::Copy)
                            .label("Copier le diagnostic")
                            .on_click(cx.listener(|this, _, _, cx| this.copy_diagnostic(cx))),
                    )
                    .child(
                        Button::new("open-logs")
                            .ghost()
                            .small()
                            .icon(IconName::FolderOpen)
                            .label("Ouvrir le dossier des journaux")
                            .on_click(cx.listener(|this, _, _, cx| this.open_logs_folder(cx))),
                    ),
            )
            .into_any_element()
    }
}

impl MissiveApp {
    pub(super) fn render_settings(&self, p: &Palette, cx: &mut Context<Self>) -> impl IntoElement {
        let section = |title: &'static str| {
            div()
                .pt_5()
                .pb_2()
                .text_size(px(12.))
                .font_weight(FontWeight::BOLD)
                .text_color(p.text_faint)
                .child(title)
        };
        let themes: Vec<AnyElement> = [
            ("theme-system", "Système", ThemePreference::System),
            ("theme-light", "Clair", ThemePreference::Light),
            ("theme-dark", "Sombre", ThemePreference::Dark),
        ]
        .into_iter()
        .map(|(id, label, value)| {
            let active = self.settings.theme == value;
            let hover = p.hover;
            div()
                .id(id)
                .flex_1()
                .h(px(36.))
                .flex()
                .items_center()
                .justify_center()
                .rounded(px(10.))
                .text_size(px(13.))
                .cursor_pointer()
                .when(active, |el| {
                    el.bg(p.raised)
                        .border_1()
                        .border_color(p.divider)
                        .font_weight(FontWeight::SEMIBOLD)
                        .text_color(p.text)
                })
                .when(!active, |el| {
                    el.text_color(p.text_muted).hover(move |s| s.bg(hover))
                })
                .child(label)
                .on_click(cx.listener(move |this, _, window, cx| this.set_theme(value, window, cx)))
                .into_any_element()
        })
        .collect();
        let version = crate::updates::current_version();
        let managed = crate::updates::is_managed();
        let local_archived = self.locally_archived_count();
        let forms = FormsView::read(cx);
        let keywords = self.render_keywords(&forms, p, cx);
        let quiet_hours = self.render_quiet_hours(&forms, p, cx);
        let priority = self.render_priority_threads(p, cx);
        let privacy = self.render_privacy(p, cx);
        let snippets = self.render_snippets(&forms, p, cx);
        let appearance = self.render_appearance(p, cx);
        let diagnostic = self.render_diagnostic(p, cx);

        div()
            .absolute()
            .inset_0()
            .flex()
            .items_center()
            .justify_center()
            .bg(hsla(0., 0., 0., 0.4))
            .child(
                v_flex()
                    .id("settings")
                    .w(px(520.))
                    .max_h(relative(0.9))
                    .rounded(px(18.))
                    .border_1()
                    .border_color(p.divider)
                    .bg(p.panel)
                    .shadow_lg()
                    .overflow_y_scroll()
                    .px_7()
                    .pt_6()
                    .pb_7()
                    .on_mouse_down(MouseButton::Left, |_, _, cx| cx.stop_propagation())
                    .child(
                        h_flex()
                            .justify_between()
                            .child(
                                div()
                                    .text_size(px(21.))
                                    .font_weight(FontWeight::EXTRA_BOLD)
                                    .child("Paramètres"),
                            )
                            .child(
                                icon_button("close-settings", IconName::X, false, p).on_click(
                                    cx.listener(|this, _, _, cx| this.toggle_settings(cx)),
                                ),
                            ),
                    )
                    .child(section("NOTIFICATIONS"))
                    .child(self.setting_row(
                        "notifications",
                        "Notifications de bureau",
                        "Une notification pour chaque nouveau message.",
                        self.settings.notifications,
                        |s, v| s.notifications = v,
                        p,
                        cx,
                    ))
                    .child(self.setting_row(
                        "preview",
                        "Afficher le contenu",
                        "Sinon, la notification indique seulement « Nouveau message ».",
                        self.settings.notification_preview,
                        |s, v| s.notification_preview = v,
                        p,
                        cx,
                    ))
                    .child(keywords)
                    .child(quiet_hours)
                    .child(priority)
                    .child(section("CONFIDENTIALITÉ"))
                    .child(privacy)
                    .child(section("RÉPONSES RAPIDES"))
                    .child(snippets)
                    .child(section("FENÊTRE"))
                    .child(self.setting_row(
                        "tray",
                        "Garder Missive dans la zone de notification",
                        "Fermer la fenêtre la masque au lieu de quitter.",
                        self.settings.close_to_tray,
                        |s, v| s.close_to_tray = v,
                        p,
                        cx,
                    ))
                    .child(self.setting_row(
                        "autostart",
                        "Lancer au démarrage",
                        "Missive démarre en arrière-plan à l'ouverture de session.",
                        self.settings.launch_at_login,
                        |s, v| s.launch_at_login = v,
                        p,
                        cx,
                    ))
                    .child(section("APPARENCE"))
                    .child(
                        h_flex()
                            .p_1()
                            .gap_1()
                            .rounded(px(12.))
                            .bg(p.field_bg)
                            .children(themes),
                    )
                    .child(appearance)
                    .child(section("DISCUSSIONS"))
                    .child(
                        h_flex()
                            .py_2()
                            .gap_4()
                            .child(
                                v_flex()
                                    .flex_1()
                                    .gap_0p5()
                                    .child(
                                        div()
                                            .text_size(px(14.))
                                            .font_weight(FontWeight::SEMIBOLD)
                                            .child("Archivées dans Missive"),
                                    )
                                    .child(
                                        div()
                                            .text_size(px(12.5))
                                            .text_color(p.text_muted)
                                            .child(SharedString::from(match local_archived {
                                                0 => "Aucune discussion archivée localement.".to_string(),
                                                1 => "1 discussion masquée de « Toutes » jusqu'au prochain message.".to_string(),
                                                n => format!("{n} discussions masquées de « Toutes » jusqu'au prochain message."),
                                            })),
                                    ),
                            )
                            .when(local_archived > 0, |el| {
                                el.child(
                                    Button::new("unarchive-all")
                                        .outline()
                                        .small()
                                        .label("Tout désarchiver")
                                        .on_click(cx.listener(|this, _, _, cx| {
                                            this.unarchive_all_local(cx)
                                        })),
                                )
                            }),
                    )
                    .child(section("MISES À JOUR"))
                    .child(self.setting_row(
                        "updates",
                        "Vérifier automatiquement",
                        "Interroge les versions publiées au démarrage.",
                        self.settings.check_updates,
                        |s, v| s.check_updates = v,
                        p,
                        cx,
                    ))
                    .child(
                        h_flex()
                            .pt_2()
                            .gap_3()
                            .text_size(px(12.5))
                            .text_color(p.text_muted)
                            .child(SharedString::from(format!("Version {version}")))
                            .when(!managed, |el| {
                                el.child(
                                    div().text_color(p.text_faint).child(
                                        "· hors installation, pas de mise à jour automatique",
                                    ),
                                )
                            })
                            .when_some(self.update.clone(), |el, update| match update {
                                UpdateState::Downloading { version } => el.child(
                                    div().text_color(p.text_muted).child(SharedString::from(
                                        format!("Téléchargement de la version {version}…"),
                                    )),
                                ),
                                UpdateState::Ready { version, .. } => {
                                    el.child(
                                        Button::new("restart-update")
                                            .primary()
                                            .small()
                                            .label(SharedString::from(format!(
                                                "Redémarrer pour passer en {version}"
                                            )))
                                            .on_click(cx.listener(|this, _, _, cx| {
                                                this.restart_to_update(cx)
                                            })),
                                    )
                                }
                            })
                            .when(managed && self.update.is_none(), |el| {
                                el.child(
                                    div()
                                        .id("check-update")
                                        .text_color(p.accent_text)
                                        .font_weight(FontWeight::SEMIBOLD)
                                        .cursor_pointer()
                                        .child("Rechercher maintenant")
                                        .on_click(
                                            cx.listener(|this, _, _, cx| this.check_for_update(cx)),
                                        ),
                                )
                            }),
                    )
                    .child(section("DIAGNOSTIC"))
                    .child(diagnostic)
                    .child(section("COMPTE"))
                    .child(
                        h_flex()
                            .gap_2()
                            .when(self.history == History::Available, |el| {
                                el.child(
                                    Button::new("settings-restore")
                                        .outline()
                                        .label("Restaurer l'historique chiffré")
                                        .on_click(cx.listener(|this, _, window, cx| {
                                            this.settings_open = false;
                                            this.open_pin(window, cx);
                                        })),
                                )
                            })
                            .child(
                                Button::new("logout")
                                    .danger()
                                    .label("Se déconnecter")
                                    .on_click(
                                        cx.listener(|this, _, window, cx| this.logout(window, cx)),
                                    ),
                            ),
                    ),
            )
    }

    #[allow(clippy::too_many_arguments)]
    fn setting_row(
        &self,
        id: &'static str,
        title: &'static str,
        description: &'static str,
        checked: bool,
        toggle: Toggle,
        p: &Palette,
        cx: &mut Context<Self>,
    ) -> AnyElement {
        let view = cx.entity();
        h_flex()
            .py_2()
            .gap_4()
            .child(
                v_flex()
                    .flex_1()
                    .min_w_0()
                    .gap_0p5()
                    .child(
                        div()
                            .text_size(px(14.))
                            .font_weight(FontWeight::SEMIBOLD)
                            .child(title),
                    )
                    .child(
                        div()
                            .w_full()
                            .text_size(px(12.5))
                            .text_color(p.text_muted)
                            .child(description),
                    ),
            )
            .child(
                Switch::new(id)
                    .checked(checked)
                    .on_click(move |value, window, cx| {
                        let value = *value;
                        view.update(cx, |this, cx| {
                            this.update_settings(window, cx, |settings| toggle(settings, value));
                        });
                    }),
            )
            .into_any_element()
    }
}

#[cfg(test)]
mod tests {
    use super::{format_time, normalize_keyword, parse_time, snippet_preview, validate_snippet};
    use crate::settings::Snippet;

    fn snippet(shortcut: &str) -> Snippet {
        Snippet {
            shortcut: shortcut.into(),
            text: "texte".into(),
        }
    }

    #[::core::prelude::v1::test]
    fn parses_common_time_formats() {
        assert_eq!(parse_time("22:30"), Some(22 * 60 + 30));
        assert_eq!(parse_time("7"), Some(7 * 60));
        assert_eq!(parse_time("07:05"), Some(7 * 60 + 5));
        assert_eq!(parse_time("7h"), Some(7 * 60));
        assert_eq!(parse_time("7h30"), Some(7 * 60 + 30));
        assert_eq!(parse_time(" 0:00 "), Some(0));
    }

    #[::core::prelude::v1::test]
    fn rejects_invalid_times() {
        for raw in ["", "24:00", "12:60", "7:5", "abc", "123", "7:30:00", "-1"] {
            assert_eq!(parse_time(raw), None, "{raw}");
        }
    }

    #[::core::prelude::v1::test]
    fn formats_minutes_as_clock() {
        assert_eq!(format_time(22 * 60), "22:00");
        assert_eq!(format_time(7 * 60 + 5), "07:05");
        assert_eq!(parse_time(&format_time(13 * 60 + 45)), Some(13 * 60 + 45));
    }

    #[::core::prelude::v1::test]
    fn keyword_is_trimmed_and_validated() {
        assert_eq!(normalize_keyword("  appel   d'offres "), Ok("appel d'offres".into()));
        assert!(normalize_keyword("   ").is_err());
        assert!(normalize_keyword("!!!").is_err());
        assert!(normalize_keyword(&"a".repeat(61)).is_err());
    }

    #[::core::prelude::v1::test]
    fn snippet_shortcut_rules() {
        let existing = vec![snippet("adresse"), snippet("merci")];
        assert_eq!(
            validate_snippet("/rdv", " À demain ! \n", &existing, None),
            Ok(Snippet {
                shortcut: "rdv".into(),
                text: "À demain !".into()
            })
        );
        assert!(validate_snippet("", "x", &existing, None).is_err());
        assert!(validate_snippet("mon rdv", "x", &existing, None).is_err());
        assert!(validate_snippet("a/b", "x", &existing, None).is_err());
        assert!(validate_snippet("rdv", "   ", &existing, None).is_err());
        assert!(validate_snippet("Merci", "x", &existing, None).is_err());
        assert!(validate_snippet("merci", "x", &existing, Some(1)).is_ok());
    }

    #[::core::prelude::v1::test]
    fn snippet_text_keeps_inner_lines() {
        let result = validate_snippet("sig", "Cordialement,\nCamille", &[], None).unwrap();
        assert_eq!(result.text, "Cordialement,\nCamille");
    }

    #[::core::prelude::v1::test]
    fn snippet_preview_joins_lines_and_truncates() {
        assert_eq!(snippet_preview("Bonjour\n\n  à tous "), "Bonjour · à tous");
        let long = "x".repeat(200);
        let preview = snippet_preview(&long);
        assert!(preview.ends_with('…'));
        assert_eq!(preview.chars().count(), 90);
    }
}
