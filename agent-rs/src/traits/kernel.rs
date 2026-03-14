// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: Elastic-2.0

use anyhow::Result;
use serde_json::{json, Value};

pub fn collect() -> Result<Value> {
    let mut map = serde_json::Map::new();

    if let Ok(v) = std::fs::read_to_string("/proc/version") {
        map.insert("version".into(), json!(v.trim()));
    }
    if let Ok(v) = std::fs::read_to_string("/proc/sys/kernel/osrelease") {
        map.insert("release".into(), json!(v.trim()));
    }
    if let Ok(v) = std::fs::read_to_string("/proc/sys/kernel/hostname") {
        map.insert("hostname".into(), json!(v.trim()));
    }

    Ok(Value::Object(map))
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn test_collect_on_linux() {
        if std::path::Path::new("/proc/version").exists() {
            let val = collect().unwrap();
            let obj = val.as_object().unwrap();
            assert!(obj.contains_key("version"));
            assert!(obj.contains_key("release"));
        }
    }
}
