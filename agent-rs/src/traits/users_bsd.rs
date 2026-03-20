// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

//! Users collector for macOS and BSD.

use anyhow::Result;
use serde_json::{json, Value};

pub fn collect() -> Result<Value> {
    let users = logged_in_users();
    Ok(json!({
        "logged_in": users,
        "count": users.len(),
    }))
}

fn logged_in_users() -> Vec<String> {
    let output = std::process::Command::new("who").output().ok();
    let stdout = output.as_ref().map(|o| String::from_utf8_lossy(&o.stdout).to_string()).unwrap_or_default();

    let mut users = Vec::new();
    for line in stdout.lines() {
        if let Some(user) = line.split_whitespace().next() {
            if !users.contains(&user.to_string()) {
                users.push(user.to_string());
            }
        }
    }
    users
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn test_logged_in_users_no_panic() {
        let _ = logged_in_users();
    }
}
