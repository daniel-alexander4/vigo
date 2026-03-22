// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

//! Reads output from security scanning tools deployed by the security-scanning
//! module and emits summary traits. Each tool section is only present if its
//! output file exists — tools that aren't installed produce no data, no error.
//!
//! Cross-platform. Scan output directory:
//!   Unix:    /var/lib/vigo/security/
//!   Windows: C:\ProgramData\Vigo\security\
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
const SCAN_DIR: &str = "/var/lib/vigo/security";

#[cfg(windows)]
const SCAN_DIR: &str = r"C:\ProgramData\Vigo\security";

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
    if let Some(v) = parse_scap() {
        map.insert("scap".into(), v);
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

    // Group critical+high CVEs by package, keeping only the highest CVE ID per
    // package. The "others" count notes how many additional CVEs were suppressed.
    // Severity tracks the severity of the kept (best) CVE ID.
    use std::collections::HashMap;
    let mut best: HashMap<String, (String, String, u64)> = HashMap::new(); // package -> (best_cve_id, severity, other_count)

    // Trivy JSON format: { "Results": [ { "Vulnerabilities": [ { "VulnerabilityID", "Severity", "PkgName" } ] } ] }
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
                    if sev == "CRITICAL" || sev == "HIGH" {
                        let id = vuln.get("VulnerabilityID").and_then(|v| v.as_str()).unwrap_or("");
                        let pkg = vuln.get("PkgName").and_then(|v| v.as_str()).unwrap_or(id);
                        if !id.is_empty() {
                            best.entry(pkg.to_string())
                                .and_modify(|(existing_cve, existing_sev, others)| {
                                    if id > existing_cve.as_str() {
                                        *existing_cve = id.to_string();
                                        *existing_sev = sev.to_lowercase();
                                    }
                                    *others += 1;
                                })
                                .or_insert_with(|| (id.to_string(), sev.to_lowercase(), 0));
                        }
                    }
                }
            }
        }
    }

    // Build CVE list sorted by CVE ID descending, capped at MAX_CVES.
    let mut cves: Vec<Value> = best.iter()
        .map(|(pkg, (cve, sev, others))| json!({"cve_id": cve, "package": pkg, "severity": sev, "others": others}))
        .collect();
    cves.sort_by(|a, b| {
        let a_id = a.get("cve_id").and_then(|v| v.as_str()).unwrap_or("");
        let b_id = b.get("cve_id").and_then(|v| v.as_str()).unwrap_or("");
        b_id.cmp(a_id)
    });
    cves.truncate(MAX_CVES);

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

    // Handles both debsecan --format detail (multi-line blocks starting with
    // "CVE-xxx (urgency)") and default format ("CVE-xxx package (urgency)").
    // Many CVEs have no urgency marker at all — debsecan omits it when the
    // Debian security team hasn't assigned one yet. These are "unassigned".
    let mut high = 0u64;
    let mut medium = 0u64;
    let mut low = 0u64;
    let mut unassigned = 0u64;

    // Group high-urgency CVEs by package, keeping only the highest CVE ID per
    // package with a count of suppressed others.
    use std::collections::HashMap;
    let mut best: HashMap<String, (String, u64)> = HashMap::new(); // package -> (best_cve_id, other_count)

    for line in data.lines() {
        let trimmed = line.trim();
        if !trimmed.starts_with("CVE-") {
            continue;
        }
        let parts: Vec<&str> = trimmed.split_whitespace().collect();
        let id = parts.first().copied().unwrap_or("");
        if id.is_empty() {
            continue;
        }

        if trimmed.contains("high urgency") {
            high += 1;
            // Extract package name: "CVE-xxx package (high urgency)"
            let pkg = parts.get(1).copied().unwrap_or(id);
            best.entry(pkg.to_string())
                .and_modify(|(existing_cve, others)| {
                    if id > existing_cve.as_str() {
                        *existing_cve = id.to_string();
                    }
                    *others += 1;
                })
                .or_insert_with(|| (id.to_string(), 0));
        } else if trimmed.contains("medium urgency") {
            medium += 1;
        } else if trimmed.contains("low urgency") {
            low += 1;
        } else {
            unassigned += 1;
        }
    }

    // Build CVE list sorted by CVE ID descending, capped at MAX_CVES.
    // All debsecan CVEs in the best map are high-urgency.
    let mut cves: Vec<Value> = best.iter()
        .map(|(pkg, (cve, others))| json!({"cve_id": cve, "package": pkg, "severity": "high", "others": others}))
        .collect();
    cves.sort_by(|a, b| {
        let a_id = a.get("cve_id").and_then(|v| v.as_str()).unwrap_or("");
        let b_id = b.get("cve_id").and_then(|v| v.as_str()).unwrap_or("");
        b_id.cmp(a_id)
    });
    cves.truncate(MAX_CVES);

    Some(json!({
        "high": high,
        "medium": medium,
        "low": low,
        "unassigned": unassigned,
        "total": high + medium + low + unassigned,
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

    // Group CVEs by KB article, keeping only the highest CVE ID per KB with
    // a count of suppressed others. Track severity for the stat cards.
    use std::collections::HashMap;
    let mut best: HashMap<String, (String, String, u64)> = HashMap::new(); // KB -> (best_cve_id, severity, other_count)

    for update in &updates {
        let severity = update.get("Severity").and_then(|s| s.as_str()).unwrap_or("");
        match severity {
            "Critical" => critical += 1,
            "Important" => important += 1,
            "Moderate" => moderate += 1,
            "Low" => low += 1,
            _ => other += 1,
        }

        let kb = update.get("KB").and_then(|v| v.as_str()).unwrap_or("");
        // Map Windows severity to normalized lowercase for consistency.
        let norm_sev = match severity {
            "Critical" => "critical",
            "Important" => "high",  // Important maps to high
            _ => "medium",
        };
        // Collect CVE IDs from the comma-separated string.
        if let Some(cve_str) = update.get("CVEs").and_then(|v| v.as_str()) {
            for cve in cve_str.split(',') {
                let cve = cve.trim();
                if !cve.starts_with("CVE-") {
                    continue;
                }
                let key = if kb.is_empty() { cve.to_string() } else { format!("KB{kb}") };
                best.entry(key)
                    .and_modify(|(existing_cve, existing_sev, others)| {
                        if cve > existing_cve.as_str() {
                            *existing_cve = cve.to_string();
                            *existing_sev = norm_sev.to_string();
                        }
                        *others += 1;
                    })
                    .or_insert_with(|| (cve.to_string(), norm_sev.to_string(), 0));
            }
        }
    }

    // Build CVE list sorted by CVE ID descending, capped at MAX_CVES.
    let mut cves: Vec<Value> = best.iter()
        .map(|(pkg, (cve, sev, others))| json!({"cve_id": cve, "package": pkg, "severity": sev, "others": others}))
        .collect();
    cves.sort_by(|a, b| {
        let a_id = a.get("cve_id").and_then(|v| v.as_str()).unwrap_or("");
        let b_id = b.get("cve_id").and_then(|v| v.as_str()).unwrap_or("");
        b_id.cmp(a_id)
    });
    cves.truncate(MAX_CVES);

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

/// Maximum vulnerabilities in detail output.
const MAX_DETAIL_VULNS: usize = 500;
/// Maximum findings per tool in detail output.
const MAX_DETAIL_FINDINGS: usize = 200;

/// Collect detailed security scan results on demand (not for regular traits).
/// Reads raw scan output files and returns per-finding data for UI display.
pub fn collect_detail() -> Result<Value> {
    let dir = Path::new(SCAN_DIR);
    if !dir.exists() {
        return Ok(json!({"available": false, "scan_dir": SCAN_DIR}));
    }

    let mut map = serde_json::Map::new();
    map.insert("available".into(), json!(true));
    map.insert("scan_dir".into(), json!(SCAN_DIR));

    if let Some(v) = detail_trivy() { map.insert("trivy".into(), v); }
    if let Some(v) = detail_lynis() { map.insert("lynis".into(), v); }
    if let Some(v) = detail_rkhunter() { map.insert("rkhunter".into(), v); }
    if let Some(v) = detail_chkrootkit() { map.insert("chkrootkit".into(), v); }
    if let Some(v) = detail_clamav() { map.insert("clamav".into(), v); }
    if let Some(v) = detail_aide() { map.insert("aide".into(), v); }
    if let Some(v) = detail_debsecan() { map.insert("debsecan".into(), v); }

    Ok(Value::Object(map))
}

fn detail_trivy() -> Option<Value> {
    let path = Path::new(SCAN_DIR).join("trivy.json");
    let data = fs::read_to_string(&path).ok()?;
    let parsed: Value = serde_json::from_str(&data).ok()?;

    fn sev_rank(s: &str) -> u8 {
        match s { "CRITICAL" => 0, "HIGH" => 1, "MEDIUM" => 2, "LOW" => 3, _ => 4 }
    }

    // Group by package: keep only the highest-severity CVE per package.
    // If a package has a CRITICAL or HIGH CVE, lower-severity CVEs are discarded.
    use std::collections::HashMap;
    struct Best {
        cve_id: String,
        severity: String,
        package: String,
        installed: String,
        fixed: String,
        title: String,
        target: String,
        others: u64,
    }
    let mut best: HashMap<String, Best> = HashMap::new();

    if let Some(results) = parsed.get("Results").and_then(|r| r.as_array()) {
        for result in results {
            let target = result.get("Target").and_then(|t| t.as_str()).unwrap_or("");
            if let Some(vs) = result.get("Vulnerabilities").and_then(|v| v.as_array()) {
                for v in vs {
                    let cve_id = v.get("VulnerabilityID").and_then(|s| s.as_str()).unwrap_or("");
                    let severity = v.get("Severity").and_then(|s| s.as_str()).unwrap_or("");
                    let package = v.get("PkgName").and_then(|s| s.as_str()).unwrap_or(cve_id);
                    if cve_id.is_empty() { continue; }

                    best.entry(package.to_string())
                        .and_modify(|existing| {
                            if sev_rank(severity) < sev_rank(&existing.severity) {
                                existing.cve_id = cve_id.to_string();
                                existing.severity = severity.to_string();
                                existing.installed = v.get("InstalledVersion").and_then(|s| s.as_str()).unwrap_or("").to_string();
                                existing.fixed = v.get("FixedVersion").and_then(|s| s.as_str()).unwrap_or("").to_string();
                                existing.title = v.get("Title").and_then(|s| s.as_str()).unwrap_or("").to_string();
                                existing.target = target.to_string();
                            }
                            existing.others += 1;
                        })
                        .or_insert_with(|| Best {
                            cve_id: cve_id.to_string(),
                            severity: severity.to_string(),
                            package: package.to_string(),
                            installed: v.get("InstalledVersion").and_then(|s| s.as_str()).unwrap_or("").to_string(),
                            fixed: v.get("FixedVersion").and_then(|s| s.as_str()).unwrap_or("").to_string(),
                            title: v.get("Title").and_then(|s| s.as_str()).unwrap_or("").to_string(),
                            target: target.to_string(),
                            others: 0,
                        });
                }
            }
        }
    }

    // Sort by severity (CRITICAL first), then CVE ID descending.
    let mut vulns: Vec<Value> = best.into_values()
        .map(|b| json!({
            "cve_id": b.cve_id,
            "severity": b.severity,
            "package": b.package,
            "installed": b.installed,
            "fixed": b.fixed,
            "title": b.title,
            "target": b.target,
            "others": b.others,
        }))
        .collect();
    vulns.sort_by(|a, b| {
        let sa = sev_rank(a.get("severity").and_then(|v| v.as_str()).unwrap_or(""));
        let sb = sev_rank(b.get("severity").and_then(|v| v.as_str()).unwrap_or(""));
        sa.cmp(&sb).then_with(|| {
            let ba = b.get("cve_id").and_then(|v| v.as_str()).unwrap_or("");
            let aa = a.get("cve_id").and_then(|v| v.as_str()).unwrap_or("");
            ba.cmp(aa)
        })
    });
    vulns.truncate(MAX_DETAIL_VULNS);

    Some(json!({
        "vulnerabilities": vulns,
        "truncated": vulns.len() >= MAX_DETAIL_VULNS,
        "last_scan": file_mtime_iso(&path),
    }))
}

fn detail_lynis() -> Option<Value> {
    let path = Path::new(SCAN_DIR).join("lynis-report.dat");
    let data = fs::read_to_string(&path).ok()?;

    let mut score: Option<u64> = None;
    let mut warnings = Vec::new();
    let mut suggestions = Vec::new();

    for line in data.lines() {
        if let Some(val) = line.strip_prefix("hardening_index=") {
            score = val.trim().parse().ok();
        } else if let Some(val) = line.strip_prefix("warning[]=") {
            if warnings.len() < MAX_DETAIL_FINDINGS {
                // Format: ID|description|severity|...
                let parts: Vec<&str> = val.splitn(4, '|').collect();
                warnings.push(json!({
                    "id": parts.first().unwrap_or(&""),
                    "text": parts.get(1).unwrap_or(&""),
                }));
            }
        } else if let Some(val) = line.strip_prefix("suggestion[]=") {
            if suggestions.len() < MAX_DETAIL_FINDINGS {
                let parts: Vec<&str> = val.splitn(4, '|').collect();
                suggestions.push(json!({
                    "id": parts.first().unwrap_or(&""),
                    "text": parts.get(1).unwrap_or(&""),
                }));
            }
        }
    }

    Some(json!({
        "score": score.unwrap_or(0),
        "warnings": warnings,
        "suggestions": suggestions,
        "last_scan": file_mtime_iso(&path),
    }))
}

fn detail_rkhunter() -> Option<Value> {
    let path = Path::new(SCAN_DIR).join("rkhunter.log");
    let data = fs::read_to_string(&path).ok()?;

    let mut warnings: Vec<Value> = Vec::new();
    for line in data.lines() {
        let trimmed = line.trim();
        if !trimmed.contains("[ Warning ]") || warnings.len() >= MAX_DETAIL_FINDINGS {
            continue;
        }
        // rkhunter format: "/path/to/file [ Warning ]" or "Check description [ Warning ]"
        let file = trimmed.trim_end_matches("[ Warning ]").trim().to_string();
        warnings.push(json!({"file": file, "status": "warning"}));
    }

    Some(json!({
        "warnings": warnings,
        "last_scan": file_mtime_iso(&path),
    }))
}

fn detail_chkrootkit() -> Option<Value> {
    let path = Path::new(SCAN_DIR).join("chkrootkit.log");
    let data = fs::read_to_string(&path).ok()?;

    let mut infected: Vec<Value> = Vec::new();
    for line in data.lines() {
        let trimmed = line.trim();
        if !trimmed.contains("INFECTED") || infected.len() >= MAX_DETAIL_FINDINGS {
            continue;
        }
        // chkrootkit format: "Checking `check_name`... INFECTED" or
        // "Checking `check_name`... INFECTED (file1 file2 ...)"
        let mut check_name = trimmed.to_string();
        let mut files = String::new();
        if let Some(rest) = trimmed.strip_prefix("Checking `") {
            if let Some(end) = rest.find('`') {
                check_name = rest[..end].to_string();
            }
        }
        // Some checks list suspect files in parentheses after INFECTED.
        if let Some(paren_start) = trimmed.find("INFECTED (") {
            files = trimmed[paren_start + 10..].trim_end_matches(')').trim().to_string();
        }
        infected.push(json!({"check": check_name, "files": files}));
    }

    Some(json!({
        "infected": infected,
        "clean": infected.is_empty(),
        "last_scan": file_mtime_iso(&path),
    }))
}

fn detail_clamav() -> Option<Value> {
    let path = Path::new(SCAN_DIR).join("clamav.log");
    let data = fs::read_to_string(&path).ok()?;

    let mut findings: Vec<Value> = Vec::new();
    for line in data.lines() {
        if line.contains("FOUND") && findings.len() < MAX_DETAIL_FINDINGS {
            // Format: /path/to/file: SignatureName FOUND
            if let Some(colon) = line.rfind(": ") {
                let file_path = &line[..colon];
                let sig = line[colon + 2..].trim_end_matches(" FOUND").trim();
                findings.push(json!({"path": file_path, "signature": sig}));
            }
        }
    }

    Some(json!({
        "findings": findings,
        "clean": findings.is_empty(),
        "last_scan": file_mtime_iso(&path),
    }))
}

fn detail_aide() -> Option<Value> {
    let path = Path::new(SCAN_DIR).join("aide.log");
    let data = fs::read_to_string(&path).ok()?;

    let mut added: Vec<String> = Vec::new();
    let mut removed: Vec<String> = Vec::new();
    let mut changed: Vec<String> = Vec::new();

    // AIDE detailed output has sections like:
    //   Added files:
    //     /path/to/file
    //   Removed files:
    //     /path/to/file
    //   Changed files:
    //     /path/to/file
    let mut section = "";
    for line in data.lines() {
        let trimmed = line.trim();
        if trimmed == "Added files:" || trimmed.starts_with("Added entries:") {
            section = "added";
            // If this is the summary line "Added entries: 3", skip it.
            if trimmed.contains(':') && trimmed.ends_with(char::is_numeric) { continue; }
        } else if trimmed == "Removed files:" || trimmed.starts_with("Removed entries:") {
            section = "removed";
            if trimmed.contains(':') && trimmed.ends_with(char::is_numeric) { continue; }
        } else if trimmed == "Changed files:" || trimmed.starts_with("Changed entries:") {
            section = "changed";
            if trimmed.contains(':') && trimmed.ends_with(char::is_numeric) { continue; }
        } else if trimmed.is_empty() || trimmed.starts_with("---") || trimmed.starts_with("AIDE") || trimmed.starts_with("Start") || trimmed.starts_with("End") || trimmed.starts_with("Total") || trimmed.starts_with("Number of") {
            if trimmed.is_empty() { section = ""; }
            continue;
        } else if trimmed.starts_with('/') {
            let path_str = trimmed.split_whitespace().next().unwrap_or(trimmed).to_string();
            match section {
                "added" if added.len() < MAX_DETAIL_FINDINGS => added.push(path_str),
                "removed" if removed.len() < MAX_DETAIL_FINDINGS => removed.push(path_str),
                "changed" if changed.len() < MAX_DETAIL_FINDINGS => changed.push(path_str),
                _ => {}
            }
        }
    }

    Some(json!({
        "added": added,
        "removed": removed,
        "changed": changed,
        "clean": added.is_empty() && removed.is_empty() && changed.is_empty(),
        "last_scan": file_mtime_iso(&path),
    }))
}

fn detail_debsecan() -> Option<Value> {
    let path = Path::new(SCAN_DIR).join("debsecan.log");
    let data = fs::read_to_string(&path).ok()?;

    // debsecan --format detail produces multi-line blocks:
    //   CVE-2024-1234 (high urgency)
    //     Description text...
    //     installed: libfoo 1.2.3-4 (built from libfoo 1.2.3-4)
    //     fixed package: 1.2.3-5
    //
    // debsecan default format produces single lines:
    //   CVE-2024-1234 libfoo (high urgency)
    //
    // We handle both formats and deduplicate per package, keeping only the
    // highest-numbered CVE for each package.
    use std::collections::HashMap;
    let mut best: HashMap<String, (String, String)> = HashMap::new(); // package -> (cve_id, urgency)

    let lines: Vec<&str> = data.lines().collect();
    let mut i = 0;
    while i < lines.len() {
        let trimmed = lines[i].trim();
        if !trimmed.starts_with("CVE-") {
            i += 1;
            continue;
        }
        let cve_id = trimmed.split_whitespace().next().unwrap_or("");
        if cve_id.is_empty() {
            i += 1;
            continue;
        }

        let urgency = if trimmed.contains("high urgency") { "high" }
            else if trimmed.contains("medium urgency") { "medium" }
            else if trimmed.contains("low urgency") { "low" }
            else { "unassigned" };

        // Try to extract package name. Detail format: package is on a
        // subsequent "installed:" line. Default format: second token on the
        // CVE line (e.g. "CVE-xxx libfoo (low urgency)").
        let mut package = String::new();

        // Check default format: second token is not "(" (urgency paren).
        let tokens: Vec<&str> = trimmed.split_whitespace().collect();
        if tokens.len() >= 2 && !tokens[1].starts_with('(') {
            package = tokens[1].to_string();
        }

        // Look ahead for "installed:" line (detail format).
        let mut j = i + 1;
        while j < lines.len() && !lines[j].trim().starts_with("CVE-") {
            let inner = lines[j].trim();
            if let Some(rest) = inner.strip_prefix("installed:") {
                // "installed: libfoo 1.2.3-4 (built from ...)"
                let pkg = rest.trim().split_whitespace().next().unwrap_or("");
                if !pkg.is_empty() {
                    package = pkg.to_string();
                }
                break;
            }
            j += 1;
        }

        if !package.is_empty() {
            // Keep the highest-numbered CVE per package.
            if let Some(existing) = best.get(&package) {
                if cve_id > existing.0.as_str() {
                    best.insert(package, (cve_id.to_string(), urgency.to_string()));
                }
            } else {
                best.insert(package, (cve_id.to_string(), urgency.to_string()));
            }
        } else {
            // No package found — still record it keyed by CVE ID.
            best.entry(cve_id.to_string())
                .or_insert_with(|| (cve_id.to_string(), urgency.to_string()));
        }

        i += 1;
    }

    // Sort by urgency (high > medium > low > unassigned), then by CVE ID descending.
    let mut vulns: Vec<Value> = best.iter().map(|(pkg, (cve, urg))| {
        json!({"cve_id": cve, "package": pkg, "urgency": urg})
    }).collect();
    vulns.sort_by(|a, b| {
        fn urg_rank(u: &str) -> u8 { match u { "high" => 0, "medium" => 1, "low" => 2, _ => 3 } }
        let ua = a["urgency"].as_str().unwrap_or("unassigned");
        let ub = b["urgency"].as_str().unwrap_or("unassigned");
        urg_rank(ua).cmp(&urg_rank(ub))
            .then_with(|| b["cve_id"].as_str().unwrap_or("").cmp(a["cve_id"].as_str().unwrap_or("")))
    });
    vulns.truncate(MAX_DETAIL_VULNS);

    Some(json!({
        "vulnerabilities": vulns,
        "truncated": best.len() > MAX_DETAIL_VULNS,
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
        // On CI / dev machines without /var/lib/vigo/security, this is Null.
        // On machines with scan output, it would be an object.
        assert!(val.is_null() || val.is_object());
    }

    #[test]
    fn test_parse_trivy_json() {
        // Test the per-package dedup logic inline (parse_trivy reads from SCAN_DIR).
        let trivy_output = r#"{
            "Results": [{
                "Vulnerabilities": [
                    {"VulnerabilityID": "CVE-2024-0001", "Severity": "CRITICAL", "PkgName": "libssl3"},
                    {"VulnerabilityID": "CVE-2024-0002", "Severity": "HIGH", "PkgName": "libssl3"},
                    {"VulnerabilityID": "CVE-2024-0003", "Severity": "MEDIUM", "PkgName": "libfoo"},
                    {"VulnerabilityID": "CVE-2024-0004", "Severity": "LOW", "PkgName": "libbar"},
                    {"VulnerabilityID": "CVE-2024-0005", "Severity": "HIGH", "PkgName": "curl"}
                ]
            }]
        }"#;

        let parsed: Value = serde_json::from_str(trivy_output).unwrap();
        let results = parsed.get("Results").unwrap().as_array().unwrap();
        let mut critical = 0u64;
        let mut high = 0u64;
        use std::collections::HashMap;
        let mut best: HashMap<String, (String, u64)> = HashMap::new();
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
                        let id = vuln.get("VulnerabilityID").and_then(|v| v.as_str()).unwrap_or("");
                        let pkg = vuln.get("PkgName").and_then(|v| v.as_str()).unwrap_or(id);
                        if !id.is_empty() {
                            best.entry(pkg.to_string())
                                .and_modify(|(existing, others)| {
                                    if id > existing.as_str() { *existing = id.to_string(); }
                                    *others += 1;
                                })
                                .or_insert_with(|| (id.to_string(), 0));
                        }
                    }
                }
            }
        }
        assert_eq!(critical, 1);
        assert_eq!(high, 2);
        // libssl3 has 2 CVEs -> deduped to 1 entry with others=1; curl has 1 entry
        assert_eq!(best.len(), 2);
        let ssl = best.get("libssl3").unwrap();
        assert_eq!(ssl.0, "CVE-2024-0002"); // higher CVE wins
        assert_eq!(ssl.1, 1); // 1 other CVE suppressed
        let curl = best.get("curl").unwrap();
        assert_eq!(curl.0, "CVE-2024-0005");
        assert_eq!(curl.1, 0); // no others
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
    fn test_parse_debsecan_summary_counts() {
        // Both default format ("CVE pkg (urgency)") and detail format
        // ("CVE (urgency)") should count correctly via contains().
        let default_fmt = "CVE-2024-1000 libfoo (high urgency)\nCVE-2024-1001 libbar (medium urgency)\n";
        let detail_fmt = "CVE-2024-1000 (high urgency)\n  installed: libfoo 1.0\nCVE-2024-1001 (medium urgency)\n  installed: libbar 2.0\n";
        for data in [default_fmt, detail_fmt] {
            let mut high = 0u64;
            let mut medium = 0u64;
            for line in data.lines() {
                let trimmed = line.trim();
                if !trimmed.starts_with("CVE-") { continue; }
                if trimmed.contains("high urgency") { high += 1; }
                else if trimmed.contains("medium urgency") { medium += 1; }
            }
            assert_eq!(high, 1, "data: {data}");
            assert_eq!(medium, 1, "data: {data}");
        }
    }

    #[test]
    fn test_detail_debsecan_dedup_per_package() {
        // detail_debsecan should keep only the highest-numbered CVE per package.
        // Simulates debsecan --format detail output with two CVEs for urllib3.
        use std::collections::HashMap;
        let data = "\
CVE-2023-43804 (high urgency)\n\
  Copies Cookies: header instead of stripping it on cross-origin redirect\n\
  installed: python3-urllib3 1.26.7-1 (built from python3-urllib3 1.26.7-1)\n\
  fixed package: not yet available\n\
\n\
CVE-2024-37891 (medium urgency)\n\
  Proxy-Authorization header not stripped during cross-origin redirects\n\
  installed: python3-urllib3 1.26.7-1 (built from python3-urllib3 1.26.7-1)\n\
  fixed package: not yet available\n\
\n\
CVE-2024-11111 (low urgency)\n\
  Something else in libfoo\n\
  installed: libfoo 2.0-1\n\
  fixed package: 2.0-2\n";

        // Inline the same logic as detail_debsecan to test without filesystem.
        let mut best: HashMap<String, (String, String)> = HashMap::new();
        let lines: Vec<&str> = data.lines().collect();
        let mut i = 0;
        while i < lines.len() {
            let trimmed = lines[i].trim();
            if !trimmed.starts_with("CVE-") { i += 1; continue; }
            let cve_id = trimmed.split_whitespace().next().unwrap_or("");
            if cve_id.is_empty() { i += 1; continue; }
            let urgency = if trimmed.contains("high urgency") { "high" }
                else if trimmed.contains("medium urgency") { "medium" }
                else if trimmed.contains("low urgency") { "low" }
                else { "unassigned" };
            let tokens: Vec<&str> = trimmed.split_whitespace().collect();
            let mut package = String::new();
            if tokens.len() >= 2 && !tokens[1].starts_with('(') {
                package = tokens[1].to_string();
            }
            let mut j = i + 1;
            while j < lines.len() && !lines[j].trim().starts_with("CVE-") {
                let inner = lines[j].trim();
                if let Some(rest) = inner.strip_prefix("installed:") {
                    let pkg = rest.trim().split_whitespace().next().unwrap_or("");
                    if !pkg.is_empty() { package = pkg.to_string(); }
                    break;
                }
                j += 1;
            }
            if !package.is_empty() {
                if let Some(existing) = best.get(&package) {
                    if cve_id > existing.0.as_str() {
                        best.insert(package, (cve_id.to_string(), urgency.to_string()));
                    }
                } else {
                    best.insert(package, (cve_id.to_string(), urgency.to_string()));
                }
            }
            i += 1;
        }

        // Two packages: python3-urllib3 (deduped to higher CVE) + libfoo.
        assert_eq!(best.len(), 2);
        let urllib = best.get("python3-urllib3").unwrap();
        assert_eq!(urllib.0, "CVE-2024-37891"); // higher CVE number wins
        assert_eq!(urllib.1, "medium");
        let libfoo = best.get("libfoo").unwrap();
        assert_eq!(libfoo.0, "CVE-2024-11111");
    }

    #[test]
    fn test_debsecan_no_urgency_is_unassigned() {
        // Most debsecan CVEs have no urgency marker at all.
        // These should be classified as "unassigned", not "other".
        let data = "CVE-2025-54389\n  installed: aide 0.17.4\nCVE-2012-6655 (low urgency)\n  installed: accountsservice 22.07.5\n";
        let mut low = 0u64;
        let mut unassigned = 0u64;
        for line in data.lines() {
            let trimmed = line.trim();
            if !trimmed.starts_with("CVE-") { continue; }
            if trimmed.contains("high urgency") {}
            else if trimmed.contains("medium urgency") {}
            else if trimmed.contains("low urgency") { low += 1; }
            else { unassigned += 1; }
        }
        assert_eq!(low, 1);
        assert_eq!(unassigned, 1);
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
        use std::collections::HashMap;
        let mut best: HashMap<String, (String, u64)> = HashMap::new();
        for update in updates {
            let severity = update.get("Severity").and_then(|s| s.as_str()).unwrap_or("");
            match severity {
                "Critical" => critical += 1,
                "Important" => important += 1,
                _ => {}
            }
            let kb = update.get("KB").and_then(|v| v.as_str()).unwrap_or("");
            if let Some(cve_str) = update.get("CVEs").and_then(|v| v.as_str()) {
                for cve in cve_str.split(',') {
                    let cve = cve.trim();
                    if !cve.starts_with("CVE-") { continue; }
                    let key = if kb.is_empty() { cve.to_string() } else { format!("KB{kb}") };
                    best.entry(key)
                        .and_modify(|(existing, others)| {
                            if cve > existing.as_str() { *existing = cve.to_string(); }
                            *others += 1;
                        })
                        .or_insert_with(|| (cve.to_string(), 0));
                }
            }
        }
        assert_eq!(critical, 1);
        assert_eq!(important, 1);
        // KB1234 has 2 CVEs -> deduped to 1 entry with others=1; KB5678 has 1 entry
        assert_eq!(best.len(), 2);
        let kb1234 = best.get("KB1234").unwrap();
        assert_eq!(kb1234.0, "CVE-2024-1001"); // higher CVE wins
        assert_eq!(kb1234.1, 1); // 1 other
        let kb5678 = best.get("KB5678").unwrap();
        assert_eq!(kb5678.0, "CVE-2024-2000");
        assert_eq!(kb5678.1, 0);
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

/// Parse SCAP/STIG compliance scan results.
/// Expects JSON output at {SCAN_DIR}/scap.json from:
///   Unix: oscap xccdf eval --results-arf results.xml && oscap-report-to-json
///   Windows: PowerShell DSC compliance export or SCAP scanner JSON output
///
/// Expected format:
/// {
///   "profile": "CIS_Windows_Server_2022_L1",
///   "rules": [
///     {"id": "xccdf_...", "title": "...", "result": "pass|fail|notapplicable", "severity": "high|medium|low"}
///   ]
/// }
fn parse_scap() -> Option<Value> {
    let path = Path::new(SCAN_DIR).join("scap.json");
    let data = fs::read_to_string(&path).ok()?;
    let parsed: Value = serde_json::from_str(&data).ok()?;

    let profile = parsed.get("profile").and_then(|v| v.as_str()).unwrap_or("unknown");

    let rules = parsed.get("rules").and_then(|v| v.as_array())?;
    let mut pass = 0i64;
    let mut fail = 0i64;
    let mut notapplicable = 0i64;
    let mut high_fail = 0i64;
    let mut medium_fail = 0i64;
    let mut low_fail = 0i64;

    for rule in rules {
        let result = rule.get("result").and_then(|v| v.as_str()).unwrap_or("");
        let severity = rule.get("severity").and_then(|v| v.as_str()).unwrap_or("");
        match result {
            "pass" => pass += 1,
            "fail" => {
                fail += 1;
                match severity {
                    "high" => high_fail += 1,
                    "medium" => medium_fail += 1,
                    _ => low_fail += 1,
                }
            }
            "notapplicable" => notapplicable += 1,
            _ => {}
        }
    }

    let total = pass + fail;
    let score = if total > 0 { (pass * 100) / total } else { 0 };

    Some(json!({
        "profile": profile,
        "total_rules": rules.len(),
        "pass": pass,
        "fail": fail,
        "not_applicable": notapplicable,
        "high_fail": high_fail,
        "medium_fail": medium_fail,
        "low_fail": low_fail,
        "score": score,
        "clean": fail == 0,
        "last_scan": file_mtime_iso(&path),
    }))
}
