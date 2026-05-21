// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

//! Cloud provider trait collector.
//!
//! Phase 1: detect provider via DMI/sysfs (zero cost, no I/O).
//! Phase 2: if a provider is detected, query its Instance Metadata Service (IMDS)
//! for instance details. IMDS is a link-local HTTP endpoint (169.254.169.254) that
//! responds in <1ms — no external network access required.
//!
//! Supported providers:
//!   Tier 1: AWS (IMDSv2), GCP, Azure
//!   Tier 2: DigitalOcean, Hetzner, Oracle Cloud

use anyhow::Result;
use serde_json::{json, Value};
use std::time::Duration;

/// HTTP timeout for IMDS calls. These are link-local and should respond in <1ms.
/// 2 seconds is generous — if it takes longer, IMDS is blocked or absent.
const IMDS_TIMEOUT: Duration = Duration::from_secs(2);

pub fn collect() -> Result<Value> {
    let provider = detect_provider();

    if provider == "none" {
        return Ok(json!({ "provider": "none" }));
    }

    let mut metadata = serde_json::Map::new();
    metadata.insert("provider".into(), json!(provider));

    // Attempt IMDS collection. If it fails, we still return the provider from DMI.
    match provider {
        "aws" => collect_aws(&mut metadata),
        "gcp" => collect_gcp(&mut metadata),
        "azure" => collect_azure(&mut metadata),
        "digitalocean" => collect_digitalocean(&mut metadata),
        "hetzner" => collect_hetzner(&mut metadata),
        "oracle" => collect_oracle(&mut metadata),
        _ => {} // Provider detected via DMI but no IMDS collector — just return provider name.
    }

    Ok(Value::Object(metadata))
}

// ---------------------------------------------------------------------------
// Provider detection via DMI (sysfs, zero cost)
// ---------------------------------------------------------------------------

fn detect_provider() -> &'static str {
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

// ---------------------------------------------------------------------------
// IMDS HTTP helpers
// ---------------------------------------------------------------------------

fn imds_agent() -> ureq::Agent {
    ureq::Agent::new_with_config(
        ureq::config::Config::builder()
            .timeout_global(Some(IMDS_TIMEOUT))
            .build(),
    )
}

/// Simple GET with optional headers. Returns body as string, or None on any error.
fn imds_get(agent: &ureq::Agent, url: &str, headers: &[(&str, &str)]) -> Option<String> {
    let mut req = agent.get(url);
    for &(k, v) in headers {
        req = req.header(k, v);
    }
    let body = req.call().ok()?.into_body().read_to_string().ok()?;
    let trimmed = body.trim().to_string();
    if trimmed.is_empty() { None } else { Some(trimmed) }
}

/// PUT request (for AWS IMDSv2 token). Returns body as string, or None on any error.
fn imds_put(agent: &ureq::Agent, url: &str, headers: &[(&str, &str)]) -> Option<String> {
    let mut req = agent.put(url);
    for &(k, v) in headers {
        req = req.header(k, v);
    }
    let body = req.send_empty().ok()?.into_body().read_to_string().ok()?;
    let trimmed = body.trim().to_string();
    if trimmed.is_empty() { None } else { Some(trimmed) }
}

fn insert_if_some(map: &mut serde_json::Map<String, Value>, key: &str, val: Option<String>) {
    if let Some(v) = val {
        map.insert(key.into(), json!(v));
    }
}

// ---------------------------------------------------------------------------
// AWS IMDSv2
// ---------------------------------------------------------------------------

