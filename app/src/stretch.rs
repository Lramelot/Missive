use std::{
    collections::VecDeque,
    f32::consts::PI,
    sync::{
        Arc,
        atomic::{AtomicU32, AtomicU64, Ordering},
    },
    time::Duration,
};

use rodio::{ChannelCount, Sample, SampleRate, Source, source::SeekError};

const WINDOW_MS: usize = 30;
const SEARCH_MS: usize = 10;
const SEARCH_STEP: usize = 2;
const COMPARE_STEP: usize = 4;
const MIN_SPEED: f32 = 0.5;
const MAX_SPEED: f32 = 3.0;

pub(crate) fn speed_cell(speed: f32) -> Arc<AtomicU32> {
    Arc::new(AtomicU32::new(speed.to_bits()))
}

pub(crate) struct TimeStretch<S: Source> {
    inner: S,
    channels: usize,
    speed: Arc<AtomicU32>,
    position: Arc<AtomicU64>,
    window: Vec<f32>,
    hop: usize,
    search: usize,
    input: Vec<Sample>,
    input_start: u64,
    next: f64,
    previous: Option<u64>,
    overlap: Vec<Sample>,
    out: VecDeque<Sample>,
    exhausted: bool,
    done: bool,
}

impl<S: Source> TimeStretch<S> {
    pub(crate) fn new(inner: S, speed: Arc<AtomicU32>, position: Arc<AtomicU64>) -> Self {
        let channels = usize::from(inner.channels().get());
        let rate = inner.sample_rate().get() as usize;
        let size = (rate * WINDOW_MS / 1000).max(4) & !1;
        let window = (0..size)
            .map(|n| 0.5 - 0.5 * (2.0 * PI * n as f32 / size as f32).cos())
            .collect();
        position.store(0, Ordering::Relaxed);
        Self {
            inner,
            channels,
            speed,
            position,
            window,
            hop: size / 2,
            search: rate * SEARCH_MS / 1000,
            input: Vec::new(),
            input_start: 0,
            next: 0.0,
            previous: None,
            overlap: vec![0.0; size / 2 * channels],
            out: VecDeque::new(),
            exhausted: false,
            done: false,
        }
    }

    fn current_speed(&self) -> f32 {
        let speed = f32::from_bits(self.speed.load(Ordering::Relaxed));
        if speed.is_finite() {
            speed.clamp(MIN_SPEED, MAX_SPEED)
        } else {
            1.0
        }
    }

    fn available(&self) -> u64 {
        self.input_start + (self.input.len() / self.channels) as u64
    }

    fn fill_to(&mut self, frame: u64) {
        while !self.exhausted && self.available() < frame {
            for _ in 0..self.channels {
                match self.inner.next() {
                    Some(sample) => self.input.push(sample),
                    None => {
                        self.exhausted = true;
                        break;
                    }
                }
            }
        }
        let whole = self.input.len() / self.channels * self.channels;
        self.input.truncate(whole);
    }

    fn sample(&self, frame: u64, channel: usize) -> Sample {
        frame
            .checked_sub(self.input_start)
            .and_then(|offset| self.input.get(offset as usize * self.channels + channel))
            .copied()
            .unwrap_or(0.0)
    }

    fn mono(&self, frame: u64) -> Sample {
        (0..self.channels)
            .map(|channel| self.sample(frame, channel))
            .sum::<Sample>()
            / self.channels as Sample
    }

    fn best_start(&self, nominal: u64, reference: u64) -> u64 {
        let lowest = nominal
            .saturating_sub(self.search as u64)
            .max(self.input_start);
        let highest = nominal + self.search as u64;
        let mut best = (nominal, f32::MIN);
        for candidate in (lowest..=highest).step_by(SEARCH_STEP) {
            let mut correlation = 0.0;
            let mut energy = 1e-9;
            for i in (0..self.hop as u64).step_by(COMPARE_STEP) {
                let value = self.mono(candidate + i);
                correlation += value * self.mono(reference + i);
                energy += value * value;
            }
            let score = correlation / energy.sqrt();
            if score > best.1 {
                best = (candidate, score);
            }
        }
        best.0
    }

    fn step(&mut self) {
        let speed = self.current_speed();
        let nominal = self.next.round() as u64;
        let search = match self.previous {
            Some(_) if (speed - 1.0).abs() > 1e-3 => self.search as u64,
            _ => 0,
        };
        self.fill_to(nominal + search + self.window.len() as u64);
        if self.exhausted && nominal >= self.available() {
            self.out.extend(self.overlap.drain(..));
            self.done = true;
            return;
        }
        let start = match self.previous {
            Some(previous) if search > 0 => self.best_start(nominal, previous + self.hop as u64),
            _ => nominal,
        };
        for i in 0..self.hop {
            for channel in 0..self.channels {
                let index = i * self.channels + channel;
                let incoming = self.window[i] * self.sample(start + i as u64, channel);
                self.out.push_back(self.overlap[index] + incoming);
                self.overlap[index] =
                    self.window[self.hop + i] * self.sample(start + (self.hop + i) as u64, channel);
            }
        }
        self.position.store(start, Ordering::Relaxed);
        self.previous = Some(start);
        self.next += self.hop as f64 * f64::from(speed);
        let keep =
            (start + self.hop as u64).min((self.next as u64).saturating_sub(self.search as u64));
        if keep > self.input_start {
            let drop = ((keep - self.input_start) as usize * self.channels).min(self.input.len());
            self.input.drain(..drop);
            self.input_start += (drop / self.channels) as u64;
        }
    }
}

