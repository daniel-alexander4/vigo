// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

//! Bundle signature verification using ED25519.
//!
//! The server signs PolicyBundle fields 1-9 with its ED25519 key. The agent
//! verifies the signature using the server's public key received during
//! registration and stored in LMDB.

use crate::proto::PolicyBundle;
use anyhow::{bail, Result};
use ed25519_dalek::{Signature, Verifier, VerifyingKey};
use prost::Message;
use spki::DecodePublicKey;

/// Verify that a PolicyBundle's signature is valid against the server's public key PEM.
///
/// Returns Ok(()) if signature is valid, or Err if verification fails.
/// Both the server public key and bundle signature are required.
pub fn verify_bundle(bundle: &PolicyBundle, server_pubkey_pem: &str) -> Result<()> {
    if server_pubkey_pem.is_empty() {
        bail!("server public key not configured");
    }

    if bundle.signature.is_empty() {
        bail!("bundle has no signature but server pubkey is configured");
    }

    let verifying_key = VerifyingKey::from_public_key_pem(server_pubkey_pem)
        .map_err(|e| anyhow::anyhow!("parsing server public key PEM: {}", e))?;

    let content_bytes = marshal_bundle_content(bundle);
    let sig = Signature::from_slice(&bundle.signature)
        .map_err(|e| anyhow::anyhow!("parsing signature: {}", e))?;

    verifying_key
        .verify(&content_bytes, &sig)
        .map_err(|_| anyhow::anyhow!("bundle signature verification failed"))?;

    Ok(())
}

/// Serialize the policy content fields (1-9) for signature verification.
/// Must match the server's `marshalBundleContent` exactly.
fn marshal_bundle_content(bundle: &PolicyBundle) -> Vec<u8> {
    let content = PolicyBundle {
        policy_version: bundle.policy_version,
        modules: bundle.modules.clone(),
        vars_json: bundle.vars_json.clone(),
        var_order: bundle.var_order.clone(),
        conditional_vars: bundle.conditional_vars.clone(),
        custom_traits: bundle.custom_traits.clone(),
        matched_pattern: bundle.matched_pattern.clone(),
        environment: bundle.environment.clone(),
        roles: bundle.roles.clone(),
        // Fields 10-12 intentionally zeroed.
        signature: vec![],
        server_pubkey_id: String::new(),
        max_age_seconds: 0,
    };
    content.encode_to_vec()
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::proto::PolicyModule;

    /// Helper: generate a keypair, sign a bundle, return (pubkey_pem, signed_bundle).
    fn sign_test_bundle(bundle: &mut PolicyBundle) -> String {
        use ed25519_dalek::{Signer, SigningKey};
        use spki::EncodePublicKey;

        let signing = SigningKey::generate(&mut rand::rngs::OsRng);
        let verifying = signing.verifying_key();
        let pem = verifying
            .to_public_key_pem(pkcs8::LineEnding::LF)
            .unwrap();

        let content_bytes = marshal_bundle_content(bundle);
        let sig = signing.sign(&content_bytes);
        bundle.signature = sig.to_bytes().to_vec();
        bundle.server_pubkey_id = "test1234".into();

        pem
    }

    #[test]
    fn verify_valid_bundle() {
        let mut bundle = PolicyBundle {
            policy_version: 42,
            matched_pattern: "*.web".into(),
            environment: "production".into(),
            roles: vec!["webserver".into()],
            modules: vec![PolicyModule {
                name: "nginx".into(),
                content_hash: "abc".into(),
                ..Default::default()
            }],
            ..Default::default()
        };

        let pem = sign_test_bundle(&mut bundle);
        verify_bundle(&bundle, &pem).expect("should verify valid bundle");
    }

    #[test]
    fn reject_bad_signature() {
        let mut bundle = PolicyBundle {
            policy_version: 1,
            matched_pattern: "*.db".into(),
            ..Default::default()
        };

        let pem = sign_test_bundle(&mut bundle);

        // Tamper with the bundle.
        bundle.policy_version = 999;

        let result = verify_bundle(&bundle, &pem);
        assert!(result.is_err(), "should reject tampered bundle");
    }

    #[test]
    fn reject_missing_signature() {
        let bundle = PolicyBundle {
            policy_version: 1,
            ..Default::default()
        };

        // Non-empty PEM but no signature on bundle.
        let pem = "-----BEGIN PUBLIC KEY-----\ntest\n-----END PUBLIC KEY-----\n";
        let result = verify_bundle(&bundle, pem);
        assert!(result.is_err(), "should reject missing signature");
    }

    #[test]
    fn reject_unsigned_when_no_server_key() {
        let bundle = PolicyBundle {
            policy_version: 1,
            ..Default::default()
        };

        // Empty PEM = server public key not configured — must reject.
        let result = verify_bundle(&bundle, "");
        assert!(result.is_err(), "should reject when server public key is not configured");
    }
}
