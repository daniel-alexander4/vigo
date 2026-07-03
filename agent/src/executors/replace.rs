// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

//! Replace executor — in-line regex substitution with backreferences.
//!
//! Supports `$1`-style capture group backreferences, `first`/`all` occurrence
//! modes, and optional region constraints via `region_start`/`region_end`.

use super::atomic;
use super::region;
use super::{Action, Executor, ResourceResult};
use regex_lite::Regex;
use std::collections::HashMap;
use std::time::Instant;

pub struct ReplaceExecutor;

impl Executor for ReplaceExecutor {
    fn execute(&self, params: &HashMap<String, String>, dry_run: bool) -> ResourceResult {
        let start = Instant::now();
        let path = params.get("target_path").cloned().unwrap_or_default();
        let mut result = ResourceResult::new("replace", &path);

        if path.is_empty() {
            result.error = Some("replace executor: target_path param is required".into());
            result.duration_ms = start.elapsed().as_millis() as i32;
            return result;
        }

        let match_pat = params.get("match_pattern").cloned().unwrap_or_default();
        if match_pat.is_empty() {
            result.error = Some("replace executor: match_pattern param is required".into());
            result.duration_ms = start.elapsed().as_millis() as i32;
            return result;
        }

        let replace_str = match params.get("replacement") {
            Some(s) => s.clone(),
            None => {
                result.error = Some("replace executor: replacement param is required".into());
                result.duration_ms = start.elapsed().as_millis() as i32;
                return result;
            }
        };

        let re = match Regex::new(&match_pat) {
            Ok(r) => r,
            Err(e) => {
                result.error = Some(format!("invalid regex {:?}: {}", match_pat, e));
                result.duration_ms = start.elapsed().as_millis() as i32;
                return result;
            }
        };

        let occurrences = params.get("occurrences").map(|s| s.as_str()).unwrap_or("all");
        let region_start = params.get("region_start").map(|s| s.as_str());
        let region_end = params.get("region_end").map(|s| s.as_str());

        let content = match std::fs::read_to_string(&path) {
            Ok(c) => c,
            Err(e) if e.kind() == std::io::ErrorKind::NotFound => {
                // Nothing to replace in a file that doesn't exist.
                result.action = Action::None;
                result.duration_ms = start.elapsed().as_millis() as i32;
                return result;
            }
            Err(e) => {
                // A real-but-unreadable target (permission denied, non-UTF-8
                // binary, I/O error) must NOT silently report "settled".
                result.error = Some(format!("replace executor: reading {}: {}", path, e));
                result.duration_ms = start.elapsed().as_millis() as i32;
                return result;
            }
        };
        let mut lines: Vec<String> = content.lines().map(|l| l.to_string()).collect();

        if lines.is_empty() {
            result.action = Action::None;
            result.duration_ms = start.elapsed().as_millis() as i32;
            return result;
        }

        // Compute region bounds.
        let (region_lo, region_hi) = if region_start.is_some() || region_end.is_some() {
            match region::compute_region(&lines, region_start, region_end) {
                Ok(r) => r,
                Err(e) => {
                    result.error = Some(e);
                    result.duration_ms = start.elapsed().as_millis() as i32;
                    return result;
                }
            }
        } else {
            (0, lines.len() - 1)
        };

        let mut changed = false;
        let mut first_done = false;

        for line_ref in lines.iter_mut().take(region_hi + 1).skip(region_lo) {
            if first_done && occurrences == "first" {
                break;
            }

            if !re.is_match(line_ref) {
                continue;
            }

            let replaced = if occurrences == "first" && !first_done {
                first_done = true;
                // Replace first occurrence within this line.
                re.replace(line_ref, replace_str.as_str()).to_string()
            } else {
                re.replace_all(line_ref, replace_str.as_str()).to_string()
            };

            if replaced != *line_ref {
                *line_ref = replaced;
                changed = true;
            }

            if occurrences == "first" {
                break;
            }
        }

        if !changed {
            result.action = Action::None;
            result.duration_ms = start.elapsed().as_millis() as i32;
            return result;
        }

        result.action = Action::Updated;
        result.changed = true;

        if !dry_run {
            let mut output = lines.join("\n");
            if content.ends_with('\n') || output.is_empty() {
                output.push('\n');
            }
            if let Err(e) = atomic::atomic_write(&path, &output) {
                result.error = Some(e);
            }
        }

        result.duration_ms = start.elapsed().as_millis() as i32;
        result
    }
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
    fn missing_path_errors() {
        let exec = ReplaceExecutor;
        let params = make_params(&[("match_pattern", "foo"), ("replacement", "bar")]);
        let result = exec.execute(&params, false);
        assert!(result.error.unwrap().contains("target_path param is required"));
    }

