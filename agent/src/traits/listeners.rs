// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

//! Listener attestation trait collector (Linux-only).
//!
//! Extends the simple `ports` trait with per-listener process
//! attribution: which PID, which binary path, and which bind address is
//! actually holding each listening TCP socket. Enables fleet-wide
//! questions like "what's listening on :443 across 200 hosts" without
//! needing shell access to each one.
//!
//! Method: read `/proc/net/tcp` + `/proc/net/tcp6` for LISTEN-state
//! socket inodes, then walk `/proc/*/fd/*` once to map each inode back
//! to its owning PID. Both steps are pure sysfs reads — no subprocess
//! spawns and no network activity. Short-circuits early once every
//! discovered listening inode has been matched.
//!
//! Scope deliberately does NOT include cipher/protocol/cert inspection
//! — that requires a real TLS handshake, and the collector is
//! explicitly passive. Cert inventory lives in `cert_lifecycle`.

use anyhow::Result;
use serde_json::{json, Value};
use std::collections::BTreeMap;

pub fn collect() -> Result<Value> {
    let listeners = collect_from_roots("/proc");
    Ok(json!({ "listeners": listeners }))
}

/// Listener rows discovered from a `/proc` tree. `proc_root` is
/// overridable so tests can point at a fixture directory.
fn collect_from_roots(proc_root: &str) -> Vec<Value> {
    let mut by_inode: BTreeMap<u64, ListenerSocket> = BTreeMap::new();

    parse_proc_net_tcp(
        &format!("{}/net/tcp", proc_root),
        "ipv4",
        &mut by_inode,
    );
    parse_proc_net_tcp(
        &format!("{}/net/tcp6", proc_root),
        "ipv6",
        &mut by_inode,
    );

    if by_inode.is_empty() {
        return Vec::new();
    }

    attribute_to_pids(proc_root, &mut by_inode);

    let mut out: Vec<Value> = by_inode
        .into_values()
        .map(|s| {
            json!({
                "port": s.port,
                "family": s.family,
                "protocol": "tcp",
                "bind_address": s.bind_address,
                "pid": s.pid,
                "comm": s.comm,
                "exe": s.exe,
            })
        })
        .collect();

    // Deterministic: sort by port, then family, then pid. Stable output
    // makes diffing across check-ins readable.
    out.sort_by(|a, b| {
        a["port"]
            .as_u64()
            .unwrap_or(0)
            .cmp(&b["port"].as_u64().unwrap_or(0))
            .then_with(|| {
                a["family"]
                    .as_str()
                    .unwrap_or("")
                    .cmp(b["family"].as_str().unwrap_or(""))
            })
            .then_with(|| {
                a["pid"]
                    .as_u64()
                    .unwrap_or(0)
                    .cmp(&b["pid"].as_u64().unwrap_or(0))
            })
    });
    out
}

#[derive(Default, Clone)]
struct ListenerSocket {
    port: u16,
    family: &'static str, // "ipv4" | "ipv6"
    bind_address: String,
    pid: Option<u32>,
    comm: String,
    exe: String,
}

/// Parse a `/proc/net/tcp*` file. Lines in LISTEN state (`0A`) are
/// recorded by inode so the follow-up `/proc/*/fd/*` walk can attribute
/// them to a PID.
fn parse_proc_net_tcp(path: &str, family: &'static str, out: &mut BTreeMap<u64, ListenerSocket>) {
    let content = match std::fs::read_to_string(path) {
        Ok(c) => c,
        Err(_) => return,
    };

    for line in content.lines().skip(1) {
        let fields: Vec<&str> = line.split_whitespace().collect();
        // We need: local_address (1), state (3), inode (9).
        if fields.len() < 10 {
            continue;
        }
        if fields[3] != "0A" {
            continue;
        }
        let inode: u64 = match fields[9].parse() {
            Ok(n) => n,
            Err(_) => continue,
        };
        if inode == 0 {
            continue;
        }
        let (bind_address, port) = match parse_local_address(fields[1], family) {
            Some(pair) => pair,
            None => continue,
        };
        out.insert(
            inode,
            ListenerSocket {
                port,
                family,
                bind_address,
                ..ListenerSocket::default()
            },
        );
    }
}

/// Parse the `local_address` column from `/proc/net/tcp*`. IPv4 arrives
/// as 8 hex chars (little-endian bytes) followed by `:` and the port in
/// hex. IPv6 arrives as 32 hex chars plus `:PORT`.
fn parse_local_address(field: &str, family: &'static str) -> Option<(String, u16)> {
    let (addr_hex, port_hex) = field.split_once(':')?;
    let port = u16::from_str_radix(port_hex, 16).ok()?;
    let addr = match family {
        "ipv4" => parse_ipv4(addr_hex)?,
        "ipv6" => parse_ipv6(addr_hex)?,
        _ => return None,
    };
    Some((addr, port))
}

