// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: BUSL-1.1

#![cfg(windows)]

//! Windows identity trait collector.
//!
//! Uses `whoami` commands to determine the current user, SID, and whether
//! the process is running with administrator privileges.

use anyhow::Result;
use serde_json::{json, Value};
use std::process::Command;

/// Well-known SID for the Administrators group.
const ADMINISTRATORS_SID: &str = "S-1-5-32-544";

pub fn collect() -> Result<Value> {
    let username = get_username();
    let privileged = is_admin();

    Ok(json!({
        "uid": 0,
        "gid": 0,
        "username": username,
        "privileged": privileged,
    }))
}

/// Get the current username from `whoami`.
fn get_username() -> String {
    Command::new("whoami")
        .output()
        .ok()
        .filter(|o| o.status.success())
        .map(|o| {
            let name = String::from_utf8_lossy(&o.stdout).trim().to_string();
            // whoami returns DOMAIN\user; strip domain prefix if present.
            name.rsplit('\\')
                .next()
                .unwrap_or(&name)
                .to_string()
        })
        .unwrap_or_default()
}

/// Check whether the current process has administrator privileges by
/// looking for the Administrators group SID in `whoami /groups`.
fn is_admin() -> bool {
    let output = match Command::new("whoami").arg("/groups").output() {
        Ok(o) if o.status.success() => String::from_utf8_lossy(&o.stdout).to_string(),
        _ => return false,
    };

    output.contains(ADMINISTRATORS_SID)
}
