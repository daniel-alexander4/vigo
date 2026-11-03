// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: Apache-2.0

//! Password-wrapped age identity + age file encryption helpers.
//!
//! The age private key never touches disk in plaintext. On `init` /
//! `enroll`, the CLI:
//!
//! 1. Generates 16 bytes of salt from the system CSPRNG.
//! 2. Derives a 32-byte wrap key via Argon2id over (password, salt).
//! 3. Generates a fresh age X25519 identity.
//! 4. Encrypts the identity's bech32 string with XChaCha20-Poly1305
//!    using the wrap key.
//! 5. Writes `identity.age` = `[1 byte version][24 byte nonce][ciphertext]`
//!    and `identity.salt` = salt, both mode 0600.
//!
//! On `unlock`, the session helper reverses the flow: reads
//! `identity.age`, prompts the user for the password, re-derives the
//! wrap key, AEAD-decrypts, parses back into an `Identity`.
//!
//! Argon2id parameters pinned to OWASP 2024 recommendation: memory
//! 64 MiB, 3 iterations, 1 lane, 32-byte output.

use std::fs;
use std::io::{self, Read, Write};
use std::path::Path;

use age::secrecy::ExposeSecret;
use age::x25519::{Identity, Recipient};
use anyhow::{bail, Context, Result};
use argon2::{Algorithm, Argon2, Params, Version};
use chacha20poly1305::aead::{Aead, KeyInit};
use chacha20poly1305::{XChaCha20Poly1305, XNonce};
use rand::RngCore;
use zeroize::Zeroizing;

// ---------- wrapping-format constants ----------

/// On-disk format version for `identity.age`. Bumped if the wrap
/// envelope ever changes shape (e.g. different AEAD or KDF).
pub const WRAP_VERSION: u8 = 0x01;

/// Length of the XChaCha20-Poly1305 nonce.
pub const WRAP_NONCE_LEN: usize = 24;

/// Length of the Argon2id salt written to `identity.salt`.
pub const WRAP_SALT_LEN: usize = 16;

// Argon2id parameters — OWASP 2024. Benchmark on target hardware;
// adjust only if `unlock` falls outside the 0.5–2 s target.
const ARGON_MEM_KIB: u32 = 64 * 1024; // 64 MiB
const ARGON_T_COST: u32 = 3;
const ARGON_P_COST: u32 = 1;
const ARGON_OUTPUT_LEN: usize = 32;

// ---------- salt ----------

/// Generate a fresh random salt.
pub fn fresh_salt() -> [u8; WRAP_SALT_LEN] {
    let mut s = [0u8; WRAP_SALT_LEN];
    rand::thread_rng().fill_bytes(&mut s);
    s
}

// ---------- KDF ----------

/// Derive the 32-byte wrap key from (password, salt) using
/// Argon2id at the pinned parameters.
fn derive_wrap_key(password: &[u8], salt: &[u8]) -> Result<Zeroizing<[u8; ARGON_OUTPUT_LEN]>> {
    let params = Params::new(
        ARGON_MEM_KIB,
        ARGON_T_COST,
        ARGON_P_COST,
        Some(ARGON_OUTPUT_LEN),
    )
    .map_err(|e| anyhow::anyhow!("argon2 param build failed: {}", e))?;
    let argon = Argon2::new(Algorithm::Argon2id, Version::V0x13, params);
    let mut out = Zeroizing::new([0u8; ARGON_OUTPUT_LEN]);
    argon
        .hash_password_into(password, salt, out.as_mut_slice())
        .map_err(|e| anyhow::anyhow!("argon2 hash failed: {}", e))?;
    Ok(out)
}

// ---------- puddle-mode wrap-key derivation ----------

/// Domain-separated challenge signed via the puddle session
/// helper to derive lockbox's wrap-key material in puddle mode
/// (ADR-016). Must not collide with any other puddle signing
/// context (announcement, puddle-cert, pair-claim, etc.) — this prefix
/// version-locks the binding between the puddle identity and a
/// lockbox wrap key.
pub const FLEET_WRAP_CHALLENGE: &[u8] = b"vigo-lockbox-wrap-v1";

