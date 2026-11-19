use std::{
    collections::{HashSet, VecDeque},
    path::PathBuf,
    time::Duration,
};

use gpui_kit::*;
use serde_json::json;

use super::{Media, MissiveApp, Outgoing, action_error};
use crate::{
    audio::{self, AudioPlayer, Recorder},
    model::{Attachment, MediaResult, Message, ReplyPreview},
};

const TICK: Duration = Duration::from_millis(100);
const LEVEL_HISTORY: usize = 48;
const MIN_VOICE_MS: i64 = 600;

pub(crate) struct Playback {
    pub(crate) key: String,
    pub(crate) position_ms: i64,
    pub(crate) duration_ms: Option<i64>,
    pub(crate) playing: bool,
    pub(crate) loading: bool,
}

pub(crate) struct Recording {
    recorder: Recorder,
    id: u64,
    pub(crate) thread_id: String,
    pub(crate) ready: bool,
    pub(crate) limit_reached: bool,
    pub(crate) elapsed_ms: i64,
    pub(crate) levels: VecDeque<f32>,
}

#[derive(Default)]
pub(crate) struct Voice {
    player: Option<AudioPlayer>,
    pub(crate) playback: Option<Playback>,
    pub(crate) undecodable: HashSet<String>,
    pub(crate) unavailable: HashSet<String>,
    pub(crate) recording: Option<Recording>,
    next_recording: u64,
    ticking: bool,
    tick_generation: u64,
}

impl Voice {
    pub(crate) fn on_thread_changed(&mut self) {
        self.recording = None;
    }

    pub(crate) fn reset(&mut self) {
        if let Some(player) = &self.player {
            player.stop();
        }
        let tick_generation = self.tick_generation + 1;
        let next_recording = self.next_recording;
        *self = Self {
            tick_generation,
            next_recording,
            ..Self::default()
        };
    }

    pub(crate) fn playback_for(&self, key: &str) -> Option<&Playback> {
        self.playback.as_ref().filter(|playback| playback.key == key)
    }
}

fn display_level(rms: f32) -> f32 {
    if rms <= 0.0 {
        return 0.0;
    }
    ((20.0 * rms.log10() + 50.0) / 50.0).clamp(0.0, 1.0)
}

impl MissiveApp {
    pub(crate) fn close_voice(&mut self, cx: &mut Context<Self>) -> bool {
        if self.voice.recording.is_none() {
            return false;
        }
        self.cancel_voice_recording(cx);
        true
    }

    pub(crate) fn toggle_voice_playback(
        &mut self,
        thread_id: &str,
        message_id: &str,
        attachment_id: &str,
        cx: &mut Context<Self>,
    ) {
        let key = super::media_key(message_id, attachment_id);
        if let Some(playback) = self
            .voice
            .playback
            .as_mut()
            .filter(|playback| playback.key == key)
        {
            if playback.loading {
                return;
            }
            playback.playing = !playback.playing;
            if let Some(player) = &self.voice.player {
                if playback.playing {
                    player.resume();
                } else {
                    player.pause();
                }
            }
            self.ensure_voice_tick(cx);
            cx.notify();
            return;
        }
        self.start_voice_playback(thread_id, message_id, attachment_id, 0, cx);
    }

    pub(crate) fn seek_voice(
        &mut self,
        thread_id: &str,
        message_id: &str,
        attachment_id: &str,
        fraction: f32,
        known_duration_ms: Option<i64>,
        cx: &mut Context<Self>,
    ) {
        let key = super::media_key(message_id, attachment_id);
        let fraction = fraction.clamp(0.0, 1.0);
        let at = |duration: Option<i64>| {
            duration
                .filter(|duration| *duration > 0)
                .map(|duration| (duration as f32 * fraction) as i64)
        };
        let active = self
            .voice
            .playback
            .as_mut()
            .filter(|playback| playback.key == key && !playback.loading);
        match active {
            Some(playback) => {
                let Some(position) = at(playback.duration_ms.or(known_duration_ms)) else {
                    return;
                };
                playback.position_ms = position;
                playback.playing = true;
                if let Some(player) = &self.voice.player {
                    player.seek(position);
                    player.resume();
                }
                self.ensure_voice_tick(cx);
                cx.notify();
            }
            None => {
                let start = at(known_duration_ms).unwrap_or(0);
                self.start_voice_playback(thread_id, message_id, attachment_id, start, cx);
            }
        }
    }

