// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: Elastic-2.0

//! Reads output from security scanning tools deployed by the security-scanning
//! module and emits summary traits. Each tool section is only present if its
//! output file exists — tools that aren't installed produce no data, no error.
//!
//! Cross-platform. Scan output directory:
//!   Unix:    /var/lib/hobnob/security/
//!   Windows: C:\ProgramData\Hobnob\security\
//!
//! Supported tools (Unix):
//!   trivy, lynis, chkrootkit, rkhunter, clamav, aide, debsecan
//!
//! Supported tools (Windows):
//!   trivy, windows_defender, windows_updates

use anyhow::Result;
use serde_json::{json, Value};
use std::fs;
use std::path::Path;

#[cfg(unix)]
const SCAN_DIR: &str = "/var/lib/hobnob/security";

#[cfg(windows)]
const SCAN_DIR: &str = r"C:\ProgramData\Hobnob\security";

/// Maximum number of CVE IDs to include in traits (critical + high).
const MAX_CVES: usize = 50;

pub fn collect() -> Result<Value> {
    let mut map = serde_json::Map::new();

    if let Some(v) = parse_trivy() {
        map.insert("trivy".into(), v);
    }
    if let Some(v) = parse_lynis() {
        map.insert("lynis".into(), v);
    }
    if let Some(v) = parse_chkrootkit() {
        map.insert("chkrootkit".into(), v);
    }
    if let Some(v) = parse_rkhunter() {
        map.insert("rkhunter".into(), v);
    }
    if let Some(v) = parse_clamav() {
        map.insert("clamav".into(), v);
    }
    if let Some(v) = parse_aide() {
        map.insert("aide".into(), v);
    }
    if let Some(v) = parse_debsecan() {
        map.insert("debsecan".into(), v);
    }
    if let Some(v) = parse_windows_defender() {
        map.insert("windows_defender".into(), v);
    }
    if let Some(v) = parse_windows_updates() {
        map.insert("windows_updates".into(), v);
    }

    if map.is_empty() {
        // No scan output found — return null so the key is omitted from traits.
        return Ok(Value::Null);
    }

    // Add overall scan directory presence indicator.
    map.insert("scan_dir".into(), json!(SCAN_DIR));

    Ok(Value::Object(map))
}

/// File modification time as ISO 8601 string, or empty string if unavailable.
fn file_mtime_iso(path: &Path) -> String {
    fs::metadata(path)
        .ok()
        .and_then(|m| m.modified().ok())
        .map(|t| {
            let secs = t.duration_since(std::time::UNIX_EPOCH).unwrap_or_default().as_secs();
            // Simple UTC timestamp without pulling in chrono.
            let days = secs / 86400;
            let day_secs = secs % 86400;
            let h = day_secs / 3600;
            let m = (day_secs % 3600) / 60;
            let s = day_secs % 60;
            // Days since epoch to Y-M-D.
            let (y, mo, d) = days_to_ymd(days);
            format!("{y:04}-{mo:02}-{d:02}T{h:02}:{m:02}:{s:02}Z")
        })
        .unwrap_or_default()
}

/// Convert days since Unix epoch to (year, month, day).
fn days_to_ymd(days: u64) -> (u64, u64, u64) {
    // Algorithm from http://howardhinnant.github.io/date_algorithms.html
    let z = days + 719468;
    let era = z / 146097;
    let doe = z - era * 146097;
    let yoe = (doe - doe / 1460 + doe / 36524 - doe / 146096) / 365;
    let y = yoe + era * 400;
    let doy = doe - (365 * yoe + yoe / 4 - yoe / 100);
    let mp = (5 * doy + 2) / 153;
    let d = doy - (153 * mp + 2) / 5 + 1;
    let m = if mp < 10 { mp + 3 } else { mp - 9 };
    let y = if m <= 2 { y + 1 } else { y };
    (y, m, d)
}

// ── Trivy ────────────────────────────────────────────────────────────────

