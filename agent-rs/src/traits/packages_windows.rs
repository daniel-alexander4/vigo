// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

#![cfg(windows)]

//! Windows packages trait collector.
//!
//! Tries Chocolatey (`choco list -l`) first, then falls back to
//! `winget list`. Returns installed packages as name-version pairs.

use anyhow::Result;
use serde_json::{json, Value};
use std::process::Command;

pub fn collect() -> Result<Value> {
    // Try Chocolatey first, fall back to winget.
    if let Some(result) = choco_packages() {
        return Ok(result);
    }

    if let Some(result) = winget_packages() {
        return Ok(result);
    }

    Ok(json!({
        "packages": {},
        "count": 0,
        "manager": "none",
    }))
}

/// Collect packages from Chocolatey (`choco list -l`).
fn choco_packages() -> Option<Value> {
    let output = Command::new("choco")
        .args(["list", "-l"])
        .output()
        .ok()?;

    if !output.status.success() {
        return None;
    }

    let text = String::from_utf8_lossy(&output.stdout);
    let packages = parse_choco_output(&text);
    let count = packages.len();

    Some(json!({
        "packages": Value::Object(packages.into_iter().collect()),
        "count": count,
        "manager": "chocolatey",
    }))
}

/// Parse choco list output. Each line is "name version" except the
/// summary line at the end (e.g., "42 packages installed.").
fn parse_choco_output(output: &str) -> Vec<(String, Value)> {
    let mut packages = Vec::new();

    for line in output.lines() {
        let line = line.trim();
        if line.is_empty() || line.contains("packages installed") || line.contains("Chocolatey") {
            continue;
        }

        let parts: Vec<&str> = line.split_whitespace().collect();
        if parts.len() >= 2 {
            packages.push((parts[0].to_string(), json!(parts[1])));
        }
    }

    packages
}

/// Collect packages from winget (`winget list`).
fn winget_packages() -> Option<Value> {
    let output = Command::new("winget")
        .args(["list", "--disable-interactivity"])
        .output()
        .ok()?;

    if !output.status.success() {
        return None;
    }

    let text = String::from_utf8_lossy(&output.stdout);
    let packages = parse_winget_output(&text);
    let count = packages.len();

    Some(json!({
        "packages": Value::Object(packages.into_iter().collect()),
        "count": count,
        "manager": "winget",
    }))
}

/// Parse winget list output. The output is a table with column headers.
/// We look for the separator line (dashes) to find column positions, then
/// parse each subsequent line.
fn parse_winget_output(output: &str) -> Vec<(String, Value)> {
    let mut packages = Vec::new();
    let lines: Vec<&str> = output.lines().collect();

    // Find the dashes separator line to determine column boundaries.
    let sep_idx = lines.iter().position(|line| {
        let trimmed = line.trim();
        trimmed.len() > 10 && trimmed.chars().all(|c| c == '-' || c == ' ')
    });

    let sep_idx = match sep_idx {
        Some(i) => i,
        None => return packages,
    };

    // Find the "Version" column start from the header line above the separator.
    if sep_idx == 0 {
        return packages;
    }

    let header = lines[sep_idx - 1];
    let version_col = header.find("Version").unwrap_or(0);

    if version_col == 0 {
        return packages;
    }

    for line in &lines[sep_idx + 1..] {
        let line = line.trim_end();
        if line.is_empty() || line.len() < version_col {
            continue;
        }

        let name = line[..version_col].trim();
        let rest = &line[version_col..];
        let version = rest.split_whitespace().next().unwrap_or("");

        if !name.is_empty() && !version.is_empty() {
            packages.push((name.to_string(), json!(version)));
        }
    }

    packages
}
