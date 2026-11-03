// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: Apache-2.0

//! Reads Connwaer compliance verification results from JSON files on disk.
//!
//! Results directory:
//!   Linux:   /var/lib/connwaer/results/
//!   Windows: C:\ProgramData\Connwaer\results\
//!
//! Each .json file is a CapabilityResult produced by the connwaer binary.
//! This collector reads them and returns the data under the "connwaer" trait
//! namespace, e.g., .Traits.connwaer.app_allowlisting.status
//!
//! Results older than 24 hours are considered stale and excluded.

use anyhow::Result;
use serde_json::Value;
use std::fs;
use std::path::Path;

#[cfg(unix)]
const RESULTS_DIR: &str = "/var/lib/connwaer/results";

#[cfg(windows)]
const RESULTS_DIR: &str = r"C:\ProgramData\Connwaer\results";

/// Maximum age of a result file before it's considered stale.
const MAX_AGE_SECS: u64 = 86400;

pub fn collect() -> Result<Value> {
    let dir = Path::new(RESULTS_DIR);
    if !dir.exists() {
        return Ok(Value::Null);
    }

    let mut map = serde_json::Map::new();
    let now = std::time::SystemTime::now();

    let entries = match fs::read_dir(dir) {
        Ok(e) => e,
        Err(_) => return Ok(Value::Null),
    };

    for entry in entries.flatten() {
        let path = entry.path();
        if path.extension().and_then(|e| e.to_str()) != Some("json") {
            continue;
        }

        // Skip .tmp files (partial writes in progress).
        if path.to_string_lossy().contains(".tmp") {
            continue;
        }

        // Skip stale results.
        if let Ok(meta) = fs::metadata(&path) {
            if let Ok(modified) = meta.modified() {
                if let Ok(age) = now.duration_since(modified) {
                    if age.as_secs() > MAX_AGE_SECS {
                        continue;
                    }
                }
            }
        }

        let name = match path.file_stem().and_then(|s| s.to_str()) {
            Some(n) if !n.is_empty() => n.to_string(),
            _ => continue,
        };

        let content = match fs::read_to_string(&path) {
            Ok(c) => c,
            Err(_) => continue,
        };

        let parsed = match serde_json::from_str::<Value>(&content) {
            Ok(v) if v.is_object() => v,
            _ => continue,
        };

        map.insert(name, parsed);
    }

    if map.is_empty() {
        return Ok(Value::Null);
    }

    Ok(Value::Object(map))
}