    fn start_voice_playback(
        &mut self,
        thread_id: &str,
        message_id: &str,
        attachment_id: &str,
        start_ms: i64,
        cx: &mut Context<Self>,
    ) {
        let key = super::media_key(message_id, attachment_id);
        if let Some(player) = &self.voice.player {
            player.stop();
        }
        self.voice.unavailable.remove(&key);
        self.voice.playback = Some(Playback {
            key: key.clone(),
            position_ms: start_ms,
            duration_ms: None,
            playing: true,
            loading: true,
        });
        cx.notify();
        if let Some(Media::Ready(path)) = self.media.get(&key).cloned() {
            self.begin_audio(key, path, start_ms, cx);
            return;
        }
        let Some(bridge) = self.bridge() else {
            self.voice.playback = None;
            return;
        };
        let params = json!({
            "thread_id": thread_id,
            "message_id": message_id,
            "attachment_id": attachment_id,
        });
        cx.spawn(async move |this, cx| {
            let result = bridge.call::<MediaResult>("media", params).await;
            let _ = this.update(cx, |this, cx| {
                let waiting = this
                    .voice
                    .playback
                    .as_ref()
                    .is_some_and(|playback| playback.key == key && playback.loading);
                match result {
                    Ok(media) => {
                        let path = PathBuf::from(media.path);
                        this.media.insert(key.clone(), Media::Ready(path.clone()));
                        if waiting {
                            this.begin_audio(key, path, start_ms, cx);
                        }
                    }
                    Err(err) => {
                        eprintln!("[voice] media download failed: {err:#}");
                        if waiting {
                            this.voice.playback = None;
                        }
                        this.voice.unavailable.insert(key);
                        this.show_toast(
                            t!("voice.error.unavailable", reason = action_error(&err)),
                            cx,
                        );
                    }
                }
                cx.notify();
            });
        })
        .detach();
    }

    fn begin_audio(&mut self, key: String, path: PathBuf, start_ms: i64, cx: &mut Context<Self>) {
        if self.voice.player.is_none() {
            match AudioPlayer::spawn() {
                Ok(player) => self.voice.player = Some(player),
                Err(err) => {
                    eprintln!("[voice] audio thread failed: {err:#}");
                    self.voice.playback = None;
                    self.show_toast(t!("voice.error.playback_failed"), cx);
                    return;
                }
            }
        }
        if let Some(player) = &self.voice.player {
            player.play(key.clone(), path, start_ms);
        }
        if let Some(playback) = self
            .voice
            .playback
            .as_mut()
            .filter(|playback| playback.key == key)
        {
            playback.loading = false;
            playback.playing = true;
        }
        self.ensure_voice_tick(cx);
        cx.notify();
    }

    fn ensure_voice_tick(&mut self, cx: &mut Context<Self>) {
        if self.voice.ticking {
            return;
        }
        self.voice.ticking = true;
        let generation = self.voice.tick_generation;
        cx.spawn(async move |this, cx| {
            loop {
                cx.background_executor().timer(TICK).await;
                let keep = this
                    .update(cx, |this, cx| {
                        if this.voice.tick_generation != generation {
                            return false;
                        }
                        let keep = this.voice_tick(cx);
                        if !keep {
                            this.voice.ticking = false;
                        }
                        keep
                    })
                    .unwrap_or(false);
                if !keep {
                    break;
                }
            }
        })
        .detach();
    }