fn collect_aws(metadata: &mut serde_json::Map<String, Value>) {
    let agent = imds_agent();
    let base = "http://169.254.169.254/latest";

    // IMDSv2 requires a session token obtained via PUT.
    let token = match imds_put(
        &agent,
        &format!("{base}/api/token"),
        &[("X-aws-ec2-metadata-token-ttl-seconds", "60")],
    ) {
        Some(t) => t,
        None => return, // IMDS unreachable or IMDSv2 disabled — fall back to provider-only.
    };
    let h = [("X-aws-ec2-metadata-token", token.as_str())];

    insert_if_some(metadata, "instance_id", imds_get(&agent, &format!("{base}/meta-data/instance-id"), &h));
    insert_if_some(metadata, "instance_type", imds_get(&agent, &format!("{base}/meta-data/instance-type"), &h));
    insert_if_some(metadata, "region", imds_get(&agent, &format!("{base}/meta-data/placement/region"), &h));
    insert_if_some(metadata, "zone", imds_get(&agent, &format!("{base}/meta-data/placement/availability-zone"), &h));
    insert_if_some(metadata, "image_id", imds_get(&agent, &format!("{base}/meta-data/ami-id"), &h));
    insert_if_some(metadata, "public_ip", imds_get(&agent, &format!("{base}/meta-data/public-ipv4"), &h));

    // Account ID from instance identity document.
    if let Some(doc) = imds_get(&agent, &format!("{base}/dynamic/instance-identity/document"), &h) {
        if let Ok(parsed) = serde_json::from_str::<Value>(&doc) {
            if let Some(acct) = parsed["accountId"].as_str() {
                metadata.insert("account_id".into(), json!(acct));
            }
        }
    }

    // VPC ID from the first network interface.
    if let Some(macs) = imds_get(&agent, &format!("{base}/meta-data/network/interfaces/macs/"), &h) {
        if let Some(mac) = macs.lines().next() {
            let mac = mac.trim_end_matches('/');
            insert_if_some(
                metadata,
                "vpc_id",
                imds_get(&agent, &format!("{base}/meta-data/network/interfaces/macs/{mac}/vpc-id"), &h),
            );
        }
    }

    // Instance tags (requires IMDS tag access to be enabled on the instance).
    if let Some(tag_list) = imds_get(&agent, &format!("{base}/meta-data/tags/instance/"), &h) {
        let mut tags = serde_json::Map::new();
        for tag_name in tag_list.lines() {
            let tag_name = tag_name.trim().trim_end_matches('/');
            if tag_name.is_empty() {
                continue;
            }
            if let Some(val) = imds_get(&agent, &format!("{base}/meta-data/tags/instance/{tag_name}"), &h) {
                tags.insert(tag_name.to_string(), json!(val));
            }
        }
        if !tags.is_empty() {
            metadata.insert("tags".into(), Value::Object(tags));
        }
    }
}

// ---------------------------------------------------------------------------
// GCP Metadata Server
// ---------------------------------------------------------------------------

fn collect_gcp(metadata: &mut serde_json::Map<String, Value>) {
    let agent = imds_agent();
    let base = "http://metadata.google.internal/computeMetadata/v1";
    let h = [("Metadata-Flavor", "Google")];

    insert_if_some(metadata, "instance_id", imds_get(&agent, &format!("{base}/instance/id"), &h));
    insert_if_some(metadata, "instance_name", imds_get(&agent, &format!("{base}/instance/name"), &h));
    insert_if_some(metadata, "account_id", imds_get(&agent, &format!("{base}/project/project-id"), &h));
    insert_if_some(metadata, "image_id", imds_get(&agent, &format!("{base}/instance/image"), &h));

    // Machine type returns "projects/123/machineTypes/e2-medium" — extract the type name.
    if let Some(mt) = imds_get(&agent, &format!("{base}/instance/machine-type"), &h) {
        let instance_type = mt.rsplit('/').next().unwrap_or(&mt).to_string();
        metadata.insert("instance_type".into(), json!(instance_type));
    }

    // Zone returns "projects/123/zones/us-central1-a" — extract zone and derive region.
    if let Some(zone_path) = imds_get(&agent, &format!("{base}/instance/zone"), &h) {
        let zone = zone_path.rsplit('/').next().unwrap_or(&zone_path).to_string();
        // Region is zone minus the trailing "-a"/"-b"/"-c"/etc.
        if let Some(idx) = zone.rfind('-') {
            metadata.insert("region".into(), json!(&zone[..idx]));
        }
        metadata.insert("zone".into(), json!(zone));
    }

    // Network (VPC) from the first interface.
    if let Some(net) = imds_get(&agent, &format!("{base}/instance/network-interfaces/0/network"), &h) {
        let vpc = net.rsplit('/').next().unwrap_or(&net).to_string();
        metadata.insert("vpc_id".into(), json!(vpc));
    }

    // External IP.
    insert_if_some(
        metadata,
        "public_ip",
        imds_get(&agent, &format!("{base}/instance/network-interfaces/0/access-configs/0/external-ip"), &h),
    );

    // Custom metadata as tags.
    if let Some(attrs) = imds_get(&agent, &format!("{base}/instance/attributes/?recursive=true"), &h) {
        if let Ok(parsed) = serde_json::from_str::<Value>(&attrs) {
            if let Some(obj) = parsed.as_object() {
                let mut tags = serde_json::Map::new();
                for (k, v) in obj {
                    if let Some(s) = v.as_str() {
                        tags.insert(k.clone(), json!(s));
                    }
                }
                if !tags.is_empty() {
                    metadata.insert("tags".into(), Value::Object(tags));
                }
            }
        }
    }
}

