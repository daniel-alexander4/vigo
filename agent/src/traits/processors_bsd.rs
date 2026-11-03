// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: Apache-2.0

//! Processor info for macOS and BSD — uses sysctl.

use anyhow::Result;
use serde_json::{json, Value};

pub fn collect() -> Result<Value> {
    let count = super::sysctl_util::sysctl_u64("hw.ncpu").unwrap_or(0);
    let physical = super::sysctl_util::sysctl_u64("hw.physicalcpu").unwrap_or(count);

    // Try macOS-specific first, then generic BSD
    let model = super::sysctl_util::sysctl_string("machdep.cpu.brand_string");
    let model = if model.is_empty() {
        super::sysctl_util::sysctl_string("hw.model")
    } else {
        model
    };

    Ok(json!({
        "count": count,
        "physical": physical,
        "model": model,
    }))
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn test_collect_no_panic() {
        let _ = collect();
    }
}
