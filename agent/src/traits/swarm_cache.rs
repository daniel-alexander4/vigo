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
use std::collections::{HashMap, HashSet};
use std::fs;

use crate::platform::paths::default_blobs_dir;
use crate::swarm::cache::BlobCache;

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
        // Complete blobs are 64-char lowercase hex filenames (not dangling symlinks).
        if s.len() == 64 && s.chars().all(|c| c.is_ascii_hexdigit() && !c.is_uppercase())
            && entry.path().is_file()
        {
            blobs.push(s.to_string());
        }
    }

    let complete_set: HashSet<&str> = blobs.iter().map(|s| s.as_str()).collect();

    // Read chunk source data for each blob (written by the scheduler after download).
    let cache = BlobCache::new(dir).ok();

    // Report in-progress downloads so the server can show "downloading" status.
    let mut downloading: HashMap<String, usize> = HashMap::new();
    if let Some(ref c) = cache {
        for sha in c.list_partials() {
            if complete_set.contains(sha.as_str()) {
                continue;
            }
            let chunks = c.chunk_bitmap(&sha, 0);
            if !chunks.is_empty() {
                downloading.insert(sha, chunks.len());
            }
        }
    }

    let mut chunk_sources: HashMap<String, Value> = HashMap::new();
    let mut chunk_details: HashMap<String, Value> = HashMap::new();
    let mut active_download_ms: HashMap<String, u64> = HashMap::new();
    if let Some(ref c) = cache {
        let shas = blobs.iter().map(String::as_str).chain(downloading.keys().map(String::as_str));
        for sha in shas {
            if let Some(data) = c.read_chunk_sources(sha) {
                chunk_sources.insert(sha.to_string(), json!(data.counts));
                if !data.chunks.is_empty() {
                    let details: Vec<Value> = data.chunks.iter().map(|cd| {
                        json!({"chunk": cd.chunk, "source": cd.source, "ms": cd.ms})
                    }).collect();
                    chunk_details.insert(sha.to_string(), json!(details));
                }
                if data.total_ms > 0 {
                    active_download_ms.insert(sha.to_string(), data.total_ms);
                }
            }
        }
    }

    let blob_values: Vec<Value> = blobs.into_iter().map(Value::String).collect();

    let mut result = json!({
        "blob_count": blob_values.len(),
        "blobs": blob_values,
    });
    if !chunk_sources.is_empty() {
        result["chunk_sources"] = json!(chunk_sources);
    }
    if !chunk_details.is_empty() {
        result["chunk_details"] = json!(chunk_details);
    }
    if !active_download_ms.is_empty() {
        result["active_download_ms"] = json!(active_download_ms);
    }
    if !downloading.is_empty() {
        result["downloading"] = json!(downloading);
    }
    Ok(result)
}