    #[test]
    fn missing_match_errors() {
        let exec = ReplaceExecutor;
        let params = make_params(&[("target_path", "/tmp/x"), ("replacement", "bar")]);
        let result = exec.execute(&params, false);
        assert!(result.error.unwrap().contains("match_pattern param is required"));
    }

    #[test]
    fn missing_file_is_noop() {
        // A target that doesn't exist has nothing to replace — no-op, no error.
        let dir = tempfile::tempdir().unwrap();
        let path = dir.path().join("nope");
        let params = make_params(&[
            ("target_path", path.to_str().unwrap()),
            ("match_pattern", "a"),
            ("replacement", "b"),
        ]);
        let result = ReplaceExecutor.execute(&params, false);
        assert_eq!(result.action, Action::None);
        assert!(result.error.is_none());
    }

    #[test]
    fn unreadable_binary_file_errors() {
        // A non-UTF-8 file must surface an error, not silently report settled
        // (the old unwrap_or_default() mapped it to an empty no-op).
        let dir = tempfile::tempdir().unwrap();
        let path = dir.path().join("bin");
        std::fs::write(&path, [0xff, 0xfe, 0x00, 0x01]).unwrap();
        let params = make_params(&[
            ("target_path", path.to_str().unwrap()),
            ("match_pattern", "a"),
            ("replacement", "b"),
        ]);
        let result = ReplaceExecutor.execute(&params, false);
        assert!(
            result.error.is_some(),
            "expected an error for an unreadable file, got action {:?}",
            result.action
        );
    }

    #[test]
    fn missing_replace_errors() {
        let exec = ReplaceExecutor;
        let params = make_params(&[("target_path", "/tmp/x"), ("match_pattern", "foo")]);
        let result = exec.execute(&params, false);
        assert!(result.error.unwrap().contains("replacement param is required"));
    }

    #[test]
    fn simple_replace_all() {
        let dir = tempfile::tempdir().unwrap();
        let path = dir.path().join("test.conf");
        std::fs::write(&path, "port=80\nhost=localhost\nport=8080\n").unwrap();

        let exec = ReplaceExecutor;
        let params = make_params(&[
            ("target_path", path.to_str().unwrap()),
            ("match_pattern", "port=\\d+"),
            ("replacement", "port=443"),
        ]);
        let result = exec.execute(&params, false);
        assert!(result.error.is_none(), "{:?}", result.error);
        assert!(result.changed);

        let content = std::fs::read_to_string(&path).unwrap();
        assert_eq!(content, "port=443\nhost=localhost\nport=443\n");
    }

    #[test]
    fn simple_replace_first() {
        let dir = tempfile::tempdir().unwrap();
        let path = dir.path().join("test.conf");
        std::fs::write(&path, "port=80\nhost=localhost\nport=8080\n").unwrap();

        let exec = ReplaceExecutor;
        let params = make_params(&[
            ("target_path", path.to_str().unwrap()),
            ("match_pattern", "port=\\d+"),
            ("replacement", "port=443"),
            ("occurrences", "first"),
        ]);
        let result = exec.execute(&params, false);
        assert!(result.error.is_none(), "{:?}", result.error);
        assert!(result.changed);

        let content = std::fs::read_to_string(&path).unwrap();
        assert_eq!(content, "port=443\nhost=localhost\nport=8080\n");
    }

    #[test]
    fn backreferences() {
        let dir = tempfile::tempdir().unwrap();
        let path = dir.path().join("sshd_config");
        std::fs::write(&path, "PermitRootLogin yes\nMaxAuthTries 6\n").unwrap();

        let exec = ReplaceExecutor;
        let params = make_params(&[
            ("target_path", path.to_str().unwrap()),
            ("match_pattern", "^(PermitRootLogin)\\s+yes"),
            ("replacement", "$1 no"),
        ]);
        let result = exec.execute(&params, false);
        assert!(result.error.is_none(), "{:?}", result.error);
        assert!(result.changed);

        let content = std::fs::read_to_string(&path).unwrap();
        assert!(content.contains("PermitRootLogin no"));
        assert!(content.contains("MaxAuthTries 6"));
    }