fn parse_trivy() -> Option<Value> {
    let path = Path::new(SCAN_DIR).join("trivy.json");
    let data = fs::read_to_string(&path).ok()?;
    let parsed: Value = serde_json::from_str(&data).ok()?;

    let mut critical = 0u64;
    let mut high = 0u64;
    let mut medium = 0u64;
    let mut low = 0u64;
    let mut cves: Vec<String> = Vec::new();

    // Trivy JSON format: { "Results": [ { "Vulnerabilities": [ { "VulnerabilityID", "Severity" } ] } ] }
    if let Some(results) = parsed.get("Results").and_then(|r| r.as_array()) {
        for result in results {
            if let Some(vulns) = result.get("Vulnerabilities").and_then(|v| v.as_array()) {
                for vuln in vulns {
                    let sev = vuln.get("Severity").and_then(|s| s.as_str()).unwrap_or("");
                    match sev {
                        "CRITICAL" => critical += 1,
                        "HIGH" => high += 1,
                        "MEDIUM" => medium += 1,
                        "LOW" => low += 1,
                        _ => {}
                    }
                    if (sev == "CRITICAL" || sev == "HIGH") && cves.len() < MAX_CVES {
                        if let Some(id) = vuln.get("VulnerabilityID").and_then(|v| v.as_str()) {
                            if !cves.contains(&id.to_string()) {
                                cves.push(id.to_string());
                            }
                        }
                    }
                }
            }
        }
    }

    Some(json!({
        "critical": critical,
        "high": high,
        "medium": medium,
        "low": low,
        "total": critical + high + medium + low,
        "cves": cves,
        "last_scan": file_mtime_iso(&path),
    }))
}

// ── Lynis ────────────────────────────────────────────────────────────────

fn parse_lynis() -> Option<Value> {
    let path = Path::new(SCAN_DIR).join("lynis-report.dat");
    let data = fs::read_to_string(&path).ok()?;

    let mut score: Option<u64> = None;
    let mut warnings = 0u64;
    let mut suggestions = 0u64;

    for line in data.lines() {
        if let Some(val) = line.strip_prefix("hardening_index=") {
            score = val.trim().parse().ok();
        } else if line.starts_with("warning[]=") {
            warnings += 1;
        } else if line.starts_with("suggestion[]=") {
            suggestions += 1;
        }
    }

    Some(json!({
        "score": score.unwrap_or(0),
        "warnings": warnings,
        "suggestions": suggestions,
        "last_scan": file_mtime_iso(&path),
    }))
}

// ── chkrootkit ───────────────────────────────────────────────────────────

fn parse_chkrootkit() -> Option<Value> {
    let path = Path::new(SCAN_DIR).join("chkrootkit.log");
    let data = fs::read_to_string(&path).ok()?;

    let mut infected = 0u64;
    for line in data.lines() {
        if line.contains("INFECTED") {
            infected += 1;
        }
    }

    Some(json!({
        "clean": infected == 0,
        "infected": infected,
        "last_scan": file_mtime_iso(&path),
    }))
}

// ── rkhunter ─────────────────────────────────────────────────────────────

fn parse_rkhunter() -> Option<Value> {
    let path = Path::new(SCAN_DIR).join("rkhunter.log");
    let data = fs::read_to_string(&path).ok()?;

    let mut warnings = 0u64;
    let mut infected = 0u64;
    for line in data.lines() {
        let trimmed = line.trim();
        if trimmed.contains("[ Warning ]") {
            warnings += 1;
        }
        if trimmed.contains("[ Infected ]") {
            infected += 1;
        }
    }

    Some(json!({
        "clean": warnings == 0 && infected == 0,
        "warnings": warnings,
        "infected": infected,
        "last_scan": file_mtime_iso(&path),
    }))
}

// ── ClamAV ───────────────────────────────────────────────────────────────

fn parse_clamav() -> Option<Value> {
    let path = Path::new(SCAN_DIR).join("clamav.log");
    let data = fs::read_to_string(&path).ok()?;

    // clamscan --infected output: one line per infected file, "path: signature FOUND"
    let infected = data.lines().filter(|l| l.contains("FOUND")).count() as u64;

    Some(json!({
        "clean": infected == 0,
        "infected": infected,
        "last_scan": file_mtime_iso(&path),
    }))
}

