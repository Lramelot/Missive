use std::path::PathBuf;

use gpui_kit::*;
use serde_json::json;

use super::{Media, MissiveApp};
use crate::model::{Attachment, MediaResult, Message};

const MAX_IN_FLIGHT: usize = 6;

#[derive(Clone, Debug, PartialEq, Eq)]
pub(crate) enum MediaJob {
    Thumb {
        thread_id: String,
        message_id: String,
        attachment_id: String,
    },
    Full {
        thread_id: String,
        message_id: String,
        attachment_id: String,
    },
    Avatar {
        url: String,
    },
}

impl MediaJob {
    fn key(&self) -> String {
        match self {
            MediaJob::Thumb {
                message_id,
                attachment_id,
                ..
            } => thumb_key(message_id, attachment_id),
            MediaJob::Full {
                message_id,
                attachment_id,
                ..
            } => super::media_key(message_id, attachment_id),
            MediaJob::Avatar { url } => avatar_key(url),
        }
    }
}

pub(crate) fn thumb_key(message_id: &str, attachment_id: &str) -> String {
    format!("{message_id}/{attachment_id}#thumb")
}

pub(crate) fn avatar_key(url: &str) -> String {
    format!("avatar:{url}")
}

fn http_source(url: &str) -> Option<ImageSource> {
    (url.starts_with("https://") || url.starts_with("http://"))
        .then(|| SharedString::from(url.to_string()).into())
}

impl MissiveApp {
    fn enqueue(&self, job: MediaJob) {
        let mut queue = self.media_queue.borrow_mut();
        if !queue.contains(&job) {
            queue.push(job);
        }
    }

    fn state(&self, key: &str) -> Option<&Media> {
        self.media.get(key)
    }

    pub(crate) fn thumb_source(
        &self,
        message: &Message,
        attachment: &Attachment,
    ) -> Option<ImageSource> {
        let key = thumb_key(&message.id, &attachment.id);
        match self.state(&key) {
            Some(Media::Ready(path)) => return Some(path.clone().into()),
            Some(Media::Loading) => return None,
            Some(Media::Failed) => {
                return attachment
                    .thumbnail_url
                    .as_deref()
                    .and_then(http_source)
                    .or_else(|| {
                        (attachment.kind == "image")
                            .then(|| attachment.url.as_deref().and_then(http_source))
                            .flatten()
                    })
                    .or_else(|| {
                        match self.state(&super::media_key(&message.id, &attachment.id)) {
                            Some(Media::Ready(path)) => Some(path.clone().into()),
                            _ => None,
                        }
                    });
            }
            None => {}
        }
        if attachment.id.is_empty() {
            return attachment.thumbnail_url.as_deref().and_then(http_source);
        }
        self.enqueue(MediaJob::Thumb {
            thread_id: message.thread_id.clone(),
            message_id: message.id.clone(),
            attachment_id: attachment.id.clone(),
        });
        None
    }

    pub(crate) fn full_source(
        &self,
        message: &Message,
        attachment: &Attachment,
    ) -> Option<ImageSource> {
        if attachment.kind == "image" {
            if let Some(source) = attachment.url.as_deref().and_then(http_source) {
                return Some(source);
            }
        }
        let key = super::media_key(&message.id, &attachment.id);
        match self.state(&key) {
            Some(Media::Ready(path)) => Some(path.clone().into()),
            Some(Media::Loading) | Some(Media::Failed) => None,
            None => {
                if !attachment.id.is_empty() {
                    self.enqueue(MediaJob::Full {
                        thread_id: message.thread_id.clone(),
                        message_id: message.id.clone(),
                        attachment_id: attachment.id.clone(),
                    });
                }
                None
            }
        }
    }

    pub(crate) fn thumb_failed(&self, message: &Message, attachment: &Attachment) -> bool {
        matches!(
            self.state(&thumb_key(&message.id, &attachment.id)),
            Some(Media::Failed)
        ) && self.thumb_source(message, attachment).is_none()
    }

