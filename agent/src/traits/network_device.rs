// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

//! Network device trait collection over SSH.
//!
//! Collects facts from Cisco IOS/NX-OS and Juniper Junos devices by running
//! show commands and parsing the output. Called during the proxy convergence
//! loop — not a standard `CollectorKind`.

use crate::transport::ssh::SshSession;
use anyhow::Result;
use serde_json::{json, Value};
use std::time::Duration;
use tracing::{debug, warn};

const TRAIT_TIMEOUT: Duration = Duration::from_secs(15);

/// Collect device traits over an SSH session.
pub async fn collect_device_traits(session: &SshSession, device_type: &str) -> Value {
    let result = match device_type {
        "cisco_ios" | "cisco_nxos" => collect_cisco(session, device_type).await,
        "juniper_junos" => collect_junos(session).await,
        _ => {
            warn!(device_type, "unsupported device type for trait collection");
            Ok(json!({}))
        }
    };

    match result {
        Ok(v) => v,
        Err(e) => {
            warn!(device_type, error = %e, "device trait collection failed");
            json!({})
        }
    }
}

/// Collect traits from Cisco IOS/NX-OS.
async fn collect_cisco(session: &SshSession, device_type: &str) -> Result<Value> {
    let mut device = serde_json::Map::new();
    device.insert("type".into(), json!(device_type));

    // show version
    match session.exec("show version", TRAIT_TIMEOUT).await {
        Ok(out) => {
            let parsed = parse_cisco_show_version(&out.stdout);
            for (k, v) in parsed {
                device.insert(k, v);
            }
        }
        Err(e) => {
            debug!(error = %e, "failed to run 'show version'");
        }
    }

    // show interfaces status
    match session.exec("show interfaces status", TRAIT_TIMEOUT).await {
        Ok(out) => {
            device.insert("interfaces".into(), parse_cisco_show_interfaces(&out.stdout));
        }
        Err(e) => {
            debug!(error = %e, "failed to run 'show interfaces status'");
        }
    }

    // show vlan brief
    match session.exec("show vlan brief", TRAIT_TIMEOUT).await {
        Ok(out) => {
            device.insert("vlans".into(), parse_cisco_show_vlan(&out.stdout));
        }
        Err(e) => {
            debug!(error = %e, "failed to run 'show vlan brief'");
        }
    }

    // show ip route summary
    match session.exec("show ip route summary", TRAIT_TIMEOUT).await {
        Ok(out) => {
            device.insert("routes".into(), parse_cisco_route_summary(&out.stdout));
        }
        Err(e) => {
            debug!(error = %e, "failed to run 'show ip route summary'");
        }
    }

    Ok(json!({ "device": Value::Object(device) }))
}

/// Collect traits from Juniper Junos.
async fn collect_junos(session: &SshSession) -> Result<Value> {
    let mut device = serde_json::Map::new();
    device.insert("type".into(), json!("juniper_junos"));

    // show version
    match session.exec("show version", TRAIT_TIMEOUT).await {
        Ok(out) => {
            let parsed = parse_junos_show_version(&out.stdout);
            for (k, v) in parsed {
                device.insert(k, v);
            }
        }
        Err(e) => {
            debug!(error = %e, "failed to run 'show version'");
        }
    }

    // show chassis hardware
    match session.exec("show chassis hardware", TRAIT_TIMEOUT).await {
        Ok(out) => {
            device.insert("modules".into(), parse_junos_chassis_hardware(&out.stdout));
        }
        Err(e) => {
            debug!(error = %e, "failed to run 'show chassis hardware'");
        }
    }

    // show interfaces terse
    match session.exec("show interfaces terse", TRAIT_TIMEOUT).await {
        Ok(out) => {
            device.insert("interfaces".into(), parse_junos_show_interfaces(&out.stdout));
        }
        Err(e) => {
            debug!(error = %e, "failed to run 'show interfaces terse'");
        }
    }

    // show route summary
    match session.exec("show route summary", TRAIT_TIMEOUT).await {
        Ok(out) => {
            device.insert("routes".into(), parse_junos_route_summary(&out.stdout));
        }
        Err(e) => {
            debug!(error = %e, "failed to run 'show route summary'");
        }
    }

    Ok(json!({ "device": Value::Object(device) }))
}

