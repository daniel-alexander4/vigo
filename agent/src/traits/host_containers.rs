// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

//! Per-container state on the local Docker daemon.
//!
//! Trait namespace: `host_containers` — a JSON array, one object per
//! container (any state, not just running):
//!
//!   - name: container name (Docker's leading `/` stripped)
//!   - image: the registry image reference the container was created with
//!   - image_id: sha256 image ID
//!   - state: Docker's `State.Status` (running, exited, restarting, …)
//!   - health: `State.Health.Status` if a healthcheck is configured;
//!     empty string otherwise
//!   - restart_count: cumulative restart count
//!   - oom_killed: whether the most-recent stop was an OOM kill
//!
//! Daemon-wide metadata (version, storage driver, running count) lives in
//! the existing `docker` collector — this one is per-container detail.
//! Returns `[]` when docker is missing or the daemon is unreachable.
//!
//! Linux + macOS only. The collector dispatcher's 5-second timeout
//! protects against a hung daemon; per-call timeouts aren't layered on
//! top to keep the path simple.

use anyhow::Result;
use serde_json::{json, Value};

#[cfg(any(target_os = "linux", target_os = "macos"))]
use std::process::Command;

pub fn collect() -> Result<Value> {
    #[cfg(any(target_os = "linux", target_os = "macos"))]
    {
        Ok(collect_docker())
    }

    #[cfg(not(any(target_os = "linux", target_os = "macos")))]
    {
        Ok(Value::Null)
    }
}

#[cfg(any(target_os = "linux", target_os = "macos"))]
fn collect_docker() -> Value {
    let ids = run_docker(&["ps", "-a", "-q", "--no-trunc"]);
    let ids: Vec<&str> = ids.lines().filter(|l| !l.is_empty()).collect();
    if ids.is_empty() {
        return json!([]);
    }

    // One docker inspect call for all containers. Tab-separated fields are
    // easier to parse than the JSON output and don't pull in any extra
    // serde dependencies on the trait side.
    let mut args: Vec<&str> = vec![
        "inspect",
        "--format",
        "{{.Name}}\t{{.Config.Image}}\t{{.Image}}\t{{.State.Status}}\t{{.State.Health.Status}}\t{{.RestartCount}}\t{{.State.OOMKilled}}",
    ];
    args.extend(ids.iter().copied());
    let raw = run_docker(&args);
    json!(parse_inspect_output(&raw))
}

/// Parse the tab-separated inspect output produced by `collect_docker`.
/// Extracted into a free function so we can unit-test the parsing without
/// touching the Docker daemon.
fn parse_inspect_output(raw: &str) -> Vec<Value> {
    let mut out = Vec::new();
    for line in raw.lines() {
        if line.is_empty() {
            continue;
        }
        let parts: Vec<&str> = line.split('\t').collect();
        if parts.len() < 7 {
            continue;
        }
        // Docker prefixes container names with `/` — strip it so the trait
        // value matches what operators wrote in their module.
        let name = parts[0].trim_start_matches('/');
        // Docker emits `<no value>` for healthcheck fields when the
        // container has no healthcheck configured.
        let health = if parts[4] == "<no value>" || parts[4] == "<nil>" {
            ""
        } else {
            parts[4]
        };
        out.push(json!({
            "name": name,
            "image": parts[1],
            "image_id": parts[2],
            "state": parts[3],
            "health": health,
            "restart_count": parts[5].parse::<u64>().unwrap_or(0),
            "oom_killed": parts[6] == "true",
        }));
    }
    out
}

#[cfg(any(target_os = "linux", target_os = "macos"))]
fn run_docker(args: &[&str]) -> String {
    Command::new("docker")
        .args(args)
        .output()
        .ok()
        .filter(|o| o.status.success())
        .map(|o| String::from_utf8_lossy(&o.stdout).to_string())
        .unwrap_or_default()
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn parse_two_containers_one_with_health() {
        let raw = "/redis-cache\tredis:7.2-alpine\tsha256:abc123\trunning\thealthy\t0\tfalse\n\
                   /old-job\tbusybox:latest\tsha256:def456\texited\t<no value>\t3\ttrue\n";
        let parsed = parse_inspect_output(raw);
        assert_eq!(parsed.len(), 2);

        assert_eq!(parsed[0]["name"], "redis-cache");
        assert_eq!(parsed[0]["image"], "redis:7.2-alpine");
        assert_eq!(parsed[0]["image_id"], "sha256:abc123");
        assert_eq!(parsed[0]["state"], "running");
        assert_eq!(parsed[0]["health"], "healthy");
        assert_eq!(parsed[0]["restart_count"], 0);
        assert_eq!(parsed[0]["oom_killed"], false);

        assert_eq!(parsed[1]["name"], "old-job");
        assert_eq!(parsed[1]["state"], "exited");
        // <no value> normalized to empty string.
        assert_eq!(parsed[1]["health"], "");
        assert_eq!(parsed[1]["restart_count"], 3);
        assert_eq!(parsed[1]["oom_killed"], true);
    }

    #[test]
    fn parse_skips_short_rows_and_empty_lines() {
        let raw = "\n\
                   /good\tnginx:latest\tsha256:111\trunning\t<no value>\t0\tfalse\n\
                   /bad\tnginx:latest\tsha256:222\trunning\n\
                   \n";
        let parsed = parse_inspect_output(raw);
        assert_eq!(parsed.len(), 1);
        assert_eq!(parsed[0]["name"], "good");
    }

    #[test]
    fn parse_empty_returns_empty() {
        assert_eq!(parse_inspect_output("").len(), 0);
        assert_eq!(parse_inspect_output("\n\n").len(), 0);
    }
}
