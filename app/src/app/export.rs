use std::path::PathBuf;

use gpui_kit::*;
use serde::Deserialize;
use serde_json::{Value, json};

use super::{MissiveApp, action_error};
use crate::bridge::BridgeError;

#[derive(Clone, Copy, PartialEq, Eq)]
pub(crate) enum ExportFormat {
    Html,
    Markdown,
    Json,
}

impl ExportFormat {
    pub(crate) const ALL: [ExportFormat; 3] =
        [ExportFormat::Html, ExportFormat::Markdown, ExportFormat::Json];

    fn bridge(self) -> &'static str {
        match self {
            ExportFormat::Html => "html",
            ExportFormat::Markdown => "markdown",
            ExportFormat::Json => "json",
        }
    }

    pub(crate) fn label(self) -> SharedString {
        match self {
            ExportFormat::Html => ts!("export.format.html.label"),
            ExportFormat::Markdown => ts!("export.format.markdown.label"),
            ExportFormat::Json => ts!("export.format.json.label"),
        }
    }

    pub(crate) fn hint(self) -> SharedString {
        match self {
            ExportFormat::Html => ts!("export.format.html.hint"),
            ExportFormat::Markdown => ts!("export.format.markdown.hint"),
            ExportFormat::Json => ts!("export.format.json.hint"),
        }
    }
}

#[derive(Clone, Deserialize)]
pub(crate) struct ExportOutcome {
    #[serde(default)]
    pub(crate) path: String,
    #[serde(default)]
    pub(crate) messages: i64,
    #[serde(default)]
    pub(crate) media: i64,
    #[serde(default)]
    pub(crate) media_failed: i64,
    #[serde(default)]
    cancelled: bool,
}

#[derive(Clone, Deserialize)]
pub(crate) struct SaveMediaOutcome {
    #[serde(default)]
    pub(crate) saved: i64,
    #[serde(default)]
    pub(crate) failed: i64,
    #[serde(default)]
    pub(crate) dir: String,
    #[serde(default)]
    cancelled: bool,
}

#[derive(Debug, PartialEq, Eq)]
enum Ending<T> {
    Done(T),
    Cancelled,
    Busy,
    Failed(String),
}

fn classify<T>(result: anyhow::Result<T>, cancelled: impl Fn(&T) -> bool) -> Ending<T> {
    match result {
        Ok(value) if cancelled(&value) => Ending::Cancelled,
        Ok(value) => Ending::Done(value),
        Err(err) => match err.downcast_ref::<BridgeError>() {
            Some(error) if error.code == "cancelled" => Ending::Cancelled,
            Some(error) if error.code == "busy" => Ending::Busy,
            _ => Ending::Failed(action_error(&err)),
        },
    }
}

pub(crate) struct ExportDialog {
    pub(crate) thread_id: String,
    pub(crate) format: ExportFormat,
    pub(crate) include_media: bool,
    pub(crate) dir: Option<PathBuf>,
}

pub(crate) struct ExportJob {
    pub(crate) thread_id: String,
    pub(crate) thread_name: String,
    pub(crate) messages: i64,
    pub(crate) media: i64,
    pub(crate) result: Option<Result<ExportOutcome, SharedString>>,
}

impl ExportJob {
    pub(crate) fn progress_label(&self) -> String {
        format!("{}, {}", message_count(self.messages), media_count(self.media))
    }
}

impl ExportOutcome {
    pub(crate) fn summary_label(&self) -> String {
        let mut summary = format!(
            "{}, {}",
            message_count(self.messages),
            media_count(self.media)
        );
        if self.media_failed > 0 {
            summary.push_str(" · ");
            summary.push_str(&tn!(
                "export.not_downloaded",
                self.media_failed,
                grouped = group_digits(self.media_failed)
            ));
        }
        summary
    }
}

impl MediaSave {
    pub(crate) fn progress_label(&self) -> String {
        match self.total {
            Some(total) if total > 0 => format!(
                "{} / {}",
                group_digits(self.saved),
                media_count(total)
            ),
            _ => media_count(self.saved),
        }
    }
}

