// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

//! Lockbox blob-content authentication.
//!
//! Background: lockbox DELETE / LEAVE / RESURRECT control envelopes are
//! Ed25519-signed and verified against the sender's peer-table pubkey
//! (see [`super::tombstones`]). Blob **content**, however, was historically
//! accepted on only AGE-magic + filename validation + not-tombstoned —
//! age gives confidentiality but **not** sender authentication, and the
//! recipient (age) pubkeys are broadcast in cleartext. An attacker who
//! could write a blob (LAN MITM, compromised-but-enrolled envoy, or the
//! mTLS-only `POST /swarm/lockbox/push`) could therefore inject
//! attacker-chosen plaintext that the victim would decrypt and trust.
//!
//! This module closes that hole by signing the blob the same way the
//! control envelopes are signed: the **sender** (the envoy pushing the
//! blob, or the envoy serving it during catch-up pull) signs a canonical
//! byte string covering the user, filename, mtime, and a SHA-256 of the
//! ciphertext, with its per-envoy Ed25519 signing key. The receiver
//! resolves that envoy's signing pubkey from its own `peers.json` (never
//! from the wire) and verifies before accepting the bytes — exactly as
//! `handle_tombstone_delete` does.
//!
//! Forgery is closed because a MITM that is not an enrolled peer has no
//! peer-table identity and cannot produce a signature that verifies. A
//! swapped blob fails because the ciphertext SHA-256 is covered. A
//! replayed *older* genuine blob is rejected by last-write-wins on the
//! covered `mtime` (callers compare against the locally-stored mtime
//! before accepting).
//!
//! Domain separation: the signed bytes are prefixed with
//! [`BLOB_DOMAIN`] (`b"vigo-lockbox-blob-v1\0"`), distinct from the
//! control-envelope domains, so a blob signature can never be replayed
//! as a DELETE / LEAVE / RESURRECT and vice versa.

use anyhow::{bail, Context, Result};
use ed25519_dalek::{Signer, Verifier};
use serde::{Deserialize, Serialize};
use sha2::{Digest, Sha256};

use super::crypt;

/// Domain-separation tag for blob-content signatures. Distinct from the
/// control-envelope canonical-bytes layouts so a signature minted for one
/// purpose can never verify for another.
pub const BLOB_DOMAIN: &[u8] = b"vigo-lockbox-blob-v1\0";

/// HTTP header carrying the JSON [`BlobSig`] on `POST /swarm/lockbox/push`.
pub const BLOBSIG_HEADER: &str = "X-Vigo-Lockbox-BlobSig";

/// The authenticated descriptor of a blob, carried alongside the
/// ciphertext (push header / catch-up file-list field). The receiver
/// recomputes the canonical bytes from the actual ciphertext it received
/// and verifies `signature` against the sender's peer-table pubkey.
///
/// `signer_pubkey` is for logs/auditing only — verification uses the
/// pubkey resolved from the local peer table by `signer_hostname`, never
/// the value carried here (a malicious peer could set it to anything).
#[derive(Debug, Clone, Serialize, Deserialize, PartialEq, Eq)]
pub struct BlobSig {
    /// Sending envoy's hostname — the peer-table key the receiver uses
    /// to resolve the authoritative signing pubkey.
    pub signer_hostname: String,
    /// Sending envoy's signing pubkey (textual `ed25519:` form). Audit
    /// only; NOT trusted for verification.
    pub signer_pubkey: String,
    /// Ciphertext mtime (unix seconds) — the last-write-wins version.
    pub mtime: i64,
    /// Hex SHA-256 of the ciphertext bytes this signature covers.
    pub ciphertext_sha256: String,
    /// Hex-encoded 64-byte Ed25519 signature over [`canonical_bytes`].
    pub signature: String,
}

