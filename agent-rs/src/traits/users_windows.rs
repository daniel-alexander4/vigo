// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: BUSL-1.1

#![cfg(windows)]

//! Windows users trait collector.
//!
//! Uses `net user` to enumerate local user accounts.

use anyhow::Result;
use serde_json::{json, Value};
use std::process::Command;

pub fn collect() -> Result<Value> {
    let users = list_users();

    Ok(json!({
        "users": users,
    }))
}

/// Parse the output of `net user` to extract the list of local user names.
///
/// The output format is:
/// ```text
/// User accounts for \\HOSTNAME
/// -------------------------------------------------------------------------------
/// Administrator            Dan                      Guest
/// The command completed successfully.
/// ```
fn list_users() -> Vec<String> {
    let output = match Command::new("net").arg("user").output() {
        Ok(o) if o.status.success() => String::from_utf8_lossy(&o.stdout).to_string(),
        _ => return Vec::new(),
    };

    parse_net_user_output(&output)
}

fn parse_net_user_output(output: &str) -> Vec<String> {
    let mut users = Vec::new();
    let mut in_user_section = false;

    for line in output.lines() {
        let line = line.trim();

        // The dashes line marks the start of the user list.
        if line.starts_with("----") {
            in_user_section = true;
            continue;
        }

        // "The command completed" marks the end.
        if line.starts_with("The command completed") {
            break;
        }

        if in_user_section && !line.is_empty() {
            for name in line.split_whitespace() {
                if !name.is_empty() {
                    users.push(name.to_string());
                }
            }
        }
    }

    users
}
