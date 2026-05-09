// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

//! HTTPS route handlers for lockbox. Piggybacks on the swarm peer
//! HTTPS server on port 1531 — the peer is already mTLS-authenticated
//! by the swarm server, so any agent that reached us is trusted.
//!
//! Routes (dispatched from `agent/src/swarm/server.rs`):
//!
//! - `GET /swarm/lockbox/pull/{user}/{filename}` — serve ciphertext
//!   bytes from `~<user>/lockbox/<filename>`. Mirrors longdrawer's pull
//!   path but with a `/pull/` prefix segment for clarity (lockbox
//!   has a matching `/push/`, longdrawer does not).
//!
//! - `POST /swarm/lockbox/push/{user}/{filename}` — receive a
//!   ciphertext blob from a peer and write it into
//!   `~<user>/lockbox/<filename>` atomically. Body capped at 1 GiB
//!   (vs. longdrawer's 128 KiB cap on `/swarm/longdrawer/announce` — push
//!   is a different beast).
//!
//! - `POST /swarm/lockbox/announce` — receive an Announcement JSON
//!   from a cross-subnet peer listed in some local user's
//!   `cross_subnet_peers:`. Body capped at 128 KiB. Feeds into the same
//!   reconciler channel as multicast discovery.
//!
//! Note: Week 2 ships the push/pull handlers so the routes are wired
//! and exercised by tests, but the sender-side fan-out that actually
//! calls them lives in Week 3 (see `encrypt` verb + inotify watcher).

use std::fs;
use std::io::Write;
use std::net::SocketAddr;
use std::path::PathBuf;

use http_body_util::{BodyExt, Full};
use hyper::body::Bytes;
use hyper::{Request, Response, StatusCode};

use crate::platform::users::user_home;

use super::leaves::{self, LeaveEnvelope};
use super::resurrects::{self, ResurrectEnvelope, ResurrectRecord, ResurrectsTable};
use super::state::PeerTable;
use super::tombstones::{
    self, effective_tombstone, DeleteEnvelope, Tombstone, TombstoneTable,
};

/// Compile-time fallback for the max push size. Read sites should
/// prefer [`max_push_bytes`] which consults the server-distributed
/// runtime limits cache (`swarm.lockbox.max_file_bytes` in
/// server.yaml). The constant is kept so other modules that pin
/// against it (auto-encrypt, doc cross-refs) keep a stable name.
pub const MAX_PUSH_BYTES: usize = 1024 * 1024 * 1024; // 1 GiB

/// Effective per-push ciphertext cap. Reads from the runtime-limits
/// cache populated on every CheckIn from `server.yaml swarm.lockbox.
/// max_file_bytes`; falls back to `MAX_PUSH_BYTES` when the server
/// hasn't published a value yet.
pub fn max_push_bytes() -> usize {
    super::super::runtime_limits::lockbox_max_file_bytes() as usize
}

/// Max size of an announcement body.
pub const MAX_ANNOUNCE_BYTES: usize = 128 * 1024;

// ---------- pull (GET /swarm/lockbox/pull/{user}/{filename}) ----------

