// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

use anyhow::Result;
use serde_json::{json, Value};

pub fn collect() -> Result<Value> {
    let provider = detect_provider();
    Ok(json!({ "provider": provider }))
}

fn detect_provider() -> &'static str {
    // Read DMI fields to detect cloud provider.
    let fields: Vec<String> = ["sys_vendor", "board_vendor", "bios_vendor", "product_name"]
        .iter()
        .filter_map(|f| super::dmi::read_dmi_field(f))
        .map(|s| s.to_lowercase())
        .collect();

    let combined = fields.join(" ");

    if combined.contains("amazon") || combined.contains("ec2") {
        return "aws";
    }
    if combined.contains("google") {
        return "gcp";
    }
    if combined.contains("microsoft") {
        return "azure";
    }
    if combined.contains("digitalocean") {
        return "digitalocean";
    }
    if combined.contains("oracle") {
        return "oracle";
    }
    if combined.contains("hetzner") {
        return "hetzner";
    }
    if combined.contains("vultr") {
        return "vultr";
    }
    if combined.contains("linode") || combined.contains("akamai") {
        return "linode";
    }

    "none"
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn test_collect_has_provider() {
        let val = collect().unwrap();
        assert!(val["provider"].is_string());
    }
}
