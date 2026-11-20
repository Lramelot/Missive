use std::{
    fs::File,
    hash::{DefaultHasher, Hash, Hasher},
    io::{Seek, Write},
    path::{Path, PathBuf},
    sync::{
        Arc, Mutex,
        atomic::{AtomicBool, AtomicU32, AtomicU64, Ordering},
        mpsc::{self, RecvTimeoutError},
    },
    time::{Duration, Instant},
};

use anyhow::{Context as _, anyhow};
use cpal::{
    FromSample, SampleFormat, SizedSample,
    traits::{DeviceTrait, HostTrait, StreamTrait},
};
use futures::channel::oneshot;
use rodio::Source as _;

pub(crate) const VOICE_RATE: u32 = 48_000;
pub(crate) const MAX_RECORDING_MS: i64 = 5 * 60 * 1000;
pub(crate) const WAVEFORM_BARS: usize = 36;

const TICK: Duration = Duration::from_millis(40);
const RELEASE_OUTPUT_AFTER: Duration = Duration::from_secs(20);
const HALT_CHECK: Duration = Duration::from_millis(100);
const STALE_RECORDING: Duration = Duration::from_secs(60 * 60);

#[derive(Clone, Debug, Default)]
pub(crate) struct PlaybackStatus {
    pub(crate) key: Option<String>,
    pub(crate) position_ms: i64,
    pub(crate) duration_ms: Option<i64>,
    pub(crate) playing: bool,
    pub(crate) finished: bool,
    pub(crate) undecodable: bool,
    pub(crate) no_device: bool,
}

enum PlayerCommand {
    Play {
        key: String,
        path: PathBuf,
        start_ms: i64,
    },
    Pause,
    Resume,
    Seek(i64),
    Stop,
}

pub(crate) struct AudioPlayer {
    commands: mpsc::Sender<PlayerCommand>,
    status: Arc<Mutex<PlaybackStatus>>,
    speed: Arc<AtomicU32>,
}

impl AudioPlayer {
    pub(crate) fn spawn(speed: f32) -> anyhow::Result<Self> {
        let (commands, receiver) = mpsc::channel();
        let status = Arc::new(Mutex::new(PlaybackStatus::default()));
        let speed = crate::stretch::speed_cell(speed);
        let shared = status.clone();
        let shared_speed = speed.clone();
        std::thread::Builder::new()
            .name("missive-audio-out".into())
            .spawn(move || player_loop(receiver, shared, shared_speed))?;
        Ok(Self {
            commands,
            status,
            speed,
        })
    }

    pub(crate) fn set_speed(&self, speed: f32) {
        self.speed.store(speed.to_bits(), Ordering::Relaxed);
    }

    pub(crate) fn play(&self, key: String, path: PathBuf, start_ms: i64) {
        if let Ok(mut status) = self.status.lock() {
            *status = PlaybackStatus {
                key: Some(key.clone()),
                position_ms: start_ms,
                playing: true,
                ..PlaybackStatus::default()
            };
        }
        let _ = self.commands.send(PlayerCommand::Play {
            key,
            path,
            start_ms,
        });
    }

    pub(crate) fn pause(&self) {
        let _ = self.commands.send(PlayerCommand::Pause);
    }

    pub(crate) fn resume(&self) {
        let _ = self.commands.send(PlayerCommand::Resume);
    }

    pub(crate) fn seek(&self, position_ms: i64) {
        let _ = self.commands.send(PlayerCommand::Seek(position_ms));
    }

    pub(crate) fn stop(&self) {
        update(&self.status, |s| *s = PlaybackStatus::default());
        let _ = self.commands.send(PlayerCommand::Stop);
    }

    pub(crate) fn status(&self) -> PlaybackStatus {
        self.status
            .lock()
            .map(|status| status.clone())
            .unwrap_or_default()
    }
}

fn update(status: &Mutex<PlaybackStatus>, apply: impl FnOnce(&mut PlaybackStatus)) {
    if let Ok(mut status) = status.lock() {
        apply(&mut status);
    }
}

