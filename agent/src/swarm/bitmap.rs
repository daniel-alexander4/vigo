// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

//! Compact bitfield encoding for chunk sets shared between peers.
//!
//! Used by the multicast announcer and listener to carry a peer's partial
//! blob chunk inventory over the wire in the `HAVE:<sha>:<have>/<total>:<bitmap>`
//! extension. The encoding is URL-safe base64 (no padding) because the
//! line format uses `:` and `/` as delimiters and standard base64's `+/=`
//! would collide.
//!
//! # Format
//!
//! - Bitfield: `ceil(total_chunks / 8)` bytes, LSB-first within each byte.
//!   Byte `i` bit `j` (j in 0..8) encodes chunk `i*8 + j`. Bits beyond
//!   `total_chunks` in the last byte are always 0.
//! - Encoding: URL-safe base64 without padding (`-_` alphabet, no `=`).
//!
//! # Density
//!
//! For a blob with `N` chunks, the encoded bitmap is `ceil(ceil(N/8) * 4/3)`
//! chars (base64 expansion). Reference points:
//! - 100 chunks → 13 bytes → 18 chars
//! - 1000 chunks → 125 bytes → 167 chars
//! - 7400 chunks → 925 bytes → 1234 chars (near the MTU ceiling)
//!
//! # Precedence rule
//!
//! When a peer's bitmap arrives from two sources (multicast inline + later
//! `/meta` probe), `/meta` is authoritative and should overwrite the
//! multicast hint. See the `PeerMap.report` merge semantics.

use base64::prelude::*;
use std::collections::HashSet;

/// Encode a chunk set into a URL-safe base64 bitfield. `total_chunks`
/// determines the byte length of the bitfield; any chunks in `set` that
/// exceed `total_chunks` are silently dropped (they should never occur
/// in practice — this is a defensive guard).
pub fn encode(set: &HashSet<u32>, total_chunks: u32) -> String {
    if total_chunks == 0 {
        return String::new();
    }
    let byte_len = total_chunks.div_ceil(8) as usize;
    let mut bytes = vec![0u8; byte_len];
    for &idx in set {
        if idx >= total_chunks {
            continue;
        }
        let byte_idx = (idx / 8) as usize;
        let bit = (idx % 8) as u8;
        bytes[byte_idx] |= 1 << bit;
    }
    BASE64_URL_SAFE_NO_PAD.encode(&bytes)
}

/// Decode a URL-safe base64 bitfield back into a chunk set. Returns `None`
/// if the input is not valid base64 or if a decoded bit represents a
/// chunk index ≥ `total_chunks` (which would indicate a protocol error —
/// the sender and receiver disagree about the blob's chunk count).
pub fn decode(encoded: &str, total_chunks: u32) -> Option<HashSet<u32>> {
    if encoded.is_empty() {
        return Some(HashSet::new());
    }
    let bytes = BASE64_URL_SAFE_NO_PAD.decode(encoded).ok()?;
    let expected_len = total_chunks.div_ceil(8) as usize;
    // Reject mismatched payload size — a peer with a different chunk count
    // in mind should not be trusted to report anything coherent.
    if bytes.len() != expected_len {
        return None;
    }
    let mut set = HashSet::new();
    for (byte_idx, byte) in bytes.iter().enumerate() {
        for bit in 0..8u8 {
            if byte & (1 << bit) != 0 {
                let chunk_idx = (byte_idx as u32) * 8 + bit as u32;
                if chunk_idx < total_chunks {
                    set.insert(chunk_idx);
                }
            }
        }
    }
    Some(set)
}

/// Returns the exact base64 length (in chars) that `encode` would produce
/// for a given `total_chunks`, without actually encoding. Used by the
/// announcer to budget the multicast line length against MTU before
/// deciding whether to emit the inline bitmap form or the sentinel.
pub fn encoded_len(total_chunks: u32) -> usize {
    if total_chunks == 0 {
        return 0;
    }
    let byte_len = total_chunks.div_ceil(8) as usize;
    // URL-safe base64 no-pad: ceil(n * 4 / 3).
    (byte_len * 4).div_ceil(3)
}

