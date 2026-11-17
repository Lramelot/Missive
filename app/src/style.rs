use std::borrow::Cow;

use gpui_kit::component::{ActiveTheme as _, Theme, ThemeMode};
use gpui_kit::*;

use crate::settings::ThemePreference;

pub const FONT_FAMILY: &str = "Figtree";

pub fn register_fonts(cx: &mut App) {
    let fonts: Vec<Cow<'static, [u8]>> = vec![Cow::Borrowed(include_bytes!(
        "../assets/fonts/Figtree-Variable.ttf"
    ))];
    if let Err(err) = cx.text_system().add_fonts(fonts) {
        eprintln!("[style] font registration failed: {err:#}");
    }
}

pub fn apply_theme(preference: ThemePreference, window: Option<&mut Window>, cx: &mut App) {
    match preference {
        ThemePreference::System => Theme::sync_system_appearance(window, cx),
        ThemePreference::Light => Theme::change(ThemeMode::Light, window, cx),
        ThemePreference::Dark => Theme::change(ThemeMode::Dark, window, cx),
    }
    let palette = Palette::get(cx);
    Theme::update(cx, |theme| {
        theme.font_family = FONT_FAMILY.into();
        theme.primary = palette.accent;
        theme.primary_hover = palette.accent_strong;
        theme.primary_active = palette.accent_strong;
        theme.primary_foreground = palette.on_accent;
        theme.button_primary = palette.accent;
        theme.button_primary_hover = palette.accent_strong;
        theme.button_primary_active = palette.accent_strong;
        theme.button_primary_foreground = palette.on_accent;
        theme.ring = palette.accent;
        theme.caret = palette.accent_text;
        theme.input = palette.border_strong;
        theme.title_bar = palette.canvas;
        theme.title_bar_border = palette.canvas;
        theme.radius = px(10.);
        theme.radius_lg = px(14.);
    });
}

#[derive(Clone, Copy)]
pub struct Palette {
    pub dark: bool,
    pub accent: Hsla,
    pub accent_soft: Hsla,
    pub accent_strong: Hsla,
    pub accent_text: Hsla,
    pub on_accent: Hsla,
    pub bubble_out: Hsla,
    pub on_bubble_out: Hsla,
    pub canvas: Hsla,
    pub panel: Hsla,
    pub raised: Hsla,
    pub hover: Hsla,
    pub divider: Hsla,
    pub border_strong: Hsla,
    pub text: Hsla,
    pub text_muted: Hsla,
    pub text_faint: Hsla,
    pub bubble_in: Hsla,
    pub bubble_in_text: Hsla,
    pub field_bg: Hsla,
    pub chip_bg: Hsla,
    pub signal: Hsla,
    pub signal_soft: Hsla,
    pub signal_text: Hsla,
    pub on_signal: Hsla,
    pub danger: Hsla,
    pub online: Hsla,
    pub warning: Hsla,
    pub link: Hsla,
}

fn hex(value: u32) -> Hsla {
    rgb(value).into()
}

pub fn with_alpha(color: Hsla, alpha: f32) -> Hsla {
    Hsla { a: alpha, ..color }
}

impl Palette {
    pub fn get(cx: &App) -> Self {
        if cx.theme().is_dark() {
            Self {
                dark: true,
                accent: hex(0x3D5FE0),
                accent_soft: hex(0x222C4A),
                accent_strong: hex(0x5373EA),
                accent_text: hex(0x8EA2FF),
                on_accent: hex(0xFFFFFF),
                bubble_out: hex(0x3D5FE0),
                on_bubble_out: hex(0xFFFFFF),
                canvas: hex(0x121722),
                panel: hex(0x1A202C),
                raised: hex(0x202737),
                hover: hex(0x232A3A),
                divider: hex(0x262D3C),
                border_strong: hex(0x303849),
                text: hex(0xE7EAF0),
                text_muted: hex(0x9AA3B5),
                text_faint: hex(0x6E788B),
                bubble_in: hex(0x252D3E),
                bubble_in_text: hex(0xE7EAF0),
                field_bg: hex(0x121722),
                chip_bg: hex(0x252D3E),
                signal: hex(0xF2A93B),
                signal_soft: hex(0x2F2614),
                signal_text: hex(0xF5D79B),
                on_signal: hex(0x2A1A02),
                danger: hex(0xF07A6A),
                online: hex(0x4CC38A),
                warning: hex(0xE0B04A),
                link: hex(0x8EA2FF),
            }
        } else {
            Self {
                dark: false,
                accent: hex(0x2446C7),
                accent_soft: hex(0xE6EBF8),
                accent_strong: hex(0x1B3AA8),
                accent_text: hex(0x2446C7),
                on_accent: hex(0xFFFFFF),
                bubble_out: hex(0x2446C7),
                on_bubble_out: hex(0xFFFFFF),
                canvas: hex(0xF3F5F8),
                panel: hex(0xFFFFFF),
                raised: hex(0xFFFFFF),
                hover: hex(0xF0F2F6),
                divider: hex(0xE4E8EE),
                border_strong: hex(0xD5DBE3),
                text: hex(0x1B2230),
                text_muted: hex(0x5A6474),
                text_faint: hex(0x8B94A3),
                bubble_in: hex(0xF0F2F6),
                bubble_in_text: hex(0x1B2230),
                field_bg: hex(0xF0F2F6),
                chip_bg: hex(0xF0F2F6),
                signal: hex(0xE59A1B),
                signal_soft: hex(0xFCF1DC),
                signal_text: hex(0x6B4508),
                on_signal: hex(0x3A2604),
                danger: hex(0xC2412D),
                online: hex(0x1E9E5A),
                warning: hex(0xB7791F),
                link: hex(0x2446C7),
            }
        }
    }
}
