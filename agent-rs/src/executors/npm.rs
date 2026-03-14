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

#[cfg(test)]
#[cfg(unix)]
mod tests {
    use super::*;
    use crate::executors::test_support::mock::*;
    use std::sync::Arc;

    #[test]
    fn missing_package_errors() {
        let mock = MockRunner::new(vec![]);
        let exec = NpmExecutor {
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
    fn install_new_package() {
        let mock = MockRunner::new(vec![
            fail_output(""),  // npm list → not installed
            ok_output(""),    // npm install -g express
        ]);
        let calls = Arc::clone(&mock.calls);
        let exec = NpmExecutor {
            runner: Box::new(mock),
        };
        let result = exec.execute(
            &make_params(&[("package", "express")]),
            false,
        );
        assert_eq!(result.action, Action::Created);
        assert!(result.changed);
        assert!(result.error.is_none(), "unexpected error: {:?}", result.error);

        let calls = calls.lock().unwrap();
        assert_eq!(calls.len(), 2);
        assert!(calls[1].1.contains(&"install".to_string()));
        assert!(calls[1].1.contains(&"-g".to_string()));
        assert!(calls[1].1.contains(&"express".to_string()));
    }

    #[test]
    fn already_installed_no_version() {
        let json = r#"{"dependencies":{"express":{"version":"4.18.0"}}}"#;
        let mock = MockRunner::new(vec![
            ok_output(json),
        ]);
        let exec = NpmExecutor {
            runner: Box::new(mock),
        };
        let result = exec.execute(
            &make_params(&[("package", "express")]),
            false,
        );
        assert_eq!(result.action, Action::None);
        assert!(!result.changed);
        assert!(result.error.is_none());
    }

    #[test]
    fn version_mismatch_upgrades() {
        let json = r#"{"dependencies":{"express":{"version":"4.17.0"}}}"#;
        let mock = MockRunner::new(vec![
            ok_output(json), // npm list → installed 4.17.0
            ok_output(""),   // npm install -g express@4.18.0
        ]);
        let calls = Arc::clone(&mock.calls);
        let exec = NpmExecutor {
            runner: Box::new(mock),
        };
        let result = exec.execute(
            &make_params(&[("package", "express"), ("version", "4.18.0")]),
            false,
        );
        assert_eq!(result.action, Action::Updated);
        assert!(result.changed);
        assert!(result.error.is_none(), "unexpected error: {:?}", result.error);

        let calls = calls.lock().unwrap();
        assert_eq!(calls.len(), 2);
        assert!(calls[1].1.contains(&"install".to_string()));
        assert!(calls[1].1.contains(&"express@4.18.0".to_string()));
    }

    #[test]
    fn absent_uninstalls() {
        let json = r#"{"dependencies":{"express":{"version":"4.18.0"}}}"#;
        let mock = MockRunner::new(vec![
            ok_output(json), // npm list → installed
            ok_output(""),   // npm uninstall -g express
        ]);
        let calls = Arc::clone(&mock.calls);
        let exec = NpmExecutor {
            runner: Box::new(mock),
        };
        let result = exec.execute(
            &make_params(&[("package", "express"), ("state", "absent")]),
            false,
        );
        assert_eq!(result.action, Action::Deleted);
        assert!(result.changed);
        assert!(result.error.is_none(), "unexpected error: {:?}", result.error);

        let calls = calls.lock().unwrap();
        assert_eq!(calls.len(), 2);
        assert!(calls[1].1.contains(&"uninstall".to_string()));
        assert!(calls[1].1.contains(&"-g".to_string()));
    }

    #[test]
    fn latest_installs_at_latest() {
        let json = r#"{"dependencies":{"express":{"version":"4.18.0"}}}"#;
        let mock = MockRunner::new(vec![
            ok_output(json), // npm list
            ok_output(""),   // npm install -g express@latest
        ]);
        let calls = Arc::clone(&mock.calls);
        let exec = NpmExecutor {
            runner: Box::new(mock),
        };
        let result = exec.execute(
            &make_params(&[("package", "express"), ("state", "latest")]),
            false,
        );
        assert_eq!(result.action, Action::Updated);
        assert!(result.changed);
        assert!(result.error.is_none(), "unexpected error: {:?}", result.error);

        let calls = calls.lock().unwrap();
        assert_eq!(calls.len(), 2);
        assert!(calls[1].1.contains(&"install".to_string()));
        assert!(calls[1].1.contains(&"express@latest".to_string()));
    }

    #[test]
    fn dry_run_no_install_call() {
        let mock = MockRunner::new(vec![
            fail_output(""), // npm list → not installed
        ]);
        let calls = Arc::clone(&mock.calls);
        let exec = NpmExecutor {
            runner: Box::new(mock),
        };
        let result = exec.execute(
            &make_params(&[("package", "express")]),
            true,
        );
        assert_eq!(result.action, Action::Created);
        assert!(result.changed);
        assert!(result.error.is_none());

        let calls = calls.lock().unwrap();
        assert_eq!(calls.len(), 1, "dry_run should only call npm list, got {:?}", *calls);
    }
}
