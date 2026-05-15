// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

//! Container executor — idempotent lifecycle for OCI containers via a local
//! runtime. Docker today; Podman / containerd land as alternate runtimes when
//! those backends are added (they'll thread through a `runtime:` field; the
//! abstraction stays absent until there's a second implementation).

use super::{Action, CommandRunner, DefaultRunner, Executor, ResourceResult};
use std::collections::HashMap;
use std::time::{Duration, Instant};

const DOCKER: &str = "docker";

/// Hard timeout on every Docker daemon call. Long enough for cold-cache image
/// inspect; short enough that a hung daemon doesn't stall convergence.
const DAEMON_TIMEOUT: Duration = Duration::from_secs(10);

pub struct ContainerExecutor {
    pub runner: Box<dyn CommandRunner>,
}

impl ContainerExecutor {
    pub fn new() -> Self {
        Self {
            runner: Box::new(DefaultRunner),
        }
    }

    fn run(&self, args: &[&str]) -> std::io::Result<std::process::Output> {
        self.runner
            .run_niced_timeout(DOCKER, args, 0, DAEMON_TIMEOUT)
    }

    fn container_status(&self, name: &str) -> Option<String> {
        let output = self
            .run(&["inspect", "--format", "{{.State.Status}}", name])
            .ok()?;
        if output.status.success() {
            Some(String::from_utf8_lossy(&output.stdout).trim().to_string())
        } else {
            None
        }
    }

    fn container_image(&self, name: &str) -> Option<String> {
        let output = self
            .run(&["inspect", "--format", "{{.Config.Image}}", name])
            .ok()?;
        if output.status.success() {
            Some(String::from_utf8_lossy(&output.stdout).trim().to_string())
        } else {
            None
        }
    }

    fn build_create_args<'a>(
        &self,
        name: &'a str,
        image: &'a str,
        params: &'a HashMap<String, String>,
    ) -> Vec<String> {
        let mut args = vec!["create".to_string(), "--name".into(), name.into()];

        if let Some(ports) = params.get("ports") {
            for port in ports.split(',').map(|s| s.trim()).filter(|s| !s.is_empty()) {
                args.push("-p".into());
                args.push(port.into());
            }
        }

        if let Some(env) = params.get("env") {
            for e in env.split(',').map(|s| s.trim()).filter(|s| !s.is_empty()) {
                args.push("-e".into());
                args.push(e.into());
            }
        }

        if let Some(volumes) = params.get("volumes") {
            for v in volumes.split(',').map(|s| s.trim()).filter(|s| !s.is_empty()) {
                args.push("-v".into());
                args.push(v.into());
            }
        }

        if let Some(network) = params.get("network") {
            if !network.is_empty() {
                args.push("--network".into());
                args.push(network.clone());
            }
        }

        if let Some(restart) = params.get("restart") {
            if !restart.is_empty() {
                args.push("--restart".into());
                args.push(restart.clone());
            }
        }

        args.push(image.into());

        if let Some(cmd) = params.get("command") {
            if !cmd.is_empty() {
                for part in cmd.split_whitespace() {
                    args.push(part.into());
                }
            }
        }

        args
    }
}