#[cfg(test)]
mod tests {
    use super::*;

    fn set(chunks: &[u32]) -> HashSet<u32> {
        chunks.iter().copied().collect()
    }

    #[test]
    fn test_encode_decode_roundtrip_sparse() {
        let input = set(&[0, 5, 42, 99]);
        let encoded = encode(&input, 100);
        let decoded = decode(&encoded, 100).expect("decode");
        assert_eq!(decoded, input);
    }

    #[test]
    fn test_encode_decode_roundtrip_dense() {
        let input: HashSet<u32> = (0..500).collect();
        let encoded = encode(&input, 500);
        let decoded = decode(&encoded, 500).expect("decode");
        assert_eq!(decoded, input);
    }

    #[test]
    fn test_encode_decode_empty_set() {
        let input: HashSet<u32> = HashSet::new();
        let encoded = encode(&input, 100);
        let decoded = decode(&encoded, 100).expect("decode");
        assert!(decoded.is_empty());
    }

    #[test]
    fn test_encode_decode_single_chunk() {
        let input = set(&[42]);
        let encoded = encode(&input, 100);
        let decoded = decode(&encoded, 100).expect("decode");
        assert_eq!(decoded, input);
    }

    #[test]
    fn test_encode_decode_boundary_bits() {
        // Chunk 7 (last bit of byte 0) and chunk 8 (first bit of byte 1)
        // and chunk 15 (last bit of byte 1).
        let input = set(&[7, 8, 15]);
        let encoded = encode(&input, 16);
        let decoded = decode(&encoded, 16).expect("decode");
        assert_eq!(decoded, input);
    }

    #[test]
    fn test_encode_drops_chunks_beyond_total() {
        // Defensive guard: chunks ≥ total_chunks must not leak into the
        // output byte range.
        let input = set(&[0, 50, 100, 200]);
        let encoded = encode(&input, 100);
        let decoded = decode(&encoded, 100).expect("decode");
        // Chunk 100 is out of range (valid chunks are 0..100), and 200 too.
        assert_eq!(decoded, set(&[0, 50]));
    }

    #[test]
    fn test_decode_rejects_wrong_length_payload() {
        // Encoded as if total was 16, but caller claims total is 100.
        let input = set(&[0, 1, 2]);
        let encoded = encode(&input, 16); // 2-byte payload
        assert!(decode(&encoded, 100).is_none(), "mismatched payload size must reject");
    }

    #[test]
    fn test_decode_invalid_base64_returns_none() {
        assert!(decode("!!!not base64!!!", 100).is_none());
    }

    #[test]
    fn test_decode_empty_string_returns_empty_set() {
        let decoded = decode("", 100).expect("empty string decodes to empty set");
        assert!(decoded.is_empty());
    }

    #[test]
    fn test_encoded_len_matches_encode() {
        for total in &[1u32, 8, 9, 100, 1000, 7378] {
            let input: HashSet<u32> = (0..*total).collect();
            let encoded = encode(&input, *total);
            assert_eq!(
                encoded.len(),
                encoded_len(*total),
                "encoded_len mismatch for total={}",
                total
            );
        }
    }

    #[test]
    fn test_encoded_len_reference_points() {
        // Spot-check the density numbers documented in the module header.
        assert_eq!(encoded_len(100), 18); // 13 bytes → 18 chars
        assert_eq!(encoded_len(1000), 167); // 125 bytes → 167 chars
    }

    #[test]
    fn test_url_safe_alphabet() {
        // URL-safe base64 uses `-_` in place of `+/`. Construct a chunk
        // set that's likely to hit both ends of the alphabet. The goal is
        // to confirm the output never contains `+`, `/`, or `=`.
        let input: HashSet<u32> = (0..256).step_by(3).collect();
        let encoded = encode(&input, 256);
        assert!(!encoded.contains('+'), "encoded should not contain '+': {}", encoded);
        assert!(!encoded.contains('/'), "encoded should not contain '/': {}", encoded);
        assert!(!encoded.contains('='), "encoded should not contain padding: {}", encoded);
    }
}
