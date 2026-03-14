// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: Elastic-2.0
#![cfg(windows)]

//! Windows user executor — manages local users via `net user` and `net localgroup`.
//!
//! Checks user existence with `net user <name>`, creates with `net user <name> /add`,
//! deletes with `net user <name> /delete`, and manages group membership with
//! `net localgroup <group> <name> /add`.

use super::{Action, Executor, ResourceResult, run_command};
use std::collections::HashMap;
use std::time::Instant;

/// Manages Windows local user accounts using `net.exe`.
pub struct UserWindowsExecutor;

impl UserWindowsExecutor {
    pub fn new() -> Self {
        Self
    }

    /// Check if a user exists by running `net user <name>`.
    /// Returns true if the user is found.
    fn user_exists(name: &str) -> bool {
        run_command("net", &["user", name])
            .map(|o| o.status.success())
            .unwrap_or(false)
    }

    /// Parse the current groups from `net user <name>` output.
    /// Looks for the "Local Group Memberships" line.
    fn current_groups(name: &str) -> Vec<String> {
        let output = match run_command("net", &["user", name]) {
            Ok(o) if o.status.success() => o,
            _ => return Vec::new(),
        };

        let stdout = String::from_utf8_lossy(&output.stdout);
        let mut in_groups = false;
        let mut groups = Vec::new();

        for line in stdout.lines() {
            if line.starts_with("Local Group Memberships") {
                in_groups = true;
                // Parse the groups after the header prefix.
                if let Some(rest) = line.split_once('*') {
                    // Groups are prefixed with '*' and separated by spaces.
                    for part in rest.1.split('*') {
                        let g = part.trim();
                        if !g.is_empty() {
                            groups.push(g.to_string());
                        }
                    }
                }
                continue;
            }
            if in_groups {
                if line.starts_with("Global Group memberships") || line.trim().is_empty() {
                    break;
                }
                // Continuation lines for groups.
                for part in line.split('*') {
                    let g = part.trim();
                    if !g.is_empty() {
                        groups.push(g.to_string());
                    }
                }
            }
        }

        groups
    }
}

