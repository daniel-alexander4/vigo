// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: BUSL-1.1

//! Sysctl executor — manages kernel parameters idempotently.

use super::{Action, CommandRunner, DefaultRunner, Executor, ResourceResult};
use std::collections::HashMap;
use std::time::Instant;

pub struct SysctlExecutor {
    pub runner: Box<dyn CommandRunner>,
}

impl SysctlExecutor {
    pub fn new() -> Self {
        Self {
            runner: Box::new(DefaultRunner),
        }
    }

    fn current_value(&self, key: &str) -> Option<String> {
        let output = self.runner.run("sysctl", &["-n", key]).ok()?;
        if output.status.success() {
            Some(String::from_utf8_lossy(&output.stdout).trim().to_string())
        } else {
            None
        }
    }

    fn conf_path(key: &str) -> String {
        let safe = key.replace('.', "-");
        format!("/etc/sysctl.d/99-hobnob-{}.conf", safe)
    }
}

impl Executor for SysctlExecutor {
    fn execute(&self, params: &HashMap<String, String>, dry_run: bool) -> ResourceResult {
        let start = Instant::now();
        let key = params
            .get("key")
            .or_else(|| params.get("name"))
            .cloned()
            .unwrap_or_default();

        let mut result = ResourceResult::new("sysctl", &key);

        if key.is_empty() {
            result.error = Some("sysctl executor: key param is required".into());
            result.duration_ms = start.elapsed().as_millis() as i32;
            return result;
        }

        let state = params.get("state").map(|s| s.as_str()).unwrap_or("present");
        let persist = params.get("persist").map(|s| s.as_str()).unwrap_or("true") == "true";

        if state == "absent" {
            let conf = Self::conf_path(&key);
            if std::path::Path::new(&conf).exists() {
                result.action = Action::Deleted;
                result.changed = true;
                if !dry_run {
                    if let Err(e) = std::fs::remove_file(&conf) {
                        result.error = Some(format!("remove {}: {}", conf, e));
                        result.duration_ms = start.elapsed().as_millis() as i32;
                        return result;
                    }
                    let _ = self.runner.run("sysctl", &["--system"]);
                }
            } else {
                result.action = Action::None;
            }
            result.duration_ms = start.elapsed().as_millis() as i32;
            return result;
        }

        let value = params.get("value").cloned().unwrap_or_default();
        if value.is_empty() {
            result.error = Some("sysctl executor: value param is required".into());
            result.duration_ms = start.elapsed().as_millis() as i32;
            return result;
        }

        let current = self.current_value(&key).unwrap_or_default();
        let conf = Self::conf_path(&key);
        let conf_exists = std::path::Path::new(&conf).exists();
        let conf_content = format!("{} = {}\n", key, value);
        let conf_matches = if conf_exists {
            std::fs::read_to_string(&conf)
                .map(|c| c == conf_content)
                .unwrap_or(false)
        } else {
            false
        };

        let runtime_match = current == value;
        let persist_match = !persist || conf_matches;

        if runtime_match && persist_match {
            result.action = Action::None;
            result.duration_ms = start.elapsed().as_millis() as i32;
            return result;
        }

        result.action = if runtime_match || conf_exists {
            Action::Updated
        } else {
            Action::Created
        };
        result.changed = true;

        if dry_run {
            result.duration_ms = start.elapsed().as_millis() as i32;
            return result;
        }

        // Set runtime value.
        if !runtime_match {
            let setting = format!("{}={}", key, value);
            let output = self.runner.run("sysctl", &["-w", &setting]);
            match output {
                Ok(o) if !o.status.success() => {
                    let stderr = String::from_utf8_lossy(&o.stderr);
                    result.error = Some(format!("sysctl -w {}: {}", setting, stderr));
                    result.duration_ms = start.elapsed().as_millis() as i32;
                    return result;
                }
                Err(e) => {
                    result.error = Some(format!("sysctl -w {}: {}", setting, e));
                    result.duration_ms = start.elapsed().as_millis() as i32;
                    return result;
                }
                _ => {}
            }
        }

        // Persist.
        if persist && !conf_matches {
            if let Err(e) = std::fs::write(&conf, conf_content) {
                result.error = Some(format!("write {}: {}", conf, e));
            }
        }

        result.duration_ms = start.elapsed().as_millis() as i32;
        result
    }
}