/// Handle `GET /swarm/lockbox/pull/{user}/{filename}` — serve one
/// ciphertext file. Returns 404 for missing files, 400 for malformed
/// paths. Does blocking I/O; caller should wrap in
/// `tokio::task::block_in_place`.
pub fn handle_pull(path: &str) -> Response<Full<Bytes>> {
    let segments: Vec<&str> = path.trim_start_matches('/').split('/').collect();
    let (user, name) = match segments.as_slice() {
        ["swarm", "lockbox", "pull", user, name] => (*user, *name),
        _ => return not_found(),
    };

    let user = match percent_decode(user) {
        Some(s) => s,
        None => return bad_request("invalid user encoding"),
    };
    let name = match percent_decode(name) {
        Some(s) => s,
        None => return bad_request("invalid filename encoding"),
    };
    if !valid_username(&user) {
        return bad_request("invalid user");
    }
    if !valid_filename(&name) {
        return bad_request("invalid filename");
    }

    let home = match user_home(&user) {
        Some(h) => h,
        None => return not_found(),
    };

    // Anti-resurrection — if this filename is effectively tombstoned
    // (tombstone exists AND no later resurrect supersedes it), return
    // 410 Gone instead of serving bytes. The puller interprets 410 as
    // "learn about this delete" and records the tombstone locally.
    // Operator override via `vigo swarm lockbox resurrect <file>`
    // lifts the gate by recording a resurrect with `resurrect_time >=
    // deletion_time` — see `tombstones::effective_tombstone`.
    let tombstones = TombstoneTable::load(&user);
    let resurrects = ResurrectsTable::load(&user);
    if let Some(t) = effective_tombstone(&name, &tombstones, &resurrects) {
        return gone_with_tombstone(&name, t.deletion_time);
    }

    let target = home.join("lockbox").join(&name);

    // Canonicalize to defeat symlink-based traversal. Refuse to serve
    // anything that resolves outside ~<user>/lockbox/.
    let content_dir = home.join("lockbox");
    let canon_target = match target.canonicalize() {
        Ok(p) => p,
        Err(_) => return not_found(),
    };
    let canon_dir = match content_dir.canonicalize() {
        Ok(p) => p,
        Err(_) => return not_found(),
    };
    if !canon_target.starts_with(&canon_dir) {
        return not_found();
    }

    let meta = match fs::metadata(&canon_target) {
        Ok(m) => m,
        Err(_) => return not_found(),
    };
    if !meta.is_file() {
        return not_found();
    }
    if meta.len() as usize > max_push_bytes() {
        return payload_too_large();
    }
    match fs::read(&canon_target) {
        Ok(bytes) => Response::builder()
            .status(StatusCode::OK)
            .header("Content-Type", "application/octet-stream")
            .header("Content-Length", bytes.len().to_string())
            .body(Full::new(Bytes::from(bytes)))
            .unwrap(),
        Err(_) => not_found(),
    }
}

// ---------- push (POST /swarm/lockbox/push/{user}/{filename}) ----------

