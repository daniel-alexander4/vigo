// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: BUSL-1.1

//! Mount executor — manages mount points and /etc/fstab entries idempotently.

use super::{Action, CommandRunner, DefaultRunner, Executor, ResourceResult};
use std::collections::HashMap;
use std::time::Instant;

pub struct MountExecutor {
    pub runner: Box<dyn CommandRunner>,
    pub fstab_path: String,
}

impl MountExecutor {
    pub fn new() -> Self {
        Self {
            runner: Box::new(DefaultRunner),
            fstab_path: "/etc/fstab".into(),
        }
    }

    fn is_mounted(&self, path: &str) -> bool {
        match self.runner.run("findmnt", &["-n", "-o", "TARGET", path]) {
            Ok(o) => o.status.success(),
            Err(_) => false,
        }
    }

    fn fstab_has_entry(&self, mount_point: &str) -> bool {
        let content = std::fs::read_to_string(&self.fstab_path).unwrap_or_default();
        content.lines().any(|line| {
            let trimmed = line.trim();
            if trimmed.is_empty() || trimmed.starts_with('#') {
                return false;
            }
            let fields: Vec<&str> = trimmed.split_whitespace().collect();
            fields.len() >= 2 && fields[1] == mount_point
        })
    }

    fn add_fstab_entry(
        &self,
        device: &str,
        mount_point: &str,
        fstype: &str,
        options: &str,
        dump: &str,
        pass: &str,
    ) -> Result<(), String> {
        let entry = format!(
            "{}\t{}\t{}\t{}\t{}\t{}\n",
            device, mount_point, fstype, options, dump, pass
        );
        let mut content = std::fs::read_to_string(&self.fstab_path).unwrap_or_default();
        if !content.ends_with('\n') && !content.is_empty() {
            content.push('\n');
        }
        content.push_str(&entry);
        std::fs::write(&self.fstab_path, content)
            .map_err(|e| format!("write fstab: {}", e))
    }

    fn remove_fstab_entry(&self, mount_point: &str) -> Result<bool, String> {
        let content = std::fs::read_to_string(&self.fstab_path).unwrap_or_default();
        let mut lines: Vec<&str> = content.lines().collect();
        let original_len = lines.len();

        lines.retain(|line| {
            let trimmed = line.trim();
            if trimmed.is_empty() || trimmed.starts_with('#') {
                return true;
            }
            let fields: Vec<&str> = trimmed.split_whitespace().collect();
            !(fields.len() >= 2 && fields[1] == mount_point)
        });

        if lines.len() == original_len {
            return Ok(false);
        }

        let mut output = lines.join("\n");
        if !output.ends_with('\n') && !output.is_empty() {
            output.push('\n');
        }
        std::fs::write(&self.fstab_path, output)
            .map_err(|e| format!("write fstab: {}", e))?;
        Ok(true)
    }

    fn update_fstab_entry(
        &self,
        device: &str,
        mount_point: &str,
        fstype: &str,
        options: &str,
        dump: &str,
        pass: &str,
    ) -> Result<bool, String> {
        let content = std::fs::read_to_string(&self.fstab_path).unwrap_or_default();
        let desired = format!("{}\t{}\t{}\t{}\t{}\t{}", device, mount_point, fstype, options, dump, pass);
        let mut updated = false;
        let mut lines: Vec<String> = Vec::new();

        for line in content.lines() {
            let trimmed = line.trim();
            if trimmed.is_empty() || trimmed.starts_with('#') {
                lines.push(line.to_string());
                continue;
            }
            let fields: Vec<&str> = trimmed.split_whitespace().collect();
            if fields.len() >= 2 && fields[1] == mount_point {
                let current = fields.join("\t");
                let desired_norm = desired.split_whitespace().collect::<Vec<_>>().join("\t");
                if current != desired_norm {
                    lines.push(desired.clone());
                    updated = true;
                } else {
                    lines.push(line.to_string());
                }
            } else {
                lines.push(line.to_string());
            }
        }

        if updated {
            let mut output = lines.join("\n");
            if !output.ends_with('\n') && !output.is_empty() {
                output.push('\n');
            }
            std::fs::write(&self.fstab_path, output)
                .map_err(|e| format!("write fstab: {}", e))?;
        }

        Ok(updated)
    }
}

