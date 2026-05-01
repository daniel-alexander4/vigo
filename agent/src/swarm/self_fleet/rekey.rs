// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

//! Self-fleet keypair rotation (ADR-018).
//!
//! Rekey is the recovery path for a compromised or lost fleet
//! keypair, and the maintenance path for hygiene rotation. The
//! founder runs `vigo swarm self-fleet rekey-pair` to generate a new
//! fleet keypair and issue a single-use claim code; surviving envoys
//! run `vigo swarm self-fleet rekey --from <host>` to claim the new
//! wrapped blob, migrate their cryptsync wrap envelope, and join the
//! new fleet identity. ADR-017 explicitly chose this over a fleet
//! "evict" verb — rotating the shared keypair is the only response
//! that addresses the underlying compromise.
//!
//! This module owns the parts that don't fit naturally in either
//! `cli.rs` (CLI verbs) or `pair.rs` (pair-specific HTTP handler):
//!
//! - the `RetiredFleetEntry` log persisted at
//!   `~/.vigo-fleet/retired_fleets.json`, including the active rekey
//!   session's 24h graceful window;
//! - the `POST /swarm/self-fleet/rekey-claim/<user>` HTTP handler;
//! - the post-rotation cryptsync sync (rewrap `identity.age`, clear
//!   stale TOFU pins, regenerate `recipients.txt`).
//!
//! The CLI verbs themselves live in `cli.rs` alongside `cmd_pair` /
//! `cmd_join` to keep the verb-level argv parsing in one place.

use std::fs;
use std::path::{Path, PathBuf};

use anyhow::{Context, Result};
use ed25519_dalek::SigningKey;
use http_body_util::Full;
use hyper::body::Bytes;
use hyper::{Request, Response, StatusCode};
use serde::{Deserialize, Serialize};

use super::codes::{self, parse_claim_path, ClaimRequest, MAX_CLAIM_BODY};
use super::config;

/// HTTP route prefix for the rekey claim endpoint. Sister of
/// `pair::PAIR_CLAIM_PREFIX`; both must stay distinct.
pub const REKEY_CLAIM_PREFIX: &str = "/swarm/self-fleet/rekey-claim/";

/// 24-hour graceful retirement window. After this, the founder
/// considers the rekey session closed and refuses to issue new claim
/// codes for the rotated-out fleet pubkey. Configurable per-call via
/// `--window-secs <N>` on `rekey-pair`; this is the default.
pub const REKEY_GRACEFUL_SECS: i64 = 24 * 3600;

/// One historical entry in `~/.vigo-fleet/retired_fleets.json`. The
/// active rekey session is the most-recent entry whose `expires_at`
/// is in the future. Older entries are kept for audit / status
/// display so the operator can see "we rotated on these dates."
#[derive(Debug, Clone, PartialEq, Eq, Serialize, Deserialize)]
pub struct RetiredFleetEntry {
    /// The fleet pubkey we rotated away from, in `ed25519:<hex>`
    /// form (matches `~/.vigo-fleet/identity.pub`).
    pub old_fleet_pubkey: String,
    /// The fleet pubkey we rotated to. Denormalized into the entry
    /// so a single file read suffices for `status` output.
    pub new_fleet_pubkey: String,
    /// Unix seconds when the rotation happened.
    pub retired_at: i64,
    /// Unix seconds after which the founder stops issuing new claim
    /// codes for surviving envoys still on the old fleet. Set to
    /// `retired_at + REKEY_GRACEFUL_SECS` at rotation time; can be
    /// pulled forward to `now` by `rekey-pair --close`.
    pub expires_at: i64,
}

/// Load the retired-fleets log. Missing file = empty list. Malformed
/// file = error (caller fails the rekey rather than risk silent
/// corruption of the audit trail).
pub fn load_retired(home: &Path) -> Result<Vec<RetiredFleetEntry>> {
    let path = config::retired_fleets_path(home);
    match fs::read_to_string(&path) {
        Ok(s) if s.trim().is_empty() => Ok(Vec::new()),
        Ok(s) => serde_json::from_str(&s)
            .with_context(|| format!("parsing {}", path.display())),
        Err(e) if e.kind() == std::io::ErrorKind::NotFound => Ok(Vec::new()),
        Err(e) => Err(e).with_context(|| format!("reading {}", path.display())),
    }
}