// ---------------------------------------------------------------------------
// Azure IMDS
// ---------------------------------------------------------------------------

fn collect_azure(metadata: &mut serde_json::Map<String, Value>) {
    let agent = imds_agent();
    let base = "http://169.254.169.254/metadata/instance";
    let h = [("Metadata", "true")];
    let api = "api-version=2021-02-01";

    // Azure returns a single JSON document with everything.
    let url = format!("{base}?{api}");
    let doc = match imds_get(&agent, &url, &h) {
        Some(d) => d,
        None => return,
    };

    let parsed: Value = match serde_json::from_str(&doc) {
        Ok(v) => v,
        Err(_) => return,
    };

    let compute = &parsed["compute"];
    insert_if_some(metadata, "instance_id", compute["vmId"].as_str().map(String::from));
    insert_if_some(metadata, "instance_type", compute["vmSize"].as_str().map(String::from));
    insert_if_some(metadata, "instance_name", compute["name"].as_str().map(String::from));
    insert_if_some(metadata, "region", compute["location"].as_str().map(String::from));
    insert_if_some(metadata, "zone", compute["zone"].as_str().map(String::from));
    insert_if_some(metadata, "account_id", compute["subscriptionId"].as_str().map(String::from));
    insert_if_some(metadata, "resource_group", compute["resourceGroupName"].as_str().map(String::from));
    insert_if_some(metadata, "image_id", compute["storageProfile"]["imageReference"]["id"].as_str().map(String::from));

    // If no explicit image ID, build one from publisher/offer/sku/version.
    if !metadata.contains_key("image_id") {
        let img = &compute["storageProfile"]["imageReference"];
        if let (Some(pub_), Some(offer), Some(sku)) = (
            img["publisher"].as_str(),
            img["offer"].as_str(),
            img["sku"].as_str(),
        ) {
            let ver = img["version"].as_str().unwrap_or("latest");
            metadata.insert("image_id".into(), json!(format!("{pub_}:{offer}:{sku}:{ver}")));
        }
    }

    // Public IP from network interfaces.
    if let Some(ip) = parsed["network"]["interface"][0]["ipv4"]["ipAddress"][0]["publicIpAddress"].as_str() {
        if !ip.is_empty() {
            metadata.insert("public_ip".into(), json!(ip));
        }
    }

    // Tags — Azure returns as an object of key:value pairs.
    if let Some(tags) = compute["tagsList"].as_array() {
        let mut tag_map = serde_json::Map::new();
        for tag in tags {
            if let (Some(name), Some(value)) = (tag["name"].as_str(), tag["value"].as_str()) {
                tag_map.insert(name.to_string(), json!(value));
            }
        }
        if !tag_map.is_empty() {
            metadata.insert("tags".into(), Value::Object(tag_map));
        }
    }
}

// ---------------------------------------------------------------------------
// DigitalOcean Metadata Service
// ---------------------------------------------------------------------------

