// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: BUSL-1.1

//! Virtualization detection for macOS and BSD.

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
    match std::env::consts::OS {
        "macos" => {
            // kern.hv_vmm_present indicates running in a VM
            if super::sysctl_util::sysctl_u64("kern.hv_vmm_present").unwrap_or(0) == 1 {
                return ("apple-hv".into(), "guest".into());
            }
            // Check for specific hypervisors via model identifier
            let model = super::sysctl_util::sysctl_string("hw.model");
            if model.contains("VMware") {
                return ("vmware".into(), "guest".into());
            }
            if model.contains("VirtualBox") {
                return ("virtualbox".into(), "guest".into());
            }
        }
        "freebsd" => {
            let guest = super::sysctl_util::sysctl_string("kern.vm_guest");
            if !guest.is_empty() && guest != "none" {
                return (guest, "guest".into());
            }
        }
        "illumos" => {
            // Check zonename — "global" means bare metal, anything else is a zone.
            if let Ok(output) = std::process::Command::new("zonename").output() {
                let zone = String::from_utf8_lossy(&output.stdout).trim().to_string();
                if output.status.success() && zone != "global" {
                    return (format!("zone:{}", zone), "guest".into());
                }
            }
        }
        "openbsd" | "netbsd" => {
            // Check hw.product for VM indicators
            let product = super::sysctl_util::sysctl_string("hw.product");
            let product_lower = product.to_lowercase();
            if product_lower.contains("virtualbox") { return ("virtualbox".into(), "guest".into()); }
            if product_lower.contains("vmware") { return ("vmware".into(), "guest".into()); }
            if product_lower.contains("kvm") || product_lower.contains("qemu") { return ("kvm".into(), "guest".into()); }
            if product_lower.contains("virtual machine") { return ("hyperv".into(), "guest".into()); }
        }
        _ => {}
    }

    // Container detection
    if std::path::Path::new("/.dockerenv").exists() {
        return ("docker".into(), "guest".into());
    }

    (String::new(), String::new())
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn test_collect_no_panic() {
        let val = collect().unwrap();
        assert!(val["is_virtual"].is_boolean());
    }
}