/// Atomic write of the retired-fleets log. 0600 user-owned. The file
/// is small (one entry per rotation event), so no compaction logic.
pub fn save_retired(
    home: &Path,
    entries: &[RetiredFleetEntry],
    username: &str,
) -> Result<()> {
    let path = config::retired_fleets_path(home);
    if let Some(parent) = path.parent() {
        fs::create_dir_all(parent)
            .with_context(|| format!("create {}", parent.display()))?;
    }
    let json = serde_json::to_string_pretty(entries)
        .context("serializing retired-fleets")?;
    let tmp = path.with_extension("json.tmp");
    {
        use std::io::Write;
        #[cfg(unix)]
        use std::os::unix::fs::OpenOptionsExt;
        let mut opts = fs::OpenOptions::new();
        opts.create(true).write(true).truncate(true);
        #[cfg(unix)]
        opts.mode(0o600);
        let mut f = opts
            .open(&tmp)
            .with_context(|| format!("create {}", tmp.display()))?;
        f.write_all(json.as_bytes())?;
        f.sync_all().ok();
    }
    fs::rename(&tmp, &path)
        .with_context(|| format!("rename to {}", path.display()))?;
    #[cfg(unix)]
    {
        let _ = crate::swarm::cryptsync::chown_to_user_pub(&path, username);
    }
    let _ = username;
    Ok(())
}

/// Return a reference to the active-session entry (most-recent
/// retirement whose `expires_at` is still in the future) or `None`
/// when no rekey session is currently live.
pub fn active_session<'a>(
    entries: &'a [RetiredFleetEntry],
    now: i64,
) -> Option<&'a RetiredFleetEntry> {
    entries
        .iter()
        .filter(|e| e.expires_at > now)
        .max_by_key(|e| e.retired_at)
}

/// Append a new retirement entry and persist atomically.
pub fn record_retirement(
    home: &Path,
    username: &str,
    old_fleet_pubkey: &str,
    new_fleet_pubkey: &str,
    retired_at: i64,
    window_secs: i64,
) -> Result<()> {
    let mut entries = load_retired(home)?;
    entries.push(RetiredFleetEntry {
        old_fleet_pubkey: old_fleet_pubkey.to_string(),
        new_fleet_pubkey: new_fleet_pubkey.to_string(),
        retired_at,
        expires_at: retired_at + window_secs,
    });
    save_retired(home, &entries, username)
}

/// Pull the active session's `expires_at` to `now`, effectively
/// closing the rekey window. If no active session exists, this is a
/// no-op. Returns the number of entries adjusted (0 or 1).
pub fn close_active_session(
    home: &Path,
    username: &str,
    now: i64,
) -> Result<usize> {
    let mut entries = load_retired(home)?;
    let mut adjusted = 0usize;
    let mut active_idx: Option<usize> = None;
    let mut latest_retired_at = i64::MIN;
    for (i, entry) in entries.iter().enumerate() {
        if entry.expires_at > now && entry.retired_at >= latest_retired_at {
            latest_retired_at = entry.retired_at;
            active_idx = Some(i);
        }
    }
    if let Some(i) = active_idx {
        entries[i].expires_at = now;
        adjusted = 1;
        save_retired(home, &entries, username)?;
    }
    Ok(adjusted)
}

/// Sentinel I/O for the rekey claim code. Thin path-binding wrappers
/// over `codes::*` — the same shape as `pair::write_pending` / etc.
pub fn write_pending(home: &Path, code: &str, username: &str) -> Result<()> {
    codes::write_pending(&config::rekey_pending_path(home), code, username)
}
pub fn read_pending(home: &Path) -> Result<Option<codes::PendingSpec>> {
    codes::read_pending(&config::rekey_pending_path(home))
}
pub fn clear_pending(home: &Path) {
    codes::clear_pending(&config::rekey_pending_path(home));
}

