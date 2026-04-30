// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

//! Peer-pair primitives for self-fleet onboarding (ADR-014).
//!
//! The active envoy (already in the fleet) issues an 8-character
//! single-use pairing code with a 5-minute TTL. The new envoy
//! presents the code over swarm mTLS to a `pair-claim` HTTP route;
//! on match, the active envoy serves the wrapped identity blob.
//! The destination envoy unwraps locally with the user's passphrase.
//!
//! Three concerns live here:
//!
//! 1. **Code generation** (`generate_pair_code`) — 8 ASCII digits in
//!    `NNNN-NNNN` form, sourced from the OS CSPRNG. Easy to read
//!    and dictate over a phone call.
//! 2. **Sentinel I/O** (`write_pending`, `read_pending`,
//!    `clear_pending`) — the user-context CLI drops `pair.pending`
//!    under `~/.vigo-fleet/`; the daemon-context HTTP handler reads
//!    it, validates the incoming code, serves the wrapped blob, and
//!    clears the sentinel.
//! 3. **HTTP claim handler** (`handle_pair_claim`) — invoked by the
//!    swarm peer server (`agent/src/swarm/server.rs`) when a peer
//!    POSTs `/swarm/self-fleet/pair-claim/<user>`.
//!
//! On-disk sentinel format is JSON. Pairing code is stored as a
//! SHA-256 hash, never plaintext, so a stolen sentinel can't be
//! played back.

use std::fs;
use std::io::Write;
use std::path::Path;

use anyhow::{bail, Context, Result};
use http_body_util::Full;
use hyper::body::Bytes;
use hyper::{Request, Response, StatusCode};
use rand::RngCore;
use serde::{Deserialize, Serialize};
use sha2::{Digest, Sha256};

use super::config;

/// 5-minute TTL for an issued pairing code. ADR-014 fixes this.
pub const PAIR_TTL_SECS: i64 = 5 * 60;

/// Maximum claim-request body size. The body carries the 8-char
/// pairing code as JSON; cap is generous to allow forward-compat
/// fields without rejecting otherwise-valid claims.
const MAX_CLAIM_BODY: usize = 4 * 1024;

/// Persisted pairing state. Lives at `~/.vigo-fleet/pair.pending`
/// while a code is active; removed after one successful claim or
/// at TTL expiry.
#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct PairPendingSpec {
    /// SHA-256 of the pairing code (raw 32 bytes, hex-encoded).
    /// Stored as a hash so a leaked sentinel doesn't reveal the
    /// active code.
    pub code_hash: String,
    /// When the code was issued (unix seconds).
    pub issued_at: i64,
    /// When the code stops being valid (`issued_at + PAIR_TTL_SECS`).
    pub expires_at: i64,
}

/// JSON body of a pair-claim request.
#[derive(Debug, Serialize, Deserialize)]
struct ClaimRequest {
    /// The pairing code, ASCII, dashes optional. Compared against
    /// the stored hash after canonicalizing (strip dashes, uppercase).
    code: String,
}

/// Generate a fresh 8-digit pairing code in `NNNN-NNNN` form.
/// Source bytes come from the OS CSPRNG via `rand::thread_rng()`.
/// 10^8 = 100M space; combined with the 5-minute TTL and single-use
/// semantics, brute force across the wire is infeasible (10M
/// requests in 5 min would saturate the receiver well before
/// covering a reasonable fraction of the keyspace, and each request
/// rides mTLS so every probe is from a fleet-CA-trusted peer).
pub fn generate_pair_code() -> String {
    let mut buf = [0u8; 4];
    rand::thread_rng().fill_bytes(&mut buf);
    let n = u32::from_be_bytes(buf) % 100_000_000;
    format!("{:04}-{:04}", n / 10_000, n % 10_000)
}

/// Hash `code` for storage. Strips dashes + uppercases so users
/// can type the code in any reasonable shape (`4827-9163`,
/// `48279163`, `4827 9163`).
pub fn hash_code(code: &str) -> String {
    let canonical: String = code
        .chars()
        .filter(|c| !c.is_whitespace() && *c != '-')
        .map(|c| c.to_ascii_uppercase())
        .collect();
    let mut hasher = Sha256::new();
    hasher.update(canonical.as_bytes());
    hex::encode(hasher.finalize())
}

/// Constant-time hash comparison. Avoids a side channel even though
/// the secret here is short — habit dictates the right primitive.
pub fn hashes_equal(a: &str, b: &str) -> bool {
    if a.len() != b.len() {
        return false;
    }
    let mut diff = 0u8;
    for (x, y) in a.bytes().zip(b.bytes()) {
        diff |= x ^ y;
    }
    diff == 0
}