/// The bytes that get signed / verified. NUL-separated fields after the
/// domain tag so unusual filenames can't collide with a different
/// `user + filename + sha` combination.
pub fn canonical_bytes(user: &str, filename: &str, mtime: i64, ciphertext_sha256: &str) -> Vec<u8> {
    let mut out = Vec::with_capacity(
        BLOB_DOMAIN.len() + user.len() + filename.len() + ciphertext_sha256.len() + 16,
    );
    out.extend_from_slice(BLOB_DOMAIN);
    out.extend_from_slice(user.as_bytes());
    out.push(0);
    out.extend_from_slice(filename.as_bytes());
    out.push(0);
    out.extend_from_slice(&mtime.to_be_bytes());
    out.push(0);
    out.extend_from_slice(ciphertext_sha256.as_bytes());
    out
}

/// Hex SHA-256 of the ciphertext bytes — the same representation
/// `snapshot::FileEntry.sha256` carries, so the two are interchangeable.
pub fn sha256_hex(ciphertext: &[u8]) -> String {
    let mut h = Sha256::new();
    h.update(ciphertext);
    hex::encode(h.finalize())
}

/// Build a signed descriptor for `ciphertext`. The signer proves
/// possession of this envoy's Ed25519 signing key; receivers verify
/// against the known pubkey from their peer table.
pub fn build(
    signing_key: &ed25519_dalek::SigningKey,
    signer_hostname: &str,
    user: &str,
    filename: &str,
    mtime: i64,
    ciphertext: &[u8],
) -> BlobSig {
    let ciphertext_sha256 = sha256_hex(ciphertext);
    let msg = canonical_bytes(user, filename, mtime, &ciphertext_sha256);
    let sig = signing_key.sign(&msg);
    BlobSig {
        signer_hostname: signer_hostname.to_string(),
        signer_pubkey: crypt::encode_sign_pubkey(&signing_key.verifying_key()),
        mtime,
        ciphertext_sha256,
        signature: hex::encode(sig.to_bytes()),
    }
}

/// Verify a blob descriptor against the expected signing pubkey (looked
/// up from `peers.json` by `sig.signer_hostname` on the receiving side)
/// AND against the actual ciphertext received. Both must agree:
///
/// 1. `sig.ciphertext_sha256` must equal the SHA-256 of `ciphertext`
///    (a swapped blob fails here even before the signature check).
/// 2. The Ed25519 signature must verify over the recomputed canonical
///    bytes under `expected_pubkey`.
///
/// `expected_pubkey` is authoritative — `sig.signer_pubkey` is never
/// trusted for verification, mirroring `tombstones::verify_envelope`.
pub fn verify(
    sig: &BlobSig,
    expected_pubkey: &str,
    user: &str,
    filename: &str,
    ciphertext: &[u8],
) -> Result<()> {
    // 1. Content binding — the signed sha must match the bytes on hand.
    let actual_sha = sha256_hex(ciphertext);
    if actual_sha != sig.ciphertext_sha256 {
        bail!(
            "ciphertext hash mismatch: signed {} but received {}",
            sig.ciphertext_sha256,
            actual_sha
        );
    }

    // 2. Signature over the canonical bytes (domain-tagged).
    let vk = crypt::decode_sign_pubkey(expected_pubkey)?;
    let sig_bytes = hex::decode(&sig.signature).context("hex-decoding blob signature")?;
    if sig_bytes.len() != crypt::SIGN_SIGNATURE_LEN {
        bail!(
            "blob signature is {} bytes, expected {}",
            sig_bytes.len(),
            crypt::SIGN_SIGNATURE_LEN
        );
    }
    let mut sig_arr = [0u8; 64];
    sig_arr.copy_from_slice(&sig_bytes);
    let signature = ed25519_dalek::Signature::from_bytes(&sig_arr);

    let msg = canonical_bytes(user, filename, sig.mtime, &sig.ciphertext_sha256);
    vk.verify(&msg, &signature)
        .map_err(|e| anyhow::anyhow!("blob signature verification failed: {}", e))?;
    Ok(())
}

