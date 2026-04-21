// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

//! HTTPS route handlers for vigocrypt. Piggybacks on the swarm peer
//! HTTPS server on port 1531 — the peer is already mTLS-authenticated
//! by the swarm server, so any agent that reached us is trusted.
//!
//! Routes (dispatched from `agent/src/swarm/server.rs`):
//!
//! - `GET /vigocrypt/pull/{user}/{filename}` — serve ciphertext bytes
//!   from `~<user>/vigocrypt/<filename>`. Mirrors vigosync's pull
//!   path but with a `/pull/` prefix segment for clarity (vigocrypt
//!   has a matching `/push/`, vigosync does not).
//!
//! - `POST /vigocrypt/push/{user}/{filename}` — receive a ciphertext
//!   blob from a peer and write it into `~<user>/vigocrypt/<filename>`
//!   atomically. Body capped at 1 GiB (vs. vigosync's 128 KiB cap on
//!   `/vigosync/announce` — push is a different beast).
//!
//! - `POST /vigocrypt/announce` — receive an Announcement JSON from
//!   a cross-subnet peer listed in some local user's `extra_peers:`.
//!   Body capped at 128 KiB. Feeds into the same reconciler channel
//!   as multicast discovery.
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

/// Max size of a single pushed ciphertext blob.
pub const MAX_PUSH_BYTES: usize = 1024 * 1024 * 1024; // 1 GiB

/// Max size of an announcement body.
pub const MAX_ANNOUNCE_BYTES: usize = 128 * 1024;

// ---------- pull (GET /vigocrypt/pull/{user}/{filename}) ----------

/// Handle `GET /vigocrypt/pull/{user}/{filename}` — serve one
/// ciphertext file. Returns 404 for missing files, 400 for malformed
/// paths. Does blocking I/O; caller should wrap in
/// `tokio::task::block_in_place`.
pub fn handle_pull(path: &str) -> Response<Full<Bytes>> {
    let segments: Vec<&str> = path.trim_start_matches('/').split('/').collect();
    let (user, name) = match segments.as_slice() {
        ["vigocrypt", "pull", user, name] => (*user, *name),
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
    let target = home.join("vigocrypt").join(&name);

    // Canonicalize to defeat symlink-based traversal. Refuse to serve
    // anything that resolves outside ~<user>/vigocrypt/.
    let content_dir = home.join("vigocrypt");
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
    if meta.len() as usize > MAX_PUSH_BYTES {
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

// ---------- push (POST /vigocrypt/push/{user}/{filename}) ----------

/// Handle `POST /vigocrypt/push/{user}/{filename}` — write a
/// ciphertext blob into `~<user>/vigocrypt/<filename>` via temp-file
/// + rename. Returns 201 on success, 400/404/413 on error.
pub async fn handle_push(
    path: &str,
    req: Request<hyper::body::Incoming>,
) -> Response<Full<Bytes>> {
    let segments: Vec<&str> = path.trim_start_matches('/').split('/').collect();
    let (user, name) = match segments.as_slice() {
        ["vigocrypt", "push", user, name] => (*user, *name),
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

    // Pre-check Content-Length (cheap rejection without reading body).
    if let Some(cl) = req
        .headers()
        .get("content-length")
        .and_then(|v| v.to_str().ok())
        .and_then(|s| s.parse::<usize>().ok())
    {
        if cl > MAX_PUSH_BYTES {
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
    if bytes.len() > MAX_PUSH_BYTES {
        return payload_too_large();
    }

    // Defense-in-depth: require the first bytes to be the age magic.
    // Rejects anyone trying to push plaintext through this endpoint.
    const AGE_MAGIC: &[u8] = b"age-encryption.org/v1\n";
    if bytes.len() < AGE_MAGIC.len() || &bytes[..AGE_MAGIC.len()] != AGE_MAGIC {
        return bad_request("pushed body is not age-encrypted");
    }

    let home = match user_home(&user) {
        Some(h) => h,
        None => return not_found(),
    };
    let content_dir = home.join("vigocrypt");
    // Create the content dir if missing — pushing to a freshly-enrolled
    // envoy may arrive before the user runs `vigo crypt init/enroll`
    // locally (rare race but valid; the envoy can't decrypt without the
    // identity but the ciphertext is still legal to store).
    if let Err(e) = fs::create_dir_all(&content_dir) {
        tracing::warn!(
            dir = %content_dir.display(),
            error = %e,
            "vigocrypt push: cannot create content dir"
        );
        return internal_error("cannot create content dir");
    }

    let target = content_dir.join(&name);
    let tmp: PathBuf = content_dir.join(format!(".{}.vigocrypt.push.tmp", name));
    match write_and_rename(&tmp, &target, &bytes) {
        Ok(()) => Response::builder()
            .status(StatusCode::CREATED)
            .header("Content-Type", "application/json")
            .body(Full::new(Bytes::from(r#"{"status":"accepted"}"#)))
            .unwrap(),
        Err(e) => {
            tracing::warn!(
                path = %target.display(),
                error = %e,
                "vigocrypt push: write failed"
            );
            internal_error("write failed")
        }
    }
}

fn write_and_rename(tmp: &std::path::Path, target: &std::path::Path, data: &[u8]) -> std::io::Result<()> {
    {
        let mut f = fs::File::create(tmp)?;
        f.write_all(data)?;
        f.sync_all().ok();
    }
    fs::rename(tmp, target)
}

// ---------- announce (POST /vigocrypt/announce) ----------

/// Handle `POST /vigocrypt/announce` — feed a unicast announcement
/// into the vigocrypt daemon's reconciler channel. Returns 503 if the
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
                "vigocrypt daemon not running on this envoy",
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
        .body(Full::new(Bytes::from("payload exceeds vigocrypt limit")))
        .unwrap()
}

fn internal_error(msg: &str) -> Response<Full<Bytes>> {
    Response::builder()
        .status(StatusCode::INTERNAL_SERVER_ERROR)
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
