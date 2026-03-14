// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: Elastic-2.0

//! ED25519 payload signing compatible with the Go agent.
//!
//! Go's `auth.SignPayload` concatenates `payload || timestamp_string` and signs
//! the result. The timestamp is the Unix seconds as a decimal string. The server
//! verifies the same way with a 5-minute window.

use ed25519_dalek::{Signer, SigningKey, Verifier, VerifyingKey};

/// Sign payload+timestamp with the given ED25519 private key.
/// `timestamp` is the Unix seconds as a decimal string (e.g. "1709683200").
pub fn sign_payload(payload: &[u8], timestamp: &str, key: &SigningKey) -> Vec<u8> {
    let msg = build_signed_message(payload, timestamp);
    let sig = key.sign(&msg);
    sig.to_bytes().to_vec()
}

/// Verify an ED25519 signature over payload+timestamp.
pub fn verify_payload(
    payload: &[u8],
    timestamp: &str,
    sig_bytes: &[u8],
    pub_key: &VerifyingKey,
) -> bool {
    if sig_bytes.len() != 64 {
        return false;
    }
    let sig_array: [u8; 64] = sig_bytes.try_into().unwrap();
    let sig = ed25519_dalek::Signature::from_bytes(&sig_array);
    let msg = build_signed_message(payload, timestamp);
    pub_key.verify(&msg, &sig).is_ok()
}

/// Concatenate payload and timestamp bytes (matching Go's buildSignedMessage).
fn build_signed_message(payload: &[u8], timestamp: &str) -> Vec<u8> {
    let mut msg = Vec::with_capacity(payload.len() + timestamp.len());
    msg.extend_from_slice(payload);
    msg.extend_from_slice(timestamp.as_bytes());
    msg
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::auth::keypair::generate_keypair;

    #[test]
    fn test_sign_payload() {
        let (_, signing) = generate_keypair().unwrap();
        let payload = b"test payload data";
        let timestamp = "1709683200";

        let sig = sign_payload(payload, timestamp, &signing);
        assert_eq!(sig.len(), 64); // ED25519 signature size
    }

    #[test]
    fn test_verify_payload() {
        let (verifying, signing) = generate_keypair().unwrap();
        let payload = b"test payload data";
        let timestamp = "1709683200";

        let sig = sign_payload(payload, timestamp, &signing);
        assert!(verify_payload(payload, timestamp, &sig, &verifying));
    }

    #[test]
    fn test_verify_payload_tampered() {
        let (verifying, signing) = generate_keypair().unwrap();
        let payload = b"original payload";
        let timestamp = "1709683200";

        let sig = sign_payload(payload, timestamp, &signing);
        assert!(!verify_payload(b"tampered payload", timestamp, &sig, &verifying));
    }

    #[test]
    fn test_verify_payload_wrong_key() {
        let (_, signing) = generate_keypair().unwrap();
        let (wrong_verifying, _) = generate_keypair().unwrap();

        let payload = b"test payload data";
        let timestamp = "1709683200";

        let sig = sign_payload(payload, timestamp, &signing);
        assert!(!verify_payload(payload, timestamp, &sig, &wrong_verifying));
    }

    #[test]
    fn test_signature_includes_timestamp() {
        let (verifying, signing) = generate_keypair().unwrap();
        let payload = b"test payload data";
        let ts1 = "1709683200";
        let ts2 = "1709683500";

        let sig = sign_payload(payload, ts1, &signing);

        // Same payload but different timestamp should fail
        assert!(!verify_payload(payload, ts2, &sig, &verifying));
        // Original timestamp should still verify
        assert!(verify_payload(payload, ts1, &sig, &verifying));
    }
}
