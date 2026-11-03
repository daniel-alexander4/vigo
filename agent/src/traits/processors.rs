// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: Apache-2.0

use anyhow::Result;
use serde_json::{json, Value};
use std::collections::HashSet;

pub fn collect() -> Result<Value> {
    let mut info = parse_cpuinfo(&std::fs::read_to_string("/proc/cpuinfo").unwrap_or_default());

    // Prefer stable max frequency over fluctuating current MHz.
    if let Ok(khz) = std::fs::read_to_string("/sys/devices/system/cpu/cpu0/cpufreq/cpuinfo_max_freq")
    {
        if let Ok(khz) = khz.trim().parse::<u64>() {
            info.insert("mhz".into(), json!(khz / 1000));
        }
    }

    // CPU microcode version (sysfs preferred, /proc/cpuinfo fallback).
    if let Ok(ver) =
        std::fs::read_to_string("/sys/devices/system/cpu/cpu0/microcode/version")
    {
        let ver = ver.trim();
        if !ver.is_empty() {
            info.insert("microcode".into(), json!(ver));
        }
    } else if !info.contains_key("microcode") {
        // Fallback: parse from /proc/cpuinfo (collected above but not extracted).
        if let Ok(cpuinfo) = std::fs::read_to_string("/proc/cpuinfo") {
            for line in cpuinfo.lines() {
                if let Some((key, val)) = line.split_once(':') {
                    if key.trim() == "microcode" {
                        let val = val.trim();
                        if !val.is_empty() {
                            info.insert("microcode".into(), json!(val));
                            break;
                        }
                    }
                }
            }
        }
    }

    Ok(json!(info))
}

fn parse_cpuinfo(content: &str) -> serde_json::Map<String, Value> {
    let mut logical = 0usize;
    let mut physical_ids = HashSet::new();
    let mut core_ids = HashSet::new();
    let mut model = String::new();
    let mut vendor = String::new();
    let mut mhz = 0.0f64;
    let mut current_physical_id = String::new();

    // ARM-specific fields.
    let mut cpu_implementer = String::new();
    let mut cpu_part = String::new();
    let mut cpu_arch = String::new();

    for line in content.lines() {
        let line = line.trim();
        if line.is_empty() {
            continue;
        }
        if let Some((key, val)) = line.split_once(':') {
            let key = key.trim();
            let val = val.trim();
            match key {
                "processor" => logical += 1,
                "physical id" => {
                    current_physical_id = val.to_string();
                    physical_ids.insert(val.to_string());
                }
                "core id" => {
                    let compound = format!("{}:{}", current_physical_id, val);
                    core_ids.insert(compound);
                }
                "model name" if model.is_empty() => model = val.to_string(),
                "vendor_id" if vendor.is_empty() => vendor = val.to_string(),
                "cpu MHz" if mhz == 0.0 => {
                    mhz = val.parse().unwrap_or(0.0);
                }
                // ARM cpuinfo fields.
                "CPU implementer" if cpu_implementer.is_empty() => cpu_implementer = val.to_string(),
                "CPU part" if cpu_part.is_empty() => cpu_part = val.to_string(),
                "CPU architecture" if cpu_arch.is_empty() => cpu_arch = val.to_string(),
                _ => {}
            }
        }
    }

    // ARM fallback: resolve implementer + part to human-readable names.
    if model.is_empty() && !cpu_implementer.is_empty() {
        vendor = arm_vendor_name(&cpu_implementer).to_string();
        model = arm_core_name(&cpu_implementer, &cpu_part).to_string();
    }

    let physical = if core_ids.is_empty() {
        logical // No physical/core id info, assume 1:1
    } else {
        core_ids.len()
    };

    let mut map = serde_json::Map::new();
    map.insert("physical_cores".into(), json!(physical));
    map.insert("logical_cores".into(), json!(logical));
    if !model.is_empty() {
        map.insert("model".into(), json!(model));
    }
    if !vendor.is_empty() {
        map.insert("vendor".into(), json!(vendor));
    }
    if mhz > 0.0 {
        map.insert("mhz".into(), json!(mhz));
    }
    if !cpu_arch.is_empty() {
        map.insert("cpu_architecture".into(), json!(cpu_arch));
    }
    map
}

/// Map ARM CPU implementer hex code to vendor name.
fn arm_vendor_name(implementer: &str) -> &'static str {
    match implementer {
        "0x41" => "ARM",
        "0x42" => "Broadcom",
        "0x43" => "Cavium",
        "0x44" => "DEC",
        "0x46" => "Fujitsu",
        "0x48" => "HiSilicon",
        "0x4e" => "NVIDIA",
        "0x50" => "APM",
        "0x51" => "Qualcomm",
        "0x53" => "Samsung",
        "0x56" => "Marvell",
        "0x61" => "Apple",
        "0x69" => "Intel",
        "0xc0" => "Ampere",
        _ => "ARM",
    }
}