// ---------------------------------------------------------------------------
// Cisco parsers
// ---------------------------------------------------------------------------

/// Parse Cisco `show version` output.
///
/// Extracts: model, firmware, serial, uptime, hostname.
pub fn parse_cisco_show_version(output: &str) -> Vec<(String, Value)> {
    let mut traits = Vec::new();

    for line in output.lines() {
        let line = line.trim();

        // Cisco IOS: "Cisco IOS Software, ... Version 16.12.4, ..."
        // Cisco NX-OS: "Cisco Nexus Operating System (NX-OS) Software ... version 9.3(8)"
        if line.contains("Version ") || line.contains("version ") {
            if let Some(ver) = extract_version(line) {
                traits.push(("firmware".into(), json!(ver)));
            }
        }

        // "cisco WS-C3750X-48T" or "cisco Nexus9000 ..."
        if line.starts_with("cisco ") && traits.iter().all(|(k, _)| k != "model") {
            let model = line
                .strip_prefix("cisco ")
                .unwrap_or(line)
                .split_whitespace()
                .next()
                .unwrap_or("")
                .to_string();
            if !model.is_empty() {
                traits.push(("model".into(), json!(model)));
            }
        }

        // "System serial number : FOC1234ABCD"
        if line.contains("serial number") || line.contains("Serial Number") {
            if let Some(serial) = line.split(':').nth(1).or_else(|| line.split('=').nth(1)) {
                let serial = serial.trim().to_string();
                if !serial.is_empty() {
                    traits.push(("serial".into(), json!(serial)));
                }
            }
        }

        // "hostname uptime is 5 days, 3 hours, 22 minutes"
        if line.contains("uptime is ") {
            if let Some(uptime) = line.split("uptime is ").nth(1) {
                traits.push(("uptime".into(), json!(uptime.trim())));
                // First word before "uptime" is hostname.
                if let Some(hn) = line.split_whitespace().next() {
                    traits.push(("hostname".into(), json!(hn)));
                }
            }
        }
    }

    traits
}

/// Parse Cisco `show interfaces status` output.
///
/// Format: Port  Name  Status  Vlan  Duplex  Speed  Type
pub fn parse_cisco_show_interfaces(output: &str) -> Value {
    let mut interfaces = Vec::new();
    let mut header_found = false;

    for line in output.lines() {
        let line = line.trim();
        if line.is_empty() {
            continue;
        }

        // Detect header line.
        if line.starts_with("Port") && line.contains("Status") {
            header_found = true;
            continue;
        }

        // Skip separator lines.
        if line.starts_with("---") || line.starts_with("===") {
            continue;
        }

        if header_found {
            let parts: Vec<&str> = line.split_whitespace().collect();
            if parts.len() >= 4 {
                let mut iface = serde_json::Map::new();
                iface.insert("name".into(), json!(parts[0]));
                // Status is typically "connected" or "notconnect" etc.
                // Name field may be multi-word or empty, so status is typically
                // the field that matches known values. We'll parse conservatively.
                let status = find_interface_status(&parts[1..]);
                iface.insert("status".into(), json!(status.0));
                if let Some(vlan) = status.1 {
                    iface.insert("vlan".into(), json!(vlan));
                }
                if let Some(speed) = status.2 {
                    iface.insert("speed".into(), json!(speed));
                }
                interfaces.push(Value::Object(iface));
            }
        }
    }

    json!(interfaces)
}

/// Find interface status from remaining fields after port name.
fn find_interface_status<'a>(parts: &'a [&'a str]) -> (&'a str, Option<&'a str>, Option<&'a str>) {
    let known_statuses = [
        "connected",
        "notconnect",
        "disabled",
        "err-disabled",
        "monitoring",
        "up",
        "down",
    ];

    for (i, &p) in parts.iter().enumerate() {
        if known_statuses.contains(&p) {
            let vlan = parts.get(i + 1).copied();
            // Speed is typically 2 fields after status (after duplex).
            let speed = parts.get(i + 3).copied();
            return (p, vlan, speed);
        }
    }

    (parts.first().copied().unwrap_or("unknown"), None, None)
}

