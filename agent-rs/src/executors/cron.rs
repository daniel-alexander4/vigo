// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: BUSL-1.1

//! Cron executor — manages cron jobs via /etc/cron.d/ drop-in files.

use super::{Action, Executor, ResourceResult};
use sha2::{Digest, Sha256};
use std::collections::HashMap;
use std::path::Path;
use std::time::Instant;

pub struct CronExecutor;

impl Executor for CronExecutor {
    fn execute(&self, params: &HashMap<String, String>, dry_run: bool) -> ResourceResult {
        let start = Instant::now();
        let name = params.get("name").cloned().unwrap_or_default();
        let mut result = ResourceResult::new("cron", &name);

        if name.is_empty() {
            result.error = Some("cron executor: name param is required".into());
            result.duration_ms = start.elapsed().as_millis() as i32;
            return result;
        }

        let filename = sanitize_cron_name(&name);
        let path = format!("/etc/cron.d/{}", filename);
        let state = params.get("state").map(|s| s.as_str()).unwrap_or("present");

        if state == "absent" {
            if !Path::new(&path).exists() {
                result.action = Action::None;
            } else {
                result.action = Action::Deleted;
                result.changed = true;
                if !dry_run {
                    if let Err(e) = std::fs::remove_file(&path) {
                        result.error = Some(format!("remove {}: {}", path, e));
                    }
                }
            }
            result.duration_ms = start.elapsed().as_millis() as i32;
            return result;
        }

        let job = params.get("job").cloned().unwrap_or_default();
        if job.is_empty() {
            result.error = Some("cron executor: job param is required when state=present".into());
            result.duration_ms = start.elapsed().as_millis() as i32;
            return result;
        }

        let schedule = build_schedule(params);
        let user = params.get("user").map(|s| s.as_str()).unwrap_or("root");
        let content = format!(
            "# Managed by hobnob -- do not edit\n{} {} {}\n",
            schedule, user, job
        );

        let desired_hash = sha256_hex(content.as_bytes());
        let existing_matches = std::fs::read(&path)
            .map(|data| sha256_hex(&data) == desired_hash)
            .unwrap_or(false);

        if existing_matches {
            result.action = Action::None;
            result.duration_ms = start.elapsed().as_millis() as i32;
            return result;
        }

        result.action = if Path::new(&path).exists() {
            Action::Updated
        } else {
            Action::Created
        };
        result.changed = true;

        if !dry_run {
            if let Err(e) = std::fs::write(&path, &content) {
                result.error = Some(format!("write {}: {}", path, e));
            }
        }

        result.duration_ms = start.elapsed().as_millis() as i32;
        result
    }
}

fn build_schedule(params: &HashMap<String, String>) -> String {
    if let Some(s) = params.get("schedule") {
        if !s.is_empty() {
            return s.clone();
        }
    }
    let keys = ["minute", "hour", "day", "month", "weekday"];
    let fields: Vec<&str> = keys
        .iter()
        .map(|k| params.get(*k).map(|s| s.as_str()).unwrap_or("*"))
        .collect();
    fields.join(" ")
}

fn sanitize_cron_name(name: &str) -> String {
    name.chars()
        .map(|c| match c {
            'a'..='z' | 'A'..='Z' | '0'..='9' | '-' | '_' => c,
            _ => '-',
        })
        .collect()
}

fn sha256_hex(data: &[u8]) -> String {
    let hash = Sha256::digest(data);
    hash.iter().map(|b| format!("{:02x}", b)).collect()
}

#[cfg(test)]
mod tests {
    use super::*;

    fn make_params(pairs: &[(&str, &str)]) -> HashMap<String, String> {
        pairs
            .iter()
            .map(|(k, v)| (k.to_string(), v.to_string()))
            .collect()
    }

    #[test]
    fn missing_name_errors() {
        let exec = CronExecutor;
        let result = exec.execute(&make_params(&[]), false);
        assert!(result.error.is_some());
        assert!(
            result.error.as_deref().unwrap().contains("name param is required"),
            "unexpected error: {:?}",
            result.error
        );
    }

    #[test]
    fn missing_job_errors() {
        let exec = CronExecutor;
        let result = exec.execute(
            &make_params(&[("name", "backup")]),
            false,
        );
        assert!(result.error.is_some());
        assert!(
            result.error.as_deref().unwrap().contains("job param is required"),
            "unexpected error: {:?}",
            result.error
        );
    }

    #[test]
    fn build_schedule_from_fields() {
        let params = make_params(&[
            ("minute", "30"),
            ("hour", "2"),
            ("day", "15"),
            ("month", "6"),
            ("weekday", "1"),
        ]);
        let schedule = build_schedule(&params);
        assert_eq!(schedule, "30 2 15 6 1");
    }

    #[test]
    fn build_schedule_from_schedule_param() {
        let params = make_params(&[
            ("schedule", "*/5 * * * *"),
            ("minute", "30"),
            ("hour", "2"),
        ]);
        let schedule = build_schedule(&params);
        assert_eq!(schedule, "*/5 * * * *");
    }

    #[test]
    fn build_schedule_defaults_stars() {
        let params = make_params(&[]);
        let schedule = build_schedule(&params);
        assert_eq!(schedule, "* * * * *");
    }

    #[test]
    fn sanitize_cron_name_special_chars() {
        assert_eq!(sanitize_cron_name("my backup job"), "my-backup-job");
        assert_eq!(sanitize_cron_name("backup@daily!"), "backup-daily-");
        assert_eq!(sanitize_cron_name("clean_tmp-2"), "clean_tmp-2");
        assert_eq!(sanitize_cron_name("a/b.c:d"), "a-b-c-d");
    }

    #[test]
    fn dry_run_does_not_write() {
        let exec = CronExecutor;
        // state=absent on a file that won't exist under /etc/cron.d/ (nonexistent name).
        let result = exec.execute(
            &make_params(&[
                ("name", "hobnob-test-nonexistent-cron-99999"),
                ("state", "absent"),
            ]),
            true,
        );
        assert_eq!(result.action, Action::None);
        assert!(!result.changed);
        assert!(result.error.is_none());
    }
}
