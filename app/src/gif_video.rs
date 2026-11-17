use std::{path::Path, sync::Arc};

use gpui_kit::RenderImage;
use image::RgbaImage;

const MAX_SIDE: u32 = 320;
#[cfg_attr(not(windows), allow(dead_code))]
const MAX_FRAMES: usize = 150;
#[cfg_attr(not(windows), allow(dead_code))]
const MIN_FRAME_MS: i64 = 40;
#[cfg_attr(not(windows), allow(dead_code))]
const MAX_DURATION_MS: i64 = 15_000;
#[cfg_attr(not(windows), allow(dead_code))]
const FALLBACK_DELAY_MS: u32 = 100;

pub const SUPPORTED: bool = cfg!(windows);

pub fn is_gif_file(path: &Path) -> bool {
    use std::io::Read as _;
    let mut magic = [0u8; 6];
    std::fs::File::open(path)
        .and_then(|mut file| file.read_exact(&mut magic))
        .is_ok()
        && (&magic == b"GIF87a" || &magic == b"GIF89a")
}

#[cfg_attr(not(windows), allow(dead_code))]
fn output_size(width: u32, height: u32) -> (u32, u32) {
    let scale = (MAX_SIDE as f64 / width.max(height) as f64).min(1.);
    (
        ((width as f64 * scale).round() as u32).max(1),
        ((height as f64 * scale).round() as u32).max(1),
    )
}

#[cfg_attr(not(windows), allow(dead_code))]
fn scale_rows<'a>(
    width: u32,
    height: u32,
    out_width: u32,
    out_height: u32,
    row: impl Fn(usize) -> Option<&'a [u8]>,
) -> Option<RgbaImage> {
    if width == 0 || height == 0 {
        return None;
    }
    let mut out = vec![0u8; out_width as usize * out_height as usize * 4];
    for y in 0..out_height as usize {
        let source = row(y * height as usize / out_height as usize)?;
        if source.len() < width as usize * 4 {
            return None;
        }
        for x in 0..out_width as usize {
            let from = x * width as usize / out_width as usize * 4;
            let to = (y * out_width as usize + x) * 4;
            out[to..to + 3].copy_from_slice(&source[from..from + 3]);
            out[to + 3] = 255;
        }
    }
    RgbaImage::from_raw(out_width, out_height, out)
}

#[cfg_attr(not(windows), allow(dead_code))]
fn scale_bgra(
    source: &[u8],
    width: u32,
    height: u32,
    stride: i32,
    out_width: u32,
    out_height: u32,
) -> Option<RgbaImage> {
    let pitch = stride.unsigned_abs() as usize;
    if pitch < width as usize * 4 || source.len() < pitch * height as usize {
        return None;
    }
    scale_rows(width, height, out_width, out_height, |y| {
        let row = if stride < 0 { height as usize - 1 - y } else { y };
        source.get(row * pitch..row * pitch + width as usize * 4)
    })
}

#[cfg_attr(not(windows), allow(dead_code))]
fn inferred_stride(length: usize, width: u32, height: u32, default: i32) -> i32 {
    let minimum = width as usize * 4;
    let measured = (height > 0)
        .then(|| length / height as usize)
        .filter(|pitch| *pitch >= minimum && length % height as usize == 0);
    match measured {
        Some(pitch) if default < 0 => -(pitch as i32),
        Some(pitch) => pitch as i32,
        None => default,
    }
}

