use chrono::{Datelike as _, Local, TimeZone as _};
use gpui_kit::assets::IconName;
use gpui_kit::component::Icon;
use gpui_kit::{prelude::FluentBuilder as _, *};

use crate::{app::Link, style::Palette};

pub fn initials(name: &str) -> String {
    let mut out: String = name
        .split(|c: char| !c.is_alphanumeric())
        .filter(|word| !word.is_empty())
        .take(2)
        .filter_map(|word| word.chars().next())
        .flat_map(char::to_uppercase)
        .collect();
    if out.is_empty() {
        out.push('?');
    }
    out
}

fn avatar_hue(name: &str) -> f32 {
    let hash = name.bytes().fold(2166136261u32, |acc, byte| {
        (acc ^ byte as u32).wrapping_mul(16777619)
    });
    (hash % 360) as f32 / 360.
}

pub fn avatar(name: &str, source: Option<ImageSource>, size: f32, p: &Palette) -> Div {
    let hue = avatar_hue(name);
    let (bg, fg) = if p.dark {
        (hsla(hue, 0.28, 0.27, 1.), hsla(hue, 0.55, 0.82, 1.))
    } else {
        (hsla(hue, 0.55, 0.90, 1.), hsla(hue, 0.45, 0.32, 1.))
    };
    let frame = div()
        .size(px(size))
        .flex_none()
        .rounded_full()
        .overflow_hidden()
        .bg(bg);
    match source {
        Some(source) => frame.child(
            img(source)
                .size_full()
                .rounded_full()
                .object_fit(ObjectFit::Cover),
        ),
        None => frame
            .flex()
            .items_center()
            .justify_center()
            .text_color(fg)
            .text_size(px((size * 0.34).max(7.)))
            .font_weight(FontWeight::BOLD)
            .child(SharedString::from(initials(name))),
    }
}

pub fn person_avatar(
    name: &str,
    source: Option<ImageSource>,
    size: f32,
    online: bool,
    ring: Hsla,
    p: &Palette,
) -> AnyElement {
    let has_own = source.is_some();
    thread_avatar(
        AvatarSpec {
            name,
            own: source,
            has_own,
            members: Vec::new(),
            online,
        },
        size,
        ring,
        p,
    )
}

pub struct AvatarSpec<'a> {
    pub name: &'a str,
    pub own: Option<ImageSource>,
    pub has_own: bool,
    pub members: Vec<Option<ImageSource>>,
    pub online: bool,
}

pub fn thread_avatar(spec: AvatarSpec, size: f32, ring: Hsla, p: &Palette) -> AnyElement {
    let dot = (size * 0.27).max(10.);
    let mut members = spec.members;
    let base = if !spec.has_own && members.len() >= 2 {
        let small = size * 0.68;
        let back = members.remove(1);
        let front = members.remove(0);
        div()
            .relative()
            .size(px(size))
            .flex_none()
            .child(
                div()
                    .absolute()
                    .top_0()
                    .right_0()
                    .child(avatar(spec.name, back, small, p)),
            )
            .child(
                div()
                    .absolute()
                    .bottom_0()
                    .left_0()
                    .rounded_full()
                    .border_2()
                    .border_color(ring)
                    .child(avatar(spec.name, front, small, p)),
            )
    } else {
        div()
            .relative()
            .size(px(size))
            .flex_none()
            .child(avatar(spec.name, spec.own, size, p))
    };
    base.when(spec.online, |el| {
        el.child(
            div()
                .absolute()
                .right(px(-1.))
                .bottom(px(-1.))
                .size(px(dot))
                .rounded_full()
                .border_2()
                .border_color(ring)
                .bg(p.online),
        )
    })
    .into_any_element()
}

pub fn icon_button(
    id: impl Into<ElementId>,
    icon: IconName,
    active: bool,
    p: &Palette,
) -> Stateful<Div> {
    let hover = p.hover;
    div()
        .id(id)
        .size(px(38.))
        .flex_none()
        .rounded(px(12.))
        .flex()
        .items_center()
        .justify_center()
        .cursor_pointer()
        .when(active, |el| el.bg(p.accent_soft))
        .when(!active, |el| el.hover(move |style| style.bg(hover)))
        .child(Icon::new(icon).size(px(18.)).text_color(if active {
            p.accent_text
        } else {
            p.text_muted
        }))
}

pub fn unread_pill(count: u32, muted: bool, p: &Palette) -> Div {
    let label: SharedString = if count > 99 {
        "99+".into()
    } else {
        count.to_string().into()
    };
    div()
        .flex_none()
        .min_w(px(20.))
        .h(px(20.))
        .px(px(6.))
        .rounded_full()
        .bg(if muted { p.text_faint } else { p.accent })
        .text_color(p.on_accent)
        .text_size(px(11.))
        .font_weight(FontWeight::BOLD)
        .flex()
        .items_center()
        .justify_center()
        .child(label)
}

pub fn typing_dots(id: impl Into<ElementId>, color: Hsla) -> impl IntoElement {
    let id: ElementId = id.into();
    h_dots(color).with_animation(
        id,
        Animation::new(std::time::Duration::from_millis(1200)).repeat(),
        move |el, delta| {
            let phase = (delta * std::f32::consts::TAU).sin() * 0.5 + 0.5;
            el.opacity(0.35 + 0.65 * phase)
        },
    )
}

