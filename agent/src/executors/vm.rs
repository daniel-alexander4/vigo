// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

//! VM executor — idempotent libvirt domain lifecycle.
//!
//! Same contract as every other executor: declarative spec, check-before-act,
//! idempotent. Models the libvirt domain as the unit of state — name (which
//! is also the idempotency key), vCPU, memory, single disk, single NIC.
//!
//! **Disk source.** `disk_path:` is a local file or `pool:volume`. `artifact:`
//! references a curator artifact (ADR-024, `kind: generic`) and routes
//! through the shared `executor_resolver` — same path `nonrepo_package` /
//! `source_package` use, returning a materialized file under
//! `/var/lib/vigo/artifacts/…`. The two are mutually exclusive.
//!
//! **Overlay model.** Whether the base came from `disk_path:` or `artifact:`,
//! the executor builds a qcow2 overlay at `/var/lib/libvirt/images/<vm>.qcow2`
//! whose backing file is the base. All writes from the running VM land in
//! the overlay; the base stays untouched. Multiple VMs can share one base
//! with effectively zero extra disk cost.
//!
//! **Cloud-init.** Optional `cloud_init_user_data:` (inline string) builds a
//! NoCloud seed ISO via `mkisofs`/`genisoimage` containing `user-data` +
//! `meta-data` and attaches it as a second CD-ROM. Without it, a bare cloud
//! image boots into an unusable state (no SSH keys, no hostname); with it,
//! the standard cloud-init bootstrap runs.

use super::{Action, CommandRunner, DefaultRunner, Executor, ResourceResult};
use sha2::{Digest, Sha256};
use std::collections::HashMap;
use std::path::Path;
use std::time::{Duration, Instant};

const VIRSH: &str = "virsh";

/// Hard timeout on every libvirt call. Long enough for cold-cache inspect on
/// a host with many domains; short enough that a wedged daemon doesn't stall
/// convergence.
const VIRSH_TIMEOUT: Duration = Duration::from_secs(15);

/// Timeout for `qemu-img create`/`info`/`resize` — bounded by metadata work,
/// not the actual disk size, so a short ceiling is fine.
const QEMU_IMG_TIMEOUT: Duration = Duration::from_secs(20);

/// Where overlays + cloud-init seed ISOs land. Standard libvirt directory.
const LIBVIRT_IMAGES_DIR: &str = "/var/lib/libvirt/images";

pub struct VmExecutor {
    pub runner: Box<dyn CommandRunner>,
}

impl VmExecutor {
    pub fn new() -> Self {
        Self {
            runner: Box::new(DefaultRunner),
        }
    }

    fn virsh(&self, args: &[&str]) -> std::io::Result<std::process::Output> {
        self.runner
            .run_niced_timeout(VIRSH, args, 0, VIRSH_TIMEOUT)
    }

    fn qemu_img(&self, args: &[&str]) -> std::io::Result<std::process::Output> {
        self.runner
            .run_niced_timeout("qemu-img", args, 0, QEMU_IMG_TIMEOUT)
    }

    /// Returns the domain's libvirt state (`running` / `shut off` / `paused`
    /// / etc.) if it's defined, else None.
    fn domstate(&self, name: &str) -> Option<String> {
        let output = self.virsh(&["domstate", name]).ok()?;
        if output.status.success() {
            Some(String::from_utf8_lossy(&output.stdout).trim().to_string())
        } else {
            None
        }
    }

    /// Returns the qcow2 overlay's backing-file path (what the overlay was
    /// created against). The drift check compares this to the resolved
    /// desired base path; mismatch means the base changed and the overlay
    /// must be rebuilt.
    fn overlay_backing_file(&self, overlay: &str) -> Option<String> {
        let output = self.qemu_img(&["info", "--output=json", overlay]).ok()?;
        if !output.status.success() {
            return None;
        }
        let json: serde_json::Value =
            serde_json::from_slice(&output.stdout).ok()?;
        // Indexing rather than `.get`: the schema generator scrapes that
        // method-style lookup as if every quoted arg were a module YAML key,
        // and `backing-filename` is qemu-img output rather than a param.
        json["backing-filename"].as_str().map(|s| s.to_string())
    }
}