/// Parse Cisco `show vlan brief` output.
///
/// Format: VLAN  Name  Status  Ports
pub fn parse_cisco_show_vlan(output: &str) -> Value {
    let mut vlans = Vec::new();
    let mut header_found = false;

    for line in output.lines() {
        let line = line.trim();
        if line.is_empty() {
            continue;
        }

        if line.starts_with("VLAN") && line.contains("Name") {
            header_found = true;
            continue;
        }
        if line.starts_with("---") || line.starts_with("===") {
            continue;
        }

        if header_found {
            let parts: Vec<&str> = line.split_whitespace().collect();
            if parts.len() >= 3 {
                // First field should be a number (VLAN ID).
                if parts[0].parse::<u32>().is_ok() {
                    let mut vlan = serde_json::Map::new();
                    vlan.insert("id".into(), json!(parts[0]));
                    vlan.insert("name".into(), json!(parts[1]));
                    vlan.insert("status".into(), json!(parts[2]));
                    if parts.len() > 3 {
                        let ports_str = parts[3..].join(" ");
                        let port_list: Vec<String> = ports_str
                            .split(',')
                            .map(|p| p.trim().to_string())
                            .filter(|p| !p.is_empty())
                            .collect();
                        if !port_list.is_empty() {
                            vlan.insert("ports".into(), json!(port_list));
                        }
                    }
                    vlans.push(Value::Object(vlan));
                }
            }
        }
    }

    json!(vlans)
}

/// Parse Cisco `show ip route summary` output.
fn parse_cisco_route_summary(output: &str) -> Value {
    let mut routes = serde_json::Map::new();

    for line in output.lines() {
        let line = line.trim();
        // "Total number of prefixes 42"
        if line.contains("Total") && line.contains("prefixes") {
            if let Some(n) = line.split_whitespace().last().and_then(|s| s.parse::<u64>().ok()) {
                routes.insert("total".into(), json!(n));
            }
        }
        // "connected    5 subnets"
        if line.starts_with("connected") {
            if let Some(n) = extract_first_number(line) {
                routes.insert("connected".into(), json!(n));
            }
        }
        // "static       3 subnets"
        if line.starts_with("static") {
            if let Some(n) = extract_first_number(line) {
                routes.insert("static".into(), json!(n));
            }
        }
        // "bgp 65000"
        if line.starts_with("bgp") {
            if let Some(n) = extract_first_number(line) {
                routes.insert("bgp".into(), json!(n));
            }
        }
    }

    Value::Object(routes)
}

// ---------------------------------------------------------------------------
// Juniper Junos parsers
// ---------------------------------------------------------------------------

/// Parse Junos `show version` output.
///
/// Format varies, but key lines:
///   Hostname: edge-rtr01
///   Model: mx240
///   Junos: 21.4R3-S1
pub fn parse_junos_show_version(output: &str) -> Vec<(String, Value)> {
    let mut traits = Vec::new();

    for line in output.lines() {
        let line = line.trim();

        if let Some(hostname) = line.strip_prefix("Hostname:") {
            traits.push(("hostname".into(), json!(hostname.trim())));
        }
        if let Some(model) = line.strip_prefix("Model:") {
            traits.push(("model".into(), json!(model.trim())));
        }
        if let Some(version) = line.strip_prefix("Junos:") {
            traits.push(("firmware".into(), json!(version.trim())));
        }
        // Alternative: "JUNOS Base OS boot [21.4R3-S1]"
        if line.starts_with("JUNOS") && line.contains('[') {
            if let Some(ver) = line.split('[').nth(1).and_then(|s| s.strip_suffix(']')) {
                if traits.iter().all(|(k, _)| k != "firmware") {
                    traits.push(("firmware".into(), json!(ver)));
                }
            }
        }
    }

    traits
}

