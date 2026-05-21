// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

//! CVE → running-binary correlation trait collector (Linux-only).
//!
//! Answers the sysadmin question "which of my currently-running
//! binaries have known CVEs?" by joining three local data sources on
//! the host itself:
//!
//! 1. **Scanner output** — re-reads `/var/lib/vigo/security/trivy.json`
//!    and `debsecan.log` (the same files `security_scan` consumes) to
//!    extract a CVE → `{severity, packages[]}` map. The scanners know
//!    which packages each CVE affects; `security_scan` discards that
//!    detail for dashboard brevity, we need it for the join.
//!
//! 2. **Running binaries** — walks `/proc/*/exe` symlinks, dedupes by
//!    resolved path, counts PIDs per binary, and flags the `(deleted)`
//!    case (kernel keeps the old inode live after the file on disk
//!    was replaced — high-signal because the running code doesn't
//!    match what's installed).
//!
//! 3. **Package ownership** — for each unique binary path, asks dpkg
//!    or rpm who owns it. Calls are batched into one subprocess to
//!    avoid a per-binary fork storm on hosts with 200+ running
//!    processes.
//!
//! Classified `periodic` — on the same hourly cadence as
//! `package_updates` because the `/proc` walk + package-manager
//! lookups are the most expensive operations in the trait pipeline.
//! Results cache between runs.

use anyhow::Result;
use serde_json::{json, Value};
use std::collections::{BTreeMap, HashMap};
use std::path::Path;
use std::process::Command;
use std::time::Duration;

/// Scanner-output directory shared with `security_scan`.
const SCAN_DIR: &str = "/var/lib/vigo/security";

/// Upper bound on binaries we inspect per run. Realistic hosts have
/// 50-200; the cap guards against pathological systems without
/// truncating meaningful data in the common case.
const MAX_BINARIES: usize = 2000;

/// Upper bound on result rows we emit (one row per CVE × binary
/// intersection). Same rationale — protects the flattened trait
/// payload from runaway growth.
const MAX_ROWS: usize = 500;

/// Wall-clock cap per batched package-manager lookup. Short enough to
/// bail out on a wedged dpkg/rpm, long enough for a cold disk cache.
const PKG_LOOKUP_TIMEOUT: Duration = Duration::from_secs(15);

pub fn collect() -> Result<Value> {
    collect_from_roots("/proc", SCAN_DIR)
}

/// Core collector split from `collect()` so tests can supply fixture
/// directories for both `/proc` and the scanner-output dir.
fn collect_from_roots(proc_root: &str, scan_dir: &str) -> Result<Value> {
    let cve_index = build_cve_package_index(scan_dir);
    let binaries = discover_running_binaries(proc_root);

    if binaries.is_empty() {
        return Ok(json!({
            "running_cves": [],
            "summary": summary_counts(&[]),
        }));
    }

    // Batched package lookup: dpkg if available, else rpm. Hosts
    // running both managers (rare) are served by whichever wins the
    // PATH check first.
    let ownership = lookup_package_ownership(&binaries);

    let mut rows = Vec::new();
    for bin in &binaries {
        let owner = match ownership.get(&bin.path) {
            Some(o) => o.clone(),
            None => continue, // binary not owned by any package
        };
        let pkg_cves = match cve_index.get(&owner.package) {
            Some(cves) => cves,
            None => continue, // no known CVEs for this package
        };
        for cve in pkg_cves {
            rows.push(json!({
                "cve_id": cve.id,
                "severity": cve.severity,
                "package": owner.package,
                "package_version": owner.version,
                "binary_path": bin.path,
                "pid_count": bin.pid_count,
                "deleted": bin.deleted,
            }));
            if rows.len() >= MAX_ROWS {
                break;
            }
        }
        if rows.len() >= MAX_ROWS {
            break;
        }
    }

    // Deterministic order: severity desc (critical first), then CVE id,
    // then binary path. Makes diffing across check-ins readable and
    // places the most-urgent rows at the front of the truncated set.
    rows.sort_by(|a, b| {
        let sa = severity_rank(a["severity"].as_str().unwrap_or(""));
        let sb = severity_rank(b["severity"].as_str().unwrap_or(""));
        sb.cmp(&sa)
            .then_with(|| a["cve_id"].as_str().cmp(&b["cve_id"].as_str()))
            .then_with(|| {
                a["binary_path"]
                    .as_str()
                    .cmp(&b["binary_path"].as_str())
            })
    });

    Ok(json!({
        "running_cves": rows,
        "summary": summary_counts(&rows),
    }))
}

