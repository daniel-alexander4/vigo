// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: Apache-2.0

//! Certificate lifecycle trait collector.
//!
//! Walks comprehensive certificate directories, parses every PEM block
//! in every candidate file, and emits a per-certificate row with CN,
//! SAN list, issuer, serial, validity window, and days-remaining.
//!
//! Parsing is done in pure Rust via `x509-parser` — no openssl
//! subprocess. Concatenated-PEM bundles (common on Debian/Ubuntu for
//! `/etc/ssl/certs/ca-certificates.crt`) are supported: each cert in
//! the bundle emits its own row.
//!
//! Per-OS path lists pick up the vendor-standard locations; non-Linux
//! Unix targets (macOS, BSD) get a minimal subset so existing
//! deployments don't lose the trait on rename from `cert_expiry`.

use anyhow::Result;
use serde_json::{json, Value};
use std::path::{Path, PathBuf};
use x509_parser::prelude::*;

/// Default certificate search paths. Linux gets the full set including
/// Kubernetes PKI and the common Debian/RHEL SSL trees; macOS and BSD
/// get a minimal subset covering their system CA bundles.
#[cfg(target_os = "linux")]
const CERT_DIRS: &[&str] = &[
    "/etc/ssl/certs",
    "/etc/ssl/private",
    "/etc/pki/tls/certs",
    "/etc/pki/tls/private",
    "/etc/kubernetes/pki",
];

#[cfg(target_os = "macos")]
const CERT_DIRS: &[&str] = &["/etc/ssl/certs", "/private/etc/ssl"];

#[cfg(any(
    target_os = "freebsd",
    target_os = "openbsd",
    target_os = "netbsd",
    target_os = "illumos"
))]
const CERT_DIRS: &[&str] = &["/etc/ssl", "/etc/ssl/certs"];

/// File extensions considered candidate certificate files. `.cer` and
/// `.crt` are PEM-wrapped X.509 on most distros; `.pem` is any PEM blob.
const CERT_EXTENSIONS: &[&str] = &["pem", "crt", "cer"];

pub fn collect() -> Result<Value> {
    let certificates = scan_dirs(CERT_DIRS);
    Ok(json!({ "certificates": certificates }))
}

/// Scan a list of directories for certificate files and return a
/// deterministic, sorted list of per-cert rows. `dirs` is overridable
/// so tests can point at fixture trees.
fn scan_dirs(dirs: &[&str]) -> Vec<Value> {
    let mut out = Vec::new();
    for dir in dirs {
        scan_dir(Path::new(dir), &mut out);
    }
    out.sort_by(|a, b| {
        a["path"]
            .as_str()
            .unwrap_or("")
            .cmp(b["path"].as_str().unwrap_or(""))
            .then_with(|| {
                a["serial"]
                    .as_str()
                    .unwrap_or("")
                    .cmp(b["serial"].as_str().unwrap_or(""))
            })
    });
    out
}

fn scan_dir(dir: &Path, out: &mut Vec<Value>) {
    let entries = match std::fs::read_dir(dir) {
        Ok(e) => e,
        Err(_) => return,
    };

    let mut files: Vec<PathBuf> = entries
        .flatten()
        .map(|e| e.path())
        .filter(|p| p.is_file())
        .filter(|p| is_cert_candidate(p))
        .filter(|p| {
            // Skip symlinks to avoid emitting duplicates (e.g.,
            // /etc/ssl/certs/ hash links that point at the same PEM).
            !p.symlink_metadata()
                .map(|m| m.file_type().is_symlink())
                .unwrap_or(false)
        })
        .collect();
    files.sort();

    for path in &files {
        parse_cert_file(path, out);
    }
}

fn is_cert_candidate(path: &Path) -> bool {
    match path.extension().and_then(|e| e.to_str()) {
        Some(ext) => CERT_EXTENSIONS.iter().any(|e| e.eq_ignore_ascii_case(ext)),
        None => false,
    }
}

/// Read a PEM file and emit one row per certificate in the file.
/// Concatenated-PEM bundles (multiple BEGIN CERTIFICATE blocks) are
/// supported — each block becomes its own row.
fn parse_cert_file(path: &Path, out: &mut Vec<Value>) {
    let data = match std::fs::read(path) {
        Ok(d) => d,
        Err(_) => return,
    };
    let path_str = path.to_string_lossy().to_string();

    // Iterate PEM blocks; ignore non-CERTIFICATE blocks silently.
    for pem in Pem::iter_from_buffer(&data) {
        let pem = match pem {
            Ok(p) => p,
            Err(_) => continue,
        };
        if pem.label != "CERTIFICATE" {
            continue;
        }
        if let Ok((_, cert)) = X509Certificate::from_der(&pem.contents) {
            out.push(cert_to_row(&path_str, &cert));
        }
    }
}