fn millis(duration: Duration) -> i64 {
    i64::try_from(duration.as_millis()).unwrap_or(i64::MAX)
}

fn update_current(
    status: &Mutex<PlaybackStatus>,
    key: &str,
    apply: impl FnOnce(&mut PlaybackStatus),
) {
    update(status, |s| {
        if s.key.as_deref() == Some(key) {
            apply(s);
        }
    });
}

struct Current {
    player: rodio::Player,
    key: String,
    position: Arc<AtomicU64>,
    rate: u32,
}

impl Current {
    fn position_ms(&self) -> i64 {
        let frames = self.position.load(Ordering::Relaxed);
        i64::try_from(frames.saturating_mul(1000) / u64::from(self.rate.max(1))).unwrap_or(i64::MAX)
    }
}

fn player_loop(
    commands: mpsc::Receiver<PlayerCommand>,
    status: Arc<Mutex<PlaybackStatus>>,
    speed: Arc<AtomicU32>,
) {
    let mut output: Option<rodio::MixerDeviceSink> = None;
    let mut player: Option<Current> = None;
    let mut heard = false;
    let mut idle_since = Instant::now();
    loop {
        let received = if player.is_some() {
            commands.recv_timeout(TICK)
        } else if output.is_some() {
            commands.recv_timeout(RELEASE_OUTPUT_AFTER.saturating_sub(idle_since.elapsed()))
        } else {
            commands.recv().map_err(|_| RecvTimeoutError::Disconnected)
        };
        match received {
            Ok(PlayerCommand::Play {
                key,
                path,
                start_ms,
            }) => {
                if let Some(previous) = player.take() {
                    previous.player.stop();
                }
                heard = start_ms > 0;
                if output.is_none() {
                    match rodio::DeviceSinkBuilder::open_default_sink() {
                        Ok(mut sink) => {
                            sink.log_on_drop(false);
                            output = Some(sink);
                        }
                        Err(err) => {
                            eprintln!("[audio] output unavailable: {err}");
                            update_current(&status, &key, |s| {
                                s.playing = false;
                                s.finished = true;
                                s.no_device = true;
                            });
                            continue;
                        }
                    }
                }
                let Some(sink) = output.as_ref() else { continue };
                let decoder = File::open(&path)
                    .ok()
                    .and_then(|file| rodio::Decoder::try_from(file).ok());
                let Some(decoder) = decoder else {
                    update_current(&status, &key, |s| {
                        s.playing = false;
                        s.finished = true;
                        s.undecodable = true;
                    });
                    continue;
                };
                let duration_ms = decoder.total_duration().map(millis);
                let rate = decoder.sample_rate().get();
                let position = Arc::new(AtomicU64::new(0));
                let mut source =
                    crate::stretch::TimeStretch::new(decoder, speed.clone(), position.clone());
                if start_ms > 0 {
                    let _ = source.try_seek(Duration::from_millis(start_ms as u64));
                }
                let next = rodio::Player::connect_new(sink.mixer());
                next.append(source);
                next.play();
                update_current(&status, &key, |s| {
                    s.duration_ms = duration_ms;
                    s.playing = true;
                });
                player = Some(Current {
                    player: next,
                    key,
                    position,
                    rate,
                });
            }
            Ok(PlayerCommand::Pause) => {
                if let Some(current) = &player {
                    current.player.pause();
                    update_current(&status, &current.key, |s| s.playing = false);
                }
            }
            Ok(PlayerCommand::Resume) => {
                if let Some(current) = &player {
                    current.player.play();
                    update_current(&status, &current.key, |s| s.playing = true);
                }
            }
            Ok(PlayerCommand::Seek(position_ms)) => {
                if let Some(current) = &player {
                    let position_ms = position_ms.max(0);
                    let _ = current
                        .player
                        .try_seek(Duration::from_millis(position_ms as u64));
                    heard = heard || position_ms > 0;
                    update_current(&status, &current.key, |s| s.position_ms = position_ms);
                }
            }
            Ok(PlayerCommand::Stop) => {
                if let Some(previous) = player.take() {
                    previous.player.stop();
                }
                idle_since = Instant::now();
            }
            Err(RecvTimeoutError::Timeout) => {
                if player.is_none() && idle_since.elapsed() >= RELEASE_OUTPUT_AFTER {
                    output = None;
                }
            }
            Err(RecvTimeoutError::Disconnected) => break,
        }
        let finished = match &player {
            Some(current) => {
                let key = &current.key;
                let position_ms = current.position_ms();
                heard = heard || position_ms > 0;
                if current.player.empty() {
                    let undecodable = !heard;
                    update_current(&status, key, |s| {
                        s.playing = false;
                        s.finished = true;
                        s.undecodable = undecodable;
                        s.position_ms = s.duration_ms.unwrap_or(s.position_ms);
                    });
                    true
                } else {
                    update_current(&status, key, |s| s.position_ms = position_ms);
                    false
                }
            }
            None => false,
        };
        if finished {
            player = None;
            idle_since = Instant::now();
        }
    }
}