/// Fan-out summary by severity, plus the total count. Mirrors the
/// shape of `security_scan.unique_*` so dashboards can line the two
/// numbers up side by side.
fn summary_counts(rows: &[Value]) -> Value {
    let mut critical = 0u64;
    let mut high = 0u64;
    let mut medium = 0u64;
    let mut low = 0u64;
    for r in rows {
        match r["severity"].as_str().unwrap_or("") {
            "critical" => critical += 1,
            "high" => high += 1,
            "medium" => medium += 1,
            "low" => low += 1,
            _ => {}
        }
    }
    json!({
        "total": rows.len(),
        "critical": critical,
        "high": high,
        "medium": medium,
        "low": low,
    })
}

fn severity_rank(s: &str) -> u8 {
    match s {
        "critical" => 4,
        "high" => 3,
        "medium" => 2,
        "low" => 1,
        _ => 0,
    }
}

// ── CVE → package index ──────────────────────────────────────────────

/// A single CVE with its severity. Used as a value in the
/// package→CVE index below.
#[derive(Clone, Debug)]
struct CveEntry {
    id: String,
    severity: String,
}

/// Build a map from package name → list of CVEs affecting it, by
/// re-reading trivy.json and debsecan.log from the scanner output
/// directory. Missing files yield an empty index (not an error) —
/// hosts without security scans get a clean empty result.
fn build_cve_package_index(scan_dir: &str) -> HashMap<String, Vec<CveEntry>> {
    let mut out: HashMap<String, Vec<CveEntry>> = HashMap::new();

    // Dedupe CVE × package pairs across scanners so a CVE reported by
    // both trivy and debsecan doesn't produce two rows.
    let mut seen: std::collections::HashSet<(String, String)> = std::collections::HashSet::new();
    let mut add = |pkg: &str, cve: CveEntry| {
        if pkg.is_empty() || cve.id.is_empty() {
            return;
        }
        let key = (pkg.to_string(), cve.id.clone());
        if !seen.insert(key) {
            return;
        }
        out.entry(pkg.to_string()).or_default().push(cve);
    };

    read_trivy_package_cves(scan_dir, |pkg, cve| add(pkg, cve));
    read_debsecan_package_cves(scan_dir, |pkg, cve| add(pkg, cve));

    out
}

/// Parse trivy.json for `{PkgName, VulnerabilityID, Severity}` tuples.
/// Uses the same scanner-output file `security_scan` reads; silently
/// skips when the file is absent or malformed.
fn read_trivy_package_cves(scan_dir: &str, mut emit: impl FnMut(&str, CveEntry)) {
    let path = Path::new(scan_dir).join("trivy.json");
    let data = match std::fs::read_to_string(&path) {
        Ok(d) => d,
        Err(_) => return,
    };
    let parsed: Value = match serde_json::from_str(&data) {
        Ok(v) => v,
        Err(_) => return,
    };

    let results = match parsed.get("Results").and_then(|r| r.as_array()) {
        Some(r) => r,
        None => return,
    };
    for result in results {
        let vulns = match result.get("Vulnerabilities").and_then(|v| v.as_array()) {
            Some(v) => v,
            None => continue,
        };
        for vuln in vulns {
            let id = vuln
                .get("VulnerabilityID")
                .and_then(|v| v.as_str())
                .unwrap_or("");
            let pkg = vuln.get("PkgName").and_then(|v| v.as_str()).unwrap_or("");
            let sev = vuln
                .get("Severity")
                .and_then(|v| v.as_str())
                .unwrap_or("")
                .to_lowercase();
            emit(
                pkg,
                CveEntry {
                    id: id.to_string(),
                    severity: sev,
                },
            );
        }
    }
}