/// NoCloud seed ISO derived from inline user-data. Filename embeds a short
/// hash of the user-data so an edit forces an idempotency miss and rebuild.
fn seed_iso_path(vm_name: &str, user_data: &str) -> String {
    let mut h = Sha256::new();
    h.update(user_data.as_bytes());
    let hash = hex::encode(&h.finalize()[..4]);
    format!("{LIBVIRT_IMAGES_DIR}/{vm_name}-seed-{hash}.iso")
}

fn overlay_path(vm_name: &str) -> String {
    format!("{LIBVIRT_IMAGES_DIR}/{vm_name}.qcow2")
}

/// Escape XML metacharacters in a value interpolated into the domain XML.
/// Covers the five predefined entities; `'` matters because the domain
/// template uses single-quoted attributes. A tiny local helper is the right
/// altitude here — the input is operator-authored (typo hardening), and a
/// full XML library would force rewriting an otherwise-fine template.
fn xml_escape(s: &str) -> String {
    let mut out = String::with_capacity(s.len());
    for c in s.chars() {
        match c {
            '&' => out.push_str("&amp;"),
            '<' => out.push_str("&lt;"),
            '>' => out.push_str("&gt;"),
            '"' => out.push_str("&quot;"),
            '\'' => out.push_str("&apos;"),
            _ => out.push(c),
        }
    }
    out
}

/// Build a minimal but valid libvirt KVM domain XML. v1 is single disk +
/// single NIC; the disk is always the qcow2 overlay; the seed ISO (if
/// present) attaches as a second virtio CD-ROM.
fn build_domain_xml(
    vm_name: &str,
    vcpu: u32,
    memory_mib: u64,
    overlay_path: &str,
    seed_iso: Option<&str>,
    network_bridge: Option<&str>,
) -> String {
    // Escape the free-text / operator-derived fields. vm_name and
    // network_bridge come straight from the operator; overlay_path and the
    // seed ISO path embed vm_name verbatim. A `<`/`&`/`'` in a name would
    // otherwise produce malformed libvirt domain XML. Numeric fields
    // (memory_mib, vcpu) are parsed to integers upstream and are safe.
    let name_esc = xml_escape(vm_name);
    let overlay_esc = xml_escape(overlay_path);
    let mut xml = String::with_capacity(1024);
    xml.push_str(&format!(
        r#"<domain type='kvm'>
  <name>{name_esc}</name>
  <memory unit='MiB'>{memory_mib}</memory>
  <currentMemory unit='MiB'>{memory_mib}</currentMemory>
  <vcpu placement='static'>{vcpu}</vcpu>
  <os>
    <type arch='x86_64' machine='q35'>hvm</type>
    <boot dev='hd'/>
  </os>
  <features>
    <acpi/>
    <apic/>
  </features>
  <cpu mode='host-passthrough' check='none'/>
  <clock offset='utc'/>
  <on_poweroff>destroy</on_poweroff>
  <on_reboot>restart</on_reboot>
  <on_crash>restart</on_crash>
  <devices>
    <emulator>/usr/bin/qemu-system-x86_64</emulator>
    <disk type='file' device='disk'>
      <driver name='qemu' type='qcow2'/>
      <source file='{overlay_esc}'/>
      <target dev='vda' bus='virtio'/>
    </disk>
"#
    ));
    if let Some(seed) = seed_iso {
        let seed_esc = xml_escape(seed);
        xml.push_str(&format!(
            "    <disk type='file' device='cdrom'>\n\
             \x20     <driver name='qemu' type='raw'/>\n\
             \x20     <source file='{seed_esc}'/>\n\
             \x20     <target dev='sda' bus='sata'/>\n\
             \x20     <readonly/>\n\
             \x20   </disk>\n"
        ));
    }
    if let Some(bridge) = network_bridge {
        let bridge_esc = xml_escape(bridge);
        xml.push_str(&format!(
            "    <interface type='bridge'>\n\
             \x20     <source bridge='{bridge_esc}'/>\n\
             \x20     <model type='virtio'/>\n\
             \x20   </interface>\n"
        ));
    }
    xml.push_str(
        "    <serial type='pty'><target port='0'/></serial>\n\
         \x20  <console type='pty'><target type='serial' port='0'/></console>\n\
         \x20  <graphics type='vnc' port='-1' autoport='yes' listen='127.0.0.1'/>\n\
         \x20</devices>\n\
         </domain>\n",
    );
    xml
}