/// Drop the pair.pending sentinel. Called by `vigo swarm self-fleet
/// pair` after generating the code. Mode 0600 user-owned so a peer
/// process on this envoy can't read it.
pub fn write_pending(home: &Path, code: &str, username: &str) -> Result<()> {
    let now = now_secs();
    let spec = PairPendingSpec {
        code_hash: hash_code(code),
        issued_at: now,
        expires_at: now + PAIR_TTL_SECS,
    };
    let path = config::pair_pending_path(home);
    if let Some(parent) = path.parent() {
        fs::create_dir_all(parent)
            .with_context(|| format!("creating {}", parent.display()))?;
    }
    let json = serde_json::to_string_pretty(&spec).context("serializing pair spec")?;

    #[cfg(unix)]
    {
        use std::os::unix::fs::OpenOptionsExt;
        let mut f = fs::OpenOptions::new()
            .write(true)
            .create(true)
            .truncate(true)
            .mode(0o600)
            .open(&path)
            .with_context(|| format!("creating {}", path.display()))?;
        f.write_all(json.as_bytes())?;
        f.sync_all().ok();
    }
    #[cfg(not(unix))]
    {
        fs::write(&path, json.as_bytes())
            .with_context(|| format!("creating {}", path.display()))?;
    }
    #[cfg(unix)]
    if let Err(e) = crate::swarm::cryptsync::chown_to_user_pub(&path, username) {
        tracing::debug!(path = %path.display(), error = %e, "self-fleet pair: chown failed");
    }
    #[cfg(not(unix))]
    let _ = username;
    Ok(())
}

/// Read the sentinel; returns `None` if absent. Caller checks
/// `expires_at` before treating the spec as live.
pub fn read_pending(home: &Path) -> Result<Option<PairPendingSpec>> {
    let path = config::pair_pending_path(home);
    let raw = match fs::read_to_string(&path) {
        Ok(s) => s,
        Err(e) if e.kind() == std::io::ErrorKind::NotFound => return Ok(None),
        Err(e) => return Err(e).context(format!("reading {}", path.display())),
    };
    let spec: PairPendingSpec =
        serde_json::from_str(&raw).context("parsing pair.pending")?;
    Ok(Some(spec))
}

/// Best-effort sentinel removal. The CLI calls it on TTL expiry,
/// the HTTP handler calls it after one successful claim. Errors
/// other than NotFound are logged at debug level and swallowed —
/// at worst a stale sentinel sits until the user retries `pair`,
/// which overwrites it via `write_pending`.
pub fn clear_pending(home: &Path) {
    let path = config::pair_pending_path(home);
    match fs::remove_file(&path) {
        Ok(()) => {}
        Err(e) if e.kind() == std::io::ErrorKind::NotFound => {}
        Err(e) => {
            tracing::debug!(
                path = %path.display(),
                error = %e,
                "self-fleet pair: failed to clear pending sentinel"
            );
        }
    }
}

