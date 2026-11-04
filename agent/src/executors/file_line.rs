// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: Apache-2.0

//! FileLine executor — manages individual lines in files idempotently.
//!
//! Supports `before:regex` and `after:regex` positioning, optional
//! `region_start`/`region_end` to constrain operations, and atomic writes.

use super::atomic;
use super::region;
use super::{Action, Executor, ResourceResult};
use regex_lite::Regex;
use std::collections::HashMap;
use std::time::Instant;

pub struct FileLineExecutor;

impl Executor for FileLineExecutor {
    fn execute(&self, params: &HashMap<String, String>, dry_run: bool) -> ResourceResult {
        let start = Instant::now();
        let path = params.get("target_path").cloned().unwrap_or_default();
        let mut result = ResourceResult::new("file_line", &path);

        if path.is_empty() {
            result.error = Some("file_line executor: target_path param is required".into());
            result.duration_ms = start.elapsed().as_millis() as i32;
            return result;
        }

        let state = params.get("state").map(|s| s.as_str()).unwrap_or("present");
        let line = params.get("line").cloned().unwrap_or_default();
        let match_pattern = params.get("match_pattern").cloned().unwrap_or_default();
        let position = params.get("position").map(|s| s.as_str()).unwrap_or("end");
        let region_start = params.get("region_start").map(|s| s.as_str());
        let region_end = params.get("region_end").map(|s| s.as_str());

        let content = match super::read_for_edit(&path) {
            Ok(c) => c,
            Err(e) => {
                result.error = Some(e);
                result.duration_ms = start.elapsed().as_millis() as i32;
                return result;
            }
        };
        let mut lines: Vec<String> = content.lines().map(|l| l.to_string()).collect();

        // Compute region bounds if specified.
        let has_region = region_start.is_some() || region_end.is_some();
        let (region_lo, region_hi) = if has_region && !lines.is_empty() {
            match region::compute_region(&lines, region_start, region_end) {
                Ok(r) => r,
                Err(e) => {
                    result.error = Some(e);
                    result.duration_ms = start.elapsed().as_millis() as i32;
                    return result;
                }
            }
        } else {
            (0, if lines.is_empty() { 0 } else { lines.len() - 1 })
        };

        match state {
            "present" => {
                if line.is_empty() {
                    result.error = Some("file_line executor: line param is required for state=present".into());
                    result.duration_ms = start.elapsed().as_millis() as i32;
                    return result;
                }

                if !match_pattern.is_empty() {
                    // Replace matching lines (within region if specified).
                    let re = match Regex::new(&match_pattern) {
                        Ok(r) => r,
                        Err(e) => {
                            result.error = Some(format!("invalid regex {:?}: {}", match_pattern, e));
                            result.duration_ms = start.elapsed().as_millis() as i32;
                            return result;
                        }
                    };

                    let mut replaced = false;
                    let scan_range = if has_region {
                        region_lo..=region_hi
                    } else {
                        0..=if lines.is_empty() { 0 } else { lines.len() - 1 }
                    };

                    if !lines.is_empty() {
                        for i in scan_range {
                            if re.is_match(&lines[i]) {
                                if lines[i] != line {
                                    lines[i] = line.clone();
                                    replaced = true;
                                }
                                if !replaced {
                                    result.action = Action::None;
                                    result.duration_ms = start.elapsed().as_millis() as i32;
                                    return result;
                                }
                                break;
                            }
                        }
                    }

                    if replaced {
                        result.action = Action::Updated;
                        result.changed = true;
                    } else {
                        // No line matches the pattern. If the exact line is
                        // already there (a `line:` the pattern doesn't match —
                        // e.g. pattern `^Foo=1`, line `Foo=2`), it was inserted
                        // by an earlier cycle: settled, not a fresh insert.
                        // Without this, every cycle appended another copy.
                        let exists = if has_region && !lines.is_empty() {
                            lines[region_lo..=region_hi].contains(&line)
                        } else {
                            lines.contains(&line)
                        };
                        if exists {
                            result.action = Action::None;
                            result.duration_ms = start.elapsed().as_millis() as i32;
                            return result;
                        }
                        // Insert at position.
                        result.action = Action::Created;
                        result.changed = true;
                        insert_line(&mut lines, &line, position);
                    }
                } else {
                    // Exact line check (within region if specified).
                    let exists = if has_region && !lines.is_empty() {
                        lines[region_lo..=region_hi].contains(&line)
                    } else {
                        lines.contains(&line)
                    };

                    if exists {
                        result.action = Action::None;
                        result.duration_ms = start.elapsed().as_millis() as i32;
                        return result;
                    }

                    result.action = Action::Created;
                    result.changed = true;
                    insert_line(&mut lines, &line, position);
                }

                if !dry_run && result.changed {
                    let mut output = lines.join("\n");
                    if content.ends_with('\n') || output.is_empty() {
                        output.push('\n');
                    }
                    if let Err(e) = atomic::atomic_write(&path, &output) {
                        result.error = Some(e);
                    }
                }
            }
            "absent" => {
                let original_count = lines.len();

                if has_region && !lines.is_empty() {
                    // Only remove within region.
                    if !match_pattern.is_empty() {
                        let re = match Regex::new(&match_pattern) {
                            Ok(r) => r,
                            Err(e) => {
                                result.error = Some(format!("invalid regex {:?}: {}", match_pattern, e));
                                result.duration_ms = start.elapsed().as_millis() as i32;
                                return result;
                            }
                        };
                        let mut to_remove = Vec::new();
                        for (i, l) in lines
                            .iter()
                            .enumerate()
                            .take(region_hi + 1)
                            .skip(region_lo)
                        {
                            if re.is_match(l) {
                                to_remove.push(i);
                            }
                        }
                        for (offset, i) in to_remove.iter().enumerate() {
                            lines.remove(i - offset);
                        }
                    } else if !line.is_empty() {
                        let mut to_remove = Vec::new();
                        for (i, l) in lines
                            .iter()
                            .enumerate()
                            .take(region_hi + 1)
                            .skip(region_lo)
                        {
                            if *l == line {
                                to_remove.push(i);
                            }
                        }
                        for (offset, i) in to_remove.iter().enumerate() {
                            lines.remove(i - offset);
                        }
                    }
                } else {
                    if !match_pattern.is_empty() {
                        let re = match Regex::new(&match_pattern) {
                            Ok(r) => r,
                            Err(e) => {
                                result.error = Some(format!("invalid regex {:?}: {}", match_pattern, e));
                                result.duration_ms = start.elapsed().as_millis() as i32;
                                return result;
                            }
                        };
                        lines.retain(|l| !re.is_match(l));
                    } else if !line.is_empty() {
                        lines.retain(|l| *l != line);
                    }
                }

                if lines.len() == original_count {
                    result.action = Action::None;
                } else {
                    result.action = Action::Deleted;
                    result.changed = true;

                    if !dry_run {
                        let mut output = lines.join("\n");
                        if content.ends_with('\n') && !output.is_empty() {
                            output.push('\n');
                        }
                        if let Err(e) = atomic::atomic_write(&path, &output) {
                            result.error = Some(e);
                        }
                    }
                }
            }
            _ => {
                result.error = Some(format!("file_line executor: unknown state {:?}", state));
            }
        }

        result.duration_ms = start.elapsed().as_millis() as i32;
        result
    }
}

