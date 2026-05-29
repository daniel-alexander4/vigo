// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

//! Battery health collector (Linux). Reads `/sys/class/power_supply/{AC,BAT*}`
//! and reports per-battery charge level, wear health (`energy_full` ÷
//! `energy_full_design`), cycle count, and status, plus a top-level
//! `health_pct` (the worst battery's wear, the signal the server classifies)
//! and `ac_online`. Pure sysfs reads — no subprocess, no upower dependency.
//! Returns an empty object on desktops/servers with no `BAT*` node so the
//! server sees no battery trait and the Agent Health card hides the row.

use anyhow::Result;
use serde_json::{json, Value};
use std::path::Path;

const POWER_SUPPLY_DIR: &str = "/sys/class/power_supply";

pub fn collect() -> Result<Value> {
    Ok(collect_from(Path::new(POWER_SUPPLY_DIR)))
}

fn collect_from(dir: &Path) -> Value {
    let entries = match std::fs::read_dir(dir) {
        Ok(e) => e,
        Err(_) => return json!({}),
    };

    let mut batteries = serde_json::Map::new();
    let mut worst_health: Option<f64> = None;
    let mut ac_online: Option<bool> = None;

    for entry in entries.flatten() {
        let base = entry.path();
        let name = entry.file_name().to_string_lossy().to_string();
        let supply_type = read_field(&base, "type");
        match supply_type.as_deref() {
            // System battery (exclude peripheral batteries like hidpp_battery_*).
            Some("Battery") if name.to_uppercase().starts_with("BAT") => {
                let (dev, health) = parse_battery(|f| read_field(&base, f));
                if let Some(h) = health {
                    worst_health = Some(worst_health.map_or(h, |w| w.min(h)));
                }
                batteries.insert(name, Value::Object(dev));
            }
            // AC adapter — any online adapter counts as "on AC".
            Some("Mains") => {
                if let Some(online) = read_field(&base, "online") {
                    ac_online = Some(ac_online.unwrap_or(false) || online == "1");
                }
            }
            _ => {}
        }
    }

    // Skip entirely when no system battery is present (desktops/servers).
    if batteries.is_empty() {
        return json!({});
    }

    let mut out = serde_json::Map::new();
    out.insert("batteries".into(), Value::Object(batteries));
    if let Some(h) = worst_health {
        out.insert("health_pct".into(), json!(h));
    }
    if let Some(ac) = ac_online {
        out.insert("ac_online".into(), json!(ac));
    }
    Value::Object(out)
}

/// Reads a single sysfs attribute, trimmed; None when absent or empty.
fn read_field(base: &Path, name: &str) -> Option<String> {
    std::fs::read_to_string(base.join(name))
        .ok()
        .map(|s| s.trim().to_string())
        .filter(|s| !s.is_empty())
}

/// Builds the per-battery trait map from a field getter and returns it
/// alongside the wear-health percentage (for the worst-across-batteries
/// rollup). The getter indirection keeps this unit-testable without a real
/// sysfs tree.
fn parse_battery(get: impl Fn(&str) -> Option<String>) -> (serde_json::Map<String, Value>, Option<f64>) {
    let mut dev = serde_json::Map::new();

    if let Some(cap) = get("capacity").and_then(|s| s.parse::<u64>().ok()) {
        dev.insert("capacity_pct".into(), json!(cap));
    }
    if let Some(status) = get("status") {
        dev.insert("status".into(), json!(status));
    }
    // Cycle count is 0 or absent on batteries that don't report it.
    if let Some(cycles) = get("cycle_count").and_then(|s| s.parse::<u64>().ok()) {
        if cycles > 0 {
            dev.insert("cycle_count".into(), json!(cycles));
        }
    }

    // Wear health = full ÷ design. Prefer energy_* (µWh); fall back to
    // charge_* (µAh) — the ratio is unit-independent.
    let full = get("energy_full")
        .or_else(|| get("charge_full"))
        .and_then(|s| s.parse::<u64>().ok());
    let design = get("energy_full_design")
        .or_else(|| get("charge_full_design"))
        .and_then(|s| s.parse::<u64>().ok());
    let mut health = None;
    if let (Some(full), Some(design)) = (full, design) {
        if design > 0 {
            let h = round1(full as f64 / design as f64 * 100.0);
            dev.insert("health_pct".into(), json!(h));
            health = Some(h);
        }
    }

    // Capacity in Wh for display — only when energy_* (µWh) is the source.
    if let (Some(full_uwh), Some(design_uwh)) = (
        get("energy_full").and_then(|s| s.parse::<u64>().ok()),
        get("energy_full_design").and_then(|s| s.parse::<u64>().ok()),
    ) {
        dev.insert("energy_full_wh".into(), json!(round1(full_uwh as f64 / 1_000_000.0)));
        dev.insert("energy_full_design_wh".into(), json!(round1(design_uwh as f64 / 1_000_000.0)));
    }

    (dev, health)
}

fn round1(v: f64) -> f64 {
    (v * 10.0).round() / 10.0
}

#[cfg(test)]
mod tests {
    use super::*;
    use std::collections::HashMap;

    fn getter(fields: &[(&str, &str)]) -> impl Fn(&str) -> Option<String> {
        let map: HashMap<String, String> =
            fields.iter().map(|(k, v)| (k.to_string(), v.to_string())).collect();
        move |f: &str| map.get(f).cloned()
    }

    #[test]
    fn test_parse_battery_worn_energy() {
        // girlslaptop's real numbers: 35.895 Wh full / 54 Wh design = 66.5%.
        let (dev, health) = parse_battery(getter(&[
            ("capacity", "73"),
            ("status", "Discharging"),
            ("cycle_count", "412"),
            ("energy_full", "35895000"),
            ("energy_full_design", "54000000"),
        ]));
        assert_eq!(health, Some(66.5));
        assert_eq!(dev["health_pct"], json!(66.5));
        assert_eq!(dev["capacity_pct"], json!(73));
        assert_eq!(dev["status"], json!("Discharging"));
        assert_eq!(dev["cycle_count"], json!(412));
        assert_eq!(dev["energy_full_wh"], json!(35.9));
        assert_eq!(dev["energy_full_design_wh"], json!(54.0));
    }

    #[test]
    fn test_parse_battery_charge_fallback() {
        // Batteries that report charge_* (µAh) instead of energy_* (µWh).
        let (dev, health) = parse_battery(getter(&[
            ("capacity", "90"),
            ("charge_full", "4500000"),
            ("charge_full_design", "5000000"),
        ]));
        assert_eq!(health, Some(90.0));
        assert_eq!(dev["health_pct"], json!(90.0));
        // No energy_* source, so no Wh display fields.
        assert!(!dev.contains_key("energy_full_wh"));
    }

    #[test]
    fn test_parse_battery_no_design_no_health() {
        // A peripheral-style battery that reports capacity but no design max.
        let (dev, health) = parse_battery(getter(&[("capacity", "50")]));
        assert_eq!(health, None);
        assert!(!dev.contains_key("health_pct"));
        assert_eq!(dev["capacity_pct"], json!(50));
    }

    #[test]
    fn test_parse_battery_zero_cycle_count_omitted() {
        let (dev, _) = parse_battery(getter(&[("cycle_count", "0")]));
        assert!(!dev.contains_key("cycle_count"));
    }

    #[test]
    fn test_collect_from_missing_dir_is_empty() {
        let v = collect_from(Path::new("/nonexistent/power_supply"));
        assert_eq!(v, json!({}));
    }

    #[test]
    fn test_collect_does_not_panic() {
        let _ = collect();
    }
}
