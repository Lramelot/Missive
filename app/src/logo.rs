const TILE: [u8; 3] = [0x24, 0x46, 0xC7];
const MARK: [u8; 3] = [0xFF, 0xFF, 0xFF];
const DOT: [u8; 3] = [0xF2, 0xA9, 0x3B];
const SAMPLES: u32 = 4;

fn in_rounded_rect(x: f32, y: f32) -> bool {
    let (min, max, radius) = (2.0, 62.0, 15.0);
    if !(min..=max).contains(&x) || !(min..=max).contains(&y) {
        return false;
    }
    let cx = x.clamp(min + radius, max - radius);
    let cy = y.clamp(min + radius, max - radius);
    let (dx, dy) = (x - cx, y - cy);
    dx * dx + dy * dy <= radius * radius
}

fn segment_distance(px: f32, py: f32, a: (f32, f32), b: (f32, f32)) -> f32 {
    let (vx, vy) = (b.0 - a.0, b.1 - a.1);
    let (wx, wy) = (px - a.0, py - a.1);
    let t = ((wx * vx + wy * vy) / (vx * vx + vy * vy)).clamp(0.0, 1.0);
    let (dx, dy) = (wx - t * vx, wy - t * vy);
    (dx * dx + dy * dy).sqrt()
}

fn in_mark(x: f32, y: f32, width: f32) -> bool {
    const POINTS: [(f32, f32); 5] = [(15.0, 46.0), (15.0, 27.0), (32.0, 40.0), (49.0, 27.0), (49.0, 46.0)];
    POINTS
        .windows(2)
        .any(|pair| segment_distance(x, y, pair[0], pair[1]) <= width / 2.0)
}

fn circle_distance(x: f32, y: f32) -> f32 {
    let (dx, dy) = (x - 52.0, y - 12.0);
    (dx * dx + dy * dy).sqrt()
}

fn sample(x: f32, y: f32, mark_width: f32, dot: bool) -> Option<[u8; 3]> {
    if dot {
        let distance = circle_distance(x, y);
        if distance <= 7.0 {
            return Some(DOT);
        }
        if distance <= 10.0 {
            return in_rounded_rect(x, y).then_some(TILE);
        }
    }
    if !in_rounded_rect(x, y) {
        return None;
    }
    Some(if in_mark(x, y, mark_width) { MARK } else { TILE })
}

pub fn mark_width(size: u32) -> f32 {
    match size {
        0..=20 => 9.0,
        21..=40 => 7.0,
        _ => 6.5,
    }
}

pub fn render_rgba(size: u32, dot: bool) -> Vec<u8> {
    let width = mark_width(size);
    let scale = 64.0 / size as f32;
    let mut rgba = vec![0u8; (size * size * 4) as usize];
    for py in 0..size {
        for px in 0..size {
            let mut sum = [0u32; 3];
            let mut hits = 0u32;
            for sy in 0..SAMPLES {
                for sx in 0..SAMPLES {
                    let x = (px as f32 + (sx as f32 + 0.5) / SAMPLES as f32) * scale;
                    let y = (py as f32 + (sy as f32 + 0.5) / SAMPLES as f32) * scale;
                    if let Some(color) = sample(x, y, width, dot) {
                        hits += 1;
                        for channel in 0..3 {
                            sum[channel] += color[channel] as u32;
                        }
                    }
                }
            }
            if hits == 0 {
                continue;
            }
            let index = ((py * size + px) * 4) as usize;
            for channel in 0..3 {
                rgba[index + channel] = (sum[channel] / hits) as u8;
            }
            rgba[index + 3] = (hits * 255 / (SAMPLES * SAMPLES)) as u8;
        }
    }
    rgba
}

#[cfg(test)]
fn write_ico(sizes: &[u32]) -> Vec<u8> {
    let images: Vec<(u32, Vec<u8>)> = sizes
        .iter()
        .map(|&size| (size, dib(size, &render_rgba(size, size >= 32))))
        .collect();
    let mut out = Vec::new();
    out.extend_from_slice(&0u16.to_le_bytes());
    out.extend_from_slice(&1u16.to_le_bytes());
    out.extend_from_slice(&(images.len() as u16).to_le_bytes());
    let mut offset = 6 + 16 * images.len() as u32;
    for (size, data) in &images {
        let dimension = if *size >= 256 { 0 } else { *size as u8 };
        out.push(dimension);
        out.push(dimension);
        out.push(0);
        out.push(0);
        out.extend_from_slice(&1u16.to_le_bytes());
        out.extend_from_slice(&32u16.to_le_bytes());
        out.extend_from_slice(&(data.len() as u32).to_le_bytes());
        out.extend_from_slice(&offset.to_le_bytes());
        offset += data.len() as u32;
    }
    for (_, data) in images {
        out.extend_from_slice(&data);
    }
    out
}