/// Derive lockbox wrap-key material from the user's puddle
/// identity by asking the session helper to sign a fixed
/// domain-separated challenge. Ed25519 signatures over a fixed
/// message are deterministic (RFC 8032), so the same puddle identity
/// always produces the same 64 bytes here.
///
/// Returned bytes are passed as the `password` argument to
/// `wrap_identity` / `unwrap_identity` in puddle mode — Argon2id then
/// stretches them into the 32-byte AEAD key the same way it does in
/// standalone mode. The stretch is redundant against already
/// high-entropy input, but it keeps the on-disk wrap envelope
/// identical across modes so the format version doesn't have to fork.
///
/// Requires the puddle session helper to be running. Returns a
/// pointed error message when it isn't, so the operator knows what
/// to fix.
pub fn derive_puddle_wrap_password() -> Result<Zeroizing<Vec<u8>>> {
    let sig = crate::swarm::puddle::session::request_sign(FLEET_WRAP_CHALLENGE).context(
        "puddle session helper unavailable; run `vigo swarm puddle unlock` first",
    )?;
    if sig.len() != 64 {
        bail!(
            "puddle session helper returned a {}-byte signature, expected 64",
            sig.len()
        );
    }
    Ok(Zeroizing::new(sig))
}

// ---------- wrap / unwrap ----------

/// Wrap an age identity with a password-derived key. Returns the
/// on-disk blob: `[version][nonce][ciphertext+tag]`.
pub fn wrap_identity(identity: &Identity, password: &[u8], salt: &[u8]) -> Result<Vec<u8>> {
    if salt.len() != WRAP_SALT_LEN {
        bail!("salt must be {} bytes, got {}", WRAP_SALT_LEN, salt.len());
    }
    let key = derive_wrap_key(password, salt)?;
    let cipher = XChaCha20Poly1305::new(key.as_slice().into());

    let mut nonce_bytes = [0u8; WRAP_NONCE_LEN];
    rand::thread_rng().fill_bytes(&mut nonce_bytes);
    let nonce = XNonce::from_slice(&nonce_bytes);

    // age identity serializes as "AGE-SECRET-KEY-1..." bech32 — plain ASCII.
    let secret = identity.to_string();
    let plaintext = Zeroizing::new(secret.expose_secret().as_bytes().to_vec());

    let ciphertext = cipher
        .encrypt(nonce, plaintext.as_slice())
        .map_err(|e| anyhow::anyhow!("XChaCha20-Poly1305 encrypt failed: {}", e))?;

    let mut out = Vec::with_capacity(1 + WRAP_NONCE_LEN + ciphertext.len());
    out.push(WRAP_VERSION);
    out.extend_from_slice(&nonce_bytes);
    out.extend_from_slice(&ciphertext);
    Ok(out)
}

/// Unwrap an on-disk identity blob with the user's password + salt.
/// Returns a parsed age identity on success. Wrong password yields an
/// AEAD authentication failure, which we report as a generic
/// "wrong password or corrupted blob" error (indistinguishable at the
/// UX layer — both mean "cannot proceed").
pub fn unwrap_identity(blob: &[u8], password: &[u8], salt: &[u8]) -> Result<Identity> {
    if blob.len() < 1 + WRAP_NONCE_LEN + 16 {
        bail!("identity.age is too short to be valid");
    }
    if blob[0] != WRAP_VERSION {
        bail!(
            "identity.age has unknown version byte 0x{:02x} (expected 0x{:02x})",
            blob[0],
            WRAP_VERSION
        );
    }
    let nonce = XNonce::from_slice(&blob[1..1 + WRAP_NONCE_LEN]);
    let ciphertext = &blob[1 + WRAP_NONCE_LEN..];

    let key = derive_wrap_key(password, salt)?;
    let cipher = XChaCha20Poly1305::new(key.as_slice().into());

    let plaintext = cipher
        .decrypt(nonce, ciphertext)
        .map_err(|_| anyhow::anyhow!("wrong password or corrupted identity.age"))?;
    let plaintext = Zeroizing::new(plaintext);

    let secret = std::str::from_utf8(plaintext.as_slice())
        .context("unwrapped identity is not valid UTF-8")?;
    secret
        .trim()
        .parse::<Identity>()
        .map_err(|e| anyhow::anyhow!("unwrapped bytes are not a valid age identity: {}", e))
}

// ---------- age content encryption ----------
//
// These mirror longdrawer/crypt.rs::encrypt_in_place / decrypt_to_writer
// but are re-exported here because lockbox is deliberately
// self-contained (see plan_lockbox.md "Reuse vs. duplication with
// longdrawer"). Shared extraction happens only if a third consumer
// appears.

/// Age file magic — every age-encrypted file begins with this line.
pub const AGE_MAGIC: &[u8] = b"age-encryption.org/v1\n";

