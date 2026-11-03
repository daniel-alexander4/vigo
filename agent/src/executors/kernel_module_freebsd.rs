// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: Apache-2.0

//! Kernel module executor for FreeBSD — manages kernel modules via `kldstat`/`kldload`/`kldunload`.

use super::{Action, CommandRunner, DefaultRunner, Executor, ResourceResult};
use std::collections::HashMap;
use std::time::Instant;

pub struct KernelModuleFreebsdExecutor {
    pub runner: Box<dyn CommandRunner>,
}

impl KernelModuleFreebsdExecutor {
    pub fn new() -> Self {
        Self { runner: Box::new(DefaultRunner) }
    }

    fn is_loaded(&self, name: &str) -> bool {
        self.runner.run("kldstat", &["-q", "-m", name])
            .map(|o| o.status.success())
            .unwrap_or(false)
    }
}

impl Executor for KernelModuleFreebsdExecutor {
    fn execute(&self, params: &HashMap<String, String>, dry_run: bool) -> ResourceResult {
        let start = Instant::now();
        let module = params.get("module").cloned().unwrap_or_default();
        let state = params.get("state").map(|s| s.as_str()).unwrap_or("present");
        let mut result = ResourceResult::new("kernel_module", &module);

        if module.is_empty() {
            result.error = Some("kernel_module executor: module param is required".into());
            result.duration_ms = start.elapsed().as_millis() as i32;
            return result;
        }

        let loaded = self.is_loaded(&module);

        match state {
            "present" | "loaded" => {
                if loaded { result.duration_ms = start.elapsed().as_millis() as i32; return result; }
                result.action = Action::Created;
                result.changed = true;
                if !dry_run {
                    match self.runner.run("kldload", &[&module]) {
                        Ok(o) if !o.status.success() => {
                            result.error = Some(format!("kldload: {}", String::from_utf8_lossy(&o.stderr)));
                        }
                        Err(e) => result.error = Some(format!("kldload: {}", e)),
                        _ => {}
                    }
                }
            }
            "absent" | "unloaded" => {
                if !loaded { result.duration_ms = start.elapsed().as_millis() as i32; return result; }
                result.action = Action::Deleted;
                result.changed = true;
                if !dry_run {
                    match self.runner.run("kldunload", &[&module]) {
                        Ok(o) if !o.status.success() => {
                            result.error = Some(format!("kldunload: {}", String::from_utf8_lossy(&o.stderr)));
                        }
                        Err(e) => result.error = Some(format!("kldunload: {}", e)),
                        _ => {}
                    }
                }
            }
            other => result.error = Some(format!("unknown kernel_module state: {}", other)),
        }

        result.duration_ms = start.elapsed().as_millis() as i32;
        result
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn missing_module_errors() {
        let exec = KernelModuleFreebsdExecutor::new();
        let result = exec.execute(&HashMap::new(), false);
        assert!(result.error.is_some());
    }
}
