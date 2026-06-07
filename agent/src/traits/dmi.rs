// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

use anyhow::Result;
use serde_json::{json, Value};
use std::path::Path;

const DMI_BASE: &str = "/sys/class/dmi/id";

// Raw SMBIOS structures the kernel exposes (root-readable). Per-DIMM
// identity (manufacturer / part number / serial) lives only here in the
// type-17 "Memory Device" records — never in /proc/meminfo.
const SMBIOS_ENTRIES: &str = "/sys/firmware/dmi/entries";

const DMI_FIELDS: &[(&str, &str)] = &[
    ("bios_vendor", "bios_vendor"),
    ("bios_version", "bios_version"),
    ("bios_date", "bios_date"),
    ("board_vendor", "board_vendor"),
    ("board_name", "board_name"),
    ("product_name", "product_name"),
    ("product_serial", "product_serial"),
    ("product_vendor", "sys_vendor"),
    ("chassis_type", "chassis_type"),
];

pub fn collect() -> Result<Value> {
    let mut result = serde_json::Map::new();

    for &(key, file) in DMI_FIELDS {
        let path = format!("{}/{}", DMI_BASE, file);
        if let Ok(val) = std::fs::read_to_string(&path) {
            let val = val.trim();
            if !val.is_empty() {
                result.insert(key.to_string(), json!(val));
            }
        }
    }

    // ARM/AArch64 boards (Raspberry Pi, etc.) don't have DMI/SMBIOS.
    // Fall back to Device Tree and /proc/cpuinfo.
    if result.is_empty() {
        collect_device_tree(&mut result);
    }

    // Per-DIMM physical inventory from SMBIOS type-17 (x86/UEFI boards).
    // Empty on ARM/no-SMBIOS systems — the key is then simply absent.
    let mem = collect_memory_devices(Path::new(SMBIOS_ENTRIES));
    if !mem.is_empty() {
        result.insert("memory_devices".to_string(), Value::Array(mem));
    }

    Ok(Value::Object(result))
}

/// Collects per-DIMM identity from every SMBIOS type-17 record under
/// `entries_dir` (`/sys/firmware/dmi/entries/17-*/raw`). Skips empty slots.
fn collect_memory_devices(entries_dir: &Path) -> Vec<Value> {
    let mut out = Vec::new();
    let rd = match std::fs::read_dir(entries_dir) {
        Ok(r) => r,
        Err(_) => return out,
    };
    for entry in rd.flatten() {
        if !entry.file_name().to_string_lossy().starts_with("17-") {
            continue;
        }
        if let Ok(raw) = std::fs::read(entry.path().join("raw")) {
            if let Some(dev) = parse_memory_device(&raw) {
                out.push(Value::Object(dev));
            }
        }
    }
    // Sort by slot locator so the array is byte-identical between refreshes
    // regardless of read_dir enumeration order (ADR-033).
    out.sort_by(|a, b| {
        a["locator"]
            .as_str()
            .unwrap_or("")
            .cmp(b["locator"].as_str().unwrap_or(""))
    });
    out
}

/// Parses one SMBIOS type-17 (Memory Device) structure into a trait map.
/// Returns None for empty slots (size == 0) or malformed input. Field
/// offsets are per the SMBIOS spec; every access is bounds-checked, so a
/// short/truncated structure yields whatever fields fit and omits the rest.
fn parse_memory_device(raw: &[u8]) -> Option<serde_json::Map<String, Value>> {
    // Strings follow the formatted area, whose length is at offset 1.
    let formatted_len = *raw.get(1)? as usize;
    let strings = raw.get(formatted_len..).unwrap_or(&[]);

    let word = |off: usize| -> Option<u16> {
        Some(u16::from_le_bytes([*raw.get(off)?, *raw.get(off + 1)?]))
    };
    let string_at = |idx_off: usize| -> Option<String> {
        smbios_string(strings, *raw.get(idx_off)?)
    };

    // Size at 0x0C: 0 = empty slot (skip), 0xFFFF = unknown, 0x7FFF = use
    // the extended DWORD at 0x1C (MB). Otherwise bit15 selects KB vs MB.
    let size = word(0x0C)?;
    if size == 0 {
        return None;
    }

    let mut dev = serde_json::Map::new();
    if let Some(loc) = string_at(0x10) {
        dev.insert("locator".into(), json!(loc));
    }
    if let Some(m) = string_at(0x17) {
        dev.insert("manufacturer".into(), json!(m));
    }
    if let Some(s) = string_at(0x18) {
        dev.insert("serial_number".into(), json!(s));
    }
    if let Some(p) = string_at(0x1A) {
        dev.insert("part_number".into(), json!(p));
    }

    let size_bytes = if size == 0x7FFF {
        raw.get(0x1C..0x20)
            .map(|b| (u32::from_le_bytes([b[0], b[1], b[2], b[3]]) as u64 & 0x7FFF_FFFF) * 1024 * 1024)
    } else if size == 0xFFFF {
        None
    } else if size & 0x8000 != 0 {
        Some((size & 0x7FFF) as u64 * 1024)
    } else {
        Some(size as u64 * 1024 * 1024)
    };
    if let Some(bytes) = size_bytes {
        dev.insert("size_bytes".into(), json!(bytes));
    }

    // Speed (MT/s) at 0x15; 0 or 0xFFFF means unknown.
    if let Some(sp) = word(0x15) {
        if sp != 0 && sp != 0xFFFF {
            dev.insert("speed_mts".into(), json!(sp));
        }
    }

    Some(dev)
}

