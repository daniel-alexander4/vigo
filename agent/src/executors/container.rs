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

/// Health-poll cadence for `wait_for_healthy`. Docker's healthcheck loop is
/// what actually determines health; this is just how often the agent asks.
const HEALTH_POLL_INTERVAL: Duration = Duration::from_secs(2);

/// Hard cap on how long the agent waits for a container to report healthy.
/// Operators control the actual liveness window via Docker's
/// `--health-interval`/`--health-retries`/`--health-start-period`.
const HEALTH_WAIT_MAX: Duration = Duration::from_secs(120);

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

    fn health_status(&self, name: &str) -> Option<String> {
        let output = self
            .run(&["inspect", "--format", "{{.State.Health.Status}}", name])
            .ok()?;
        if !output.status.success() {
            return None;
        }
        let s = String::from_utf8_lossy(&output.stdout).trim().to_string();
        // Docker emits `<no value>` when the container has no healthcheck.
        if s.is_empty() || s == "<no value>" {
            None
        } else {
            Some(s)
        }
    }

    fn wait_for_healthy(&self, name: &str) -> Result<(), String> {
        let deadline = Instant::now() + HEALTH_WAIT_MAX;
        loop {
            match self.health_status(name).as_deref() {
                Some("healthy") => return Ok(()),
                Some("unhealthy") => {
                    return Err(format!("container {} reported unhealthy", name));
                }
                _ => {} // "starting" or no healthcheck configured (None)
            }
            if Instant::now() >= deadline {
                return Err(format!(
                    "container {} did not become healthy within {}s",
                    name,
                    HEALTH_WAIT_MAX.as_secs()
                ));
            }
            std::thread::sleep(HEALTH_POLL_INTERVAL);
        }
    }

    fn build_create_args(
        &self,
        name: &str,
        image: &str,
        params: &HashMap<String, String>,
    ) -> Vec<String> {
        let mut args = vec!["create".to_string(), "--name".into(), name.into()];

        push_repeated(&mut args, params, "ports", "-p");
        push_repeated(&mut args, params, "env", "-e");
        push_repeated(&mut args, params, "volumes", "-v");
        push_repeated(&mut args, params, "tmpfs", "--tmpfs");
        push_repeated(&mut args, params, "cap_drop", "--cap-drop");
        push_repeated(&mut args, params, "cap_add", "--cap-add");
        push_repeated(&mut args, params, "labels", "--label");
        push_repeated(&mut args, params, "log_options", "--log-opt");

        push_single(&mut args, params, "network", "--network");
        push_single(&mut args, params, "restart", "--restart");
        push_single(&mut args, params, "user", "--user");
        push_single(&mut args, params, "log_driver", "--log-driver");

        push_single(&mut args, params, "healthcheck_command", "--health-cmd");
        push_single(&mut args, params, "healthcheck_interval", "--health-interval");
        push_single(&mut args, params, "healthcheck_timeout", "--health-timeout");
        push_single(&mut args, params, "healthcheck_retries", "--health-retries");
        push_single(
            &mut args,
            params,
            "healthcheck_start_period",
            "--health-start-period",
        );

        if params
            .get("read_only")
            .map(|s| s == "true")
            .unwrap_or(false)
        {
            args.push("--read-only".into());
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

/// Append `flag <item>` for each comma-separated item in `params[key]`.
fn push_repeated(
    args: &mut Vec<String>,
    params: &HashMap<String, String>,
    key: &str,
    flag: &str,
) {
    if let Some(val) = params.get(key) {
        for item in val.split(',').map(|s| s.trim()).filter(|s| !s.is_empty()) {
            args.push(flag.into());
            args.push(item.into());
        }
    }
}

/// Append `flag <value>` once if `params[key]` is set and non-empty.
fn push_single(
    args: &mut Vec<String>,
    params: &HashMap<String, String>,
    key: &str,
    flag: &str,
) {
    if let Some(val) = params.get(key) {
        if !val.is_empty() {
            args.push(flag.into());
            args.push(val.clone());
        }
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

        // Secret refs inside `volumes:` are a UX trap — the operator likely
        // expects the agent to mount the decrypted secret as a file. That's a
        // separate feature; for v1, refuse loudly so they don't ship a broken
        // bind mount thinking it works.
        if let Some(vols) = params.get("volumes") {
            for v in vols.split(',').map(|s| s.trim()).filter(|s| !s.is_empty()) {
                if v.starts_with("secret:") {
                    result.error = Some(
                        "container executor: `secret:` in volumes is not supported in v1; \
                         use env vars or write the secret to a file with a separate `file:` resource"
                            .into(),
                    );
                    result.duration_ms = start.elapsed().as_millis() as i32;
                    return result;
                }
            }
        }

        let state = params.get("state").map(|s| s.as_str()).unwrap_or("running");
        let image = params.get("image").cloned().unwrap_or_default();
        let pull = params.get("pull").map(|s| s.as_str()).unwrap_or("missing");
        let has_healthcheck = params
            .get("healthcheck_command")
            .map(|s| !s.is_empty())
            .unwrap_or(false);

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
                            result.duration_ms = start.elapsed().as_millis() as i32;
                            return result;
                        }
                        Err(e) => {
                            result.error = Some(format!("docker start: {}", e));
                            result.duration_ms = start.elapsed().as_millis() as i32;
                            return result;
                        }
                        _ => {}
                    }
                    if has_healthcheck {
                        if let Err(e) = self.wait_for_healthy(&name) {
                            result.error = Some(e);
                        }
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
                        result.duration_ms = start.elapsed().as_millis() as i32;
                        return result;
                    }
                    Err(e) => {
                        result.error = Some(format!("docker start: {}", e));
                        result.duration_ms = start.elapsed().as_millis() as i32;
                        return result;
                    }
                    _ => {}
                }

                if has_healthcheck {
                    if let Err(e) = self.wait_for_healthy(&name) {
                        result.error = Some(e);
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

    #[test]
    fn secret_in_volumes_rejected() {
        let mock = MockRunner::new(vec![]);
        let exec = ContainerExecutor {
            runner: Box::new(mock),
        };
        let result = exec.execute(
            &make_params(&[
                ("container", "web"),
                ("image", "nginx:latest"),
                ("volumes", "secret:db/password:/etc/secrets/dbpass"),
            ]),
            false,
        );
        assert!(result.error.is_some());
        assert!(
            result
                .error
                .as_deref()
                .unwrap()
                .contains("secret:` in volumes is not supported"),
            "unexpected error: {:?}",
            result.error
        );
    }

    #[test]
    fn create_args_include_full_surface() {
        let exec = ContainerExecutor {
            runner: Box::new(MockRunner::new(vec![])),
        };
        let params = make_params(&[
            ("container", "web"),
            ("ports", "80:80,443:443"),
            ("env", "FOO=bar"),
            ("volumes", "/data:/var/lib/data"),
            ("tmpfs", "/tmp,/run"),
            ("cap_drop", "ALL"),
            ("cap_add", "SETUID,SETGID"),
            ("labels", "vigo.module=redis"),
            ("log_options", "max-size=10m,max-file=5"),
            ("network", "app-net"),
            ("restart", "unless-stopped"),
            ("user", "999:999"),
            ("log_driver", "json-file"),
            ("healthcheck_command", "redis-cli ping"),
            ("healthcheck_interval", "10s"),
            ("healthcheck_retries", "3"),
            ("healthcheck_start_period", "5s"),
            ("read_only", "true"),
        ]);
        let args = exec.build_create_args("web", "redis:7-alpine", &params);

        // Order is: action, --name <n>, repeated/single flags, --read-only, image, command...
        assert_eq!(args[0], "create");
        assert_eq!(args[1], "--name");
        assert_eq!(args[2], "web");
        // Image lands before any optional trailing command.
        assert!(args.contains(&"redis:7-alpine".to_string()));

        // Spot-check every new flag made it in.
        let joined = args.join(" ");
        assert!(joined.contains("-p 80:80"), "missing -p; got: {}", joined);
        assert!(joined.contains("-p 443:443"));
        assert!(joined.contains("-e FOO=bar"));
        assert!(joined.contains("-v /data:/var/lib/data"));
        assert!(joined.contains("--tmpfs /tmp"));
        assert!(joined.contains("--tmpfs /run"));
        assert!(joined.contains("--cap-drop ALL"));
        assert!(joined.contains("--cap-add SETUID"));
        assert!(joined.contains("--cap-add SETGID"));
        assert!(joined.contains("--label vigo.module=redis"));
        assert!(joined.contains("--log-opt max-size=10m"));
        assert!(joined.contains("--log-opt max-file=5"));
        assert!(joined.contains("--network app-net"));
        assert!(joined.contains("--restart unless-stopped"));
        assert!(joined.contains("--user 999:999"));
        assert!(joined.contains("--log-driver json-file"));
        assert!(joined.contains("--health-cmd redis-cli ping"));
        assert!(joined.contains("--health-interval 10s"));
        assert!(joined.contains("--health-retries 3"));
        assert!(joined.contains("--health-start-period 5s"));
        assert!(joined.contains("--read-only"));
    }

    #[test]
    fn read_only_false_omits_flag() {
        let exec = ContainerExecutor {
            runner: Box::new(MockRunner::new(vec![])),
        };
        let params = make_params(&[("container", "web"), ("read_only", "false")]);
        let args = exec.build_create_args("web", "redis:7", &params);
        assert!(!args.iter().any(|a| a == "--read-only"));
    }

    #[test]
    fn create_then_wait_for_healthy() {
        // inspect (not exists) → pull → create → start → health-inspect (healthy)
        let mock = MockRunner::new(vec![
            fail_output("No such container"),
            ok_output(""),
            ok_output(""),
            ok_output(""),
            ok_output("healthy\n"),
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
                ("healthcheck_command", "curl -f localhost"),
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
        assert_eq!(calls.len(), 5);
        // Last call is the health-status inspect.
        assert_eq!(calls[4].1[0], "inspect");
        assert!(calls[4].1.iter().any(|a| a == "{{.State.Health.Status}}"));
    }

    #[test]
    fn unhealthy_container_surfaces_error() {
        let mock = MockRunner::new(vec![
            fail_output("No such container"),
            ok_output(""),
            ok_output(""),
            ok_output(""),
            ok_output("unhealthy\n"),
        ]);
        let exec = ContainerExecutor {
            runner: Box::new(mock),
        };
        let result = exec.execute(
            &make_params(&[
                ("container", "web"),
                ("image", "nginx:latest"),
                ("state", "running"),
                ("healthcheck_command", "curl -f localhost"),
            ]),
            false,
        );
        assert_eq!(result.action, Action::Created);
        assert!(result.changed);
        assert!(result.error.is_some());
        assert!(
            result
                .error
                .as_deref()
                .unwrap()
                .contains("reported unhealthy"),
            "unexpected error: {:?}",
            result.error
        );
    }
}
