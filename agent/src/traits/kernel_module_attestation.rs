// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

//! Kernel module attestation trait collector (Linux-only).
//!
//! Enumerates loaded kernel modules from `/proc/modules` and correlates
//! each with `/sys/module/<name>/taint` to determine signature status.
//! Pure sysfs reads — no subprocess spawning, no modinfo calls — so the
//! collector is cheap enough to run on every host without violating the
//! "invisible to host" rule.
//!
//! Taint flags we interpret:
//! - `E` — unsigned module loaded
//! - `X` — module with bad signature loaded
//! - `P` — proprietary module (informational, not failure)
//! - `O` — out-of-tree module (informational, not failure)

use anyhow::Result;
use serde_json::{json, Value};

/// Top-level collector entry: reads `/proc/modules` and, for each module,
/// reads the matching taint flags from sysfs. Returns the standard
/// `{"modules": [...], "summary": {...}}` shape.
pub fn collect() -> Result<Value> {
    let modules = collect_from_roots("/proc/modules", "/sys/module");
    let summary = summarize(&modules);
    Ok(json!({
        "modules": modules,
        "summary": summary,
    }))
}

/// Reader split from `collect()` so tests can pass fixture paths for
/// both `/proc/modules` and the `/sys/module` directory root.
fn collect_from_roots(proc_modules: &str, sys_module_root: &str) -> Vec<Value> {
    let content = match std::fs::read_to_string(proc_modules) {
        Ok(c) => c,
        Err(_) => return Vec::new(),
    };

    let mut out = Vec::new();
    for line in content.lines() {
        if let Some(entry) = parse_proc_modules_line(line) {
            let taint = read_taint(sys_module_root, &entry.name);
            let signature_status = classify_signature(&taint);
            out.push(json!({
                "name": entry.name,
                "size": entry.size,
                "use_count": entry.use_count,
                "state": entry.state,
                "taint": taint,
                "signature_status": signature_status,
            }));
        }
    }
    out
}

/// Parsed row from `/proc/modules`. The file has fixed column order:
/// `name size use_count [used_by] state offset`.
struct ProcModulesRow {
    name: String,
    size: u64,
    use_count: u32,
    state: String,
}

fn parse_proc_modules_line(line: &str) -> Option<ProcModulesRow> {
    let fields: Vec<&str> = line.split_whitespace().collect();
    if fields.len() < 4 {
        return None;
    }
    let name = fields[0].to_string();
    let size: u64 = fields[1].parse().ok()?;
    let use_count: u32 = fields[2].parse().ok()?;
    // The "used_by" field (index 3) is a comma-separated list that can
    // also be "-" when nothing depends on the module. The state follows.
    // Count backwards from the end for robustness: last field is offset,
    // second-to-last is state.
    if fields.len() < 6 {
        return None;
    }
    let state = fields[fields.len() - 2].to_string();
    Some(ProcModulesRow {
        name,
        size,
        use_count,
        state,
    })
}

/// Read `/sys/module/<name>/taint` and return the trimmed taint string
/// (empty if the file doesn't exist or is empty, which is the common
/// clean case).
fn read_taint(sys_module_root: &str, name: &str) -> String {
    let path = format!("{}/{}/taint", sys_module_root, name);
    std::fs::read_to_string(&path)
        .map(|s| s.trim().to_string())
        .unwrap_or_default()
}

/// Map a taint flag string to one of `signed`, `unsigned`, or
/// `bad_signature`. Taint flags unrelated to signing (P, O, F, etc.) do
/// not affect classification — a proprietary signed module reports
/// `signed`.
fn classify_signature(taint: &str) -> &'static str {
    if taint.contains('X') {
        "bad_signature"
    } else if taint.contains('E') {
        "unsigned"
    } else {
        "signed"
    }
}

