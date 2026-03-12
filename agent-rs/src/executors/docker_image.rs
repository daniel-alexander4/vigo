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
