// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: Apache-2.0

//! Generic 8-digit claim-code primitives shared by `pair` (initial
//! join, ADR-014) and `rekey` (puddle keypair rotation, ADR-018).
//!
//! Both flows follow the same pattern:
//!
//! 1. The active envoy generates a single-use 8-character code with a
//!    5-minute TTL.
//! 2. The code's SHA-256 hash + issuance/expiry timestamps are written
//!    to a sentinel file under `~/.vigo-puddle/`. The plaintext code is
//!    only ever in the operator's head and on the receiving envoy's
//!    stdin.
//! 3. The receiving envoy POSTs the code to a swarm-mTLS endpoint on
//!    the issuing envoy.
//! 4. The handler validates code + freshness + single-use, serves a
//!    response packing `(salt, wrapped_blob)`, and clears the sentinel.
//!
//! Only the sentinel filename and the HTTP route differ between
//! flows. Everything else lives here.

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

/// 5-minute TTL for an issued claim code. Matches the original ADR-014
/// pair window; ADR-018 reuses the same window deliberately so the
/// operator's mental model is uniform.
pub const CLAIM_TTL_SECS: i64 = 5 * 60;

/// Maximum claim-request body size. The body carries the 8-character
/// code as JSON; cap is generous to allow forward-compat fields
/// without rejecting otherwise-valid claims.
pub const MAX_CLAIM_BODY: usize = 4 * 1024;

/// Persisted claim state. Lives at a flow-specific sentinel path
/// (`pair.pending` or `rekey.pending`) while a code is active.
#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct PendingSpec {
    /// SHA-256 of the canonicalized code, hex-encoded. The plaintext
    /// code is never written to disk.
    pub code_hash: String,
    /// Unix seconds when the code was issued.
    pub issued_at: i64,
    /// Unix seconds when the code stops being valid
    /// (`issued_at + CLAIM_TTL_SECS`).
    pub expires_at: i64,
}

/// JSON body of a claim request — both `pair-claim` and
/// `rekey-claim` route through this shape.
#[derive(Debug, Serialize, Deserialize)]
pub struct ClaimRequest {
    /// The claim code. Dashes / whitespace tolerated; canonicalized
    /// before hashing.
    pub code: String,
}

/// Generate a fresh 8-digit claim code in `NNNN-NNNN` form. Source
/// bytes come from the OS CSPRNG via `rand::thread_rng()`. 10^8 = 100M
/// space; a session is deleted after [`MAX_CLAIM_FAILURES`] wrong codes,
/// so a guesser gets that many tries per session, not the TTL's worth.
pub fn generate_code() -> String {
    let mut buf = [0u8; 4];
    rand::thread_rng().fill_bytes(&mut buf);
    let n = u32::from_be_bytes(buf) % 100_000_000;
    format!("{:04}-{:04}", n / 10_000, n % 10_000)
}

/// Hash `code` for storage. Strips dashes + whitespace and uppercases
/// so users can type the code in any reasonable shape (`4827-9163`,
/// `48279163`, `4827 9163`, etc.).
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

/// Wrong codes tolerated per pending session before it is deleted. Without
/// a bound any fleet envoy could walk the 10^8 code space during the TTL;
/// with it, a session survives at most this many guesses.
pub const MAX_CLAIM_FAILURES: u32 = 5;

/// Per-session wrong-code counters, keyed by the pending session's stored
/// code hash (a fresh `pair`/`rekey` mints a fresh code, so a fresh key).
/// In memory on purpose: the pending file lives in the user's home and a
/// counter there would be the guesser's to reset.
static CLAIM_FAILURES: std::sync::Mutex<Option<std::collections::HashMap<String, u32>>> =
    std::sync::Mutex::new(None);

/// Record one wrong code against the session whose stored hash is
/// `session_hash`. Returns true when the session has now used up its
/// [`MAX_CLAIM_FAILURES`] and the caller must delete the pending file.
pub fn record_claim_failure(session_hash: &str) -> bool {
    let mut g = CLAIM_FAILURES.lock().unwrap_or_else(|p| p.into_inner());
    let map = g.get_or_insert_with(Default::default);
    if map.len() > 1024 {
        map.clear();
    }
    let n = map.entry(session_hash.to_string()).or_insert(0);
    *n += 1;
    if *n >= MAX_CLAIM_FAILURES {
        map.remove(session_hash);
        true
    } else {
        false
    }
}

/// Forget a session's counter (on success or expiry).
pub fn clear_claim_failures(session_hash: &str) {
    let mut g = CLAIM_FAILURES.lock().unwrap_or_else(|p| p.into_inner());
    if let Some(map) = g.as_mut() {
        map.remove(session_hash);
    }
}

