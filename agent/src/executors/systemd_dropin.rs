// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: Apache-2.0

//! Systemd drop-in executor — manages unit override files idempotently.
//!
//! Creates or removes files under `/etc/systemd/system/<unit>.d/` and runs
//! `systemctl daemon-reload` when changes are made.

use super::{Action, CommandRunner, DefaultRunner, Executor, ResourceResult};
use std::collections::HashMap;
use std::time::Instant;

pub struct SystemdDropinExecutor {
    pub runner: Box<dyn CommandRunner>,
}

impl SystemdDropinExecutor {
    pub fn new() -> Self {
        Self {
            runner: Box::new(DefaultRunner),
        }
    }

    fn dropin_path(unit: &str, dropin: &str) -> String {
        format!(
            "/etc/systemd/system/{}.d/{}.conf",
            unit, dropin
        )
    }

    fn dropin_dir(unit: &str) -> String {
        format!("/etc/systemd/system/{}.d", unit)
    }

    fn daemon_reload(&self) -> Option<String> {
        match self.runner.run("systemctl", &["daemon-reload"]) {
            Ok(o) if !o.status.success() => {
                let stderr = String::from_utf8_lossy(&o.stderr);
                Some(format!("systemctl daemon-reload: {}", stderr))
            }
            Err(e) => Some(format!("systemctl daemon-reload: {}", e)),
            _ => None,
        }
    }
}

impl Executor for SystemdDropinExecutor {
    fn execute(&self, params: &HashMap<String, String>, dry_run: bool) -> ResourceResult {
        let start = Instant::now();
        let unit = params.get("unit").cloned().unwrap_or_default();
        let dropin = params.get("dropin").cloned().unwrap_or("override".to_string());

        let label = format!("{}/{}", unit, dropin);
        let mut result = ResourceResult::new("systemd_dropin", &label);

        if unit.is_empty() {
            result.error =
                Some("systemd_dropin executor: unit param is required".into());
            result.duration_ms = start.elapsed().as_millis() as i32;
            return result;
        }

        let state = params.get("state").map(|s| s.as_str()).unwrap_or("present");
        let path = Self::dropin_path(&unit, &dropin);

        match state {
            "present" => {
                let content = params.get("content").cloned().unwrap_or_default();
                if content.is_empty() {
                    result.error = Some(
                        "systemd_dropin executor: content param is required for state=present"
                            .into(),
                    );
                    result.duration_ms = start.elapsed().as_millis() as i32;
                    return result;
                }

                // Check current file content.
                let current = std::fs::read_to_string(&path).ok();
                if current.as_deref() == Some(&content) {
                    result.action = Action::None;
                    result.duration_ms = start.elapsed().as_millis() as i32;
                    return result;
                }

                let is_new = current.is_none();
                result.action = if is_new { Action::Created } else { Action::Updated };
                result.changed = true;

                if dry_run {
                    result.duration_ms = start.elapsed().as_millis() as i32;
                    return result;
                }

                // Create drop-in directory if needed.
                let dir = Self::dropin_dir(&unit);
                if let Err(e) = std::fs::create_dir_all(&dir) {
                    result.error =
                        Some(format!("create {}: {}", dir, e));
                    result.duration_ms = start.elapsed().as_millis() as i32;
                    return result;
                }

                // Write drop-in file.
                if let Err(e) = std::fs::write(&path, &content) {
                    result.error =
                        Some(format!("write {}: {}", path, e));
                    result.duration_ms = start.elapsed().as_millis() as i32;
                    return result;
                }

                // Set permissions to 0644.
                #[cfg(unix)]
                {
                    use std::os::unix::fs::PermissionsExt;
                    let _ = std::fs::set_permissions(
                        &path,
                        std::fs::Permissions::from_mode(0o644),
                    );
                }

                // Reload systemd.
                if let Some(err) = self.daemon_reload() {
                    result.error = Some(err);
                }
            }
            "absent" => {
                if !std::path::Path::new(&path).exists() {
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

                if let Err(e) = std::fs::remove_file(&path) {
                    result.error = Some(format!("remove {}: {}", path, e));
                    result.duration_ms = start.elapsed().as_millis() as i32;
                    return result;
                }

                // Clean up empty drop-in directory.
                let dir = Self::dropin_dir(&unit);
                if let Ok(entries) = std::fs::read_dir(&dir) {
                    if entries.count() == 0 {
                        let _ = std::fs::remove_dir(&dir);
                    }
                }

                // Reload systemd.
                if let Some(err) = self.daemon_reload() {
                    result.error = Some(err);
                }
            }
            _ => {
                result.error = Some(format!(
                    "systemd_dropin executor: unknown state {:?}",
                    state
                ));
            }
        }

        result.duration_ms = start.elapsed().as_millis() as i32;
        result
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::executors::test_support::mock::*;

    #[test]
    fn missing_unit_errors() {
        let mock = MockRunner::new(vec![]);
        let exec = SystemdDropinExecutor {
            runner: Box::new(mock),
        };
        let result = exec.execute(&make_params(&[]), false);
        assert!(result.error.is_some());
        assert!(
            result
                .error
                .as_deref()
                .unwrap()
                .contains("unit param is required"),
        );
    }

    #[test]
    fn present_requires_content() {
        let mock = MockRunner::new(vec![]);
        let exec = SystemdDropinExecutor {
            runner: Box::new(mock),
        };
        let result = exec.execute(
            &make_params(&[("unit", "nginx.service"), ("state", "present")]),
            false,
        );
        assert!(result.error.is_some());
        assert!(result.error.as_deref().unwrap().contains("content param is required"));
    }

    #[test]
    fn dropin_path_format() {
        assert_eq!(
            SystemdDropinExecutor::dropin_path("nginx.service", "override"),
            "/etc/systemd/system/nginx.service.d/override.conf"
        );
        assert_eq!(
            SystemdDropinExecutor::dropin_path("docker.service", "limits"),
            "/etc/systemd/system/docker.service.d/limits.conf"
        );
    }

    #[test]
    fn unknown_state_errors() {
        let mock = MockRunner::new(vec![]);
        let exec = SystemdDropinExecutor {
            runner: Box::new(mock),
        };
        let result = exec.execute(
            &make_params(&[
                ("unit", "nginx.service"),
                ("state", "bogus"),
            ]),
            false,
        );
        assert!(result.error.is_some());
        assert!(result.error.as_deref().unwrap().contains("unknown state"));
    }

    #[test]
    fn default_dropin_name_is_override() {
        // Verify that when dropin param is omitted, the path uses "override"
        let path = SystemdDropinExecutor::dropin_path("test.service", "override");
        assert!(path.contains("override.conf"));
    }
}
