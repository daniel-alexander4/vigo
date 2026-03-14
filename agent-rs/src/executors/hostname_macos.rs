// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: BUSL-1.1

//! Hostname executor for macOS — uses `scutil`.

use super::{Action, CommandRunner, DefaultRunner, Executor, ResourceResult};
use std::collections::HashMap;
use std::time::Instant;

pub struct HostnameMacosExecutor {
    pub runner: Box<dyn CommandRunner>,
}

impl HostnameMacosExecutor {
    pub fn new() -> Self {
        Self { runner: Box::new(DefaultRunner) }
    }

    fn current_hostname(&self) -> String {
        hostname::get()
            .map(|h| h.to_string_lossy().to_string())
            .unwrap_or_default()
    }
}

impl Executor for HostnameMacosExecutor {
    fn execute(&self, params: &HashMap<String, String>, dry_run: bool) -> ResourceResult {
        let start = Instant::now();
        let desired = params.get("hostname").or_else(|| params.get("name")).cloned().unwrap_or_default();
        let mut result = ResourceResult::new("hostname", &desired);

        if desired.is_empty() {
            result.error = Some("hostname executor: hostname param is required".into());
            result.duration_ms = start.elapsed().as_millis() as i32;
            return result;
        }

        if self.current_hostname() == desired {
            result.duration_ms = start.elapsed().as_millis() as i32;
            return result;
        }

        result.action = Action::Updated;
        result.changed = true;

        if dry_run {
            result.duration_ms = start.elapsed().as_millis() as i32;
            return result;
        }

        // Set all three macOS hostname types.
        for name_type in &["HostName", "ComputerName", "LocalHostName"] {
            match self.runner.run("scutil", &["--set", name_type, &desired]) {
                Ok(o) if o.status.success() => {}
                Ok(o) => {
                    let stderr = String::from_utf8_lossy(&o.stderr);
                    result.error = Some(format!("scutil --set {}: {}", name_type, stderr));
                    break;
                }
                Err(e) => {
                    result.error = Some(format!("scutil --set {}: {}", name_type, e));
                    break;
                }
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
    fn missing_hostname_errors() {
        let exec = HostnameMacosExecutor::new();
        let result = exec.execute(&HashMap::new(), false);
        assert!(result.error.is_some());
    }
}