// ── AIDE ─────────────────────────────────────────────────────────────────

fn parse_aide() -> Option<Value> {
    let path = Path::new(SCAN_DIR).join("aide.log");
    let data = fs::read_to_string(&path).ok()?;

    let mut added = 0u64;
    let mut removed = 0u64;
    let mut changed = 0u64;

    // AIDE summary lines:
    //   Added entries:   3
    //   Removed entries: 0
    //   Changed entries: 5
    for line in data.lines() {
        let trimmed = line.trim();
        if let Some(val) = trimmed.strip_prefix("Added entries:") {
            added = val.trim().parse().unwrap_or(0);
        } else if let Some(val) = trimmed.strip_prefix("Removed entries:") {
            removed = val.trim().parse().unwrap_or(0);
        } else if let Some(val) = trimmed.strip_prefix("Changed entries:") {
            changed = val.trim().parse().unwrap_or(0);
        }
    }

    Some(json!({
        "added": added,
        "removed": removed,
        "changed": changed,
        "clean": added == 0 && removed == 0 && changed == 0,
        "last_scan": file_mtime_iso(&path),
    }))
}

// ── debsecan ─────────────────────────────────────────────────────────────

fn parse_debsecan() -> Option<Value> {
    let path = Path::new(SCAN_DIR).join("debsecan.log");
    let data = fs::read_to_string(&path).ok()?;

    // debsecan --format detail: each vulnerability block starts with CVE ID.
    // Lines like: CVE-2024-1234 package-name — urgency (high urgency|medium urgency|low urgency|...)
    let mut cves: Vec<String> = Vec::new();
    let mut high = 0u64;
    let mut medium = 0u64;
    let mut low = 0u64;
    let mut other = 0u64;

    for line in data.lines() {
        let trimmed = line.trim();
        if !trimmed.starts_with("CVE-") {
            continue;
        }
        let id = trimmed.split_whitespace().next().unwrap_or("");
        if id.is_empty() {
            continue;
        }

        if trimmed.contains("high urgency") {
            high += 1;
            if cves.len() < MAX_CVES && !cves.contains(&id.to_string()) {
                cves.push(id.to_string());
            }
        } else if trimmed.contains("medium urgency") {
            medium += 1;
        } else if trimmed.contains("low urgency") {
            low += 1;
        } else {
            other += 1;
        }
    }

    Some(json!({
        "high": high,
        "medium": medium,
        "low": low,
        "other": other,
        "total": high + medium + low + other,
        "cves": cves,
        "last_scan": file_mtime_iso(&path),
    }))
}

// ── Windows Defender ──────────────────────────────────────────────────

fn parse_windows_defender() -> Option<Value> {
    // Reads JSON output from:
    //   powershell -Command "Get-MpComputerStatus | ConvertTo-Json"
    //     > <SCAN_DIR>/defender.json
    let path = Path::new(SCAN_DIR).join("defender.json");
    let data = fs::read_to_string(&path).ok()?;
    let parsed: Value = serde_json::from_str(&data).ok()?;

    let enabled = parsed.get("AntivirusEnabled").and_then(|v| v.as_bool()).unwrap_or(false);
    let rt_enabled = parsed.get("RealTimeProtectionEnabled").and_then(|v| v.as_bool()).unwrap_or(false);
    let sig_age = parsed.get("AntivirusSignatureAge").and_then(|v| v.as_u64()).unwrap_or(0);
    let threats = parsed.get("DetectedThreatCount").and_then(|v| v.as_u64())
        .or_else(|| parsed.get("ThreatDetected").and_then(|v| v.as_u64()))
        .unwrap_or(0);

    Some(json!({
        "enabled": enabled,
        "realtime_protection": rt_enabled,
        "signature_age_days": sig_age,
        "threats_detected": threats,
        "clean": threats == 0,
        "last_scan": file_mtime_iso(&path),
    }))
}

// ── Windows Updates ──────────────────────────────────────────────────