impl Executor for VmExecutor {
    fn execute(&self, params: &HashMap<String, String>, dry_run: bool) -> ResourceResult {
        let start = Instant::now();
        let name = params.get("vm_name").cloned().unwrap_or_default();

        let mut result = ResourceResult::new("vm", &name);

        if name.is_empty() {
            result.error = Some("vm executor: vm_name param is required".into());
            result.duration_ms = start.elapsed().as_millis() as i32;
            return result;
        }

        let state = params.get("state").map(|s| s.as_str()).unwrap_or("running");

        // --- Parse + validate params up front. -----------------------------

        let disk_path = params.get("disk_path").cloned().unwrap_or_default();
        let artifact = params.get("artifact").cloned().unwrap_or_default();
        if !disk_path.is_empty() && !artifact.is_empty() {
            result.error = Some(
                "vm executor: `disk_path:` and `artifact:` are mutually exclusive".into(),
            );
            result.duration_ms = start.elapsed().as_millis() as i32;
            return result;
        }

        let memory_mib: u64 = match params.get("memory_mib").and_then(|s| s.parse().ok()) {
            Some(v) if v > 0 => v,
            _ if state == "absent" => 0, // unused on the absent path
            _ => {
                result.error =
                    Some("vm executor: memory_mib param is required (positive integer)".into());
                result.duration_ms = start.elapsed().as_millis() as i32;
                return result;
            }
        };
        let vcpu: u32 = match params.get("vcpu").and_then(|s| s.parse().ok()) {
            Some(v) if v > 0 => v,
            _ if state == "absent" => 0,
            _ => {
                result.error = Some("vm executor: vcpu param is required (positive integer)".into());
                result.duration_ms = start.elapsed().as_millis() as i32;
                return result;
            }
        };

        let user_data = params.get("cloud_init_user_data").cloned().unwrap_or_default();
        let network_bridge = params.get("network_bridge").cloned();
        let disk_size_gib: Option<u64> = params
            .get("disk_size_gib")
            .and_then(|s| s.parse().ok())
            .filter(|n: &u64| *n > 0);

        // --- Observe current state. ----------------------------------------

        let cur_state = self.domstate(&name);
        let exists = cur_state.is_some();
        let running = cur_state.as_deref() == Some("running");

        // --- absent: short-circuit, no disk work. --------------------------

        if state == "absent" {
            if !exists {
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
            if running {
                let _ = self.virsh(&["destroy", &name]);
            }
            // --remove-all-storage cleans the managed overlay + seed ISO.
            if let Err(e) = run_or_msg(self.virsh(&["undefine", "--remove-all-storage", &name])) {
                result.error = Some(format!("virsh undefine: {e}"));
            }
            result.duration_ms = start.elapsed().as_millis() as i32;
            return result;
        }

        if !matches!(state, "running" | "stopped" | "defined") {
            result.error = Some(format!("vm executor: unknown state {state:?}"));
            result.duration_ms = start.elapsed().as_millis() as i32;
            return result;
        }

        // --- Resolve the base disk: either disk_path or curator artifact. --

        let base_path: String = if !artifact.is_empty() {
            let resolver = match crate::swarm::curator::executor_resolver::build_resolver_from_agent_config() {
                Ok(r) => r,
                Err(e) => {
                    result.error = Some(format!(
                        "vm executor: cannot construct curator resolver: {e:#}"
                    ));
                    result.duration_ms = start.elapsed().as_millis() as i32;
                    return result;
                }
            };
            match crate::swarm::curator::executor_resolver::resolve_artifact_param(&artifact, &resolver) {
                Ok(r) => r.path.to_string_lossy().to_string(),
                Err(e) => {
                    result.error = Some(format!(
                        "vm executor: resolving `artifact: {artifact}`: {e:#}"
                    ));
                    result.duration_ms = start.elapsed().as_millis() as i32;
                    return result;
                }
            }
        } else if !disk_path.is_empty() {
            disk_path.clone()
        } else {
            result.error = Some(
                "vm executor: one of `disk_path:` or `artifact:` is required".into(),
            );
            result.duration_ms = start.elapsed().as_millis() as i32;
            return result;
        };

        // --- Decide what changes to make. ----------------------------------
        //
        // Drift dimensions:
        //   1. Domain is defined.
        //   2. Overlay exists and its backing file matches `base_path`.
        //   3. Seed ISO matches the cloud-init user-data hash (if any).
        //   4. Running state matches desired.
        //
        // A 1/2/3 mismatch forces undefine + rebuild. A 4-only mismatch is
        // a cheap start/destroy.

        let overlay = overlay_path(&name);
        let overlay_ok = Path::new(&overlay).exists()
            && self.overlay_backing_file(&overlay).as_deref() == Some(base_path.as_str());

        let desired_seed = if user_data.is_empty() {
            None
        } else {
            Some(seed_iso_path(&name, &user_data))
        };
        let seed_ok = match &desired_seed {
            Some(p) => Path::new(p).exists(),
            None => true,
        };

        let structural_ok = exists && overlay_ok && seed_ok;

        if structural_ok {
            // Domain + storage match desired. Only running-state may need work.
            match (state, running) {
                ("running", true) | ("stopped", false) | ("defined", _) => {
                    result.action = Action::None;
                    result.duration_ms = start.elapsed().as_millis() as i32;
                    return result;
                }
                ("running", false) => {
                    result.action = Action::Updated;
                    result.changed = true;
                    if dry_run {
                        result.duration_ms = start.elapsed().as_millis() as i32;
                        return result;
                    }
                    if let Err(e) = run_or_msg(self.virsh(&["start", &name])) {
                        result.error = Some(format!("virsh start: {e}"));
                    }
                    result.duration_ms = start.elapsed().as_millis() as i32;
                    return result;
                }
                ("stopped", true) => {
                    result.action = Action::Updated;
                    result.changed = true;
                    if dry_run {
                        result.duration_ms = start.elapsed().as_millis() as i32;
                        return result;
                    }
                    if let Err(e) = run_or_msg(self.virsh(&["shutdown", &name])) {
                        result.error = Some(format!("virsh shutdown: {e}"));
                    }
                    result.duration_ms = start.elapsed().as_millis() as i32;
                    return result;
                }
                _ => unreachable!(),
            }
        }

        // --- Structural rebuild required. ----------------------------------

        if exists {
            result.action = Action::Updated;
        } else {
            result.action = Action::Created;
        }
        result.changed = true;

        if dry_run {
            result.duration_ms = start.elapsed().as_millis() as i32;
            return result;
        }

        if exists {
            if running {
                let _ = self.virsh(&["destroy", &name]);
            }
            // Keep storage; we rebuild the overlay ourselves below so we
            // own the cache semantics. (`--remove-all-storage` would also
            // erase manually-staged disks the operator wants kept.)
            let _ = self.virsh(&["undefine", &name]);
        }

        // (Re)build the qcow2 overlay against the base.
        if !overlay_ok {
            // Remove stale overlay if present.
            if Path::new(&overlay).exists() {
                let _ = std::fs::remove_file(&overlay);
            }
            let mut create_args = vec![
                "create".to_string(),
                "-F".to_string(),
                "qcow2".to_string(),
                "-b".to_string(),
                base_path.clone(),
                "-f".to_string(),
                "qcow2".to_string(),
                overlay.clone(),
            ];
            if let Some(g) = disk_size_gib {
                create_args.push(format!("{g}G"));
            }
            let refs: Vec<&str> = create_args.iter().map(|s| s.as_str()).collect();
            if let Err(e) = run_or_msg(self.qemu_img(&refs)) {
                result.error = Some(format!("qemu-img create: {e}"));
                result.duration_ms = start.elapsed().as_millis() as i32;
                return result;
            }
        }

        // (Re)build the cloud-init seed ISO if user-data is set.
        if let Some(seed) = &desired_seed {
            if !Path::new(seed).exists() {
                if let Err(e) = self.build_seed_iso(&name, &user_data, seed) {
                    result.error = Some(format!("cloud-init seed: {e}"));
                    result.duration_ms = start.elapsed().as_millis() as i32;
                    return result;
                }
            }
        }

        // Define + (maybe) start.
        let xml = build_domain_xml(
            &name,
            vcpu,
            memory_mib,
            &overlay,
            desired_seed.as_deref(),
            network_bridge.as_deref(),
        );
        if let Err(e) = self.virsh_define(&xml) {
            result.error = Some(format!("virsh define: {e}"));
            result.duration_ms = start.elapsed().as_millis() as i32;
            return result;
        }

        if state == "running" {
            if let Err(e) = run_or_msg(self.virsh(&["start", &name])) {
                result.error = Some(format!("virsh start: {e}"));
            }
        }

        result.duration_ms = start.elapsed().as_millis() as i32;
        result
    }
}

impl VmExecutor {
    /// Build a NoCloud seed ISO containing user-data + meta-data. cloud-init
    /// reads instance-id from meta-data, so vary it per-VM. `mkisofs` and
    /// `genisoimage` produce byte-identical output for this; pick whichever
    /// is on PATH.
    fn build_seed_iso(&self, vm_name: &str, user_data: &str, out_path: &str) -> Result<(), String> {
        let dir = tempfile::tempdir()
            .map_err(|e| format!("tempdir: {e}"))?;
        let dir_path = dir.path();
        std::fs::write(dir_path.join("user-data"), user_data)
            .map_err(|e| format!("write user-data: {e}"))?;
        let meta = format!("instance-id: {vm_name}\nlocal-hostname: {vm_name}\n");
        std::fs::write(dir_path.join("meta-data"), meta)
            .map_err(|e| format!("write meta-data: {e}"))?;

        let user_p = dir_path.join("user-data").to_string_lossy().to_string();
        let meta_p = dir_path.join("meta-data").to_string_lossy().to_string();

        let args = vec![
            "-output", out_path,
            "-volid", "cidata",
            "-joliet", "-rock",
            &user_p, &meta_p,
        ];

        // mkisofs first; fall back to genisoimage. Both accept identical flags.
        let res = self
            .runner
            .run_niced_timeout("mkisofs", &args, 0, QEMU_IMG_TIMEOUT)
            .or_else(|_| {
                self.runner
                    .run_niced_timeout("genisoimage", &args, 0, QEMU_IMG_TIMEOUT)
            });

        match res {
            Ok(o) if o.status.success() => Ok(()),
            Ok(o) => Err(String::from_utf8_lossy(&o.stderr).into_owned()),
            Err(e) => Err(format!(
                "neither mkisofs nor genisoimage is on PATH (install genisoimage or libisofs): {e}"
            )),
        }
    }

