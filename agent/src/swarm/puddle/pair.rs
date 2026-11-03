// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: Apache-2.0

//! Peer-pair onboarding for puddle (ADR-014).
//!
//! The active envoy issues an 8-character single-use pairing code
//! with a 5-minute TTL. The new envoy presents the code over swarm
//! mTLS to a `pair-claim` HTTP route; on match, the active envoy
//! serves the wrapped identity blob. The destination envoy unwraps
//! locally with the user's passphrase.
//!
//! The generic claim-code mechanics (code generation, sentinel I/O,
//! HTTP response packing, request parsing) live in
//! [`super::codes`] and are shared with the `rekey` flow under
//! ADR-018. This module owns only the pair-specific filename
//! (`pair.pending`), the pair-specific route prefix
//! (`PAIR_CLAIM_PREFIX`), and the pair-claim HTTP handler.

use std::fs;

use anyhow::{Context, Result};
use http_body_util::Full;
use hyper::body::Bytes;
use hyper::{Request, Response, StatusCode};

use super::codes::{
    self, parse_claim_path, ClaimRequest, MAX_CLAIM_BODY,
};
use super::config;

// ---------- pair-specific surface ----------

/// 5-minute TTL re-export — `cli.rs` formats this in operator output.
pub use super::codes::CLAIM_TTL_SECS as PAIR_TTL_SECS;
pub use super::codes::PendingSpec as PairPendingSpec;

/// HTTP route prefix this handler answers on. Used by
/// `agent/src/swarm/server.rs` to dispatch claim requests.
pub const PAIR_CLAIM_PREFIX: &str = "/swarm/puddle/pair-claim/";

/// Generate a fresh 8-digit pairing code in `NNNN-NNNN` form.
pub fn generate_pair_code() -> String {
    codes::generate_code()
}

/// Hash a pairing code for storage / comparison.
pub fn hash_code(code: &str) -> String {
    codes::hash_code(code)
}

/// Constant-time hash comparison.
pub fn hashes_equal(a: &str, b: &str) -> bool {
    codes::hashes_equal(a, b)
}

/// Drop the pair.pending sentinel under `~/.vigo-puddle/`. Called by
/// `vigo swarm puddle pair` after generating the code.
pub fn write_pending(home: &std::path::Path, code: &str, username: &str) -> Result<()> {
    codes::write_pending(&config::pair_pending_path(home), code, username)
}

/// Read the pair.pending sentinel; returns `None` if absent.
pub fn read_pending(home: &std::path::Path) -> Result<Option<PairPendingSpec>> {
    codes::read_pending(&config::pair_pending_path(home))
}

/// Best-effort sentinel removal.
pub fn clear_pending(home: &std::path::Path) {
    codes::clear_pending(&config::pair_pending_path(home));
}

/// Parse the body returned by `pair-claim` into `(salt, wrapped)`.
/// Used by the `join` CLI verb on the receiving side.
pub fn parse_pair_response(body: &[u8]) -> Result<(Vec<u8>, Vec<u8>)> {
    codes::parse_response(body)
}

// ---------- HTTP handler ----------

/// HTTP handler for `POST /swarm/puddle/pair-claim/<user>`.
/// Wired from the daemon's swarm peer server. Runs in a root context
/// (the daemon user); reads the target user's `~/.vigo-puddle/`
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
/// and `[1 byte salt-len][salt][wrapped blob]`. The sentinel is
/// removed before returning so the code is single-use even if the
/// response is interrupted and the peer retries.
pub async fn handle_pair_claim(
    path: &str,
    req: Request<hyper::body::Incoming>,
) -> Result<Response<Full<Bytes>>> {
    let user = match parse_claim_path(path, PAIR_CLAIM_PREFIX) {
        Some(u) => u,
        None => return Ok(codes::plain(StatusCode::BAD_REQUEST, "malformed path")),
    };

    let home = match resolve_user_home(&user) {
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

    let spec = match read_pending(&home) {
        Ok(Some(s)) => s,
        Ok(None) => {
            return Ok(codes::plain(
                StatusCode::NOT_FOUND,
                "no active pair session",
            ))
        }
        Err(e) => {
            tracing::warn!(error = %e, "puddle pair: cannot read pending");
            return Ok(codes::plain(
                StatusCode::INTERNAL_SERVER_ERROR,
                "pair state read failed",
            ));
        }
    };

    let now = codes::now_secs();
    if now >= spec.expires_at {
        clear_pending(&home);
        return Ok(codes::plain(StatusCode::GONE, "pair session expired"));
    }

    let incoming_hash = hash_code(&claim.code);
    if !hashes_equal(&incoming_hash, &spec.code_hash) {
        return Ok(codes::plain(StatusCode::UNAUTHORIZED, "code mismatch"));
    }

    let wrapped_path = config::wrapped_path(&home);
    let wrapped = match fs::read(&wrapped_path) {
        Ok(b) => b,
        Err(e) => {
            tracing::warn!(
                path = %wrapped_path.display(),
                error = %e,
                "puddle pair: cannot read wrapped blob"
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
                "puddle pair: cannot read salt"
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
            tracing::warn!(error = %e, "puddle pair: pack_response failed");
            return Ok(codes::plain(
                StatusCode::INTERNAL_SERVER_ERROR,
                "response pack failed",
            ));
        }
    };

    tracing::info!(
        user = %user,
        "puddle pair: served wrapped identity to claimant"
    );
    Response::builder()
        .status(StatusCode::OK)
        .header("Content-Type", "application/octet-stream")
        .body(Full::new(Bytes::from(body)))
        .context("building pair response")
}

fn resolve_user_home(username: &str) -> Option<std::path::PathBuf> {
    crate::swarm::lockbox::home_for_user_pub(username)
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn pair_ttl_secs_matches_codes_constant() {
        // The re-export must keep cli.rs's "5 minutes" output honest
        // — if codes::CLAIM_TTL_SECS ever drifts, this catches it.
        assert_eq!(PAIR_TTL_SECS, super::codes::CLAIM_TTL_SECS);
    }

    #[test]
    fn pair_claim_prefix_is_unique() {
        // Sanity-check no other route owns this prefix; pair-claim
        // and rekey-claim share the `/swarm/puddle/` namespace
        // and must not collide.
        assert_eq!(PAIR_CLAIM_PREFIX, "/swarm/puddle/pair-claim/");
    }
}