/// Map ARM CPU implementer + part hex codes to core name.
fn arm_core_name(implementer: &str, part: &str) -> &'static str {
    match (implementer, part) {
        // ARM cores
        ("0x41", "0xd03") => "Cortex-A53",
        ("0x41", "0xd04") => "Cortex-A35",
        ("0x41", "0xd05") => "Cortex-A55",
        ("0x41", "0xd06") => "Cortex-A65",
        ("0x41", "0xd07") => "Cortex-A57",
        ("0x41", "0xd08") => "Cortex-A72",
        ("0x41", "0xd09") => "Cortex-A73",
        ("0x41", "0xd0a") => "Cortex-A75",
        ("0x41", "0xd0b") => "Cortex-A76",
        ("0x41", "0xd0c") => "Neoverse N1",
        ("0x41", "0xd0d") => "Cortex-A77",
        ("0x41", "0xd0e") => "Cortex-A76AE",
        ("0x41", "0xd40") => "Neoverse V1",
        ("0x41", "0xd41") => "Cortex-A78",
        ("0x41", "0xd42") => "Cortex-A78AE",
        ("0x41", "0xd44") => "Cortex-X1",
        ("0x41", "0xd46") => "Cortex-A510",
        ("0x41", "0xd47") => "Cortex-A710",
        ("0x41", "0xd48") => "Cortex-X2",
        ("0x41", "0xd49") => "Neoverse N2",
        ("0x41", "0xd4a") => "Neoverse E1",
        ("0x41", "0xd4b") => "Cortex-A78C",
        ("0x41", "0xd4d") => "Cortex-A715",
        ("0x41", "0xd4e") => "Cortex-X3",
        ("0x41", "0xd80") => "Cortex-A520",
        ("0x41", "0xd81") => "Cortex-A720",
        ("0x41", "0xd82") => "Cortex-X4",
        // Broadcom
        ("0x42", "0x516") => "Vulcan",
        // Qualcomm
        ("0x51", "0x800") => "Kryo 260",
        ("0x51", "0x801") => "Kryo 280",
        ("0x51", "0x802") => "Kryo 385 Gold",
        ("0x51", "0x803") => "Kryo 385 Silver",
        ("0x51", "0x804") => "Kryo 485 Gold",
        ("0x51", "0x805") => "Kryo 485 Silver",
        // Apple
        ("0x61", "0x022") => "M1 Icestorm",
        ("0x61", "0x023") => "M1 Firestorm",
        ("0x61", "0x024") => "M1 Pro/Max Icestorm",
        ("0x61", "0x025") => "M1 Pro/Max Firestorm",
        ("0x61", "0x028") => "M2 Blizzard",
        ("0x61", "0x029") => "M2 Avalanche",
        // Ampere
        ("0xc0", "0xac3") => "Ampere-1",
        ("0xc0", "0xac4") => "Ampere-1A",
        _ => "ARM",
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn test_parse_cpuinfo() {
        let content = r#"processor	: 0
vendor_id	: GenuineIntel
model name	: Intel(R) Core(TM) i7-9750H CPU @ 2.60GHz
cpu MHz		: 2600.000
physical id	: 0
core id		: 0

processor	: 1
vendor_id	: GenuineIntel
model name	: Intel(R) Core(TM) i7-9750H CPU @ 2.60GHz
cpu MHz		: 2600.000
physical id	: 0
core id		: 1

"#;
        let info = parse_cpuinfo(content);
        assert_eq!(info["logical_cores"], 2);
        assert_eq!(info["physical_cores"], 2);
        assert_eq!(info["model"], "Intel(R) Core(TM) i7-9750H CPU @ 2.60GHz");
        assert_eq!(info["vendor"], "GenuineIntel");
        assert_eq!(info["mhz"], 2600.0);
    }

    #[test]
    fn test_parse_cpuinfo_no_physical_id() {
        let content = "processor\t: 0\nprocessor\t: 1\n";
        let info = parse_cpuinfo(content);
        assert_eq!(info["logical_cores"], 2);
        assert_eq!(info["physical_cores"], 2);
    }

    #[test]
    fn test_parse_cpuinfo_arm() {
        let content = r#"processor	: 0
BogoMIPS	: 108.00
Features	: fp asimd evtstrm crc32 pmull sha1 sha2 aes
CPU implementer	: 0x41
CPU architecture: 8
CPU variant	: 0x0
CPU part	: 0xd08
CPU revision	: 3

processor	: 1
BogoMIPS	: 108.00
Features	: fp asimd evtstrm crc32 pmull sha1 sha2 aes
CPU implementer	: 0x41
CPU architecture: 8
CPU variant	: 0x0
CPU part	: 0xd08
CPU revision	: 3

"#;
        let info = parse_cpuinfo(content);
        assert_eq!(info["logical_cores"], 2);
        assert_eq!(info["model"], "Cortex-A72");
        assert_eq!(info["vendor"], "ARM");
        assert_eq!(info["cpu_architecture"], "8");
    }
}