const BADGE: [u8; 3] = [0xF2, 0xA9, 0x3B];
const BADGE_TEXT: [u8; 3] = [0x2A, 0x1A, 0x02];

const GLYPHS: [(char, [u8; 5]); 11] = [
    ('0', [0b111, 0b101, 0b101, 0b101, 0b111]),
    ('1', [0b010, 0b110, 0b010, 0b010, 0b111]),
    ('2', [0b111, 0b001, 0b111, 0b100, 0b111]),
    ('3', [0b111, 0b001, 0b111, 0b001, 0b111]),
    ('4', [0b101, 0b101, 0b111, 0b001, 0b001]),
    ('5', [0b111, 0b100, 0b111, 0b001, 0b111]),
    ('6', [0b111, 0b100, 0b111, 0b101, 0b111]),
    ('7', [0b111, 0b001, 0b001, 0b001, 0b001]),
    ('8', [0b111, 0b101, 0b111, 0b101, 0b111]),
    ('9', [0b111, 0b101, 0b111, 0b001, 0b111]),
    ('+', [0b000, 0b010, 0b111, 0b010, 0b000]),
];

pub fn badge_label(count: u32) -> String {
    if count > 9 {
        "9+".to_string()
    } else {
        count.to_string()
    }
}

fn glyph_hit(label: &[[u8; 5]], x: f32, y: f32) -> bool {
    if x < 0.0 || y < 0.0 || y >= 5.0 {
        return false;
    }
    let column = x as usize;
    let index = column / 4;
    let within = column % 4;
    if index >= label.len() || within == 3 {
        return false;
    }
    label[index][y as usize] & (0b100 >> within) != 0
}

struct Badge {
    cx: f32,
    cy: f32,
    radius: f32,
    gap: f32,
    unit: f32,
    origin_x: f32,
    origin_y: f32,
    glyphs: Vec<[u8; 5]>,
}

impl Badge {
    fn new(cx: f32, cy: f32, radius: f32, gap: f32, count: u32) -> Self {
        let glyphs: Vec<[u8; 5]> = badge_label(count)
            .chars()
            .filter_map(|c| GLYPHS.iter().find(|(g, _)| *g == c).map(|(_, rows)| *rows))
            .collect();
        let width_units = (glyphs.len() * 4).saturating_sub(1).max(1) as f32;
        let unit = (radius * 1.15 / 5.0).min(radius * 1.45 / width_units);
        Self {
            cx,
            cy,
            radius,
            gap,
            unit,
            origin_x: cx - width_units * unit / 2.0,
            origin_y: cy - 2.5 * unit,
            glyphs,
        }
    }

    fn sample(&self, x: f32, y: f32) -> Option<Option<[u8; 3]>> {
        let distance = ((x - self.cx).powi(2) + (y - self.cy).powi(2)).sqrt();
        if distance > self.radius + self.gap {
            return None;
        }
        if distance > self.radius {
            return Some(None);
        }
        let gx = (x - self.origin_x) / self.unit;
        let gy = (y - self.origin_y) / self.unit;
        Some(Some(if glyph_hit(&self.glyphs, gx, gy) {
            BADGE_TEXT
        } else {
            BADGE
        }))
    }
}

fn rasterize(size: u32, mut sample: impl FnMut(f32, f32) -> Option<[u8; 3]>) -> Vec<u8> {
    let mut rgba = vec![0u8; (size * size * 4) as usize];
    for py in 0..size {
        for px in 0..size {
            let mut sum = [0u32; 3];
            let mut hits = 0u32;
            for sy in 0..SAMPLES {
                for sx in 0..SAMPLES {
                    let x = px as f32 + (sx as f32 + 0.5) / SAMPLES as f32;
                    let y = py as f32 + (sy as f32 + 0.5) / SAMPLES as f32;
                    if let Some(color) = sample(x, y) {
                        hits += 1;
                        for channel in 0..3 {
                            sum[channel] += color[channel] as u32;
                        }
                    }
                }
            }
            if hits == 0 {
                continue;
            }
            let index = ((py * size + px) * 4) as usize;
            for channel in 0..3 {
                rgba[index + channel] = (sum[channel] / hits) as u8;
            }
            rgba[index + 3] = (hits * 255 / (SAMPLES * SAMPLES)) as u8;
        }
    }
    rgba
}