fn insert_line(lines: &mut Vec<String>, line: &str, position: &str) {
    if position == "begin" {
        lines.insert(0, line.to_string());
    } else if let Some(pattern) = position.strip_prefix("after:") {
        if let Ok(re) = Regex::new(pattern) {
            let idx = lines.iter().rposition(|l| re.is_match(l));
            match idx {
                Some(i) => lines.insert(i + 1, line.to_string()),
                None => lines.push(line.to_string()),
            }
        } else {
            lines.push(line.to_string());
        }
    } else if let Some(pattern) = position.strip_prefix("before:") {
        if let Ok(re) = Regex::new(pattern) {
            match lines.iter().position(|l| re.is_match(l)) {
                Some(i) => lines.insert(i, line.to_string()),
                None => lines.push(line.to_string()),
            }
        } else {
            lines.push(line.to_string());
        }
    } else {
        // "end" or default.
        lines.push(line.to_string());
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    fn make_params(pairs: &[(&str, &str)]) -> HashMap<String, String> {
        pairs.iter().map(|(k, v)| (k.to_string(), v.to_string())).collect()
    }

    #[test]
    fn missing_path_errors() {
        let exec = FileLineExecutor;
        let params = make_params(&[("line", "some line")]);
        let result = exec.execute(&params, false);
        assert!(result.error.is_some());
        assert!(result.error.unwrap().contains("target_path param is required"));
    }

    #[test]
    fn present_adds_line_at_end() {
        let dir = tempfile::tempdir().unwrap();
        let path = dir.path().join("test.conf");
        std::fs::write(&path, "first line\nsecond line\n").unwrap();

        let exec = FileLineExecutor;
        let params = make_params(&[
            ("target_path", path.to_str().unwrap()),
            ("line", "third line"),
            ("state", "present"),
        ]);
        let result = exec.execute(&params, false);
        assert!(result.error.is_none(), "unexpected error: {:?}", result.error);
        assert!(result.changed);
        assert_eq!(result.action, Action::Created);

        let content = std::fs::read_to_string(&path).unwrap();
        assert!(content.contains("third line"));
        let lines: Vec<&str> = content.lines().collect();
        assert_eq!(lines.last().unwrap(), &"third line");
    }

    #[test]
    fn present_line_already_exists() {
        let dir = tempfile::tempdir().unwrap();
        let path = dir.path().join("test.conf");
        std::fs::write(&path, "first line\nsecond line\n").unwrap();

        let exec = FileLineExecutor;
        let params = make_params(&[
            ("target_path", path.to_str().unwrap()),
            ("line", "second line"),
            ("state", "present"),
        ]);
        let result = exec.execute(&params, false);
        assert!(result.error.is_none(), "unexpected error: {:?}", result.error);
        assert!(!result.changed);
        assert_eq!(result.action, Action::None);
    }

    #[test]
    fn present_replaces_matching_regex() {
        let dir = tempfile::tempdir().unwrap();
        let path = dir.path().join("test.conf");
        std::fs::write(&path, "host=localhost\nport=80\n").unwrap();

        let exec = FileLineExecutor;
        let params = make_params(&[
            ("target_path", path.to_str().unwrap()),
            ("line", "port=443"),
            ("match_pattern", "^port="),
            ("state", "present"),
        ]);
        let result = exec.execute(&params, false);
        assert!(result.error.is_none(), "unexpected error: {:?}", result.error);
        assert!(result.changed);
        assert_eq!(result.action, Action::Updated);

        let content = std::fs::read_to_string(&path).unwrap();
        assert!(content.contains("port=443"));
        assert!(!content.contains("port=80"));
        assert!(content.contains("host=localhost"));
    }

    #[test]
    fn absent_removes_exact_line() {
        let dir = tempfile::tempdir().unwrap();
        let path = dir.path().join("test.conf");
        std::fs::write(&path, "keep this\nremove this\nkeep too\n").unwrap();

        let exec = FileLineExecutor;
        let params = make_params(&[
            ("target_path", path.to_str().unwrap()),
            ("line", "remove this"),
            ("state", "absent"),
        ]);
        let result = exec.execute(&params, false);
        assert!(result.error.is_none(), "unexpected error: {:?}", result.error);
        assert!(result.changed);
        assert_eq!(result.action, Action::Deleted);

        let content = std::fs::read_to_string(&path).unwrap();
        assert!(!content.contains("remove this"));
        assert!(content.contains("keep this"));
        assert!(content.contains("keep too"));
    }

    #[test]
    fn absent_removes_by_regex() {
        let dir = tempfile::tempdir().unwrap();
        let path = dir.path().join("test.conf");
        std::fs::write(&path, "port=80\nhost=localhost\nport=8080\n").unwrap();

        let exec = FileLineExecutor;
        let params = make_params(&[
            ("target_path", path.to_str().unwrap()),
            ("match_pattern", "^port="),
            ("state", "absent"),
        ]);
        let result = exec.execute(&params, false);
        assert!(result.error.is_none(), "unexpected error: {:?}", result.error);
        assert!(result.changed);
        assert_eq!(result.action, Action::Deleted);

        let content = std::fs::read_to_string(&path).unwrap();
        assert!(!content.contains("port="));
        assert!(content.contains("host=localhost"));
    }

    #[test]
    fn absent_line_not_found() {
        let dir = tempfile::tempdir().unwrap();
        let path = dir.path().join("test.conf");
        std::fs::write(&path, "first line\nsecond line\n").unwrap();

        let exec = FileLineExecutor;
        let params = make_params(&[
            ("target_path", path.to_str().unwrap()),
            ("line", "nonexistent line"),
            ("state", "absent"),
        ]);
        let result = exec.execute(&params, false);
        assert!(result.error.is_none(), "unexpected error: {:?}", result.error);
        assert!(!result.changed);
        assert_eq!(result.action, Action::None);
    }

    #[test]
    fn dry_run_does_not_modify() {
        let dir = tempfile::tempdir().unwrap();
        let path = dir.path().join("test.conf");
        let original = "existing line\n";
        std::fs::write(&path, original).unwrap();

        let exec = FileLineExecutor;
        let params = make_params(&[
            ("target_path", path.to_str().unwrap()),
            ("line", "new line"),
            ("state", "present"),
        ]);
        let result = exec.execute(&params, true);
        assert!(result.error.is_none(), "unexpected error: {:?}", result.error);
        assert!(result.changed);

        let content = std::fs::read_to_string(&path).unwrap();
        assert_eq!(content, original);
    }

    // ── before:regex tests ────────────────────────────────────────────

    #[test]
    fn before_regex_inserts_before_match() {
        let dir = tempfile::tempdir().unwrap();
        let path = dir.path().join("test.conf");
        std::fs::write(&path, "first\nsecond\nthird\n").unwrap();

        let exec = FileLineExecutor;
        let params = make_params(&[
            ("target_path", path.to_str().unwrap()),
            ("line", "inserted"),
            ("position", "before:^second"),
            ("state", "present"),
        ]);
        let result = exec.execute(&params, false);
        assert!(result.error.is_none(), "{:?}", result.error);
        assert!(result.changed);

        let content = std::fs::read_to_string(&path).unwrap();
        let lines: Vec<&str> = content.lines().collect();
        assert_eq!(lines, vec!["first", "inserted", "second", "third"]);
    }

    #[test]
    fn before_no_match_appends() {
        let dir = tempfile::tempdir().unwrap();
        let path = dir.path().join("test.conf");
        std::fs::write(&path, "first\nsecond\n").unwrap();

        let exec = FileLineExecutor;
        let params = make_params(&[
            ("target_path", path.to_str().unwrap()),
            ("line", "appended"),
            ("position", "before:^MISSING"),
            ("state", "present"),
        ]);
        let result = exec.execute(&params, false);
        assert!(result.error.is_none(), "{:?}", result.error);
        assert!(result.changed);

        let content = std::fs::read_to_string(&path).unwrap();
        let lines: Vec<&str> = content.lines().collect();
        assert_eq!(lines.last().unwrap(), &"appended");
    }

    // ── region tests ──────────────────────────────────────────────────

    #[test]
    fn region_present_match_only_scans_region() {
        let dir = tempfile::tempdir().unwrap();
        let path = dir.path().join("test.conf");
        std::fs::write(
            &path,
            "port=80\n# START\nport=80\n# END\nport=80\n",
        ).unwrap();

        let exec = FileLineExecutor;
        let params = make_params(&[
            ("target_path", path.to_str().unwrap()),
            ("line", "port=443"),
            ("match_pattern", "^port="),
            ("state", "present"),
            ("region_start", "^# START"),
            ("region_end", "^# END"),
        ]);
        let result = exec.execute(&params, false);
        assert!(result.error.is_none(), "{:?}", result.error);
        assert!(result.changed);

        let content = std::fs::read_to_string(&path).unwrap();
        let lines: Vec<&str> = content.lines().collect();
        // Before region: unchanged
        assert_eq!(lines[0], "port=80");
        // Inside region: changed
        assert_eq!(lines[2], "port=443");
        // After region: unchanged
        assert_eq!(lines[4], "port=80");
    }

    #[test]
    fn region_present_exact_only_checks_region() {
        let dir = tempfile::tempdir().unwrap();
        let path = dir.path().join("test.conf");
        // Line exists outside region but not inside — should still insert.
        std::fs::write(
            &path,
            "target-line\n# START\nother\n# END\n",
        ).unwrap();

        let exec = FileLineExecutor;
        let params = make_params(&[
            ("target_path", path.to_str().unwrap()),
            ("line", "target-line"),
            ("state", "present"),
            ("region_start", "^# START"),
            ("region_end", "^# END"),
        ]);
        let result = exec.execute(&params, false);
        assert!(result.error.is_none(), "{:?}", result.error);
        // Line exists outside region, but not within — should add it.
        assert!(result.changed);
    }

    #[test]
    fn region_absent_only_removes_in_region() {
        let dir = tempfile::tempdir().unwrap();
        let path = dir.path().join("test.conf");
        std::fs::write(
            &path,
            "port=80\n# START\nport=80\n# END\nport=80\n",
        ).unwrap();

        let exec = FileLineExecutor;
        let params = make_params(&[
            ("target_path", path.to_str().unwrap()),
            ("match_pattern", "^port="),
            ("state", "absent"),
            ("region_start", "^# START"),
            ("region_end", "^# END"),
        ]);
        let result = exec.execute(&params, false);
        assert!(result.error.is_none(), "{:?}", result.error);
        assert!(result.changed);
        assert_eq!(result.action, Action::Deleted);

        let content = std::fs::read_to_string(&path).unwrap();
        let lines: Vec<&str> = content.lines().collect();
        // port=80 outside region should survive.
        assert_eq!(lines[0], "port=80");
        assert_eq!(lines[1], "# START");
        assert_eq!(lines[2], "# END");
        assert_eq!(lines[3], "port=80");
    }

    #[test]
    fn region_start_only() {
        let dir = tempfile::tempdir().unwrap();
        let path = dir.path().join("test.conf");
        std::fs::write(&path, "before\n# START\nport=80\nport=8080\n").unwrap();

        let exec = FileLineExecutor;
        let params = make_params(&[
            ("target_path", path.to_str().unwrap()),
            ("match_pattern", "^port="),
            ("state", "absent"),
            ("region_start", "^# START"),
        ]);
        let result = exec.execute(&params, false);
        assert!(result.error.is_none(), "{:?}", result.error);
        assert!(result.changed);

        let content = std::fs::read_to_string(&path).unwrap();
        let lines: Vec<&str> = content.lines().collect();
        assert_eq!(lines, vec!["before", "# START"]);
    }

    #[test]
    fn region_end_only() {
        let dir = tempfile::tempdir().unwrap();
        let path = dir.path().join("test.conf");
        std::fs::write(&path, "port=80\nport=8080\n# END\nport=9090\n").unwrap();

        let exec = FileLineExecutor;
        let params = make_params(&[
            ("target_path", path.to_str().unwrap()),
            ("match_pattern", "^port="),
            ("state", "absent"),
            ("region_end", "^# END"),
        ]);
        let result = exec.execute(&params, false);
        assert!(result.error.is_none(), "{:?}", result.error);
        assert!(result.changed);

        let content = std::fs::read_to_string(&path).unwrap();
        let lines: Vec<&str> = content.lines().collect();
        assert_eq!(lines, vec!["# END", "port=9090"]);
    }

    /// An unreadable / non-UTF-8 file is an error, never "empty" (which
    /// rewrote it holding only this one line).
    #[test]
    fn unreadable_file_fails_and_is_left_intact() {
        let dir = tempfile::tempdir().unwrap();
        let path = dir.path().join("bin.conf");
        let original: &[u8] = b"keep=\xff\xfe\n";
        std::fs::write(&path, original).unwrap();
        let params = make_params(&[("target_path", path.to_str().unwrap()), ("line", "x=1"), ("state", "present")]);
        let r = FileLineExecutor.execute(&params, false);
        assert!(r.error.is_some(), "non-UTF-8 file must fail the resource");
        assert_eq!(std::fs::read(&path).unwrap(), original);
    }

    /// match_pattern that the managed line itself doesn't match: the first
    /// cycle inserts it, later cycles must see it and settle (they appended a
    /// fresh copy every cycle).
    #[test]
    fn insert_when_pattern_misses_is_idempotent() {
        let dir = tempfile::tempdir().unwrap();
        let path = dir.path().join("t.conf");
        std::fs::write(&path, "a=1\n").unwrap();
        let params = make_params(&[
            ("target_path", path.to_str().unwrap()),
            ("line", "Foo=2"),
            ("match_pattern", "^Foo=1$"),
            ("state", "present"),
        ]);
        assert!(FileLineExecutor.execute(&params, false).changed);
        let r2 = FileLineExecutor.execute(&params, false);
        assert!(r2.error.is_none());
        assert!(!r2.changed, "second cycle re-inserted the line");
        assert_eq!(std::fs::read_to_string(&path).unwrap().matches("Foo=2").count(), 1);
    }
}