/// Handle `POST /swarm/lockbox/push/{user}/{filename}` — write a
/// ciphertext blob into `~<user>/lockbox/<filename>` via temp-file
/// + rename. Returns 201 on success, 400/404/413 on error.
pub async fn handle_push(
    path: &str,
    req: Request<hyper::body::Incoming>,
) -> Response<Full<Bytes>> {
    let segments: Vec<&str> = path.trim_start_matches('/').split('/').collect();
    let (user, name) = match segments.as_slice() {
        ["swarm", "lockbox", "push", user, name] => (*user, *name),
        _ => return not_found(),
    };
    let user = match percent_decode(user) {
        Some(s) => s,
        None => return bad_request("invalid user encoding"),
    };
    let name = match percent_decode(name) {
        Some(s) => s,
        None => return bad_request("invalid filename encoding"),
    };
    if !valid_username(&user) {
        return bad_request("invalid user");
    }
    if !valid_filename(&name) {
        return bad_request("invalid filename");
    }

    // Receiver-side disk-pressure guard. Closes the race between the
    // sender's pre-flight probe and the push arriving — if free-pct on
    // ~<user>/lockbox has dropped at or below the threshold since then,
    // reject 507 before reading a single body byte. Cheap statvfs call.
    {
        use crate::swarm::peer_health::{is_self_pressured, Purpose};
        let purpose = Purpose::Lockbox { user: user.clone() };
        if is_self_pressured(&purpose) {
            return insufficient_storage("target filesystem under disk pressure");
        }
    }

    // Pre-check Content-Length (cheap rejection without reading body).
    if let Some(cl) = req
        .headers()
        .get("content-length")
        .and_then(|v| v.to_str().ok())
        .and_then(|s| s.parse::<usize>().ok())
    {
        if cl > max_push_bytes() {
            return payload_too_large();
        }
    }

    // Drain body with streaming cap.
    let body = req.into_body();
    let collected = match body.collect().await {
        Ok(c) => c,
        Err(_) => return bad_request("read body"),
    };
    let bytes = collected.to_bytes();
    if bytes.len() > max_push_bytes() {
        return payload_too_large();
    }

    // Defense-in-depth: require the first bytes to be the age magic.
    // Rejects anyone trying to push plaintext through this endpoint.
    const AGE_MAGIC: &[u8] = b"age-encryption.org/v1\n";
    if bytes.len() < AGE_MAGIC.len() || &bytes[..AGE_MAGIC.len()] != AGE_MAGIC {
        return bad_request("pushed body is not age-encrypted");
    }

    // Anti-resurrection — if this filename is effectively tombstoned
    // (tombstone exists AND no later resurrect supersedes it), reject
    // the push with 410 Gone. The pusher doesn't carry an mtime in
    // v1, so we don't try to compare ages — a tombstone wins until
    // the operator explicitly resurrects via
    // `vigo swarm lockbox resurrect <file>`, which records a
    // resurrect (`resurrect_time >= deletion_time`) and lifts the
    // gate. See `tombstones::effective_tombstone` and the resurrect
    // verb in `cli.rs`.
    {
        let tombstones = TombstoneTable::load(&user);
        let resurrects = ResurrectsTable::load(&user);
        if let Some(t) = effective_tombstone(&name, &tombstones, &resurrects) {
            return gone_with_tombstone(&name, t.deletion_time);
        }
    }

    let home = match user_home(&user) {
        Some(h) => h,
        None => return not_found(),
    };
    let content_dir = home.join("lockbox");
    // Create the content dir if missing — pushing to a freshly-enrolled
    // envoy may arrive before the user runs `vigo swarm lockbox {fleet,standalone} init`
    // locally (rare race but valid; the envoy can't decrypt without the
    // identity but the ciphertext is still legal to store).
    if let Err(e) = fs::create_dir_all(&content_dir) {
        tracing::warn!(
            dir = %content_dir.display(),
            error = %e,
            "lockbox push: cannot create content dir"
        );
        return internal_error("cannot create content dir");
    }

    let target = content_dir.join(&name);
    let tmp: PathBuf = content_dir.join(format!(".{}.lockbox.push.tmp", name));
    if let Err(e) = write_tmp(&tmp, &bytes) {
        tracing::warn!(
            path = %tmp.display(),
            error = %e,
            "lockbox push: tmp write failed"
        );
        let _ = fs::remove_file(&tmp);
        return internal_error("write failed");
    }
    match super::finalize_user_ciphertext(&tmp, &target, &user) {
        Ok(()) => Response::builder()
            .status(StatusCode::CREATED)
            .header("Content-Type", "application/json")
            .body(Full::new(Bytes::from(r#"{"status":"accepted"}"#)))
            .unwrap(),
        Err(e) => {
            tracing::warn!(
                path = %target.display(),
                error = %e,
                "lockbox push: finalize failed"
            );
            let _ = fs::remove_file(&tmp);
            internal_error("write failed")
        }
    }
}

/// Write the pushed ciphertext to a temp path with mode 0600. The
/// dotfile name keeps it hidden during the brief window before
/// `finalize_user_ciphertext` chowns + renames it into place.
fn write_tmp(tmp: &std::path::Path, data: &[u8]) -> std::io::Result<()> {
    #[cfg(unix)]
    {
        use std::os::unix::fs::OpenOptionsExt;
        let mut f = fs::OpenOptions::new()
            .write(true)
            .create(true)
            .truncate(true)
            .mode(0o600)
            .open(tmp)?;
        f.write_all(data)?;
        f.sync_all().ok();
        Ok(())
    }
    #[cfg(not(unix))]
    {
        let mut f = fs::File::create(tmp)?;
        f.write_all(data)?;
        f.sync_all().ok();
        Ok(())
    }
}

// ---------- DELETE tombstone (POST /swarm/lockbox/tombstones/{user}/{filename}) ----------

/// Handle a signed DELETE envelope from a peer.
///
/// Pipeline:
/// 1. Parse the body as `DeleteEnvelope`.
/// 2. Validate path components match the envelope (no mismatched
///    smuggling — e.g. URL says delete `foo.pdf` but envelope
///    body says delete `bar.pdf`).
/// 3. Find the sender in the local user's `peers.json`; look up
///    the sender's Ed25519 pubkey there. A sender unknown to
///    this envoy, OR known without a signing pubkey (pre-0.27.4
///    peer), is rejected with 403.
/// 4. Verify the signature against the known pubkey. Invalid → 403.
/// 5. Record tombstone locally.
/// 6. Remove the local ciphertext file if present. Idempotent —
///    already-missing files are fine.
///
/// Returns 200 on success, 400 on malformed input, 403 on auth
/// failure, 503 if the lockbox daemon isn't running here (can't
/// get to the peer table).
pub async fn handle_tombstone_delete(
    path: &str,
    req: Request<hyper::body::Incoming>,
    _peer_addr: SocketAddr,
) -> Response<Full<Bytes>> {
    let segments: Vec<&str> = path.trim_start_matches('/').split('/').collect();
    let (user, name) = match segments.as_slice() {
        ["swarm", "lockbox", "tombstones", user, name] => (*user, *name),
        _ => return not_found(),
    };
    let user = match percent_decode(user) {
        Some(s) => s,
        None => return bad_request("invalid user encoding"),
    };
    let name = match percent_decode(name) {
        Some(s) => s,
        None => return bad_request("invalid filename encoding"),
    };
    if !valid_username(&user) {
        return bad_request("invalid user");
    }
    if !valid_filename(&name) {
        return bad_request("invalid filename");
    }

    let collected = match req.into_body().collect().await {
        Ok(c) => c,
        Err(_) => return bad_request("read body"),
    };
    let body_bytes = collected.to_bytes();
    if body_bytes.len() > MAX_ANNOUNCE_BYTES {
        return payload_too_large();
    }

    let envelope: DeleteEnvelope = match serde_json::from_slice(&body_bytes) {
        Ok(e) => e,
        Err(e) => return bad_request(&format!("malformed envelope: {}", e)),
    };

    // URL path must match the envelope body — no smuggling.
    if envelope.user != user || envelope.filename != name {
        return bad_request("path / envelope mismatch");
    }

    // Resolve signing pubkey via the peer table. We trust the
    // pubkey recorded there (set by verified announcements), not
    // the one in the envelope body.
    let table = PeerTable::load(&user);
    let expected_pubkey = match table.sign_pubkey_for(&envelope.source_hostname) {
        Some(s) if !s.is_empty() => s.to_string(),
        _ => {
            tracing::warn!(
                user = %user,
                filename = %name,
                sender = %envelope.source_hostname,
                "lockbox: DELETE rejected — sender unknown to peer table or has no signing pubkey"
            );
            return forbidden("unknown sender or no signing pubkey");
        }
    };

    if let Err(e) = tombstones::verify_envelope(&envelope, &expected_pubkey) {
        tracing::warn!(
            user = %user,
            filename = %name,
            sender = %envelope.source_hostname,
            error = %e,
            "lockbox: DELETE rejected — signature verification failed"
        );
        return forbidden("signature verification failed");
    }

    // Record tombstone locally under the per-user lock so
    // simultaneous DELETE envelopes from different peers don't
    // race their load-modify-save pairs.
    if let Err(e) = TombstoneTable::modify(&user, |table| {
        table.upsert(
            &name,
            Tombstone {
                deletion_time: envelope.deletion_time,
                source_hostname: envelope.source_hostname.clone(),
            },
        )
    }) {
        tracing::warn!(error = %e, "lockbox DELETE: tombstone save failed");
    }

    // Remove the local file if still present. Idempotent.
    if let Some(home) = user_home(&user) {
        let content_path = home.join("lockbox").join(&name);
        if content_path.exists() {
            if let Err(e) = fs::remove_file(&content_path) {
                tracing::warn!(
                    path = %content_path.display(),
                    error = %e,
                    "lockbox DELETE: local file remove failed"
                );
            }
        }
    }

    Response::builder()
        .status(StatusCode::OK)
        .header("Content-Type", "application/json")
        .body(Full::new(Bytes::from(r#"{"status":"accepted"}"#)))
        .unwrap()
}

// ---------- tombstones list (GET /swarm/lockbox/tombstones/{user}) ----------

/// Return the full tombstone table for `user` as JSON. Used by
/// peers that observed a tombstones_digest mismatch on an
/// announcement and need to catch up. Does blocking I/O; caller
/// wraps in `block_in_place`.
pub fn handle_tombstones_list(path: &str) -> Response<Full<Bytes>> {
    let segments: Vec<&str> = path.trim_start_matches('/').split('/').collect();
    let user = match segments.as_slice() {
        ["swarm", "lockbox", "tombstones", user] => *user,
        _ => return not_found(),
    };
    let user = match percent_decode(user) {
        Some(s) => s,
        None => return bad_request("invalid user encoding"),
    };
    if !valid_username(&user) {
        return bad_request("invalid user");
    }

    let table = TombstoneTable::load(&user);
    match serde_json::to_vec(&table) {
        Ok(body) => Response::builder()
            .status(StatusCode::OK)
            .header("Content-Type", "application/json")
            .body(Full::new(Bytes::from(body)))
            .unwrap(),
        Err(_) => internal_error("serialize tombstone table"),
    }
}

// ---------- leaves list (GET /swarm/lockbox/leaves/{user}) ----------

/// Return the full leaves table for `user` as JSON. Mirror of
/// `handle_tombstones_list`. Used by peers that observed a
/// `leaves_digest` mismatch on an announcement and need to catch
/// up — typical case is an envoy that was offline during a LEAVE
/// rejoining the mesh and discovering the historical envelopes via
/// digest divergence rather than just observing the leaver vanish
/// from announcements. Does blocking I/O; caller wraps in
/// `block_in_place`.
pub fn handle_leaves_list(path: &str) -> Response<Full<Bytes>> {
    let segments: Vec<&str> = path.trim_start_matches('/').split('/').collect();
    let user = match segments.as_slice() {
        ["swarm", "lockbox", "leaves", user] => *user,
        _ => return not_found(),
    };
    let user = match percent_decode(user) {
        Some(s) => s,
        None => return bad_request("invalid user encoding"),
    };
    if !valid_username(&user) {
        return bad_request("invalid user");
    }

    let table = leaves::LeavesTable::load(&user);
    match serde_json::to_vec(&table) {
        Ok(body) => Response::builder()
            .status(StatusCode::OK)
            .header("Content-Type", "application/json")
            .body(Full::new(Bytes::from(body)))
            .unwrap(),
        Err(_) => internal_error("serialize leaves table"),
    }
}

// ---------- LEAVE (POST /swarm/lockbox/leaves/{user}) ----------

/// Handle a signed LEAVE envelope from a peer.
///
/// Pipeline mirrors `handle_tombstone_delete`:
/// 1. Parse body as `LeaveEnvelope`.
/// 2. Validate path user matches envelope user.
/// 3. Look up the signing pubkey registered for
///    `envelope.hostname_leaving` in the local peer table. Signer
///    MUST be the subject — we do NOT allow one peer to sign a
///    LEAVE for another (self-leave-only at the P2P layer).
/// 4. Verify signature; reject with 403 on failure.
/// 5. Call shared `apply_leave` to drop the pubkey from peer table,
///    regenerate recipients.txt, drop reencrypt.pending, persist to
///    leaves.json.
pub async fn handle_leave(
    path: &str,
    req: Request<hyper::body::Incoming>,
    _peer_addr: SocketAddr,
) -> Response<Full<Bytes>> {
    let segments: Vec<&str> = path.trim_start_matches('/').split('/').collect();
    let user = match segments.as_slice() {
        ["swarm", "lockbox", "leaves", user] => *user,
        _ => return not_found(),
    };
    let user = match percent_decode(user) {
        Some(s) => s,
        None => return bad_request("invalid user encoding"),
    };
    if !valid_username(&user) {
        return bad_request("invalid user");
    }

    let collected = match req.into_body().collect().await {
        Ok(c) => c,
        Err(_) => return bad_request("read body"),
    };
    let body_bytes = collected.to_bytes();
    if body_bytes.len() > MAX_ANNOUNCE_BYTES {
        return payload_too_large();
    }

    let envelope: LeaveEnvelope = match serde_json::from_slice(&body_bytes) {
        Ok(e) => e,
        Err(e) => return bad_request(&format!("malformed envelope: {}", e)),
    };

    if envelope.user != user {
        return bad_request("path / envelope user mismatch");
    }

    let table = PeerTable::load(&user);
    let expected_pubkey = match table.sign_pubkey_for(&envelope.hostname_leaving) {
        Some(s) if !s.is_empty() => s.to_string(),
        _ => {
            tracing::warn!(
                user = %user,
                hostname_leaving = %envelope.hostname_leaving,
                "lockbox: LEAVE rejected — subject unknown to peer table or has no signing pubkey"
            );
            return forbidden("unknown subject or no signing pubkey");
        }
    };

    if let Err(e) = leaves::verify_envelope(&envelope, &expected_pubkey) {
        tracing::warn!(
            user = %user,
            hostname_leaving = %envelope.hostname_leaving,
            error = %e,
            "lockbox: LEAVE rejected — signature verification failed"
        );
        return forbidden("signature verification failed");
    }

    // Apply locally via the shared helper. Needs the user's home
    // dir to regenerate recipients.txt. If we can't resolve the
    // home (rare: user removed between announce + LEAVE), still
    // persist the LEAVE record so future announcements respect it.
    if let Some(home) = user_home(&user) {
        if let Err(e) = super::apply_leave(
            &user,
            &envelope.age_pubkey_leaving,
            &envelope.hostname_leaving,
            envelope.leave_time,
            &home,
        ) {
            tracing::warn!(error = %e, "lockbox: apply_leave failed");
            return internal_error("apply_leave");
        }
    } else {
        // No home — persist to leaves.json at minimum.
        let mut table = super::leaves::LeavesTable::load(&user);
        let record = super::leaves::LeaveRecord {
            leave_time: envelope.leave_time,
            hostname_leaving: envelope.hostname_leaving.clone(),
        };
        if table.upsert(&envelope.age_pubkey_leaving, record) {
            if let Err(e) = table.save() {
                tracing::warn!(error = %e, "lockbox LEAVE: leaves.json save failed");
            }
        }
    }

    Response::builder()
        .status(StatusCode::OK)
        .header("Content-Type", "application/json")
        .body(Full::new(Bytes::from(r#"{"status":"accepted"}"#)))
        .unwrap()
}

// ---------- RESURRECT (POST /swarm/lockbox/resurrects/{user}/{filename}) ----------

/// Handle a signed RESURRECT envelope from a peer.
///
/// Mirrors `handle_tombstone_delete` in shape — same path validation,
/// same peer-table sign_pubkey lookup, same Ed25519 verify, same
/// per-user lock around the modify-save pair. On success, the local
/// `ResurrectsTable` is updated and the next push of this filename is
/// allowed by the gate (`effective_tombstone` returns None).
///
/// Returns 200 on success, 400 on malformed input, 403 on auth
/// failure (sender unknown to peer table or signature verification
/// failed).
pub async fn handle_resurrect(
    path: &str,
    req: Request<hyper::body::Incoming>,
    _peer_addr: SocketAddr,
) -> Response<Full<Bytes>> {
    let segments: Vec<&str> = path.trim_start_matches('/').split('/').collect();
    let (user, name) = match segments.as_slice() {
        ["swarm", "lockbox", "resurrects", user, name] => (*user, *name),
        _ => return not_found(),
    };
    let user = match percent_decode(user) {
        Some(s) => s,
        None => return bad_request("invalid user encoding"),
    };
    let name = match percent_decode(name) {
        Some(s) => s,
        None => return bad_request("invalid filename encoding"),
    };
    if !valid_username(&user) {
        return bad_request("invalid user");
    }
    if !valid_filename(&name) {
        return bad_request("invalid filename");
    }

    let collected = match req.into_body().collect().await {
        Ok(c) => c,
        Err(_) => return bad_request("read body"),
    };
    let body_bytes = collected.to_bytes();
    if body_bytes.len() > MAX_ANNOUNCE_BYTES {
        return payload_too_large();
    }

    let envelope: ResurrectEnvelope = match serde_json::from_slice(&body_bytes) {
        Ok(e) => e,
        Err(e) => return bad_request(&format!("malformed envelope: {}", e)),
    };

    // URL path must match the envelope body — no smuggling.
    if envelope.user != user || envelope.filename != name {
        return bad_request("path / envelope mismatch");
    }

    let table = PeerTable::load(&user);
    let expected_pubkey = match table.sign_pubkey_for(&envelope.source_hostname) {
        Some(s) if !s.is_empty() => s.to_string(),
        _ => {
            tracing::warn!(
                user = %user,
                filename = %name,
                sender = %envelope.source_hostname,
                "lockbox: RESURRECT rejected — sender unknown to peer table or has no signing pubkey"
            );
            return forbidden("unknown sender or no signing pubkey");
        }
    };

    if let Err(e) = resurrects::verify_envelope(&envelope, &expected_pubkey) {
        tracing::warn!(
            user = %user,
            filename = %name,
            sender = %envelope.source_hostname,
            error = %e,
            "lockbox: RESURRECT rejected — signature verification failed"
        );
        return forbidden("signature verification failed");
    }

    if let Err(e) = ResurrectsTable::modify(&user, |table| {
        table.upsert(
            &name,
            ResurrectRecord {
                resurrect_time: envelope.resurrect_time,
                source_hostname: envelope.source_hostname.clone(),
            },
        )
    }) {
        tracing::warn!(error = %e, "lockbox RESURRECT: resurrect save failed");
    }

    tracing::info!(
        user = %user,
        filename = %name,
        sender = %envelope.source_hostname,
        resurrect_time = envelope.resurrect_time,
        "lockbox: RESURRECT accepted"
    );

    Response::builder()
        .status(StatusCode::OK)
        .header("Content-Type", "application/json")
        .body(Full::new(Bytes::from(r#"{"status":"accepted"}"#)))
        .unwrap()
}

/// Return the full resurrects table for `user` as JSON. Mirror of
/// `handle_tombstones_list` — peers that observe a `resurrects_digest`
/// mismatch on an announcement pull the full list and merge.
pub fn handle_resurrects_list(path: &str) -> Response<Full<Bytes>> {
    let segments: Vec<&str> = path.trim_start_matches('/').split('/').collect();
    let user = match segments.as_slice() {
        ["swarm", "lockbox", "resurrects", user] => *user,
        _ => return not_found(),
    };
    let user = match percent_decode(user) {
        Some(s) => s,
        None => return bad_request("invalid user encoding"),
    };
    if !valid_username(&user) {
        return bad_request("invalid user");
    }

    let table = ResurrectsTable::load(&user);
    match serde_json::to_vec(&table) {
        Ok(body) => Response::builder()
            .status(StatusCode::OK)
            .header("Content-Type", "application/json")
            .body(Full::new(Bytes::from(body)))
            .unwrap(),
        Err(_) => internal_error("serialize resurrect table"),
    }
}

// ---------- announce (POST /swarm/lockbox/announce) ----------

/// Handle `POST /swarm/lockbox/announce` — feed a unicast announcement
/// into the lockbox daemon's reconciler channel. Returns 503 if the
/// daemon is not running (no TLS certs, not spawned yet).
pub async fn handle_announce(
    req: Request<hyper::body::Incoming>,
    peer_addr: SocketAddr,
) -> Response<Full<Bytes>> {
    let collected = match req.into_body().collect().await {
        Ok(c) => c,
        Err(_) => return bad_request("read body"),
    };
    let body_bytes = collected.to_bytes();
    if body_bytes.len() > MAX_ANNOUNCE_BYTES {
        return payload_too_large();
    }

    let announcement: super::state::Announcement = match serde_json::from_slice(&body_bytes) {
        Ok(a) => a,
        Err(e) => return bad_request(&format!("malformed announcement: {}", e)),
    };

    if super::submit_announcement(peer_addr, announcement) {
        Response::builder()
            .status(StatusCode::OK)
            .header("Content-Type", "application/json")
            .body(Full::new(Bytes::from(r#"{"status":"accepted"}"#)))
            .unwrap()
    } else {
        Response::builder()
            .status(StatusCode::SERVICE_UNAVAILABLE)
            .body(Full::new(Bytes::from(
                "lockbox daemon not running on this envoy",
            )))
            .unwrap()
    }
}

// ---------- helpers ----------

fn valid_username(name: &str) -> bool {
    if name.is_empty() || name.len() > 64 {
        return false;
    }
    name.chars()
        .all(|c| c.is_ascii_alphanumeric() || c == '-' || c == '_' || c == '.')
}

/// Filename validation. Rejects dotfiles, path-traversal, and
/// anything that isn't a single filesystem segment.
fn valid_filename(name: &str) -> bool {
    if name.is_empty() || name.len() > 255 {
        return false;
    }
    if name.starts_with('.') {
        return false;
    }
    !name.contains('/') && !name.contains('\\') && !name.contains('\0')
}

fn percent_decode(s: &str) -> Option<String> {
    let mut out = Vec::with_capacity(s.len());
    let bytes = s.as_bytes();
    let mut i = 0;
    while i < bytes.len() {
        if bytes[i] == b'%' {
            if i + 2 >= bytes.len() {
                return None;
            }
            let hi = hex_val(bytes[i + 1])?;
            let lo = hex_val(bytes[i + 2])?;
            out.push(hi * 16 + lo);
            i += 3;
        } else {
            out.push(bytes[i]);
            i += 1;
        }
    }
    String::from_utf8(out).ok()
}

fn hex_val(b: u8) -> Option<u8> {
    match b {
        b'0'..=b'9' => Some(b - b'0'),
        b'a'..=b'f' => Some(b - b'a' + 10),
        b'A'..=b'F' => Some(b - b'A' + 10),
        _ => None,
    }
}

fn not_found() -> Response<Full<Bytes>> {
    Response::builder()
        .status(StatusCode::NOT_FOUND)
        .body(Full::new(Bytes::from("not found")))
        .unwrap()
}

fn bad_request(msg: &str) -> Response<Full<Bytes>> {
    Response::builder()
        .status(StatusCode::BAD_REQUEST)
        .body(Full::new(Bytes::from(msg.to_string())))
        .unwrap()
}

fn payload_too_large() -> Response<Full<Bytes>> {
    Response::builder()
        .status(StatusCode::PAYLOAD_TOO_LARGE)
        .body(Full::new(Bytes::from("payload exceeds lockbox limit")))
        .unwrap()
}

fn insufficient_storage(msg: &str) -> Response<Full<Bytes>> {
    Response::builder()
        .status(StatusCode::INSUFFICIENT_STORAGE)
        .body(Full::new(Bytes::from(msg.to_string())))
        .unwrap()
}

fn internal_error(msg: &str) -> Response<Full<Bytes>> {
    Response::builder()
        .status(StatusCode::INTERNAL_SERVER_ERROR)
        .body(Full::new(Bytes::from(msg.to_string())))
        .unwrap()
}

fn forbidden(msg: &str) -> Response<Full<Bytes>> {
    Response::builder()
        .status(StatusCode::FORBIDDEN)
        .header("Content-Type", "application/json")
        .body(Full::new(Bytes::from(format!(
            r#"{{"status":"forbidden","reason":"{}"}}"#,
            msg.replace('"', "'")
        ))))
        .unwrap()
}

/// 410 Gone with a JSON body carrying the tombstone's deletion_time.
/// Pullers parse this response to learn about a delete they missed
/// (announcement-digest mismatch recovery) and record the tombstone
/// in their own store on the spot.
fn gone_with_tombstone(filename: &str, deletion_time: i64) -> Response<Full<Bytes>> {
    let body = format!(
        r#"{{"status":"tombstoned","filename":"{}","deletion_time":{}}}"#,
        filename.replace('"', "'"),
        deletion_time,
    );
    Response::builder()
        .status(StatusCode::GONE)
        .header("Content-Type", "application/json")
        .body(Full::new(Bytes::from(body)))
        .unwrap()
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn username_validation() {
        assert!(valid_username("dan"));
        assert!(valid_username("user_1"));
        assert!(valid_username("user.name"));
        assert!(!valid_username(""));
        assert!(!valid_username("bad/user"));
        assert!(!valid_username(&"a".repeat(65)));
    }

    #[test]
    fn filename_validation() {
        assert!(valid_filename("tax.pdf"));
        assert!(valid_filename("notes.md"));
        assert!(valid_filename("with-dash_and.dot"));
        assert!(!valid_filename(".hidden"));
        assert!(!valid_filename("../escape.txt"));
        assert!(!valid_filename("sub/dir.txt"));
        assert!(!valid_filename(""));
        assert!(!valid_filename(&"a".repeat(256)));
        assert!(!valid_filename("null\0inside.txt"));
    }

    #[test]
    fn decode_percent_encoding() {
        assert_eq!(percent_decode("dan").as_deref(), Some("dan"));
        assert_eq!(percent_decode("file%20name").as_deref(), Some("file name"));
        assert_eq!(percent_decode("bad%ZZ"), None);
        assert_eq!(percent_decode("trail%2"), None);
    }
}
