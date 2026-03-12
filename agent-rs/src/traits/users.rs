// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: BUSL-1.1

use anyhow::Result;
use serde_json::{json, Value};
use std::collections::BTreeSet;
use std::process::Command;

pub fn collect() -> Result<Value> {
    let users = logged_in_users();
    let count = users.len();
    let list: Vec<String> = users.into_iter().collect();

    Ok(json!({
        "count": count,
        "list": list,
    }))
}

fn logged_in_users() -> BTreeSet<String> {
    let output = match Command::new("who").output() {
        Ok(o) if o.status.success() => String::from_utf8_lossy(&o.stdout).to_string(),
        _ => return BTreeSet::new(),
    };

    output
        .lines()
        .filter_map(|line| {
            let user = line.split_whitespace().next()?;
            if user.is_empty() {
                None
            } else {
                Some(user.to_string())
            }
        })
        .collect()
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn test_collect_returns_expected_keys() {
        let val = collect().unwrap();
        let obj = val.as_object().unwrap();
        assert!(obj.contains_key("count"));
        assert!(obj.contains_key("list"));
        assert!(obj["count"].is_number());
        assert!(obj["list"].is_array());
    }
}
