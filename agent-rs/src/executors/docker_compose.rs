// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

//! Docker Compose executor — manages multi-container stacks idempotently.

use super::{Action, CommandRunner, DefaultRunner, Executor, ResourceResult};
use std::collections::HashMap;
use std::time::Instant;

pub struct DockerComposeExecutor {
    pub runner: Box<dyn CommandRunner>,
}

impl DockerComposeExecutor {
    pub fn new() -> Self {
        Self {
            runner: Box::new(DefaultRunner),
        }
    }

    /// Check if compose services are running. Returns (all_running, any_exist).
    fn services_status(&self, compose_file: &str, project: Option<&str>) -> (bool, bool) {
        let mut args = vec!["compose".to_string()];
        args.push("-f".into());
        args.push(compose_file.into());
        if let Some(p) = project {
            args.push("-p".into());
            args.push(p.into());
        }
        args.push("ps".into());
        args.push("--format".into());
        args.push("{{.State}}".into());

        let arg_refs: Vec<&str> = args.iter().map(|s| s.as_str()).collect();
        let output = match self.runner.run("docker", &arg_refs) {
            Ok(o) if o.status.success() => {
                String::from_utf8_lossy(&o.stdout).to_string()
            }
            _ => return (false, false),
        };

        let lines: Vec<&str> = output.lines().filter(|l| !l.trim().is_empty()).collect();
        if lines.is_empty() {
            return (false, false);
        }

        let all_running = lines.iter().all(|l| l.trim() == "running");
        (all_running, true)
    }

    fn build_up_args(
        &self,
        compose_file: &str,
        params: &HashMap<String, String>,
    ) -> Vec<String> {
        let mut args = vec!["compose".to_string()];
        args.push("-f".into());
        args.push(compose_file.into());

        if let Some(project) = params.get("project") {
            if !project.is_empty() {
                args.push("-p".into());
                args.push(project.clone());
            }
        }

        args.push("up".into());
        args.push("-d".into());

        if params.get("build").map(|s| s.as_str()) == Some("true") {
            args.push("--build".into());
        }

        let pull = params.get("pull").map(|s| s.as_str()).unwrap_or("missing");
        match pull {
            "always" => { args.push("--pull".into()); args.push("always".into()); }
            "never" => { args.push("--pull".into()); args.push("never".into()); }
            _ => { args.push("--pull".into()); args.push("missing".into()); }
        }

        if params.get("remove_orphans").map(|s| s.as_str()) == Some("true") {
            args.push("--remove-orphans".into());
        }

        // Specific services at the end.
        if let Some(services) = params.get("services") {
            for svc in services.split(',').map(|s| s.trim()).filter(|s| !s.is_empty()) {
                args.push(svc.into());
            }
        }

        args
    }

    fn build_down_args(
        &self,
        compose_file: &str,
        params: &HashMap<String, String>,
    ) -> Vec<String> {
        let mut args = vec!["compose".to_string()];
        args.push("-f".into());
        args.push(compose_file.into());

        if let Some(project) = params.get("project") {
            if !project.is_empty() {
                args.push("-p".into());
                args.push(project.clone());
            }
        }

        args.push("down".into());

        if params.get("remove_orphans").map(|s| s.as_str()) == Some("true") {
            args.push("--remove-orphans".into());
        }

        if params.get("volumes").map(|s| s.as_str()) == Some("true") {
            args.push("--volumes".into());
        }

        args
    }
}

