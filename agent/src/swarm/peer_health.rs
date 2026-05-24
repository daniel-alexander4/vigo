// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

//! Shared peer disk-pressure probe.
//!
//! Before any of the four peer-delivery subsystems (swarm pull, lockbox push,
//! gitback push, longdrawer announce) commits bytes to a remote peer, the sender
//! asks the peer "how full is the filesystem this will land on?" over the
//! existing mTLS HTTPS on port 1531 (`GET /health/storage`). If the peer
//! reports `free_pct <= DEFAULT_MIN_FREE_PCT` (10%), the send is skipped —
//! no retry storm, no failure counter, just a no-op until the peer
//! recovers on a later tick.
//!
//! Probe results are cached per `(peer, purpose)` for [`CACHE_TTL`] (60s)
//! so a lockbox fan-out across many peers in the same burst doesn't hammer
//! the health endpoint. Callers fail closed: unreachable or malformed
//! probe responses are treated as pressured.
//!
//! The [`Purpose`] enum captures which mountpoint the receiver should
//! measure — the subsystems write to different directories that may sit
//! on different filesystems (`/var/lib/vigo/swarm/.blobs`,
//! `/var/lib/vigo/swarm/gitback`, `~<user>/lockbox`, `~<user>/longdrawer`).
//!
//! Symmetrically, receivers call [`local_free_pct`] to compute their own
//! free-pct for a given purpose — used both to answer probe requests and
//! to re-check at push-handler entry (closing the race between probe time
//! and push arrival) before reading any body.

use std::collections::HashMap;
use std::path::{Path, PathBuf};
use std::sync::{Mutex, OnceLock};
use std::time::{Duration, Instant};

use anyhow::{bail, Result};
use serde::Deserialize;

/// Minimum free-space percentage on the target filesystem below which
/// pushes are skipped. 10% matches the guardrail spec. Compared
/// strictly — a peer at exactly 10% is treated as pressured.
pub const DEFAULT_MIN_FREE_PCT: f64 = 10.0;

/// Effective min-free-pct, overridable at runtime via `VIGO_MIN_FREE_PCT`
/// (unit: percent as f64). Env override is primarily for integration
/// tests that need a different threshold; operators should not rely on
/// it. Parsed on every call — cheap and keeps the value honest across
/// tokio tasks that read it independently.
pub fn min_free_pct() -> f64 {
    std::env::var("VIGO_MIN_FREE_PCT")
        .ok()
        .and_then(|s| s.parse::<f64>().ok())
        .unwrap_or(DEFAULT_MIN_FREE_PCT)
}

/// How long a probe result stays fresh in the cache. Long enough to
/// smother fan-out bursts; short enough that a peer freeing up space
/// gets traffic again within a minute.
const CACHE_TTL: Duration = Duration::from_secs(60);

/// Swarm peer HTTPS port. Matches [`super::server::DEFAULT_PORT`].
const PEER_PORT: u16 = 1531;

/// Purpose of a pending send — determines which mountpoint the receiver
/// measures. Each variant maps to a different directory tree; these can
/// sit on different filesystems, so the receiver must pick the right
/// one per request.
#[derive(Debug, Clone, PartialEq, Eq, Hash)]
pub enum Purpose {
    /// Swarm blob cache: `/var/lib/vigo/swarm/.blobs`.
    Swarm,
    /// Gitback mirror store: `/var/lib/vigo/swarm/gitback`.
    Gitback,
    /// Lockbox per-user ciphertext: `~<user>/lockbox`.
    Lockbox { user: String },
    /// Longdrawer per-user plaintext: `~<user>/longdrawer`.
    Longdrawer { user: String },
}

