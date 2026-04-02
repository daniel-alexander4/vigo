// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

#![cfg(windows)]

//! Windows timezone trait collector.
//!
//! Uses `tzutil /g` to get the current Windows timezone name and maps it
//! to a common abbreviation.

use anyhow::Result;
use serde_json::{json, Value};
use std::process::Command;

pub fn collect() -> Result<Value> {
    let name = get_timezone_name();
    let abbreviation = abbreviate_timezone(&name);

    Ok(json!({
        "name": name,
        "abbreviation": abbreviation,
    }))
}

/// Run `tzutil /g` to get the current timezone name.
fn get_timezone_name() -> String {
    Command::new("tzutil")
        .arg("/g")
        .output()
        .ok()
        .filter(|o| o.status.success())
        .map(|o| String::from_utf8_lossy(&o.stdout).trim().to_string())
        .unwrap_or_default()
}

/// Map well-known Windows timezone names to common abbreviations.
fn abbreviate_timezone(name: &str) -> String {
    match name {
        "Eastern Standard Time" => "EST".to_string(),
        "Central Standard Time" => "CST".to_string(),
        "Mountain Standard Time" => "MST".to_string(),
        "Pacific Standard Time" => "PST".to_string(),
        "Alaska Standard Time" => "AKST".to_string(),
        "Hawaiian Standard Time" => "HST".to_string(),
        "UTC" | "Coordinated Universal Time" => "UTC".to_string(),
        "GMT Standard Time" => "GMT".to_string(),
        "Central European Standard Time" => "CET".to_string(),
        "W. Europe Standard Time" => "WET".to_string(),
        "E. Europe Standard Time" => "EET".to_string(),
        "India Standard Time" => "IST".to_string(),
        "China Standard Time" => "CST".to_string(),
        "Tokyo Standard Time" => "JST".to_string(),
        "AUS Eastern Standard Time" => "AEST".to_string(),
        "New Zealand Standard Time" => "NZST".to_string(),
        _ => {
            // Fall back to extracting uppercase initials.
            name.split_whitespace()
                .filter_map(|w| w.chars().next())
                .filter(|c| c.is_uppercase())
                .collect()
        }
    }
}