pub(crate) struct Recording {
    pub(crate) path: PathBuf,
    pub(crate) duration_ms: i64,
    pub(crate) waveform: Vec<f32>,
}

struct RecorderShared {
    level: AtomicU32,
    frames: AtomicU64,
    rate: AtomicU32,
    halted: AtomicBool,
}

pub(crate) struct Recorder {
    stop: Option<mpsc::Sender<bool>>,
    shared: Arc<RecorderShared>,
    done: Option<oneshot::Receiver<anyhow::Result<Option<Recording>>>>,
}

impl Recorder {
    pub(crate) fn start(path: PathBuf) -> (Self, oneshot::Receiver<anyhow::Result<()>>) {
        let (stop, stop_rx) = mpsc::channel();
        let (ready_tx, ready_rx) = oneshot::channel();
        let (done_tx, done_rx) = oneshot::channel();
        let shared = Arc::new(RecorderShared {
            level: AtomicU32::new(0),
            frames: AtomicU64::new(0),
            rate: AtomicU32::new(0),
            halted: AtomicBool::new(false),
        });
        let thread_shared = shared.clone();
        let spawned = std::thread::Builder::new()
            .name("missive-audio-in".into())
            .spawn(move || record_thread(path, stop_rx, ready_tx, done_tx, thread_shared));
        let recorder = Self {
            stop: Some(stop),
            shared,
            done: Some(done_rx),
        };
        match spawned {
            Ok(_) => (recorder, ready_rx),
            Err(err) => {
                let (failed_tx, failed_rx) = oneshot::channel();
                let _ = failed_tx.send(Err(anyhow!(err)));
                (recorder, failed_rx)
            }
        }
    }

    pub(crate) fn level(&self) -> f32 {
        f32::from_bits(self.shared.level.load(Ordering::Relaxed))
    }

    pub(crate) fn halted(&self) -> bool {
        self.shared.halted.load(Ordering::Relaxed)
    }

    pub(crate) fn elapsed_ms(&self) -> i64 {
        let rate = self.shared.rate.load(Ordering::Relaxed);
        if rate == 0 {
            return 0;
        }
        let frames = self.shared.frames.load(Ordering::Relaxed);
        i64::try_from(frames.saturating_mul(1000) / u64::from(rate)).unwrap_or(i64::MAX)
    }

    pub(crate) fn finish(mut self) -> oneshot::Receiver<anyhow::Result<Option<Recording>>> {
        if let Some(stop) = self.stop.take() {
            let _ = stop.send(true);
        }
        self.done.take().unwrap_or_else(|| oneshot::channel().1)
    }
}

impl Drop for Recorder {
    fn drop(&mut self) {
        if let Some(stop) = self.stop.take() {
            let _ = stop.send(false);
        }
    }
}