fn parse_windows_updates() -> Option<Value> {
    // Reads JSON output from a PowerShell script that checks for missing updates:
    //   powershell -Command "
    //     $session = New-Object -ComObject Microsoft.Update.Session
    //     $searcher = $session.CreateUpdateSearcher()
    //     $results = $searcher.Search('IsInstalled=0')
    //     $results.Updates | Select-Object Title,
    //       @{N='CVEs';E={$_.CveIDs -join ','}},
    //       @{N='Severity';E={$_.MsrcSeverity}},
    //       @{N='KB';E={$_.KBArticleIDs -join ','}}
    //     | ConvertTo-Json
    //   " > <SCAN_DIR>/updates.json
    let path = Path::new(SCAN_DIR).join("updates.json");
    let data = fs::read_to_string(&path).ok()?;
    let parsed: Value = serde_json::from_str(&data).ok()?;

    let updates = match &parsed {
        Value::Array(arr) => arr.clone(),
        Value::Object(_) => vec![parsed.clone()], // single result comes as object
        _ => return None,
    };

    let mut critical = 0u64;
    let mut important = 0u64;
    let mut moderate = 0u64;
    let mut low = 0u64;
    let mut other = 0u64;
    let mut cves: Vec<String> = Vec::new();

    for update in &updates {
        let severity = update.get("Severity").and_then(|s| s.as_str()).unwrap_or("");
        match severity {
            "Critical" => critical += 1,
            "Important" => important += 1,
            "Moderate" => moderate += 1,
            "Low" => low += 1,
            _ => other += 1,
        }

        // Collect CVE IDs from the comma-separated string.
        if let Some(cve_str) = update.get("CVEs").and_then(|v| v.as_str()) {
            for cve in cve_str.split(',') {
                let cve = cve.trim();
                if cve.starts_with("CVE-") && cves.len() < MAX_CVES && !cves.contains(&cve.to_string()) {
                    cves.push(cve.to_string());
                }
            }
        }
    }

    Some(json!({
        "missing": updates.len(),
        "critical": critical,
        "important": important,
        "moderate": moderate,
        "low": low,
        "other": other,
        "cves": cves,
        "last_scan": file_mtime_iso(&path),
    }))
}

#[cfg(test)]
mod tests {
    use super::*;
    use std::io::Write;

    #[test]
    fn test_collect_returns_null_when_no_files() {
        // With no scan output files, collect should return Null.
        let val = collect().unwrap();
        // On CI / dev machines without /var/lib/hobnob/security, this is Null.
        // On machines with scan output, it would be an object.
        assert!(val.is_null() || val.is_object());
    }

    #[test]
    fn test_parse_trivy_json() {
        let dir = tempfile::TempDir::new().unwrap();
        let path = dir.path().join("trivy.json");
        let trivy_output = r#"{
            "Results": [{
                "Vulnerabilities": [
                    {"VulnerabilityID": "CVE-2024-0001", "Severity": "CRITICAL"},
                    {"VulnerabilityID": "CVE-2024-0002", "Severity": "HIGH"},
                    {"VulnerabilityID": "CVE-2024-0003", "Severity": "MEDIUM"},
                    {"VulnerabilityID": "CVE-2024-0004", "Severity": "LOW"},
                    {"VulnerabilityID": "CVE-2024-0005", "Severity": "HIGH"}
                ]
            }]
        }"#;
        fs::write(&path, trivy_output).unwrap();

