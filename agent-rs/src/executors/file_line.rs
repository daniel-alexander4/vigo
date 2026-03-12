// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: BUSL-1.1

//! FileLine executor — manages individual lines in files idempotently.

use super::{Action, Executor, ResourceResult};
use regex::Regex;
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