    /// `virsh define` reads an XML domain definition from a file argument.
    /// We feed XML via stdin to avoid a tempfile dance — `virsh define
    /// /dev/stdin` is well-supported.
    fn virsh_define(&self, xml: &str) -> Result<(), String> {
        let output = self
            .runner
            .run_with_stdin_niced_timeout(xml.as_bytes(), VIRSH, &["define", "/dev/stdin"], 0, VIRSH_TIMEOUT)
            .map_err(|e| e.to_string())?;
        if !output.status.success() {
            return Err(String::from_utf8_lossy(&output.stderr).into_owned());
        }
        Ok(())
    }
}

/// Collapse `Result<Output>` from a CommandRunner call into "ok or stderr
/// message" for the executor's error field.
fn run_or_msg(res: std::io::Result<std::process::Output>) -> Result<(), String> {
    match res {
        Ok(o) if o.status.success() => Ok(()),
        Ok(o) => Err(String::from_utf8_lossy(&o.stderr).trim().to_string()),
        Err(e) => Err(e.to_string()),
    }
}

#[cfg(test)]
#[cfg(unix)]
mod tests {
    use super::*;
    use crate::executors::test_support::mock::*;
    use std::sync::Arc;

    fn base_params() -> Vec<(&'static str, &'static str)> {
        vec![
            ("vm_name", "web01"),
            ("memory_mib", "2048"),
            ("vcpu", "2"),
            ("disk_path", "/var/lib/libvirt/images/ubuntu-base.qcow2"),
            ("network_bridge", "br0"),
        ]
    }

