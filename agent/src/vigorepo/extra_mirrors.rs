// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

//! Source-side direct push for cross-subnet `extra_mirrors`.
//!
//! When a `.vigorepo.yaml` sidecar lists explicit targets via
//! `extra_mirrors:`, the source envoy opens an mTLS connection
//! to each named host on port 1531 and POSTs the bundle to
//! `/vigorepo/push/<label>`. The receiver validates the request
//! and integrates the bundle the same way it would handle a
//! LAN-discovered mirror (inject manifest entry + ingest LAN
//! claim + materialize on next reactor tick).
//!
//! Unlike the LAN path (Phase 5/6 multicast discovery + claim
//! announcement, followed by pull-based swarm download), the
//! cross-subnet path is **push-the-blob**: the source sends the
//! full bundle body in a single POST. This is simpler than
//! orchestrating a pull-back over the existing swarm transport
//! — the operator explicitly named the target, the bundles are
//! small enough (tens to hundreds of MB for a typical personal
//! repo) that a single upload is fine, and it works even when
//! the receiver can't reach back to the source through a NAT.
//!
//! Failure modes:
//!
//! - DNS resolution failure → log + retry next tick
//! - TCP connect refused → log + retry next tick
//! - mTLS handshake failure → log (usually a cert mismatch) +
//!   retry next tick
//! - HTTP 4xx/5xx → log the status + reason, retry next tick
//! - Wrong hostname in cert SAN → rustls rejects at handshake;
//!   this catches typos in `extra_mirrors:` that point at a
//!   non-fleet host
//!
//! Every failure is non-fatal — the source keeps retrying on
//! every reactor tick until the push succeeds. The next
//! successful push refreshes the receiver's LAN claim TTL, so
//! transient outages don't age mirrors out.

use std::path::Path;

use anyhow::{anyhow, Context, Result};
use tracing::debug;

use crate::update::TlsCerts;

/// Port the swarm peer HTTPS server listens on. Matches
/// `swarm::server::DEFAULT_PORT`.
pub const SWARM_PEER_PORT: u16 = 1531;

/// Push a single bundle to one `extra_mirrors` target. Blocking —
/// runs on the reactor's blocking-worker thread.
///
/// Arguments:
/// - `host`: the target hostname from `extra_mirrors:`
/// - `label`: full content-seed label
/// - `seeder`: this envoy's canonical hostname
/// - `bundle_path`: on-disk path to the bundle file (the swarm
///   seed file produced by the publisher)
/// - `tls`: mTLS certs for the fleet CA
///
/// Returns `Ok(())` on 200 from the target, or a descriptive
/// error for the caller to log. Never panics; never retries
/// internally (retry cadence is managed by the reactor tick).
pub fn push_bundle(
    host: &str,
    label: &str,
    seeder: &str,
    bundle_path: &Path,
    tls: &TlsCerts,
) -> Result<()> {
    // Read bundle bytes. For phase 7 we buffer the whole file in
    // memory — bundles cap at MAX_BUNDLE_SIZE (20 GiB) but the
    // realistic personal-DR case is tens to hundreds of MB.
    // Streaming upload is a straightforward follow-up if big
    // bundles become common.
    let data = std::fs::read(bundle_path)
        .with_context(|| format!("read bundle {}", bundle_path.display()))?;
    let size = data.len() as u64;
    if size > crate::vigorepo::MAX_BUNDLE_SIZE {
        return Err(anyhow!(
            "bundle {} exceeds MAX_BUNDLE_SIZE ({} bytes > {})",
            bundle_path.display(),
            size,
            crate::vigorepo::MAX_BUNDLE_SIZE,
        ));
    }

    let sha = sha256_hex(&data);

    // Build a fresh mTLS agent per call. The existing swarm
    // peer_client caches one agent for the LAN download path; we
    // could reuse it, but push is low-frequency (once per
    // reactor tick per target, typically a handful of targets)
    // and building the agent per-call keeps the dependency graph
    // simple. Optimization deferred.
    let agent = crate::swarm::peer_client::build_peer_agent(tls)
        .context("build mTLS agent for vigorepo push")?;

    let url = format!(
        "https://{}:{}/vigorepo/push/{}",
        host,
        SWARM_PEER_PORT,
        url_encode_label(label),
    );

    debug!(
        host = host,
        label = label,
        size,
        sha = %&sha[..12.min(sha.len())],
        "vigorepo: pushing bundle to cross-subnet target"
    );

    let resp = agent
        .post(&url)
        .header("x-vigo-seeder", seeder)
        .header("x-vigo-blob-sha256", sha.as_str())
        .header("content-type", "application/octet-stream")
        .send(&data[..])
        .map_err(|e| anyhow!("POST {} failed: {}", url, e))?;

    let status = resp.status();
    if status == 200 {
        Ok(())
    } else {
        let body = resp
            .into_body()
            .read_to_string()
            .unwrap_or_else(|_| "<unreadable body>".to_string());
        Err(anyhow!(
            "POST {} returned {}: {}",
            url,
            status,
            body.trim()
        ))
    }
}