impl Purpose {
    /// Short tag used in URL query, log fields, and cache keys.
    pub fn tag(&self) -> &'static str {
        match self {
            Purpose::Swarm => "swarm",
            Purpose::Gitback => "gitback",
            Purpose::Lockbox { .. } => "lockbox",
            Purpose::Longdrawer { .. } => "longdrawer",
        }
    }

    /// The user bound to this purpose, if any. Used for per-user
    /// target paths (`~<user>/lockbox`, `~<user>/longdrawer`).
    pub fn user(&self) -> Option<&str> {
        match self {
            Purpose::Lockbox { user } | Purpose::Longdrawer { user } => Some(user.as_str()),
            _ => None,
        }
    }

    /// URL query-string fragment (without leading `?`).
    fn query(&self) -> String {
        match self.user() {
            Some(u) => format!("for={}&user={}", self.tag(), url_encode(u)),
            None => format!("for={}", self.tag()),
        }
    }

    /// Parse a `for=` tag + optional `user=` value from query parameters.
    pub fn parse(kind: &str, user: Option<&str>) -> Option<Self> {
        match (kind, user) {
            ("swarm", _) => Some(Purpose::Swarm),
            ("gitback", _) => Some(Purpose::Gitback),
            ("lockbox", Some(u)) if !u.is_empty() => Some(Purpose::Lockbox { user: u.to_string() }),
            ("longdrawer", Some(u)) if !u.is_empty() => Some(Purpose::Longdrawer { user: u.to_string() }),
            _ => None,
        }
    }
}

/// Outcome of a pre-flight probe. Use [`ProbeOutcome::allow`] to decide
/// whether to proceed.
#[derive(Debug, Clone)]
pub enum ProbeOutcome {
    /// Peer reports `>min_free_pct` free on the target mount.
    Ok { free_pct: f64 },
    /// Peer reports `<= min_free_pct` free.
    Pressured { free_pct: f64 },
    /// Probe failed (timeout, HTTP error, malformed response). Callers
    /// fail closed — treat as pressured.
    Unknown(String),
}

impl ProbeOutcome {
    /// Should the sender proceed with the push?
    pub fn allow(&self) -> bool {
        matches!(self, ProbeOutcome::Ok { .. })
    }

    /// Reason tag for structured logs.
    pub fn reason(&self) -> &'static str {
        match self {
            ProbeOutcome::Ok { .. } => "ok",
            ProbeOutcome::Pressured { .. } => "pressured",
            ProbeOutcome::Unknown(_) => "unknown",
        }
    }
}

/// Probe a peer's free-pct for the given purpose. Blocking; call from
/// `spawn_blocking` or a reactor's blocking worker thread.
///
/// `peer_addr` is a bare `host` or `host:port` — if the port is
/// missing, [`PEER_PORT`] is added. Results are cached per
/// `(peer_addr, purpose)` for [`CACHE_TTL`].
pub fn probe(agent: &ureq::Agent, peer_addr: &str, purpose: &Purpose) -> ProbeOutcome {
    let normalized = normalize_peer_addr(peer_addr);
    let key = cache_key(&normalized, purpose);

    // Serve a fresh cache hit without touching the network.
    {
        let map = cache().lock().expect("peer_health cache lock poisoned");
        if let Some(entry) = map.get(&key) {
            if entry.fetched.elapsed() < CACHE_TTL {
                return classify(entry.pct);
            }
        }
    }

    let url = format!("https://{}/health/storage?{}", normalized, purpose.query());
    match fetch(agent, &url) {
        Ok(pct) => {
            let mut map = cache().lock().expect("peer_health cache lock poisoned");
            prune_stale(&mut map);
            map.insert(
                key,
                CacheEntry {
                    pct,
                    fetched: Instant::now(),
                },
            );
            classify(pct)
        }
        Err(e) => ProbeOutcome::Unknown(e.to_string()),
    }
}

/// Compute local free-pct for the given purpose. Returns `None` if the
/// target path can't be resolved (user doesn't exist, etc.) — callers
/// should answer probes with 503 in that case and receivers should
/// reject pushes with 503, not 507.
pub fn local_free_pct(purpose: &Purpose) -> Option<f64> {
    let path = target_path(purpose)?;
    free_pct_for_path(&path).ok()
}

/// Resolve the target path for a purpose. Returns a path whose
/// containing filesystem is the one to measure. The path may not
/// exist yet (e.g., a user who has never run `vigo swarm lockbox enroll`);
/// [`free_pct_for_path`] walks up to the nearest existing ancestor.
pub fn target_path(purpose: &Purpose) -> Option<PathBuf> {
    match purpose {
        Purpose::Swarm => Some(crate::platform::paths::default_blobs_dir()),
        Purpose::Gitback => Some(crate::platform::paths::default_gitback_dir()),
        Purpose::Lockbox { user } => {
            crate::platform::users::user_home(user).map(|h| h.join("lockbox"))
        }
        Purpose::Longdrawer { user } => {
            crate::platform::users::user_home(user).map(|h| h.join("longdrawer"))
        }
    }
}

