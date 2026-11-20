use gpui_kit::assets::IconName;
use gpui_kit::component::{Icon, h_flex};
use gpui_kit::{prelude::FluentBuilder as _, *};

use super::composer::composer_field;
use super::widgets::{format_duration, icon_button};
use crate::{
    app::{Media, MissiveApp, media_key},
    audio::{WAVEFORM_BARS, bucket_waveform, pseudo_waveform},
    model::{Attachment, Message, Thread},
    settings::VoiceSpeed,
    style::{Palette, with_alpha},
};

const BAR_WIDTH: f32 = 3.;
const BAR_GAP: f32 = 2.;
const WAVE_HEIGHT: f32 = 26.;
const METER_HEIGHT: f32 = 22.;

#[derive(Clone)]
struct Target {
    thread_id: SharedString,
    message_id: SharedString,
    attachment_id: SharedString,
    duration_ms: Option<i64>,
}

fn theme_accent(value: &str) -> Option<Hsla> {
    let hex = value.trim().strip_prefix('#')?;
    if hex.len() != 6 {
        return None;
    }
    u32::from_str_radix(hex, 16).ok().map(|value| rgb(value).into())
}

fn time_label(ms: Option<i64>) -> SharedString {
    ms.map(format_duration)
        .unwrap_or_else(|| "0:00".into())
        .into()
}