    fn voice_tick(&mut self, cx: &mut Context<Self>) -> bool {
        let mut active = false;
        let mut message: Option<SharedString> = None;
        let status = self.voice.player.as_ref().map(AudioPlayer::status);
        if let Some(playback) = self.voice.playback.as_mut() {
            if let Some(status) = status.filter(|status| {
                !playback.loading && status.key.as_deref() == Some(playback.key.as_str())
            }) {
                playback.position_ms = status.position_ms;
                if status.duration_ms.is_some() {
                    playback.duration_ms = status.duration_ms;
                }
                playback.playing = status.playing;
                if status.finished {
                    if status.undecodable {
                        self.voice.undecodable.insert(playback.key.clone());
                        message = Some(ts!("voice.error.unsupported_format"));
                    } else if status.no_device {
                        message = Some(ts!("voice.error.no_output_device"));
                    }
                    self.voice.playback = None;
                } else {
                    active = status.playing;
                }
            } else {
                active = playback.loading;
            }
        }
        if let Some(recording) = self.voice.recording.as_mut() {
            if !recording.ready {
                active = true;
            } else if recording.recorder.halted() {
                recording.limit_reached = true;
                recording.elapsed_ms = audio::MAX_RECORDING_MS;
            } else {
                active = true;
                recording.elapsed_ms = recording
                    .recorder
                    .elapsed_ms()
                    .min(audio::MAX_RECORDING_MS);
                recording
                    .levels
                    .push_back(display_level(recording.recorder.level()));
                while recording.levels.len() > LEVEL_HISTORY {
                    recording.levels.pop_front();
                }
            }
        }
        if let Some(message) = message {
            self.show_toast(message, cx);
        }
        cx.notify();
        active
    }

    pub(crate) fn start_voice_recording(&mut self, cx: &mut Context<Self>) {
        if self.voice.recording.is_some() {
            return;
        }
        let Some(thread_id) = self.selected.clone() else {
            return;
        };
        if let Some(player) = &self.voice.player {
            player.stop();
        }
        self.voice.playback = None;
        let stamp = chrono::Utc::now().timestamp_millis();
        let path = std::env::temp_dir().join(format!("missive-voice-{stamp}.wav"));
        let (recorder, ready) = Recorder::start(path);
        self.voice.next_recording += 1;
        let id = self.voice.next_recording;
        self.voice.recording = Some(Recording {
            recorder,
            id,
            thread_id,
            ready: false,
            limit_reached: false,
            elapsed_ms: 0,
            levels: VecDeque::new(),
        });
        self.ensure_voice_tick(cx);
        cx.spawn(async move |this, cx| {
            let result = ready.await;
            let _ = this.update(cx, |this, cx| {
                let current = this
                    .voice
                    .recording
                    .as_mut()
                    .filter(|recording| recording.id == id);
                match (result, current) {
                    (Ok(Ok(())), Some(recording)) => recording.ready = true,
                    (Ok(Ok(())), None) => {}
                    (failure, current) => {
                        if let Ok(Err(err)) = &failure {
                            eprintln!("[voice] microphone unavailable: {err:#}");
                        }
                        if current.is_some() {
                            this.voice.recording = None;
                            this.show_toast(t!("voice.error.microphone_unavailable"), cx);
                        }
                    }
                }
                cx.notify();
            });
        })
        .detach();
        cx.notify();
    }

    pub(crate) fn cancel_voice_recording(&mut self, cx: &mut Context<Self>) {
        if self.voice.recording.take().is_some() {
            cx.notify();
        }
    }