#[cfg(windows)]
pub fn decode(path: &Path) -> anyhow::Result<Arc<RenderImage>> {
    use windows::Win32::Media::MediaFoundation::{MF_VERSION, MFSTARTUP_LITE, MFShutdown, MFStartup};
    use windows::Win32::System::Com::{COINIT_MULTITHREADED, CoInitializeEx, CoUninitialize};

    struct Session {
        com: bool,
    }

    impl Drop for Session {
        fn drop(&mut self) {
            unsafe {
                let _ = MFShutdown();
                if self.com {
                    CoUninitialize();
                }
            }
        }
    }

    let com = unsafe { CoInitializeEx(None, COINIT_MULTITHREADED) }.is_ok();
    if let Err(err) = unsafe { MFStartup(MF_VERSION, MFSTARTUP_LITE) } {
        if com {
            unsafe { CoUninitialize() };
        }
        return Err(err.into());
    }
    let _session = Session { com };
    let frames = read_frames(path)?;
    anyhow::ensure!(!frames.is_empty(), "no video frame decoded");
    Ok(Arc::new(RenderImage::new(frames)))
}

#[cfg(windows)]
fn read_frames(path: &Path) -> anyhow::Result<Vec<image::Frame>> {
    use anyhow::Context as _;
    use image::{Delay, Frame};
    use windows::Win32::Media::MediaFoundation::{
        IMFAttributes, IMFSample, MF_MT_DEFAULT_STRIDE, MF_MT_FRAME_SIZE, MF_MT_MAJOR_TYPE,
        MF_MT_SUBTYPE, MF_SOURCE_READER_ALL_STREAMS, MF_SOURCE_READER_ENABLE_VIDEO_PROCESSING,
        MF_SOURCE_READER_FIRST_VIDEO_STREAM, MF_SOURCE_READERF_ENDOFSTREAM, MFCreateAttributes,
        MFCreateMediaType, MFCreateSourceReaderFromURL, MFMediaType_Video, MFVideoFormat_RGB32,
    };
    use windows::core::HSTRING;

    let delay = |ms: i64| Delay::from_numer_denom_ms(ms.clamp(20, 10_000) as u32, 1);

    unsafe {
        let mut attributes: Option<IMFAttributes> = None;
        MFCreateAttributes(&mut attributes, 1)?;
        let attributes = attributes.context("no Media Foundation attributes")?;
        attributes.SetUINT32(&MF_SOURCE_READER_ENABLE_VIDEO_PROCESSING, 1)?;
        let reader = MFCreateSourceReaderFromURL(&HSTRING::from(path.as_os_str()), &attributes)?;
        let video = MF_SOURCE_READER_FIRST_VIDEO_STREAM.0 as u32;
        reader.SetStreamSelection(MF_SOURCE_READER_ALL_STREAMS.0 as u32, false)?;
        reader.SetStreamSelection(video, true)?;
        let wanted = MFCreateMediaType()?;
        wanted.SetGUID(&MF_MT_MAJOR_TYPE, &MFMediaType_Video)?;
        wanted.SetGUID(&MF_MT_SUBTYPE, &MFVideoFormat_RGB32)?;
        reader.SetCurrentMediaType(video, None, &wanted)?;
        let actual = reader.GetCurrentMediaType(video)?;
        let packed = actual.GetUINT64(&MF_MT_FRAME_SIZE)?;
        let (width, height) = ((packed >> 32) as u32, packed as u32);
        anyhow::ensure!(
            (1..=8192).contains(&width) && (1..=8192).contains(&height),
            "unexpected video size {width}x{height}"
        );
        let stride = actual
            .GetUINT32(&MF_MT_DEFAULT_STRIDE)
            .map(|stride| stride as i32)
            .unwrap_or((width * 4) as i32);
        let (out_width, out_height) = output_size(width, height);

        let mut frames = Vec::new();
        let mut pending: Option<(RgbaImage, i64)> = None;
        let mut last_delay = FALLBACK_DELAY_MS as i64;
        loop {
            let mut flags = 0u32;
            let mut timestamp = 0i64;
            let mut sample: Option<IMFSample> = None;
            reader.ReadSample(
                video,
                0,
                None,
                Some(&mut flags),
                Some(&mut timestamp),
                Some(&mut sample),
            )?;
            if flags & MF_SOURCE_READERF_ENDOFSTREAM.0 as u32 != 0 {
                break;
            }
            let Some(sample) = sample else { continue };
            let at_ms = timestamp / 10_000;
            if at_ms > MAX_DURATION_MS {
                break;
            }
            if let Some((_, previous)) = &pending {
                if at_ms - previous < MIN_FRAME_MS {
                    continue;
                }
            }
            let Some(image) =
                frame_image(&sample, width, height, stride, out_width, out_height)?
            else {
                continue;
            };
            if let Some((previous, previous_at)) = pending.take() {
                last_delay = at_ms - previous_at;
                frames.push(Frame::from_parts(previous, 0, 0, delay(last_delay)));
            }
            pending = Some((image, at_ms));
            if frames.len() >= MAX_FRAMES {
                break;
            }
        }
        if let Some((image, _)) = pending {
            frames.push(Frame::from_parts(image, 0, 0, delay(last_delay)));
        }
        Ok(frames)
    }
}