/// Parse debsecan.log for `CVE-XXXX-YYYY package-name <severity> urgency`
/// lines. Same file `security_scan` reads.
fn read_debsecan_package_cves(scan_dir: &str, mut emit: impl FnMut(&str, CveEntry)) {
    let path = Path::new(scan_dir).join("debsecan.log");
    let data = match std::fs::read_to_string(&path) {
        Ok(d) => d,
        Err(_) => return,
    };
    for line in data.lines() {
        let trimmed = line.trim();
        if !trimmed.starts_with("CVE-") {
            continue;
        }
        let parts: Vec<&str> = trimmed.split_whitespace().collect();
        if parts.len() < 2 {
            continue;
        }
        let id = parts[0];
        let pkg = parts[1];
        let sev = if trimmed.contains("high urgency") {
            "high"
        } else if trimmed.contains("medium urgency") {
            "medium"
        } else {
            // "low urgency" and unassigned both map to low, matching security_scan.
            "low"
        };
        emit(
            pkg,
            CveEntry {
                id: id.to_string(),
                severity: sev.to_string(),
            },
        );
    }
}

// ── Running binary discovery ─────────────────────────────────────────

#[derive(Clone, Debug)]
struct RunningBinary {
    path: String,
    pid_count: u32,
    deleted: bool, // /proc/<pid>/exe showed a "(deleted)" suffix
}

/// Walk `/proc/*/exe` and return the deduped set of binary paths
/// currently mapped into running processes. Kernel threads have no
/// `/proc/<pid>/exe` link and are silently skipped. The resulting
/// vector is capped at MAX_BINARIES.
fn discover_running_binaries(proc_root: &str) -> Vec<RunningBinary> {
    let mut by_path: BTreeMap<String, RunningBinary> = BTreeMap::new();

    let entries = match std::fs::read_dir(proc_root) {
        Ok(e) => e,
        Err(_) => return Vec::new(),
    };

    for entry in entries.flatten() {
        let name = match entry.file_name().into_string() {
            Ok(s) => s,
            Err(_) => continue,
        };
        // Only numeric PID entries; skip /proc/self, /proc/sys, etc.
        if name.parse::<u32>().is_err() {
            continue;
        }

        let exe_link = entry.path().join("exe");
        let target = match std::fs::read_link(&exe_link) {
            Ok(p) => p,
            Err(_) => continue, // kernel thread or race with exit
        };

        let (path, deleted) = canonicalize_exe_link(&target);
        if path.is_empty() {
            continue;
        }

        let e = by_path.entry(path.clone()).or_insert(RunningBinary {
            path,
            pid_count: 0,
            deleted,
        });
        e.pid_count += 1;
        // If any PID has the deleted variant, flag the whole binary.
        if deleted {
            e.deleted = true;
        }

        if by_path.len() >= MAX_BINARIES {
            break;
        }
    }

    by_path.into_values().collect()
}

/// Strip the ` (deleted)` suffix that the kernel appends when the
/// on-disk file was unlinked or replaced while a PID still holds the
/// old inode. Returns `(clean_path, deleted_flag)`.
fn canonicalize_exe_link(link_target: &Path) -> (String, bool) {
    let s = link_target.to_string_lossy();
    if let Some(prefix) = s.strip_suffix(" (deleted)") {
        (prefix.to_string(), true)
    } else {
        (s.to_string(), false)
    }
}

// ── Package ownership ────────────────────────────────────────────────

#[derive(Clone, Debug)]
struct PackageOwner {
    package: String,
    version: String,
}

