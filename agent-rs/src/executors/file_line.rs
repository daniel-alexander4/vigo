// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: BUSL-1.1

//! FileLine executor — manages individual lines in files idempotently.

use super::{Action, Executor, ResourceResult};
use regex_lite::Regex;
use std::collections::HashMap;
use std::time::Instant;

pub struct FileLineExecutor;

impl Executor for FileLineExecutor {
    fn execute(&self, params: &HashMap<String, String>, dry_run: bool) -> ResourceResult {
        let start = Instant::now();
        let path = params.get("path").cloned().unwrap_or_default();
        let mut result = ResourceResult::new("file_line", &path);

        if path.is_empty() {
            result.error = Some("file_line executor: path param is required".into());
            result.duration_ms = start.elapsed().as_millis() as i32;
            return result;
        }

        let state = params.get("state").map(|s| s.as_str()).unwrap_or("present");
        let line = params.get("line").cloned().unwrap_or_default();
        let match_pattern = params.get("match").cloned().unwrap_or_default();
        let position = params.get("position").map(|s| s.as_str()).unwrap_or("end");

        let content = std::fs::read_to_string(&path).unwrap_or_default();
        let mut lines: Vec<String> = content.lines().map(|l| l.to_string()).collect();

        match state {
            "present" => {
                if line.is_empty() {
                    result.error = Some("file_line executor: line param is required for state=present".into());
                    result.duration_ms = start.elapsed().as_millis() as i32;
                    return result;
                }

                if !match_pattern.is_empty() {
                    // Replace matching lines.
                    let re = match Regex::new(&match_pattern) {
                        Ok(r) => r,
                        Err(e) => {
                            result.error = Some(format!("invalid regex {:?}: {}", match_pattern, e));
                            result.duration_ms = start.elapsed().as_millis() as i32;
                            return result;
                        }
                    };

                    let mut replaced = false;
                    for l in &mut lines {
                        if re.is_match(l) {
                            if *l != line {
                                *l = line.clone();
                                replaced = true;
                            }
                            // Already replaced (or already matches), stop.
                            if !replaced {
                                result.action = Action::None;
                                result.duration_ms = start.elapsed().as_millis() as i32;
                                return result;
                            }
                            break;
                        }
                    }

                    if replaced {
                        result.action = Action::Updated;
                        result.changed = true;
                    } else {
                        // No match found — insert at position.
                        result.action = Action::Created;
                        result.changed = true;
                        insert_line(&mut lines, &line, position);
                    }
                } else {
                    // Exact line check.
                    if lines.iter().any(|l| *l == line) {
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
                    if let Err(e) = std::fs::write(&path, output) {
                        result.error = Some(format!("write {}: {}", path, e));
                    }
                }
            }
            "absent" => {
                let original_count = lines.len();

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
                        if let Err(e) = std::fs::write(&path, output) {
                            result.error = Some(format!("write {}: {}", path, e));
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
        assert!(result.error.unwrap().contains("path param is required"));
    }

    #[test]
    fn present_adds_line_at_end() {
        let dir = tempfile::tempdir().unwrap();
        let path = dir.path().join("test.conf");
        std::fs::write(&path, "first line\nsecond line\n").unwrap();

        let exec = FileLineExecutor;
        let params = make_params(&[
            ("path", path.to_str().unwrap()),
            ("line", "third line"),
            ("state", "present"),
        ]);
        let result = exec.execute(&params, false);
        assert!(result.error.is_none(), "unexpected error: {:?}", result.error);
        assert!(result.changed);
        assert_eq!(result.action, Action::Created);

        let content = std::fs::read_to_string(&path).unwrap();
        assert!(content.contains("third line"));
        // The new line should be at the end.
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
            ("path", path.to_str().unwrap()),
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
            ("path", path.to_str().unwrap()),
            ("line", "port=443"),
            ("match", "^port="),
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
            ("path", path.to_str().unwrap()),
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
            ("path", path.to_str().unwrap()),
            ("match", "^port="),
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
            ("path", path.to_str().unwrap()),
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
            ("path", path.to_str().unwrap()),
            ("line", "new line"),
            ("state", "present"),
        ]);
        let result = exec.execute(&params, true);
        assert!(result.error.is_none(), "unexpected error: {:?}", result.error);
        assert!(result.changed);

        // File should be unchanged because of dry_run.
        let content = std::fs::read_to_string(&path).unwrap();
        assert_eq!(content, original);
    }
}
