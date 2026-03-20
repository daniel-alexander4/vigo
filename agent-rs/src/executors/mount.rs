// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

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

#[cfg(test)]
mod tests {
    use super::*;
    use crate::executors::test_support::mock::*;
    use std::sync::Arc;

    #[test]
    fn missing_path_errors() {
        let fstab = tempfile::NamedTempFile::new().unwrap();
        let mock = MockRunner::new(vec![]);
        let exec = MountExecutor {
            runner: Box::new(mock),
            fstab_path: fstab.path().to_str().unwrap().to_string(),
        };
        let result = exec.execute(&make_params(&[]), false);
        assert!(result.error.is_some());
        assert!(
            result.error.as_deref().unwrap().contains("path param is required"),
            "unexpected error: {:?}",
            result.error
        );
    }

    #[test]
    fn mounted_adds_fstab_and_mounts() {
        // Empty fstab, findmnt says not mounted -> adds fstab entry + calls mount
        let fstab = tempfile::NamedTempFile::new().unwrap();
        let mock = MockRunner::new(vec![
            fail_output(""),  // findmnt -> not mounted
            ok_output(""),    // mount
        ]);
        let calls = Arc::clone(&mock.calls);
        let exec = MountExecutor {
            runner: Box::new(mock),
            fstab_path: fstab.path().to_str().unwrap().to_string(),
        };
        let result = exec.execute(
            &make_params(&[
                ("path", "/mnt/data"),
                ("device", "/dev/sdb1"),
                ("fstype", "ext4"),
                ("state", "mounted"),
            ]),
            false,
        );
        assert_eq!(result.action, Action::Created);
        assert!(result.changed);
        assert!(result.error.is_none(), "unexpected error: {:?}", result.error);
        // Verify fstab was written
        let content = std::fs::read_to_string(fstab.path()).unwrap();
        assert!(content.contains("/dev/sdb1"), "fstab should contain device");
        assert!(content.contains("/mnt/data"), "fstab should contain mount point");
        // Verify mount was called
        let calls = calls.lock().unwrap();
        assert_eq!(calls.len(), 2);
        assert_eq!(calls[1].0, "mount");
    }

    #[test]
    fn already_mounted_with_fstab() {
        // fstab has entry, findmnt says mounted -> Action::None
        let fstab = tempfile::NamedTempFile::new().unwrap();
        std::fs::write(
            fstab.path(),
            "/dev/sdb1\t/mnt/data\text4\tdefaults\t0\t0\n",
        )
        .unwrap();
        let mock = MockRunner::new(vec![
            ok_output("/mnt/data\n"),  // findmnt -> mounted
        ]);
        let exec = MountExecutor {
            runner: Box::new(mock),
            fstab_path: fstab.path().to_str().unwrap().to_string(),
        };
        let result = exec.execute(
            &make_params(&[
                ("path", "/mnt/data"),
                ("device", "/dev/sdb1"),
                ("fstype", "ext4"),
                ("state", "mounted"),
            ]),
            false,
        );
        assert_eq!(result.action, Action::None);
        assert!(!result.changed);
        assert!(result.error.is_none());
    }

    #[test]
    fn present_adds_fstab_only() {
        // state=present, not in fstab, not mounted -> adds fstab but doesn't mount
        let fstab = tempfile::NamedTempFile::new().unwrap();
        let mock = MockRunner::new(vec![
            fail_output(""),  // findmnt -> not mounted
            // No mount call expected for state=present
        ]);
        let calls = Arc::clone(&mock.calls);
        let exec = MountExecutor {
            runner: Box::new(mock),
            fstab_path: fstab.path().to_str().unwrap().to_string(),
        };
        let result = exec.execute(
            &make_params(&[
                ("path", "/mnt/data"),
                ("device", "/dev/sdb1"),
                ("fstype", "ext4"),
                ("state", "present"),
            ]),
            false,
        );
        assert_eq!(result.action, Action::Created);
        assert!(result.changed);
        assert!(result.error.is_none(), "unexpected error: {:?}", result.error);
        // Verify fstab was written
        let content = std::fs::read_to_string(fstab.path()).unwrap();
        assert!(content.contains("/dev/sdb1"), "fstab should contain device");
        // Only findmnt was called, no mount
        let calls = calls.lock().unwrap();
        assert_eq!(calls.len(), 1, "state=present should not call mount, got {:?}", *calls);
    }