/// Resolves a 1-based SMBIOS string index against the structure's string
/// table (NUL-separated). Index 0 and empty strings return None.
fn smbios_string(table: &[u8], idx: u8) -> Option<String> {
    if idx == 0 {
        return None;
    }
    table
        .split(|&b| b == 0)
        .nth((idx - 1) as usize)
        .map(|s| String::from_utf8_lossy(s).trim().to_string())
        .filter(|s| !s.is_empty())
}

/// Collect hardware info from Device Tree and /proc/cpuinfo (ARM boards).
fn collect_device_tree(result: &mut serde_json::Map<String, Value>) {
    // /proc/device-tree/model — e.g., "Raspberry Pi 4 Model B Rev 1.4"
    if let Ok(val) = std::fs::read_to_string("/proc/device-tree/model") {
        let val = val.trim_end_matches('\0').trim();
        if !val.is_empty() {
            result.insert("product_name".to_string(), json!(val));
        }
    }

    // /proc/device-tree/compatible — e.g., "raspberrypi,4-model-b\0brcm,bcm2711"
    if let Ok(val) = std::fs::read_to_string("/proc/device-tree/compatible") {
        let parts: Vec<&str> = val.split('\0').filter(|s| !s.is_empty()).collect();
        if let Some(first) = parts.first() {
            if let Some((vendor, _)) = first.split_once(',') {
                result.insert("board_vendor".to_string(), json!(vendor));
            }
        }
    }

    // /proc/device-tree/serial-number
    if let Ok(val) = std::fs::read_to_string("/proc/device-tree/serial-number") {
        let val = val.trim_end_matches('\0').trim();
        if !val.is_empty() {
            result.insert("serial_number".to_string(), json!(val));
        }
    }

    // /proc/cpuinfo on ARM has Hardware, Revision, Serial, Model fields.
    if let Ok(cpuinfo) = std::fs::read_to_string("/proc/cpuinfo") {
        for line in cpuinfo.lines() {
            if let Some((k, v)) = line.split_once(':') {
                let k = k.trim();
                let v = v.trim();
                match k {
                    "Hardware" => {
                        if !result.contains_key("board_name") {
                            result.insert("board_name".to_string(), json!(v));
                        }
                    }
                    "Revision" => {
                        result.insert("board_revision".to_string(), json!(v));
                    }
                    "Model" => {
                        if !result.contains_key("product_name") {
                            result.insert("product_name".to_string(), json!(v));
                        }
                    }
                    _ => {}
                }
            }
        }
    }
}

/// Read a DMI field by filename. Used by the cloud collector.
pub(crate) fn read_dmi_field(filename: &str) -> Option<String> {
    std::fs::read_to_string(format!("{}/{}", DMI_BASE, filename))
        .ok()
        .map(|s| s.trim().to_string())
        .filter(|s| !s.is_empty())
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn test_collect_returns_object() {
        let val = collect().unwrap();
        assert!(val.is_object());
    }

    // Builds a synthetic SMBIOS type-17 structure: 27-byte formatted area
    // (through Part Number at 0x1A) plus a NUL-separated string table.
    fn type17(size_word: u16, speed: u16, strings: &[&str]) -> Vec<u8> {
        let mut b = vec![0u8; 0x1B];
        b[0] = 17; // type
        b[1] = 0x1B; // formatted length
        b[0x0C..0x0E].copy_from_slice(&size_word.to_le_bytes());
        b[0x10] = 1; // device locator → string 1
        b[0x15..0x17].copy_from_slice(&speed.to_le_bytes());
        b[0x17] = 2; // manufacturer → string 2
        b[0x18] = 3; // serial → string 3
        b[0x1A] = 4; // part number → string 4
        for s in strings {
            b.extend_from_slice(s.as_bytes());
            b.push(0);
        }
        b.push(0); // double-NUL terminates the string table
        b
    }

    #[test]
    fn test_parse_memory_device_full() {
        // size 0x2000 = 8192 (MB), bit15 clear → 8 GiB.
        let raw = type17(0x2000, 3200, &["DIMM_A1", "Samsung", "SN123456", "M471A1K43DB1"]);
        let dev = parse_memory_device(&raw).expect("populated slot");
        assert_eq!(dev["locator"], json!("DIMM_A1"));
        assert_eq!(dev["manufacturer"], json!("Samsung"));
        assert_eq!(dev["serial_number"], json!("SN123456"));
        assert_eq!(dev["part_number"], json!("M471A1K43DB1"));
        assert_eq!(dev["size_bytes"], json!(8192u64 * 1024 * 1024));
        assert_eq!(dev["speed_mts"], json!(3200));
    }

    #[test]
    fn test_parse_memory_device_empty_slot_skipped() {
        let raw = type17(0, 0, &["DIMM_B1", "", "", ""]);
        assert!(parse_memory_device(&raw).is_none());
    }

    #[test]
    fn test_parse_memory_device_unknown_size_and_speed_omitted() {
        // size 0xFFFF (unknown) but a real locator → still emitted, no size.
        let raw = type17(0xFFFF, 0xFFFF, &["DIMM_A1", "", "", ""]);
        let dev = parse_memory_device(&raw).expect("locator present");
        assert_eq!(dev["locator"], json!("DIMM_A1"));
        assert!(!dev.contains_key("size_bytes"));
        assert!(!dev.contains_key("speed_mts"));
        assert!(!dev.contains_key("manufacturer"));
    }

    #[test]
    fn test_parse_memory_device_truncated_no_panic() {
        // A structure too short to reach the size word must not panic.
        assert!(parse_memory_device(&[17, 0x04, 0, 0]).is_none());
        let _ = parse_memory_device(&[]);
        let _ = parse_memory_device(&[17]);
    }

    #[test]
    fn test_collect_memory_devices_missing_dir() {
        assert!(collect_memory_devices(Path::new("/nonexistent/dmi/entries")).is_empty());
    }
}