/// HTTP handler for `POST /swarm/self-fleet/pair-claim/<user>`.
/// Wired from the daemon's swarm peer server. Runs in a root context
/// (the daemon user); reads the target user's `~/.vigo-fleet/`
/// state, validates the incoming code against the stored hash, and
/// serves the wrapped identity blob on match.
///
/// Failure modes:
/// - 404: user has no pair.pending sentinel (no active session).
/// - 410: sentinel exists but has expired.
/// - 401: code mismatch.
/// - 500: any read or filesystem issue.
///
/// On success: 200 with `Content-Type: application/octet-stream`
/// and the raw wrapped blob (`[1 byte version][24 byte
/// nonce][ciphertext+tag]`). The sentinel is removed before
/// returning so the code is single-use even if the response is
/// retried.
pub async fn handle_pair_claim(
    path: &str,
    req: Request<hyper::body::Incoming>,
) -> Result<Response<Full<Bytes>>> {
    let user = match parse_pair_claim_path(path) {
        Some(u) => u,
        None => return Ok(plain(StatusCode::BAD_REQUEST, "malformed path")),
    };

    let home = match resolve_user_home(&user) {
        Some(h) => h,
        None => return Ok(plain(StatusCode::NOT_FOUND, "no such user")),
    };

    // Read the request body (capped). The body shape is small JSON.
    let body_bytes = match read_body_capped(req, MAX_CLAIM_BODY).await {
        Ok(b) => b,
        Err(e) => return Ok(plain(StatusCode::BAD_REQUEST, &format!("body: {}", e))),
    };
    let claim: ClaimRequest = match serde_json::from_slice(&body_bytes) {
        Ok(c) => c,
        Err(_) => {
            return Ok(plain(
                StatusCode::BAD_REQUEST,
                "malformed claim body (expected JSON {\"code\":\"NNNN-NNNN\"})",
            ))
        }
    };

    let spec = match read_pending(&home) {
        Ok(Some(s)) => s,
        Ok(None) => return Ok(plain(StatusCode::NOT_FOUND, "no active pair session")),
        Err(e) => {
            tracing::warn!(error = %e, "self-fleet pair: cannot read pending");
            return Ok(plain(StatusCode::INTERNAL_SERVER_ERROR, "pair state read failed"));
        }
    };

    let now = now_secs();
    if now >= spec.expires_at {
        // Stale code — clear and reject. The active session is over.
        clear_pending(&home);
        return Ok(plain(StatusCode::GONE, "pair session expired"));
    }

    let incoming_hash = hash_code(&claim.code);
    if !hashes_equal(&incoming_hash, &spec.code_hash) {
        // Don't clear on mismatch — let the user retry within the TTL.
        // The TTL itself bounds retries.
        return Ok(plain(StatusCode::UNAUTHORIZED, "code mismatch"));
    }

    // Valid + alive + matching. Read the wrapped blob and serve it.
    // Clear the sentinel BEFORE serving so the code is single-use
    // even if the response is interrupted and the peer retries.
    let wrapped_path = config::wrapped_path(&home);
    let wrapped = match fs::read(&wrapped_path) {
        Ok(b) => b,
        Err(e) => {
            tracing::warn!(
                path = %wrapped_path.display(),
                error = %e,
                "self-fleet pair: cannot read wrapped blob"
            );
            return Ok(plain(StatusCode::INTERNAL_SERVER_ERROR, "wrapped read failed"));
        }
    };
    let salt_path = config::salt_path(&home);
    let salt = match fs::read(&salt_path) {
        Ok(b) => b,
        Err(e) => {
            tracing::warn!(
                path = %salt_path.display(),
                error = %e,
                "self-fleet pair: cannot read salt"
            );
            return Ok(plain(StatusCode::INTERNAL_SERVER_ERROR, "salt read failed"));
        }
    };

    clear_pending(&home);

    // Wire format: 1 byte salt-len + salt bytes + wrapped blob.
    // Receiver splits, reuses cryptsync's unwrap shape.
    let mut body = Vec::with_capacity(1 + salt.len() + wrapped.len());
    if salt.len() > 255 {
        return Ok(plain(
            StatusCode::INTERNAL_SERVER_ERROR,
            "unexpected salt length",
        ));
    }
    body.push(salt.len() as u8);
    body.extend_from_slice(&salt);
    body.extend_from_slice(&wrapped);

    tracing::info!(
        user = %user,
        "self-fleet pair: served wrapped identity to claimant"
    );
    Ok(Response::builder()
        .status(StatusCode::OK)
        .header("Content-Type", "application/octet-stream")
        .body(Full::new(Bytes::from(body)))
        .unwrap())
}

fn parse_pair_claim_path(path: &str) -> Option<String> {
    let prefix = "/swarm/self-fleet/pair-claim/";
    let rest = path.strip_prefix(prefix)?;
    // Username component only — no further path segments.
    if rest.contains('/') || rest.is_empty() {
        return None;
    }
    Some(rest.to_string())
}

fn resolve_user_home(username: &str) -> Option<std::path::PathBuf> {
    crate::swarm::cryptsync::home_for_user_pub(username)
}

async fn read_body_capped(
    req: Request<hyper::body::Incoming>,
    cap: usize,
) -> Result<Vec<u8>> {
    use http_body_util::BodyExt;
    let collected = req.into_body().collect().await.context("reading body")?;
    let bytes = collected.to_bytes();
    if bytes.len() > cap {
        bail!("body exceeds {}-byte cap", cap);
    }
    Ok(bytes.to_vec())
}

fn plain(status: StatusCode, msg: &str) -> Response<Full<Bytes>> {
    Response::builder()
        .status(status)
        .header("Content-Type", "text/plain; charset=utf-8")
        .body(Full::new(Bytes::from(format!("{}\n", msg))))
        .unwrap()
}

fn now_secs() -> i64 {
    std::time::SystemTime::now()
        .duration_since(std::time::UNIX_EPOCH)
        .map(|d| d.as_secs() as i64)
        .unwrap_or(0)
}

/// Parse the body returned by `pair-claim` into `(salt, wrapped)`.
/// Used by the `join` CLI verb on the receiving side.
pub fn parse_pair_response(body: &[u8]) -> Result<(Vec<u8>, Vec<u8>)> {
    if body.is_empty() {
        bail!("empty pair-claim response");
    }
    let salt_len = body[0] as usize;
    if body.len() < 1 + salt_len {
        bail!("pair-claim response truncated (salt incomplete)");
    }
    let salt = body[1..1 + salt_len].to_vec();
    let wrapped = body[1 + salt_len..].to_vec();
    if wrapped.is_empty() {
        bail!("pair-claim response missing wrapped blob");
    }
    Ok((salt, wrapped))
}