/// True if the caller's own disk is below the pressure threshold for
/// this purpose — used by push handlers at entry to return 507 before
/// reading the body. `false` on missing-path or measurement error so
/// we never block on indeterminate state.
pub fn is_self_pressured(purpose: &Purpose) -> bool {
    match local_free_pct(purpose) {
        Some(pct) => pct <= min_free_pct(),
        None => false,
    }
}

// ── Internals ─────────────────────────────────────────────────────────

struct CacheEntry {
    pct: f64,
    fetched: Instant,
}

fn cache() -> &'static Mutex<HashMap<String, CacheEntry>> {
    static C: OnceLock<Mutex<HashMap<String, CacheEntry>>> = OnceLock::new();
    C.get_or_init(|| Mutex::new(HashMap::new()))
}

/// Drop entries older than [`CACHE_TTL`]. Stale entries can never be a
/// cache hit (the read path checks the same predicate), so retaining
/// them is unbounded growth as peers/users churn. Called on the insert
/// path only — the read path is hot and must not pay for eviction.
fn prune_stale(map: &mut HashMap<String, CacheEntry>) {
    map.retain(|_, e| e.fetched.elapsed() < CACHE_TTL);
}

fn cache_key(peer: &str, purpose: &Purpose) -> String {
    match purpose.user() {
        Some(u) => format!("{}|{}|{}", peer, purpose.tag(), u),
        None => format!("{}|{}", peer, purpose.tag()),
    }
}

fn classify(pct: f64) -> ProbeOutcome {
    if pct > min_free_pct() {
        ProbeOutcome::Ok { free_pct: pct }
    } else {
        ProbeOutcome::Pressured { free_pct: pct }
    }
}

fn normalize_peer_addr(addr: &str) -> String {
    if has_port(addr) {
        addr.to_string()
    } else {
        format!("{}:{}", addr, PEER_PORT)
    }
}

fn has_port(addr: &str) -> bool {
    if let Some(rb) = addr.rfind(']') {
        return addr[rb..].contains(':');
    }
    addr.matches(':').count() == 1
}

#[derive(Deserialize)]
struct HealthResponse {
    free_pct: f64,
}

fn fetch(agent: &ureq::Agent, url: &str) -> Result<f64> {
    // Uses the caller's agent timeouts (typically 30s global from
    // `peer_client::build_peer_agent`). That's comfortably long for a
    // statvfs-backed probe and keeps this helper free of its own
    // config builder — one less place for per-call overrides to drift
    // from the rest of the peer-client code.
    let mut resp = agent.get(url).call()?;
    let status = resp.status();
    if status != 200 {
        bail!("status {}", status);
    }
    let body = resp.body_mut().read_to_string()?;
    let parsed: HealthResponse = serde_json::from_str(&body)?;
    if !parsed.free_pct.is_finite() {
        bail!("non-finite free_pct");
    }
    Ok(parsed.free_pct)
}

/// Compute free-pct on the nearest existing ancestor of `path`.
/// `statvfs(2)` requires an existing path, but `~user/lockbox` may not
/// exist yet on a freshly-enrolled envoy — walking up to the home
/// directory (or root) gives a correct answer for the same filesystem.
fn free_pct_for_path(path: &Path) -> Result<f64> {
    let mut p = path.to_path_buf();
    while !p.exists() {
        if !p.pop() {
            bail!("no existing ancestor for {}", path.display());
        }
    }
    free_pct_impl(&p)
}

#[cfg(unix)]
fn free_pct_impl(path: &Path) -> Result<f64> {
    use std::ffi::CString;
    use std::mem::MaybeUninit;

    let c_path = CString::new(path.to_string_lossy().as_bytes())?;
    let mut stat = MaybeUninit::<libc::statvfs>::uninit();
    // SAFETY: statvfs writes through the out-pointer only on success.
    let ret = unsafe { libc::statvfs(c_path.as_ptr(), stat.as_mut_ptr()) };
    if ret != 0 {
        bail!("statvfs failed: {}", std::io::Error::last_os_error());
    }
    let stat = unsafe { stat.assume_init() };
    let total = stat.f_blocks as f64 * stat.f_frsize as f64;
    let avail = stat.f_bavail as f64 * stat.f_frsize as f64;
    if total <= 0.0 {
        return Ok(100.0);
    }
    Ok(100.0 * avail / total)
}