/// Constant-time hash comparison. Habit dictates the right primitive
/// even though the hashes are short and the secret is too.
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

/// Drop a flow-specific sentinel file at `path`. Mode 0600, chowned
/// to `username` so a peer process on this envoy can't read it.
pub fn write_pending(path: &Path, code: &str, username: &str) -> Result<()> {
    let now = now_secs();
    let spec = PendingSpec {
        code_hash: hash_code(code),
        issued_at: now,
        expires_at: now + CLAIM_TTL_SECS,
    };
    if let Some(parent) = path.parent() {
        fs::create_dir_all(parent)
            .with_context(|| format!("creating {}", parent.display()))?;
    }
    let json = serde_json::to_string_pretty(&spec).context("serializing claim spec")?;

    #[cfg(unix)]
    {
        use std::os::unix::fs::OpenOptionsExt;
        let mut f = fs::OpenOptions::new()
            .write(true)
            .create(true)
            .truncate(true)
            .mode(0o600)
            .open(path)
            .with_context(|| format!("creating {}", path.display()))?;
        f.write_all(json.as_bytes())?;
        f.sync_all().ok();
    }
    #[cfg(not(unix))]
    {
        fs::write(path, json.as_bytes())
            .with_context(|| format!("creating {}", path.display()))?;
    }
    #[cfg(unix)]
    if let Err(e) = crate::swarm::lockbox::chown_to_user_pub(path, username) {
        tracing::debug!(path = %path.display(), error = %e, "puddle claim: chown failed");
    }
    #[cfg(not(unix))]
    let _ = username;
    Ok(())
}

/// Read the sentinel; returns `None` if absent. Caller is responsible
/// for checking `expires_at` before treating the spec as live.
pub fn read_pending(path: &Path) -> Result<Option<PendingSpec>> {
    let raw = match fs::read_to_string(path) {
        Ok(s) => s,
        Err(e) if e.kind() == std::io::ErrorKind::NotFound => return Ok(None),
        Err(e) => return Err(e).context(format!("reading {}", path.display())),
    };
    let spec: PendingSpec =
        serde_json::from_str(&raw).context("parsing claim sentinel")?;
    Ok(Some(spec))
}

/// Best-effort sentinel removal. Errors other than NotFound are
/// logged at debug level and swallowed — at worst a stale sentinel
/// sits until the user retries.
pub fn clear_pending(path: &Path) {
    match fs::remove_file(path) {
        Ok(()) => {}
        Err(e) if e.kind() == std::io::ErrorKind::NotFound => {}
        Err(e) => {
            tracing::debug!(
                path = %path.display(),
                error = %e,
                "puddle claim: failed to clear pending sentinel"
            );
        }
    }
}

/// Strip a known route prefix and verify the remainder is a single
/// path component (no further `/`). Used by both pair-claim and
/// rekey-claim path parsers.
pub fn parse_claim_path(path: &str, prefix: &str) -> Option<String> {
    let rest = path.strip_prefix(prefix)?;
    if rest.contains('/') || rest.is_empty() {
        return None;
    }
    Some(rest.to_string())
}

/// Read the request body up to `cap` bytes, stopping the read at the
/// cap (see [`crate::swarm::server::read_body_capped`]). Bails on overflow.
pub async fn read_body_capped(
    req: Request<hyper::body::Incoming>,
    cap: usize,
) -> Result<Vec<u8>> {
    use crate::swarm::server::BodyReadError;
    match crate::swarm::server::read_body_capped(req.into_body(), cap as u64).await {
        Ok(b) => Ok(b),
        Err(BodyReadError::TooLarge) => bail!("body exceeds {}-byte cap", cap),
        Err(BodyReadError::Read(e)) => bail!("reading body: {}", e),
    }
}

/// Render a plain-text response.
pub fn plain(status: StatusCode, msg: &str) -> Response<Full<Bytes>> {
    Response::builder()
        .status(status)
        .header("Content-Type", "text/plain; charset=utf-8")
        .body(Full::new(Bytes::from(format!("{}\n", msg))))
        .unwrap()
}

/// Pack a `(salt, wrapped)` response body. Wire format:
/// `[1 byte salt-len][salt bytes][wrapped blob]`. Used by both
/// pair-claim and rekey-claim handlers.
pub fn pack_response(salt: &[u8], wrapped: &[u8]) -> Result<Vec<u8>> {
    if salt.len() > 255 {
        bail!("salt length {} exceeds 255-byte wire field", salt.len());
    }
    let mut body = Vec::with_capacity(1 + salt.len() + wrapped.len());
    body.push(salt.len() as u8);
    body.extend_from_slice(salt);
    body.extend_from_slice(wrapped);
    Ok(body)
}

