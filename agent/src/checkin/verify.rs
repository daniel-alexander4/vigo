// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

//! Bundle signature verification using ED25519.
//!
//! The server signs a subset of PolicyBundle fields with its ED25519 key
//! (see `protobuf/vigo.proto` — fields marked `[(vigo.v1.signed) = false]`
//! are excluded; everything else is signed). The agent re-marshals the
//! same subset here and verifies against the server's public key, which
//! was received during registration and cached on disk + in memory.
//!
//! Source-of-truth split: the proto annotation declares *what* is
//! signed. `marshal_bundle_content` below mirrors that subset in a
//! hand-written field copy — required because prost-reflect's
//! `DynamicMessage` encodes proto `map<…>` fields with non-deterministic
//! ordering, which would defeat the BTreeMap-based determinism the
//! agent's prost-build config enforces (see `agent/build.rs`).
//! `assert_signed_field_set_matches_proto` runs once at startup, walks
//! the embedded file_descriptor_set, and panics if the hand-listed set
//! diverges from the `(vigo.v1.signed)` annotations — the proto stays
//! the source of truth and drift can't ship.

use crate::proto::PolicyBundle;
use anyhow::{bail, Result};
use ed25519_dalek::{Signature, Verifier, VerifyingKey};
use prost::Message;
use prost_reflect::{DescriptorPool, Value};
use spki::DecodePublicKey;
use std::collections::HashSet;
use std::sync::OnceLock;

/// PolicyBundle field tags whose contents participate in the signature.
/// Mirrors `protobuf/vigo.proto`'s fields not marked
/// `[(vigo.v1.signed) = false]`. The startup check below proves the two
/// declarations agree.
const SIGNED_FIELD_TAGS: &[u32] = &[1, 2, 3, 4, 6, 7, 8, 9, 15];

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

/// Serialize the policy content fields for signature computation. Must
/// match the server's `marshalBundleContent` byte-for-byte; the startup
/// check proves the field set agrees with the proto's
/// `(vigo.v1.signed)` annotations, and the prost-build `btree_map`
/// config on every proto map field gives both sides the same
/// alphabetical wire order.
fn marshal_bundle_content(bundle: &PolicyBundle) -> Vec<u8> {
    let content = PolicyBundle {
        policy_version: bundle.policy_version,          // tag 1
        configcrates: bundle.configcrates.clone(),                // tag 2
        vars_json: bundle.vars_json.clone(),            // tag 3
        var_order: bundle.var_order.clone(),            // tag 4
        custom_traits: bundle.custom_traits.clone(),    // tag 6
        matched_pattern: bundle.matched_pattern.clone(), // tag 7
        environment: bundle.environment.clone(),        // tag 8
        roles: bundle.roles.clone(),                    // tag 9
        merkle_root: bundle.merkle_root.clone(),        // tag 15
        // Non-signed fields zeroed — see SIGNED_FIELD_TAGS above and
        // the matching `[(vigo.v1.signed) = false]` annotations in
        // protobuf/vigo.proto.
        signature: vec![],
        server_pubkey_id: String::new(),
        max_age_seconds: 0,
        stream_edit_allowed_paths: vec![],
        stream_edit_timeout_secs: 0,
        secrets_epoch: 0,
    };
    content.encode_to_vec()
}

/// File descriptor set emitted by `agent/build.rs` — contains every
/// proto declared in `protobuf/`. Used by the startup drift-check to
/// read the `(vigo.v1.signed)` field option without an external
/// schema file.
const DESCRIPTOR_BYTES: &[u8] = include_bytes!(concat!(env!("OUT_DIR"), "/vigo_descriptor.bin"));

fn descriptor_pool() -> &'static DescriptorPool {
    static POOL: OnceLock<DescriptorPool> = OnceLock::new();
    POOL.get_or_init(|| {
        DescriptorPool::decode(DESCRIPTOR_BYTES)
            .expect("embedded vigo descriptor set is malformed — build.rs broken")
    })
}

