// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: Apache-2.0

//! Sysctl executor for macOS and BSD — uses `sysctl -w` for runtime, `/etc/sysctl.conf` for persistence.

use super::{Action, CommandRunner, DefaultRunner, Executor, ResourceResult};
use std::collections::HashMap;
use std::time::Instant;

pub struct SysctlBsdExecutor {
    pub runner: Box<dyn CommandRunner>,
}

impl SysctlBsdExecutor {
    pub fn new() -> Self {
        Self { runner: Box::new(DefaultRunner) }
    }

    fn current_value(&self, key: &str) -> Option<String> {
        self.runner.run("sysctl", &["-n", key])
            .ok()
            .filter(|o| o.status.success())
            .map(|o| String::from_utf8_lossy(&o.stdout).trim().to_string())
    }
}

impl Executor for SysctlBsdExecutor {
    fn execute(&self, params: &HashMap<String, String>, dry_run: bool) -> ResourceResult {
        let start = Instant::now();
        let key = params.get("key").cloned().unwrap_or_default();
        let value = params.get("value").cloned().unwrap_or_default();

        let mut result = ResourceResult::new("sysctl", &key);

        if key.is_empty() || value.is_empty() {
            result.error = Some("sysctl executor: key and value params are required".into());
            result.duration_ms = start.elapsed().as_millis() as i32;
            return result;
        }

        if let Some(current) = self.current_value(&key) {
            if current == value {
                result.duration_ms = start.elapsed().as_millis() as i32;
                return result;
            }
        }

        result.action = Action::Updated;
        result.changed = true;

        if dry_run {
            result.duration_ms = start.elapsed().as_millis() as i32;
            return result;
        }

        // Set runtime value.
        let setting = format!("{}={}", key, value);
        match self.runner.run("sysctl", &["-w", &setting]) {
            Ok(o) if o.status.success() => {}
            Ok(o) => {
                // macOS uses sysctl without -w in some cases, try plain
                match self.runner.run("sysctl", &[&setting]) {
                    Ok(o2) if o2.status.success() => {}
                    _ => {
                        let stderr = String::from_utf8_lossy(&o.stderr);
                        result.error = Some(format!("sysctl set: {}", stderr));
                        result.duration_ms = start.elapsed().as_millis() as i32;
                        return result;
                    }
                }
            }
            Err(e) => {
                result.error = Some(format!("sysctl: {}", e));
                result.duration_ms = start.elapsed().as_millis() as i32;
                return result;
            }
        }

        // Persist to /etc/sysctl.conf if requested.
        if params.get("persist").map(|s| s.as_str()) == Some("true") {
            let conf_path = "/etc/sysctl.conf";
            let content = std::fs::read_to_string(conf_path).unwrap_or_default();
            let line = format!("{}={}", key, value);
            let mut found = false;
            let mut new_lines: Vec<String> = Vec::new();
            for l in content.lines() {
                if l.trim_start().starts_with(&format!("{}=", key)) || l.trim_start().starts_with(&format!("{} =", key)) {
                    new_lines.push(line.clone());
                    found = true;
                } else {
                    new_lines.push(l.to_string());
                }
            }
            if !found {
                new_lines.push(line);
            }
            if let Err(e) = std::fs::write(conf_path, new_lines.join("\n") + "\n") {
                result.error = Some(format!("write sysctl.conf: {}", e));
            }
        }

        result.duration_ms = start.elapsed().as_millis() as i32;
        result
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn missing_params_errors() {
        let exec = SysctlBsdExecutor::new();
        let result = exec.execute(&HashMap::new(), false);
        assert!(result.error.is_some());
    }
}