/// Extract the fields we report from a parsed certificate.
fn cert_to_row(path: &str, cert: &X509Certificate<'_>) -> Value {
    let subject = cert.subject().to_string();
    let issuer = cert.issuer().to_string();
    let cn = extract_cn(&subject);
    let sans = extract_sans(cert);
    let serial = cert.raw_serial_as_string();

    // Validity times. x509-parser returns ASN1Time; convert to Unix
    // seconds via timestamp() which returns i64 seconds since epoch.
    let not_before_ts = cert.validity().not_before.timestamp();
    let not_after_ts = cert.validity().not_after.timestamp();

    let not_before = format_rfc3339(not_before_ts);
    let not_after = format_rfc3339(not_after_ts);

    // days-remaining is NOT emitted: it is a pure function of not_after + now,
    // so it would tick daily and flap the ADR-033 inventory hash. The server
    // derives it from not_after at read time (server/certexpiry).
    json!({
        "path": path,
        "cn": cn,
        "subject": subject,
        "sans": sans,
        "issuer": issuer,
        "serial": serial,
        "not_before": not_before,
        "not_after": not_after,
        "expires": not_after, // back-compat with the old cert_expiry trait
        "is_ca": cert.is_ca(),
    })
}

/// Pull the Common Name out of a subject DN string like
/// `"CN=foo.example.com, O=Example, C=US"`. Returns an empty string
/// when no CN is present.
fn extract_cn(subject: &str) -> String {
    for rdn in subject.split(',') {
        let trimmed = rdn.trim();
        if let Some(rest) = trimmed.strip_prefix("CN=") {
            return rest.to_string();
        }
    }
    String::new()
}

/// Extract Subject Alternative Names (DNS + IP) from the cert's SAN
/// extension. Returns an empty list when the extension is absent.
fn extract_sans(cert: &X509Certificate<'_>) -> Vec<String> {
    let mut out = Vec::new();
    for ext in cert.extensions() {
        if let ParsedExtension::SubjectAlternativeName(san) = ext.parsed_extension() {
            for name in &san.general_names {
                match name {
                    GeneralName::DNSName(s) => out.push(s.to_string()),
                    GeneralName::IPAddress(b) => out.push(format_ip(b)),
                    _ => {}
                }
            }
        }
    }
    out
}

fn format_ip(bytes: &[u8]) -> String {
    match bytes.len() {
        4 => format!("{}.{}.{}.{}", bytes[0], bytes[1], bytes[2], bytes[3]),
        16 => {
            let mut s = String::new();
            for i in 0..8 {
                if i > 0 {
                    s.push(':');
                }
                s.push_str(&format!("{:x}", u16::from_be_bytes([bytes[i * 2], bytes[i * 2 + 1]])));
            }
            s
        }
        _ => String::new(),
    }
}

/// Format Unix seconds as RFC 3339 (`YYYY-MM-DDTHH:MM:SSZ`). Shares
/// calendar math with other trait collectors to avoid pulling in
/// `chrono` solely for date formatting.
fn format_rfc3339(ts: i64) -> String {
    if ts <= 0 {
        return String::new();
    }
    let mut days = ts / 86400;
    let mut secs = ts % 86400;
    if secs < 0 {
        days -= 1;
        secs += 86400;
    }
    let h = secs / 3600;
    let m = (secs % 3600) / 60;
    let s = secs % 60;

    let (year, month, day) = days_to_ymd(days);
    format!(
        "{:04}-{:02}-{:02}T{:02}:{:02}:{:02}Z",
        year, month, day, h, m, s
    )
}

fn days_to_ymd(mut days: i64) -> (i32, u32, u32) {
    let mut year: i32 = 1970;
    loop {
        let yd = if is_leap(year) { 366 } else { 365 };
        if days < yd {
            break;
        }
        days -= yd;
        year += 1;
    }
    let month_days: [i64; 12] = [
        31,
        if is_leap(year) { 29 } else { 28 },
        31,
        30,
        31,
        30,
        31,
        31,
        30,
        31,
        30,
        31,
    ];
    let mut month = 1u32;
    for &md in &month_days {
        if days < md {
            break;
        }
        days -= md;
        month += 1;
    }
    (year, month, (days + 1) as u32)
}