fn record_thread(
    path: PathBuf,
    stop: mpsc::Receiver<bool>,
    ready: oneshot::Sender<anyhow::Result<()>>,
    done: oneshot::Sender<anyhow::Result<Option<Recording>>>,
    shared: Arc<RecorderShared>,
) {
    if let Some(dir) = path.parent() {
        remove_stale_recordings(dir, &path);
    }
    let samples = Arc::new(Mutex::new(Vec::<f32>::new()));
    let (stream, rate) = match open_input(samples.clone(), shared.clone()) {
        Ok(opened) => opened,
        Err(err) => {
            let _ = ready.send(Err(err));
            let _ = done.send(Ok(None));
            return;
        }
    };
    shared.rate.store(rate, Ordering::Relaxed);
    let _ = ready.send(Ok(()));
    let limit = max_frames(rate);
    let mut stream = Some(stream);
    let keep = loop {
        match stop.recv_timeout(HALT_CHECK) {
            Ok(keep) => break keep,
            Err(RecvTimeoutError::Disconnected) => break false,
            Err(RecvTimeoutError::Timeout) => {
                if stream.is_some() && shared.frames.load(Ordering::Relaxed) >= limit {
                    stream = None;
                    shared.level.store(0, Ordering::Relaxed);
                    shared.halted.store(true, Ordering::Relaxed);
                }
            }
        }
    };
    drop(stream);
    if !keep {
        let _ = done.send(Ok(None));
        return;
    }
    let captured = samples
        .lock()
        .map(|mut samples| std::mem::take(&mut *samples))
        .unwrap_or_default();
    let _ = done.send(finish_recording(&path, captured, rate).map(Some));
}

fn remove_stale_recordings(dir: &Path, current: &Path) {
    let Ok(entries) = std::fs::read_dir(dir) else {
        return;
    };
    for entry in entries.flatten() {
        let path = entry.path();
        if path == current || !is_voice_temp(&path) {
            continue;
        }
        let stale = entry
            .metadata()
            .and_then(|meta| meta.modified())
            .ok()
            .and_then(|modified| modified.elapsed().ok())
            .is_some_and(|age| age >= STALE_RECORDING);
        if stale {
            let _ = std::fs::remove_file(path);
        }
    }
}

pub(crate) fn is_voice_temp(path: &Path) -> bool {
    path.file_name()
        .and_then(|name| name.to_str())
        .is_some_and(|name| name.starts_with("missive-voice-") && name.ends_with(".wav"))
}

fn finish_recording(path: &Path, captured: Vec<f32>, rate: u32) -> anyhow::Result<Recording> {
    let out_rate = output_rate(rate);
    let samples = if out_rate == rate {
        captured
    } else {
        resample_linear(&captured, rate, out_rate)
    };
    let file = File::create(path).context("création du fichier audio")?;
    write_wav(std::io::BufWriter::new(file), &samples, out_rate)?;
    let duration_ms =
        i64::try_from(samples.len() as u64 * 1000 / u64::from(out_rate)).unwrap_or(i64::MAX);
    Ok(Recording {
        path: path.to_path_buf(),
        duration_ms,
        waveform: waveform_from_samples(&samples, WAVEFORM_BARS),
    })
}

fn open_input(
    samples: Arc<Mutex<Vec<f32>>>,
    shared: Arc<RecorderShared>,
) -> anyhow::Result<(cpal::Stream, u32)> {
    let device = cpal::default_host()
        .default_input_device()
        .context("aucun micro")?;
    let supported = device.default_input_config()?;
    let config = supported.config();
    let rate = config.sample_rate;
    let stream = match supported.sample_format() {
        SampleFormat::F32 => build_input::<f32>(&device, config, samples, shared)?,
        SampleFormat::F64 => build_input::<f64>(&device, config, samples, shared)?,
        SampleFormat::I16 => build_input::<i16>(&device, config, samples, shared)?,
        SampleFormat::I32 => build_input::<i32>(&device, config, samples, shared)?,
        SampleFormat::I8 => build_input::<i8>(&device, config, samples, shared)?,
        SampleFormat::U16 => build_input::<u16>(&device, config, samples, shared)?,
        SampleFormat::U8 => build_input::<u8>(&device, config, samples, shared)?,
        other => return Err(anyhow!("format micro non pris en charge : {other:?}")),
    };
    stream.play()?;
    Ok((stream, rate))
}