pub(crate) struct MediaSave {
    pub(crate) thread_id: String,
    pub(crate) saved: i64,
    pub(crate) total: Option<i64>,
    pub(crate) result: Option<Result<SaveMediaOutcome, SharedString>>,
}

#[derive(Default)]
pub(crate) struct Exports {
    pub(crate) dialog: Option<ExportDialog>,
    pub(crate) job: Option<ExportJob>,
    pub(crate) saves: Vec<MediaSave>,
    cancelling: bool,
    last_dir: Option<PathBuf>,
}

fn busy_toast() -> SharedString {
    ts!("export.busy")
}

fn int(data: &Value, key: &str) -> Option<i64> {
    data.get(key).and_then(Value::as_i64)
}

fn group_digits(value: i64) -> String {
    let digits = value.unsigned_abs().to_string();
    let separator = if crate::i18n::is_french() { '\u{a0}' } else { ',' };
    let mut out = String::new();
    for (index, c) in digits.chars().enumerate() {
        if index > 0 && (digits.len() - index).is_multiple_of(3) {
            out.push(separator);
        }
        out.push(c);
    }
    if value < 0 { format!("-{out}") } else { out }
}

pub(crate) fn message_count(value: i64) -> String {
    tn!("export.messages", value, grouped = group_digits(value)).into_owned()
}

pub(crate) fn media_count(value: i64) -> String {
    tn!("export.media", value, grouped = group_digits(value)).into_owned()
}

pub(crate) fn thread_display(app: &MissiveApp, thread_id: &str) -> String {
    app.threads
        .iter()
        .find(|thread| thread.id == thread_id)
        .map(|thread| thread.name.trim().to_string())
        .filter(|name| !name.is_empty())
        .unwrap_or_else(|| t!("export.default_thread_name").into_owned())
}

impl MissiveApp {
    pub(crate) fn on_export_event(&mut self, name: &str, data: Value, cx: &mut Context<Self>) {
        let Some(thread_id) = data.get("thread_id").and_then(Value::as_str) else {
            return;
        };
        match name {
            "export_progress" => {
                if let Some(job) = self
                    .exports
                    .job
                    .as_mut()
                    .filter(|job| job.thread_id == thread_id && job.result.is_none())
                {
                    job.messages = int(&data, "messages").unwrap_or(job.messages);
                    job.media = int(&data, "media").unwrap_or(job.media);
                    cx.notify();
                }
            }
            "save_media_progress" => {
                if let Some(save) = self
                    .exports
                    .saves
                    .iter_mut()
                    .find(|save| save.thread_id == thread_id && save.result.is_none())
                {
                    save.saved = int(&data, "saved").unwrap_or(save.saved);
                    save.total = int(&data, "total").or(save.total);
                    cx.notify();
                }
            }
            _ => {}
        }
    }

    pub(crate) fn export_format_options(&self) -> Vec<(ExportFormat, bool)> {
        let current = self.exports.dialog.as_ref().map(|dialog| dialog.format);
        ExportFormat::ALL
            .into_iter()
            .map(|format| (format, Some(format) == current))
            .collect()
    }

    pub(crate) fn export_running(&self) -> bool {
        self.exports
            .job
            .as_ref()
            .is_some_and(|job| job.result.is_none())
    }

    pub(crate) fn open_export(&mut self, thread_id: &str, cx: &mut Context<Self>) {
        self.thread_ops.menu = None;
        if self
            .exports
            .job
            .as_ref()
            .is_some_and(|job| job.result.is_some() && job.thread_id != thread_id)
        {
            self.exports.job = None;
        }
        let dir = self.exports.last_dir.clone().or_else(dirs::document_dir);
        self.exports.dialog = Some(ExportDialog {
            thread_id: thread_id.to_string(),
            format: ExportFormat::Html,
            include_media: true,
            dir,
        });
        cx.notify();
    }

