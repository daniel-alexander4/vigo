// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: Elastic-2.0

use anyhow::Result;
use serde_json::{json, Value};
use std::mem::MaybeUninit;

pub fn collect() -> Result<Value> {
    let (abbreviation, offset_secs) = get_timezone_info();
    let name = get_timezone_name(&abbreviation);
    let utc_offset = format_utc_offset(offset_secs);

    Ok(json!({
        "abbreviation": abbreviation,
        "utc_offset_seconds": offset_secs,
        "utc_offset": utc_offset,
        "name": name,
    }))
}

fn get_timezone_info() -> (String, i32) {
    unsafe {
        let mut t: libc::time_t = 0;
        libc::time(&mut t);
        let mut tm = MaybeUninit::<libc::tm>::uninit();
        libc::localtime_r(&t, tm.as_mut_ptr());
        let tm = tm.assume_init();

        let abbr = if tm.tm_zone.is_null() {
            String::new()
        } else {
            std::ffi::CStr::from_ptr(tm.tm_zone)
                .to_string_lossy()
                .into_owned()
        };

        (abbr, tm.tm_gmtoff as i32)
    }
}

fn get_timezone_name(abbreviation: &str) -> String {
    // Try /etc/timezone first.
    if let Ok(name) = std::fs::read_to_string("/etc/timezone") {
        let name = name.trim();
        if !name.is_empty() {
            return name.to_string();
        }
    }

    // Try /etc/localtime symlink target.
    if let Ok(target) = std::fs::read_link("/etc/localtime") {
        let target = target.to_string_lossy().to_string();
        if let Some(idx) = target.find("zoneinfo/") {
            return target[idx + 9..].to_string();
        }
    }

    abbreviation.to_string()
}

fn format_utc_offset(offset_secs: i32) -> String {
    let sign = if offset_secs >= 0 { '+' } else { '-' };
    let abs = offset_secs.unsigned_abs();
    let hours = abs / 3600;
    let minutes = (abs % 3600) / 60;
    format!("{}{:02}:{:02}", sign, hours, minutes)
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn test_format_utc_offset() {
        assert_eq!(format_utc_offset(0), "+00:00");
        assert_eq!(format_utc_offset(3600), "+01:00");
        assert_eq!(format_utc_offset(-18000), "-05:00");
        assert_eq!(format_utc_offset(19800), "+05:30");
    }

    #[test]
    fn test_collect_returns_expected_keys() {
        let val = collect().unwrap();
        let obj = val.as_object().unwrap();
        assert!(obj.contains_key("abbreviation"));
        assert!(obj.contains_key("utc_offset_seconds"));
        assert!(obj.contains_key("utc_offset"));
        assert!(obj.contains_key("name"));
    }
}