fn build_input<T>(
    device: &cpal::Device,
    config: cpal::StreamConfig,
    samples: Arc<Mutex<Vec<f32>>>,
    shared: Arc<RecorderShared>,
) -> anyhow::Result<cpal::Stream>
where
    T: SizedSample,
    f32: FromSample<T>,
{
    let channels = usize::from(config.channels.max(1));
    let max_frames = max_frames(config.sample_rate);
    let mut mono = Vec::new();
    let stream = device.build_input_stream(
        config,
        move |data: &[T], _: &cpal::InputCallbackInfo| {
            mix_to_mono(data, channels, &mut mono);
            shared.level.store(rms(&mono).to_bits(), Ordering::Relaxed);
            let recorded = shared.frames.load(Ordering::Relaxed);
            if recorded >= max_frames {
                return;
            }
            let room = usize::try_from(max_frames - recorded).unwrap_or(usize::MAX);
            let take = mono.len().min(room);
            if let Ok(mut samples) = samples.lock() {
                samples.extend_from_slice(&mono[..take]);
            }
            shared.frames.fetch_add(take as u64, Ordering::Relaxed);
        },
        |err| eprintln!("[audio] input stream error: {err}"),
        None,
    )?;
    Ok(stream)
}

fn max_frames(rate: u32) -> u64 {
    u64::from(rate) * MAX_RECORDING_MS as u64 / 1000
}

pub(crate) fn output_rate(device_rate: u32) -> u32 {
    match device_rate {
        16_000 | 48_000 => device_rate,
        _ => VOICE_RATE,
    }
}

pub(crate) fn mix_to_mono<T>(data: &[T], channels: usize, out: &mut Vec<f32>)
where
    T: Copy,
    f32: FromSample<T>,
{
    out.clear();
    out.extend(data.chunks(channels.max(1)).map(|frame| {
        frame.iter().map(|sample| f32::from_sample_(*sample)).sum::<f32>() / frame.len() as f32
    }));
}

pub(crate) fn resample_linear(input: &[f32], from: u32, to: u32) -> Vec<f32> {
    if input.is_empty() || from == 0 || to == 0 {
        return Vec::new();
    }
    if from == to {
        return input.to_vec();
    }
    let out_len = ((input.len() as u64 * u64::from(to)) / u64::from(from)).max(1) as usize;
    let step = f64::from(from) / f64::from(to);
    (0..out_len)
        .map(|i| {
            let position = i as f64 * step;
            let index = position.floor() as usize;
            let fraction = (position - index as f64) as f32;
            let a = input[index.min(input.len() - 1)];
            let b = input[(index + 1).min(input.len() - 1)];
            a + (b - a) * fraction
        })
        .collect()
}

pub(crate) fn write_wav<W: Write + Seek>(
    writer: W,
    samples: &[f32],
    rate: u32,
) -> anyhow::Result<()> {
    let spec = hound::WavSpec {
        channels: 1,
        sample_rate: rate,
        bits_per_sample: 16,
        sample_format: hound::SampleFormat::Int,
    };
    let mut wav = hound::WavWriter::new(writer, spec)?;
    for sample in samples {
        let value = (sample.clamp(-1.0, 1.0) * f32::from(i16::MAX)).round() as i16;
        wav.write_sample(value)?;
    }
    wav.finalize()?;
    Ok(())
}

pub(crate) fn rms(samples: &[f32]) -> f32 {
    if samples.is_empty() {
        return 0.0;
    }
    (samples.iter().map(|s| s * s).sum::<f32>() / samples.len() as f32).sqrt()
}