    #[test]
    fn missing_name_errors() {
        let exec = VmExecutor {
            runner: Box::new(MockRunner::empty()),
        };
        let result = exec.execute(&make_params(&[]), false);
        assert!(result.error.as_deref().unwrap().contains("vm_name"));
    }

    #[test]
    fn disk_path_and_artifact_mutually_exclusive() {
        let exec = VmExecutor {
            runner: Box::new(MockRunner::empty()),
        };
        let mut p = base_params();
        p.push(("artifact", "alexander4/ubuntu:1.0"));
        let result = exec.execute(&make_params(&p), false);
        assert!(
            result
                .error
                .as_deref()
                .unwrap()
                .contains("mutually exclusive")
        );
    }

    #[test]
    fn neither_disk_path_nor_artifact_errors() {
        let mock = MockRunner::new(vec![fail_output("error: failed to get domain")]);
        let exec = VmExecutor {
            runner: Box::new(mock),
        };
        let result = exec.execute(
            &make_params(&[
                ("vm_name", "web01"),
                ("memory_mib", "2048"),
                ("vcpu", "2"),
            ]),
            false,
        );
        assert!(result.error.as_deref().unwrap().contains("one of"));
    }

    #[test]
    fn memory_required() {
        let exec = VmExecutor {
            runner: Box::new(MockRunner::empty()),
        };
        let result = exec.execute(
            &make_params(&[("vm_name", "web01"), ("vcpu", "2"), ("disk_path", "/x")]),
            false,
        );
        assert!(result.error.as_deref().unwrap().contains("memory_mib"));
    }

