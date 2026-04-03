// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

//! Reports cached swarm blobs as traits so the server can track distribution
//! progress. Reads the blob cache directory and lists complete blobs.
//!
//! Trait namespace: `swarm`
//!   - swarm.blob_count: number of complete blobs
//!   - swarm.blobs: list of SHA256 hashes of cached blobs

use anyhow::Result;
use serde_json::{json, Value};
use std::fs;

use crate::platform::paths::default_blobs_dir;

pub fn collect() -> Result<Value> {
    let dir = default_blobs_dir();
    if !dir.exists() {
        return Ok(Value::Null);
    }

    let entries = match fs::read_dir(&dir) {
        Ok(e) => e,
        Err(_) => return Ok(Value::Null),
    };

    let mut blobs = Vec::new();
    for entry in entries.flatten() {
        let name = entry.file_name();
        let s = name.to_string_lossy();
        // Complete blobs are 64-char lowercase hex filenames.
        if s.len() == 64 && s.chars().all(|c| c.is_ascii_hexdigit() && !c.is_uppercase()) {
            blobs.push(Value::String(s.to_string()));
        }
    }

    Ok(json!({
        "blob_count": blobs.len(),
        "blobs": blobs,
    }))
}