pub(crate) fn waveform_from_samples(samples: &[f32], bars: usize) -> Vec<f32> {
    if bars == 0 || samples.is_empty() {
        return Vec::new();
    }
    let levels: Vec<f32> = (0..bars)
        .map(|bar| {
            let start = bar * samples.len() / bars;
            let end = ((bar + 1) * samples.len() / bars).max(start + 1).min(samples.len());
            rms(&samples[start.min(end)..end])
        })
        .collect();
    let peak = levels.iter().copied().fold(0.0f32, f32::max);
    if peak <= f32::EPSILON {
        return vec![0.0; bars];
    }
    levels.iter().map(|level| (level / peak).clamp(0.0, 1.0)).collect()
}

pub(crate) fn bucket_waveform(waveform: &[f32], bars: usize) -> Vec<f32> {
    if bars == 0 || waveform.is_empty() {
        return Vec::new();
    }
    (0..bars)
        .map(|bar| {
            let start = bar * waveform.len() / bars;
            let end = ((bar + 1) * waveform.len() / bars).max(start + 1).min(waveform.len());
            waveform[start.min(waveform.len() - 1)..end]
                .iter()
                .copied()
                .fold(0.0f32, f32::max)
                .clamp(0.0, 1.0)
        })
        .collect()
}

pub(crate) fn pseudo_waveform(seed: &str, bars: usize) -> Vec<f32> {
    let mut hasher = DefaultHasher::new();
    seed.hash(&mut hasher);
    let mut state = hasher.finish() | 1;
    (0..bars)
        .map(|_| {
            state ^= state << 13;
            state ^= state >> 7;
            state ^= state << 17;
            0.3 + (state % 1000) as f32 / 1000.0 * 0.35
        })
        .collect()
}

#[cfg(test)]
mod tests {
    use super::*;

    #[::core::prelude::v1::test]
    fn output_rate_keeps_supported_rates() {
        assert_eq!(output_rate(16_000), 16_000);
        assert_eq!(output_rate(48_000), 48_000);
        assert_eq!(output_rate(44_100), 48_000);
        assert_eq!(output_rate(96_000), 48_000);
    }

    #[::core::prelude::v1::test]
    fn mix_to_mono_averages_frames() {
        let mut out = vec![9.0];
        mix_to_mono(&[1.0f32, 0.0, 0.5, 0.5], 2, &mut out);
        assert_eq!(out, vec![0.5, 0.5]);
        mix_to_mono(&[0.25f32, -0.25], 1, &mut out);
        assert_eq!(out, vec![0.25, -0.25]);
        mix_to_mono(&[i16::MAX, i16::MAX], 2, &mut out);
        assert!((out[0] - 1.0).abs() < 1e-3);
    }

    #[::core::prelude::v1::test]
    fn resample_identity_and_lengths() {
        let input: Vec<f32> = (0..441).map(|i| i as f32 / 441.0).collect();
        assert_eq!(resample_linear(&input, 48_000, 48_000), input);
        assert_eq!(resample_linear(&input, 44_100, 48_000).len(), 480);
        assert_eq!(resample_linear(&input, 48_000, 16_000).len(), 147);
        assert!(resample_linear(&[], 44_100, 48_000).is_empty());
    }

    #[::core::prelude::v1::test]
    fn resample_interpolates_linearly() {
        let out = resample_linear(&[0.0, 1.0], 1, 2);
        assert_eq!(out, vec![0.0, 0.5, 1.0, 1.0]);
        let ramp: Vec<f32> = (0..100).map(|i| i as f32).collect();
        let up = resample_linear(&ramp, 44_100, 48_000);
        for pair in up.windows(2).take(100) {
            assert!(pair[1] >= pair[0]);
        }
        assert!((up[48] - 48.0 * 44_100.0 / 48_000.0).abs() < 1e-3);
    }

