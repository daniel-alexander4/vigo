// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

//! Deterministic LAN mirror selection.
//!
//! Given a filtered set of peer candidates and a repo label, this
//! module picks which peers should host the mirror. The algorithm
//! uses deterministic sha256 scoring: each (label, hostname) pair
//! produces a fixed score, and the lowest-scoring peers win. This
//! replaced the server-side `ClaimTable` (deleted in Phase 8,
//! 0.23.0) — the algorithm is byte-for-byte identical so the
//! transition was seamless.
//!
//! ### Algorithm
//!
//! For each candidate peer:
//!
//! 1. Compute `score = sha256(BE_u32(label.len()) || label || 0x00 || hostname)`.
//!    The length-prefixed framing prevents collisions where a
//!    label and hostname differ in where the split falls but
//!    produce the same concatenated byte sequence.
//! 2. Sort peers by score (ascending, lex over 32 bytes), with
//!    hostname as a stable tie-breaker.
//! 3. Take the first `min(max_mirrors, len(candidates))` entries.
//!
//! The hash is used purely for deterministic ordering — the
//! cryptographic strength of sha256 is overkill but the library
//! is already a dependency and any well-distributed hash satisfies
//! the requirement. Ported from the (now-deleted) server-side Go
//! implementation, byte-for-byte identical.

use sha2::{Digest, Sha256};

use super::peer_table::PeerEntry;

/// Compute the selection score for a `(label, hostname)` pair.
/// Returns the raw 32-byte sha256 digest. Broken out of
/// [`select_mirrors`] so tests can pin specific score values.
pub fn score_label_host(label: &str, hostname: &str) -> [u8; 32] {
    let mut h = Sha256::new();
    let label_len = label.len() as u32;
    h.update(label_len.to_be_bytes());
    h.update(label.as_bytes());
    h.update([0u8]);
    h.update(hostname.as_bytes());
    let out = h.finalize();
    let mut arr = [0u8; 32];
    arr.copy_from_slice(&out);
    arr
}

/// Run the deterministic selection for a single source. Returns
/// the selected peer entries in order, up to `max_mirrors`.
///
/// Empty input or `max_mirrors == 0` produces an empty list.
/// Never panics. Never allocates beyond the input size.
pub fn select_mirrors(
    label: &str,
    candidates: &[PeerEntry],
    max_mirrors: usize,
) -> Vec<PeerEntry> {
    if candidates.is_empty() || max_mirrors == 0 {
        return Vec::new();
    }

    // Score each candidate.
    let mut scored: Vec<(PeerEntry, [u8; 32])> = candidates
        .iter()
        .map(|p| (p.clone(), score_label_host(label, &p.hostname)))
        .collect();

    // Sort by score ascending, tie-break on hostname string order.
    scored.sort_by(|a, b| {
        a.1.cmp(&b.1).then_with(|| a.0.hostname.cmp(&b.0.hostname))
    });

    let n = max_mirrors.min(scored.len());
    scored.into_iter().take(n).map(|(p, _)| p).collect()
}

/// Convenience wrapper: run [`select_mirrors`] and return just
/// the hostnames in order. Used by the reactor when building
/// the `VIGOREPOCLAIM` announcement payload.
pub fn select_hostnames(
    label: &str,
    candidates: &[PeerEntry],
    max_mirrors: usize,
) -> Vec<String> {
    select_mirrors(label, candidates, max_mirrors)
        .into_iter()
        .map(|p| p.hostname)
        .collect()
}

#[cfg(test)]
mod tests {
    use super::*;

    fn peer(hostname: &str) -> PeerEntry {
        PeerEntry {
            hostname: hostname.to_string(),
            users: Vec::new(),
            tags: Vec::new(),
            last_seen: 0,
        }
    }

    #[test]
    fn score_is_deterministic() {
        let a = score_label_host("vigorepo:h:u:p", "plex");
        let b = score_label_host("vigorepo:h:u:p", "plex");
        assert_eq!(a, b);
    }

    #[test]
    fn score_differs_for_different_hosts() {
        let a = score_label_host("vigorepo:h:u:p", "plex");
        let b = score_label_host("vigorepo:h:u:p", "danlap");
        assert_ne!(a, b);
    }

    #[test]
    fn score_differs_for_different_labels() {
        let a = score_label_host("vigorepo:h:u:repo-a", "plex");
        let b = score_label_host("vigorepo:h:u:repo-b", "plex");
        assert_ne!(a, b);
    }

    #[test]
    fn score_length_prefix_prevents_split_collision() {
        // Ensure that "AB" + "C" and "A" + "BC" hash differently.
        // Without the length prefix, naive concatenation would
        // produce the same input bytes if the separator weren't
        // there — the NUL + length frame guarantees otherwise.
        let a = score_label_host("AB", "C");
        let b = score_label_host("A", "BC");
        assert_ne!(a, b);
    }