/// Percent-encode the colons in a vigorepo label for use in a URL
/// path segment. `vigorepo:h:u:p` becomes `vigorepo%3Ah%3Au%3Ap`.
/// The receiver's [`crate::swarm::server::url_decode`] reverses
/// this before parsing the label.
fn url_encode_label(label: &str) -> String {
    let mut out = String::with_capacity(label.len() + label.matches(':').count() * 2);
    for c in label.chars() {
        if c == ':' {
            out.push_str("%3A");
        } else {
            out.push(c);
        }
    }
    out
}

/// Compute a hex-encoded sha256 of a byte slice. Kept local so
/// this module has no dep on the swarm server crate.
fn sha256_hex(data: &[u8]) -> String {
    use sha2::{Digest, Sha256};
    let mut h = Sha256::new();
    h.update(data);
    let out = h.finalize();
    let mut s = String::with_capacity(64);
    for b in out.iter() {
        s.push_str(&format!("{:02x}", b));
    }
    s
}

/// Per-target push state tracked in the reactor's `PassState`.
/// Used to dedup pushes: don't re-upload a bundle to a target
/// that already acknowledged the same sha. The dedup is in-
/// memory and resets on agent restart — a restart forces one
/// fresh push per (label, target, sha) combo, which refreshes
/// the target's LAN claim TTL and is functionally correct.
#[derive(Debug, Clone, Default)]
pub struct ExtraPushState {
    /// Last successfully-pushed `(label, target_host) -> blob_sha`.
    /// A new tick pushes only if the current sha differs from the
    /// cached one, OR if the cached entry has aged out.
    last_success: std::collections::HashMap<(String, String), LastSuccess>,
}

#[derive(Debug, Clone)]
struct LastSuccess {
    sha: String,
    at_unix: u64,
}

/// Safety-net re-push window: even without a sha change, push
/// again after this many seconds so the receiver's LAN claim TTL
/// (90 seconds) gets refreshed before it ages out. Matches the
/// Phase 6 `RE_ANNOUNCE_INTERVAL_SECS` value for consistency.
pub const RE_PUSH_INTERVAL_SECS: u64 = 60;

impl ExtraPushState {
    pub fn new() -> Self {
        Self::default()
    }

    /// Returns true if `(label, target)` should be pushed this
    /// tick. True on first-time pushes, content changes, and
    /// after the re-push interval elapses.
    pub fn should_push(&self, label: &str, target: &str, current_sha: &str, now: u64) -> bool {
        let key = (label.to_string(), target.to_string());
        match self.last_success.get(&key) {
            None => true,
            Some(last) => {
                last.sha != current_sha
                    || now.saturating_sub(last.at_unix) >= RE_PUSH_INTERVAL_SECS
            }
        }
    }