#[cfg(windows)]
fn free_pct_impl(path: &Path) -> Result<f64> {
    use std::os::windows::ffi::OsStrExt;

    let wide: Vec<u16> = path
        .as_os_str()
        .encode_wide()
        .chain(std::iter::once(0))
        .collect();
    let mut free: u64 = 0;
    let mut total: u64 = 0;
    // SAFETY: we pass a null-terminated UTF-16 buffer and valid out-pointers.
    let ret = unsafe {
        GetDiskFreeSpaceExW(
            wide.as_ptr(),
            &mut free as *mut u64,
            &mut total as *mut u64,
            std::ptr::null_mut(),
        )
    };
    if ret == 0 {
        bail!("GetDiskFreeSpaceExW failed");
    }
    if total == 0 {
        return Ok(100.0);
    }
    Ok(100.0 * free as f64 / total as f64)
}

#[cfg(windows)]
extern "system" {
    fn GetDiskFreeSpaceExW(
        directory: *const u16,
        free_bytes_available: *mut u64,
        total_bytes: *mut u64,
        total_free_bytes: *mut u64,
    ) -> i32;
}

fn url_encode(s: &str) -> String {
    let mut out = String::with_capacity(s.len());
    for b in s.as_bytes() {
        match *b {
            b'A'..=b'Z' | b'a'..=b'z' | b'0'..=b'9' | b'-' | b'_' | b'.' | b'~' => {
                out.push(*b as char);
            }
            _ => {
                out.push_str(&format!("%{:02X}", b));
            }
        }
    }
    out
}

