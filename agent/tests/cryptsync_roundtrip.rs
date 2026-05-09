// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

//! Integration test pinning the cryptsync encrypt/decrypt round-trip
//! and the passphrase-wrapped identity envelope.
//!
//! Two assertions:
//!
//! 1. **File round-trip.** Generate a fresh age x25519 identity. Write
//!    a plaintext file. `encrypt_file(src, dst, [identity.recipient])`.
//!    `decrypt_to_writer(dst, identity, &mut buf)`. Plaintext bytes
//!    must match exactly, and the ciphertext must start with the
//!    `age-encryption.org/v1` magic.
//!
//! 2. **Passphrase wrap round-trip.** Wrap a fresh identity under a
//!    test passphrase + salt via `wrap_identity`. Unwrap via
//!    `unwrap_identity` with the same passphrase + salt. Resulting
//!    Identity's pubkey must match the original — a corrupted wrap or
//!    a regressed Argon2id/XChaCha20-Poly1305 envelope would surface
//!    here.
//!
//! These run in <100 ms on a working build.

use std::io::{Cursor, Read};

use age::secrecy::ExposeSecret;
use age::x25519::Identity;
use tempfile::TempDir;

use vigo::swarm::cryptsync::crypt::{
    decrypt_to_writer, encrypt_file, fresh_salt, unwrap_identity, wrap_identity, AGE_MAGIC,
};

#[test]
fn encrypt_then_decrypt_roundtrip() {
    let tmp = TempDir::new().expect("tempdir");
    let src = tmp.path().join("plain.txt");
    let dst = tmp.path().join("plain.txt.age");

    let plaintext = b"cryptsync round-trip test bytes\n";
    std::fs::write(&src, plaintext).unwrap();

    let identity = Identity::generate();
    let recipient = identity.to_public();

    encrypt_file(&src, &dst, &[recipient]).expect("encrypt");

    // Ciphertext must carry the age v1 magic — pin the on-disk format.
    let mut header = [0u8; AGE_MAGIC.len()];
    let mut f = std::fs::File::open(&dst).unwrap();
    let n = f.read(&mut header).unwrap();
    assert_eq!(n, AGE_MAGIC.len(), "ciphertext too short to carry magic");
    assert_eq!(
        &header, AGE_MAGIC,
        "ciphertext does not start with age v1 magic"
    );

    // Round-trip decrypt yields the exact bytes back.
    let mut out = Cursor::new(Vec::new());
    decrypt_to_writer(&dst, &identity, &mut out).expect("decrypt");
    assert_eq!(out.into_inner(), plaintext);
}

#[test]
fn passphrase_wrap_unwrap_roundtrip() {
    let identity = Identity::generate();
    let original_pub = identity.to_public().to_string();

    let salt = fresh_salt();
    let password = b"substrate-test-passphrase";
    let wrapped = wrap_identity(&identity, password, &salt).expect("wrap");

    let recovered = unwrap_identity(&wrapped, password, &salt).expect("unwrap");
    assert_eq!(
        recovered.to_public().to_string(),
        original_pub,
        "unwrapped identity pubkey must match original",
    );

    // Round-trip the secret key string too — equality on the public
    // half doesn't catch a wrap that silently substituted a different
    // identity carrying the same pubkey form.
    assert_eq!(
        recovered.to_string().expose_secret(),
        identity.to_string().expose_secret(),
    );
}

#[test]
fn unwrap_rejects_wrong_passphrase() {
    let identity = Identity::generate();
    let salt = fresh_salt();
    let wrapped = wrap_identity(&identity, b"correct", &salt).expect("wrap");
    let result = unwrap_identity(&wrapped, b"wrong", &salt);
    let err = match result {
        Ok(_) => panic!("unwrap with wrong passphrase must fail"),
        Err(e) => e,
    };
    let msg = format!("{}", err).to_lowercase();
    assert!(
        msg.contains("wrong password") || msg.contains("corrupted"),
        "expected wrong-password / corrupted error, got: {}",
        msg,
    );
}

#[test]
fn unwrap_rejects_wrong_salt() {
    let identity = Identity::generate();
    let wrapped = wrap_identity(&identity, b"pw", &fresh_salt()).expect("wrap");
    let result = unwrap_identity(&wrapped, b"pw", &fresh_salt());
    let err = match result {
        Ok(_) => panic!("unwrap with wrong salt must fail"),
        Err(e) => e,
    };
    let msg = format!("{}", err).to_lowercase();
    assert!(
        msg.contains("wrong password") || msg.contains("corrupted"),
        "expected wrong-password / corrupted error, got: {}",
        msg,
    );
}
