// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: BUSL-1.1

//! Alternatives executor — manages update-alternatives entries.

use super::{Action, CommandRunner, DefaultRunner, Executor, ResourceResult};
use std::collections::HashMap;
use std::time::Instant;

pub struct AlternativesExecutor {
    pub runner: Box<dyn CommandRunner>,
}

impl AlternativesExecutor {
    pub fn new() -> Self {
        Self {
            runner: Box::new(DefaultRunner),
        }
    }

    fn current_value(&self, name: &str) -> Option<String> {
        // Try update-alternatives --query.
        if let Ok(o) = self.runner.run("update-alternatives", &["--query", name]) {
            if o.status.success() {
                let stdout = String::from_utf8_lossy(&o.stdout);
                for line in stdout.lines() {
                    if let Some(val) = line.strip_prefix("Value: ") {
                        return Some(val.trim().to_string());
                    }
                }
            }
        }
        // Fallback: read symlink.
        let link = format!("/etc/alternatives/{}", name);
        std::fs::read_link(&link)
            .ok()
            .map(|p| p.to_string_lossy().to_string())
    }
}

impl Executor for AlternativesExecutor {
    fn execute(&self, params: &HashMap<String, String>, dry_run: bool) -> ResourceResult {
        let start = Instant::now();
        let name = params.get("name").cloned().unwrap_or_default();
        let mut result = ResourceResult::new("alternatives", &name);

        if name.is_empty() {
            result.error = Some("alternatives executor: name param is required".into());
            result.duration_ms = start.elapsed().as_millis() as i32;
            return result;
        }

        let path = params.get("path").cloned().unwrap_or_default();
        if path.is_empty() {
            result.error = Some("alternatives executor: path param is required".into());
            result.duration_ms = start.elapsed().as_millis() as i32;
            return result;
        }

        let state = params.get("state").map(|s| s.as_str()).unwrap_or("present");
        let link = params
            .get("link")
            .cloned()
            .unwrap_or_else(|| format!("/usr/bin/{}", name));
        let priority = params.get("priority").map(|s| s.as_str()).unwrap_or("50");

        match state {
            "present" => {
                let current = self.current_value(&name);

                if current.as_deref() == Some(&path) {
                    result.action = Action::None;
                    result.duration_ms = start.elapsed().as_millis() as i32;
                    return result;
                }

                result.action = if current.is_some() {
                    Action::Updated
                } else {
                    Action::Created
                };
                result.changed = true;

                if dry_run {
                    result.duration_ms = start.elapsed().as_millis() as i32;
                    return result;
                }

                // Install alternative.
                match self.runner.run(
                    "update-alternatives",
                    &["--install", &link, &name, &path, priority],
                ) {
                    Ok(o) if !o.status.success() => {
                        let stderr = String::from_utf8_lossy(&o.stderr);
                        result.error = Some(format!("update-alternatives --install: {}", stderr));
                        result.duration_ms = start.elapsed().as_millis() as i32;
                        return result;
                    }
                    Err(e) => {
                        result.error = Some(format!("update-alternatives --install: {}", e));
                        result.duration_ms = start.elapsed().as_millis() as i32;
                        return result;
                    }
                    _ => {}
                }

                // Set as active.
                match self.runner.run("update-alternatives", &["--set", &name, &path]) {
                    Ok(o) if !o.status.success() => {
                        let stderr = String::from_utf8_lossy(&o.stderr);
                        result.error = Some(format!("update-alternatives --set: {}", stderr));
                    }
                    Err(e) => {
                        result.error = Some(format!("update-alternatives --set: {}", e));
                    }
                    _ => {}
                }
            }
            "absent" => {
                let current = self.current_value(&name);
                if current.is_none() {
                    result.action = Action::None;
                    result.duration_ms = start.elapsed().as_millis() as i32;
                    return result;
                }

                result.action = Action::Deleted;
                result.changed = true;

                if !dry_run {
                    match self.runner.run("update-alternatives", &["--remove", &name, &path]) {
                        Ok(o) if !o.status.success() => {
                            let stderr = String::from_utf8_lossy(&o.stderr);
                            result.error = Some(format!("update-alternatives --remove: {}", stderr));
                        }
                        Err(e) => {
                            result.error = Some(format!("update-alternatives --remove: {}", e));
                        }
                        _ => {}
                    }
                }
            }
            _ => {
                result.error = Some(format!("alternatives executor: unknown state {:?}", state));
            }
        }

        result.duration_ms = start.elapsed().as_millis() as i32;
        result
    }
}