/// Last-write-wins replay decision: is a blob signed at `signed_mtime`
/// stale relative to a locally-held copy at `local_mtime`? A strictly
/// older signed mtime is a replayed older genuine blob and must lose;
/// equal mtime is allowed (idempotent re-push / same version). Callers
/// pass `local_mtime` only when a local copy exists — a fresh filename
/// is never a replay.
pub fn is_replay(signed_mtime: i64, local_mtime: i64) -> bool {
    signed_mtime < local_mtime
}

#[cfg(test)]
mod tests {
    use super::*;

    fn ct() -> Vec<u8> {
        let mut v = b"age-encryption.org/v1\n".to_vec();
        v.extend_from_slice(b"some encrypted bytes here");
        v
    }

    #[test]
    fn roundtrip_verifies_ok() {
        let key = crypt::generate_signing_keypair();
        let pubkey = crypt::encode_sign_pubkey(&key.verifying_key());
        let bytes = ct();
        let sig = build(&key, "host-a", "dan", "foo.pdf", 1000, &bytes);
        verify(&sig, &pubkey, "dan", "foo.pdf", &bytes).unwrap();
    }

    #[test]
    fn rejects_wrong_key() {
        let key_a = crypt::generate_signing_keypair();
        let key_b = crypt::generate_signing_keypair();
        let bytes = ct();
        let sig = build(&key_a, "host-a", "dan", "foo.pdf", 1000, &bytes);
        let wrong = crypt::encode_sign_pubkey(&key_b.verifying_key());
        assert!(verify(&sig, &wrong, "dan", "foo.pdf", &bytes).is_err());
    }

    #[test]
    fn rejects_swapped_ciphertext() {
        // Attacker keeps a valid signature but substitutes the bytes.
        let key = crypt::generate_signing_keypair();
        let pubkey = crypt::encode_sign_pubkey(&key.verifying_key());
        let bytes = ct();
        let sig = build(&key, "host-a", "dan", "foo.pdf", 1000, &bytes);

        let mut evil = b"age-encryption.org/v1\n".to_vec();
        evil.extend_from_slice(b"attacker-chosen plaintext envelope");
        assert!(verify(&sig, &pubkey, "dan", "foo.pdf", &evil).is_err());
    }

    #[test]
    fn rejects_tampered_filename() {
        let key = crypt::generate_signing_keypair();
        let pubkey = crypt::encode_sign_pubkey(&key.verifying_key());
        let bytes = ct();
        let sig = build(&key, "host-a", "dan", "foo.pdf", 1000, &bytes);
        // URL/path claims a different filename than what was signed.
        assert!(verify(&sig, &pubkey, "dan", "evil.pdf", &bytes).is_err());
    }

    #[test]
    fn rejects_tampered_mtime() {
        let key = crypt::generate_signing_keypair();
        let pubkey = crypt::encode_sign_pubkey(&key.verifying_key());
        let bytes = ct();
        let mut sig = build(&key, "host-a", "dan", "foo.pdf", 1000, &bytes);
        // Bump the version field post-sign — signature no longer covers it.
        sig.mtime = 5000;
        assert!(verify(&sig, &pubkey, "dan", "foo.pdf", &bytes).is_err());
    }

    #[test]
    fn domain_tag_present_in_canonical_bytes() {
        let b = canonical_bytes("dan", "foo.pdf", 1, "abc");
        assert!(b.starts_with(BLOB_DOMAIN));
    }

    #[test]
    fn replayed_older_blob_loses_to_newer_local() {
        // A genuine blob signed at an older mtime than the copy we hold
        // is a replay — reject. Newer or equal is accepted.
        assert!(is_replay(/* signed */ 100, /* local */ 200));
        assert!(!is_replay(300, 200)); // newer wins
        assert!(!is_replay(200, 200)); // equal allowed (idempotent re-push)
    }
}