    pub(crate) fn close_export(&mut self, cx: &mut Context<Self>) {
        self.exports.dialog = None;
        if self
            .exports
            .job
            .as_ref()
            .is_some_and(|job| job.result.is_some())
        {
            self.exports.job = None;
        }
        cx.notify();
    }

    pub(crate) fn set_export_format(&mut self, format: ExportFormat, cx: &mut Context<Self>) {
        if let Some(dialog) = self.exports.dialog.as_mut() {
            dialog.format = format;
            cx.notify();
        }
    }

    pub(crate) fn toggle_export_media(&mut self, cx: &mut Context<Self>) {
        if let Some(dialog) = self.exports.dialog.as_mut() {
            dialog.include_media = !dialog.include_media;
            cx.notify();
        }
    }

    pub(crate) fn pick_export_dir(&mut self, cx: &mut Context<Self>) {
        let receiver = cx.prompt_for_paths(PathPromptOptions {
            files: false,
            directories: true,
            multiple: false,
            prompt: Some(ts!("export.pick_folder_prompt")),
        });
        cx.spawn(async move |this, cx| {
            let Ok(Ok(Some(paths))) = receiver.await else {
                return;
            };
            let Some(path) = paths.into_iter().next() else {
                return;
            };
            let _ = this.update(cx, |this, cx| {
                this.exports.last_dir = Some(path.clone());
                if let Some(dialog) = this.exports.dialog.as_mut() {
                    dialog.dir = Some(path);
                }
                cx.notify();
            });
        })
        .detach();
    }

    pub(crate) fn export_enter(&mut self, cx: &mut Context<Self>) {
        let job_for_dialog = match (&self.exports.dialog, &self.exports.job) {
            (Some(dialog), Some(job)) => job.thread_id == dialog.thread_id,
            _ => false,
        };
        if job_for_dialog {
            if !self.export_running() {
                self.close_export(cx);
            }
            return;
        }
        self.start_export(cx);
    }

    pub(crate) fn start_export(&mut self, cx: &mut Context<Self>) {
        if self.archive_busy() {
            self.show_toast(busy_toast(), cx);
            return;
        }
        let Some(dialog) = self.exports.dialog.as_ref() else {
            return;
        };
        let Some(dir) = dialog.dir.clone() else {
            self.pick_export_dir(cx);
            return;
        };
        let Some(bridge) = self.bridge() else { return };
        let thread_id = dialog.thread_id.clone();
        let params = json!({
            "thread_id": thread_id,
            "dir": dir.to_string_lossy(),
            "format": dialog.format.bridge(),
            "include_media": dialog.include_media,
            "locale": crate::i18n::current(),
        });
        self.exports.last_dir = Some(dir);
        self.exports.job = Some(ExportJob {
            thread_id: thread_id.clone(),
            thread_name: thread_display(self, &thread_id),
            messages: 0,
            media: 0,
            result: None,
        });
        cx.notify();
        cx.spawn(async move |this, cx| {
            let result = bridge.call::<ExportOutcome>("export_thread", params).await;
            let _ = this.update(cx, |this, cx| {
                let current = this
                    .exports
                    .job
                    .as_ref()
                    .is_some_and(|job| job.thread_id == thread_id && job.result.is_none());
                if !current {
                    return;
                }
                this.exports.cancelling = false;
                let toast = match classify(result, |outcome: &ExportOutcome| outcome.cancelled) {
                    Ending::Cancelled => {
                        this.exports.job = None;
                        t!("export.cancelled").into_owned()
                    }
                    Ending::Busy => {
                        this.exports.job = None;
                        t!("export.already_running").into_owned()
                    }
                    Ending::Done(outcome) => {
                        let Some(job) = this.exports.job.as_mut() else {
                            return;
                        };
                        job.messages = outcome.messages;
                        job.media = outcome.media;
                        let text = t!(
                            "export.done",
                            name = job.thread_name,
                            count = message_count(outcome.messages)
                        )
                        .into_owned();
                        job.result = Some(Ok(outcome));
                        text
                    }
                    Ending::Failed(error) => {
                        if let Some(job) = this.exports.job.as_mut() {
                            job.result = Some(Err(error.clone().into()));
                        }
                        t!("export.failed", error = error).into_owned()
                    }
                };
                this.show_toast(toast, cx);
            });
        })
        .detach();
    }