fn parse_ipv4(hex: &str) -> Option<String> {
    if hex.len() != 8 {
        return None;
    }
    // Bytes are little-endian in /proc/net/tcp.
    let b0 = u8::from_str_radix(&hex[6..8], 16).ok()?;
    let b1 = u8::from_str_radix(&hex[4..6], 16).ok()?;
    let b2 = u8::from_str_radix(&hex[2..4], 16).ok()?;
    let b3 = u8::from_str_radix(&hex[0..2], 16).ok()?;
    Some(format!("{}.{}.{}.{}", b0, b1, b2, b3))
}

fn parse_ipv6(hex: &str) -> Option<String> {
    if hex.len() != 32 {
        return None;
    }
    // The kernel dumps each 32-bit word little-endian but concatenates
    // the four words in network order — reverse bytes within each word.
    let mut bytes = [0u8; 16];
    for word in 0..4 {
        for byte in 0..4 {
            let hex_pos = word * 8 + (3 - byte) * 2;
            bytes[word * 4 + byte] =
                u8::from_str_radix(&hex[hex_pos..hex_pos + 2], 16).ok()?;
        }
    }
    let mut s = String::new();
    for i in 0..8 {
        if i > 0 {
            s.push(':');
        }
        let word = u16::from_be_bytes([bytes[i * 2], bytes[i * 2 + 1]]);
        s.push_str(&format!("{:x}", word));
    }
    Some(s)
}

/// Walk `/proc/*/fd/*`, resolve each symlink, and match `socket:[<inode>]`
/// entries against the set of listening inodes we already collected. The
/// first PID that owns an inode wins; processes that share the socket
/// via fork() resolve deterministically to the first numerical PID seen.
fn attribute_to_pids(proc_root: &str, by_inode: &mut BTreeMap<u64, ListenerSocket>) {
    let entries = match std::fs::read_dir(proc_root) {
        Ok(e) => e,
        Err(_) => return,
    };

    let remaining = by_inode.len();
    let mut matched = 0usize;

    for entry in entries.flatten() {
        if matched >= remaining {
            return;
        }
        let name = match entry.file_name().into_string() {
            Ok(s) => s,
            Err(_) => continue,
        };
        let pid: u32 = match name.parse() {
            Ok(n) => n,
            Err(_) => continue, // non-PID /proc entries (self, sys, etc.)
        };

        let fd_dir = entry.path().join("fd");
        let fd_entries = match std::fs::read_dir(&fd_dir) {
            Ok(e) => e,
            Err(_) => continue, // process may have exited, or we lack permission
        };

        let mut pid_matched_any = false;
        for fd_entry in fd_entries.flatten() {
            let target = match std::fs::read_link(fd_entry.path()) {
                Ok(p) => p,
                Err(_) => continue,
            };
            let target = target.to_string_lossy();
            let inode = match extract_socket_inode(&target) {
                Some(n) => n,
                None => continue,
            };
            if let Some(sock) = by_inode.get_mut(&inode) {
                if sock.pid.is_none() {
                    sock.pid = Some(pid);
                    pid_matched_any = true;
                    matched += 1;
                }
            }
        }

        if pid_matched_any {
            let comm = read_comm(&entry.path());
            let exe = read_exe(&entry.path());
            // Backfill every socket we attributed to this PID.
            for sock in by_inode.values_mut() {
                if sock.pid == Some(pid) && sock.comm.is_empty() {
                    sock.comm = comm.clone();
                    sock.exe = exe.clone();
                }
            }
        }
    }
}

/// Extract the inode from a kernel-style symlink target like
/// `socket:[123456]`. Returns `None` for non-socket fd targets.
fn extract_socket_inode(target: &str) -> Option<u64> {
    let rest = target.strip_prefix("socket:[")?;
    let inner = rest.strip_suffix(']')?;
    inner.parse().ok()
}

fn read_comm(proc_pid_dir: &std::path::Path) -> String {
    std::fs::read_to_string(proc_pid_dir.join("comm"))
        .map(|s| s.trim().to_string())
        .unwrap_or_default()
}

fn read_exe(proc_pid_dir: &std::path::Path) -> String {
    std::fs::read_link(proc_pid_dir.join("exe"))
        .map(|p| p.to_string_lossy().to_string())
        .unwrap_or_default()
}

#[cfg(test)]
mod tests {
    use super::*;
    use std::fs;
    use std::os::unix::fs::symlink;