/// Aggregate the per-module rows into a summary block for dashboard use.
fn summarize(modules: &[Value]) -> Value {
    let total = modules.len();
    let mut signed = 0usize;
    let mut unsigned = 0usize;
    let mut bad_signature = 0usize;
    let mut proprietary = 0usize;
    let mut out_of_tree = 0usize;

    for m in modules {
        match m["signature_status"].as_str().unwrap_or("") {
            "signed" => signed += 1,
            "unsigned" => unsigned += 1,
            "bad_signature" => bad_signature += 1,
            _ => {}
        }
        let taint = m["taint"].as_str().unwrap_or("");
        if taint.contains('P') {
            proprietary += 1;
        }
        if taint.contains('O') {
            out_of_tree += 1;
        }
    }

    json!({
        "total": total,
        "signed": signed,
        "unsigned": unsigned,
        "bad_signature": bad_signature,
        "proprietary": proprietary,
        "out_of_tree": out_of_tree,
    })
}

#[cfg(test)]
mod tests {
    use super::*;
    use std::fs;

    #[test]
    fn test_collect_returns_shape() {
        let val = collect().unwrap();
        let obj = val.as_object().unwrap();
        assert!(obj.contains_key("modules"));
        assert!(obj.contains_key("summary"));
    }

    #[test]
    fn test_parse_proc_modules_line() {
        let row = parse_proc_modules_line(
            "ext4 987136 3 crc32c_intel,mbcache,jbd2, Live 0x0000000000000000",
        )
        .unwrap();
        assert_eq!(row.name, "ext4");
        assert_eq!(row.size, 987136);
        assert_eq!(row.use_count, 3);
        assert_eq!(row.state, "Live");

        let row = parse_proc_modules_line("nvidia 55488512 47 - Live 0x0000000000000000").unwrap();
        assert_eq!(row.name, "nvidia");
        assert_eq!(row.use_count, 47);
        assert_eq!(row.state, "Live");
    }

    #[test]
    fn test_classify_signature() {
        assert_eq!(classify_signature(""), "signed");
        assert_eq!(classify_signature("PO"), "signed"); // proprietary + out-of-tree, still signed
        assert_eq!(classify_signature("E"), "unsigned");
        assert_eq!(classify_signature("PE"), "unsigned"); // mixed, E wins
        assert_eq!(classify_signature("X"), "bad_signature");
        assert_eq!(classify_signature("EX"), "bad_signature"); // X > E
    }

    #[test]
    fn test_collect_from_roots() {
        let dir = tempfile::tempdir().unwrap();
        let proc = dir.path().join("proc");
        let sys = dir.path().join("sys/module");
        fs::create_dir_all(&proc).unwrap();
        fs::create_dir_all(&sys).unwrap();

        fs::write(
            proc.join("modules"),
            "\
ext4 987136 3 crc32c_intel,mbcache,jbd2, Live 0x0000000000000000
nvidia 55488512 47 - Live 0x0000000000000000
bad_mod 1024 0 - Live 0x0000000000000000
",
        )
        .unwrap();

        // ext4: clean (no taint file — empty == signed)
        fs::create_dir_all(sys.join("ext4")).unwrap();
        // nvidia: proprietary + unsigned
        fs::create_dir_all(sys.join("nvidia")).unwrap();
        fs::write(sys.join("nvidia/taint"), "POE\n").unwrap();
        // bad_mod: bad signature
        fs::create_dir_all(sys.join("bad_mod")).unwrap();
        fs::write(sys.join("bad_mod/taint"), "X\n").unwrap();

        let modules = collect_from_roots(
            proc.join("modules").to_str().unwrap(),
            sys.to_str().unwrap(),
        );

        assert_eq!(modules.len(), 3);
        assert_eq!(modules[0]["name"], "ext4");
        assert_eq!(modules[0]["signature_status"], "signed");
        assert_eq!(modules[1]["name"], "nvidia");
        assert_eq!(modules[1]["signature_status"], "unsigned");
        assert_eq!(modules[1]["taint"], "POE");
        assert_eq!(modules[2]["name"], "bad_mod");
        assert_eq!(modules[2]["signature_status"], "bad_signature");

        let summary = summarize(&modules);
        assert_eq!(summary["total"], 3);
        assert_eq!(summary["signed"], 1);
        assert_eq!(summary["unsigned"], 1);
        assert_eq!(summary["bad_signature"], 1);
        assert_eq!(summary["proprietary"], 1);
        assert_eq!(summary["out_of_tree"], 1);
    }
}