impl Executor for UserWindowsExecutor {
    fn execute(&self, params: &HashMap<String, String>, dry_run: bool) -> ResourceResult {
        let start = Instant::now();
        let name = params
            .get("username")
            .cloned()
            .unwrap_or_default();

        let mut result = ResourceResult::new("user_windows", &name);

        if name.is_empty() {
            result.error = Some(
                "user_windows executor: name param is required".to_string(),
            );
            result.duration_ms = start.elapsed().as_millis() as i32;
            return result;
        }

        let state = params
            .get("state")
            .map(|s| s.as_str())
            .unwrap_or("present");
        let exists = Self::user_exists(&name);

        match state {
            "present" => {
                if exists {
                    // User already exists. Check if groups need updating.
                    let desired_groups: Vec<&str> = params
                        .get("groups")
                        .map(|g| {
                            g.split(',')
                                .map(|s| s.trim())
                                .filter(|s| !s.is_empty())
                                .collect()
                        })
                        .unwrap_or_default();

                    let current = Self::current_groups(&name);
                    let mut groups_to_add = Vec::new();
                    for g in &desired_groups {
                        if !current.iter().any(|c| c.eq_ignore_ascii_case(g)) {
                            groups_to_add.push(*g);
                        }
                    }

                    if groups_to_add.is_empty() {
                        result.action = Action::None;
                        result.duration_ms = start.elapsed().as_millis() as i32;
                        return result;
                    }

                    result.action = Action::Updated;
                    result.changed = true;

                    if dry_run {
                        result.duration_ms = start.elapsed().as_millis() as i32;
                        return result;
                    }

                    for group in &groups_to_add {
                        match run_command("net", &["localgroup", group, &name, "/add"]) {
                            Ok(o) if !o.status.success() => {
                                let stderr = String::from_utf8_lossy(&o.stderr);
                                let stdout_str = String::from_utf8_lossy(&o.stdout);
                                result.error = Some(format!(
                                    "net localgroup {} {} /add: {} {}",
                                    group, name, stderr, stdout_str
                                ));
                                result.duration_ms = start.elapsed().as_millis() as i32;
                                return result;
                            }
                            Err(e) => {
                                result.error = Some(format!(
                                    "net localgroup {} {} /add: {}",
                                    group, name, e
                                ));
                                result.duration_ms = start.elapsed().as_millis() as i32;
                                return result;
                            }
                            _ => {}
                        }
                    }
                } else {
                    // Create user.
                    result.action = Action::Created;
                    result.changed = true;

                    if dry_run {
                        result.duration_ms = start.elapsed().as_millis() as i32;
                        return result;
                    }

                    let password = params
                        .get("password")
                        .cloned()
                        .unwrap_or_default();

                    let mut args: Vec<&str> = vec!["user", &name];
                    if !password.is_empty() {
                        args.push(&password);
                    }
                    args.push("/add");

                    // Add optional comment.
                    let comment_arg;
                    if let Some(comment) = params.get("comment") {
                        if !comment.is_empty() {
                            comment_arg = format!("/comment:{}", comment);
                            args.push(&comment_arg);
                        }
                    }

                    match run_command("net", &args) {
                        Ok(o) if !o.status.success() => {
                            let stderr = String::from_utf8_lossy(&o.stderr);
                            let stdout_str = String::from_utf8_lossy(&o.stdout);
                            result.error = Some(format!(
                                "net user {} /add: {} {}",
                                name, stderr, stdout_str
                            ));
                            result.duration_ms = start.elapsed().as_millis() as i32;
                            return result;
                        }
                        Err(e) => {
                            result.error =
                                Some(format!("net user {} /add: {}", name, e));
                            result.duration_ms = start.elapsed().as_millis() as i32;
                            return result;
                        }
                        _ => {}
                    }

                    // Add to groups.
                    let groups: Vec<&str> = params
                        .get("groups")
                        .map(|g| {
                            g.split(',')
                                .map(|s| s.trim())
                                .filter(|s| !s.is_empty())
                                .collect()
                        })
                        .unwrap_or_default();

                    for group in &groups {
                        match run_command("net", &["localgroup", group, &name, "/add"]) {
                            Ok(o) if !o.status.success() => {
                                let stderr = String::from_utf8_lossy(&o.stderr);
                                let stdout_str = String::from_utf8_lossy(&o.stdout);
                                result.error = Some(format!(
                                    "net localgroup {} {} /add: {} {}",
                                    group, name, stderr, stdout_str
                                ));
                                result.duration_ms = start.elapsed().as_millis() as i32;
                                return result;
                            }
                            Err(e) => {
                                result.error = Some(format!(
                                    "net localgroup {} {} /add: {}",
                                    group, name, e
                                ));
                                result.duration_ms = start.elapsed().as_millis() as i32;
                                return result;
                            }
                            _ => {}
                        }
                    }
                }
            }
            "absent" => {
                if !exists {
                    result.action = Action::None;
                    result.duration_ms = start.elapsed().as_millis() as i32;
                    return result;
                }

                result.action = Action::Deleted;
                result.changed = true;

                if dry_run {
                    result.duration_ms = start.elapsed().as_millis() as i32;
                    return result;
                }

                match run_command("net", &["user", &name, "/delete"]) {
                    Ok(o) if !o.status.success() => {
                        let stderr = String::from_utf8_lossy(&o.stderr);
                        let stdout_str = String::from_utf8_lossy(&o.stdout);
                        result.error = Some(format!(
                            "net user {} /delete: {} {}",
                            name, stderr, stdout_str
                        ));
                    }
                    Err(e) => {
                        result.error =
                            Some(format!("net user {} /delete: {}", name, e));
                    }
                    _ => {}
                }
            }
            _ => {
                result.error = Some(format!(
                    "user_windows executor: unknown state {:?}",
                    state
                ));
            }
        }

        result.duration_ms = start.elapsed().as_millis() as i32;
        result
    }
}
