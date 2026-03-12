// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: BUSL-1.1

//! BlockInFile executor — manages marked blocks of content in files.

use super::{Action, Executor, ResourceResult};
use std::collections::HashMap;
use std::time::Instant;

pub struct BlockInFileExecutor;

impl Executor for BlockInFileExecutor {
    fn execute(&self, params: &HashMap<String, String>, dry_run: bool) -> ResourceResult {
        let start = Instant::now();
        let path = params.get("path").cloned().unwrap_or_default();
        let mut result = ResourceResult::new("blockinfile", &path);

        if path.is_empty() {
            result.error = Some("blockinfile executor: path param is required".into());
            result.duration_ms = start.elapsed().as_millis() as i32;
            return result;
        }

        let state = params.get("state").map(|s| s.as_str()).unwrap_or("present");
        let block = params.get("block").cloned().unwrap_or_default();
        let label = params.get("label").cloned().unwrap_or_default();

        let mut marker_begin = params
            .get("marker_begin")
            .cloned()
            .unwrap_or_else(|| "# BEGIN HOBNOB MANAGED BLOCK".into());
        let mut marker_end = params
            .get("marker_end")
            .cloned()
            .unwrap_or_else(|| "# END HOBNOB MANAGED BLOCK".into());

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
                    result.error = Some("blockinfile executor: block param is required for state=present".into());
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
                            if let Err(e) = std::fs::write(&path, output) {
                                result.error = Some(format!("write {}: {}", path, e));
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

                        if !file_exists {
                            // Set permissions if creating new file.
                            if let Err(e) = std::fs::write(&path, &new_content) {
                                result.error = Some(format!("write {}: {}", path, e));
                                result.duration_ms = start.elapsed().as_millis() as i32;
                                return result;
                            }
                            #[cfg(unix)]
                            {
                                use std::os::unix::fs::PermissionsExt;
                                let mode_str = params.get("mode").map(|s| s.as_str()).unwrap_or("0644");
                                if let Ok(mode) = u32::from_str_radix(mode_str.trim_start_matches('0'), 8) {
                                    let _ = std::fs::set_permissions(&path, std::fs::Permissions::from_mode(mode));
                                }
                            }
                        } else if let Err(e) = std::fs::write(&path, &new_content) {
                            result.error = Some(format!("write {}: {}", path, e));
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
                            if let Err(e) = std::fs::write(&path, output) {
                                result.error = Some(format!("write {}: {}", path, e));
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
