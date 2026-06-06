// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

pub mod backoff;
pub mod client;
pub mod loop_impl;
pub mod configcratecache;
pub mod stream;
pub mod traceparent;
pub mod verify;

use crate::proto::{CheckInResponse, PolicyBundle};
use prost::Message;
use std::io::Read;
use tracing::warn;

/// Inflate a gzip-compressed policy bundle back into the structured field.
///
/// The server ships large bundles in `policy_bundle_gzip` to save wire bytes
/// (see `compressBundle` in server/grpc/agentstream.go); decompressing at every
/// reception point keeps all downstream code bundle-shaped (it only ever reads
/// `policy_bundle`). No-op when the compressed field is empty (small bundle or
/// no_change response). On decode failure the bundle is left absent and a
/// warning logged — the agent then converges offline against its cached bundle,
/// the same as any other missing-bundle cycle.
pub(crate) fn inflate_policy_bundle(resp: &mut CheckInResponse) {
    if resp.policy_bundle_gzip.is_empty() {
        return;
    }
    let mut decoder = flate2::read::GzDecoder::new(resp.policy_bundle_gzip.as_slice());
    let mut raw = Vec::new();
    match decoder.read_to_end(&mut raw) {
        Ok(_) => match PolicyBundle::decode(raw.as_slice()) {
            Ok(bundle) => resp.policy_bundle = Some(bundle),
            Err(e) => warn!("failed to decode inflated policy bundle: {e}"),
        },
        Err(e) => warn!("failed to inflate compressed policy bundle: {e}"),
    }
    resp.policy_bundle_gzip = Vec::new();
}

#[cfg(test)]
mod tests {
    use super::*;
    use flate2::write::GzEncoder;
    use flate2::Compression;
    use std::io::Write;

    fn gzip(bytes: &[u8]) -> Vec<u8> {
        let mut enc = GzEncoder::new(Vec::new(), Compression::default());
        enc.write_all(bytes).unwrap();
        enc.finish().unwrap()
    }

    #[test]
    fn inflates_compressed_bundle_into_structured_field() {
        let bundle = PolicyBundle {
            policy_version: 42,
            merkle_root: "abc".into(),
            ..Default::default()
        };
        let mut resp = CheckInResponse {
            policy_bundle_gzip: gzip(&bundle.encode_to_vec()),
            ..Default::default()
        };
        inflate_policy_bundle(&mut resp);
        assert_eq!(
            resp.policy_bundle.as_ref().map(|b| b.policy_version),
            Some(42)
        );
        assert!(
            resp.policy_bundle_gzip.is_empty(),
            "gzip field must be cleared after inflation"
        );
    }

    #[test]
    fn noop_when_no_compressed_field() {
        let mut resp = CheckInResponse::default();
        inflate_policy_bundle(&mut resp);
        assert!(resp.policy_bundle.is_none());
    }

    #[test]
    fn structured_bundle_passes_through() {
        // Small bundles arrive in policy_bundle directly; inflate is a no-op.
        let mut resp = CheckInResponse {
            policy_bundle: Some(PolicyBundle {
                policy_version: 5,
                ..Default::default()
            }),
            ..Default::default()
        };
        inflate_policy_bundle(&mut resp);
        assert_eq!(resp.policy_bundle.unwrap().policy_version, 5);
    }

    #[test]
    fn malformed_gzip_is_safe() {
        let mut resp = CheckInResponse {
            policy_bundle_gzip: vec![0xde, 0xad, 0xbe, 0xef],
            ..Default::default()
        };
        inflate_policy_bundle(&mut resp);
        assert!(resp.policy_bundle.is_none());
        assert!(resp.policy_bundle_gzip.is_empty());
    }
}