impl Executor for ContainerExecutor {
    fn execute(&self, params: &HashMap<String, String>, dry_run: bool) -> ResourceResult {
        let start = Instant::now();
        let name = params.get("container").cloned().unwrap_or_default();

        let mut result = ResourceResult::new("container", &name);

        if name.is_empty() {
            result.error = Some("container executor: container param is required".into());
            result.duration_ms = start.elapsed().as_millis() as i32;
            return result;
        }

        let state = params.get("state").map(|s| s.as_str()).unwrap_or("running");
        let image = params.get("image").cloned().unwrap_or_default();
        let pull = params.get("pull").map(|s| s.as_str()).unwrap_or("missing");

        let status = self.container_status(&name);
        let exists = status.is_some();
        let running = status.as_deref() == Some("running");

        match state {
            "running" => {
                if image.is_empty() {
                    result.error =
                        Some("container executor: image required for state=running".into());
                    result.duration_ms = start.elapsed().as_millis() as i32;
                    return result;
                }

                if exists && running {
                    let current_image = self.container_image(&name).unwrap_or_default();
                    if current_image == image {
                        result.action = Action::None;
                        result.duration_ms = start.elapsed().as_millis() as i32;
                        return result;
                    }

                    result.action = Action::Updated;
                    result.changed = true;

                    if dry_run {
                        result.duration_ms = start.elapsed().as_millis() as i32;
                        return result;
                    }

                    let _ = self.run(&["stop", &name]);
                    let _ = self.run(&["rm", &name]);
                } else if exists {
                    result.action = Action::Updated;
                    result.changed = true;

                    if dry_run {
                        result.duration_ms = start.elapsed().as_millis() as i32;
                        return result;
                    }

                    match self.run(&["start", &name]) {
                        Ok(o) if !o.status.success() => {
                            let stderr = String::from_utf8_lossy(&o.stderr);
                            result.error = Some(format!("docker start: {}", stderr));
                        }
                        Err(e) => result.error = Some(format!("docker start: {}", e)),
                        _ => {}
                    }
                    result.duration_ms = start.elapsed().as_millis() as i32;
                    return result;
                } else {
                    result.action = Action::Created;
                    result.changed = true;
                }

                if dry_run {
                    result.duration_ms = start.elapsed().as_millis() as i32;
                    return result;
                }

                if pull == "always" || (pull == "missing" && !exists) {
                    let _ = self.run(&["pull", &image]);
                }

                let create_args = self.build_create_args(&name, &image, params);
                let arg_refs: Vec<&str> = create_args.iter().map(|s| s.as_str()).collect();
                match self.run(&arg_refs) {
                    Ok(o) if !o.status.success() => {
                        let stderr = String::from_utf8_lossy(&o.stderr);
                        result.error = Some(format!("docker create: {}", stderr));
                        result.duration_ms = start.elapsed().as_millis() as i32;
                        return result;
                    }
                    Err(e) => {
                        result.error = Some(format!("docker create: {}", e));
                        result.duration_ms = start.elapsed().as_millis() as i32;
                        return result;
                    }
                    _ => {}
                }

                match self.run(&["start", &name]) {
                    Ok(o) if !o.status.success() => {
                        let stderr = String::from_utf8_lossy(&o.stderr);
                        result.error = Some(format!("docker start: {}", stderr));
                    }
                    Err(e) => result.error = Some(format!("docker start: {}", e)),
                    _ => {}
                }
            }
            "stopped" => {
                if !exists || !running {
                    result.action = Action::None;
                } else {
                    result.action = Action::Updated;
                    result.changed = true;
                    if !dry_run {
                        match self.run(&["stop", &name]) {
                            Ok(o) if !o.status.success() => {
                                let stderr = String::from_utf8_lossy(&o.stderr);
                                result.error = Some(format!("docker stop: {}", stderr));
                            }
                            Err(e) => result.error = Some(format!("docker stop: {}", e)),
                            _ => {}
                        }
                    }
                }
            }
            "absent" => {
                if !exists {
                    result.action = Action::None;
                } else {
                    result.action = Action::Deleted;
                    result.changed = true;
                    if !dry_run {
                        if running {
                            let _ = self.run(&["stop", &name]);
                        }
                        match self.run(&["rm", &name]) {
                            Ok(o) if !o.status.success() => {
                                let stderr = String::from_utf8_lossy(&o.stderr);
                                result.error = Some(format!("docker rm: {}", stderr));
                            }
                            Err(e) => result.error = Some(format!("docker rm: {}", e)),
                            _ => {}
                        }
                    }
                }
            }
            _ => {
                result.error =
                    Some(format!("container executor: unknown state {:?}", state));
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
    fn missing_name_errors() {
        let mock = MockRunner::new(vec![]);
        let exec = ContainerExecutor {
            runner: Box::new(mock),
        };
        let result = exec.execute(&make_params(&[]), false);
        assert!(result.error.is_some());
        assert!(
            result
                .error
                .as_deref()
                .unwrap()
                .contains("container param is required"),
            "unexpected error: {:?}",
            result.error
        );
    }

    #[test]
    fn running_container_already_running_same_image() {
        let mock = MockRunner::new(vec![
            ok_output("running\n"),
            ok_output("nginx:latest\n"),
        ]);
        let calls = Arc::clone(&mock.calls);
        let exec = ContainerExecutor {
            runner: Box::new(mock),
        };
        let result = exec.execute(
            &make_params(&[
                ("container", "web"),
                ("image", "nginx:latest"),
                ("state", "running"),
            ]),
            false,
        );
        assert_eq!(result.action, Action::None);
        assert!(!result.changed);
        assert!(result.error.is_none());
        let calls = calls.lock().unwrap();
        assert_eq!(calls.len(), 2);
        assert_eq!(calls[0].0, "docker");
        assert_eq!(calls[1].0, "docker");
    }

    #[test]
    fn create_and_start_new_container() {
        let mock = MockRunner::new(vec![
            fail_output("No such container"),
            ok_output(""),
            ok_output(""),
            ok_output(""),
        ]);
        let calls = Arc::clone(&mock.calls);
        let exec = ContainerExecutor {
            runner: Box::new(mock),
        };
        let result = exec.execute(
            &make_params(&[
                ("container", "web"),
                ("image", "nginx:latest"),
                ("state", "running"),
            ]),
            false,
        );
        assert_eq!(result.action, Action::Created);
        assert!(result.changed);
        assert!(
            result.error.is_none(),
            "unexpected error: {:?}",
            result.error
        );
        let calls = calls.lock().unwrap();
        assert_eq!(calls.len(), 4);
        assert_eq!(calls[0].0, "docker");
        assert_eq!(calls[1].1[0], "pull");
        assert_eq!(calls[2].1[0], "create");
        assert_eq!(calls[3].1[0], "start");
    }

    #[test]
    fn start_stopped_container() {
        let mock = MockRunner::new(vec![ok_output("exited\n"), ok_output("")]);
        let calls = Arc::clone(&mock.calls);
        let exec = ContainerExecutor {
            runner: Box::new(mock),
        };
        let result = exec.execute(
            &make_params(&[
                ("container", "web"),
                ("image", "nginx:latest"),
                ("state", "running"),
            ]),
            false,
        );
        assert_eq!(result.action, Action::Updated);
        assert!(result.changed);
        assert!(result.error.is_none());
        let calls = calls.lock().unwrap();
        assert_eq!(calls.len(), 2);
        assert_eq!(calls[1].1[0], "start");
    }

    #[test]
    fn stop_running_container() {
        let mock = MockRunner::new(vec![ok_output("running\n"), ok_output("")]);
        let calls = Arc::clone(&mock.calls);
        let exec = ContainerExecutor {
            runner: Box::new(mock),
        };
        let result = exec.execute(
            &make_params(&[("container", "web"), ("state", "stopped")]),
            false,
        );
        assert_eq!(result.action, Action::Updated);
        assert!(result.changed);
        assert!(result.error.is_none());
        let calls = calls.lock().unwrap();
        assert_eq!(calls.len(), 2);
        assert_eq!(calls[1].1[0], "stop");
    }

    #[test]
    fn absent_stops_and_removes() {
        let mock = MockRunner::new(vec![
            ok_output("running\n"),
            ok_output(""),
            ok_output(""),
        ]);
        let calls = Arc::clone(&mock.calls);
        let exec = ContainerExecutor {
            runner: Box::new(mock),
        };
        let result = exec.execute(
            &make_params(&[("container", "web"), ("state", "absent")]),
            false,
        );
        assert_eq!(result.action, Action::Deleted);
        assert!(result.changed);
        assert!(result.error.is_none());
        let calls = calls.lock().unwrap();
        assert_eq!(calls.len(), 3);
        assert_eq!(calls[1].1[0], "stop");
        assert_eq!(calls[2].1[0], "rm");
    }

    #[test]
    fn absent_noop_when_missing() {
        let mock = MockRunner::new(vec![fail_output("No such container")]);
        let exec = ContainerExecutor {
            runner: Box::new(mock),
        };
        let result = exec.execute(
            &make_params(&[("container", "web"), ("state", "absent")]),
            false,
        );
        assert_eq!(result.action, Action::None);
        assert!(!result.changed);
        assert!(result.error.is_none());
    }

    #[test]
    fn dry_run_does_not_create() {
        let mock = MockRunner::new(vec![fail_output("No such container")]);
        let calls = Arc::clone(&mock.calls);
        let exec = ContainerExecutor {
            runner: Box::new(mock),
        };
        let result = exec.execute(
            &make_params(&[
                ("container", "web"),
                ("image", "nginx:latest"),
                ("state", "running"),
            ]),
            true,
        );
        assert_eq!(result.action, Action::Created);
        assert!(result.changed);
        assert!(result.error.is_none());
        let calls = calls.lock().unwrap();
        assert_eq!(
            calls.len(),
            1,
            "dry_run should only call inspect, got {:?}",
            *calls
        );
    }

    #[test]
    fn present_state_no_longer_accepted() {
        // `present` was a Docker-API leakage. Vigo's three-state vocab is
        // running / stopped / absent. Use depends_on for staged starts.
        let mock = MockRunner::new(vec![fail_output("No such container")]);
        let exec = ContainerExecutor {
            runner: Box::new(mock),
        };
        let result = exec.execute(
            &make_params(&[
                ("container", "web"),
                ("image", "nginx:latest"),
                ("state", "present"),
            ]),
            false,
        );
        assert!(result.error.is_some());
        assert!(
            result
                .error
                .as_deref()
                .unwrap()
                .contains("unknown state"),
            "unexpected error: {:?}",
            result.error
        );
    }
}