/// Resolve the owning package for each binary path via a single
/// batched call to whichever package manager is available
/// (`dpkg-query` or `rpm`). Returns a map path → (package, version).
/// Paths with no owning package are absent from the returned map.
fn lookup_package_ownership(binaries: &[RunningBinary]) -> HashMap<String, PackageOwner> {
    if binaries.is_empty() {
        return HashMap::new();
    }
    let paths: Vec<&str> = binaries.iter().map(|b| b.path.as_str()).collect();

    if command_exists("dpkg-query") {
        return lookup_dpkg(&paths);
    }
    if command_exists("rpm") {
        return lookup_rpm(&paths);
    }
    HashMap::new()
}

fn command_exists(cmd: &str) -> bool {
    // Honour the "invisible to host" rule: no `which` fork — just
    // walk PATH ourselves.
    let path = match std::env::var_os("PATH") {
        Some(p) => p,
        None => return false,
    };
    for dir in std::env::split_paths(&path) {
        if dir.join(cmd).is_file() {
            return true;
        }
    }
    false
}

/// Batched dpkg ownership lookup. `dpkg-query -S path1 path2 ...`
/// prints `pkg:arch: /path` per resolved entry and `dpkg-query: no
/// path found matching ...` per miss. We parse only the success lines.
/// Version comes from a follow-up `dpkg-query -W -f` call over the
/// unique package list.
fn lookup_dpkg(paths: &[&str]) -> HashMap<String, PackageOwner> {
    let mut out: HashMap<String, PackageOwner> = HashMap::new();
    let output = match run_with_timeout("dpkg-query", &prepend("-S", paths)) {
        Some(o) => o,
        None => return out,
    };

    // Per-line format: `pkg:arch: /usr/sbin/sshd`  OR
    //                  `pkg: /usr/sbin/sshd` on single-arch systems.
    // Misses go to stderr as "no path found" — we read stdout only.
    let mut path_to_pkg: HashMap<String, String> = HashMap::new();
    for line in output.lines() {
        let (head, tail) = match line.split_once(": ") {
            Some(pair) => pair,
            None => continue,
        };
        let pkg = head.split(':').next().unwrap_or(head).trim().to_string();
        let resolved = tail.trim();
        if pkg.is_empty() || resolved.is_empty() {
            continue;
        }
        path_to_pkg.insert(resolved.to_string(), pkg);
    }
    if path_to_pkg.is_empty() {
        return out;
    }

    // Resolve installed versions for the unique package set in one call.
    let unique_pkgs: Vec<String> = {
        let mut seen: std::collections::HashSet<String> = std::collections::HashSet::new();
        path_to_pkg
            .values()
            .filter(|p| seen.insert((*p).clone()))
            .cloned()
            .collect()
    };
    let mut versions: HashMap<String, String> = HashMap::new();
    let mut args: Vec<String> = vec!["-W".into(), "-f=${Package} ${Version}\n".into()];
    for pkg in &unique_pkgs {
        args.push(pkg.clone());
    }
    let args_ref: Vec<&str> = args.iter().map(|s| s.as_str()).collect();
    if let Some(out_text) = run_with_timeout("dpkg-query", &args_ref) {
        for line in out_text.lines() {
            let mut parts = line.split_whitespace();
            if let (Some(pkg), Some(ver)) = (parts.next(), parts.next()) {
                versions.insert(pkg.to_string(), ver.to_string());
            }
        }
    }

    for (path, pkg) in path_to_pkg {
        let version = versions.get(&pkg).cloned().unwrap_or_default();
        out.insert(path, PackageOwner { package: pkg, version });
    }
    out
}