/// Post-rotation cryptsync sync. Runs on every envoy that has
/// rotated its self-fleet keypair (founder during `rekey-pair`,
/// surviving envoys during `rekey`). Three steps, all best-effort
/// after the fleet rotation has already committed to disk:
///
/// 1. Re-wrap `~/.cryptsync/identity.age` from the old fleet-derived
///    wrap key to the new one. Skipped when cryptsync isn't enrolled
///    or runs in standalone wrap-mode.
/// 2. Clear local cryptsync TOFU pins for the retired fleet pubkey
///    so the next announce from a peer that has also rekeyed re-pins
///    cleanly under the new fleet pubkey.
/// 3. Regenerate `recipients.txt` so post-rotation writes go to the
///    correct recipient set immediately, instead of waiting for the
///    next announce tick.
///
/// Returns the cryptsync rewrap outcome for the caller's UX.
pub fn apply_post_rotation_cryptsync_sync(
    home: &Path,
    username: &str,
    old: &SigningKey,
    new: &SigningKey,
    old_fleet_pubkey_hex: &str,
) -> Result<crate::swarm::cryptsync::identity_rewrap::RewrapOutcome> {
    let outcome = crate::swarm::cryptsync::identity_rewrap::rewrap_identity_for_new_fleet_key(
        home, username, old, new,
    )
    .context("rewrap cryptsync identity for new fleet keypair")?;

    // Clear stale TOFU pins so post-rotation peer announces re-pin.
    // Best-effort: if the peer table doesn't exist yet (cryptsync not
    // enrolled), this is a no-op. The hex form lives in peer
    // entries' `fleet_pubkey` field, so strip the `ed25519:` prefix.
    let retired_hex = old_fleet_pubkey_hex
        .strip_prefix("ed25519:")
        .unwrap_or(old_fleet_pubkey_hex)
        .to_ascii_lowercase();
    let _ = crate::swarm::cryptsync::state::PeerTable::modify(username, |table| {
        table.clear_pins_for_fleet(&retired_hex) > 0
    });

    // Regenerate recipients.txt so the next encrypt sees the new
    // fleet filter. Best-effort — when cryptsync isn't enrolled the
    // recipients.txt path doesn't exist and the call no-ops at the
    // load_local_pubkey step. Don't propagate errors here; the
    // rotation itself has already succeeded on disk.
    if let Err(e) = crate::swarm::cryptsync::regenerate_recipients_now(username, home) {
        tracing::debug!(
            error = %e,
            "self-fleet rekey: regenerate_recipients_now skipped (cryptsync not enrolled?)"
        );
    }

    Ok(outcome)
}