    #[test]
    fn absent_noop_when_missing() {
        // virsh domstate fails (domain not defined). state: absent → None.
        let mock = MockRunner::new(vec![fail_output("error: failed to get domain")]);
        let exec = VmExecutor {
            runner: Box::new(mock),
        };
        let result = exec.execute(
            &make_params(&[("vm_name", "web01"), ("state", "absent")]),
            false,
        );
        assert_eq!(result.action, Action::None);
        assert!(!result.changed);
        assert!(result.error.is_none());
    }

    #[test]
    fn absent_destroys_and_undefines_running() {
        // domstate=running → destroy → undefine.
        let mock = MockRunner::new(vec![
            ok_output("running\n"),
            ok_output(""),
            ok_output(""),
        ]);
        let calls = Arc::clone(&mock.calls);
        let exec = VmExecutor {
            runner: Box::new(mock),
        };
        let result = exec.execute(
            &make_params(&[("vm_name", "web01"), ("state", "absent")]),
            false,
        );
        assert_eq!(result.action, Action::Deleted);
        assert!(result.changed);
        assert!(result.error.is_none());
        let calls = calls.lock().unwrap();
        assert_eq!(calls.len(), 3);
        assert_eq!(calls[0].1[0], "domstate");
        assert_eq!(calls[1].1[0], "destroy");
        assert_eq!(calls[2].1[0], "undefine");
        assert!(calls[2].1.iter().any(|a| a == "--remove-all-storage"));
    }

