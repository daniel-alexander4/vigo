// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: BUSL-1.1

//! Kernel module executor — loads/unloads kernel modules idempotently.

use super::{Action, CommandRunner, DefaultRunner, Executor, ResourceResult};
use std::collections::HashMap;
use std::time::Instant;

pub struct KernelModuleExecutor {
    pub runner: Box<dyn CommandRunner>,
}

impl KernelModuleExecutor {
    pub fn new() -> Self {
        Self {
            runner: Box::new(DefaultRunner),
        }
    }

    fn is_loaded(&self, name: &str) -> bool {
        let output = match self.runner.run("lsmod", &[]) {
            Ok(o) => o,
            Err(_) => return false,
        };
        let stdout = String::from_utf8_lossy(&output.stdout);
        let normalized = name.replace('-', "_");
        stdout.lines().any(|line| {
            line.split_whitespace()
                .next()
                .map(|m| m.replace('-', "_") == normalized)
                .unwrap_or(false)
        })
    }

    fn conf_path(name: &str) -> String {
        format!("/etc/modules-load.d/hobnob-{}.conf", name)
    }

    fn is_persisted(name: &str) -> bool {
        std::path::Path::new(&Self::conf_path(name)).exists()
    }
}

impl Executor for KernelModuleExecutor {
    fn execute(&self, params: &HashMap<String, String>, dry_run: bool) -> ResourceResult {
        let start = Instant::now();
        let name = params
            .get("module")
            .or_else(|| params.get("name"))
            .cloned()
            .unwrap_or_default();

        let mut result = ResourceResult::new("kernel_module", &name);

        if name.is_empty() {
            result.error = Some("kernel_module executor: module param is required".into());
            result.duration_ms = start.elapsed().as_millis() as i32;
            return result;
        }

        let state = params.get("state").map(|s| s.as_str()).unwrap_or("present");
        let mod_params = params.get("params").cloned().unwrap_or_default();
        let persist = params.get("persist").map(|s| s.as_str()).unwrap_or("true") == "true";
        let loaded = self.is_loaded(&name);
        let persisted = Self::is_persisted(&name);

        match state {
            "present" => {
                let need_load = !loaded;
                let need_persist = persist && !persisted;

                if !need_load && !need_persist {
                    result.action = Action::None;
                    result.duration_ms = start.elapsed().as_millis() as i32;
                    return result;
                }

                result.action = Action::Created;
                result.changed = true;

                if dry_run {
                    result.duration_ms = start.elapsed().as_millis() as i32;
                    return result;
                }

                if need_load {
                    let mut args = vec![name.as_str()];
                    let param_parts: Vec<&str> = if !mod_params.is_empty() {
                        mod_params.split_whitespace().collect()
                    } else {
                        Vec::new()
                    };
                    args.extend(&param_parts);
                    match self.runner.run("modprobe", &args) {
                        Ok(o) if !o.status.success() => {
                            let stderr = String::from_utf8_lossy(&o.stderr);
                            result.error = Some(format!("modprobe {}: {}", name, stderr));
                            result.duration_ms = start.elapsed().as_millis() as i32;
                            return result;
                        }
                        Err(e) => {
                            result.error = Some(format!("modprobe {}: {}", name, e));
                            result.duration_ms = start.elapsed().as_millis() as i32;
                            return result;
                        }
                        _ => {}
                    }
                }

                if need_persist {
                    let conf = Self::conf_path(&name);
                    let content = if mod_params.is_empty() {
                        format!("{}\n", name)
                    } else {
                        format!("{} {}\n", name, mod_params)
                    };
                    if let Err(e) = std::fs::write(&conf, content) {
                        result.error = Some(format!("write {}: {}", conf, e));
                    }
                }
            }
            "absent" => {
                let need_unload = loaded;
                let need_unpersist = persisted;

                if !need_unload && !need_unpersist {
                    result.action = Action::None;
                    result.duration_ms = start.elapsed().as_millis() as i32;
                    return result;
                }

                result.action = Action::Deleted;
                result.changed = true;

                if dry_run {
                    result.duration_ms = start.elapsed().as_millis() as i32;
                    return result;
                }

                if need_unload {
                    match self.runner.run("modprobe", &["-r", &name]) {
                        Ok(o) if !o.status.success() => {
                            let stderr = String::from_utf8_lossy(&o.stderr);
                            result.error = Some(format!("modprobe -r {}: {}", name, stderr));
                            result.duration_ms = start.elapsed().as_millis() as i32;
                            return result;
                        }
                        Err(e) => {
                            result.error = Some(format!("modprobe -r {}: {}", name, e));
                            result.duration_ms = start.elapsed().as_millis() as i32;
                            return result;
                        }
                        _ => {}
                    }
                }

                if need_unpersist {
                    let conf = Self::conf_path(&name);
                    if let Err(e) = std::fs::remove_file(&conf) {
                        result.error = Some(format!("remove {}: {}", conf, e));
                    }
                }
            }
            "loaded" => {
                if loaded {
                    result.action = Action::None;
                } else {
                    result.action = Action::Created;
                    result.changed = true;
                    if !dry_run {
                        let mut args = vec![name.as_str()];
                        let param_parts: Vec<&str> = if !mod_params.is_empty() {
                            mod_params.split_whitespace().collect()
                        } else {
                            Vec::new()
                        };
                        args.extend(&param_parts);
                        match self.runner.run("modprobe", &args) {
                            Ok(o) if !o.status.success() => {
                                let stderr = String::from_utf8_lossy(&o.stderr);
                                result.error = Some(format!("modprobe {}: {}", name, stderr));
                            }
                            Err(e) => result.error = Some(format!("modprobe {}: {}", name, e)),
                            _ => {}
                        }
                    }
                }
            }
            "unloaded" => {
                if !loaded {
                    result.action = Action::None;
                } else {
                    result.action = Action::Deleted;
                    result.changed = true;
                    if !dry_run {
                        match self.runner.run("modprobe", &["-r", &name]) {
                            Ok(o) if !o.status.success() => {
                                let stderr = String::from_utf8_lossy(&o.stderr);
                                result.error = Some(format!("modprobe -r {}: {}", name, stderr));
                            }
                            Err(e) => result.error = Some(format!("modprobe -r {}: {}", name, e)),
                            _ => {}
                        }
                    }
                }
            }
            _ => {
                result.error = Some(format!("kernel_module executor: unknown state {:?}", state));
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

    #[test]
    fn missing_module_errors() {
        let mock = MockRunner::new(vec![]);
        let exec = KernelModuleExecutor {
            runner: Box::new(mock),
        };
        let result = exec.execute(&make_params(&[]), false);
        assert!(result.error.is_some());
        assert!(result.error.unwrap().contains("module param is required"));
    }

    #[test]
    fn present_already_loaded_and_persisted() {
        // Module is loaded (appears in lsmod) and persist=false so we skip the
        // filesystem check entirely. This avoids needing /etc/modules-load.d/.
        let lsmod = ok_output(
            "Module                  Size  Used by\n\
             br_netfilter           32768  0\n\
             overlay                163840  0\n",
        );
        let mock = MockRunner::new(vec![lsmod]);
        let exec = KernelModuleExecutor {
            runner: Box::new(mock),
        };
        let result = exec.execute(
            &make_params(&[
                ("module", "br_netfilter"),
                ("state", "present"),
                ("persist", "false"),
            ]),
            false,
        );
        assert_eq!(result.action, Action::None);
        assert!(!result.changed);
        assert!(result.error.is_none());
    }

    #[test]
    fn present_loads_module_dry_run() {
        // Module not loaded, dry_run=true => Action::Created, only 1 call (lsmod).
        let lsmod = ok_output(
            "Module                  Size  Used by\n\
             ip_tables              32768  0\n",
        );
        let mock = MockRunner::new(vec![lsmod]);
        let calls = mock.calls.clone();
        let exec = KernelModuleExecutor {
            runner: Box::new(mock),
        };
        let result = exec.execute(
            &make_params(&[
                ("module", "br_netfilter"),
                ("state", "present"),
                ("persist", "false"),
            ]),
            true,
        );
        assert_eq!(result.action, Action::Created);
        assert!(result.changed);

        let calls = calls.lock().unwrap();
        assert_eq!(calls.len(), 1, "dry_run should only call lsmod, not modprobe");
        assert_eq!(calls[0].0, "lsmod");
    }

    #[test]
    fn absent_not_loaded() {
        // Module not loaded (not in lsmod) and not persisted (persist=false
        // so we skip the filesystem check) => Action::None.
        let lsmod = ok_output(
            "Module                  Size  Used by\n\
             ip_tables              32768  0\n",
        );
        let mock = MockRunner::new(vec![lsmod]);
        let exec = KernelModuleExecutor {
            runner: Box::new(mock),
        };
        // Persist is true by default which would check /etc/modules-load.d/
        // which won't exist in test. That's fine — is_persisted returns false
        // for non-existent file, so need_unpersist = false.
        let result = exec.execute(
            &make_params(&[("module", "br_netfilter"), ("state", "absent")]),
            false,
        );
        assert_eq!(result.action, Action::None);
        assert!(!result.changed);
    }

    #[test]
    fn absent_unloads_module_dry_run() {
        // Module loaded, dry_run=true => Action::Deleted, only 1 call (lsmod).
        let lsmod = ok_output(
            "Module                  Size  Used by\n\
             br_netfilter           32768  0\n",
        );
        let mock = MockRunner::new(vec![lsmod]);
        let calls = mock.calls.clone();
        let exec = KernelModuleExecutor {
            runner: Box::new(mock),
        };
        let result = exec.execute(
            &make_params(&[("module", "br_netfilter"), ("state", "absent")]),
            true,
        );
        assert_eq!(result.action, Action::Deleted);
        assert!(result.changed);

        let calls = calls.lock().unwrap();
        assert_eq!(calls.len(), 1, "dry_run should only call lsmod, not modprobe -r");
    }

    #[test]
    fn unknown_state_errors() {
        let lsmod = ok_output("Module                  Size  Used by\n");
        let mock = MockRunner::new(vec![lsmod]);
        let exec = KernelModuleExecutor {
            runner: Box::new(mock),
        };
        let result = exec.execute(
            &make_params(&[("module", "br_netfilter"), ("state", "bogus")]),
            false,
        );
        assert!(result.error.is_some());
        assert!(result.error.unwrap().contains("unknown state"));
    }

    #[test]
    fn loaded_state_only_runtime() {
        // state="loaded" — runtime only, no persistence check.
        // Module not loaded => Action::Created. dry_run so no modprobe call.
        let lsmod = ok_output(
            "Module                  Size  Used by\n\
             ip_tables              32768  0\n",
        );
        let mock = MockRunner::new(vec![lsmod]);
        let calls = mock.calls.clone();
        let exec = KernelModuleExecutor {
            runner: Box::new(mock),
        };
        let result = exec.execute(
            &make_params(&[("module", "br_netfilter"), ("state", "loaded")]),
            true,
        );
        assert_eq!(result.action, Action::Created);
        assert!(result.changed);

        let calls = calls.lock().unwrap();
        assert_eq!(calls.len(), 1);
        assert_eq!(calls[0].0, "lsmod");
    }
}