/// HTTP handler for `POST /swarm/self-fleet/rekey-claim/<user>`.
/// Mirrors `pair::handle_pair_claim` with two differences:
///
/// - reads `~/.vigo-fleet/rekey.pending` instead of `pair.pending`;
/// - additionally rejects (410 Gone) if no active rekey session
///   exists in `retired_fleets.json` for this user, even when a
///   `rekey.pending` sentinel survives — covers the case where the
///   24h window has elapsed but the sentinel hasn't been GC'd yet.
pub async fn handle_rekey_claim(
    path: &str,
    req: Request<hyper::body::Incoming>,
) -> Result<Response<Full<Bytes>>> {
    let user = match parse_claim_path(path, REKEY_CLAIM_PREFIX) {
        Some(u) => u,
        None => {
            return Ok(codes::plain(StatusCode::BAD_REQUEST, "malformed path"))
        }
    };
    let home = match crate::swarm::cryptsync::home_for_user_pub(&user) {
        Some(h) => h,
        None => return Ok(codes::plain(StatusCode::NOT_FOUND, "no such user")),
    };

    let body_bytes = match codes::read_body_capped(req, MAX_CLAIM_BODY).await {
        Ok(b) => b,
        Err(e) => {
            return Ok(codes::plain(
                StatusCode::BAD_REQUEST,
                &format!("body: {}", e),
            ))
        }
    };
    let claim: ClaimRequest = match serde_json::from_slice(&body_bytes) {
        Ok(c) => c,
        Err(_) => {
            return Ok(codes::plain(
                StatusCode::BAD_REQUEST,
                "malformed claim body (expected JSON {\"code\":\"NNNN-NNNN\"})",
            ))
        }
    };

    // 24h-window check first — protects against a stale sentinel
    // outliving its retirement entry.
    let now = codes::now_secs();
    let entries = match load_retired(&home) {
        Ok(e) => e,
        Err(e) => {
            tracing::warn!(error = %e, "self-fleet rekey: cannot read retired_fleets");
            return Ok(codes::plain(
                StatusCode::INTERNAL_SERVER_ERROR,
                "rekey state read failed",
            ));
        }
    };
    if active_session(&entries, now).is_none() {
        // Either no rotation has happened or the window has lapsed.
        // Clear the orphan sentinel as a side effect so future probes
        // don't keep triggering this branch.
        clear_pending(&home);
        return Ok(codes::plain(StatusCode::GONE, "no active rekey session"));
    }

    let spec = match read_pending(&home) {
        Ok(Some(s)) => s,
        Ok(None) => {
            return Ok(codes::plain(
                StatusCode::NOT_FOUND,
                "no active rekey claim",
            ))
        }
        Err(e) => {
            tracing::warn!(error = %e, "self-fleet rekey: cannot read pending");
            return Ok(codes::plain(
                StatusCode::INTERNAL_SERVER_ERROR,
                "rekey state read failed",
            ));
        }
    };
    if now >= spec.expires_at {
        clear_pending(&home);
        return Ok(codes::plain(StatusCode::GONE, "rekey claim code expired"));
    }
    if !codes::hashes_equal(&codes::hash_code(&claim.code), &spec.code_hash) {
        return Ok(codes::plain(StatusCode::UNAUTHORIZED, "code mismatch"));
    }

    let wrapped_path = config::wrapped_path(&home);
    let wrapped = match fs::read(&wrapped_path) {
        Ok(b) => b,
        Err(e) => {
            tracing::warn!(
                path = %wrapped_path.display(),
                error = %e,
                "self-fleet rekey: cannot read wrapped blob"
            );
            return Ok(codes::plain(
                StatusCode::INTERNAL_SERVER_ERROR,
                "wrapped read failed",
            ));
        }
    };
    let salt_path = config::salt_path(&home);
    let salt = match fs::read(&salt_path) {
        Ok(b) => b,
        Err(e) => {
            tracing::warn!(
                path = %salt_path.display(),
                error = %e,
                "self-fleet rekey: cannot read salt"
            );
            return Ok(codes::plain(
                StatusCode::INTERNAL_SERVER_ERROR,
                "salt read failed",
            ));
        }
    };
    clear_pending(&home);

    let body = match codes::pack_response(&salt, &wrapped) {
        Ok(b) => b,
        Err(e) => {
            tracing::warn!(error = %e, "self-fleet rekey: pack_response failed");
            return Ok(codes::plain(
                StatusCode::INTERNAL_SERVER_ERROR,
                "response pack failed",
            ));
        }
    };

    tracing::info!(
        user = %user,
        "self-fleet rekey: served new wrapped identity to claimant"
    );
    Response::builder()
        .status(StatusCode::OK)
        .header("Content-Type", "application/octet-stream")
        .body(Full::new(Bytes::from(body)))
        .context("building rekey response")
}

/// Sibling of `super::config::session_unlocked_path` — the rekey
/// flow checks for a running self-fleet session helper to decide
/// whether it can derive the old wrap password locally without
/// re-prompting. Exposed as a public helper so `cli.rs` can use the
/// same predicate without re-implementing the path resolution.
pub fn session_helper_running(home: &Path) -> bool {
    let path = config::session_unlocked_path(home);
    matches!(fs::metadata(&path), Ok(_))
}

/// Local convenience for sibling subsystems / status output. Returns
/// the path to the retired-fleets log even if the file doesn't yet
/// exist.
pub fn retired_fleets_path_for(home: &Path) -> PathBuf {
    config::retired_fleets_path(home)
}

#[cfg(test)]
mod tests {
    use super::*;

    fn entry(old: &str, new: &str, retired_at: i64, expires_at: i64) -> RetiredFleetEntry {
        RetiredFleetEntry {
            old_fleet_pubkey: old.to_string(),
            new_fleet_pubkey: new.to_string(),
            retired_at,
            expires_at,
        }
    }

    #[test]
    fn load_retired_missing_file_returns_empty() {
        let dir = tempfile::tempdir().unwrap();
        let entries = load_retired(dir.path()).unwrap();
        assert!(entries.is_empty());
    }

    #[test]
    fn save_then_load_retired_roundtrips() {
        let dir = tempfile::tempdir().unwrap();
        // dot dir created lazily by save.
        let entries = vec![entry("ed25519:old", "ed25519:new", 1000, 100_000)];
        save_retired(dir.path(), &entries, "tester").unwrap();
        let reloaded = load_retired(dir.path()).unwrap();
        assert_eq!(reloaded, entries);
    }

