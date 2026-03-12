// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: BUSL-1.1

//! Tmpfile executor — manages systemd-tmpfiles configs idempotently.

use super::{Action, CommandRunner, DefaultRunner, Executor, ResourceResult};
use sha2::{Digest, Sha256};
use std::collections::HashMap;
use std::time::Instant;

pub struct TmpfileExecutor {
    pub runner: Box<dyn CommandRunner>,
}

impl TmpfileExecutor {
    pub fn new() -> Self {
        Self {
            runner: Box::new(DefaultRunner),
        }
    }
}

impl Executor for TmpfileExecutor {
    fn execute(&self, params: &HashMap<String, String>, dry_run: bool) -> ResourceResult {
        let start = Instant::now();
        let name = params.get("name").cloned().unwrap_or_default();
        let mut result = ResourceResult::new("tmpfile", &name);

        if name.is_empty() {
            result.error = Some("tmpfile executor: name param is required".into());
            result.duration_ms = start.elapsed().as_millis() as i32;
            return result;
        }

        let conf_path = format!("/etc/tmpfiles.d/{}.conf", name);
        let state = params.get("state").map(|s| s.as_str()).unwrap_or("present");

        if state == "absent" {
            if !std::path::Path::new(&conf_path).exists() {
                result.action = Action::None;
            } else {
                result.action = Action::Deleted;
                result.changed = true;
                if !dry_run {
                    if let Err(e) = std::fs::remove_file(&conf_path) {
                        result.error = Some(format!("remove {}: {}", conf_path, e));
                    }
                }
            }
            result.duration_ms = start.elapsed().as_millis() as i32;
            return result;
        }

        let path = params.get("path").cloned().unwrap_or_default();
        if path.is_empty() {
            result.error = Some("tmpfile executor: path param is required for state=present".into());
            result.duration_ms = start.elapsed().as_millis() as i32;
            return result;
        }

        let entry_type = params.get("entry_type").map(|s| s.as_str()).unwrap_or("d");
        let mode = params.get("mode").map(|s| s.as_str()).unwrap_or("0755");
        let user = params.get("user").map(|s| s.as_str()).unwrap_or("-");
        let group = params.get("group").map(|s| s.as_str()).unwrap_or("-");
        let age = params.get("age").map(|s| s.as_str()).unwrap_or("-");

        let content = format!(
            "{} {} {} {} {} {}\n",
            entry_type, path, mode, user, group, age
        );

        let desired_hash = sha256_hex(content.as_bytes());
        let existing_matches = std::fs::read(&conf_path)
            .map(|data| sha256_hex(&data) == desired_hash)
            .unwrap_or(false);

        if existing_matches {
            result.action = Action::None;
            result.duration_ms = start.elapsed().as_millis() as i32;
            return result;
        }

        result.action = if std::path::Path::new(&conf_path).exists() {
            Action::Updated
        } else {
            Action::Created
        };
        result.changed = true;

        if !dry_run {
            if let Err(e) = std::fs::write(&conf_path, &content) {
                result.error = Some(format!("write {}: {}", conf_path, e));
                result.duration_ms = start.elapsed().as_millis() as i32;
                return result;
            }

            // Apply immediately.
            match self.runner.run("systemd-tmpfiles", &["--create", &conf_path]) {
                Ok(o) if !o.status.success() => {
                    let stderr = String::from_utf8_lossy(&o.stderr);
                    result.error = Some(format!("systemd-tmpfiles --create: {}", stderr));
                }
                Err(e) => {
                    result.error = Some(format!("systemd-tmpfiles --create: {}", e));
                }
                _ => {}
            }
        }

        result.duration_ms = start.elapsed().as_millis() as i32;
        result
    }
}

fn sha256_hex(data: &[u8]) -> String {
    let hash = Sha256::digest(data);
    hash.iter().map(|b| format!("{:02x}", b)).collect()
}