fn is_leap(y: i32) -> bool {
    (y % 4 == 0 && y % 100 != 0) || y % 400 == 0
}

#[cfg(test)]
mod tests {
    use super::*;
    use rcgen::{CertificateParams, KeyPair};
    use std::fs;

    fn generate_test_cert(cn: &str) -> String {
        let mut params = CertificateParams::new(vec![cn.to_string()]).expect("params");
        params.distinguished_name.push(rcgen::DnType::CommonName, cn);
        let key = KeyPair::generate().expect("keypair");
        let cert = params.self_signed(&key).expect("self-sign");
        cert.pem()
    }

    #[test]
    fn test_collect_returns_certificates_key() {
        let val = collect().unwrap();
        let obj = val.as_object().unwrap();
        assert!(obj.contains_key("certificates"));
        assert!(obj["certificates"].is_array());
    }

    #[test]
    fn test_scan_extracts_cn_and_validity() {
        let dir = tempfile::tempdir().unwrap();
        let pem = generate_test_cert("test.example.com");
        fs::write(dir.path().join("server.pem"), pem).unwrap();

        let certs = scan_dirs(&[dir.path().to_str().unwrap()]);
        assert_eq!(certs.len(), 1, "expected one cert, got {:?}", certs);

        let c = &certs[0];
        assert_eq!(c["cn"], "test.example.com");
        // rcgen defaults produce a wide validity window (roughly
        // 1975..4096). Assert the fields are present and RFC3339-shaped
        // rather than year-pinning.
        let not_after = c["not_after"].as_str().unwrap();
        let not_before = c["not_before"].as_str().unwrap();
        assert!(not_after.len() == 20 && not_after.ends_with('Z'), "not_after={}", not_after);
        assert!(not_before.len() == 20 && not_before.ends_with('Z'), "not_before={}", not_before);
        // days_remaining is intentionally NOT emitted (derived server-side from
        // not_after — see server/certexpiry); the collector stays deterministic.
        assert!(c.get("days_remaining").is_none(), "days_remaining must not be emitted");
        // SAN populated from the cn passed to CertificateParams::new.
        let sans: Vec<&str> = c["sans"]
            .as_array()
            .unwrap()
            .iter()
            .map(|v| v.as_str().unwrap())
            .collect();
        assert!(sans.contains(&"test.example.com"), "sans={:?}", sans);
    }

    #[test]
    fn test_scan_supports_bundle_pem() {
        // Concatenated-PEM bundle (the /etc/ssl/certs/ca-certificates.crt
        // pattern). Each cert in the file emits its own row.
        let dir = tempfile::tempdir().unwrap();
        let pem1 = generate_test_cert("cert1.example.com");
        let pem2 = generate_test_cert("cert2.example.com");
        fs::write(
            dir.path().join("ca-bundle.pem"),
            format!("{}\n{}", pem1, pem2),
        )
        .unwrap();

        let certs = scan_dirs(&[dir.path().to_str().unwrap()]);
        assert_eq!(certs.len(), 2);
        let cns: Vec<&str> = certs.iter().map(|c| c["cn"].as_str().unwrap()).collect();
        assert!(cns.contains(&"cert1.example.com"));
        assert!(cns.contains(&"cert2.example.com"));
    }

    #[test]
    fn test_scan_ignores_non_cert_files() {
        let dir = tempfile::tempdir().unwrap();
        fs::write(dir.path().join("readme.txt"), "not a cert").unwrap();
        fs::write(dir.path().join("config.json"), "{}").unwrap();
        // A .pem file with no CERTIFICATE blocks — valid PEM but wrong kind.
        fs::write(
            dir.path().join("key.pem"),
            "-----BEGIN PRIVATE KEY-----\nAAAA\n-----END PRIVATE KEY-----\n",
        )
        .unwrap();

        let certs = scan_dirs(&[dir.path().to_str().unwrap()]);
        assert_eq!(certs.len(), 0);
    }

    #[test]
    fn test_extract_cn() {
        assert_eq!(extract_cn("CN=foo, O=bar"), "foo");
        assert_eq!(extract_cn("O=bar, CN=foo.example.com"), "foo.example.com");
        assert_eq!(extract_cn("O=bar"), "");
    }

    #[test]
    fn test_format_rfc3339() {
        assert_eq!(format_rfc3339(0), "");
        assert_eq!(format_rfc3339(1_700_000_000), "2023-11-14T22:13:20Z");
    }

    #[test]
    fn test_format_ip_v4() {
        assert_eq!(format_ip(&[192, 168, 1, 1]), "192.168.1.1");
    }
}
