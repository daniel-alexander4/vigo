// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: BUSL-1.1

//! FieldEdit executor — column-level editing in tabular files.
//!
//! Supports set/append/prepend/delete operations on individual fields within
//! lines matched by regex. Handles colon-delimited (/etc/passwd, /etc/group),
//! whitespace-delimited (/etc/fstab), and custom delimiters (CSV).

use super::atomic;
use super::{Action, Executor, ResourceResult};
use regex_lite::Regex;
use std::collections::HashMap;
use std::time::Instant;

pub struct FieldEditExecutor;

impl Executor for FieldEditExecutor {
    fn execute(&self, params: &HashMap<String, String>, dry_run: bool) -> ResourceResult {
        let start = Instant::now();
        let path = params.get("path").cloned().unwrap_or_default();
        let mut result = ResourceResult::new("field_edit", &path);

        if path.is_empty() {
            result.error = Some("field_edit executor: path param is required".into());
            result.duration_ms = start.elapsed().as_millis() as i32;
            return result;
        }

        let line_match = params.get("line_match").cloned().unwrap_or_default();
        if line_match.is_empty() {
            result.error = Some("field_edit executor: line_match param is required".into());
            result.duration_ms = start.elapsed().as_millis() as i32;
            return result;
        }

        let select_field_str = params.get("select_field").cloned().unwrap_or_default();
        if select_field_str.is_empty() {
            result.error = Some("field_edit executor: select_field param is required".into());
            result.duration_ms = start.elapsed().as_millis() as i32;
            return result;
        }
        let select_field: usize = match select_field_str.parse() {
            Ok(n) if n >= 1 => n,
            _ => {
                result.error = Some(format!(
                    "field_edit executor: select_field must be a positive integer, got {:?}",
                    select_field_str
                ));
                result.duration_ms = start.elapsed().as_millis() as i32;
                return result;
            }
        };

        let state = params.get("state").map(|s| s.as_str()).unwrap_or("present");
        let field_sep = params.get("field_separator").map(|s| s.as_str()).unwrap_or(":");
        let field_value = params.get("field_value").cloned().unwrap_or_default();
        let field_op = params.get("field_operation").map(|s| s.as_str()).unwrap_or("set");
        let value_sep = params.get("value_separator").map(|s| s.as_str()).unwrap_or(",");
        let extend_fields = params.get("extend_fields").map(|s| s == "true").unwrap_or(false);

        // field_value is required unless state=absent.
        if state != "absent" && field_value.is_empty() {
            result.error = Some("field_edit executor: field_value param is required when state=present".into());
            result.duration_ms = start.elapsed().as_millis() as i32;
            return result;
        }

        let re = match Regex::new(&line_match) {
            Ok(r) => r,
            Err(e) => {
                result.error = Some(format!("invalid regex {:?}: {}", line_match, e));
                result.duration_ms = start.elapsed().as_millis() as i32;
                return result;
            }
        };

        let is_whitespace_sep = field_sep == "\\s+";
        let split_re = if is_whitespace_sep {
            Some(Regex::new(r"\s+").unwrap())
        } else {
            None
        };

        let content = std::fs::read_to_string(&path).unwrap_or_default();
        let mut lines: Vec<String> = content.lines().map(|l| l.to_string()).collect();
        let mut changed = false;

        if state == "absent" {
            // Remove entire matching lines.
            let original_len = lines.len();
            lines.retain(|l| !re.is_match(l));
            if lines.len() != original_len {
                changed = true;
                result.action = Action::Deleted;
            } else {
                result.action = Action::None;
            }
        } else {
            // Edit fields in matching lines.
            let field_idx = select_field - 1; // 0-indexed

            for line in &mut lines {
                if !re.is_match(line) {
                    continue;
                }

                let mut fields: Vec<String> = if let Some(ref sre) = split_re {
                    sre.split(line).map(|s| s.to_string()).collect()
                } else {
                    line.split(field_sep).map(|s| s.to_string()).collect()
                };

                if field_idx >= fields.len() {
                    if !extend_fields {
                        result.error = Some(format!(
                            "field_edit executor: line has {} fields but select_field={} — set extend_fields=true to pad",
                            fields.len(), select_field
                        ));
                        result.duration_ms = start.elapsed().as_millis() as i32;
                        return result;
                    }
                    // Pad with empty fields.
                    while fields.len() <= field_idx {
                        fields.push(String::new());
                    }
                }

                let old_field = fields[field_idx].clone();
                let new_field = match field_op {
                    "set" => field_value.clone(),
                    "append" => append_subfield(&old_field, &field_value, value_sep),
                    "prepend" => prepend_subfield(&old_field, &field_value, value_sep),
                    "delete" => delete_subfield(&old_field, &field_value, value_sep),
                    _ => {
                        result.error = Some(format!(
                            "field_edit executor: unknown field_operation {:?}",
                            field_op
                        ));
                        result.duration_ms = start.elapsed().as_millis() as i32;
                        return result;
                    }
                };

                if new_field != old_field {
                    fields[field_idx] = new_field;
                    let join_sep = if is_whitespace_sep { "\t" } else { field_sep };
                    *line = fields.join(join_sep);
                    changed = true;
                }
            }

            if changed {
                result.action = Action::Updated;
            } else {
                result.action = Action::None;
            }
        }

        result.changed = changed;

        if changed && !dry_run {
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

/// Append `value` to a sub-field list if not already present.
fn append_subfield(field: &str, value: &str, sep: &str) -> String {
    if field.is_empty() {
        return value.to_string();
    }
    let parts: Vec<&str> = field.split(sep).collect();
    if parts.iter().any(|p| *p == value) {
        return field.to_string(); // Already present — idempotent.
    }
    format!("{}{}{}", field, sep, value)
}

/// Prepend `value` to a sub-field list if not already present.
fn prepend_subfield(field: &str, value: &str, sep: &str) -> String {
    if field.is_empty() {
        return value.to_string();
    }
    let parts: Vec<&str> = field.split(sep).collect();
    if parts.iter().any(|p| *p == value) {
        return field.to_string(); // Already present — idempotent.
    }
    format!("{}{}{}", value, sep, field)
}

/// Delete `value` from a sub-field list.
fn delete_subfield(field: &str, value: &str, sep: &str) -> String {
    let parts: Vec<&str> = field.split(sep).filter(|p| *p != value).collect();
    parts.join(sep)
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
        let exec = FieldEditExecutor;
        let params = make_params(&[("line_match", "^root:"), ("select_field", "4")]);
        let result = exec.execute(&params, false);
        assert!(result.error.unwrap().contains("path param is required"));
    }

    #[test]
    fn missing_line_match_errors() {
        let exec = FieldEditExecutor;
        let params = make_params(&[("path", "/tmp/x"), ("select_field", "4")]);
        let result = exec.execute(&params, false);
        assert!(result.error.unwrap().contains("line_match param is required"));
    }

    #[test]
    fn missing_select_field_errors() {
        let exec = FieldEditExecutor;
        let params = make_params(&[("path", "/tmp/x"), ("line_match", "^root:")]);
        let result = exec.execute(&params, false);
        assert!(result.error.unwrap().contains("select_field param is required"));
    }

    #[test]
    fn missing_field_value_errors() {
        let dir = tempfile::tempdir().unwrap();
        let path = dir.path().join("group");
        std::fs::write(&path, "docker:x:999:\n").unwrap();

        let exec = FieldEditExecutor;
        let params = make_params(&[
            ("path", path.to_str().unwrap()),
            ("line_match", "^docker:"),
            ("select_field", "4"),
        ]);
        let result = exec.execute(&params, false);
        assert!(result.error.unwrap().contains("field_value param is required"));
    }

    #[test]
    fn set_field() {
        let dir = tempfile::tempdir().unwrap();
        let path = dir.path().join("passwd");
        std::fs::write(&path, "root:x:0:0:root:/root:/bin/bash\n").unwrap();

        let exec = FieldEditExecutor;
        let params = make_params(&[
            ("path", path.to_str().unwrap()),
            ("line_match", "^root:"),
            ("select_field", "7"),
            ("field_value", "/bin/zsh"),
        ]);
        let result = exec.execute(&params, false);
        assert!(result.error.is_none(), "{:?}", result.error);
        assert!(result.changed);
        assert_eq!(result.action, Action::Updated);

        let content = std::fs::read_to_string(&path).unwrap();
        assert!(content.contains("root:x:0:0:root:/root:/bin/zsh"));
    }

    #[test]
    fn set_already_correct() {
        let dir = tempfile::tempdir().unwrap();
        let path = dir.path().join("passwd");
        std::fs::write(&path, "root:x:0:0:root:/root:/bin/bash\n").unwrap();

        let exec = FieldEditExecutor;
        let params = make_params(&[
            ("path", path.to_str().unwrap()),
            ("line_match", "^root:"),
            ("select_field", "7"),
            ("field_value", "/bin/bash"),
        ]);
        let result = exec.execute(&params, false);
        assert!(result.error.is_none(), "{:?}", result.error);
        assert!(!result.changed);
        assert_eq!(result.action, Action::None);
    }

    #[test]
    fn append_to_group() {
        let dir = tempfile::tempdir().unwrap();
        let path = dir.path().join("group");
        std::fs::write(&path, "docker:x:999:alice\n").unwrap();

        let exec = FieldEditExecutor;
        let params = make_params(&[
            ("path", path.to_str().unwrap()),
            ("line_match", "^docker:"),
            ("select_field", "4"),
            ("field_value", "deploy"),
            ("field_operation", "append"),
            ("value_separator", ","),
        ]);
        let result = exec.execute(&params, false);
        assert!(result.error.is_none(), "{:?}", result.error);
        assert!(result.changed);

        let content = std::fs::read_to_string(&path).unwrap();
        assert!(content.contains("docker:x:999:alice,deploy"));
    }

    #[test]
    fn append_idempotent() {
        let dir = tempfile::tempdir().unwrap();
        let path = dir.path().join("group");
        std::fs::write(&path, "docker:x:999:alice,deploy\n").unwrap();

        let exec = FieldEditExecutor;
        let params = make_params(&[
            ("path", path.to_str().unwrap()),
            ("line_match", "^docker:"),
            ("select_field", "4"),
            ("field_value", "deploy"),
            ("field_operation", "append"),
            ("value_separator", ","),
        ]);
        let result = exec.execute(&params, false);
        assert!(result.error.is_none(), "{:?}", result.error);
        assert!(!result.changed);
        assert_eq!(result.action, Action::None);
    }

    #[test]
    fn prepend_subfield() {
        let dir = tempfile::tempdir().unwrap();
        let path = dir.path().join("group");
        std::fs::write(&path, "docker:x:999:alice\n").unwrap();

        let exec = FieldEditExecutor;
        let params = make_params(&[
            ("path", path.to_str().unwrap()),
            ("line_match", "^docker:"),
            ("select_field", "4"),
            ("field_value", "deploy"),
            ("field_operation", "prepend"),
            ("value_separator", ","),
        ]);
        let result = exec.execute(&params, false);
        assert!(result.error.is_none(), "{:?}", result.error);
        assert!(result.changed);

        let content = std::fs::read_to_string(&path).unwrap();
        assert!(content.contains("docker:x:999:deploy,alice"));
    }

    #[test]
    fn delete_subfield_present() {
        let dir = tempfile::tempdir().unwrap();
        let path = dir.path().join("group");
        std::fs::write(&path, "docker:x:999:alice,deploy,bob\n").unwrap();

        let exec = FieldEditExecutor;
        let params = make_params(&[
            ("path", path.to_str().unwrap()),
            ("line_match", "^docker:"),
            ("select_field", "4"),
            ("field_value", "deploy"),
            ("field_operation", "delete"),
            ("value_separator", ","),
        ]);
        let result = exec.execute(&params, false);
        assert!(result.error.is_none(), "{:?}", result.error);
        assert!(result.changed);

        let content = std::fs::read_to_string(&path).unwrap();
        assert!(content.contains("docker:x:999:alice,bob"));
    }

    #[test]
    fn delete_subfield_not_present() {
        let dir = tempfile::tempdir().unwrap();
        let path = dir.path().join("group");
        std::fs::write(&path, "docker:x:999:alice,bob\n").unwrap();

        let exec = FieldEditExecutor;
        let params = make_params(&[
            ("path", path.to_str().unwrap()),
            ("line_match", "^docker:"),
            ("select_field", "4"),
            ("field_value", "deploy"),
            ("field_operation", "delete"),
            ("value_separator", ","),
        ]);
        let result = exec.execute(&params, false);
        assert!(result.error.is_none(), "{:?}", result.error);
        assert!(!result.changed);
    }

    #[test]
    fn extend_fields_true() {
        let dir = tempfile::tempdir().unwrap();
        let path = dir.path().join("data.csv");
        std::fs::write(&path, "a:b\n").unwrap();

        let exec = FieldEditExecutor;
        let params = make_params(&[
            ("path", path.to_str().unwrap()),
            ("line_match", "^a:"),
            ("select_field", "4"),
            ("field_value", "new"),
            ("extend_fields", "true"),
        ]);
        let result = exec.execute(&params, false);
        assert!(result.error.is_none(), "{:?}", result.error);
        assert!(result.changed);

        let content = std::fs::read_to_string(&path).unwrap();
        assert_eq!(content, "a:b::new\n");
    }

    #[test]
    fn extend_fields_false_errors() {
        let dir = tempfile::tempdir().unwrap();
        let path = dir.path().join("data.csv");
        std::fs::write(&path, "a:b\n").unwrap();

        let exec = FieldEditExecutor;
        let params = make_params(&[
            ("path", path.to_str().unwrap()),
            ("line_match", "^a:"),
            ("select_field", "4"),
            ("field_value", "new"),
        ]);
        let result = exec.execute(&params, false);
        assert!(result.error.is_some());
        assert!(result.error.unwrap().contains("extend_fields"));
    }

    #[test]
    fn whitespace_separator_fstab() {
        let dir = tempfile::tempdir().unwrap();
        let path = dir.path().join("fstab");
        std::fs::write(
            &path,
            "/dev/sda1\t/\text4\tdefaults\t0\t1\n/dev/sdb1\t/data\txfs\tdefaults\t0\t0\n",
        )
        .unwrap();

        let exec = FieldEditExecutor;
        let params = make_params(&[
            ("path", path.to_str().unwrap()),
            ("line_match", "/data"),
            ("field_separator", "\\s+"),
            ("select_field", "4"),
            ("field_value", "noatime,defaults"),
        ]);
        let result = exec.execute(&params, false);
        assert!(result.error.is_none(), "{:?}", result.error);
        assert!(result.changed);

        let content = std::fs::read_to_string(&path).unwrap();
        assert!(content.contains("noatime,defaults"));
    }

    #[test]
    fn no_match_no_change() {
        let dir = tempfile::tempdir().unwrap();
        let path = dir.path().join("group");
        std::fs::write(&path, "users:x:100:\n").unwrap();

        let exec = FieldEditExecutor;
        let params = make_params(&[
            ("path", path.to_str().unwrap()),
            ("line_match", "^docker:"),
            ("select_field", "4"),
            ("field_value", "deploy"),
            ("field_operation", "append"),
        ]);
        let result = exec.execute(&params, false);
        assert!(result.error.is_none(), "{:?}", result.error);
        assert!(!result.changed);
    }

    #[test]
    fn multiple_matches() {
        let dir = tempfile::tempdir().unwrap();
        let path = dir.path().join("data");
        std::fs::write(&path, "row:a:old\nrow:b:old\nother:c:keep\n").unwrap();

        let exec = FieldEditExecutor;
        let params = make_params(&[
            ("path", path.to_str().unwrap()),
            ("line_match", "^row:"),
            ("select_field", "3"),
            ("field_value", "new"),
        ]);
        let result = exec.execute(&params, false);
        assert!(result.error.is_none(), "{:?}", result.error);
        assert!(result.changed);

        let content = std::fs::read_to_string(&path).unwrap();
        assert_eq!(content, "row:a:new\nrow:b:new\nother:c:keep\n");
    }

    #[test]
    fn dry_run_does_not_modify() {
        let dir = tempfile::tempdir().unwrap();
        let path = dir.path().join("group");
        let original = "docker:x:999:\n";
        std::fs::write(&path, original).unwrap();

        let exec = FieldEditExecutor;
        let params = make_params(&[
            ("path", path.to_str().unwrap()),
            ("line_match", "^docker:"),
            ("select_field", "4"),
            ("field_value", "deploy"),
            ("field_operation", "append"),
        ]);
        let result = exec.execute(&params, true);
        assert!(result.error.is_none(), "{:?}", result.error);
        assert!(result.changed);

        let content = std::fs::read_to_string(&path).unwrap();
        assert_eq!(content, original);
    }

    #[test]
    fn absent_removes_matching_lines() {
        let dir = tempfile::tempdir().unwrap();
        let path = dir.path().join("data");
        std::fs::write(&path, "keep:1\nremove:2\nkeep:3\n").unwrap();

        let exec = FieldEditExecutor;
        let params = make_params(&[
            ("path", path.to_str().unwrap()),
            ("line_match", "^remove:"),
            ("select_field", "1"),
            ("state", "absent"),
        ]);
        let result = exec.execute(&params, false);
        assert!(result.error.is_none(), "{:?}", result.error);
        assert!(result.changed);
        assert_eq!(result.action, Action::Deleted);

        let content = std::fs::read_to_string(&path).unwrap();
        assert_eq!(content, "keep:1\nkeep:3\n");
    }
}
