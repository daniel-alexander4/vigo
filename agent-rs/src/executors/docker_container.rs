// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: BUSL-1.1

//! Docker container executor — manages Docker containers idempotently.

use super::{Action, CommandRunner, DefaultRunner, Executor, ResourceResult};
use std::collections::HashMap;
use std::time::Instant;

pub struct DockerContainerExecutor {
    pub runner: Box<dyn CommandRunner>,
}

impl DockerContainerExecutor {
    pub fn new() -> Self {
        Self {
            runner: Box::new(DefaultRunner),
        }
    }

    fn container_status(&self, name: &str) -> Option<String> {
        let output = self
            .runner
            .run(
                "docker",
                &["inspect", "--format", "{{.State.Status}}", name],
            )
            .ok()?;
        if output.status.success() {
            Some(String::from_utf8_lossy(&output.stdout).trim().to_string())
        } else {
            None
        }
    }

    fn container_image(&self, name: &str) -> Option<String> {
        let output = self
            .runner
            .run(
                "docker",
                &["inspect", "--format", "{{.Config.Image}}", name],
            )
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

impl Executor for DockerContainerExecutor {
    fn execute(&self, params: &HashMap<String, String>, dry_run: bool) -> ResourceResult {
        let start = Instant::now();
        let name = params
            .get("container")
            .cloned()
            .unwrap_or_default();

        let mut result = ResourceResult::new("docker_container", &name);

        if name.is_empty() {
            result.error = Some("docker_container executor: container param is required".into());
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
                    result.error = Some("docker_container executor: image required for state=running".into());
                    result.duration_ms = start.elapsed().as_millis() as i32;
                    return result;
                }

                if exists && running {
                    // Check if image changed.
                    let current_image = self.container_image(&name).unwrap_or_default();
                    if current_image == image {
                        result.action = Action::None;
                        result.duration_ms = start.elapsed().as_millis() as i32;
                        return result;
                    }

                    // Image changed — recreate.
                    result.action = Action::Updated;
                    result.changed = true;

                    if dry_run {
                        result.duration_ms = start.elapsed().as_millis() as i32;
                        return result;
                    }

                    let _ = self.runner.run("docker", &["stop", &name]);
                    let _ = self.runner.run("docker", &["rm", &name]);
                } else if exists {
                    // Container exists but not running — start it.
                    result.action = Action::Updated;
                    result.changed = true;

                    if dry_run {
                        result.duration_ms = start.elapsed().as_millis() as i32;
                        return result;
                    }

                    match self.runner.run("docker", &["start", &name]) {
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

                // Pull image if needed.
                if pull == "always" || (pull == "missing" && !exists) {
                    let _ = self.runner.run("docker", &["pull", &image]);
                }

                // Create container.
                let create_args = self.build_create_args(&name, &image, params);
                let arg_refs: Vec<&str> = create_args.iter().map(|s| s.as_str()).collect();
                match self.runner.run("docker", &arg_refs) {
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

                // Start container.
                match self.runner.run("docker", &["start", &name]) {
                    Ok(o) if !o.status.success() => {
                        let stderr = String::from_utf8_lossy(&o.stderr);
                        result.error = Some(format!("docker start: {}", stderr));
                    }
                    Err(e) => result.error = Some(format!("docker start: {}", e)),
                    _ => {}
                }
            }
            "present" => {
                if exists {
                    result.action = Action::None;
                } else {
                    if image.is_empty() {
                        result.error = Some("docker_container executor: image required for state=present".into());
                        result.duration_ms = start.elapsed().as_millis() as i32;
                        return result;
                    }

                    result.action = Action::Created;
                    result.changed = true;

                    if !dry_run {
                        if pull != "never" {
                            let _ = self.runner.run("docker", &["pull", &image]);
                        }
                        let create_args = self.build_create_args(&name, &image, params);
                        let arg_refs: Vec<&str> = create_args.iter().map(|s| s.as_str()).collect();
                        match self.runner.run("docker", &arg_refs) {
                            Ok(o) if !o.status.success() => {
                                let stderr = String::from_utf8_lossy(&o.stderr);
                                result.error = Some(format!("docker create: {}", stderr));
                            }
                            Err(e) => result.error = Some(format!("docker create: {}", e)),
                            _ => {}
                        }
                    }
                }
            }
            "stopped" => {
                if !exists || !running {
                    result.action = Action::None;
                } else {
                    result.action = Action::Updated;
                    result.changed = true;
                    if !dry_run {
                        match self.runner.run("docker", &["stop", &name]) {
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
                            let _ = self.runner.run("docker", &["stop", &name]);
                        }
                        match self.runner.run("docker", &["rm", &name]) {
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
                result.error = Some(format!("docker_container executor: unknown state {:?}", state));
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
        let exec = DockerContainerExecutor {
            runner: Box::new(mock),
        };
        let result = exec.execute(&make_params(&[]), false);
        assert!(result.error.is_some());
        assert!(
            result.error.as_deref().unwrap().contains("container param is required"),
            "unexpected error: {:?}",
            result.error
        );
    }

    #[test]
    fn running_container_already_running_same_image() {
        // container_status() -> docker inspect --format {{.State.Status}} -> "running"
        // container_image()  -> docker inspect --format {{.Config.Image}} -> "nginx:latest"
        let mock = MockRunner::new(vec![
            ok_output("running\n"),
            ok_output("nginx:latest\n"),
        ]);
        let calls = Arc::clone(&mock.calls);
        let exec = DockerContainerExecutor {
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
        // Both calls are docker inspect
        assert_eq!(calls[0].0, "docker");
        assert_eq!(calls[1].0, "docker");
    }

    #[test]
    fn create_and_start_new_container() {
        // container_status() -> inspect fails (not exists)
        // pull, create, start
        let mock = MockRunner::new(vec![
            fail_output("No such container"),  // inspect (status)
            ok_output(""),                      // docker pull
            ok_output(""),                      // docker create
            ok_output(""),                      // docker start
        ]);
        let calls = Arc::clone(&mock.calls);
        let exec = DockerContainerExecutor {
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
        assert!(result.error.is_none(), "unexpected error: {:?}", result.error);
        let calls = calls.lock().unwrap();
        assert_eq!(calls.len(), 4);
        assert_eq!(calls[0].0, "docker"); // inspect
        assert_eq!(calls[1].1[0], "pull");
        assert_eq!(calls[2].1[0], "create");
        assert_eq!(calls[3].1[0], "start");
    }

    #[test]
    fn start_stopped_container() {
        // container_status() -> "exited" (exists but not running)
        // docker start
        let mock = MockRunner::new(vec![
            ok_output("exited\n"),  // inspect (status)
            ok_output(""),           // docker start
        ]);
        let calls = Arc::clone(&mock.calls);
        let exec = DockerContainerExecutor {
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
        // container_status() -> "running"
        // docker stop
        let mock = MockRunner::new(vec![
            ok_output("running\n"),  // inspect (status)
            ok_output(""),            // docker stop
        ]);
        let calls = Arc::clone(&mock.calls);
        let exec = DockerContainerExecutor {
            runner: Box::new(mock),
        };
        let result = exec.execute(
            &make_params(&[
                ("container", "web"),
                ("state", "stopped"),
            ]),
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
        // container_status() -> "running"
        // docker stop, docker rm
        let mock = MockRunner::new(vec![
            ok_output("running\n"),  // inspect (status)
            ok_output(""),            // docker stop
            ok_output(""),            // docker rm
        ]);
        let calls = Arc::clone(&mock.calls);
        let exec = DockerContainerExecutor {
            runner: Box::new(mock),
        };
        let result = exec.execute(
            &make_params(&[
                ("container", "web"),
                ("state", "absent"),
            ]),
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
        // container_status() -> None (inspect fails)
        let mock = MockRunner::new(vec![
            fail_output("No such container"),
        ]);
        let exec = DockerContainerExecutor {
            runner: Box::new(mock),
        };
        let result = exec.execute(
            &make_params(&[
                ("container", "web"),
                ("state", "absent"),
            ]),
            false,
        );
        assert_eq!(result.action, Action::None);
        assert!(!result.changed);
        assert!(result.error.is_none());
    }

    #[test]
    fn dry_run_does_not_create() {
        // container_status() -> fails (not exists)
        // dry_run=true: should not call pull/create/start
        let mock = MockRunner::new(vec![
            fail_output("No such container"),
        ]);
        let calls = Arc::clone(&mock.calls);
        let exec = DockerContainerExecutor {
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
        // Only the inspect call should have been made
        let calls = calls.lock().unwrap();
        assert_eq!(calls.len(), 1, "dry_run should only call inspect, got {:?}", *calls);
    }
}
