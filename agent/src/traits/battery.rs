// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: Apache-2.0

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

    // Identity (asset tracking) — each is optional; firmware that doesn't
    // populate the node leaves the field absent rather than empty.
    for (field, key) in [
        ("manufacturer", "manufacturer"),
        ("model_name", "model_name"),
        ("serial_number", "serial_number"),
        ("technology", "technology"),
    ] {
        if let Some(v) = get(field) {
            dev.insert(key.into(), json!(v));
        }
    }

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

    // Delivery-side readings, in volts and watts. These are the fields that
    // separate a pack that has lost *capacity* from one that has lost the
    // ability to *deliver current* — a pack can read 93% and still sag below
    // the EC's cutoff on a transient, which removes power with nothing logged.
    //
    // Reported raw and deliberately unclassified. `voltage_min_design` is NOT
    // usable as a discharge floor: measured on two of this fleet's laptops it
    // carries the pack's *nominal* voltage (11.25 V and 11.1 V, both 3.7-3.75
    // V/cell across 3 cells) rather than the ~3.0 V/cell empty cutoff, so a
    // "margin" derived from it goes negative during ordinary discharge. It is
    // emitted as what the firmware says, for a human to interpret.
    //
    // Note also what a check-in-cadence sample cannot do: the sag that drops
    // a machine lasts milliseconds. These fields trend a pack over weeks; they
    // do not catch the event.
    let volts = |k: &str| get(k).and_then(|s| s.parse::<f64>().ok()).map(|v| v / 1_000_000.0);
    let voltage_now_v = volts("voltage_now");
    if let Some(v) = voltage_now_v {
        dev.insert("voltage_now_v".into(), json!(round2(v)));
    }
    if let Some(v) = volts("voltage_min_design").filter(|v| *v > 0.0) {
        dev.insert("voltage_min_design_v".into(), json!(round2(v)));
    }

    // Instantaneous load. The energy family reports it directly; the charge
    // family (danlap's pack, and every battery with no `energy_*` node) does
    // not, and there `voltage_now × current_now` is the only way to get a
    // figure at all. Both are the same physical quantity, so one key.
    let load_w = get("power_now")
        .and_then(|s| s.parse::<f64>().ok())
        .map(|p| p / 1_000_000.0)
        .or_else(|| {
            let amps = get("current_now").and_then(|s| s.parse::<f64>().ok())? / 1_000_000.0;
            Some(voltage_now_v? * amps.abs())
        });
    if let Some(w) = load_w {
        dev.insert("power_now_w".into(), json!(round2(w)));
    }

    // Time-to-empty / time-to-full (seconds), only while actively
    // (dis)charging. Prefer the energy family (µWh ÷ µW); fall back to the
    // charge family (µAh ÷ µA) for batteries that don't report energy_*.
    // Both families divide to hours, so ×3600 → seconds; multiply before
    // dividing to keep integer precision. Instantaneous-rate estimate — it
    // jitters with load (display-only, never feeds health classification).
    let uval = |k: &str| get(k).and_then(|s| s.parse::<u64>().ok());
    let (now, cap_full, rate) = match (uval("energy_now"), uval("power_now")) {
        (Some(n), Some(p)) => (Some(n), uval("energy_full"), Some(p)),
        _ => (uval("charge_now"), uval("charge_full"), uval("current_now")),
    };
    if let (Some(now), Some(rate)) = (now, rate) {
        if rate > 0 {
            match dev.get("status").and_then(Value::as_str) {
                Some("Discharging") => {
                    dev.insert("time_to_empty_secs".into(), json!(now.saturating_mul(3600) / rate));
                }
                Some("Charging") => {
                    if let Some(full) = cap_full {
                        if full > now {
                            dev.insert(
                                "time_to_full_secs".into(),
                                json!((full - now).saturating_mul(3600) / rate),
                            );
                        }
                    }
                }
                _ => {}
            }
        }
    }

    (dev, health)
}

fn round1(v: f64) -> f64 {
    (v * 10.0).round() / 10.0
}