    #[test]
    fn active_session_returns_most_recent_unexpired() {
        let entries = vec![
            entry("a", "b", 100, 200),   // expired at now=300
            entry("b", "c", 250, 1000),  // active
            entry("c", "d", 500, 2000),  // active, more recent
        ];
        let now = 300;
        let active = active_session(&entries, now).unwrap();
        assert_eq!(active.old_fleet_pubkey, "c");
        assert_eq!(active.retired_at, 500);
    }

    #[test]
    fn active_session_none_when_all_expired() {
        let entries = vec![entry("a", "b", 100, 200), entry("b", "c", 250, 290)];
        assert!(active_session(&entries, 1000).is_none());
    }

    #[test]
    fn record_retirement_appends() {
        let dir = tempfile::tempdir().unwrap();
        record_retirement(dir.path(), "tester", "old1", "new1", 1000, 86400).unwrap();
        record_retirement(dir.path(), "tester", "old2", "new2", 2000, 86400).unwrap();
        let entries = load_retired(dir.path()).unwrap();
        assert_eq!(entries.len(), 2);
        assert_eq!(entries[0].old_fleet_pubkey, "old1");
        assert_eq!(entries[1].old_fleet_pubkey, "old2");
        assert_eq!(entries[1].expires_at, 2000 + 86400);
    }

    #[test]
    fn close_active_session_pulls_expires_at_forward() {
        let dir = tempfile::tempdir().unwrap();
        record_retirement(dir.path(), "tester", "old", "new", 1000, 86400).unwrap();
        let now = 5000;
        let adjusted = close_active_session(dir.path(), "tester", now).unwrap();
        assert_eq!(adjusted, 1);
        let entries = load_retired(dir.path()).unwrap();
        assert_eq!(entries[0].expires_at, now);
        // active_session at `now` returns None — the entry's
        // expires_at is exactly `now`, and the predicate is `> now`.
        assert!(active_session(&entries, now).is_none());
    }

    #[test]
    fn close_active_session_no_op_when_no_active() {
        let dir = tempfile::tempdir().unwrap();
        let adjusted = close_active_session(dir.path(), "tester", 100).unwrap();
        assert_eq!(adjusted, 0);
    }

    #[test]
    fn close_active_session_only_touches_latest_active() {
        let dir = tempfile::tempdir().unwrap();
        // Two retirements; the older expired, the newer is active.
        record_retirement(dir.path(), "tester", "old1", "new1", 1000, 100).unwrap();
        record_retirement(dir.path(), "tester", "old2", "new2", 2000, 86400).unwrap();
        let now = 5000;
        close_active_session(dir.path(), "tester", now).unwrap();
        let entries = load_retired(dir.path()).unwrap();
        // Older entry's expires_at unchanged.
        assert_eq!(entries[0].expires_at, 1000 + 100);
        // Newer entry now closed.
        assert_eq!(entries[1].expires_at, now);
    }

    #[test]
    fn rekey_pending_lives_at_rekey_pending_path() {
        let dir = tempfile::tempdir().unwrap();
        let username = std::env::var("USER").unwrap_or_else(|_| "nobody".into());
        write_pending(dir.path(), "1234-5678", &username).unwrap();
        // pair.pending must be untouched.
        assert!(!config::pair_pending_path(dir.path()).exists());
        // rekey.pending must exist.
        assert!(config::rekey_pending_path(dir.path()).exists());
        let spec = read_pending(dir.path()).unwrap().unwrap();
        assert_eq!(spec.code_hash, codes::hash_code("1234-5678"));
        clear_pending(dir.path());
        assert!(read_pending(dir.path()).unwrap().is_none());
    }

    #[test]
    fn rekey_claim_prefix_is_distinct_from_pair() {
        // Sister-route lockdown — both must share the
        // `/swarm/self-fleet/` namespace without colliding.
        assert_ne!(REKEY_CLAIM_PREFIX, super::super::pair::PAIR_CLAIM_PREFIX);
        assert!(REKEY_CLAIM_PREFIX.starts_with("/swarm/self-fleet/"));
    }
}