/// Parse Junos `show chassis hardware` output.
///
/// Format: Item  Version  Part number  Serial number  Description
pub fn parse_junos_chassis_hardware(output: &str) -> Value {
    let mut modules = Vec::new();
    let mut header_found = false;

    for line in output.lines() {
        let line = line.trim();
        if line.is_empty() {
            continue;
        }

        if line.starts_with("Item") || line.starts_with("Hardware inventory") {
            header_found = true;
            continue;
        }

        if header_found {
            let parts: Vec<&str> = line.split_whitespace().collect();
            if parts.len() >= 2 {
                let mut module = serde_json::Map::new();
                module.insert("name".into(), json!(parts[0]));
                // Last field is typically the description if long enough.
                if parts.len() >= 5 {
                    module.insert("serial".into(), json!(parts[3]));
                    module.insert(
                        "description".into(),
                        json!(parts[4..].join(" ")),
                    );
                }
                modules.push(Value::Object(module));
            }
        }
    }

    json!(modules)
}

/// Parse Junos `show interfaces terse` output.
///
/// Format: Interface  Admin  Link  Proto  Local  Remote
pub fn parse_junos_show_interfaces(output: &str) -> Value {
    let mut interfaces = Vec::new();

    for line in output.lines() {
        let line = line.trim();
        if line.is_empty()
            || line.starts_with("Interface")
            || line.starts_with("---")
        {
            continue;
        }

        let parts: Vec<&str> = line.split_whitespace().collect();
        if parts.len() >= 3 {
            let mut iface = serde_json::Map::new();
            iface.insert("name".into(), json!(parts[0]));
            iface.insert("admin".into(), json!(parts[1]));
            iface.insert("link".into(), json!(parts[2]));
            if parts.len() >= 4 {
                iface.insert("proto".into(), json!(parts[3]));
            }
            if parts.len() >= 5 {
                iface.insert("local".into(), json!(parts[4]));
            }
            interfaces.push(Value::Object(iface));
        }
    }

    json!(interfaces)
}