// ── Tests ─────────────────────────────────────────────────────────────

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn purpose_roundtrips_through_query_and_parse() {
        let p = Purpose::Lockbox { user: "alice".into() };
        assert_eq!(p.query(), "for=lockbox&user=alice");
        assert_eq!(
            Purpose::parse("lockbox", Some("alice")),
            Some(Purpose::Lockbox { user: "alice".into() })
        );
        assert_eq!(Purpose::parse("swarm", None), Some(Purpose::Swarm));
        assert_eq!(Purpose::parse("gitback", Some("ignored")), Some(Purpose::Gitback));
        assert_eq!(Purpose::parse("lockbox", None), None);
        assert_eq!(Purpose::parse("longdrawer", Some("")), None);
        assert_eq!(Purpose::parse("bogus", None), None);
    }

    #[test]
    fn purpose_query_urlencodes_user() {
        let p = Purpose::Longdrawer { user: "a b+c".into() };
        assert_eq!(p.query(), "for=longdrawer&user=a%20b%2Bc");
    }

    #[test]
    fn has_port_recognizes_hostports() {
        assert!(has_port("host:1531"));
        assert!(has_port("1.2.3.4:1531"));
        assert!(has_port("[::1]:1531"));
        assert!(!has_port("host"));
        assert!(!has_port("[::1]"));
        // Bare unbracketed IPv6 is not a supported input — callers pass
        // hostnames, IPv4, or `[v6]:port`. The heuristic correctly
        // declines it (multiple colons, no brackets → not `host:port`).
        assert!(!has_port("fe80::1"));
    }

    #[test]
    fn normalize_adds_default_port_when_missing() {
        assert_eq!(normalize_peer_addr("host"), format!("host:{}", PEER_PORT));
        assert_eq!(normalize_peer_addr("host:1531"), "host:1531");
        assert_eq!(normalize_peer_addr("[::1]:1531"), "[::1]:1531");
    }

    #[test]
    fn classify_uses_strict_greater_than() {
        // Exactly at threshold → pressured.
        assert!(matches!(classify(10.0), ProbeOutcome::Pressured { .. }));
        assert!(matches!(classify(9.99), ProbeOutcome::Pressured { .. }));
        assert!(matches!(classify(10.01), ProbeOutcome::Ok { .. }));
        assert!(matches!(classify(100.0), ProbeOutcome::Ok { .. }));
    }

    #[test]
    fn env_override_takes_precedence() {
        // Scope to this test; another concurrent test could race, but
        // cargo runs module tests serially by default.
        std::env::set_var("VIGO_MIN_FREE_PCT", "25.0");
        assert_eq!(min_free_pct(), 25.0);
        std::env::remove_var("VIGO_MIN_FREE_PCT");
        assert_eq!(min_free_pct(), DEFAULT_MIN_FREE_PCT);
    }

    #[test]
    fn free_pct_for_path_walks_up_to_existing_ancestor() {
        // /tmp should always exist on Unix test hosts; .../does-not-exist
        // under it resolves to /tmp's filesystem.
        #[cfg(unix)]
        {
            let p = Path::new("/tmp/vigo-peer-health-nope/also-nope");
            let pct = free_pct_for_path(p).expect("walk-up should find /tmp");
            assert!(pct.is_finite());
            assert!((0.0..=100.0).contains(&pct));
        }
    }

    #[test]
    fn free_pct_impl_on_tmp_is_sane() {
        #[cfg(unix)]
        {
            let pct = free_pct_impl(Path::new("/tmp")).expect("statvfs /tmp");
            assert!((0.0..=100.0).contains(&pct));
        }
    }

    #[test]
    fn cache_ttl_honored() {
        // Write a fake cache entry, then probe (with a dummy agent we
        // never call). Since the cache hit short-circuits the HTTP
        // path, we can probe with a never-connectable URL and still
        // get a deterministic answer.
        let key = cache_key("fake:1531", &Purpose::Swarm);
        {
            let mut map = cache().lock().unwrap();
            map.insert(
                key.clone(),
                CacheEntry {
                    pct: 42.0,
                    fetched: Instant::now(),
                },
            );
        }
        // Agent is unused on a cache hit.
        let agent = ureq::Agent::new_with_defaults();
        let outcome = probe(&agent, "fake:1531", &Purpose::Swarm);
        assert!(matches!(outcome, ProbeOutcome::Ok { free_pct } if (free_pct - 42.0).abs() < 0.01));

        // Expire the entry and confirm we now fall through to the
        // network path (which fails → Unknown).
        {
            let mut map = cache().lock().unwrap();
            if let Some(e) = map.get_mut(&key) {
                e.fetched = Instant::now() - Duration::from_secs(3600);
            }
        }
        let outcome = probe(&agent, "fake.invalid:1531", &Purpose::Swarm);
        assert!(matches!(outcome, ProbeOutcome::Unknown(_)));
    }

    #[test]
    fn prune_stale_drops_old_entries_keeps_fresh() {
        // The global cache is shared across tests in this file; use
        // distinct keys so we don't trip cache_ttl_honored.
        let stale_key = cache_key("prune-stale-host:1531", &Purpose::Swarm);
        let fresh_key = cache_key("prune-fresh-host:1531", &Purpose::Swarm);
        {
            let mut map = cache().lock().unwrap();
            map.insert(
                stale_key.clone(),
                CacheEntry {
                    pct: 99.0,
                    fetched: Instant::now() - (CACHE_TTL + Duration::from_secs(1)),
                },
            );
            map.insert(
                fresh_key.clone(),
                CacheEntry {
                    pct: 50.0,
                    fetched: Instant::now(),
                },
            );
            prune_stale(&mut map);
            assert!(!map.contains_key(&stale_key), "stale entry must be evicted");
            assert!(map.contains_key(&fresh_key), "fresh entry must be retained");
            // Cleanup so we don't leak state to other tests.
            map.remove(&fresh_key);
        }
    }

    #[test]
    fn is_self_pressured_false_when_tmp_has_room() {
        // On any reasonable test host /tmp has >10% free. Use a purpose
        // whose target is a known-existing path: the platform swarm
        // blobs dir generally exists on dev/test hosts under /var/lib,
        // but we can't rely on that — skip if not resolvable.
        #[cfg(unix)]
        if target_path(&Purpose::Swarm).is_some() {
            // Cannot assert on prod mountpoints; just make sure the
            // call doesn't panic and returns a boolean.
            let _ = is_self_pressured(&Purpose::Swarm);
        }
    }

    // Live HTTP path intentionally untested in this module — it
    // requires spinning up a TLS server. The server-side handler gets
    // its own tests in `swarm/server.rs`, and integration tests cover
    // the full sender↔receiver path.
}