    #[test]
    fn test_collect_returns_listeners_key() {
        let val = collect().unwrap();
        let obj = val.as_object().unwrap();
        assert!(obj.contains_key("listeners"));
        assert!(obj["listeners"].is_array());
    }

    #[test]
    fn test_parse_ipv4() {
        // 0x0100007F little-endian = 127.0.0.1
        assert_eq!(parse_ipv4("0100007F").as_deref(), Some("127.0.0.1"));
        // 0x00000000 = 0.0.0.0 (any)
        assert_eq!(parse_ipv4("00000000").as_deref(), Some("0.0.0.0"));
    }

    #[test]
    fn test_extract_socket_inode() {
        assert_eq!(extract_socket_inode("socket:[12345]"), Some(12345));
        assert_eq!(extract_socket_inode("pipe:[99]"), None);
        assert_eq!(extract_socket_inode("/dev/null"), None);
    }

    #[test]
    fn test_full_flow() {
        let dir = tempfile::tempdir().unwrap();
        let proc_root = dir.path();

        // /proc/net/tcp fixture with two LISTEN sockets at ports 22 and
        // 443 (inodes 11111, 22222) plus one ESTABLISHED (not counted).
        let net = proc_root.join("net");
        fs::create_dir_all(&net).unwrap();
        fs::write(
            net.join("tcp"),
            "  sl  local_address rem_address   st tx_queue rx_queue tr tm->when retrnsmt   uid  timeout inode
   0: 00000000:0016 00000000:0000 0A 00000000:00000000 00:00000000 00000000     0        0 11111 1 0000000000000000 100 0 0 10 0
   1: 0100007F:01BB 00000000:0000 0A 00000000:00000000 00:00000000 00000000     0        0 22222 1 0000000000000000 100 0 0 10 0
   2: 00000000:0050 AABBCCDD:BEEF 01 00000000:00000000 00:00000000 00000000     0        0 33333 1 0000000000000000 100 0 0 10 0
",
        )
        .unwrap();

        // Process 1234 owns both sockets. /proc/1234/fd/3 and .../fd/4
        // are symlinks to socket:[11111] and socket:[22222].
        let pid_dir = proc_root.join("1234");
        let fd_dir = pid_dir.join("fd");
        fs::create_dir_all(&fd_dir).unwrap();
        symlink("socket:[11111]", fd_dir.join("3")).unwrap();
        symlink("socket:[22222]", fd_dir.join("4")).unwrap();
        symlink("/dev/null", fd_dir.join("0")).unwrap();
        fs::write(pid_dir.join("comm"), "nginx\n").unwrap();
        symlink("/usr/sbin/nginx", pid_dir.join("exe")).unwrap();

        // A second PID with no relevant sockets — shouldn't interfere.
        let other = proc_root.join("5678");
        fs::create_dir_all(other.join("fd")).unwrap();
        fs::write(other.join("comm"), "bash\n").unwrap();

        let listeners = collect_from_roots(proc_root.to_str().unwrap());

        assert_eq!(listeners.len(), 2);
        // Sorted by port: 22 first, then 443.
        assert_eq!(listeners[0]["port"], 22);
        assert_eq!(listeners[0]["bind_address"], "0.0.0.0");
        assert_eq!(listeners[0]["pid"], 1234);
        assert_eq!(listeners[0]["comm"], "nginx");
        assert_eq!(listeners[0]["exe"], "/usr/sbin/nginx");

        assert_eq!(listeners[1]["port"], 443);
        assert_eq!(listeners[1]["bind_address"], "127.0.0.1");
        assert_eq!(listeners[1]["pid"], 1234);
    }

    #[test]
    fn test_listener_without_pid_still_emits() {
        // When the agent can't see the owning process (permission, or
        // the process already exited), we still emit the port row with
        // pid=null. That's more useful than dropping the entry.
        let dir = tempfile::tempdir().unwrap();
        let net = dir.path().join("net");
        fs::create_dir_all(&net).unwrap();
        fs::write(
            net.join("tcp"),
            "  sl  local_address rem_address   st tx_queue rx_queue tr tm->when retrnsmt   uid  timeout inode
   0: 00000000:22B8 00000000:0000 0A 00000000:00000000 00:00000000 00000000     0        0 99999 1 0000000000000000 100 0 0 10 0
",
        )
        .unwrap();

        let listeners = collect_from_roots(dir.path().to_str().unwrap());
        assert_eq!(listeners.len(), 1);
        assert_eq!(listeners[0]["port"], 0x22B8); // 8888
        assert!(listeners[0]["pid"].is_null());
        assert_eq!(listeners[0]["comm"], "");
    }
}
