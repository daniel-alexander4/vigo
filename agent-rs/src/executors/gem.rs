// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: BUSL-1.1

//! Gem executor — manages Ruby gems via the gem command.

use super::{Action, CommandRunner, DefaultRunner, Executor, ResourceResult};
use std::collections::HashMap;
use std::time::Instant;

pub struct GemExecutor {
    pub runner: Box<dyn CommandRunner>,
}

impl GemExecutor {
    pub fn new() -> Self {
        Self {
            runner: Box::new(DefaultRunner),
        }
    }

    fn get_version(&self, gem_cmd: &str, name: &str) -> Option<String> {
        let output = self.runner.run(gem_cmd, &["list", "--exact", name]).ok()?;
        if !output.status.success() {
            return None;
        }
        let stdout = String::from_utf8_lossy(&output.stdout);
        // Format: "gemname (1.2.3)" or "gemname (1.2.3, 1.1.0)"
        for line in stdout.lines() {
            if line.starts_with(name) {
                if let Some(start) = line.find('(') {
                    if let Some(end) = line.find(')') {
                        let versions = &line[start + 1..end];
                        // Take first (latest) version.
                        let ver = versions.split(',').next().unwrap_or("").trim();
                        if !ver.is_empty() {
                            return Some(ver.to_string());
                        }
                    }
                }
            }
        }
        None
    }
}

impl Executor for GemExecutor {
    fn execute(&self, params: &HashMap<String, String>, dry_run: bool) -> ResourceResult {
        let start = Instant::now();
        let name = params
            .get("package")
            .or_else(|| params.get("name"))
            .cloned()
            .unwrap_or_default();

        let mut result = ResourceResult::new("gem", &name);

        if name.is_empty() {
            result.error = Some("gem executor: package param is required".into());
            result.duration_ms = start.elapsed().as_millis() as i32;
            return result;
        }

        let state = params.get("state").map(|s| s.as_str()).unwrap_or("present");
        let version = params.get("version").cloned().unwrap_or_default();
        let gem_cmd = params.get("gem_cmd").map(|s| s.as_str()).unwrap_or("gem");
        let no_document = params.get("no_document").map(|s| s != "false").unwrap_or(true);

        let current = self.get_version(gem_cmd, &name);

        match state {
            "present" => {
                if let Some(ref cur) = current {
                    if version.is_empty() || *cur == version {
                        result.action = Action::None;
                        result.duration_ms = start.elapsed().as_millis() as i32;
                        return result;
                    }
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

                let mut args = vec!["install", &name];
                if !version.is_empty() {
                    args.extend(["--version", &version]);
                }
                if no_document {
                    args.push("--no-document");
                }

                match self.runner.run(gem_cmd, &args) {
                    Ok(o) if !o.status.success() => {
                        let stderr = String::from_utf8_lossy(&o.stderr);
                        result.error = Some(format!("gem install: {}", stderr));
                    }
                    Err(e) => result.error = Some(format!("gem install: {}", e)),
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

                let mut args = vec!["update", &name];
                if no_document {
                    args.push("--no-document");
                }

                match self.runner.run(gem_cmd, &args) {
                    Ok(o) if !o.status.success() => {
                        let stderr = String::from_utf8_lossy(&o.stderr);
                        result.error = Some(format!("gem update: {}", stderr));
                    }
                    Err(e) => result.error = Some(format!("gem update: {}", e)),
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
                    match self.runner.run(gem_cmd, &["uninstall", &name, "--executables", "--all"]) {
                        Ok(o) if !o.status.success() => {
                            let stderr = String::from_utf8_lossy(&o.stderr);
                            result.error = Some(format!("gem uninstall: {}", stderr));
                        }
                        Err(e) => result.error = Some(format!("gem uninstall: {}", e)),
                        _ => {}
                    }
                }
            }
            _ => {
                result.error = Some(format!("gem executor: unknown state {:?}", state));
            }
        }

        result.duration_ms = start.elapsed().as_millis() as i32;
        result
    }
}