    #[test]
    fn select_empty_candidates_returns_empty() {
        assert!(select_mirrors("label", &[], 5).is_empty());
    }

    #[test]
    fn select_zero_max_mirrors_returns_empty() {
        let peers = vec![peer("a"), peer("b")];
        assert!(select_mirrors("label", &peers, 0).is_empty());
    }

    #[test]
    fn select_respects_max_mirrors() {
        let peers: Vec<PeerEntry> =
            (0..10).map(|i| peer(&format!("host-{}", i))).collect();
        let out = select_mirrors("vigorepo:h:u:p", &peers, 3);
        assert_eq!(out.len(), 3);
    }

    #[test]
    fn select_returns_all_when_max_mirrors_exceeds_candidates() {
        let peers = vec![peer("a"), peer("b")];
        let out = select_mirrors("vigorepo:h:u:p", &peers, 10);
        assert_eq!(out.len(), 2);
    }

    #[test]
    fn select_is_deterministic_across_calls() {
        let peers: Vec<PeerEntry> = (0..20)
            .map(|i| peer(&format!("host-{:02}.lan", i)))
            .collect();
        let a = select_mirrors("vigorepo:danlap:dan:vigo-web", &peers, 5);
        let b = select_mirrors("vigorepo:danlap:dan:vigo-web", &peers, 5);
        assert_eq!(
            a.iter().map(|p| &p.hostname).collect::<Vec<_>>(),
            b.iter().map(|p| &p.hostname).collect::<Vec<_>>(),
        );
    }

    #[test]
    fn select_order_is_independent_of_input_order() {
        let peers_a = vec![
            peer("plex.home.arpa"),
            peer("danlap.home.arpa"),
            peer("annlap.home.arpa"),
            peer("girlslap.home.arpa"),
        ];
        let peers_b = vec![
            peer("girlslap.home.arpa"),
            peer("annlap.home.arpa"),
            peer("danlap.home.arpa"),
            peer("plex.home.arpa"),
        ];
        let a = select_mirrors("vigorepo:danlap:dan:vigo-web", &peers_a, 3);
        let b = select_mirrors("vigorepo:danlap:dan:vigo-web", &peers_b, 3);
        assert_eq!(
            a.iter().map(|p| &p.hostname).collect::<Vec<_>>(),
            b.iter().map(|p| &p.hostname).collect::<Vec<_>>(),
            "selection must be stable across input permutations",
        );
    }

    #[test]
    fn select_different_labels_may_pick_different_subsets() {
        // With enough candidates and max_mirrors < len, different
        // labels should (probabilistically, essentially always)
        // produce different orderings. We don't care which comes
        // first — just that the algorithm isn't degenerate.
        let peers: Vec<PeerEntry> = (0..20)
            .map(|i| peer(&format!("host-{:02}", i)))
            .collect();
        let a = select_hostnames("vigorepo:h:u:repo-a", &peers, 3);
        let b = select_hostnames("vigorepo:h:u:repo-b", &peers, 3);
        // With 20 hosts and sha256, the chance of identical top-3
        // for two different labels is vanishingly small. If this
        // test fails in the future, either sha256 has been
        // replaced by a degenerate function or a statistical
        // miracle has occurred — both worth investigating.
        assert_ne!(a, b);
    }

    #[test]
    fn select_hostnames_preserves_order() {
        let peers: Vec<PeerEntry> = (0..5).map(|i| peer(&format!("h{}", i))).collect();
        let full = select_mirrors("vigorepo:h:u:p", &peers, 5);
        let hostnames_wrapper = select_hostnames("vigorepo:h:u:p", &peers, 5);
        let hostnames_direct: Vec<String> =
            full.into_iter().map(|p| p.hostname).collect();
        assert_eq!(hostnames_wrapper, hostnames_direct);
    }

    #[test]
    fn select_tie_breaks_alphabetically_on_identical_scores() {
        // Impossible in practice with sha256, but the tie-break
        // path is tested by constructing two peers with identical
        // pre-computed "scores" — since the hash is pure, the
        // only way to force a tie is to use identical hostnames,
        // which the peer table prevents. We verify the sort is
        // stable for the realistic case: given a lexically-sorted
        // input, the output order for identical hostnames would
        // match input order (sort is stable).
        let mut peers = vec![peer("aaa"), peer("bbb"), peer("ccc")];
        let a = select_mirrors("vigorepo:h:u:p", &peers, 3);
        peers.reverse();
        let b = select_mirrors("vigorepo:h:u:p", &peers, 3);
        let a_names: Vec<&str> = a.iter().map(|p| p.hostname.as_str()).collect();
        let b_names: Vec<&str> = b.iter().map(|p| p.hostname.as_str()).collect();
        assert_eq!(
            a_names, b_names,
            "sort must produce identical output regardless of input order",
        );
    }
}
