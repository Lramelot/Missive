use std::borrow::Cow;

use chrono::{Datelike, NaiveDate, NaiveTime, Timelike, Weekday};

use crate::i18n;

#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub enum Day {
    Today,
    Tomorrow,
    Yesterday,
    LastWeek(Weekday),
    NextWeek(Weekday),
    Later,
}

pub fn relative_day(target: NaiveDate, today: NaiveDate) -> Day {
    match (target - today).num_days() {
        0 => Day::Today,
        1 => Day::Tomorrow,
        -1 => Day::Yesterday,
        2..=6 => Day::NextWeek(target.weekday()),
        -6..=-2 => Day::LastWeek(target.weekday()),
        _ => Day::Later,
    }
}

fn month_first() -> bool {
    !i18n::is_french()
}

fn time_of(time: &impl Timelike) -> NaiveTime {
    NaiveTime::from_hms_opt(time.hour(), time.minute(), 0).unwrap_or_default()
}

pub fn clock(time: &impl Timelike) -> String {
    let time = time_of(time);
    if month_first() {
        time.format("%-I:%M %p").to_string()
    } else {
        time.format("%H:%M").to_string()
    }
}

pub fn clock_compact(time: &impl Timelike) -> String {
    let time = time_of(time);
    if month_first() {
        time.format("%-I:%M %p").to_string()
    } else {
        time.format("%-H:%M").to_string()
    }
}

pub fn input_clock(time: &impl Timelike) -> String {
    time_of(time).format("%H:%M").to_string()
}

pub fn short_date(date: &impl Datelike) -> String {
    let (day, month, year) = (date.day(), date.month(), date.year().rem_euclid(100));
    if month_first() {
        format!("{month:02}/{day:02}/{year:02}")
    } else {
        format!("{day:02}/{month:02}/{year:02}")
    }
}

pub fn day_month_numeric(date: &impl Datelike) -> String {
    let (day, month) = (date.day(), date.month());
    if month_first() {
        format!("{month:02}/{day:02}")
    } else {
        format!("{day:02}/{month:02}")
    }
}

pub fn input_date(date: NaiveDate) -> String {
    if month_first() {
        date.format("%m/%d/%Y").to_string()
    } else {
        date.format("%d/%m/%Y").to_string()
    }
}

pub fn weekday_name(weekday: Weekday) -> Cow<'static, str> {
    match weekday {
        Weekday::Mon => t!("common.weekday.mon"),
        Weekday::Tue => t!("common.weekday.tue"),
        Weekday::Wed => t!("common.weekday.wed"),
        Weekday::Thu => t!("common.weekday.thu"),
        Weekday::Fri => t!("common.weekday.fri"),
        Weekday::Sat => t!("common.weekday.sat"),
        Weekday::Sun => t!("common.weekday.sun"),
    }
}

pub fn weekday_short(weekday: Weekday) -> Cow<'static, str> {
    match weekday {
        Weekday::Mon => t!("widgets.weekday_short.mon"),
        Weekday::Tue => t!("widgets.weekday_short.tue"),
        Weekday::Wed => t!("widgets.weekday_short.wed"),
        Weekday::Thu => t!("widgets.weekday_short.thu"),
        Weekday::Fri => t!("widgets.weekday_short.fri"),
        Weekday::Sat => t!("widgets.weekday_short.sat"),
        Weekday::Sun => t!("widgets.weekday_short.sun"),
    }
}

pub fn month_short(month: u32) -> Cow<'static, str> {
    match month {
        1 => t!("common.month_short.jan"),
        2 => t!("common.month_short.feb"),
        3 => t!("common.month_short.mar"),
        4 => t!("common.month_short.apr"),
        5 => t!("common.month_short.may"),
        6 => t!("common.month_short.jun"),
        7 => t!("common.month_short.jul"),
        8 => t!("common.month_short.aug"),
        9 => t!("common.month_short.sep"),
        10 => t!("common.month_short.oct"),
        11 => t!("common.month_short.nov"),
        _ => t!("common.month_short.dec"),
    }
}

pub fn capitalized(text: &str) -> String {
    let mut chars = text.chars();
    match chars.next() {
        Some(first) => first.to_uppercase().chain(chars).collect(),
        None => String::new(),
    }
}

pub fn parse_input_date(text: &str, today: NaiveDate) -> Option<NaiveDate> {
    parse_date_with(text, today, month_first())
}

