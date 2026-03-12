// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: BUSL-1.1

//! NPM executor — manages Node.js packages via npm.

use super::{Action, CommandRunner, DefaultRunner, Executor, ResourceResult};
use std::collections::HashMap;
use std::time::Instant;

pub struct NpmExecutor {
    pub runner: Box<dyn CommandRunner>,
}

impl NpmExecutor {
    pub fn new() -> Self {
        Self {
            runner: Box::new(DefaultRunner),
        }
    }

    fn get_version(&self, npm_cmd: &str, name: &str) -> Option<String> {
        let output = self
            .runner
            .run(npm_cmd, &["list", "-g", name, "--depth=0", "--json"])
            .ok()?;
        if !output.status.success() {
            return None;
        }
        let stdout = String::from_utf8_lossy(&output.stdout);
        // Quick parse: look for "version" in the JSON.
        let parsed: serde_json::Value = serde_json::from_str(&stdout).ok()?;
        parsed
            .pointer(&format!("/dependencies/{}/version", name))
            .and_then(|v| v.as_str())
            .map(|s| s.to_string())
    }
}

impl Executor for NpmExecutor {
    fn execute(&self, params: &HashMap<String, String>, dry_run: bool) -> ResourceResult {
        let start = Instant::now();
        let name = params
            .get("package")
            .or_else(|| params.get("name"))
            .cloned()
            .unwrap_or_default();

        let mut result = ResourceResult::new("npm", &name);

        if name.is_empty() {
            result.error = Some("npm executor: package param is required".into());
            result.duration_ms = start.elapsed().as_millis() as i32;
            return result;
        }

        let state = params.get("state").map(|s| s.as_str()).unwrap_or("present");
        let version = params.get("version").cloned().unwrap_or_default();
        let npm_cmd = params.get("npm_cmd").map(|s| s.as_str()).unwrap_or("npm");

        let current = self.get_version(npm_cmd, &name);

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

                let pkg_spec = if version.is_empty() {
                    name.clone()
                } else {
                    format!("{}@{}", name, version)
                };

                match self.runner.run(npm_cmd, &["install", "-g", &pkg_spec]) {
                    Ok(o) if !o.status.success() => {
                        let stderr = String::from_utf8_lossy(&o.stderr);
                        result.error = Some(format!("npm install: {}", stderr));
                    }
                    Err(e) => result.error = Some(format!("npm install: {}", e)),
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

                let pkg_spec = format!("{}@latest", name);
                match self.runner.run(npm_cmd, &["install", "-g", &pkg_spec]) {
                    Ok(o) if !o.status.success() => {
                        let stderr = String::from_utf8_lossy(&o.stderr);
                        result.error = Some(format!("npm install latest: {}", stderr));
                    }
                    Err(e) => result.error = Some(format!("npm install latest: {}", e)),
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
                    match self.runner.run(npm_cmd, &["uninstall", "-g", &name]) {
                        Ok(o) if !o.status.success() => {
                            let stderr = String::from_utf8_lossy(&o.stderr);
                            result.error = Some(format!("npm uninstall: {}", stderr));
                        }
                        Err(e) => result.error = Some(format!("npm uninstall: {}", e)),
                        _ => {}
                    }
                }
            }
            _ => {
                result.error = Some(format!("npm executor: unknown state {:?}", state));
            }
        }

        result.duration_ms = start.elapsed().as_millis() as i32;
        result
    }
}