/// Parse Junos `show route summary` output.
fn parse_junos_route_summary(output: &str) -> Value {
    let mut routes = serde_json::Map::new();

    for line in output.lines() {
        let line = line.trim();
        // "X destinations, Y routes (Z active, ...)"
        if line.contains("destinations") && line.contains("routes") {
            if let Some(dest) = extract_first_number(line) {
                routes.insert("total".into(), json!(dest));
            }
        }
        // "Direct:    5 routes"
        if line.starts_with("Direct:") {
            if let Some(n) = extract_first_number(line) {
                routes.insert("direct".into(), json!(n));
            }
        }
        // "Static:    3 routes"
        if line.starts_with("Static:") {
            if let Some(n) = extract_first_number(line) {
                routes.insert("static".into(), json!(n));
            }
        }
        // "BGP:       8 routes"
        if line.starts_with("BGP:") {
            if let Some(n) = extract_first_number(line) {
                routes.insert("bgp".into(), json!(n));
            }
        }
    }

    Value::Object(routes)
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

/// Extract a version string from a line containing "Version X.Y.Z" or "version X.Y.Z".
fn extract_version(line: &str) -> Option<String> {
    let lower = line.to_lowercase();
    let idx = lower.find("version ")?;
    let after = &line[idx + 8..];
    let ver = after
        .split([',', ' ', '\t'])
        .next()?;
    if ver.is_empty() {
        None
    } else {
        Some(ver.to_string())
    }
}

/// Extract the first number from a string.
fn extract_first_number(line: &str) -> Option<u64> {
    line.split_whitespace()
        .find_map(|w| w.trim_matches(|c: char| !c.is_ascii_digit()).parse().ok())
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn test_parse_cisco_show_version_ios() {
        let output = r#"Cisco IOS Software, C3750E Software (C3750E-UNIVERSALK9-M), Version 16.12.4, RELEASE SOFTWARE (fc5)
Technical Support: http://www.cisco.com/techsupport
Copyright (c) 1986-2020 by Cisco Systems, Inc.

cisco WS-C3750X-48T (PowerPC405) processor (revision A0) with 262144K bytes of memory.
System serial number : FOC1234ABCD
Model number         : WS-C3750X-48T-S

core-sw01 uptime is 15 days, 3 hours, 22 minutes
"#;

        let traits = parse_cisco_show_version(output);
        let map: std::collections::HashMap<_, _> = traits.into_iter().collect();

        assert_eq!(map.get("firmware"), Some(&json!("16.12.4")));
        assert_eq!(map.get("model"), Some(&json!("WS-C3750X-48T")));
        assert_eq!(map.get("serial"), Some(&json!("FOC1234ABCD")));
        assert_eq!(map.get("hostname"), Some(&json!("core-sw01")));
        assert!(map.contains_key("uptime"));
    }

    #[test]
    fn test_parse_cisco_show_interfaces() {
        let output = r#"Port      Name               Status       Vlan       Duplex  Speed Type
Gi0/1     Server-1           connected    100        a-full  a-1000 10/100/1000BaseTX
Gi0/2                        notconnect   1          auto    auto  10/100/1000BaseTX
Gi0/3     Uplink             connected    trunk      a-full  a-1000 10/100/1000BaseTX
"#;

        let result = parse_cisco_show_interfaces(output);
        let arr = result.as_array().unwrap();
        assert_eq!(arr.len(), 3);
        assert_eq!(arr[0]["name"], "Gi0/1");
        assert_eq!(arr[0]["status"], "connected");
        assert_eq!(arr[1]["status"], "notconnect");
    }

    #[test]
    fn test_parse_cisco_show_vlan() {
        let output = r#"VLAN Name                             Status    Ports
---- -------------------------------- --------- -------------------------------
1    default                          active    Gi0/1, Gi0/2
100  Production                       active    Gi0/3
200  Servers                          active
"#;

        let result = parse_cisco_show_vlan(output);
        let arr = result.as_array().unwrap();
        assert_eq!(arr.len(), 3);
        assert_eq!(arr[0]["id"], "1");
        assert_eq!(arr[0]["name"], "default");
        assert_eq!(arr[0]["status"], "active");
        assert_eq!(arr[1]["id"], "100");
        assert_eq!(arr[1]["name"], "Production");
    }

    #[test]
    fn test_parse_junos_show_version() {
        let output = r#"Hostname: edge-rtr01
Model: mx240
Junos: 21.4R3-S1
"#;

        let traits = parse_junos_show_version(output);
        let map: std::collections::HashMap<_, _> = traits.into_iter().collect();

        assert_eq!(map.get("hostname"), Some(&json!("edge-rtr01")));
        assert_eq!(map.get("model"), Some(&json!("mx240")));
        assert_eq!(map.get("firmware"), Some(&json!("21.4R3-S1")));
    }

    #[test]
    fn test_parse_junos_show_interfaces() {
        let output = r#"Interface               Admin Link Proto    Local                 Remote
ge-0/0/0                up    up   inet     10.0.1.1/24
ge-0/0/0.0              up    up   inet     10.0.1.1/24
ge-0/0/1                up    down
lo0                     up    up   inet     127.0.0.1
"#;

        let result = parse_junos_show_interfaces(output);
        let arr = result.as_array().unwrap();
        assert_eq!(arr.len(), 4);
        assert_eq!(arr[0]["name"], "ge-0/0/0");
        assert_eq!(arr[0]["admin"], "up");
        assert_eq!(arr[0]["link"], "up");
        assert_eq!(arr[2]["link"], "down");
    }

    #[test]
    fn test_extract_version() {
        assert_eq!(extract_version("Version 16.12.4, RELEASE"), Some("16.12.4".into()));
        assert_eq!(extract_version("version 9.3(8)"), Some("9.3(8)".into()));
        assert_eq!(extract_version("no matching text"), None);
    }

    #[test]
    fn test_extract_first_number() {
        assert_eq!(extract_first_number("connected    5 subnets"), Some(5));
        assert_eq!(extract_first_number("Total: 42 routes"), Some(42));
        assert_eq!(extract_first_number("no numbers"), None);
    }
}
