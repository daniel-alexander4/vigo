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

#[cfg(test)]
#[cfg(unix)]
mod tests {
    use super::*;
    use std::os::unix::process::ExitStatusExt;
    use std::process::Output;
    use std::sync::{Arc, Mutex};

    struct MockRunner {
        responses: Mutex<Vec<std::io::Result<Output>>>,
        calls: Arc<Mutex<Vec<(String, Vec<String>)>>>,
    }

    impl MockRunner {
        fn new(responses: Vec<Output>) -> Self {
            Self {
                responses: Mutex::new(responses.into_iter().map(Ok).collect()),
                calls: Arc::new(Mutex::new(Vec::new())),
            }
        }

        fn ok_output(stdout: &str) -> Output {
            Output {
                status: std::process::ExitStatus::from_raw(0),
                stdout: stdout.as_bytes().to_vec(),
                stderr: Vec::new(),
            }
        }

        fn fail_output(stderr: &str) -> Output {
            Output {
                status: std::process::ExitStatus::from_raw(256),
                stdout: Vec::new(),
                stderr: stderr.as_bytes().to_vec(),
            }
        }
    }

    impl CommandRunner for MockRunner {
        fn run(&self, name: &str, args: &[&str]) -> std::io::Result<Output> {
            self.calls.lock().unwrap().push((
                name.to_string(),
                args.iter().map(|s| s.to_string()).collect(),
            ));
            self.responses.lock().unwrap().remove(0)
        }

        fn run_with_stdin(&self, _: &[u8], name: &str, args: &[&str]) -> std::io::Result<Output> {
            self.run(name, args)
        }
    }

    fn make_params(pairs: &[(&str, &str)]) -> HashMap<String, String> {
        pairs.iter().map(|(k, v)| (k.to_string(), v.to_string())).collect()
    }

    #[test]
    fn missing_package_errors() {
        let mock = MockRunner::new(vec![]);
        let exec = PipExecutor {
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
            MockRunner::fail_output(""),  // pip3 show requests → not installed
            MockRunner::ok_output(""),    // pip3 install requests
        ]);
        let calls = Arc::clone(&mock.calls);
        let exec = PipExecutor {
            runner: Box::new(mock),
        };
        let result = exec.execute(
            &make_params(&[("package", "requests")]),
            false,
        );
        assert_eq!(result.action, Action::Created);
        assert!(result.changed);
        assert!(result.error.is_none(), "unexpected error: {:?}", result.error);

        let calls = calls.lock().unwrap();
        assert_eq!(calls.len(), 2);
        assert_eq!(calls[0].1, vec!["show", "requests"]);
        assert_eq!(calls[1].0, "pip3");
        assert!(calls[1].1.contains(&"install".to_string()));
        assert!(calls[1].1.contains(&"requests".to_string()));
    }

    #[test]
    fn already_installed_no_version() {
        let mock = MockRunner::new(vec![
            MockRunner::ok_output("Name: requests\nVersion: 1.0.0\nSummary: HTTP lib\n"),
        ]);
        let exec = PipExecutor {
            runner: Box::new(mock),
        };
        let result = exec.execute(
            &make_params(&[("package", "requests")]),
            false,
        );
        assert_eq!(result.action, Action::None);
        assert!(!result.changed);
        assert!(result.error.is_none());
    }

    #[test]
    fn version_mismatch_upgrades() {
        let mock = MockRunner::new(vec![
            MockRunner::ok_output("Name: requests\nVersion: 1.0.0\n"), // pip3 show
            MockRunner::ok_output(""),                                  // pip3 install requests==2.0.0
        ]);
        let calls = Arc::clone(&mock.calls);
        let exec = PipExecutor {
            runner: Box::new(mock),
        };
        let result = exec.execute(
            &make_params(&[("package", "requests"), ("version", "2.0.0")]),
            false,
        );
        assert_eq!(result.action, Action::Updated);
        assert!(result.changed);
        assert!(result.error.is_none(), "unexpected error: {:?}", result.error);

        let calls = calls.lock().unwrap();
        assert_eq!(calls.len(), 2);
        assert!(calls[1].1.contains(&"install".to_string()));
        assert!(calls[1].1.contains(&"requests==2.0.0".to_string()));
    }

    #[test]
    fn absent_uninstalls() {
        let mock = MockRunner::new(vec![
            MockRunner::ok_output("Name: requests\nVersion: 2.0.0\n"), // pip3 show
            MockRunner::ok_output(""),                                  // pip3 uninstall -y
        ]);
        let calls = Arc::clone(&mock.calls);
        let exec = PipExecutor {
            runner: Box::new(mock),
        };
        let result = exec.execute(
            &make_params(&[("package", "requests"), ("state", "absent")]),
            false,
        );
        assert_eq!(result.action, Action::Deleted);
        assert!(result.changed);
        assert!(result.error.is_none(), "unexpected error: {:?}", result.error);

        let calls = calls.lock().unwrap();
        assert_eq!(calls.len(), 2);
        assert!(calls[1].1.contains(&"uninstall".to_string()));
        assert!(calls[1].1.contains(&"-y".to_string()));
    }

    #[test]
    fn absent_noop_when_missing() {
        let mock = MockRunner::new(vec![
            MockRunner::fail_output(""), // pip3 show → not installed
        ]);
        let exec = PipExecutor {
            runner: Box::new(mock),
        };
        let result = exec.execute(
            &make_params(&[("package", "requests"), ("state", "absent")]),
            false,
        );
        assert_eq!(result.action, Action::None);
        assert!(!result.changed);
        assert!(result.error.is_none());
    }

    #[test]
    fn dry_run_install_no_pip_call() {
        let mock = MockRunner::new(vec![
            MockRunner::fail_output(""), // pip3 show → not installed
        ]);
        let calls = Arc::clone(&mock.calls);
        let exec = PipExecutor {
            runner: Box::new(mock),
        };
        let result = exec.execute(
            &make_params(&[("package", "requests")]),
            true,
        );
        assert_eq!(result.action, Action::Created);
        assert!(result.changed);
        assert!(result.error.is_none());

        let calls = calls.lock().unwrap();
        assert_eq!(calls.len(), 1, "dry_run should only call pip show, got {:?}", *calls);
        assert_eq!(calls[0].1, vec!["show", "requests"]);
    }
}
