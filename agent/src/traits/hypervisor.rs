// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: Apache-2.0

//! Hypervisor trait — host-side libvirt detection.
//!
//! Flip side of `virt`: `virt` answers "am I running *inside* a VM" (guest
//! detection via DMI / cgroup). This collector answers "am I a libvirt
//! *host*" — i.e., can the `vm:` executor reach a libvirt daemon to manage
//! domains? Used by module authors as a `when:` guard so VM modules only
//! land on real hypervisor hosts.
//!
//! Pure presence check on the system-bus and user-bus libvirt sockets. No
//! subprocess spawn, no version probe — keeps the collector cheap and
//! deterministic, per the hot-path doctrine (CLAUDE.md "Prefer sysfs/procfs
//! over subprocesses").

use anyhow::Result;
use serde_json::{json, Value};
use std::path::Path;

/// Where libvirtd binds its read/write Unix sockets. The system bus is the
/// canonical host path; the read-only sibling is enough to prove the daemon
/// is up (the executor's `virsh` calls will use the rw socket).
const LIBVIRT_SOCKETS: &[&str] = &[
    "/var/run/libvirt/libvirt-sock",
    "/run/libvirt/libvirt-sock",
];

pub fn collect() -> Result<Value> {
    let host_is_libvirt = LIBVIRT_SOCKETS.iter().any(|p| Path::new(p).exists());
    Ok(json!({
        "host_is_libvirt": host_is_libvirt,
    }))
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn collect_returns_bool_field() {
        let val = collect().unwrap();
        let obj = val.as_object().unwrap();
        assert!(obj.contains_key("host_is_libvirt"));
        assert!(obj["host_is_libvirt"].is_boolean());
    }
}
