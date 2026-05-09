// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

//! HTTPS route handler for serving longdrawer file contents + per-user state
//! to peer agents. Piggybacks on the swarm peer HTTPS server on port 1531
//! — the peer is already mTLS-authenticated by the swarm server, so any
//! agent that reached us is trusted.
//!
//! Routes:
//!   `GET /swarm/longdrawer/state/{user}`    → full `UserState` JSON (pulled on
//!                                 digest-mismatch from the 0.30.11+
//!                                 digest-only multicast announcement)
//!   `GET /swarm/longdrawer/{user}/{filename}` → raw file bytes

use http_body_util::Full;
use hyper::body::Bytes;
use hyper::{Response, StatusCode};
use std::fs;

use super::applier::valid_filename;
use super::scanner::effective_max_file_size;
use super::state::UserState;
use crate::platform::paths::default_longdrawer_dir;
use crate::platform::users::user_home;

/// Handle a `/swarm/longdrawer/...` request. Returns 404 for missing files,
/// 400 for malformed paths, 413 for oversize files, 200 with raw bytes or
/// JSON.
pub fn handle(path: &str) -> Response<Full<Bytes>> {
    let segments: Vec<&str> = path.trim_start_matches('/').split('/').collect();
    // Per-user state pull. Receivers call this on digest mismatch to
    // catch up; the announcement only carried the digest so we never
    // leaked file metadata to the LAN.
    if let ["swarm", "longdrawer", "state", user] = segments.as_slice() {
        return handle_state(user);
    }
    // ["swarm", "longdrawer", user, filename]
    let (user, name) = match segments.as_slice() {
        ["swarm", "longdrawer", user, name] => (*user, *name),
        _ => return not_found(),
    };

    // URL-decode both components.
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
    let target = home.join("longdrawer").join(&name);

    // Belt-and-braces: ensure the resolved path is still inside ~<user>/longdrawer/
    // after any symlink shenanigans. Use canonicalize and check prefix.
    let sync_dir = home.join("longdrawer");
    let canon_target = match target.canonicalize() {
        Ok(p) => p,
        Err(_) => return not_found(),
    };
    let canon_dir = match sync_dir.canonicalize() {
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
    if meta.len() > effective_max_file_size() {
        return Response::builder()
            .status(StatusCode::PAYLOAD_TOO_LARGE)
            .body(Full::new(Bytes::from("file exceeds longdrawer size limit")))
            .unwrap();
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

/// Handle `GET /swarm/longdrawer/state/{user}` — return the full UserState JSON for
/// `user` on this envoy. Called by the multicast-receiver side of a peer
/// when the announced digest doesn't match the peer's cached value for
/// this (user, machine) tuple.
fn handle_state(user: &str) -> Response<Full<Bytes>> {
    let user = match percent_decode(user) {
        Some(s) => s,
        None => return bad_request("invalid user encoding"),
    };
    if !valid_username(&user) {
        return bad_request("invalid user");
    }

    // Verify this envoy actually has a local account for `user`.
    // Without a home dir there's nothing to serve and returning an
    // empty state would look like a live user with zero files.
    if user_home(&user).is_none() {
        return not_found();
    }

    let state_dir = default_longdrawer_dir();
    let state = UserState::load(&state_dir, &user);
    match serde_json::to_vec(&state) {
        Ok(body) => Response::builder()
            .status(StatusCode::OK)
            .header("Content-Type", "application/json")
            .body(Full::new(Bytes::from(body)))
            .unwrap(),
        Err(_) => Response::builder()
            .status(StatusCode::INTERNAL_SERVER_ERROR)
            .body(Full::new(Bytes::from("serialize state")))
            .unwrap(),
    }
}

fn valid_username(name: &str) -> bool {
    if name.is_empty() || name.len() > 64 {
        return false;
    }
    name.chars()
        .all(|c| c.is_ascii_alphanumeric() || c == '-' || c == '_' || c == '.')
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
    fn decode_percent_encoding() {
        assert_eq!(percent_decode("dan").as_deref(), Some("dan"));
        assert_eq!(percent_decode("file%20name").as_deref(), Some("file name"));
        assert_eq!(percent_decode("%41").as_deref(), Some("A"));
        assert_eq!(percent_decode("bad%ZZ"), None);
        assert_eq!(percent_decode("trail%2"), None);
    }
}
