// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

//! BlockInFile executor — manages marked blocks of content in files.

use super::atomic;
use super::{Action, Executor, ResourceResult};
use std::collections::HashMap;
use std::time::Instant;

pub struct BlockInFileExecutor;

impl Executor for BlockInFileExecutor {
    fn execute(&self, params: &HashMap<String, String>, dry_run: bool) -> ResourceResult {
        let start = Instant::now();
        let path = params.get("target_path").cloned().unwrap_or_default();
        let mut result = ResourceResult::new("blockinfile", &path);

        if path.is_empty() {
            result.error = Some("blockinfile executor: target_path param is required".into());
            result.duration_ms = start.elapsed().as_millis() as i32;
            return result;
        }

        let state = params.get("state").map(|s| s.as_str()).unwrap_or("present");
        let block = params.get("block_content").cloned().unwrap_or_default();
        let label = params.get("label").cloned().unwrap_or_default();

        let mut marker_begin = params
            .get("marker_begin")
            .cloned()
            .unwrap_or_else(|| "# BEGIN VIGO MANAGED BLOCK".into());
        let mut marker_end = params
            .get("marker_end")
            .cloned()
            .unwrap_or_else(|| "# END VIGO MANAGED BLOCK".into());

        if !label.is_empty() {
            marker_begin = format!("{} {}", marker_begin, label);
            marker_end = format!("{} {}", marker_end, label);
        }

        let create = params.get("create").map(|s| s == "true").unwrap_or(false);
        let file_exists = std::path::Path::new(&path).exists();

        let content = if file_exists {
            match std::fs::read_to_string(&path) {
                Ok(c) => c,
                Err(e) => {
                    result.error = Some(format!("read {}: {}", path, e));
                    result.duration_ms = start.elapsed().as_millis() as i32;
                    return result;
                }
            }
        } else if state == "present" && create {
            String::new()
        } else if state == "present" {
            result.error = Some(format!(
                "blockinfile executor: {} does not exist (set create=true to create)",
                path
            ));
            result.duration_ms = start.elapsed().as_millis() as i32;
            return result;
        } else {
            // absent + file doesn't exist = nothing to do.
            result.action = Action::None;
            result.duration_ms = start.elapsed().as_millis() as i32;
            return result;
        };

        let lines: Vec<&str> = content.lines().collect();
        let begin_idx = lines.iter().position(|l| l.trim() == marker_begin);
        let end_idx = lines.iter().position(|l| l.trim() == marker_end);

        match state {
            "present" => {
                if block.is_empty() {
                    result.error = Some("blockinfile executor: block_content param is required for state=present".into());
                    result.duration_ms = start.elapsed().as_millis() as i32;
                    return result;
                }

                let desired_block = format!("{}\n{}\n{}", marker_begin, block, marker_end);

                if let (Some(bi), Some(ei)) = (begin_idx, end_idx) {
                    if ei > bi {
                        // Extract existing block.
                        let existing: Vec<&str> = lines[bi..=ei].to_vec();
                        let existing_text = existing.join("\n");
                        if existing_text == desired_block {
                            result.action = Action::None;
                            result.duration_ms = start.elapsed().as_millis() as i32;
                            return result;
                        }

                        // Replace block.
                        result.action = Action::Updated;
                        result.changed = true;

                        if !dry_run {
                            let mut new_lines: Vec<&str> = Vec::new();
                            new_lines.extend_from_slice(&lines[..bi]);
                            let block_lines: Vec<&str> = desired_block.lines().collect();
                            new_lines.extend(block_lines);
                            if ei + 1 < lines.len() {
                                new_lines.extend_from_slice(&lines[ei + 1..]);
                            }
                            let mut output = new_lines.join("\n");
                            if content.ends_with('\n') {
                                output.push('\n');
                            }
                            if let Err(e) = atomic::atomic_write(&path, &output) {
                                result.error = Some(e);
                            }
                        }
                    } else {
                        // Malformed markers.
                        result.error = Some("blockinfile executor: end marker before begin marker".into());
                    }
                } else {
                    // No existing block — append.
                    result.action = Action::Created;
                    result.changed = true;

                    if !dry_run {
                        let mut new_content = content.clone();
                        if !new_content.is_empty() && !new_content.ends_with('\n') {
                            new_content.push('\n');
                        }
                        new_content.push_str(&desired_block);
                        new_content.push('\n');

                        if let Err(e) = atomic::atomic_write(&path, &new_content) {
                            result.error = Some(e);
                            result.duration_ms = start.elapsed().as_millis() as i32;
                            return result;
                        }
                        if !file_exists {
                            #[cfg(unix)]
                            {
                                use std::os::unix::fs::PermissionsExt;
                                let mode_str = params.get("mode").map(|s| s.as_str()).unwrap_or("0644");
                                if let Ok(mode) = u32::from_str_radix(mode_str.trim_start_matches('0'), 8) {
                                    let _ = std::fs::set_permissions(&path, std::fs::Permissions::from_mode(mode));
                                }
                            }
                        }
                    }
                }
            }
            "absent" => {
                if let (Some(bi), Some(ei)) = (begin_idx, end_idx) {
                    if ei >= bi {
                        result.action = Action::Deleted;
                        result.changed = true;

                        if !dry_run {
                            let mut new_lines: Vec<&str> = Vec::new();
                            new_lines.extend_from_slice(&lines[..bi]);
                            if ei + 1 < lines.len() {
                                new_lines.extend_from_slice(&lines[ei + 1..]);
                            }
                            let mut output = new_lines.join("\n");
                            if content.ends_with('\n') && !output.is_empty() {
                                output.push('\n');
                            }
                            if let Err(e) = atomic::atomic_write(&path, &output) {
                                result.error = Some(e);
                            }
                        }
                    } else {
                        result.action = Action::None;
                    }
                } else {
                    result.action = Action::None;
                }
            }
            _ => {
                result.error = Some(format!("blockinfile executor: unknown state {:?}", state));
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
        pairs.iter().map(|(k, v)| (k.to_string(), v.to_string())).collect()
    }

    #[test]
    fn missing_path_errors() {
        let exec = BlockInFileExecutor;
        let params = make_params(&[("block_content", "some content")]);
        let result = exec.execute(&params, false);
        assert!(result.error.is_some());
        assert!(result.error.unwrap().contains("target_path param is required"));
    }

    #[test]
    fn present_appends_block() {
        let dir = tempfile::tempdir().unwrap();
        let path = dir.path().join("config.conf");
        std::fs::write(&path, "# existing content\nfoo=bar\n").unwrap();

        let exec = BlockInFileExecutor;
        let params = make_params(&[
            ("target_path", path.to_str().unwrap()),
            ("block_content", "nameserver 8.8.8.8\nnameserver 8.8.4.4"),
            ("state", "present"),
        ]);
        let result = exec.execute(&params, false);
        assert!(result.error.is_none(), "unexpected error: {:?}", result.error);
        assert!(result.changed);
        assert_eq!(result.action, Action::Created);

        let content = std::fs::read_to_string(&path).unwrap();
        assert!(content.contains("# BEGIN VIGO MANAGED BLOCK"));
        assert!(content.contains("nameserver 8.8.8.8"));
        assert!(content.contains("nameserver 8.8.4.4"));
        assert!(content.contains("# END VIGO MANAGED BLOCK"));
        // Original content should still be present.
        assert!(content.contains("foo=bar"));
    }

    #[test]
    fn present_block_already_matches() {
        let dir = tempfile::tempdir().unwrap();
        let path = dir.path().join("config.conf");
        let content = "# existing\n# BEGIN VIGO MANAGED BLOCK\nhello world\n# END VIGO MANAGED BLOCK\n";
        std::fs::write(&path, content).unwrap();

        let exec = BlockInFileExecutor;
        let params = make_params(&[
            ("target_path", path.to_str().unwrap()),
            ("block_content", "hello world"),
            ("state", "present"),
        ]);
        let result = exec.execute(&params, false);
        assert!(result.error.is_none(), "unexpected error: {:?}", result.error);
        assert!(!result.changed);
        assert_eq!(result.action, Action::None);
    }

    #[test]
    fn present_updates_block() {
        let dir = tempfile::tempdir().unwrap();
        let path = dir.path().join("config.conf");
        let content = "header\n# BEGIN VIGO MANAGED BLOCK\nold content\n# END VIGO MANAGED BLOCK\nfooter\n";
        std::fs::write(&path, content).unwrap();

        let exec = BlockInFileExecutor;
        let params = make_params(&[
            ("target_path", path.to_str().unwrap()),
            ("block_content", "new content"),
            ("state", "present"),
        ]);
        let result = exec.execute(&params, false);
        assert!(result.error.is_none(), "unexpected error: {:?}", result.error);
        assert!(result.changed);
        assert_eq!(result.action, Action::Updated);

        let updated = std::fs::read_to_string(&path).unwrap();
        assert!(updated.contains("new content"));
        assert!(!updated.contains("old content"));
        assert!(updated.contains("header"));
        assert!(updated.contains("footer"));
    }

    #[test]
    fn absent_removes_block() {
        let dir = tempfile::tempdir().unwrap();
        let path = dir.path().join("config.conf");
        let content = "header\n# BEGIN VIGO MANAGED BLOCK\nmanaged content\n# END VIGO MANAGED BLOCK\nfooter\n";
        std::fs::write(&path, content).unwrap();

        let exec = BlockInFileExecutor;
        let params = make_params(&[
            ("target_path", path.to_str().unwrap()),
            ("state", "absent"),
        ]);
        let result = exec.execute(&params, false);
        assert!(result.error.is_none(), "unexpected error: {:?}", result.error);
        assert!(result.changed);
        assert_eq!(result.action, Action::Deleted);

        let updated = std::fs::read_to_string(&path).unwrap();
        assert!(!updated.contains("BEGIN VIGO MANAGED BLOCK"));
        assert!(!updated.contains("managed content"));
        assert!(!updated.contains("END VIGO MANAGED BLOCK"));
        assert!(updated.contains("header"));
        assert!(updated.contains("footer"));
    }

    #[test]
    fn absent_no_block() {
        let dir = tempfile::tempdir().unwrap();
        let path = dir.path().join("config.conf");
        std::fs::write(&path, "just normal content\n").unwrap();

        let exec = BlockInFileExecutor;
        let params = make_params(&[
            ("target_path", path.to_str().unwrap()),
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
        let path = dir.path().join("config.conf");
        let original = "existing content\n";
        std::fs::write(&path, original).unwrap();

        let exec = BlockInFileExecutor;
        let params = make_params(&[
            ("target_path", path.to_str().unwrap()),
            ("block_content", "new block content"),
            ("state", "present"),
        ]);
        let result = exec.execute(&params, true);
        assert!(result.error.is_none(), "unexpected error: {:?}", result.error);
        assert!(result.changed);

        // File should be unchanged because of dry_run.
        let content = std::fs::read_to_string(&path).unwrap();
        assert_eq!(content, original);
    }

    #[test]
    fn custom_label() {
        let dir = tempfile::tempdir().unwrap();
        let path = dir.path().join("config.conf");
        std::fs::write(&path, "existing\n").unwrap();

        let exec = BlockInFileExecutor;
        let params = make_params(&[
            ("target_path", path.to_str().unwrap()),
            ("block_content", "labeled content"),
            ("label", "myapp"),
            ("state", "present"),
        ]);
        let result = exec.execute(&params, false);
        assert!(result.error.is_none(), "unexpected error: {:?}", result.error);
        assert!(result.changed);
        assert_eq!(result.action, Action::Created);

        let content = std::fs::read_to_string(&path).unwrap();
        assert!(content.contains("# BEGIN VIGO MANAGED BLOCK myapp"));
        assert!(content.contains("# END VIGO MANAGED BLOCK myapp"));
        assert!(content.contains("labeled content"));
    }
}