/// Marker for the swarm server's path matcher. Kept here next to the
/// path parser so changes ride together.
pub const PAIR_CLAIM_PREFIX: &str = "/swarm/self-fleet/pair-claim/";

/// Helper for the `read_body_capped` reader's signature — exposed
/// for sibling code that needs the same primitive without pulling in
/// hyper imports.
pub fn _unused_marker() {}
// (No re-exported state today — the handler is invoked directly.)

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn pair_code_shape() {
        for _ in 0..100 {
            let code = generate_pair_code();
            assert_eq!(code.len(), 9, "shape NNNN-NNNN is 9 chars");
            assert_eq!(code.chars().nth(4), Some('-'));
            for (i, c) in code.chars().enumerate() {
                if i == 4 {
                    continue;
                }
                assert!(c.is_ascii_digit(), "non-digit at {}: {}", i, c);
            }
        }
    }

    #[test]
    fn pair_code_changes_each_call() {
        // 100 codes × 100M space ≈ 1 in 1M chance of any collision;
        // the test would have to be unlucky enough to fail before a
        // real bug surfaced. Cheap + catches a stuck-CSPRNG regression.
        let mut codes = std::collections::HashSet::new();
        for _ in 0..100 {
            codes.insert(generate_pair_code());
        }
        assert!(codes.len() > 95, "got {} unique codes from 100", codes.len());
    }

    #[test]
    fn hash_code_normalizes_input() {
        let a = hash_code("4827-9163");
        let b = hash_code("48279163");
        let c = hash_code("4827 9163");
        let d = hash_code("48 27 91 63");
        assert_eq!(a, b);
        assert_eq!(a, c);
        assert_eq!(a, d);
    }

    #[test]
    fn hash_code_is_case_insensitive_for_alpha() {
        // Today's codes are pure digits, but the canonicalization
        // also uppercases letters so a future code grammar stays
        // forgiving.
        let a = hash_code("abcd-1234");
        let b = hash_code("ABCD-1234");
        assert_eq!(a, b);
    }

    #[test]
    fn hash_code_distinguishes_distinct_codes() {
        assert_ne!(hash_code("4827-9163"), hash_code("4827-9164"));
    }

    #[test]
    fn hashes_equal_constant_time_basic() {
        assert!(hashes_equal("abc", "abc"));
        assert!(!hashes_equal("abc", "abd"));
        assert!(!hashes_equal("abc", "abcd"));
        assert!(!hashes_equal("", "x"));
        assert!(hashes_equal("", ""));
    }

    #[test]
    fn parse_pair_claim_path_rejects_extras() {
        assert_eq!(
            parse_pair_claim_path("/swarm/self-fleet/pair-claim/dan"),
            Some("dan".to_string())
        );
        assert!(parse_pair_claim_path("/swarm/self-fleet/pair-claim/").is_none());
        assert!(parse_pair_claim_path("/swarm/self-fleet/pair-claim/dan/x").is_none());
        assert!(parse_pair_claim_path("/other/path").is_none());
    }

    #[test]
    fn parse_pair_response_roundtrip() {
        let salt = vec![0xaa; 16];
        let wrapped = vec![0xbb; 64];
        let mut body = Vec::new();
        body.push(salt.len() as u8);
        body.extend_from_slice(&salt);
        body.extend_from_slice(&wrapped);
        let (s, w) = parse_pair_response(&body).unwrap();
        assert_eq!(s, salt);
        assert_eq!(w, wrapped);
    }

    #[test]
    fn parse_pair_response_rejects_truncated() {
        assert!(parse_pair_response(&[]).is_err());
        assert!(parse_pair_response(&[16, 0, 0]).is_err());
    }

    #[test]
    fn write_then_read_pending_roundtrip() {
        let dir = tempfile::tempdir().unwrap();
        let home = dir.path();
        // Write under the invoking user; chown is a no-op for self.
        let username = std::env::var("USER").unwrap_or_else(|_| "nobody".into());
        write_pending(home, "4827-9163", &username).unwrap();
        let spec = read_pending(home).unwrap().expect("sentinel present");
        assert_eq!(spec.code_hash, hash_code("4827-9163"));
        assert!(spec.expires_at > spec.issued_at);
        assert_eq!(spec.expires_at - spec.issued_at, PAIR_TTL_SECS);
        clear_pending(home);
        assert!(read_pending(home).unwrap().is_none());
    }
}