    pub(crate) fn send_voice_recording(&mut self, cx: &mut Context<Self>) {
        let Some(recording) = self.voice.recording.take() else {
            return;
        };
        cx.notify();
        if !recording.ready {
            return;
        }
        let Some(bridge) = self.bridge() else { return };
        let thread_id = recording.thread_id.clone();
        let reply_to = self
            .reply_to
            .as_ref()
            .filter(|message| message.thread_id == thread_id)
            .map(|message| message.id.clone());
        if reply_to.is_some() {
            self.reply_to = None;
        }
        let done = recording.recorder.finish();
        cx.spawn(async move |this, cx| {
            let recorded = match done.await {
                Ok(Ok(Some(recorded))) => recorded,
                Ok(Err(err)) => {
                    eprintln!("[voice] recording failed: {err:#}");
                    let _ = this.update(cx, |this, cx| {
                        this.show_toast(t!("voice.error.recording_failed"), cx)
                    });
                    return;
                }
                _ => {
                    let _ = this.update(cx, |this, cx| {
                        this.show_toast(t!("voice.error.recording_interrupted"), cx)
                    });
                    return;
                }
            };
            if recorded.duration_ms < MIN_VOICE_MS {
                let _ = std::fs::remove_file(&recorded.path);
                let _ = this.update(cx, |this, cx| {
                    this.show_toast(t!("voice.error.too_short"), cx)
                });
                return;
            }
            let inserted = this.update(cx, |this, cx| {
                this.insert_voice_message(&thread_id, reply_to.clone(), &recorded, cx)
            });
            let Ok(client_id) = inserted else {
                let _ = std::fs::remove_file(&recorded.path);
                return;
            };
            let mut params = json!({
                "thread_id": thread_id,
                "path": recorded.path.to_string_lossy(),
                "duration_ms": recorded.duration_ms,
                "client_id": client_id,
            });
            if let Some(reply_to) = &reply_to {
                params["reply_to_id"] = json!(reply_to);
            }
            let result = bridge.request("send_voice", params).await;
            let _ = std::fs::remove_file(&recorded.path);
            let _ = this.update(cx, |this, cx| {
                this.outgoing.remove(&client_id);
                if let Err(err) = result {
                    this.on_message_removed(&thread_id, &client_id, cx);
                    this.remeasure_last_outgoing(&thread_id, cx);
                    this.show_toast(
                        t!("voice.error.send_failed", reason = action_error(&err)),
                        cx,
                    );
                }
                cx.notify();
            });
        })
        .detach();
    }

    fn insert_voice_message(
        &mut self,
        thread_id: &str,
        reply_to: Option<String>,
        recorded: &audio::Recording,
        cx: &mut Context<Self>,
    ) -> String {
        self.next_client_id += 1;
        let now = chrono::Utc::now().timestamp_millis();
        let client_id = format!("local-{now}-{}", self.next_client_id);
        let (sender_id, sender_name) = self
            .user
            .as_ref()
            .map(|user| (user.id.clone(), user.name.clone()))
            .unwrap_or_default();
        let reply_preview = reply_to.as_ref().and_then(|id| {
            self.messages
                .get(thread_id)
                .and_then(|list| list.iter().find(|m| &m.id == id))
                .map(|quoted| ReplyPreview {
                    id: quoted.id.clone(),
                    sender_name: quoted.sender_name.clone(),
                    text: quoted.text.clone(),
                    kind: quoted.attachments.first().map(|a| a.kind.clone()),
                })
        });
        let message = Message {
            id: client_id.clone(),
            client_id: Some(client_id.clone()),
            thread_id: thread_id.to_string(),
            sender_id,
            sender_name,
            timestamp_ms: now,
            is_outgoing: true,
            attachments: vec![Attachment {
                id: "local0".into(),
                kind: "audio".into(),
                duration_ms: Some(recorded.duration_ms),
                waveform: recorded.waveform.clone(),
                ..Attachment::default()
            }],
            reply_to: reply_preview,
            ..Message::default()
        };
        if let Some(thread) = self.threads.iter_mut().find(|t| t.id == thread_id) {
            thread.snippet = super::preview(&message);
            thread.last_activity_ms = thread.last_activity_ms.max(now);
        }
        self.sort_threads();
        let is_selected = self.selected.as_deref() == Some(thread_id);
        self.remeasure_last_outgoing(thread_id, cx);
        if let Some(list) = self.messages.get_mut(thread_id) {
            list.push(message);
            if is_selected {
                self.scroller.update(cx, |scroller, cx| {
                    scroller.append(1, cx);
                    scroller.scroll_to_end(cx);
                });
            }
        }
        self.outgoing.insert(client_id.clone(), Outgoing::Sending);
        cx.notify();
        client_id
    }
}

#[cfg(test)]
mod tests {
    use super::display_level;

    #[::core::prelude::v1::test]
    fn display_level_maps_decibels() {
        assert_eq!(display_level(0.0), 0.0);
        assert_eq!(display_level(1.0), 1.0);
        assert!((display_level(0.1) - 0.6).abs() < 1e-4);
        assert_eq!(display_level(0.001), 0.0);
    }
}