pub fn tray_rgba(size: u32, unread: u32) -> Vec<u8> {
    if unread == 0 {
        return render_rgba(size, false);
    }
    let s = size as f32;
    let badge = Badge::new(s * 0.69, s * 0.69, s * 0.31, s * 0.05, unread);
    let width = mark_width(size);
    let scale = 64.0 / s;
    rasterize(size, |x, y| match badge.sample(x, y) {
        Some(inside) => inside,
        None => sample(x * scale, y * scale, width, false),
    })
}

#[cfg(any(windows, test))]
pub fn overlay_rgba(size: u32, count: u32) -> Vec<u8> {
    let s = size as f32;
    let badge = Badge::new(s / 2.0, s / 2.0, s / 2.0 - 0.5, 0.0, count);
    rasterize(size, |x, y| badge.sample(x, y).flatten())
}

#[cfg(windows)]
pub fn overlay_resource(size: u32, count: u32) -> Vec<u8> {
    dib(size, &overlay_rgba(size, count))
}

#[cfg(windows)]
pub fn icon_resource(size: u32) -> Vec<u8> {
    dib(size, &render_rgba(size, size >= 32))
}

#[cfg(windows)]
fn dib(size: u32, rgba: &[u8]) -> Vec<u8> {
    let mask_row = size.div_ceil(32) * 4;
    let mut out = Vec::new();
    out.extend_from_slice(&40u32.to_le_bytes());
    out.extend_from_slice(&(size as i32).to_le_bytes());
    out.extend_from_slice(&((size * 2) as i32).to_le_bytes());
    out.extend_from_slice(&1u16.to_le_bytes());
    out.extend_from_slice(&32u16.to_le_bytes());
    out.extend_from_slice(&0u32.to_le_bytes());
    out.extend_from_slice(&(size * size * 4 + mask_row * size).to_le_bytes());
    out.extend_from_slice(&[0u8; 16]);
    for row in (0..size).rev() {
        for column in 0..size {
            let index = ((row * size + column) * 4) as usize;
            out.extend_from_slice(&[rgba[index + 2], rgba[index + 1], rgba[index], rgba[index + 3]]);
        }
    }
    out.extend(std::iter::repeat_n(0u8, (mask_row * size) as usize));
    out
}

#[cfg(test)]
mod tests {
    use super::*;

    #[::core::prelude::v1::test]
    fn icon_has_tile_mark_and_transparent_corners() {
        let rgba = render_rgba(64, true);
        let pixel = |x: usize, y: usize| &rgba[(y * 64 + x) * 4..(y * 64 + x) * 4 + 4];
        assert_eq!(pixel(0, 0)[3], 0);
        assert_eq!(&pixel(32, 55)[..3], &TILE);
        assert_eq!(&pixel(15, 36)[..3], &MARK);
        assert_eq!(&pixel(52, 12)[..3], &DOT);
    }

    #[::core::prelude::v1::test]
    fn badge_labels_cap_at_nine_plus() {
        assert_eq!(badge_label(1), "1");
        assert_eq!(badge_label(9), "9");
        assert_eq!(badge_label(10), "9+");
        assert_eq!(badge_label(250), "9+");
    }

    #[::core::prelude::v1::test]
    fn overlay_has_badge_color_and_digit() {
        let rgba = overlay_rgba(32, 3);
        let colors: Vec<[u8; 3]> = rgba
            .chunks(4)
            .filter(|px| px[3] == 255)
            .map(|px| [px[0], px[1], px[2]])
            .collect();
        assert!(colors.contains(&BADGE));
        assert!(colors.contains(&BADGE_TEXT));
        assert_eq!(rgba[3], 0);
    }

    #[::core::prelude::v1::test]
    fn tray_without_unread_is_plain_logo() {
        assert_eq!(tray_rgba(32, 0), render_rgba(32, false));
        assert_ne!(tray_rgba(32, 4), render_rgba(32, false));
    }

    #[::core::prelude::v1::test]
    fn ico_directory_lists_every_size() {
        let ico = write_ico(&[16, 32, 256]);
        assert_eq!(u16::from_le_bytes([ico[4], ico[5]]), 3);
        assert_eq!(ico[6], 16);
        assert_eq!(ico[6 + 32], 0);
    }

    #[::core::prelude::v1::test]
    #[ignore]
    fn regenerate_packaging_icon() {
        let path = std::path::Path::new(env!("CARGO_MANIFEST_DIR")).join("../packaging/missive.ico");
        std::fs::write(path, write_ico(&[16, 20, 24, 32, 40, 48, 64, 128, 256])).unwrap();
    }
}