#[cfg(windows)]
unsafe fn frame_image(
    sample: &windows::Win32::Media::MediaFoundation::IMFSample,
    width: u32,
    height: u32,
    stride: i32,
    out_width: u32,
    out_height: u32,
) -> windows::core::Result<Option<RgbaImage>> {
    use windows::Win32::Media::MediaFoundation::IMF2DBuffer;
    use windows::core::Interface as _;

    unsafe {
        let buffer = sample.GetBufferByIndex(0)?;
        if let Ok(planar) = buffer.cast::<IMF2DBuffer>() {
            let mut scanline = std::ptr::null_mut();
            let mut pitch = 0i32;
            planar.Lock2D(&mut scanline, &mut pitch)?;
            let row_bytes = width as usize * 4;
            let image = if scanline.is_null() || (pitch.unsigned_abs() as usize) < row_bytes {
                None
            } else {
                scale_rows(width, height, out_width, out_height, |y| {
                    let start = scanline.offset(y as isize * pitch as isize);
                    Some(std::slice::from_raw_parts(start as *const u8, row_bytes))
                })
            };
            planar.Unlock2D()?;
            return Ok(image);
        }
        let buffer = sample.ConvertToContiguousBuffer()?;
        let mut data = std::ptr::null_mut();
        let mut length = 0u32;
        buffer.Lock(&mut data, None, Some(&mut length))?;
        let image = if data.is_null() {
            None
        } else {
            let bytes = std::slice::from_raw_parts(data as *const u8, length as usize);
            let stride = inferred_stride(bytes.len(), width, height, stride);
            scale_bgra(bytes, width, height, stride, out_width, out_height)
        };
        buffer.Unlock()?;
        Ok(image)
    }
}

#[cfg(not(windows))]
pub fn decode(_: &Path) -> anyhow::Result<Arc<RenderImage>> {
    anyhow::bail!("animated video decoding is only available on Windows")
}

#[cfg(test)]
mod tests {
    use super::{inferred_stride, is_gif_file, output_size, scale_bgra};

    #[test]
    fn is_gif_file_reads_the_signature() {
        let dir = std::env::temp_dir().join(format!("missive-gif-{}", std::process::id()));
        std::fs::create_dir_all(&dir).unwrap();
        let gif = dir.join("a.mp4");
        std::fs::write(&gif, b"GIF89a\x01\x00").unwrap();
        let mp4 = dir.join("b.gif");
        std::fs::write(&mp4, b"\x00\x00\x00\x18ftypmp42").unwrap();
        let short = dir.join("c");
        std::fs::write(&short, b"GIF").unwrap();
        assert!(is_gif_file(&gif));
        assert!(!is_gif_file(&mp4));
        assert!(!is_gif_file(&short));
        assert!(!is_gif_file(&dir.join("missing")));
        let _ = std::fs::remove_dir_all(&dir);
    }