    pub(crate) fn archive_busy(&self) -> bool {
        self.export_running() || self.exports.saves.iter().any(|save| save.result.is_none())
    }

    pub(crate) fn archive_cancelling(&self) -> bool {
        self.exports.cancelling && self.archive_busy()
    }

    pub(crate) fn cancel_archive(&mut self, cx: &mut Context<Self>) {
        if !self.archive_busy() || self.exports.cancelling {
            return;
        }
        let Some(bridge) = self.bridge() else { return };
        self.exports.cancelling = true;
        cx.notify();
        cx.spawn(async move |this, cx| {
            let result = bridge.request("cancel_archive", json!({})).await;
            if let Err(err) = result {
                let _ = this.update(cx, |this, cx| {
                    this.exports.cancelling = false;
                    this.show_toast(
                        t!("export.cancel_failed", error = action_error(&err)).into_owned(),
                        cx,
                    );
                });
            }
        })
        .detach();
    }

    pub(crate) fn open_export_folder(&mut self, cx: &mut Context<Self>) {
        let Some(Ok(outcome)) = self.exports.job.as_ref().and_then(|job| job.result.as_ref())
        else {
            return;
        };
        open_folder(&PathBuf::from(&outcome.path), cx);
    }

    pub(crate) fn media_save_for(&self, thread_id: &str) -> Option<&MediaSave> {
        self.exports
            .saves
            .iter()
            .find(|save| save.thread_id == thread_id)
    }

    pub(crate) fn save_all_media(&mut self, thread_id: &str, cx: &mut Context<Self>) {
        if self.archive_busy() {
            self.show_toast(busy_toast(), cx);
            return;
        }
        let receiver = cx.prompt_for_paths(PathPromptOptions {
            files: false,
            directories: true,
            multiple: false,
            prompt: Some(ts!("export.save_here_prompt")),
        });
        let thread_id = thread_id.to_string();
        cx.spawn(async move |this, cx| {
            let Ok(Ok(Some(paths))) = receiver.await else {
                return;
            };
            let Some(dir) = paths.into_iter().next() else {
                return;
            };
            let _ = this.update(cx, |this, cx| this.start_media_save(thread_id, dir, cx));
        })
        .detach();
    }

    fn start_media_save(&mut self, thread_id: String, dir: PathBuf, cx: &mut Context<Self>) {
        let Some(bridge) = self.bridge() else { return };
        if self.archive_busy() {
            self.show_toast(busy_toast(), cx);
            return;
        }
        self.exports.saves.retain(|save| save.thread_id != thread_id);
        self.exports.saves.push(MediaSave {
            thread_id: thread_id.clone(),
            saved: 0,
            total: None,
            result: None,
        });
        self.exports.last_dir = Some(dir.clone());
        let params = json!({
            "thread_id": thread_id,
            "dir": dir.to_string_lossy(),
            "locale": crate::i18n::current(),
        });
        cx.notify();
        cx.spawn(async move |this, cx| {
            let result = bridge.call::<SaveMediaOutcome>("save_media", params).await;
            let _ = this.update(cx, |this, cx| {
                let name = thread_display(this, &thread_id);
                let Some(index) = this
                    .exports
                    .saves
                    .iter()
                    .position(|save| save.thread_id == thread_id && save.result.is_none())
                else {
                    return;
                };
                this.exports.cancelling = false;
                let ending = classify(result, |outcome: &SaveMediaOutcome| outcome.cancelled);
                let toast = match ending {
                    Ending::Cancelled => {
                        this.exports.saves.remove(index);
                        t!("export.download_cancelled").into_owned()
                    }
                    Ending::Busy => {
                        this.exports.saves.remove(index);
                        t!("export.already_running").into_owned()
                    }
                    Ending::Done(outcome) => {
                        let save = &mut this.exports.saves[index];
                        save.saved = outcome.saved;
                        let mut text = t!(
                            "export.saved",
                            name = name,
                            count = media_count(outcome.saved)
                        )
                        .into_owned();
                        if outcome.failed > 0 {
                            text.push_str(&t!(
                                "export.saved_failed",
                                count = group_digits(outcome.failed)
                            ));
                        }
                        save.result = Some(Ok(outcome));
                        text
                    }
                    Ending::Failed(error) => {
                        this.exports.saves[index].result = Some(Err(error.clone().into()));
                        t!("export.download_failed", error = error).into_owned()
                    }
                };
                this.show_toast(toast, cx);
            });
        })
        .detach();
    }