/// Batched rpm ownership lookup. `rpm -qf --qf '%{NAME} %{VERSION}-%{RELEASE} %{N}\n' path1 path2 ...`
/// prints one line per path (or "... is not owned by any package" on
/// miss). Successful lines have three fields: package, version, and
/// the original path (via `%{N}` substitution fallback — actually rpm
/// doesn't emit the queried path, so we rely on output order matching
/// input order).
fn lookup_rpm(paths: &[&str]) -> HashMap<String, PackageOwner> {
    let mut out: HashMap<String, PackageOwner> = HashMap::new();
    let mut args: Vec<String> = vec![
        "-qf".into(),
        "--qf".into(),
        "%{NAME} %{VERSION}-%{RELEASE}\n".into(),
    ];
    for p in paths {
        args.push((*p).to_string());
    }
    let args_ref: Vec<&str> = args.iter().map(|s| s.as_str()).collect();
    let output = match run_with_timeout("rpm", &args_ref) {
        Some(o) => o,
        None => return out,
    };
    // rpm mixes success and miss lines in input order. Walk output
    // and input paths in lockstep, skipping the miss marker lines.
    let mut input_iter = paths.iter();
    for line in output.lines() {
        let trimmed = line.trim();
        if trimmed.is_empty() {
            continue;
        }
        // Miss line example: "file /foo is not owned by any package"
        if trimmed.contains("is not owned by any package") {
            input_iter.next();
            continue;
        }
        let mut parts = trimmed.split_whitespace();
        let (pkg, ver) = match (parts.next(), parts.next()) {
            (Some(p), Some(v)) => (p.to_string(), v.to_string()),
            _ => {
                input_iter.next();
                continue;
            }
        };
        let path = match input_iter.next() {
            Some(p) => (*p).to_string(),
            None => continue,
        };
        out.insert(path, PackageOwner { package: pkg, version: ver });
    }
    out
}