/// Two places for volts and watts — a 10 mV drift is the resolution that
/// matters when watching a pack sag, and one place would hide it.
fn round2(v: f64) -> f64 {
    (v * 100.0).round() / 100.0
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
    fn test_parse_battery_time_to_empty_energy() {
        // Discharging, energy family: 35 Wh remaining at 20 W → 1.75 h = 6300 s.
        let (dev, _) = parse_battery(getter(&[
            ("status", "Discharging"),
            ("energy_now", "35000000"),
            ("power_now", "20000000"),
            ("energy_full", "50000000"),
        ]));
        assert_eq!(dev["time_to_empty_secs"], json!(6300));
        assert!(!dev.contains_key("time_to_full_secs"));
    }

    #[test]
    fn test_parse_battery_time_to_full_energy() {
        // Charging, energy family: (50−35) Wh headroom at 20 W → 0.75 h = 2700 s.
        let (dev, _) = parse_battery(getter(&[
            ("status", "Charging"),
            ("energy_now", "35000000"),
            ("power_now", "20000000"),
            ("energy_full", "50000000"),
        ]));
        assert_eq!(dev["time_to_full_secs"], json!(2700));
        assert!(!dev.contains_key("time_to_empty_secs"));
    }

    #[test]
    fn test_parse_battery_time_charge_fallback() {
        // Discharging, charge family (no energy_*/power_*): 4 Ah at 2 A → 2 h.
        let (dev, _) = parse_battery(getter(&[
            ("status", "Discharging"),
            ("charge_now", "4000000"),
            ("current_now", "2000000"),
            ("charge_full", "5000000"),
        ]));
        assert_eq!(dev["time_to_empty_secs"], json!(7200));
    }

    #[test]
    fn test_parse_battery_time_skipped_when_idle_or_no_rate() {
        // Full status → no estimate even with a known rate.
        let (full_dev, _) = parse_battery(getter(&[
            ("status", "Full"),
            ("energy_now", "50000000"),
            ("power_now", "20000000"),
            ("energy_full", "50000000"),
        ]));
        assert!(!full_dev.contains_key("time_to_empty_secs"));
        assert!(!full_dev.contains_key("time_to_full_secs"));

        // Discharging but zero rate → no estimate (no div-by-zero).
        let (zero_dev, _) = parse_battery(getter(&[
            ("status", "Discharging"),
            ("energy_now", "50000000"),
            ("power_now", "0"),
        ]));
        assert!(!zero_dev.contains_key("time_to_empty_secs"));
    }

    #[test]
    fn test_parse_battery_delivery_fields_energy_family() {
        // annlap's real numbers: 12.268 V now, 11.1 V "min design", 5 W load.
        let (dev, _) = parse_battery(getter(&[
            ("status", "Discharging"),
            ("voltage_now", "12268000"),
            ("voltage_min_design", "11100000"),
            ("power_now", "5000000"),
            ("energy_now", "20000000"),
            ("energy_full", "37880000"),
        ]));
        assert_eq!(dev["voltage_now_v"], json!(12.27));
        assert_eq!(dev["voltage_min_design_v"], json!(11.1));
        assert_eq!(dev["power_now_w"], json!(5.0));
    }

    #[test]
    fn test_parse_battery_load_synthesized_for_charge_family() {
        // danlap's pack: no power_now / energy_* at all, so the only way to a
        // load figure is voltage_now × current_now.
        let (dev, _) = parse_battery(getter(&[
            ("status", "Discharging"),
            ("voltage_now", "12000000"),
            ("current_now", "1500000"),
            ("charge_now", "4000000"),
            ("charge_full", "5000000"),
        ]));
        assert_eq!(dev["power_now_w"], json!(18.0), "12 V × 1.5 A");
    }

    #[test]
    fn test_parse_battery_load_uses_current_magnitude() {
        // Some drivers report current_now signed while discharging; power is
        // a magnitude either way, and a negative watt figure is nonsense.
        let (dev, _) = parse_battery(getter(&[
            ("voltage_now", "12000000"),
            ("current_now", "-1500000"),
        ]));
        assert_eq!(dev["power_now_w"], json!(18.0));
    }

    #[test]
    fn test_parse_battery_delivery_fields_omitted_when_absent() {
        // Firmware that populates none of them must yield no keys at all,
        // never a zero that reads as a real measurement.
        let (dev, _) = parse_battery(getter(&[("capacity", "80")]));
        assert!(!dev.contains_key("voltage_now_v"));
        assert!(!dev.contains_key("voltage_min_design_v"));
        assert!(!dev.contains_key("power_now_w"));
    }

    #[test]
    fn test_parse_battery_zero_voltage_min_design_omitted() {
        // A zero here is firmware declining to answer, not an 0 V floor.
        let (dev, _) = parse_battery(getter(&[
            ("voltage_now", "12000000"),
            ("voltage_min_design", "0"),
        ]));
        assert_eq!(dev["voltage_now_v"], json!(12.0));
        assert!(!dev.contains_key("voltage_min_design_v"));
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
    fn test_parse_battery_identity_fields() {
        let (dev, _) = parse_battery(getter(&[
            ("manufacturer", "LGC"),
            ("model_name", "DELL ABC123"),
            ("serial_number", "0001"),
            ("technology", "Li-poly"),
            ("capacity", "80"),
        ]));
        assert_eq!(dev["manufacturer"], json!("LGC"));
        assert_eq!(dev["model_name"], json!("DELL ABC123"));
        assert_eq!(dev["serial_number"], json!("0001"));
        assert_eq!(dev["technology"], json!("Li-poly"));
    }

    #[test]
    fn test_parse_battery_identity_absent_omitted() {
        let (dev, _) = parse_battery(getter(&[("capacity", "80")]));
        assert!(!dev.contains_key("manufacturer"));
        assert!(!dev.contains_key("serial_number"));
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