fn h_dots(color: Hsla) -> Div {
    div()
        .flex()
        .gap(px(4.))
        .children((0..3).map(|_| div().size(px(6.)).rounded_full().bg(color)))
}

pub fn link_color(link: Link, p: &Palette) -> Hsla {
    match link {
        Link::Online => p.online,
        Link::Connecting | Link::Reconnecting => p.warning,
        Link::Error => p.danger,
    }
}

pub fn link_label(link: Link, detail: Option<&SharedString>) -> SharedString {
    match (link, detail) {
        (Link::Connecting, _) => "Connexion à Messenger…".into(),
        (Link::Reconnecting, _) => "Connexion perdue, nouvelle tentative…".into(),
        (Link::Error, Some(detail)) => format!("Erreur : {detail}").into(),
        (Link::Error, None) => "Erreur de connexion".into(),
        (Link::Online, _) => "En ligne".into(),
    }
}

const WEEKDAYS: [&str; 7] = ["lun.", "mar.", "mer.", "jeu.", "ven.", "sam.", "dim."];
const WEEKDAYS_LONG: [&str; 7] = [
    "Lundi", "Mardi", "Mercredi", "Jeudi", "Vendredi", "Samedi", "Dimanche",
];
const MONTHS: [&str; 12] = [
    "janv.", "févr.", "mars", "avr.", "mai", "juin", "juil.", "août", "sept.", "oct.", "nov.",
    "déc.",
];

pub fn format_short(timestamp_ms: i64) -> SharedString {
    let Some(time) = Local.timestamp_millis_opt(timestamp_ms).single() else {
        return SharedString::default();
    };
    let now = Local::now();
    let minutes = (now - time).num_minutes();
    let days = (now.date_naive() - time.date_naive()).num_days();
    let text = if (0..1).contains(&minutes) {
        "maintenant".into()
    } else if (1..60).contains(&minutes) {
        format!("{minutes} min")
    } else if days == 0 {
        time.format("%H:%M").to_string()
    } else if days == 1 {
        "Hier".into()
    } else if days < 7 {
        WEEKDAYS[time.weekday().num_days_from_monday() as usize].into()
    } else if time.year() == now.year() {
        format!("{} {}", time.day(), MONTHS[time.month0() as usize])
    } else {
        time.format("%d/%m/%y").to_string()
    };
    text.into()
}

pub fn format_separator(timestamp_ms: i64) -> SharedString {
    let Some(time) = Local.timestamp_millis_opt(timestamp_ms).single() else {
        return SharedString::default();
    };
    let now = Local::now();
    let days = (now.date_naive() - time.date_naive()).num_days();
    let clock = time.format("%H:%M");
    let text = match days {
        0 => format!("Aujourd'hui · {clock}"),
        1 => format!("Hier · {clock}"),
        2..=6 => format!(
            "{} · {clock}",
            WEEKDAYS_LONG[time.weekday().num_days_from_monday() as usize]
        ),
        _ if time.year() == now.year() => {
            format!(
                "{} {} · {clock}",
                time.day(),
                MONTHS[time.month0() as usize]
            )
        }
        _ => format!(
            "{} {} {} · {clock}",
            time.day(),
            MONTHS[time.month0() as usize],
            time.year()
        ),
    };
    text.into()
}

pub fn format_duration(ms: i64) -> String {
    let seconds = (ms / 1000).max(0);
    format!("{}:{:02}", seconds / 60, seconds % 60)
}

pub fn find_urls(text: &str) -> Vec<std::ops::Range<usize>> {
    let mut ranges = Vec::new();
    let mut search_from = 0;
    while let Some(offset) = text[search_from..].find("http") {
        let start = search_from + offset;
        let rest = &text[start..];
        if !rest.starts_with("https://") && !rest.starts_with("http://") {
            search_from = start + 4;
            continue;
        }
        let end = text[start..]
            .find(|c: char| c.is_whitespace())
            .map(|len| start + len)
            .unwrap_or(text.len());
        let trimmed_end = text[start..end]
            .trim_end_matches(['.', ',', ')', '!', '?', ';', ':'])
            .len()
            + start;
        if trimmed_end > start + 8 {
            ranges.push(start..trimmed_end);
        }
        search_from = end;
        if search_from >= text.len() {
            break;
        }
    }
    ranges
}

#[cfg(test)]
mod tests {
    use super::{find_urls, initials};

    #[test]
    fn finds_urls_and_trims_punctuation() {
        let text = "voir https://exemple.fr/a?b=1, et http://x.org.";
        let ranges = find_urls(text);
        let found: Vec<&str> = ranges.iter().map(|r| &text[r.clone()]).collect();
        assert_eq!(found, vec!["https://exemple.fr/a?b=1", "http://x.org"]);
    }

    #[test]
    fn initials_skip_emoji() {
        assert_eq!(initials("Coloc 🏠"), "C");
        assert_eq!(initials("Week-end à Annecy"), "WE");
    }
}