fn prepend<'a>(first: &'a str, rest: &[&'a str]) -> Vec<&'a str> {
    let mut v = Vec::with_capacity(rest.len() + 1);
    v.push(first);
    v.extend_from_slice(rest);
    v
}

/// Run a subprocess with a wall-clock timeout. Returns `Some(stdout)`
/// on success, `None` on any failure (command missing, non-zero exit,
/// timeout elapsed). Subprocess runs with default stdin/stderr —
/// stderr is deliberately ignored so non-fatal warnings from
/// dpkg-query / rpm don't pollute the trait.
fn run_with_timeout(cmd: &str, args: &[&str]) -> Option<String> {
    use std::io::Read;
    use std::process::Stdio;
    let mut child = Command::new(cmd)
        .args(args)
        .stdin(Stdio::null())
        .stdout(Stdio::piped())
        .stderr(Stdio::null())
        .spawn()
        .ok()?;

    let start = std::time::Instant::now();
    loop {
        match child.try_wait() {
            Ok(Some(status)) if status.success() => {
                let mut out = String::new();
                if let Some(mut s) = child.stdout.take() {
                    s.read_to_string(&mut out).ok();
                }
                return Some(out);
            }
            Ok(Some(_)) => {
                // Non-zero exit — still read stdout because dpkg-query -S
                // returns 1 when some paths don't match but prints hits.
                let mut out = String::new();
                if let Some(mut s) = child.stdout.take() {
                    s.read_to_string(&mut out).ok();
                }
                return if out.is_empty() { None } else { Some(out) };
            }
            Ok(None) => {
                if start.elapsed() >= PKG_LOOKUP_TIMEOUT {
                    let _ = child.kill();
                    return None;
                }
                std::thread::sleep(Duration::from_millis(50));
            }
            Err(_) => return None,
        }
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use std::fs;
    use std::os::unix::fs::symlink;

    #[test]
    fn test_collect_returns_shape_on_empty_host() {
        // No scan dir, no /proc walk — emit the empty-shape sentinel
        // rather than erroring.
        let dir = tempfile::tempdir().unwrap();
        let empty_proc = dir.path().join("empty-proc");
        fs::create_dir_all(&empty_proc).unwrap();
        let val = collect_from_roots(
            empty_proc.to_str().unwrap(),
            "/nonexistent/scan/dir",
        )
        .unwrap();
        assert_eq!(val["running_cves"].as_array().unwrap().len(), 0);
        assert_eq!(val["summary"]["total"], 0);
    }

    #[test]
    fn test_canonicalize_exe_link() {
        let (p, d) = canonicalize_exe_link(Path::new("/usr/sbin/sshd"));
        assert_eq!(p, "/usr/sbin/sshd");
        assert!(!d);
        let (p, d) = canonicalize_exe_link(Path::new("/usr/bin/nginx (deleted)"));
        assert_eq!(p, "/usr/bin/nginx");
        assert!(d);
    }

    #[test]
    fn test_discover_running_binaries_dedupes_and_counts_pids() {
        let dir = tempfile::tempdir().unwrap();
        let proc_root = dir.path();
        // Two PIDs pointing at the same binary, one at a different one,
        // one kernel-thread-style dir (no exe link).
        let mk = |pid: &str, target: &str| {
            let d = proc_root.join(pid);
            fs::create_dir_all(&d).unwrap();
            symlink(target, d.join("exe")).unwrap();
        };
        mk("100", "/usr/sbin/sshd");
        mk("101", "/usr/sbin/sshd");
        mk("200", "/usr/bin/nginx (deleted)");
        fs::create_dir_all(proc_root.join("300")).unwrap(); // no exe → skipped
        // Non-PID entries must not confuse the walker.
        fs::create_dir_all(proc_root.join("self")).unwrap();

        let bins = discover_running_binaries(proc_root.to_str().unwrap());
        assert_eq!(bins.len(), 2);

        let sshd = bins.iter().find(|b| b.path == "/usr/sbin/sshd").unwrap();
        assert_eq!(sshd.pid_count, 2);
        assert!(!sshd.deleted);

        let nginx = bins.iter().find(|b| b.path == "/usr/bin/nginx").unwrap();
        assert_eq!(nginx.pid_count, 1);
        assert!(nginx.deleted);
    }

    #[test]
    fn test_cve_index_from_trivy_and_debsecan() {
        let dir = tempfile::tempdir().unwrap();
        fs::write(
            dir.path().join("trivy.json"),
            r#"{
                "Results": [{
                    "Vulnerabilities": [
                        {"VulnerabilityID": "CVE-2024-1111", "Severity": "CRITICAL", "PkgName": "openssl"},
                        {"VulnerabilityID": "CVE-2024-2222", "Severity": "HIGH", "PkgName": "curl"}
                    ]
                }]
            }"#,
        )
        .unwrap();
        fs::write(
            dir.path().join("debsecan.log"),
            "CVE-2024-3333 nginx high urgency\nCVE-2024-1111 openssl medium urgency\n",
        )
        .unwrap();

        let index = build_cve_package_index(dir.path().to_str().unwrap());

        // openssl gets the trivy row (CVE-2024-1111) deduped against
        // the debsecan row for the same CVE id.
        let openssl = index.get("openssl").expect("openssl cves missing");
        assert_eq!(openssl.len(), 1);
        assert_eq!(openssl[0].id, "CVE-2024-1111");
        assert_eq!(openssl[0].severity, "critical");

        assert_eq!(index.get("curl").unwrap().len(), 1);
        assert_eq!(index.get("nginx").unwrap().len(), 1);
    }

    #[test]
    fn test_summary_counts() {
        let rows = vec![
            json!({"severity": "critical"}),
            json!({"severity": "high"}),
            json!({"severity": "high"}),
            json!({"severity": "medium"}),
        ];
        let s = summary_counts(&rows);
        assert_eq!(s["total"], 4);
        assert_eq!(s["critical"], 1);
        assert_eq!(s["high"], 2);
        assert_eq!(s["medium"], 1);
        assert_eq!(s["low"], 0);
    }

    #[test]
    fn test_severity_rank() {
        assert!(severity_rank("critical") > severity_rank("high"));
        assert!(severity_rank("high") > severity_rank("medium"));
        assert!(severity_rank("medium") > severity_rank("low"));
        assert_eq!(severity_rank("unknown"), 0);
    }
}