    #[test]
    fn dry_run_does_not_act() {
        // Domain doesn't exist; running requested. Dry-run skips create/start.
        let mock = MockRunner::new(vec![fail_output("error: failed to get domain")]);
        let calls = Arc::clone(&mock.calls);
        let exec = VmExecutor {
            runner: Box::new(mock),
        };
        let result = exec.execute(&make_params(&base_params()), true);
        assert_eq!(result.action, Action::Created);
        assert!(result.changed);
        assert!(result.error.is_none(), "unexpected: {:?}", result.error);
        let calls = calls.lock().unwrap();
        assert_eq!(calls.len(), 1, "dry_run should only call domstate");
    }

    #[test]
    fn unknown_state_errors() {
        let mock = MockRunner::new(vec![fail_output("error: failed to get domain")]);
        let exec = VmExecutor {
            runner: Box::new(mock),
        };
        let mut p = base_params();
        p.push(("state", "present"));
        let result = exec.execute(&make_params(&p), false);
        assert!(result.error.as_deref().unwrap().contains("unknown state"));
    }

    #[test]
    fn domain_xml_contains_required_elements() {
        let xml = build_domain_xml(
            "web01",
            4,
            4096,
            "/var/lib/libvirt/images/web01.qcow2",
            Some("/var/lib/libvirt/images/web01-seed-abc.iso"),
            Some("br0"),
        );
        assert!(xml.contains("<name>web01</name>"));
        assert!(xml.contains("<memory unit='MiB'>4096</memory>"));
        assert!(xml.contains("<vcpu placement='static'>4</vcpu>"));
        assert!(xml.contains("/var/lib/libvirt/images/web01.qcow2"));
        assert!(xml.contains("/var/lib/libvirt/images/web01-seed-abc.iso"));
        assert!(xml.contains("<source bridge='br0'/>"));
        assert!(xml.contains("model type='virtio'"));
    }

    #[test]
    fn domain_xml_escapes_special_chars() {
        // A vm_name / bridge with XML metacharacters must be escaped, not
        // emitted raw (which would corrupt the domain definition).
        let xml = build_domain_xml(
            "we<b>&'01",
            2,
            2048,
            "/img/we&b.qcow2",
            None,
            Some("br&'0"),
        );
        assert!(
            xml.contains("<name>we&lt;b&gt;&amp;&apos;01</name>"),
            "name not escaped: {xml}"
        );
        assert!(
            xml.contains("<source file='/img/we&amp;b.qcow2'/>"),
            "overlay path not escaped: {xml}"
        );
        assert!(
            xml.contains("<source bridge='br&amp;&apos;0'/>"),
            "bridge not escaped: {xml}"
        );
        // The raw, unescaped name must not leak through.
        assert!(!xml.contains("<name>we<b>"), "raw metacharacters leaked: {xml}");
    }

    #[test]
    fn domain_xml_omits_network_when_absent() {
        let xml = build_domain_xml(
            "web01",
            1,
            512,
            "/var/lib/libvirt/images/web01.qcow2",
            None,
            None,
        );
        assert!(!xml.contains("<interface"));
        assert!(!xml.contains("cdrom"));
    }

    #[test]
    fn seed_iso_path_hash_changes_with_user_data() {
        let a = seed_iso_path("web01", "#cloud-config\nhostname: web01");
        let b = seed_iso_path("web01", "#cloud-config\nhostname: web02");
        assert_ne!(a, b, "different user-data must produce different ISO paths");
    }

    #[test]
    fn seed_iso_path_stable_for_same_user_data() {
        let a = seed_iso_path("web01", "#cloud-config\nhostname: web01");
        let b = seed_iso_path("web01", "#cloud-config\nhostname: web01");
        assert_eq!(a, b, "identical user-data must produce the same ISO path");
    }
}