    /// Record a successful push. Called by the reactor after
    /// [`push_bundle`] returns `Ok(())`.
    pub fn record_success(&mut self, label: &str, target: &str, sha: &str, now: u64) {
        self.last_success.insert(
            (label.to_string(), target.to_string()),
            LastSuccess {
                sha: sha.to_string(),
                at_unix: now,
            },
        );
    }

    /// Drop the record for a `(label, target)` pair — used when
    /// a source is paused or its sidecar removes the target from
    /// `extra_mirrors:`.
    pub fn forget(&mut self, label: &str, target: &str) {
        self.last_success
            .remove(&(label.to_string(), target.to_string()));
    }

    /// Drop all records for a given label (paused or deleted
    /// source). O(n) walk, acceptable for small maps.
    pub fn forget_label(&mut self, label: &str) {
        self.last_success.retain(|(l, _), _| l != label);
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn url_encode_label_replaces_colons() {
        assert_eq!(
            url_encode_label("vigorepo:danlap:dan:vigo-web"),
            "vigorepo%3Adanlap%3Adan%3Avigo-web",
        );
    }

    #[test]
    fn url_encode_label_preserves_non_colon_chars() {
        assert_eq!(url_encode_label("abc-123_xyz"), "abc-123_xyz");
    }

    #[test]
    fn sha256_hex_matches_known_value() {
        // sha256("") = e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855
        assert_eq!(
            sha256_hex(b""),
            "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855",
        );
    }

    #[test]
    fn should_push_is_true_on_first_call() {
        let s = ExtraPushState::new();
        assert!(s.should_push("label", "host", "sha1", 1000));
    }

    #[test]
    fn should_push_is_false_within_interval_when_unchanged() {
        let mut s = ExtraPushState::new();
        s.record_success("label", "host", "sha1", 1000);
        // Same sha, only 10 seconds later — still in the
        // re-push cool-down window.
        assert!(!s.should_push("label", "host", "sha1", 1010));
    }

    #[test]
    fn should_push_is_true_when_sha_changes() {
        let mut s = ExtraPushState::new();
        s.record_success("label", "host", "sha1", 1000);
        assert!(s.should_push("label", "host", "sha2", 1010));
    }

    #[test]
    fn should_push_is_true_after_re_push_interval() {
        let mut s = ExtraPushState::new();
        s.record_success("label", "host", "sha1", 1000);
        // Advance past the re-push interval.
        assert!(s.should_push("label", "host", "sha1", 1000 + RE_PUSH_INTERVAL_SECS));
    }

    #[test]
    fn should_push_is_per_target_not_per_label() {
        let mut s = ExtraPushState::new();
        s.record_success("label", "host-a", "sha1", 1000);
        // Different target — the cool-down doesn't apply.
        assert!(s.should_push("label", "host-b", "sha1", 1010));
    }

    #[test]
    fn forget_clears_one_target_only() {
        let mut s = ExtraPushState::new();
        s.record_success("label", "host-a", "sha1", 1000);
        s.record_success("label", "host-b", "sha1", 1000);
        s.forget("label", "host-a");
        // host-a should re-push immediately, host-b should not.
        assert!(s.should_push("label", "host-a", "sha1", 1010));
        assert!(!s.should_push("label", "host-b", "sha1", 1010));
    }

    #[test]
    fn forget_label_clears_all_targets_for_that_label() {
        let mut s = ExtraPushState::new();
        s.record_success("label-a", "host-1", "sha1", 1000);
        s.record_success("label-a", "host-2", "sha1", 1000);
        s.record_success("label-b", "host-1", "sha2", 1000);
        s.forget_label("label-a");
        assert!(s.should_push("label-a", "host-1", "sha1", 1010));
        assert!(s.should_push("label-a", "host-2", "sha1", 1010));
        assert!(!s.should_push("label-b", "host-1", "sha2", 1010));
    }
}