/// Return true if the first bytes of `path` match the age magic.
pub fn is_age_encrypted(path: &Path) -> io::Result<bool> {
    let mut f = fs::File::open(path)?;
    let mut buf = [0u8; AGE_MAGIC.len()];
    let n = f.read(&mut buf)?;
    Ok(n == AGE_MAGIC.len() && buf == *AGE_MAGIC)
}

/// Encrypt `src` → age ciphertext at `dst`, to the given recipients.
/// Truncates/creates `dst`. Does not fsync — caller's responsibility
/// if durability matters.
pub fn encrypt_file(src: &Path, dst: &Path, recipients: &[Recipient]) -> Result<()> {
    if recipients.is_empty() {
        bail!("encrypt_file called with no recipients");
    }
    let encryptor = age::Encryptor::with_recipients(
        recipients.iter().map(|r| r as &dyn age::Recipient),
    )
    .map_err(|e| anyhow::anyhow!("age encryptor init failed: {}", e))?;

    let mut reader = io::BufReader::new(
        fs::File::open(src).with_context(|| format!("opening {}", src.display()))?,
    );
    let mut writer = io::BufWriter::new(
        fs::File::create(dst).with_context(|| format!("creating {}", dst.display()))?,
    );

    {
        let mut w = encryptor
            .wrap_output(&mut writer)
            .context("age wrap_output failed")?;
        io::copy(&mut reader, &mut w).context("streaming encryption")?;
        w.finish().context("age writer finish failed")?;
    }
    writer.flush().ok();
    Ok(())
}

/// Decrypt a ciphertext file and stream plaintext to `writer`.
pub fn decrypt_to_writer(
    path: &Path,
    identity: &Identity,
    writer: &mut dyn Write,
) -> Result<()> {
    let f = fs::File::open(path)
        .with_context(|| format!("opening {}", path.display()))?;
    let reader = io::BufReader::new(f);
    let decryptor = age::Decryptor::new(reader).context("reading age header")?;
    let ids: Vec<Box<dyn age::Identity>> = vec![Box::new(identity.clone())];
    let mut r = decryptor
        .decrypt(ids.iter().map(|i| i.as_ref()))
        .context("decrypt — identity does not match any recipient in the file")?;
    io::copy(&mut r, writer).context("streaming decryption")?;
    Ok(())
}

// ---------- Ed25519 signing identity (for DELETE envelopes) ----------
//
// Each enrolled envoy generates an Ed25519 keypair alongside its age
// X25519 identity. The Ed25519 keypair authenticates destructive
// operations (currently: DELETE envelopes that propagate tombstones).
// mTLS says "you're a vigo agent in the fleet"; the Ed25519 signature
// says "you are specifically envoy A speaking for user X," which is
// what we need before we let one envoy delete files off another.
//
// The private signing key is wrapped with the same Argon2id +
// XChaCha20-Poly1305 mechanism as the age identity. Wrapped form:
// `[1 byte version][24 byte nonce][ciphertext+tag]`. Mode 0600.
//
// Public key distribution: every envoy advertises its signing
// pubkey on lockbox multicast announcements (`state.rs`), and
// the receiving peer records it in `peers.json` next to the age
// pubkey. DELETE verification reads the sender's signing pubkey
// out of the local peer table; no central key registry.

/// Length of a raw Ed25519 public key.
pub const SIGN_PUBKEY_LEN: usize = 32;

/// Length of a raw Ed25519 signature.
pub const SIGN_SIGNATURE_LEN: usize = 64;

/// Generate a fresh Ed25519 signing keypair for this envoy.
pub fn generate_signing_keypair() -> ed25519_dalek::SigningKey {
    use rand::rngs::OsRng;
    ed25519_dalek::SigningKey::generate(&mut OsRng)
}