impl MissiveApp {
    pub(super) fn render_voice_attachment(
        &self,
        message: &Message,
        attachment: &Attachment,
        outgoing: bool,
        view: &Entity<Self>,
        p: &Palette,
        cx: &App,
    ) -> Option<AnyElement> {
        let _ = cx;
        if attachment.kind != "audio" {
            return None;
        }
        let local = attachment.id.is_empty()
            || message.client_id.as_deref() == Some(message.id.as_str());
        let key = media_key(&message.id, &attachment.id);
        let playback = self.voice.playback_for(&key);
        let undecodable = self.voice.undecodable.contains(&key);
        let unavailable = self.voice.unavailable.contains(&key);
        let path = match self.media.get(&key) {
            Some(Media::Ready(path)) => Some(path.clone()),
            _ => None,
        };
        let duration_ms = playback
            .and_then(|playback| playback.duration_ms)
            .or(attachment.duration_ms);
        let progress = match (playback, duration_ms) {
            (Some(playback), Some(duration)) if duration > 0 => {
                (playback.position_ms as f32 / duration as f32).clamp(0.0, 1.0)
            }
            _ => 0.0,
        };
        let loading = playback.is_some_and(|playback| playback.loading);
        let playing = playback.is_some_and(|playback| playback.playing);

        let accent = outgoing
            .then(|| {
                self.threads
                    .iter()
                    .find(|thread| thread.id == message.thread_id)
                    .and_then(|thread| thread.theme.as_ref())
                    .and_then(|theme| theme_accent(&theme.accent))
            })
            .flatten();
        let (bg, fg) = match (outgoing, accent) {
            (true, Some(accent)) => (accent, gpui_kit::white()),
            (true, None) => (p.bubble_out, p.on_bubble_out),
            (false, _) => (p.bubble_in, p.bubble_in_text),
        };
        let (button_bg, button_icon_color) = if outgoing {
            (fg, bg)
        } else {
            (p.accent, p.on_accent)
        };
        let played_color = if outgoing { fg } else { p.accent_text };
        let unplayed_color = if outgoing {
            with_alpha(fg, 0.4)
        } else if p.dark {
            with_alpha(p.text_faint, 0.5)
        } else {
            with_alpha(p.accent_text, 0.38)
        };

        let target = Target {
            thread_id: message.thread_id.clone().into(),
            message_id: message.id.clone().into(),
            attachment_id: attachment.id.clone().into(),
            duration_ms,
        };

        let icon = if loading {
            IconName::LoaderCircle
        } else if undecodable && path.is_some() {
            IconName::ExternalLink
        } else if playing {
            IconName::Pause
        } else {
            IconName::Play
        };
        let button = div()
            .id(SharedString::from(format!("voice-btn-{key}")))
            .size(px(34.))
            .flex_none()
            .rounded_full()
            .flex()
            .items_center()
            .justify_center()
            .bg(button_bg)
            .child(Icon::new(icon).size(px(15.)).text_color(button_icon_color))
            .when(local, |el| el.opacity(0.55))
            .when(!local, |el| {
                let view = view.clone();
                let target = target.clone();
                let open_path = path.clone().filter(|_| undecodable);
                el.cursor_pointer().on_click(move |_, _, cx| {
                    cx.stop_propagation();
                    if let Some(path) = &open_path {
                        crate::desktop::open_local(path, cx);
                        return;
                    }
                    view.update(cx, |this, cx| {
                        this.toggle_voice_playback(
                            &target.thread_id,
                            &target.message_id,
                            &target.attachment_id,
                            cx,
                        )
                    });
                })
            });

        let levels = if attachment.waveform.is_empty() {
            pseudo_waveform(&key, WAVEFORM_BARS)
        } else {
            bucket_waveform(&attachment.waveform, WAVEFORM_BARS)
        };
        let played_bars = progress * WAVEFORM_BARS as f32;
        let bars = levels.into_iter().enumerate().map(|(index, level)| {
            let played = playback.is_some() && (index as f32) < played_bars;
            let color = if played {
                played_color
            } else {
                unplayed_color
            };
            let height = (level * WAVE_HEIGHT).max(3.);
            div()
                .id(SharedString::from(format!("voice-bar-{key}-{index}")))
                .h_full()
                .w(px(BAR_WIDTH + BAR_GAP))
                .flex()
                .items_center()
                .child(
                    div()
                        .w(px(BAR_WIDTH))
                        .h(px(height))
                        .rounded_full()
                        .bg(color),
                )
                .when(!local && !undecodable, |el| {
                    let view = view.clone();
                    let target = target.clone();
                    let fraction = (index as f32 + 0.5) / WAVEFORM_BARS as f32;
                    el.cursor_pointer().on_click(move |_, _, cx| {
                        cx.stop_propagation();
                        view.update(cx, |this, cx| {
                            this.seek_voice(
                                &target.thread_id,
                                &target.message_id,
                                &target.attachment_id,
                                fraction,
                                target.duration_ms,
                                cx,
                            )
                        });
                    })
                })
        });

        let label: SharedString = if undecodable {
            ts!("voice_view.label.open")
        } else if unavailable {
            ts!("voice_view.label.unavailable")
        } else {
            match (playback, duration_ms) {
                (Some(playback), Some(duration)) if !playback.loading => SharedString::from(
                    format!(
                        "{} / {}",
                        format_duration(playback.position_ms),
                        format_duration(duration)
                    ),
                ),
                (Some(playback), None) if !playback.loading => {
                    time_label(Some(playback.position_ms))
                }
                _ => time_label(duration_ms),
            }
        };
        let round = px(super::message::BUBBLE_RADIUS);
        let tail = px(super::message::BUBBLE_TAIL);
        let speed_button = (playback.is_some() && !local && !undecodable).then(|| {
            let label = match self.settings.voice_speed {
                VoiceSpeed::Normal => ts!("voice_view.speed.normal"),
                VoiceSpeed::Faster => ts!("voice_view.speed.faster"),
                VoiceSpeed::Fast => ts!("voice_view.speed.fast"),
                VoiceSpeed::Double => ts!("voice_view.speed.double"),
            };
            let view = view.clone();
            div()
                .id(SharedString::from(format!("voice-speed-{key}")))
                .flex_none()
                .px(px(7.))
                .py(px(2.))
                .rounded_full()
                .bg(with_alpha(if outgoing { fg } else { p.accent_text }, 0.16))
                .text_size(px(11.5))
                .font_weight(FontWeight::BOLD)
                .text_color(if outgoing { fg } else { p.accent_text })
                .cursor_pointer()
                .child(label)
                .on_click(move |_, _, cx| {
                    cx.stop_propagation();
                    view.update(cx, |this, cx| this.cycle_voice_speed(cx));
                })
        });

        Some(
            h_flex()
                .id(SharedString::from(format!("voice-{key}")))
                .gap(px(10.))
                .pl(px(7.))
                .pr(px(14.))
                .py(px(7.))
                .rounded_tl(round)
                .rounded_tr(round)
                .rounded_bl(if outgoing { round } else { tail })
                .rounded_br(if outgoing { tail } else { round })
                .bg(bg)
                .text_color(fg)
                .child(button)
                .child(
                    h_flex()
                        .h(px(WAVE_HEIGHT))
                        .items_center()
                        .children(bars),
                )
                .child(
                    div()
                        .min_w(px(34.))
                        .text_size(px(12.5))
                        .font_weight(FontWeight::SEMIBOLD)
                        .text_color(if outgoing { fg } else { p.text_muted })
                        .child(label),
                )
                .children(speed_button)
                .into_any_element(),
        )
    }

