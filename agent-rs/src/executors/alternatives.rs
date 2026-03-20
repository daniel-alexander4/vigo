// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

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

#[cfg(test)]
mod tests {
    use super::*;
    use crate::executors::test_support::mock::*;
    use std::process::Output;

    fn query_output(value: &str) -> Output {
        ok_output(&format!(
            "Name: editor\nLink: /usr/bin/editor\nStatus: auto\nBest: /usr/bin/vim\nValue: {}\n",
            value
        ))
    }

    #[test]
    fn missing_name_errors() {
        let mock = MockRunner::new(vec![]);
        let exec = AlternativesExecutor {
            runner: Box::new(mock),
        };
        let result = exec.execute(&make_params(&[]), false);
        assert!(result.error.is_some());
        assert!(result.error.unwrap().contains("name param is required"));
    }

    #[test]
    fn missing_path_errors() {
        let mock = MockRunner::new(vec![]);
        let exec = AlternativesExecutor {
            runner: Box::new(mock),
        };
        let result = exec.execute(&make_params(&[("name", "editor")]), false);
        assert!(result.error.is_some());
        assert!(result.error.unwrap().contains("path param is required"));
    }

    #[test]
    fn already_correct() {
        // update-alternatives --query returns current value matching desired path.
        let query = query_output("/usr/bin/vim");
        let mock = MockRunner::new(vec![query]);
        let exec = AlternativesExecutor {
            runner: Box::new(mock),
        };
        let result = exec.execute(
            &make_params(&[("name", "editor"), ("path", "/usr/bin/vim")]),
            false,
        );
        assert_eq!(result.action, Action::None);
        assert!(!result.changed);
        assert!(result.error.is_none());
    }

    #[test]
    fn updates_alternative() {
        // Current value differs from desired -> install + set.
        let query = query_output("/usr/bin/nano");
        let install = ok_output("");
        let set = ok_output("");
        let mock = MockRunner::new(vec![query, install, set]);
        let calls = mock.calls.clone();
        let exec = AlternativesExecutor {
            runner: Box::new(mock),
        };
        let result = exec.execute(
            &make_params(&[("name", "editor"), ("path", "/usr/bin/vim"), ("priority", "100")]),
            false,
        );
        assert_eq!(result.action, Action::Updated);
        assert!(result.changed);
        assert!(result.error.is_none());

        let calls = calls.lock().unwrap();
        assert_eq!(calls.len(), 3);
        // First call: --query
        assert_eq!(calls[0].1[0], "--query");
        // Second call: --install
        assert_eq!(calls[1].0, "update-alternatives");
        assert_eq!(calls[1].1[0], "--install");
        assert!(calls[1].1.contains(&"/usr/bin/vim".to_string()));
        assert!(calls[1].1.contains(&"100".to_string()));
        // Third call: --set
        assert_eq!(calls[2].1[0], "--set");
    }

    #[test]
    fn dry_run_does_not_modify() {
        // Current value differs, dry_run -> changed but no install/set commands.
        let query = query_output("/usr/bin/nano");
        let mock = MockRunner::new(vec![query]);
        let calls = mock.calls.clone();
        let exec = AlternativesExecutor {
            runner: Box::new(mock),
        };
        let result = exec.execute(
            &make_params(&[("name", "editor"), ("path", "/usr/bin/vim")]),
            true,
        );
        assert_eq!(result.action, Action::Updated);
        assert!(result.changed);
        assert!(result.error.is_none());

        let calls = calls.lock().unwrap();
        assert_eq!(calls.len(), 1, "dry_run should only call --query, not --install or --set");
    }

    #[test]
    fn absent_removes_alternative() {
        // Current value exists -> removes with --remove.
        let query = query_output("/usr/bin/vim");
        let remove = ok_output("");
        let mock = MockRunner::new(vec![query, remove]);
        let calls = mock.calls.clone();
        let exec = AlternativesExecutor {
            runner: Box::new(mock),
        };
        let result = exec.execute(
            &make_params(&[
                ("name", "editor"),
                ("path", "/usr/bin/vim"),
                ("state", "absent"),
            ]),
            false,
        );
        assert_eq!(result.action, Action::Deleted);
        assert!(result.changed);
        assert!(result.error.is_none());

        let calls = calls.lock().unwrap();
        assert_eq!(calls.len(), 2);
        assert_eq!(calls[1].1[0], "--remove");
    }

    #[test]
    fn absent_already_gone() {
        // --query fails and no symlink exists -> nothing to remove.
        // Use a name with no real /etc/alternatives/ symlink on this system.
        let query_fail = fail_output("no alternatives for vigo_fake_test_alt");
        let mock = MockRunner::new(vec![query_fail]);
        let exec = AlternativesExecutor {
            runner: Box::new(mock),
        };
        let result = exec.execute(
            &make_params(&[
                ("name", "vigo_fake_test_alt"),
                ("path", "/usr/bin/nonexistent"),
                ("state", "absent"),
            ]),
            false,
        );
        assert_eq!(result.action, Action::None);
        assert!(!result.changed);
        assert!(result.error.is_none());
    }
}