    #[test]
    fn unmounted_calls_umount() {
        // state=unmounted, findmnt says mounted -> umount called
        let fstab = tempfile::NamedTempFile::new().unwrap();
        let mock = MockRunner::new(vec![
            ok_output("/mnt/data\n"),  // findmnt -> mounted
            ok_output(""),              // umount
        ]);
        let calls = Arc::clone(&mock.calls);
        let exec = MountExecutor {
            runner: Box::new(mock),
            fstab_path: fstab.path().to_str().unwrap().to_string(),
        };
        let result = exec.execute(
            &make_params(&[
                ("path", "/mnt/data"),
                ("state", "unmounted"),
            ]),
            false,
        );
        assert_eq!(result.action, Action::Updated);
        assert!(result.changed);
        assert!(result.error.is_none());
        let calls = calls.lock().unwrap();
        assert_eq!(calls.len(), 2);
        assert_eq!(calls[1].0, "umount");
    }

    #[test]
    fn absent_unmounts_and_removes_fstab() {
        // mounted + in fstab, state=absent -> umount + fstab entry removed
        let fstab = tempfile::NamedTempFile::new().unwrap();
        std::fs::write(
            fstab.path(),
            "/dev/sdb1\t/mnt/data\text4\tdefaults\t0\t0\n",
        )
        .unwrap();
        let mock = MockRunner::new(vec![
            ok_output("/mnt/data\n"),  // findmnt -> mounted
            ok_output(""),              // umount
        ]);
        let calls = Arc::clone(&mock.calls);
        let exec = MountExecutor {
            runner: Box::new(mock),
            fstab_path: fstab.path().to_str().unwrap().to_string(),
        };
        let result = exec.execute(
            &make_params(&[
                ("path", "/mnt/data"),
                ("state", "absent"),
            ]),
            false,
        );
        assert_eq!(result.action, Action::Deleted);
        assert!(result.changed);
        assert!(result.error.is_none(), "unexpected error: {:?}", result.error);
        // Verify fstab entry was removed
        let content = std::fs::read_to_string(fstab.path()).unwrap();
        assert!(
            !content.contains("/mnt/data"),
            "fstab should not contain mount point after absent"
        );
        // Verify umount was called
        let calls = calls.lock().unwrap();
        assert_eq!(calls[1].0, "umount");
    }

    #[test]
    fn absent_noop_when_nothing() {
        // state=absent, not mounted, not in fstab -> Action::None
        let fstab = tempfile::NamedTempFile::new().unwrap();
        let mock = MockRunner::new(vec![
            fail_output(""),  // findmnt -> not mounted
        ]);
        let exec = MountExecutor {
            runner: Box::new(mock),
            fstab_path: fstab.path().to_str().unwrap().to_string(),
        };
        let result = exec.execute(
            &make_params(&[
                ("path", "/mnt/data"),
                ("state", "absent"),
            ]),
            false,
        );
        assert_eq!(result.action, Action::None);
        assert!(!result.changed);
        assert!(result.error.is_none());
    }

    #[test]
    fn dry_run_does_not_modify() {
        // dry_run=true, needs mount -> Created but fstab unchanged and no mount call
        let fstab = tempfile::NamedTempFile::new().unwrap();
        let mock = MockRunner::new(vec![
            fail_output(""),  // findmnt -> not mounted
            // No mount call expected in dry_run
        ]);
        let calls = Arc::clone(&mock.calls);
        let exec = MountExecutor {
            runner: Box::new(mock),
            fstab_path: fstab.path().to_str().unwrap().to_string(),
        };
        let result = exec.execute(
            &make_params(&[
                ("path", "/mnt/data"),
                ("device", "/dev/sdb1"),
                ("fstype", "ext4"),
                ("state", "mounted"),
            ]),
            true,
        );
        assert_eq!(result.action, Action::Created);
        assert!(result.changed);
        assert!(result.error.is_none());
        // Fstab should be unchanged (empty)
        let content = std::fs::read_to_string(fstab.path()).unwrap();
        assert!(content.is_empty(), "dry_run should not modify fstab, got: {}", content);
        // Only findmnt was called
        let calls = calls.lock().unwrap();
        assert_eq!(calls.len(), 1, "dry_run should only call findmnt, got {:?}", *calls);
    }
}