    pub(crate) fn avatar_source(&self, url: Option<&str>) -> Option<ImageSource> {
        let url = url.filter(|url| !url.is_empty())?;
        if !url.starts_with("http") {
            return Some(PathBuf::from(url).into());
        }
        match self.state(&avatar_key(url)) {
            Some(Media::Ready(path)) => Some(path.clone().into()),
            Some(Media::Failed) => http_source(url),
            Some(Media::Loading) => None,
            None => {
                self.enqueue(MediaJob::Avatar {
                    url: url.to_string(),
                });
                None
            }
        }
    }

    pub(crate) fn avatar_spec<'a>(
        &self,
        thread: &'a crate::model::Thread,
    ) -> crate::views::AvatarSpec<'a> {
        let has_own = thread
            .avatar_url
            .as_deref()
            .is_some_and(|url| !url.is_empty());
        crate::views::AvatarSpec {
            name: &thread.name,
            own: self.avatar_source(thread.avatar_url.as_deref()),
            has_own,
            members: if has_own {
                Vec::new()
            } else {
                thread
                    .member_avatars
                    .iter()
                    .take(2)
                    .map(|url| self.avatar_source(Some(url)))
                    .collect()
            },
            online: self.is_online(thread),
        }
    }

    pub(crate) fn pump_media(&mut self, cx: &mut Context<Self>) {
        let Some(bridge) = self.bridge() else { return };
        while self.media_in_flight < MAX_IN_FLIGHT {
            let Some(job) = self.media_queue.borrow_mut().pop() else {
                break;
            };
            let key = job.key();
            if self.media.contains_key(&key) {
                continue;
            }
            self.media.insert(key.clone(), Media::Loading);
            self.media_in_flight += 1;
            let bridge = bridge.clone();
            let (cmd, params, remeasure) = match &job {
                MediaJob::Thumb {
                    thread_id,
                    message_id,
                    attachment_id,
                } => (
                    "media",
                    json!({ "thread_id": thread_id, "message_id": message_id, "attachment_id": attachment_id, "variant": "thumb" }),
                    Some((thread_id.clone(), message_id.clone())),
                ),
                MediaJob::Full {
                    thread_id,
                    message_id,
                    attachment_id,
                } => (
                    "media",
                    json!({ "thread_id": thread_id, "message_id": message_id, "attachment_id": attachment_id }),
                    Some((thread_id.clone(), message_id.clone())),
                ),
                MediaJob::Avatar { url } => ("avatar", json!({ "url": url }), None),
            };
            cx.spawn(async move |this, cx| {
                let result = bridge.call::<MediaResult>(cmd, params).await;
                let _ = this.update(cx, |this, cx| {
                    this.media_in_flight = this.media_in_flight.saturating_sub(1);
                    let state = match result {
                        Ok(media) => Media::Ready(PathBuf::from(media.path)),
                        Err(err) => {
                            eprintln!("[app] {cmd} failed: {err:#}");
                            Media::Failed
                        }
                    };
                    this.media.insert(key, state);
                    if let Some((thread_id, message_id)) = remeasure {
                        this.remeasure_message(&thread_id, &message_id, cx);
                    }
                    this.pump_media(cx);
                    cx.notify();
                });
            })
            .detach();
        }
    }

    pub(crate) fn remeasure_message(
        &mut self,
        thread_id: &str,
        message_id: &str,
        cx: &mut Context<Self>,
    ) {
        if self.selected.as_deref() != Some(thread_id) {
            return;
        }
        let Some(index) = self
            .messages
            .get(thread_id)
            .and_then(|list| list.iter().position(|m| m.id == message_id))
        else {
            return;
        };
        self.scroller.update(cx, |scroller, cx| {
            scroller.remeasure_items(index..index + 1, cx)
        });
    }
}