fn parse_date_with(text: &str, today: NaiveDate, month_first: bool) -> Option<NaiveDate> {
    let lower = text.trim().to_lowercase();
    match lower.as_str() {
        "aujourd'hui" | "aujourd’hui" | "today" => return Some(today),
        "demain" | "tomorrow" => return Some(today + chrono::Duration::days(1)),
        _ => {}
    }
    let parts: Vec<&str> = lower.split(['/', '-', '.']).map(str::trim).collect();
    let number = |part: &str| -> Option<u32> {
        (!part.is_empty() && part.chars().all(|c| c.is_ascii_digit()))
            .then(|| part.parse().ok())
            .flatten()
    };
    let order = |first: u32, second: u32| {
        if month_first {
            (second, first)
        } else {
            (first, second)
        }
    };
    match parts.as_slice() {
        [first, second] => {
            let (day, month) = order(number(first)?, number(second)?);
            let this_year = NaiveDate::from_ymd_opt(today.year(), month, day)?;
            if this_year < today {
                NaiveDate::from_ymd_opt(today.year() + 1, month, day)
            } else {
                Some(this_year)
            }
        }
        [first, second, year] => {
            let (day, month) = order(number(first)?, number(second)?);
            let year = match number(year)? {
                year @ 0..=99 => 2000 + year as i32,
                year @ 1000..=9999 => year as i32,
                _ => return None,
            };
            NaiveDate::from_ymd_opt(year, month, day)
        }
        _ => None,
    }
}

#[cfg(test)]
mod tests {
    use super::{
        Day, clock, clock_compact, day_month_numeric, input_date, parse_date_with, relative_day,
        short_date,
    };
    use chrono::{NaiveDate, NaiveTime, Weekday};

    fn date(year: i32, month: u32, day: u32) -> NaiveDate {
        NaiveDate::from_ymd_opt(year, month, day).unwrap()
    }

    #[::core::prelude::v1::test]
    fn relative_days_cover_both_directions() {
        let today = date(2026, 11, 18);
        assert_eq!(relative_day(today, today), Day::Today);
        assert_eq!(relative_day(date(2026, 11, 19), today), Day::Tomorrow);
        assert_eq!(relative_day(date(2026, 11, 17), today), Day::Yesterday);
        assert_eq!(
            relative_day(date(2026, 11, 23), today),
            Day::NextWeek(Weekday::Mon)
        );
        assert_eq!(
            relative_day(date(2026, 11, 13), today),
            Day::LastWeek(Weekday::Fri)
        );
        assert_eq!(relative_day(date(2026, 11, 11), today), Day::Later);
        assert_eq!(relative_day(date(2026, 11, 25), today), Day::Later);
    }

    #[::core::prelude::v1::test]
    fn french_formats_are_day_first_and_24_hours() {
        let _guard = crate::i18n::scoped("fr");
        let afternoon = NaiveTime::from_hms_opt(14, 5, 0).unwrap();
        let morning = NaiveTime::from_hms_opt(9, 5, 0).unwrap();
        assert_eq!(clock(&afternoon), "14:05");
        assert_eq!(clock(&morning), "09:05");
        assert_eq!(clock_compact(&morning), "9:05");
        assert_eq!(short_date(&date(2025, 3, 7)), "07/03/25");
        assert_eq!(day_month_numeric(&date(2025, 3, 7)), "07/03");
        assert_eq!(input_date(date(2025, 3, 7)), "07/03/2025");
    }

    #[::core::prelude::v1::test]
    fn english_formats_are_month_first_and_12_hours() {
        let _guard = crate::i18n::scoped("en");
        let afternoon = NaiveTime::from_hms_opt(14, 5, 0).unwrap();
        let morning = NaiveTime::from_hms_opt(9, 5, 0).unwrap();
        assert_eq!(clock(&afternoon), "2:05 PM");
        assert_eq!(clock(&morning), "9:05 AM");
        assert_eq!(clock_compact(&NaiveTime::from_hms_opt(0, 30, 0).unwrap()), "12:30 AM");
        assert_eq!(short_date(&date(2025, 3, 7)), "03/07/25");
        assert_eq!(day_month_numeric(&date(2025, 3, 7)), "03/07");
        assert_eq!(input_date(date(2025, 3, 7)), "03/07/2025");
    }

    #[::core::prelude::v1::test]
    fn input_dates_follow_the_language_order() {
        let today = date(2026, 11, 18);
        assert_eq!(parse_date_with("02/01/2027", today, false), Some(date(2027, 1, 2)));
        assert_eq!(parse_date_with("02/01/2027", today, true), Some(date(2027, 2, 1)));
        assert_eq!(parse_date_with("12/25", today, true), Some(date(2026, 12, 25)));
        assert_eq!(parse_date_with("25/12", today, true), None);
        assert_eq!(parse_date_with("tomorrow", today, true), Some(date(2026, 11, 19)));
        assert_eq!(parse_date_with("01/02/26", today, false), Some(date(2026, 2, 1)));
    }
}
