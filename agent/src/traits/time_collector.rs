// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: Apache-2.0

use anyhow::Result;
use serde_json::{json, Value};

pub fn collect() -> Result<Value> {
    let (local, utc) = local_and_utc_time();

    Ok(json!({
        "hour": local.hour,
        "minute": local.min,
        "weekday": weekday_name(local.wday),
        "day": local.mday,
        "month": local.mon + 1,
        "year": local.year + 1900,
        "timezone": get_timezone_name(),
        "utc_hour": utc.hour,
        "clock": format!("{:02}:{:02}", local.hour, local.min),
        "date": format!("{:04}-{:02}-{:02}", local.year + 1900, local.mon + 1, local.mday),
    }))
}

struct TimeFields {
    hour: i32,
    min: i32,
    wday: i32,  // 0 = Sunday
    mday: i32,  // 1–31
    mon: i32,   // 0-based (0 = January)
    year: i32,  // years since 1900
}

#[cfg(unix)]
#[allow(deprecated)]
fn local_and_utc_time() -> (TimeFields, TimeFields) {
    use std::mem::MaybeUninit;
    unsafe {
        let mut t: libc::time_t = 0;
        libc::time(&mut t);

        let mut local = MaybeUninit::<libc::tm>::uninit();
        libc::localtime_r(&t, local.as_mut_ptr());

        let mut utc = MaybeUninit::<libc::tm>::uninit();
        libc::gmtime_r(&t, utc.as_mut_ptr());

        let l = local.assume_init();
        let u = utc.assume_init();
        (
            TimeFields {
                hour: l.tm_hour, min: l.tm_min, wday: l.tm_wday,
                mday: l.tm_mday, mon: l.tm_mon, year: l.tm_year,
            },
            TimeFields {
                hour: u.tm_hour, min: u.tm_min, wday: u.tm_wday,
                mday: u.tm_mday, mon: u.tm_mon, year: u.tm_year,
            },
        )
    }
}

#[cfg(windows)]
fn local_and_utc_time() -> (TimeFields, TimeFields) {
    #[repr(C)]
    struct WinSystemTime {
        year: u16,
        month: u16,
        day_of_week: u16,
        day: u16,
        hour: u16,
        minute: u16,
        second: u16,
        milliseconds: u16,
    }

    extern "system" {
        fn GetLocalTime(st: *mut WinSystemTime);
        fn GetSystemTime(st: *mut WinSystemTime);
    }

    unsafe {
        let mut local: WinSystemTime = std::mem::zeroed();
        let mut utc: WinSystemTime = std::mem::zeroed();
        GetLocalTime(&mut local);
        GetSystemTime(&mut utc);
        (
            TimeFields {
                hour: local.hour as i32,
                min: local.minute as i32,
                wday: local.day_of_week as i32,
                mday: local.day as i32,
                mon: local.month as i32 - 1,
                year: local.year as i32 - 1900,
            },
            TimeFields {
                hour: utc.hour as i32,
                min: utc.minute as i32,
                wday: utc.day_of_week as i32,
                mday: utc.day as i32,
                mon: utc.month as i32 - 1,
                year: utc.year as i32 - 1900,
            },
        )
    }
}

fn weekday_name(wday: i32) -> &'static str {
    match wday {
        0 => "sunday",
        1 => "monday",
        2 => "tuesday",
        3 => "wednesday",
        4 => "thursday",
        5 => "friday",
        6 => "saturday",
        _ => "unknown",
    }
}

fn get_timezone_name() -> String {
    if let Ok(tz) = std::env::var("TZ") {
        if !tz.is_empty() {
            return tz;
        }
    }
    "Local".to_string()
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn test_collect_has_expected_keys() {
        let val = collect().unwrap();
        let obj = val.as_object().unwrap();
        for key in &[
            "hour", "minute", "weekday", "day", "month", "year",
            "timezone", "utc_hour", "clock", "date",
        ] {
            assert!(obj.contains_key(*key), "missing key: {}", key);
        }
    }

    #[test]
    fn test_weekday_name() {
        assert_eq!(weekday_name(0), "sunday");
        assert_eq!(weekday_name(1), "monday");
        assert_eq!(weekday_name(6), "saturday");
    }

    #[test]
    fn test_hour_in_range() {
        let val = collect().unwrap();
        let hour = val["hour"].as_i64().unwrap();
        assert!((0..24).contains(&hour));
    }

    #[test]
    fn test_clock_format() {
        let val = collect().unwrap();
        let clock = val["clock"].as_str().unwrap();
        assert_eq!(clock.len(), 5);
        assert_eq!(&clock[2..3], ":");
    }

    #[test]
    fn test_date_format() {
        let val = collect().unwrap();
        let date = val["date"].as_str().unwrap();
        assert_eq!(date.len(), 10);
        assert_eq!(&date[4..5], "-");
        assert_eq!(&date[7..8], "-");
    }
}