/// Wrap an Ed25519 signing key with the lockbox password.
/// On-disk blob layout is identical to `wrap_identity` so the
/// two files share a decryption path — only the key material
/// inside differs.
pub fn wrap_signing_key(
    key: &ed25519_dalek::SigningKey,
    password: &[u8],
    salt: &[u8],
) -> Result<Vec<u8>> {
    if salt.len() != WRAP_SALT_LEN {
        bail!("salt must be {} bytes, got {}", WRAP_SALT_LEN, salt.len());
    }
    let wrap_key = derive_wrap_key(password, salt)?;
    let cipher = XChaCha20Poly1305::new(wrap_key.as_slice().into());

    let mut nonce_bytes = [0u8; WRAP_NONCE_LEN];
    rand::thread_rng().fill_bytes(&mut nonce_bytes);
    let nonce = XNonce::from_slice(&nonce_bytes);

    let plaintext = Zeroizing::new(key.to_bytes().to_vec());

    let ciphertext = cipher
        .encrypt(nonce, plaintext.as_slice())
        .map_err(|e| anyhow::anyhow!("XChaCha20-Poly1305 encrypt failed: {}", e))?;

    let mut out = Vec::with_capacity(1 + WRAP_NONCE_LEN + ciphertext.len());
    out.push(WRAP_VERSION);
    out.extend_from_slice(&nonce_bytes);
    out.extend_from_slice(&ciphertext);
    Ok(out)
}

/// Unwrap a stored Ed25519 signing key with the lockbox password
/// + salt.
pub fn unwrap_signing_key(
    blob: &[u8],
    password: &[u8],
    salt: &[u8],
) -> Result<ed25519_dalek::SigningKey> {
    if blob.len() < 1 + WRAP_NONCE_LEN + 16 {
        bail!("identity.sign is too short to be valid");
    }
    if blob[0] != WRAP_VERSION {
        bail!(
            "identity.sign has unknown version byte 0x{:02x} (expected 0x{:02x})",
            blob[0],
            WRAP_VERSION
        );
    }
    let nonce = XNonce::from_slice(&blob[1..1 + WRAP_NONCE_LEN]);
    let ciphertext = &blob[1 + WRAP_NONCE_LEN..];

    let wrap_key = derive_wrap_key(password, salt)?;
    let cipher = XChaCha20Poly1305::new(wrap_key.as_slice().into());

    let plaintext = cipher
        .decrypt(nonce, ciphertext)
        .map_err(|_| anyhow::anyhow!("wrong password or corrupted identity.sign"))?;
    let plaintext = Zeroizing::new(plaintext);

    if plaintext.len() != 32 {
        bail!(
            "unwrapped signing key is {} bytes, expected 32",
            plaintext.len()
        );
    }
    let mut bytes = [0u8; 32];
    bytes.copy_from_slice(&plaintext[..]);
    Ok(ed25519_dalek::SigningKey::from_bytes(&bytes))
}

/// Bech32-ish textual form for an Ed25519 pubkey, keeping lockbox's
/// line-based "one key per line" convention. We don't actually use
/// bech32 — just hex with an `ed25519:` prefix so it's obvious what
/// the reader is looking at. Mirrors age's own `age1...` prefix style
/// without pretending it's the same envelope.
pub fn encode_sign_pubkey(pk: &ed25519_dalek::VerifyingKey) -> String {
    format!("ed25519:{}", hex::encode(pk.to_bytes()))
}

/// Parse the textual form back into a verifying key.
pub fn decode_sign_pubkey(s: &str) -> Result<ed25519_dalek::VerifyingKey> {
    let hexpart = s
        .strip_prefix("ed25519:")
        .ok_or_else(|| anyhow::anyhow!("signing pubkey missing ed25519: prefix"))?;
    let bytes = hex::decode(hexpart).context("signing pubkey hex decode")?;
    if bytes.len() != SIGN_PUBKEY_LEN {
        bail!(
            "signing pubkey is {} bytes, expected {}",
            bytes.len(),
            SIGN_PUBKEY_LEN
        );
    }
    let mut arr = [0u8; SIGN_PUBKEY_LEN];
    arr.copy_from_slice(&bytes);
    ed25519_dalek::VerifyingKey::from_bytes(&arr)
        .map_err(|e| anyhow::anyhow!("invalid ed25519 verifying key: {}", e))
}

#[cfg(test)]
mod tests {
    use super::*;

    // ---------- wire-format snapshots ----------
    //
    // Pin the on-disk envelope shape so a future change to the wrap
    // format or KDF parameters trips here loudly. Round-trip semantics
    // are covered by `wrap_unwrap_roundtrips` and the integration
    // test at `agent/tests/lockbox_roundtrip.rs`.

    #[test]
    fn snapshot_age_magic() {
        assert_eq!(AGE_MAGIC, b"age-encryption.org/v1\n");
    }

    #[test]
    fn snapshot_wrap_envelope_constants() {
        // `[1 byte version][24 byte nonce][ciphertext+tag]` — every
        // existing identity.age on disk depends on this byte layout.
        assert_eq!(WRAP_VERSION, 0x01);
        assert_eq!(WRAP_NONCE_LEN, 24);
        assert_eq!(WRAP_SALT_LEN, 16);
    }