    #[test]
    fn no_match_no_change() {
        let dir = tempfile::tempdir().unwrap();
        let path = dir.path().join("test.conf");
        std::fs::write(&path, "host=localhost\n").unwrap();

        let exec = ReplaceExecutor;
        let params = make_params(&[
            ("target_path", path.to_str().unwrap()),
            ("match_pattern", "^port="),
            ("replacement", "port=443"),
        ]);
        let result = exec.execute(&params, false);
        assert!(result.error.is_none(), "{:?}", result.error);
        assert!(!result.changed);
        assert_eq!(result.action, Action::None);
    }

    #[test]
    fn region_constrained() {
        let dir = tempfile::tempdir().unwrap();
        let path = dir.path().join("sshd_config");
        std::fs::write(
            &path,
            "# Global\nPermitRootLogin yes\n# Authentication\nPermitRootLogin yes\n# Logging\nPermitRootLogin yes\n",
        )
        .unwrap();

        let exec = ReplaceExecutor;
        let params = make_params(&[
            ("target_path", path.to_str().unwrap()),
            ("match_pattern", "PermitRootLogin yes"),
            ("replacement", "PermitRootLogin no"),
            ("region_start", "^# Authentication"),
            ("region_end", "^# Logging"),
        ]);
        let result = exec.execute(&params, false);
        assert!(result.error.is_none(), "{:?}", result.error);
        assert!(result.changed);

        let _lines: Vec<&str> = std::fs::read_to_string(&path)
            .unwrap()
            .lines()
            .collect::<Vec<_>>()
            .into_iter()
            .collect();
        // Read the file fresh for assertions.
        let content = std::fs::read_to_string(&path).unwrap();
        let lines: Vec<&str> = content.lines().collect();
        // Line 1 (before region): unchanged
        assert_eq!(lines[1], "PermitRootLogin yes");
        // Line 3 (inside region): changed
        assert_eq!(lines[3], "PermitRootLogin no");
        // Line 5 (after region): unchanged
        assert_eq!(lines[5], "PermitRootLogin yes");
    }

    #[test]
    fn region_start_only() {
        let dir = tempfile::tempdir().unwrap();
        let path = dir.path().join("test.conf");
        std::fs::write(&path, "before\n# START\nfoo=1\nfoo=2\n").unwrap();

        let exec = ReplaceExecutor;
        let params = make_params(&[
            ("target_path", path.to_str().unwrap()),
            ("match_pattern", "foo="),
            ("replacement", "bar="),
            ("region_start", "^# START"),
        ]);
        let result = exec.execute(&params, false);
        assert!(result.error.is_none(), "{:?}", result.error);
        assert!(result.changed);

        let content = std::fs::read_to_string(&path).unwrap();
        assert_eq!(content, "before\n# START\nbar=1\nbar=2\n");
    }

    #[test]
    fn region_end_only() {
        let dir = tempfile::tempdir().unwrap();
        let path = dir.path().join("test.conf");
        std::fs::write(&path, "foo=1\nfoo=2\n# END\nfoo=3\n").unwrap();

        let exec = ReplaceExecutor;
        let params = make_params(&[
            ("target_path", path.to_str().unwrap()),
            ("match_pattern", "foo="),
            ("replacement", "bar="),
            ("region_end", "^# END"),
        ]);
        let result = exec.execute(&params, false);
        assert!(result.error.is_none(), "{:?}", result.error);
        assert!(result.changed);

        let content = std::fs::read_to_string(&path).unwrap();
        assert_eq!(content, "bar=1\nbar=2\n# END\nfoo=3\n");
    }

    #[test]
    fn dry_run_does_not_modify() {
        let dir = tempfile::tempdir().unwrap();
        let path = dir.path().join("test.conf");
        let original = "port=80\n";
        std::fs::write(&path, original).unwrap();

        let exec = ReplaceExecutor;
        let params = make_params(&[
            ("target_path", path.to_str().unwrap()),
            ("match_pattern", "port=80"),
            ("replacement", "port=443"),
        ]);
        let result = exec.execute(&params, true);
        assert!(result.error.is_none(), "{:?}", result.error);
        assert!(result.changed);

        let content = std::fs::read_to_string(&path).unwrap();
        assert_eq!(content, original);
    }

    #[test]
    fn empty_file_no_change() {
        let dir = tempfile::tempdir().unwrap();
        let path = dir.path().join("empty.conf");
        std::fs::write(&path, "").unwrap();

        let exec = ReplaceExecutor;
        let params = make_params(&[
            ("target_path", path.to_str().unwrap()),
            ("match_pattern", "foo"),
            ("replacement", "bar"),
        ]);
        let result = exec.execute(&params, false);
        assert!(result.error.is_none(), "{:?}", result.error);
        assert!(!result.changed);
    }
}