impl<S: Source> Iterator for TimeStretch<S> {
    type Item = Sample;

    fn next(&mut self) -> Option<Sample> {
        loop {
            if let Some(sample) = self.out.pop_front() {
                return Some(sample);
            }
            if self.done {
                return None;
            }
            self.step();
        }
    }
}

impl<S: Source> Source for TimeStretch<S> {
    fn current_span_len(&self) -> Option<usize> {
        None
    }

    fn channels(&self) -> ChannelCount {
        self.inner.channels()
    }

    fn sample_rate(&self) -> SampleRate {
        self.inner.sample_rate()
    }

    fn total_duration(&self) -> Option<Duration> {
        None
    }

    fn try_seek(&mut self, pos: Duration) -> Result<(), SeekError> {
        self.inner.try_seek(pos)?;
        let frame = (pos.as_secs_f64() * f64::from(self.inner.sample_rate().get())) as u64;
        self.input.clear();
        self.input_start = frame;
        self.next = frame as f64;
        self.previous = None;
        self.overlap.fill(0.0);
        self.out.clear();
        self.exhausted = false;
        self.done = false;
        self.position.store(frame, Ordering::Relaxed);
        Ok(())
    }
}

#[cfg(test)]
mod tests {
    use std::num::NonZero;

    use rodio::buffer::SamplesBuffer;

    use super::*;

    const RATE: u32 = 48_000;

    fn tone(seconds: f32, channels: u16) -> Vec<f32> {
        let frames = (RATE as f32 * seconds) as usize;
        (0..frames)
            .flat_map(|n| {
                let value = 0.5 * (2.0 * PI * 220.0 * n as f32 / RATE as f32).sin();
                std::iter::repeat_n(value, usize::from(channels))
            })
            .collect()
    }

    fn stretch(data: Vec<f32>, channels: u16, speed: f32) -> (Vec<f32>, Arc<AtomicU64>) {
        let source = SamplesBuffer::new(
            NonZero::new(channels).unwrap(),
            NonZero::new(RATE).unwrap(),
            data,
        );
        let position = Arc::new(AtomicU64::new(0));
        let out = TimeStretch::new(source, speed_cell(speed), position.clone()).collect();
        (out, position)
    }

    fn crossings(samples: &[f32]) -> usize {
        samples
            .windows(2)
            .filter(|pair| pair[0] < 0.0 && pair[1] >= 0.0)
            .count()
    }

    #[test]
    fn normal_speed_reproduces_the_input() {
        let input = tone(1.0, 1);
        let (out, position) = stretch(input.clone(), 1, 1.0);
        let hop = (RATE as usize * WINDOW_MS / 1000) / 2;
        assert!(out.len() >= input.len());
        for (index, (a, b)) in input.iter().zip(&out).enumerate().skip(hop) {
            assert!((a - b).abs() < 1e-4, "sample {index}: {a} vs {b}");
        }
        assert!(position.load(Ordering::Relaxed) > RATE as u64 * 9 / 10);
    }

    #[test]
    fn faster_speeds_shorten_without_changing_pitch() {
        let input = tone(2.0, 2);
        let input_crossings = crossings(&input.iter().step_by(2).copied().collect::<Vec<_>>());
        for speed in [1.25, 1.5, 2.0] {
            let (out, _) = stretch(input.clone(), 2, speed);
            let left: Vec<f32> = out.iter().step_by(2).copied().collect();
            let expected = input.len() as f32 / 2.0 / speed;
            assert!(
                (left.len() as f32 - expected).abs() < RATE as f32 * 0.05,
                "{speed}x lasts {} frames, expected about {expected}",
                left.len()
            );
            let per_second = crossings(&left) as f32 / (left.len() as f32 / RATE as f32);
            let original = input_crossings as f32 / 2.0;
            assert!(
                (per_second - original).abs() < original * 0.03,
                "{speed}x changed the pitch: {per_second} vs {original} cycles per second"
            );
            assert!(
                out.chunks(2)
                    .all(|frame| (frame[0] - frame[1]).abs() < 1e-6)
            );
        }
    }

    #[test]
    fn seeking_moves_the_content_position() {
        let source = SamplesBuffer::new(
            NonZero::new(1).unwrap(),
            NonZero::new(RATE).unwrap(),
            tone(2.0, 1),
        );
        let position = Arc::new(AtomicU64::new(0));
        let mut stretch = TimeStretch::new(source, speed_cell(2.0), position.clone());
        stretch.try_seek(Duration::from_millis(1500)).unwrap();
        assert_eq!(position.load(Ordering::Relaxed), RATE as u64 * 3 / 2);
        let remaining = stretch.count();
        assert!((remaining as f32 - RATE as f32 / 4.0).abs() < RATE as f32 * 0.05);
    }

    #[test]
    fn invalid_speeds_fall_back_to_a_safe_range() {
        let (out, _) = stretch(tone(1.0, 1), 1, f32::NAN);
        assert!((out.len() as f32 - RATE as f32).abs() < RATE as f32 * 0.05);
        let (out, _) = stretch(tone(1.0, 1), 1, 100.0);
        assert!((out.len() as f32 - RATE as f32 / MAX_SPEED).abs() < RATE as f32 * 0.05);
    }
}
