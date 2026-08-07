// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

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
            "present" if version == "latest" => {
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
                result.error =
                    Some("gem executor: state: latest was retired — use version: latest".into());
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

#[cfg(test)]
#[cfg(unix)]
mod tests {
    use super::*;
    use crate::executors::test_support::mock::*;
    use std::sync::Arc;

    #[test]
    fn missing_package_errors() {
        let mock = MockRunner::new(vec![]);
        let exec = GemExecutor {
            runner: Box::new(mock),
        };
        let result = exec.execute(&make_params(&[]), false);
        assert!(result.error.is_some());
        assert!(
            result.error.as_deref().unwrap().contains("package param is required"),
            "unexpected error: {:?}",
            result.error
        );
    }

    #[test]
    fn install_new_gem() {
        let mock = MockRunner::new(vec![
            ok_output(""),    // gem list --exact rails → empty (not installed)
            ok_output(""),    // gem install rails
        ]);
        let calls = Arc::clone(&mock.calls);
        let exec = GemExecutor {
            runner: Box::new(mock),
        };
        let result = exec.execute(
            &make_params(&[("package", "rails")]),
            false,
        );
        assert_eq!(result.action, Action::Created);
        assert!(result.changed);
        assert!(result.error.is_none(), "unexpected error: {:?}", result.error);

        let calls = calls.lock().unwrap();
        assert_eq!(calls.len(), 2);
        assert_eq!(calls[0].1, vec!["list", "--exact", "rails"]);
        assert!(calls[1].1.contains(&"install".to_string()));
        assert!(calls[1].1.contains(&"rails".to_string()));
    }

    #[test]
    fn already_installed_no_version() {
        let mock = MockRunner::new(vec![
            ok_output("rails (7.0.0)\n"),
        ]);
        let exec = GemExecutor {
            runner: Box::new(mock),
        };
        let result = exec.execute(
            &make_params(&[("package", "rails")]),
            false,
        );
        assert_eq!(result.action, Action::None);
        assert!(!result.changed);
        assert!(result.error.is_none());
    }

    #[test]
    fn version_mismatch_upgrades() {
        let mock = MockRunner::new(vec![
            ok_output("rails (6.0.0)\n"), // gem list → installed 6.0.0
            ok_output(""),                 // gem install --version 7.0.0
        ]);
        let calls = Arc::clone(&mock.calls);
        let exec = GemExecutor {
            runner: Box::new(mock),
        };
        let result = exec.execute(
            &make_params(&[("package", "rails"), ("version", "7.0.0")]),
            false,
        );
        assert_eq!(result.action, Action::Updated);
        assert!(result.changed);
        assert!(result.error.is_none(), "unexpected error: {:?}", result.error);

        let calls = calls.lock().unwrap();
        assert_eq!(calls.len(), 2);
        assert!(calls[1].1.contains(&"--version".to_string()));
        assert!(calls[1].1.contains(&"7.0.0".to_string()));
    }

    #[test]
    fn absent_uninstalls() {
        let mock = MockRunner::new(vec![
            ok_output("rails (7.0.0)\n"), // gem list → installed
            ok_output(""),                 // gem uninstall
        ]);
        let calls = Arc::clone(&mock.calls);
        let exec = GemExecutor {
            runner: Box::new(mock),
        };
        let result = exec.execute(
            &make_params(&[("package", "rails"), ("state", "absent")]),
            false,
        );
        assert_eq!(result.action, Action::Deleted);
        assert!(result.changed);
        assert!(result.error.is_none(), "unexpected error: {:?}", result.error);

        let calls = calls.lock().unwrap();
        assert_eq!(calls.len(), 2);
        assert!(calls[1].1.contains(&"uninstall".to_string()));
        assert!(calls[1].1.contains(&"--executables".to_string()));
        assert!(calls[1].1.contains(&"--all".to_string()));
    }

    #[test]
    fn version_latest_calls_update() {
        let mock = MockRunner::new(vec![
            ok_output("rails (6.0.0)\n"), // gem list
            ok_output(""),                 // gem update
        ]);
        let calls = Arc::clone(&mock.calls);
        let exec = GemExecutor {
            runner: Box::new(mock),
        };
        let result = exec.execute(
            &make_params(&[("package", "rails"), ("version", "latest")]),
            false,
        );
        assert_eq!(result.action, Action::Updated);
        assert!(result.changed);
        assert!(result.error.is_none(), "unexpected error: {:?}", result.error);

        let calls = calls.lock().unwrap();
        assert_eq!(calls.len(), 2);
        assert!(calls[1].1.contains(&"update".to_string()));
        assert!(calls[1].1.contains(&"rails".to_string()));
    }

    #[test]
    fn state_latest_retired_errors() {
        let mock = MockRunner::new(vec![
            ok_output("rails (6.0.0)\n"), // gem list
        ]);
        let exec = GemExecutor {
            runner: Box::new(mock),
        };
        let result = exec.execute(
            &make_params(&[("package", "rails"), ("state", "latest")]),
            false,
        );
        assert!(result
            .error
            .as_deref()
            .unwrap()
            .contains("state: latest was retired — use version: latest"));
    }

    #[test]
    fn dry_run_no_install_call() {
        let mock = MockRunner::new(vec![
            ok_output(""), // gem list → empty (not installed)
        ]);
        let calls = Arc::clone(&mock.calls);
        let exec = GemExecutor {
            runner: Box::new(mock),
        };
        let result = exec.execute(
            &make_params(&[("package", "rails")]),
            true,
        );
        assert_eq!(result.action, Action::Created);
        assert!(result.changed);
        assert!(result.error.is_none());

        let calls = calls.lock().unwrap();
        assert_eq!(calls.len(), 1, "dry_run should only call gem list, got {:?}", *calls);
        assert_eq!(calls[0].1, vec!["list", "--exact", "rails"]);
    }
}