/// Assert at startup that the SIGNED_FIELD_TAGS constant matches the
/// `(vigo.v1.signed) = false` annotations on `vigo.v1.PolicyBundle`.
/// Caller invokes this once during agent boot (see
/// `agent/src/main.rs`); it panics on drift so the operator can't ship
/// a binary whose hand-listed signed set has fallen out of sync with
/// the proto.
pub fn assert_signed_field_set_matches_proto() {
    let pool = descriptor_pool();
    let msg = pool
        .get_message_by_name("vigo.v1.PolicyBundle")
        .expect("vigo.v1.PolicyBundle missing from descriptor pool");

    let from_proto: HashSet<u32> = msg
        .fields()
        .filter(|f| is_signed_per_proto(f))
        .map(|f| f.number())
        .collect();
    let from_code: HashSet<u32> = SIGNED_FIELD_TAGS.iter().copied().collect();

    if from_proto != from_code {
        let only_proto: Vec<_> = from_proto.difference(&from_code).copied().collect();
        let only_code: Vec<_> = from_code.difference(&from_proto).copied().collect();
        panic!(
            "PolicyBundle signed-field-set drift: proto annotations and \
             agent's SIGNED_FIELD_TAGS disagree. \
             In proto but not in code: {only_proto:?}. \
             In code but not in proto: {only_code:?}. \
             Update SIGNED_FIELD_TAGS + marshal_bundle_content in \
             agent/src/checkin/verify.rs to match \
             protobuf/vigo.proto's (vigo.v1.signed) annotations."
        );
    }
}

/// Read the `(vigo.v1.signed)` extension on a field descriptor. Default
/// is `true` (signed) when the annotation isn't present, matching the
/// proto's secure-by-default semantics — new fields ship signed unless
/// explicitly marked otherwise.
///
/// Note on prost-reflect quirk: `DynamicMessage::get_extension` returns
/// the proto3 *default* (`false` for a `bool`) when the extension isn't
/// set, which would collapse "unset" and "set to false" into the same
/// answer. `has_extension` is the SoT for "is this annotation actually
/// present" — check it first and only then read the value.
fn is_signed_per_proto(field: &prost_reflect::FieldDescriptor) -> bool {
    let pool = descriptor_pool();
    let ext = match pool.get_extension_by_name("vigo.v1.signed") {
        Some(e) => e,
        // Extension itself missing from the pool — should be impossible
        // since vigo_options.proto is compiled in. Fail closed.
        None => panic!("vigo.v1.signed extension missing from descriptor pool"),
    };
    let options = field.options();
    if !options.has_extension(&ext) {
        // No annotation on this field → secure-by-default: signed.
        return true;
    }
    match options.get_extension(&ext).as_ref() {
        Value::Bool(b) => *b,
        // Wrong type would be a proto bug; treat as signed to fail closed.
        _ => true,
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::proto::PolicyConfigcrate;

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
            configcrates: vec![PolicyConfigcrate {
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

    /// The drift check itself. Runs the same code the agent runs at
    /// boot; CI catches a stale SIGNED_FIELD_TAGS before it ever ships.
    #[test]
    fn signed_field_set_matches_proto_annotations() {
        assert_signed_field_set_matches_proto();
    }

    /// Excluded fields must not affect the signed bytes: setting each
    /// non-signed field to a non-default value should produce the
    /// exact same marshal_bundle_content output as a fresh bundle.
    /// This is the contract `(vigo.v1.signed) = false` declares; if
    /// marshal_bundle_content stops zeroing one of these fields, the
    /// signature would suddenly cover it and this test catches it.
    #[test]
    fn non_signed_fields_do_not_affect_signed_bytes() {
        let baseline = PolicyBundle {
            policy_version: 7,
            configcrates: vec![PolicyConfigcrate {
                name: "m".into(),
                content_hash: "h".into(),
                ..Default::default()
            }],
            merkle_root: "r".into(),
            ..Default::default()
        };
        let baseline_bytes = marshal_bundle_content(&baseline);

        let tampered = PolicyBundle {
            signature: vec![0xff; 64],
            server_pubkey_id: "attacker".into(),
            max_age_seconds: 9999,
            stream_edit_allowed_paths: vec!["/evil".into()],
            stream_edit_timeout_secs: 999,
            secrets_epoch: 9999,
            ..baseline
        };
        let tampered_bytes = marshal_bundle_content(&tampered);

        assert_eq!(
            baseline_bytes, tampered_bytes,
            "marshal_bundle_content must ignore every (vigo.v1.signed)=false field"
        );
    }
}