    pub(super) fn render_voice_button(
        &self,
        thread: &Thread,
        p: &Palette,
        cx: &mut Context<Self>,
    ) -> Option<AnyElement> {
        if thread.blocked {
            return None;
        }
        Some(
            icon_button("voice-record", IconName::Mic, false, p)
                .on_click(cx.listener(|this, _, _, cx| this.start_voice_recording(cx)))
                .into_any_element(),
        )
    }

    pub(super) fn render_recording_bar(
        &self,
        p: &Palette,
        cx: &mut Context<Self>,
    ) -> Option<AnyElement> {
        let recording = self.voice.recording.as_ref()?;
        let history = recording.levels.len();
        let slots = 48usize;
        let meter = (0..slots).map(|slot| {
            let level = (slot + history)
                .checked_sub(slots)
                .and_then(|index| recording.levels.get(index))
                .copied()
                .unwrap_or(0.0);
            div()
                .w(px(BAR_WIDTH))
                .h(px((level * METER_HEIGHT).max(3.)))
                .rounded_full()
                .bg(if level > 0.0 {
                    p.accent_text
                } else {
                    with_alpha(p.text_faint, 0.5)
                })
        });
        let center = if recording.limit_reached {
            div()
                .flex_1()
                .min_w_0()
                .text_size(px(13.))
                .font_weight(FontWeight::SEMIBOLD)
                .text_color(p.warning)
                .child(ts!("voice_view.recording.max_reached"))
                .into_any_element()
        } else if recording.ready {
            h_flex()
                .flex_1()
                .min_w_0()
                .h(px(METER_HEIGHT + 4.))
                .gap(px(BAR_GAP))
                .items_center()
                .justify_end()
                .overflow_hidden()
                .children(meter)
                .into_any_element()
        } else {
            div()
                .flex_1()
                .text_size(px(13.))
                .text_color(p.text_muted)
                .child(ts!("voice_view.recording.opening_microphone"))
                .into_any_element()
        };
        Some(
            composer_field(p)
                .items_center()
                .child(
                    icon_button("voice-cancel", IconName::Trash, false, p)
                        .on_click(cx.listener(|this, _, _, cx| this.cancel_voice_recording(cx))),
                )
                .child(
                    h_flex()
                        .flex_1()
                        .min_w_0()
                        .h(px(40.))
                        .px_2()
                        .gap_3()
                        .items_center()
                        .child(div().size(px(9.)).flex_none().rounded_full().bg(
                            if recording.limit_reached {
                                p.text_faint
                            } else {
                                p.danger
                            },
                        ))
                        .child(center)
                        .child(
                            h_flex()
                                .flex_none()
                                .gap_1()
                                .text_size(px(13.))
                                .child(
                                    div()
                                        .text_color(p.text)
                                        .font_weight(FontWeight::SEMIBOLD)
                                        .child(time_label(Some(recording.elapsed_ms))),
                                )
                                .child(
                                    div()
                                        .text_color(p.text_faint)
                                        .child(SharedString::from(format!(
                                            "/ {}",
                                            format_duration(crate::audio::MAX_RECORDING_MS)
                                        ))),
                                ),
                        ),
                )
                .child(
                    div()
                        .id("voice-send")
                        .size(px(40.))
                        .flex_none()
                        .rounded(px(12.))
                        .flex()
                        .items_center()
                        .justify_center()
                        .bg(p.accent)
                        .when(recording.ready, |el| {
                            let hover = p.accent_strong;
                            el.cursor_pointer()
                                .hover(move |style| style.bg(hover))
                                .on_click(cx.listener(|this, _, _, cx| {
                                    this.send_voice_recording(cx)
                                }))
                        })
                        .when(!recording.ready, |el| el.opacity(0.5))
                        .child(Icon::new(IconName::Send).size(px(18.)).text_color(p.on_accent)),
                )
                .into_any_element(),
        )
    }
}
