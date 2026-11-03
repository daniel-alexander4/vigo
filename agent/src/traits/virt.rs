// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: Apache-2.0

use anyhow::Result;
use serde_json::{json, Value};

pub fn collect() -> Result<Value> {
    let (system, role) = detect_virtualization();
    let is_virtual = !system.is_empty();

    Ok(json!({
        "is_virtual": is_virtual,
        "system": system,
        "role": role,
    }))
}

fn detect_virtualization() -> (String, String) {
    // Check /sys/hypervisor/type (Xen).
    if let Ok(hv) = std::fs::read_to_string("/sys/hypervisor/type") {
        let hv = hv.trim();
        if !hv.is_empty() {
            return (hv.to_string(), "guest".to_string());
        }
    }

    // Check DMI product name for known hypervisors.
    if let Ok(product) = std::fs::read_to_string("/sys/class/dmi/id/product_name") {
        let product = product.trim().to_lowercase();
        if product.contains("virtualbox") {
            return ("virtualbox".into(), "guest".into());
        }
        if product.contains("vmware") {
            return ("vmware".into(), "guest".into());
        }
        if product.contains("kvm") || product.contains("qemu") {
            return ("kvm".into(), "guest".into());
        }
        if product.contains("virtual machine") {
            return ("hyperv".into(), "guest".into());
        }
    }

    // Check sys_vendor for cloud providers.
    if let Ok(vendor) = std::fs::read_to_string("/sys/class/dmi/id/sys_vendor") {
        let vendor = vendor.trim().to_lowercase();
        if vendor.contains("amazon") || vendor.contains("ec2") {
            return ("kvm".into(), "guest".into());
        }
        if vendor.contains("google") {
            return ("kvm".into(), "guest".into());
        }
        if vendor.contains("microsoft") {
            return ("hyperv".into(), "guest".into());
        }
        if vendor.contains("qemu") {
            return ("kvm".into(), "guest".into());
        }
    }

    // Check for container runtimes.
    if std::path::Path::new("/.dockerenv").exists() {
        return ("docker".into(), "guest".into());
    }
    if let Ok(cgroup) = std::fs::read_to_string("/proc/1/cgroup") {
        if cgroup.contains("docker") || cgroup.contains("containerd") {
            return ("docker".into(), "guest".into());
        }
        if cgroup.contains("lxc") {
            return ("lxc".into(), "guest".into());
        }
    }

    (String::new(), String::new())
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn test_collect_returns_expected_keys() {
        let val = collect().unwrap();
        let obj = val.as_object().unwrap();
        assert!(obj.contains_key("is_virtual"));
        assert!(obj.contains_key("system"));
        assert!(obj.contains_key("role"));
        assert!(obj["is_virtual"].is_boolean());
    }
}