    #[test]
    fn snapshot_argon2_parameters() {
        // OWASP 2024 recommendation. Cranking these down weakens the
        // wrap; cranking them up makes `unlock` slower than the
        // 0.5-2s target. Bump deliberately.
        assert_eq!(ARGON_MEM_KIB, 64 * 1024);
        assert_eq!(ARGON_T_COST, 3);
        assert_eq!(ARGON_P_COST, 1);
        assert_eq!(ARGON_OUTPUT_LEN, 32);
    }

    #[test]
    fn wrap_unwrap_roundtrips() {
        let id = Identity::generate();
        let salt = fresh_salt();
        let pw = b"correct-horse-battery-staple";
        let blob = wrap_identity(&id, pw, &salt).unwrap();
        let unwrapped = unwrap_identity(&blob, pw, &salt).unwrap();
        assert_eq!(
            unwrapped.to_public().to_string(),
            id.to_public().to_string()
        );
    }

    // age::x25519::Identity doesn't implement Debug, so
    // Result::unwrap_err() is unavailable. Destructure explicitly.
    fn err_message<T>(r: Result<T>) -> String {
        match r {
            Ok(_) => panic!("expected error, got Ok"),
            Err(e) => e.to_string(),
        }
    }

    #[test]
    fn unwrap_rejects_wrong_password() {
        let id = Identity::generate();
        let salt = fresh_salt();
        let blob = wrap_identity(&id, b"right-password", &salt).unwrap();
        let msg = err_message(unwrap_identity(&blob, b"wrong-password", &salt));
        assert!(
            msg.contains("wrong password or corrupted"),
            "unexpected error: {}",
            msg
        );
    }

    #[test]
    fn unwrap_rejects_wrong_salt() {
        let id = Identity::generate();
        let salt1 = fresh_salt();
        let salt2 = fresh_salt();
        let blob = wrap_identity(&id, b"pw", &salt1).unwrap();
        let msg = err_message(unwrap_identity(&blob, b"pw", &salt2));
        assert!(msg.contains("wrong password or corrupted"));
    }

    #[test]
    fn unwrap_rejects_bad_version_byte() {
        let id = Identity::generate();
        let salt = fresh_salt();
        let mut blob = wrap_identity(&id, b"pw", &salt).unwrap();
        blob[0] = 0xFF;
        let msg = err_message(unwrap_identity(&blob, b"pw", &salt));
        assert!(msg.contains("unknown version"));
    }

    #[test]
    fn unwrap_rejects_truncated_blob() {
        let msg = err_message(unwrap_identity(&[0x01, 0x02], b"pw", &fresh_salt()));
        assert!(msg.contains("too short"));
    }

    #[test]
    fn wrap_is_nondeterministic() {
        let id = Identity::generate();
        let salt = fresh_salt();
        let pw = b"pw";
        let a = wrap_identity(&id, pw, &salt).unwrap();
        let b = wrap_identity(&id, pw, &salt).unwrap();
        assert_ne!(a, b, "fresh nonce per wrap should make blobs differ");
    }

    #[test]
    fn puddle_wrap_challenge_is_domain_separated() {
        // The challenge prefix must not collide with any other
        // puddle signing context (announcements, puddle certs,
        // pair-claim tokens). A collision would let a signature
        // produced for one purpose be reused as a wrap key — same
        // identity, same key derivation, different intent. The
        // version suffix locks us into v1 so a future re-keying flow
        // can rotate to v2 without ambiguity.
        assert_eq!(FLEET_WRAP_CHALLENGE, b"vigo-lockbox-wrap-v1");
        // Sanity-check it isn't an empty / sub-byte challenge.
        assert!(FLEET_WRAP_CHALLENGE.len() >= 8);
    }

    #[test]
    fn encrypt_then_decrypt_file_roundtrips() {
        let dir = tempfile::tempdir().unwrap();
        let id = Identity::generate();
        let src = dir.path().join("plain.txt");
        let dst = dir.path().join("enc.age");
        fs::write(&src, b"secret contents").unwrap();
        encrypt_file(&src, &dst, &[id.to_public()]).unwrap();
        assert!(is_age_encrypted(&dst).unwrap());
        let mut out = Vec::new();
        decrypt_to_writer(&dst, &id, &mut out).unwrap();
        assert_eq!(out, b"secret contents");
    }
}
