// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: BUSL-1.1

use anyhow::Result;
use serde_json::{json, Value};
use std::mem::MaybeUninit;

pub fn collect() -> Result<Value> {
    let (local, utc) = local_and_utc_time();

    Ok(json!({
        "hour": local.tm_hour,
        "minute": local.tm_min,
        "weekday": weekday_name(local.tm_wday),
        "day": local.tm_mday,
        "month": local.tm_mon + 1,
        "year": local.tm_year + 1900,
        "timezone": get_timezone_name(),
        "utc_hour": utc.tm_hour,
        "clock": format!("{:02}:{:02}", local.tm_hour, local.tm_min),
        "date": format!("{:04}-{:02}-{:02}", local.tm_year + 1900, local.tm_mon + 1, local.tm_mday),
    }))
}

fn local_and_utc_time() -> (libc::tm, libc::tm) {
    unsafe {
        let mut t: libc::time_t = 0;
        libc::time(&mut t);

        let mut local = MaybeUninit::<libc::tm>::uninit();
        libc::localtime_r(&t, local.as_mut_ptr());

        let mut utc = MaybeUninit::<libc::tm>::uninit();
        libc::gmtime_r(&t, utc.as_mut_ptr());

        (local.assume_init(), utc.assume_init())
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
