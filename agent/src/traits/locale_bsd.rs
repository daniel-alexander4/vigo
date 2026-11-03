// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: Apache-2.0

//! Locale collector for macOS and BSD.

use anyhow::Result;
use serde_json::{json, Value};

pub fn collect() -> Result<Value> {
    let output = std::process::Command::new("locale").output().ok();
    let stdout = output.as_ref()
        .filter(|o| o.status.success())
        .map(|o| String::from_utf8_lossy(&o.stdout).to_string())
        .unwrap_or_default();

    let mut lang = String::new();
    let mut lc_all = String::new();

    for line in stdout.lines() {
        if let Some((key, val)) = line.split_once('=') {
            let val = val.trim_matches('"');
            match key {
                "LANG" => lang = val.to_string(),
                "LC_ALL" => lc_all = val.to_string(),
                _ => {}
            }
        }
    }

    Ok(json!({
        "lang": lang,
        "lc_all": lc_all,
    }))
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn test_collect_no_panic() {
        let _ = collect();
    }
}
