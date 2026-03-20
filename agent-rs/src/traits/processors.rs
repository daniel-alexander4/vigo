// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

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
                _ => {}
            }
        }
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
    map
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
}