/// Parse the body returned by a claim handler into `(salt, wrapped)`.
pub fn parse_response(body: &[u8]) -> Result<(Vec<u8>, Vec<u8>)> {
    if body.is_empty() {
        bail!("empty claim response");
    }
    let salt_len = body[0] as usize;
    if body.len() < 1 + salt_len {
        bail!("claim response truncated (salt incomplete)");
    }
    let salt = body[1..1 + salt_len].to_vec();
    let wrapped = body[1 + salt_len..].to_vec();
    if wrapped.is_empty() {
        bail!("claim response missing wrapped blob");
    }
    Ok((salt, wrapped))
}

/// Unix seconds since epoch.
pub fn now_secs() -> i64 {
    std::time::SystemTime::now()
        .duration_since(std::time::UNIX_EPOCH)
        .map(|d| d.as_secs() as i64)
        .unwrap_or(0)
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn code_shape() {
        for _ in 0..100 {
            let code = generate_code();
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
    fn code_changes_each_call() {
        let mut codes = std::collections::HashSet::new();
        for _ in 0..100 {
            codes.insert(generate_code());
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
        let a = hash_code("abcd-1234");
        let b = hash_code("ABCD-1234");
        assert_eq!(a, b);
    }

    #[test]
    fn hash_code_distinguishes_distinct_codes() {
        assert_ne!(hash_code("4827-9163"), hash_code("4827-9164"));
    }

    /// A pending session dies after MAX_CLAIM_FAILURES wrong codes, so the
    /// code space cannot be walked during the TTL.
    #[test]
    fn claim_failures_cancel_session_at_the_bound() {
        let key = format!("test-session-{}", rand::random::<u64>());
        for _ in 1..MAX_CLAIM_FAILURES {
            assert!(!record_claim_failure(&key));
        }
        assert!(record_claim_failure(&key), "the bound-th failure cancels");
        // Counter reset afterwards (a new session gets a full budget).
        assert!(!record_claim_failure(&key));
        clear_claim_failures(&key);
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
    fn parse_claim_path_rejects_extras() {
        let prefix = "/swarm/puddle/pair-claim/";
        assert_eq!(
            parse_claim_path("/swarm/puddle/pair-claim/dan", prefix),
            Some("dan".to_string())
        );
        assert!(parse_claim_path("/swarm/puddle/pair-claim/", prefix).is_none());
        assert!(parse_claim_path("/swarm/puddle/pair-claim/dan/x", prefix).is_none());
        assert!(parse_claim_path("/other/path", prefix).is_none());
    }

    #[test]
    fn pack_then_parse_response_roundtrips() {
        let salt = vec![0xaa; 16];
        let wrapped = vec![0xbb; 64];
        let body = pack_response(&salt, &wrapped).unwrap();
        let (s, w) = parse_response(&body).unwrap();
        assert_eq!(s, salt);
        assert_eq!(w, wrapped);
    }

    #[test]
    fn parse_response_rejects_truncated() {
        assert!(parse_response(&[]).is_err());
        assert!(parse_response(&[16, 0, 0]).is_err());
    }

    #[test]
    fn pack_response_rejects_oversized_salt() {
        let salt = vec![0u8; 256];
        let wrapped = vec![0u8; 4];
        assert!(pack_response(&salt, &wrapped).is_err());
    }

    #[test]
    fn write_then_read_pending_roundtrip() {
        let dir = tempfile::tempdir().unwrap();
        let path = dir.path().join("pair.pending");
        let username = std::env::var("USER").unwrap_or_else(|_| "nobody".into());
        write_pending(&path, "4827-9163", &username).unwrap();
        let spec = read_pending(&path).unwrap().expect("sentinel present");
        assert_eq!(spec.code_hash, hash_code("4827-9163"));
        assert!(spec.expires_at > spec.issued_at);
        assert_eq!(spec.expires_at - spec.issued_at, CLAIM_TTL_SECS);
        clear_pending(&path);
        assert!(read_pending(&path).unwrap().is_none());
    }

    #[test]
    fn clear_pending_is_idempotent_on_missing() {
        let dir = tempfile::tempdir().unwrap();
        clear_pending(&dir.path().join("nonexistent.pending"));
        // No panic, no error surface — best-effort behavior verified.
    }
}