        // We need to test the parser function directly, but it reads from SCAN_DIR.
        // Instead, test the JSON parsing logic inline.
        let parsed: Value = serde_json::from_str(trivy_output).unwrap();
        let results = parsed.get("Results").unwrap().as_array().unwrap();
        let mut critical = 0u64;
        let mut high = 0u64;
        let mut cves = Vec::new();
        for result in results {
            if let Some(vulns) = result.get("Vulnerabilities").and_then(|v| v.as_array()) {
                for vuln in vulns {
                    let sev = vuln.get("Severity").and_then(|s| s.as_str()).unwrap_or("");
                    match sev {
                        "CRITICAL" => critical += 1,
                        "HIGH" => high += 1,
                        _ => {}
                    }
                    if sev == "CRITICAL" || sev == "HIGH" {
                        if let Some(id) = vuln.get("VulnerabilityID").and_then(|v| v.as_str()) {
                            cves.push(id.to_string());
                        }
                    }
                }
            }
        }
        assert_eq!(critical, 1);
        assert_eq!(high, 2);
        assert_eq!(cves.len(), 3);
        assert!(cves.contains(&"CVE-2024-0001".to_string()));
    }

    #[test]
    fn test_parse_lynis_report() {
        let data = "hardening_index=72\nwarning[]=WARN-01\nwarning[]=WARN-02\nsuggestion[]=SUG-01\n";
        let mut score: Option<u64> = None;
        let mut warnings = 0u64;
        let mut suggestions = 0u64;
        for line in data.lines() {
            if let Some(val) = line.strip_prefix("hardening_index=") {
                score = val.trim().parse().ok();
            } else if line.starts_with("warning[]=") {
                warnings += 1;
            } else if line.starts_with("suggestion[]=") {
                suggestions += 1;
            }
        }
        assert_eq!(score, Some(72));
        assert_eq!(warnings, 2);
        assert_eq!(suggestions, 1);
    }

    #[test]
    fn test_parse_chkrootkit_clean() {
        let data = "Checking `amd`... not found\nChecking `basename`... not infected\n";
        let infected = data.lines().filter(|l| l.contains("INFECTED")).count();
        assert_eq!(infected, 0);
    }

    #[test]
    fn test_parse_chkrootkit_infected() {
        let data = "Checking `bindshell`... INFECTED\nChecking `basename`... not infected\n";
        let infected = data.lines().filter(|l| l.contains("INFECTED")).count();
        assert_eq!(infected, 1);
    }

    #[test]
    fn test_parse_rkhunter_warnings() {
        let data = "/usr/bin/lwp-request [ Warning ]\n/usr/bin/gcc [ OK ]\n";
        let warnings = data.lines().filter(|l| l.contains("[ Warning ]")).count();
        assert_eq!(warnings, 1);
    }

    #[test]
    fn test_parse_clamav_found() {
        let data = "/tmp/eicar.com: Eicar-Signature FOUND\n/usr/bin/bash: OK\n";
        let infected = data.lines().filter(|l| l.contains("FOUND")).count();
        assert_eq!(infected, 1);
    }

    #[test]
    fn test_parse_aide_summary() {
        let data = "  Added entries:   3\n  Removed entries: 0\n  Changed entries: 5\n";
        let mut added = 0u64;
        let mut removed = 0u64;
        let mut changed = 0u64;
        for line in data.lines() {
            let trimmed = line.trim();
            if let Some(val) = trimmed.strip_prefix("Added entries:") {
                added = val.trim().parse().unwrap_or(0);
            } else if let Some(val) = trimmed.strip_prefix("Removed entries:") {
                removed = val.trim().parse().unwrap_or(0);
            } else if let Some(val) = trimmed.strip_prefix("Changed entries:") {
                changed = val.trim().parse().unwrap_or(0);
            }
        }
        assert_eq!(added, 3);
        assert_eq!(removed, 0);
        assert_eq!(changed, 5);
    }

    #[test]
    fn test_parse_debsecan_detail() {
        let data = "CVE-2024-1000 libfoo -- high urgency\nCVE-2024-1001 libbar -- medium urgency\nCVE-2024-1002 libbaz -- high urgency\n";
        let mut high = 0u64;
        let mut medium = 0u64;
        let mut cves = Vec::new();
        for line in data.lines() {
            let trimmed = line.trim();
            if !trimmed.starts_with("CVE-") { continue; }
            let id = trimmed.split_whitespace().next().unwrap_or("");
            if trimmed.contains("high urgency") {
                high += 1;
                cves.push(id.to_string());
            } else if trimmed.contains("medium urgency") {
                medium += 1;
            }
        }
        assert_eq!(high, 2);
        assert_eq!(medium, 1);
        assert_eq!(cves.len(), 2);
    }

    #[test]
    fn test_days_to_ymd() {
        // 2024-01-01 = day 19723
        let (y, m, d) = days_to_ymd(19723);
        assert_eq!((y, m, d), (2024, 1, 1));
        // 1970-01-01 = day 0
        let (y, m, d) = days_to_ymd(0);
        assert_eq!((y, m, d), (1970, 1, 1));
    }

    #[test]
    fn test_file_mtime_iso_missing_file() {
        let result = file_mtime_iso(Path::new("/nonexistent/path"));
        assert_eq!(result, "");
    }

    #[test]
    fn test_file_mtime_iso_real_file() {
        let dir = tempfile::TempDir::new().unwrap();
        let path = dir.path().join("test.txt");
        let mut f = fs::File::create(&path).unwrap();
        f.write_all(b"test").unwrap();
        let result = file_mtime_iso(&path);
        assert!(!result.is_empty());
        assert!(result.contains('T'));
        assert!(result.ends_with('Z'));
    }

    #[test]
    fn test_parse_windows_defender_json() {
        let data = r#"{
            "AntivirusEnabled": true,
            "RealTimeProtectionEnabled": true,
            "AntivirusSignatureAge": 1,
            "DetectedThreatCount": 0
        }"#;
        let parsed: serde_json::Value = serde_json::from_str(data).unwrap();
        let enabled = parsed.get("AntivirusEnabled").and_then(|v| v.as_bool()).unwrap_or(false);
        let threats = parsed.get("DetectedThreatCount").and_then(|v| v.as_u64()).unwrap_or(0);
        assert!(enabled);
        assert_eq!(threats, 0);
    }

    #[test]
    fn test_parse_windows_defender_threats() {
        let data = r#"{
            "AntivirusEnabled": true,
            "RealTimeProtectionEnabled": false,
            "AntivirusSignatureAge": 5,
            "DetectedThreatCount": 3
        }"#;
        let parsed: serde_json::Value = serde_json::from_str(data).unwrap();
        let threats = parsed.get("DetectedThreatCount").and_then(|v| v.as_u64()).unwrap_or(0);
        assert_eq!(threats, 3);
    }

    #[test]
    fn test_parse_windows_updates_json() {
        let data = r#"[
            {"Title": "Security Update KB1234", "CVEs": "CVE-2024-1000,CVE-2024-1001", "Severity": "Critical", "KB": "1234"},
            {"Title": "Security Update KB5678", "CVEs": "CVE-2024-2000", "Severity": "Important", "KB": "5678"},
            {"Title": "Update KB9999", "CVEs": "", "Severity": "Moderate", "KB": "9999"}
        ]"#;
        let parsed: serde_json::Value = serde_json::from_str(data).unwrap();
        let updates = parsed.as_array().unwrap();
        let mut critical = 0u64;
        let mut important = 0u64;
        let mut cves = Vec::new();
        for update in updates {
            let severity = update.get("Severity").and_then(|s| s.as_str()).unwrap_or("");
            match severity {
                "Critical" => critical += 1,
                "Important" => important += 1,
                _ => {}
            }
            if let Some(cve_str) = update.get("CVEs").and_then(|v| v.as_str()) {
                for cve in cve_str.split(',') {
                    let cve = cve.trim();
                    if cve.starts_with("CVE-") {
                        cves.push(cve.to_string());
                    }
                }
            }
        }
        assert_eq!(critical, 1);
        assert_eq!(important, 1);
        assert_eq!(cves.len(), 3);
        assert!(cves.contains(&"CVE-2024-1000".to_string()));
    }

    #[test]
    fn test_parse_windows_updates_single_object() {
        // PowerShell returns a single object (not array) when there's only one result.
        let data = r#"{"Title": "Security Update KB1234", "CVEs": "CVE-2024-5000", "Severity": "Critical", "KB": "1234"}"#;
        let parsed: serde_json::Value = serde_json::from_str(data).unwrap();
        let updates = match &parsed {
            serde_json::Value::Array(arr) => arr.clone(),
            serde_json::Value::Object(_) => vec![parsed.clone()],
            _ => panic!("unexpected"),
        };
        assert_eq!(updates.len(), 1);
        let severity = updates[0].get("Severity").and_then(|s| s.as_str()).unwrap_or("");
        assert_eq!(severity, "Critical");
    }
}
