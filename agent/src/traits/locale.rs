// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

use anyhow::Result;
use serde_json::{json, Value};
use std::process::Command;

pub fn collect() -> Result<Value> {
    let output = match Command::new("locale").output() {
        Ok(o) if o.status.success() => String::from_utf8_lossy(&o.stdout).to_string(),
        _ => return Ok(json!({})),
    };

    Ok(parse_locale_output(&output))
}

fn parse_locale_output(output: &str) -> Value {
    let mut map = serde_json::Map::new();
    for line in output.lines() {
        if let Some((key, val)) = line.split_once('=') {
            let key = key.to_lowercase();
            let val = val.trim_matches('"');
            map.insert(key, json!(val));
        }
    }
    Value::Object(map)
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn test_parse_locale_output() {
        let output = r#"LANG=en_US.UTF-8
LC_CTYPE="en_US.UTF-8"
LC_NUMERIC="en_US.UTF-8"
LC_TIME="en_US.UTF-8"
"#;
        let val = parse_locale_output(output);
        let obj = val.as_object().unwrap();
        assert_eq!(obj["lang"], "en_US.UTF-8");
        assert_eq!(obj["lc_ctype"], "en_US.UTF-8");
    }

    #[test]
    fn test_parse_locale_empty() {
        let val = parse_locale_output("");
        assert_eq!(val.as_object().unwrap().len(), 0);
    }
}
