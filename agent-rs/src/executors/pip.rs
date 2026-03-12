// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: BUSL-1.1

//! Pip executor — manages Python packages via pip.

use super::{Action, CommandRunner, DefaultRunner, Executor, ResourceResult};
use std::collections::HashMap;
use std::time::Instant;

pub struct PipExecutor {
    pub runner: Box<dyn CommandRunner>,
}

impl PipExecutor {
    pub fn new() -> Self {
        Self {
            runner: Box::new(DefaultRunner),
        }
    }

    fn pip_cmd(params: &HashMap<String, String>) -> String {
        let mut cmd = params.get("pip_cmd").cloned().unwrap_or_else(|| "pip3".into());
        if let Some(venv) = params.get("virtualenv") {
            if !venv.is_empty() {
                cmd = format!("{}/bin/{}", venv, cmd);
            }
        }
        cmd
    }

    fn get_version(&self, pip: &str, package: &str) -> Option<String> {
        let output = self.runner.run(pip, &["show", package]).ok()?;
        if !output.status.success() {
            return None;
        }
        let stdout = String::from_utf8_lossy(&output.stdout);
        for line in stdout.lines() {
            if let Some(ver) = line.strip_prefix("Version: ") {
                return Some(ver.trim().to_string());
            }
        }
        None
    }
}

impl Executor for PipExecutor {
    fn execute(&self, params: &HashMap<String, String>, dry_run: bool) -> ResourceResult {
        let start = Instant::now();
        let name = params
            .get("package")
            .or_else(|| params.get("name"))
            .cloned()
            .unwrap_or_default();

        let mut result = ResourceResult::new("pip", &name);

        if name.is_empty() {
            result.error = Some("pip executor: package param is required".into());
            result.duration_ms = start.elapsed().as_millis() as i32;
            return result;
        }

        let state = params.get("state").map(|s| s.as_str()).unwrap_or("present");
        let version = params.get("version").cloned().unwrap_or_default();
        let extra_args = params.get("extra_args").cloned().unwrap_or_default();
        let pip = Self::pip_cmd(params);
        let current = self.get_version(&pip, &name);

        match state {
            "present" => {
                if let Some(ref cur) = current {
                    if version.is_empty() || *cur == version {
                        result.action = Action::None;
                        result.duration_ms = start.elapsed().as_millis() as i32;
                        return result;
                    }
                    // Version mismatch — upgrade.
                    result.action = Action::Updated;
                    result.changed = true;
                } else {
                    result.action = Action::Created;
                    result.changed = true;
                }

                if dry_run {
                    result.duration_ms = start.elapsed().as_millis() as i32;
                    return result;
                }

                let pkg_spec = if version.is_empty() {
                    name.clone()
                } else {
                    format!("{}=={}", name, version)
                };

                let mut args = vec!["install", &pkg_spec];
                let extra_parts: Vec<&str> = if !extra_args.is_empty() {
                    extra_args.split_whitespace().collect()
                } else {
                    Vec::new()
                };
                args.extend(&extra_parts);

                match self.runner.run(&pip, &args) {
                    Ok(o) if !o.status.success() => {
                        let stderr = String::from_utf8_lossy(&o.stderr);
                        result.error = Some(format!("pip install: {}", stderr));
                    }
                    Err(e) => result.error = Some(format!("pip install: {}", e)),
                    _ => {}
                }
            }
            "latest" => {
                result.action = Action::Updated;
                result.changed = true;

                if dry_run {
                    result.duration_ms = start.elapsed().as_millis() as i32;
                    return result;
                }

                match self.runner.run(&pip, &["install", "--upgrade", &name]) {
                    Ok(o) if !o.status.success() => {
                        let stderr = String::from_utf8_lossy(&o.stderr);
                        result.error = Some(format!("pip install --upgrade: {}", stderr));
                    }
                    Err(e) => result.error = Some(format!("pip install --upgrade: {}", e)),
                    _ => {}
                }
            }
            "absent" => {
                if current.is_none() {
                    result.action = Action::None;
                    result.duration_ms = start.elapsed().as_millis() as i32;
                    return result;
                }

                result.action = Action::Deleted;
                result.changed = true;

                if !dry_run {
                    match self.runner.run(&pip, &["uninstall", "-y", &name]) {
                        Ok(o) if !o.status.success() => {
                            let stderr = String::from_utf8_lossy(&o.stderr);
                            result.error = Some(format!("pip uninstall: {}", stderr));
                        }
                        Err(e) => result.error = Some(format!("pip uninstall: {}", e)),
                        _ => {}
                    }
                }
            }
            _ => {
                result.error = Some(format!("pip executor: unknown state {:?}", state));
            }
        }

        result.duration_ms = start.elapsed().as_millis() as i32;
        result
    }
}
