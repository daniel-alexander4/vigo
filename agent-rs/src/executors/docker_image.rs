// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: BUSL-1.1

//! Docker image executor — manages Docker images idempotently.

use super::{Action, CommandRunner, DefaultRunner, Executor, ResourceResult};
use std::collections::HashMap;
use std::time::Instant;

pub struct DockerImageExecutor {
    pub runner: Box<dyn CommandRunner>,
}

impl DockerImageExecutor {
    pub fn new() -> Self {
        Self {
            runner: Box::new(DefaultRunner),
        }
    }

    fn image_exists(&self, image: &str) -> bool {
        match self.runner.run("docker", &["image", "inspect", image]) {
            Ok(o) => o.status.success(),
            Err(_) => false,
        }
    }
}

impl Executor for DockerImageExecutor {
    fn execute(&self, params: &HashMap<String, String>, dry_run: bool) -> ResourceResult {
        let start = Instant::now();
        let mut image = params
            .get("image")
            .or_else(|| params.get("name"))
            .cloned()
            .unwrap_or_default();

        let mut result = ResourceResult::new("docker_image", &image);

        if image.is_empty() {
            result.error = Some("docker_image executor: image param is required".into());
            result.duration_ms = start.elapsed().as_millis() as i32;
            return result;
        }

        let state = params.get("state").map(|s| s.as_str()).unwrap_or("present");
        let force = params.get("force").map(|s| s == "true").unwrap_or(false);

        // Append tag if not specified.
        if !image.contains(':') {
            let tag = params.get("tag").map(|s| s.as_str()).unwrap_or("latest");
            image = format!("{}:{}", image, tag);
            result.resource_name = image.clone();
        }

        let exists = self.image_exists(&image);

        match state {
            "present" => {
                if exists && !force {
                    result.action = Action::None;
                    result.duration_ms = start.elapsed().as_millis() as i32;
                    return result;
                }

                result.action = if exists {
                    Action::Updated
                } else {
                    Action::Created
                };
                result.changed = true;

                if dry_run {
                    result.duration_ms = start.elapsed().as_millis() as i32;
                    return result;
                }

                match self.runner.run("docker", &["pull", &image]) {
                    Ok(o) if !o.status.success() => {
                        let stderr = String::from_utf8_lossy(&o.stderr);
                        result.error = Some(format!("docker pull {}: {}", image, stderr));
                    }
                    Err(e) => result.error = Some(format!("docker pull {}: {}", image, e)),
                    _ => {}
                }
            }
            "absent" => {
                if !exists {
                    result.action = Action::None;
                } else {
                    result.action = Action::Deleted;
                    result.changed = true;
                    if !dry_run {
                        match self.runner.run("docker", &["rmi", &image]) {
                            Ok(o) if !o.status.success() => {
                                let stderr = String::from_utf8_lossy(&o.stderr);
                                result.error = Some(format!("docker rmi {}: {}", image, stderr));
                            }
                            Err(e) => result.error = Some(format!("docker rmi {}: {}", image, e)),
                            _ => {}
                        }
                    }
                }
            }
            _ => {
                result.error = Some(format!("docker_image executor: unknown state {:?}", state));
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
    fn missing_image_errors() {
        let mock = MockRunner::new(vec![]);
        let exec = DockerImageExecutor {
            runner: Box::new(mock),
        };
        let result = exec.execute(&make_params(&[]), false);
        assert!(result.error.is_some());
        assert!(
            result.error.as_deref().unwrap().contains("image param is required"),
            "unexpected error: {:?}",
            result.error
        );
    }

    #[test]
    fn pull_new_image() {
        // image inspect fails, pull succeeds
        let mock = MockRunner::new(vec![
            fail_output("No such image"),  // docker image inspect
            ok_output(""),                  // docker pull
        ]);
        let calls = Arc::clone(&mock.calls);
        let exec = DockerImageExecutor {
            runner: Box::new(mock),
        };
        let result = exec.execute(
            &make_params(&[("image", "nginx:latest")]),
            false,
        );
        assert_eq!(result.action, Action::Created);
        assert!(result.changed);
        assert!(result.error.is_none());
        let calls = calls.lock().unwrap();
        assert_eq!(calls.len(), 2);
        assert_eq!(calls[0].1[0], "image");
        assert_eq!(calls[1].1[0], "pull");
    }

    #[test]
    fn image_already_present() {
        let mock = MockRunner::new(vec![
            ok_output(""),  // docker image inspect succeeds
        ]);
        let exec = DockerImageExecutor {
            runner: Box::new(mock),
        };
        let result = exec.execute(
            &make_params(&[("image", "nginx:latest")]),
            false,
        );
        assert_eq!(result.action, Action::None);
        assert!(!result.changed);
        assert!(result.error.is_none());
    }

    #[test]
    fn force_pull_existing() {
        // image exists, force=true -> pull again -> Updated
        let mock = MockRunner::new(vec![
            ok_output(""),  // docker image inspect
            ok_output(""),  // docker pull
        ]);
        let calls = Arc::clone(&mock.calls);
        let exec = DockerImageExecutor {
            runner: Box::new(mock),
        };
        let result = exec.execute(
            &make_params(&[("image", "nginx:latest"), ("force", "true")]),
            false,
        );
        assert_eq!(result.action, Action::Updated);
        assert!(result.changed);
        assert!(result.error.is_none());
        let calls = calls.lock().unwrap();
        assert_eq!(calls.len(), 2);
        assert_eq!(calls[1].1[0], "pull");
    }

    #[test]
    fn absent_removes_image() {
        // image exists, state=absent -> rmi
        let mock = MockRunner::new(vec![
            ok_output(""),  // docker image inspect
            ok_output(""),  // docker rmi
        ]);
        let calls = Arc::clone(&mock.calls);
        let exec = DockerImageExecutor {
            runner: Box::new(mock),
        };
        let result = exec.execute(
            &make_params(&[("image", "nginx:latest"), ("state", "absent")]),
            false,
        );
        assert_eq!(result.action, Action::Deleted);
        assert!(result.changed);
        assert!(result.error.is_none());
        let calls = calls.lock().unwrap();
        assert_eq!(calls.len(), 2);
        assert_eq!(calls[1].1[0], "rmi");
    }

    #[test]
    fn tag_appended_when_missing() {
        // image "nginx" (no tag) -> should inspect "nginx:latest"
        let mock = MockRunner::new(vec![
            ok_output(""),  // docker image inspect nginx:latest
        ]);
        let calls = Arc::clone(&mock.calls);
        let exec = DockerImageExecutor {
            runner: Box::new(mock),
        };
        let result = exec.execute(
            &make_params(&[("image", "nginx")]),
            false,
        );
        assert_eq!(result.action, Action::None);
        assert_eq!(result.resource_name, "nginx:latest");
        let calls = calls.lock().unwrap();
        // The inspect call should use "nginx:latest"
        assert!(
            calls[0].1.contains(&"nginx:latest".to_string()),
            "expected nginx:latest in args, got {:?}",
            calls[0].1
        );
    }
}