impl Executor for MountExecutor {
    fn execute(&self, params: &HashMap<String, String>, dry_run: bool) -> ResourceResult {
        let start = Instant::now();
        let mount_point = params
            .get("path")
            .or_else(|| params.get("name"))
            .cloned()
            .unwrap_or_default();

        let mut result = ResourceResult::new("mount", &mount_point);

        if mount_point.is_empty() {
            result.error = Some("mount executor: path param is required".into());
            result.duration_ms = start.elapsed().as_millis() as i32;
            return result;
        }

        let state = params.get("state").map(|s| s.as_str()).unwrap_or("mounted");
        let device = params.get("device").cloned().unwrap_or_default();
        let fstype = params.get("fstype").cloned().unwrap_or_default();
        let options = params.get("options").map(|s| s.as_str()).unwrap_or("defaults");
        let dump = params.get("dump").map(|s| s.as_str()).unwrap_or("0");
        let pass = params.get("pass").map(|s| s.as_str()).unwrap_or("0");

        let mounted = self.is_mounted(&mount_point);
        let in_fstab = self.fstab_has_entry(&mount_point);

        match state {
            "mounted" | "present" => {
                if device.is_empty() || fstype.is_empty() {
                    result.error = Some("mount executor: device and fstype required for mounted/present".into());
                    result.duration_ms = start.elapsed().as_millis() as i32;
                    return result;
                }

                let mut changed = false;

                // Ensure fstab entry.
                if !in_fstab {
                    changed = true;
                    if !dry_run {
                        if let Err(e) = self.add_fstab_entry(&device, &mount_point, &fstype, options, dump, pass) {
                            result.error = Some(e);
                            result.duration_ms = start.elapsed().as_millis() as i32;
                            return result;
                        }
                    }
                } else if !dry_run {
                    // Update fstab if different.
                    match self.update_fstab_entry(&device, &mount_point, &fstype, options, dump, pass) {
                        Ok(true) => changed = true,
                        Err(e) => {
                            result.error = Some(e);
                            result.duration_ms = start.elapsed().as_millis() as i32;
                            return result;
                        }
                        _ => {}
                    }
                }

                // Mount if state=mounted and not yet mounted.
                if state == "mounted" && !mounted {
                    changed = true;
                    if !dry_run {
                        let _ = std::fs::create_dir_all(&mount_point);
                        match self.runner.run("mount", &[&mount_point]) {
                            Ok(o) if !o.status.success() => {
                                let stderr = String::from_utf8_lossy(&o.stderr);
                                result.error = Some(format!("mount {}: {}", mount_point, stderr));
                                result.duration_ms = start.elapsed().as_millis() as i32;
                                return result;
                            }
                            Err(e) => {
                                result.error = Some(format!("mount {}: {}", mount_point, e));
                                result.duration_ms = start.elapsed().as_millis() as i32;
                                return result;
                            }
                            _ => {}
                        }
                    }
                }

                if changed {
                    result.action = if in_fstab { Action::Updated } else { Action::Created };
                    result.changed = true;
                } else {
                    result.action = Action::None;
                }
            }
            "unmounted" => {
                if mounted {
                    result.action = Action::Updated;
                    result.changed = true;
                    if !dry_run {
                        match self.runner.run("umount", &[&mount_point]) {
                            Ok(o) if !o.status.success() => {
                                let stderr = String::from_utf8_lossy(&o.stderr);
                                result.error = Some(format!("umount {}: {}", mount_point, stderr));
                            }
                            Err(e) => result.error = Some(format!("umount {}: {}", mount_point, e)),
                            _ => {}
                        }
                    }
                } else {
                    result.action = Action::None;
                }
            }
            "absent" => {
                let mut changed = false;

                if mounted {
                    changed = true;
                    if !dry_run {
                        match self.runner.run("umount", &[&mount_point]) {
                            Ok(o) if !o.status.success() => {
                                let stderr = String::from_utf8_lossy(&o.stderr);
                                result.error = Some(format!("umount {}: {}", mount_point, stderr));
                                result.duration_ms = start.elapsed().as_millis() as i32;
                                return result;
                            }
                            Err(e) => {
                                result.error = Some(format!("umount {}: {}", mount_point, e));
                                result.duration_ms = start.elapsed().as_millis() as i32;
                                return result;
                            }
                            _ => {}
                        }
                    }
                }

                if in_fstab {
                    changed = true;
                    if !dry_run {
                        if let Err(e) = self.remove_fstab_entry(&mount_point) {
                            result.error = Some(e);
                        }
                    }
                }

                if changed {
                    result.action = Action::Deleted;
                    result.changed = true;
                } else {
                    result.action = Action::None;
                }
            }
            _ => {
                result.error = Some(format!("mount executor: unknown state {:?}", state));
            }
        }

        result.duration_ms = start.elapsed().as_millis() as i32;
        result
    }
}
