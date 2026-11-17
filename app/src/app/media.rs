use std::path::PathBuf;

use gpui_kit::*;
use serde_json::json;

use super::{GifFrames, Media, MissiveApp};
use crate::model::{Attachment, MediaResult, Message};

const MAX_IN_FLIGHT: usize = 6;
const MAX_GIF_DECODES: usize = 2;
const MAX_CACHED_GIFS: usize = 12;

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

    pub(crate) fn gif_source(
        &self,
        message: &Message,
        attachment: &Attachment,
    ) -> Option<ImageSource> {
        if !attachment.gif || !crate::gif_video::SUPPORTED || attachment.id.is_empty() {
            return None;
        }
        let key = super::media_key(&message.id, &attachment.id);
        match self.gifs.get(&key) {
            Some(GifFrames::Ready(image)) => return Some(image.clone()),
            Some(GifFrames::Decoding) | Some(GifFrames::Failed) => return None,
            None => {}
        }
        match self.state(&key) {
            Some(Media::Ready(path)) => {
                let mut queue = self.gif_queue.borrow_mut();
                if !queue.iter().any(|(queued, _)| *queued == key) {
                    queue.push((key, path.clone()));
                }
            }
            Some(Media::Loading) | Some(Media::Failed) => {}
            None => self.enqueue(MediaJob::Full {
                thread_id: message.thread_id.clone(),
                message_id: message.id.clone(),
                attachment_id: attachment.id.clone(),
            }),
        }
        None
    }

    pub(crate) fn pump_gifs(&mut self, cx: &mut Context<Self>) {
        while self.gif_in_flight < MAX_GIF_DECODES {
            let Some((key, path)) = self.gif_queue.borrow_mut().pop() else {
                break;
            };
            if self.gifs.contains_key(&key) {
                continue;
            }
            if crate::gif_video::is_gif_file(&path) {
                self.gifs.insert(key.clone(), GifFrames::Ready(path.into()));
                self.remember_gif(key, cx);
                cx.notify();
                continue;
            }
            self.gifs.insert(key.clone(), GifFrames::Decoding);
            self.gif_in_flight += 1;
            let (tx, rx) = futures::channel::oneshot::channel();
            let spawned = std::thread::Builder::new()
                .name("gif-decode".into())
                .spawn(move || {
                    let _ = tx.send(crate::gif_video::decode(&path));
                });
            if let Err(err) = spawned {
                eprintln!("[app] gif decoder not started: {err}");
            }
            cx.spawn(async move |this, cx| {
                let result = rx.await;
                let _ = this.update(cx, |this, cx| {
                    this.gif_in_flight = this.gif_in_flight.saturating_sub(1);
                    match result {
                        Ok(Ok(image)) => {
                            this.gifs.insert(key.clone(), GifFrames::Ready(image.into()));
                            this.remember_gif(key, cx);
                        }
                        Ok(Err(err)) => {
                            eprintln!("[app] gif decode failed: {err:#}");
                            this.gifs.insert(key, GifFrames::Failed);
                        }
                        Err(_) => {
                            this.gifs.insert(key, GifFrames::Failed);
                        }
                    }
                    this.pump_gifs(cx);
                    cx.notify();
                });
            })
            .detach();
        }
    }

    fn remember_gif(&mut self, key: String, cx: &mut Context<Self>) {
        self.gif_order.push(key);
        while self.gif_order.len() > MAX_CACHED_GIFS {
            let evicted = self.gif_order.remove(0);
            if let Some(GifFrames::Ready(ImageSource::Render(image))) = self.gifs.remove(&evicted) {
                cx.drop_image(image, None);
            }
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