    pub(crate) fn open_media_folder(&mut self, thread_id: &str, cx: &mut Context<Self>) {
        let Some(Ok(outcome)) = self
            .media_save_for(thread_id)
            .and_then(|save| save.result.as_ref())
        else {
            return;
        };
        if outcome.dir.is_empty() {
            return;
        }
        open_folder(&PathBuf::from(&outcome.dir), cx);
    }
}

fn open_folder(path: &std::path::Path, cx: &mut App) {
    if path.is_dir() {
        cx.open_with_system(path);
    } else {
        crate::desktop::open_local(path, cx);
    }
}

#[cfg(test)]
mod tests {
    use super::{Ending, action_error, classify, group_digits, media_count, message_count};
    use crate::bridge::BridgeError;

    fn bridge_error(code: &str, message: &str) -> anyhow::Error {
        BridgeError {
            code: code.into(),
            message: message.into(),
            data: None,
        }
        .into()
    }

    #[::core::prelude::v1::test]
    fn archive_endings_are_classified() {
        let flag = |value: &bool| *value;
        assert_eq!(classify(Ok(false), flag), Ending::Done(false));
        assert_eq!(classify(Ok(true), flag), Ending::Cancelled);
        assert_eq!(
            classify(Err(bridge_error("cancelled", "stop")), flag),
            Ending::Cancelled
        );
        assert_eq!(
            classify(
                Err(bridge_error("busy", "an archive job is already running")),
                flag
            ),
            Ending::Busy
        );
        assert!(matches!(
            classify(
                Err(bridge_error("bad_request", "Un export est déjà en cours")),
                flag
            ),
            Ending::Failed(_)
        ));
        assert!(matches!(
            classify(Err(bridge_error("bad_request", "dossier introuvable")), flag),
            Ending::Failed(_)
        ));
    }

    #[::core::prelude::v1::test]
    fn rejected_edit_follows_the_language() {
        let rejected = || action_error(&bridge_error("edit_rejected", "rejected"));
        {
            let _guard = crate::i18n::scoped("fr");
            assert_eq!(rejected(), "Messenger a refusé la modification");
        }
        let _guard = crate::i18n::scoped("en");
        assert_eq!(rejected(), "Messenger rejected the edit");
    }

    #[::core::prelude::v1::test]
    fn counts_are_grouped_and_pluralized() {
        let _guard = crate::i18n::scoped("fr");
        assert_eq!(group_digits(1240), "1\u{a0}240");
        assert_eq!(group_digits(85), "85");
        assert_eq!(group_digits(1_000_000), "1\u{a0}000\u{a0}000");
        assert_eq!(message_count(1), "1 message");
        assert_eq!(media_count(0), "0 média");
        assert_eq!(message_count(1240), "1\u{a0}240 messages");
    }

    #[::core::prelude::v1::test]
    fn counts_follow_english_plurals() {
        let _guard = crate::i18n::scoped("en");
        assert_eq!(message_count(1), "1 message");
        assert_eq!(message_count(0), "0 messages");
        assert_eq!(media_count(1240), "1,240 media files");
        assert_eq!(media_count(1), "1 media file");
    }
}