    #[test]
    fn inferred_stride_reads_padding_from_the_buffer_length() {
        assert_eq!(inferred_stride(2 * 2 * 4, 2, 2, 8), 8);
        assert_eq!(inferred_stride(2 * 12, 2, 2, 8), 12);
        assert_eq!(inferred_stride(2 * 12, 2, 2, -8), -12);
        assert_eq!(inferred_stride(25, 2, 2, 8), 8);
        assert_eq!(inferred_stride(2 * 4, 2, 2, 8), 8);
    }

    fn pixel(image: &image::RgbaImage, x: u32, y: u32) -> [u8; 4] {
        image.get_pixel(x, y).0
    }

    #[test]
    fn output_size_caps_the_longest_side() {
        assert_eq!(output_size(640, 480), (320, 240));
        assert_eq!(output_size(480, 640), (240, 320));
        assert_eq!(output_size(200, 100), (200, 100));
        assert_eq!(output_size(4000, 2), (320, 1));
    }

    #[test]
    fn scale_bgra_keeps_rows_in_order_for_both_strides() {
        let top_down = [1, 2, 3, 0, 4, 5, 6, 0, 7, 8, 9, 0, 10, 11, 12, 0];
        let image = scale_bgra(&top_down, 2, 2, 8, 2, 2).unwrap();
        assert_eq!(pixel(&image, 0, 0), [1, 2, 3, 255]);
        assert_eq!(pixel(&image, 1, 1), [10, 11, 12, 255]);

        let bottom_up = [7, 8, 9, 0, 10, 11, 12, 0, 1, 2, 3, 0, 4, 5, 6, 0];
        let image = scale_bgra(&bottom_up, 2, 2, -8, 2, 2).unwrap();
        assert_eq!(pixel(&image, 0, 0), [1, 2, 3, 255]);
        assert_eq!(pixel(&image, 1, 1), [10, 11, 12, 255]);
    }

    #[test]
    fn scale_bgra_honours_padded_rows_and_rejects_short_buffers() {
        let padded = [1, 1, 1, 0, 9, 9, 9, 9, 2, 2, 2, 0, 9, 9, 9, 9];
        let image = scale_bgra(&padded, 1, 2, 8, 1, 2).unwrap();
        assert_eq!(pixel(&image, 0, 1), [2, 2, 2, 255]);
        assert!(scale_bgra(&padded[..12], 1, 2, 8, 1, 2).is_none());
        assert!(scale_bgra(&padded, 3, 2, 8, 3, 2).is_none());
    }

    #[cfg(windows)]
    #[test]
    #[ignore]
    fn decode_sample_video() {
        let Ok(sample) = std::env::var("MISSIVE_VIDEO_SAMPLE") else {
            return;
        };
        let image = super::decode(std::path::Path::new(&sample)).unwrap();
        let size = image.size(0);
        let (width, height) = (i32::from(size.width) as u32, i32::from(size.height) as u32);
        assert!(image.frame_count() > 1);
        assert!(width.max(height) <= super::MAX_SIDE);
        assert!(image.frame_count() <= super::MAX_FRAMES);
        if let Ok(out) = std::env::var("MISSIVE_VIDEO_FRAME_OUT") {
            let index = std::env::var("MISSIVE_VIDEO_FRAME_INDEX")
                .ok()
                .and_then(|index| index.parse::<usize>().ok())
                .unwrap_or(image.frame_count() / 2)
                .min(image.frame_count() - 1);
            std::fs::write(&out, image.as_bytes(index).unwrap()).unwrap();
            std::fs::write(
                format!("{out}.txt"),
                format!("{width} {height} {}", image.frame_count()),
            )
            .unwrap();
        }
    }

    #[test]
    fn scale_bgra_downsamples() {
        let source: Vec<u8> = (0..16u8).flat_map(|i| [i, i, i, 0]).collect();
        let image = scale_bgra(&source, 4, 4, 16, 2, 2).unwrap();
        assert_eq!(pixel(&image, 0, 0), [0, 0, 0, 255]);
        assert_eq!(pixel(&image, 1, 1), [10, 10, 10, 255]);
    }
}