impl Executor for DockerComposeExecutor {
    fn execute(&self, params: &HashMap<String, String>, dry_run: bool) -> ResourceResult {
        let start = Instant::now();
        let compose_file = params
            .get("compose_file")
            .cloned()
            .unwrap_or_default();

        let mut result = ResourceResult::new("docker_compose", &compose_file);

        if compose_file.is_empty() {
            result.error = Some(
                "docker_compose executor: compose_file param is required".into(),
            );
            result.duration_ms = start.elapsed().as_millis() as i32;
            return result;
        }

        let state = params.get("state").map(|s| s.as_str()).unwrap_or("up");
        let project = params.get("project").map(|s| s.as_str());

        match state {
            "up" => {
                let (all_running, _any_exist) = self.services_status(&compose_file, project);

                if all_running {
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

                let up_args = self.build_up_args(&compose_file, params);
                let arg_refs: Vec<&str> = up_args.iter().map(|s| s.as_str()).collect();
                match self.runner.run("docker", &arg_refs) {
                    Ok(o) if !o.status.success() => {
                        let stderr = String::from_utf8_lossy(&o.stderr);
                        result.error =
                            Some(format!("docker compose up: {}", stderr));
                    }
                    Err(e) => {
                        result.error =
                            Some(format!("docker compose up: {}", e));
                    }
                    _ => {}
                }
            }
            "down" => {
                let (_all_running, any_exist) = self.services_status(&compose_file, project);

                if !any_exist {
                    result.action = Action::None;
                    result.duration_ms = start.elapsed().as_millis() as i32;
                    return result;
                }

                result.action = Action::Deleted;
                result.changed = true;

                if dry_run {
                    result.duration_ms = start.elapsed().as_millis() as i32;
                    return result;
                }

                let down_args = self.build_down_args(&compose_file, params);
                let arg_refs: Vec<&str> = down_args.iter().map(|s| s.as_str()).collect();
                match self.runner.run("docker", &arg_refs) {
                    Ok(o) if !o.status.success() => {
                        let stderr = String::from_utf8_lossy(&o.stderr);
                        result.error =
                            Some(format!("docker compose down: {}", stderr));
                    }
                    Err(e) => {
                        result.error =
                            Some(format!("docker compose down: {}", e));
                    }
                    _ => {}
                }
            }
            _ => {
                result.error = Some(format!(
                    "docker_compose executor: unknown state {:?}",
                    state
                ));
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
    fn missing_compose_file_errors() {
        let mock = MockRunner::new(vec![]);
        let exec = DockerComposeExecutor {
            runner: Box::new(mock),
        };
        let result = exec.execute(&make_params(&[]), false);
        assert!(result.error.is_some());
        assert!(
            result
                .error
                .as_deref()
                .unwrap()
                .contains("compose_file param is required"),
            "unexpected error: {:?}",
            result.error
        );
    }

    #[test]
    fn up_already_running() {
        // docker compose ps returns all services running
        let mock = MockRunner::new(vec![ok_output("running\nrunning\n")]);
        let calls = Arc::clone(&mock.calls);
        let exec = DockerComposeExecutor {
            runner: Box::new(mock),
        };
        let result = exec.execute(
            &make_params(&[
                ("compose_file", "/opt/app/docker-compose.yml"),
                ("state", "up"),
            ]),
            false,
        );
        assert_eq!(result.action, Action::None);
        assert!(!result.changed);
        assert!(result.error.is_none());
        let calls = calls.lock().unwrap();
        assert_eq!(calls.len(), 1); // Only the ps check
    }

    #[test]
    fn up_services_not_running() {
        // docker compose ps returns no services (empty output)
        // docker compose up succeeds
        let mock = MockRunner::new(vec![
            ok_output(""),                 // ps: no services
            ok_output("Container started"), // up
        ]);
        let calls = Arc::clone(&mock.calls);
        let exec = DockerComposeExecutor {
            runner: Box::new(mock),
        };
        let result = exec.execute(
            &make_params(&[
                ("compose_file", "/opt/app/docker-compose.yml"),
                ("state", "up"),
            ]),
            false,
        );
        assert_eq!(result.action, Action::Updated);
        assert!(result.changed);
        assert!(result.error.is_none(), "unexpected error: {:?}", result.error);
        let calls = calls.lock().unwrap();
        assert_eq!(calls.len(), 2);
        assert!(calls[1].1.contains(&"up".to_string()));
        assert!(calls[1].1.contains(&"-d".to_string()));
    }

    #[test]
    fn up_partial_running() {
        // One running, one exited
        let mock = MockRunner::new(vec![
            ok_output("running\nexited\n"), // ps
            ok_output(""),                  // up
        ]);
        let exec = DockerComposeExecutor {
            runner: Box::new(mock),
        };
        let result = exec.execute(
            &make_params(&[
                ("compose_file", "/opt/app/docker-compose.yml"),
                ("state", "up"),
            ]),
            false,
        );
        assert_eq!(result.action, Action::Updated);
        assert!(result.changed);
    }

    #[test]
    fn up_with_build_and_pull() {
        let mock = MockRunner::new(vec![
            ok_output(""),  // ps: no services
            ok_output(""),  // up
        ]);
        let calls = Arc::clone(&mock.calls);
        let exec = DockerComposeExecutor {
            runner: Box::new(mock),
        };
        let result = exec.execute(
            &make_params(&[
                ("compose_file", "/opt/app/docker-compose.yml"),
                ("state", "up"),
                ("build", "true"),
                ("pull", "always"),
            ]),
            false,
        );
        assert!(result.changed);
        assert!(result.error.is_none(), "unexpected error: {:?}", result.error);
        let calls = calls.lock().unwrap();
        let up_args = &calls[1].1;
        assert!(up_args.contains(&"--build".to_string()));
        assert!(up_args.contains(&"always".to_string()));
    }

    #[test]
    fn up_with_project_and_services() {
        let mock = MockRunner::new(vec![
            ok_output(""),  // ps
            ok_output(""),  // up
        ]);
        let calls = Arc::clone(&mock.calls);
        let exec = DockerComposeExecutor {
            runner: Box::new(mock),
        };
        let result = exec.execute(
            &make_params(&[
                ("compose_file", "/opt/app/docker-compose.yml"),
                ("state", "up"),
                ("project", "myapp"),
                ("services", "web,db"),
            ]),
            false,
        );
        assert!(result.changed);
        assert!(result.error.is_none(), "unexpected error: {:?}", result.error);
        let calls = calls.lock().unwrap();
        let up_args = &calls[1].1;
        assert!(up_args.contains(&"-p".to_string()));
        assert!(up_args.contains(&"myapp".to_string()));
        assert!(up_args.contains(&"web".to_string()));
        assert!(up_args.contains(&"db".to_string()));
    }

    #[test]
    fn down_already_gone() {
        // ps fails or returns empty: no services
        let mock = MockRunner::new(vec![ok_output("")]);
        let exec = DockerComposeExecutor {
            runner: Box::new(mock),
        };
        let result = exec.execute(
            &make_params(&[
                ("compose_file", "/opt/app/docker-compose.yml"),
                ("state", "down"),
            ]),
            false,
        );
        assert_eq!(result.action, Action::None);
        assert!(!result.changed);
    }

    #[test]
    fn down_removes_running_services() {
        let mock = MockRunner::new(vec![
            ok_output("running\nrunning\n"), // ps
            ok_output(""),                   // down
        ]);
        let calls = Arc::clone(&mock.calls);
        let exec = DockerComposeExecutor {
            runner: Box::new(mock),
        };
        let result = exec.execute(
            &make_params(&[
                ("compose_file", "/opt/app/docker-compose.yml"),
                ("state", "down"),
            ]),
            false,
        );
        assert_eq!(result.action, Action::Deleted);
        assert!(result.changed);
        assert!(result.error.is_none());
        let calls = calls.lock().unwrap();
        assert_eq!(calls.len(), 2);
        assert!(calls[1].1.contains(&"down".to_string()));
    }

    #[test]
    fn down_with_volumes() {
        let mock = MockRunner::new(vec![
            ok_output("running\n"), // ps
            ok_output(""),          // down
        ]);
        let calls = Arc::clone(&mock.calls);
        let exec = DockerComposeExecutor {
            runner: Box::new(mock),
        };
        let result = exec.execute(
            &make_params(&[
                ("compose_file", "/opt/app/docker-compose.yml"),
                ("state", "down"),
                ("volumes", "true"),
            ]),
            false,
        );
        assert!(result.changed);
        let calls = calls.lock().unwrap();
        assert!(calls[1].1.contains(&"--volumes".to_string()));
    }

    #[test]
    fn dry_run_does_not_execute() {
        let mock = MockRunner::new(vec![
            ok_output(""), // ps: no services
        ]);
        let calls = Arc::clone(&mock.calls);
        let exec = DockerComposeExecutor {
            runner: Box::new(mock),
        };
        let result = exec.execute(
            &make_params(&[
                ("compose_file", "/opt/app/docker-compose.yml"),
                ("state", "up"),
            ]),
            true,
        );
        assert_eq!(result.action, Action::Updated);
        assert!(result.changed);
        assert!(result.error.is_none());
        let calls = calls.lock().unwrap();
        assert_eq!(calls.len(), 1, "dry_run should only call ps, got {:?}", *calls);
    }

    #[test]
    fn unknown_state_errors() {
        let mock = MockRunner::new(vec![]);
        let exec = DockerComposeExecutor {
            runner: Box::new(mock),
        };
        let result = exec.execute(
            &make_params(&[
                ("compose_file", "/opt/app/docker-compose.yml"),
                ("state", "bogus"),
            ]),
            false,
        );
        assert!(result.error.is_some());
        assert!(result.error.as_deref().unwrap().contains("unknown state"));
    }
}