    #[::core::prelude::v1::test]
    fn write_wav_produces_pcm16_mono() {
        let mut buffer = std::io::Cursor::new(Vec::new());
        write_wav(&mut buffer, &[0.0, 1.0, -1.0, 2.0, 0.5], 48_000).unwrap();
        buffer.set_position(0);
        let mut reader = hound::WavReader::new(buffer).unwrap();
        let spec = reader.spec();
        assert_eq!(spec.channels, 1);
        assert_eq!(spec.sample_rate, 48_000);
        assert_eq!(spec.bits_per_sample, 16);
        assert_eq!(spec.sample_format, hound::SampleFormat::Int);
        let samples: Vec<i16> = reader.samples::<i16>().map(Result::unwrap).collect();
        assert_eq!(samples, vec![0, i16::MAX, -i16::MAX, i16::MAX, 16384]);
    }

    #[::core::prelude::v1::test]
    fn bucket_waveform_takes_peaks_and_stretches() {
        let source = [0.1, 0.9, 0.2, 0.4, 0.3, 0.8];
        assert_eq!(bucket_waveform(&source, 3), vec![0.9, 0.4, 0.8]);
        assert_eq!(bucket_waveform(&[0.5, 1.5], 4), vec![0.5, 0.5, 1.0, 1.0]);
        assert_eq!(bucket_waveform(&source, 6), source.to_vec());
        assert!(bucket_waveform(&[], 4).is_empty());
    }

    #[::core::prelude::v1::test]
    fn waveform_from_samples_is_normalised() {
        let mut samples = vec![0.0f32; 300];
        for sample in &mut samples[200..] {
            *sample = 0.5;
        }
        for sample in &mut samples[100..200] {
            *sample = 0.25;
        }
        let bars = waveform_from_samples(&samples, 3);
        assert_eq!(bars.len(), 3);
        assert_eq!(bars[0], 0.0);
        assert!((bars[1] - 0.5).abs() < 1e-6);
        assert!((bars[2] - 1.0).abs() < 1e-6);
        assert_eq!(waveform_from_samples(&[0.0; 10], 4), vec![0.0; 4]);
    }

    #[::core::prelude::v1::test]
    fn pseudo_waveform_is_stable_and_bounded() {
        let a = pseudo_waveform("attachment-1", 36);
        assert_eq!(a, pseudo_waveform("attachment-1", 36));
        assert_ne!(a, pseudo_waveform("attachment-2", 36));
        assert!(a.iter().all(|v| (0.3..=0.65).contains(v)));
    }

    #[::core::prelude::v1::test]
    fn max_frames_is_five_minutes() {
        assert_eq!(max_frames(48_000), 48_000 * 300);
        assert_eq!(max_frames(44_100), 44_100 * 300);
    }

    #[::core::prelude::v1::test]
    fn voice_temp_names() {
        assert!(is_voice_temp(Path::new("C:/tmp/missive-voice-1700000000000.wav")));
        assert!(!is_voice_temp(Path::new("C:/tmp/missive-voice-1.mp3")));
        assert!(!is_voice_temp(Path::new("C:/tmp/other.wav")));
    }

    #[::core::prelude::v1::test]
    fn finish_recording_resamples_and_writes_file() {
        let dir = std::env::temp_dir().join(format!("missive-audio-test-{}", std::process::id()));
        std::fs::create_dir_all(&dir).unwrap();
        let path = dir.join("clip.wav");
        let captured = vec![0.1f32; 44_100];
        let recording = finish_recording(&path, captured, 44_100).unwrap();
        assert_eq!(recording.duration_ms, 1000);
        assert_eq!(recording.waveform.len(), WAVEFORM_BARS);
        let reader = hound::WavReader::open(&path).unwrap();
        assert_eq!(reader.spec().sample_rate, 48_000);
        assert_eq!(reader.len(), 48_000);
        drop(reader);
        std::fs::remove_dir_all(&dir).unwrap();
    }
}