fn collect_digitalocean(metadata: &mut serde_json::Map<String, Value>) {
    let agent = imds_agent();
    let base = "http://169.254.169.254/metadata/v1";
    let h: &[(&str, &str)] = &[];

    insert_if_some(metadata, "instance_id", imds_get(&agent, &format!("{base}/id"), h));
    insert_if_some(metadata, "instance_name", imds_get(&agent, &format!("{base}/hostname"), h));
    insert_if_some(metadata, "region", imds_get(&agent, &format!("{base}/region"), h));
    insert_if_some(metadata, "image_id", imds_get(&agent, &format!("{base}/image/slug"), h));
    insert_if_some(metadata, "public_ip", imds_get(&agent, &format!("{base}/interfaces/public/0/ipv4/address"), h));

    // DigitalOcean doesn't expose instance type directly via metadata.
    // Droplet size is available via the API but not IMDS.

    // Tags.
    if let Some(tag_str) = imds_get(&agent, &format!("{base}/tags"), h) {
        let mut tags = serde_json::Map::new();
        for tag in tag_str.lines() {
            let tag = tag.trim();
            if !tag.is_empty() {
                tags.insert(tag.to_string(), json!(true));
            }
        }
        if !tags.is_empty() {
            metadata.insert("tags".into(), Value::Object(tags));
        }
    }
}

// ---------------------------------------------------------------------------
// Hetzner Cloud Metadata Service
// ---------------------------------------------------------------------------

fn collect_hetzner(metadata: &mut serde_json::Map<String, Value>) {
    let agent = imds_agent();
    let base = "http://169.254.169.254/hetzner/v1/metadata";
    let h: &[(&str, &str)] = &[];

    // Hetzner returns YAML, but individual endpoints return plain text.
    insert_if_some(metadata, "instance_id", imds_get(&agent, &format!("{base}/instance-id"), h));
    insert_if_some(metadata, "instance_name", imds_get(&agent, &format!("{base}/hostname"), h));
    insert_if_some(metadata, "region", imds_get(&agent, &format!("{base}/availability-zone"), h));
    insert_if_some(metadata, "public_ip", imds_get(&agent, &format!("{base}/public-ipv4"), h));
}

// ---------------------------------------------------------------------------
// Oracle Cloud Infrastructure (OCI) IMDS
// ---------------------------------------------------------------------------

fn collect_oracle(metadata: &mut serde_json::Map<String, Value>) {
    let agent = imds_agent();
    let base = "http://169.254.169.254/opc/v2";
    let h = [("Authorization", "Bearer Oracle")];

    // OCI returns a single JSON document.
    let url = format!("{base}/instance/");
    let doc = match imds_get(&agent, &url, &h) {
        Some(d) => d,
        None => return,
    };

    let parsed: Value = match serde_json::from_str(&doc) {
        Ok(v) => v,
        Err(_) => return,
    };

    insert_if_some(metadata, "instance_id", parsed["id"].as_str().map(String::from));
    insert_if_some(metadata, "instance_name", parsed["displayName"].as_str().map(String::from));
    insert_if_some(metadata, "instance_type", parsed["shape"].as_str().map(String::from));
    insert_if_some(metadata, "region", parsed["canonicalRegionName"].as_str().map(String::from));
    insert_if_some(metadata, "zone", parsed["availabilityDomain"].as_str().map(String::from));
    insert_if_some(metadata, "account_id", parsed["tenantId"].as_str().map(String::from));
    insert_if_some(metadata, "image_id", parsed["image"].as_str().map(String::from));

    // Tags — OCI uses freeformTags and definedTags.
    if let Some(tags) = parsed["freeformTags"].as_object() {
        let mut tag_map = serde_json::Map::new();
        for (k, v) in tags {
            if let Some(s) = v.as_str() {
                tag_map.insert(k.clone(), json!(s));
            }
        }
        if !tag_map.is_empty() {
            metadata.insert("tags".into(), Value::Object(tag_map));
        }
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn test_collect_has_provider() {
        let val = collect().unwrap();
        assert!(val["provider"].is_string());
    }

    #[test]
    fn test_non_cloud_returns_minimal() {
        // On a non-cloud machine, should return just { "provider": "none" }.
        let val = collect().unwrap();
        let obj = val.as_object().unwrap();
        if obj["provider"] == "none" {
            assert_eq!(obj.len(), 1, "non-cloud should only have provider field");
        }
    }
}
