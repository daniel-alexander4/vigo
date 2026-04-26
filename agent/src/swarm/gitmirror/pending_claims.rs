// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

//! Shared handle for claim announcements pending multicast emission.
//!
//! The gitmirror reactor owns the authoritative view of "for each
//! non-paused source, which peers should hold the mirror right
//! now". It recomputes this on every tick by filtering the LAN
//! peer table and running the deterministic selector. The swarm
//! multicast announcer owns the wire — when its timer fires it
//! builds an announcement frame and sends it.
//!
//! These two subsystems run on independent schedules (reactor tick
//! is 5s, swarm announce cadence is 1-5s) and live in different
//! modules. Rather than couple them through a channel (lossy) or
//! a direct call (cross-module dep), they share this little
//! in-memory map: the reactor publishes the "current claim list
//! per label" and the announcer reads a snapshot on every emit.
//!
//! ### Rate limiting
//!
//! The reactor decides when to update [`PendingClaims`]. Per the
//! P2P-first plan the claim announcement is rate-limited to
//! *changes* (re-emit on every tick would produce roughly one
//! GITMIRRORCLAIM per source per second during active periods),
//! plus a safety-net re-broadcast every
//! [`RE_ANNOUNCE_INTERVAL_SECS`] so a late-joining receiver
//! doesn't wait out the LAN claim TTL to see the current list.
//!
//! Rate limiting lives in the reactor (which owns the "last
//! announced claim list per label" dedup state), not in this
//! module — [`PendingClaims`] is a pure shared cache.

use std::collections::HashMap;
use std::sync::RwLock;

/// Safety-net re-broadcast window for GITMIRRORCLAIM announcements.
/// Even with no change to the selected peer list, the reactor
/// forces a re-emit after this many seconds so a receiver that
/// joined the LAN mid-stream or missed an earlier multicast
/// catches up before its ingested LAN claim TTL expires.
///
/// Set to **60 seconds** — shorter than `LAN_CLAIM_TTL_SECS = 90`
/// so there's one refresh cycle of headroom before a stale entry
/// would age out on the receiver.
pub const RE_ANNOUNCE_INTERVAL_SECS: u64 = 60;

/// One-shot decision produced by the reactor's rate-limit check.
/// Tells the caller whether a given label's claim list should be
/// published this tick, and if so what the current list is.
#[derive(Debug, PartialEq, Eq)]
pub enum EmitDecision {
    /// Don't emit this tick — nothing changed and the safety
    /// re-broadcast window hasn't elapsed.
    Skip,
    /// Emit the given claim list. The reactor should also update
    /// its `last_announced` bookkeeping with `now` so the next
    /// tick's rate-limit check is correct.
    Emit(Vec<String>),
}

/// Rate-limit decision logic, pulled out of the reactor tick so
/// it's easy to unit-test.
///
/// Returns [`EmitDecision::Emit`] if either:
/// - `new_list` differs from `prev_list` (content change), OR
/// - `now - last_announced_at >= RE_ANNOUNCE_INTERVAL_SECS` (safety net).
///
/// Otherwise returns [`EmitDecision::Skip`].
pub fn decide_emit(
    prev_list: Option<&[String]>,
    new_list: &[String],
    last_announced_at: Option<u64>,
    now: u64,
) -> EmitDecision {
    let changed = match prev_list {
        None => true,
        Some(p) => p != new_list,
    };
    if changed {
        return EmitDecision::Emit(new_list.to_vec());
    }
    let should_re_announce = match last_announced_at {
        None => true,
        Some(t) => now.saturating_sub(t) >= RE_ANNOUNCE_INTERVAL_SECS,
    };
    if should_re_announce {
        EmitDecision::Emit(new_list.to_vec())
    } else {
        EmitDecision::Skip
    }
}

/// Shared claim map. Reactor writes per-label, announcer reads a
/// snapshot. Cheap to clone (Arc internally).
#[derive(Debug, Default)]
pub struct PendingClaims {
    inner: RwLock<HashMap<String, Vec<String>>>,
}

impl PendingClaims {
    pub fn new() -> Self {
        Self::default()
    }

    /// Overwrite the pending claim list for a single label. The
    /// reactor calls this after running the LAN selector and
    /// consulting [`decide_emit`] — only when the decision was
    /// `Emit`.
    pub fn publish(&self, label: &str, hosts: Vec<String>) {
        let mut w = self.inner.write().unwrap_or_else(|e| e.into_inner());
        w.insert(label.to_string(), hosts);
    }

    /// Drop a label entirely — used when a source disappears
    /// (sidecar deleted, `paused: true`, or project directory
    /// removed) so receivers stop seeing its claim in future
    /// announcements.
    pub fn forget(&self, label: &str) {
        let mut w = self.inner.write().unwrap_or_else(|e| e.into_inner());
        w.remove(label);
    }

    /// Drop every label. Called when the gitmirror gate flips off so
    /// the announcer stops emitting stale GITMIRRORCLAIM lines for a
    /// subsystem that is no longer active. When the gate flips back
    /// on the reactor repopulates this map on its next pass.
    pub fn clear(&self) {
        let mut w = self.inner.write().unwrap_or_else(|e| e.into_inner());
        w.clear();
    }

    /// Snapshot every pending (label, hosts) pair. Called by the
    /// swarm announcer on each multicast send. Returns a sorted-
    /// by-label Vec so the on-wire ordering is deterministic,
    /// which makes inter-envoy replay logs easier to diff.
    pub fn snapshot(&self) -> Vec<(String, Vec<String>)> {
        let r = self.inner.read().unwrap_or_else(|e| e.into_inner());
        let mut out: Vec<(String, Vec<String>)> =
            r.iter().map(|(k, v)| (k.clone(), v.clone())).collect();
        out.sort_by(|a, b| a.0.cmp(&b.0));
        out
    }

    /// Number of labels currently staged for announcement.
    pub fn len(&self) -> usize {
        self.inner.read().unwrap_or_else(|e| e.into_inner()).len()
    }

    /// Returns true if no labels are currently pending.
    pub fn is_empty(&self) -> bool {
        self.len() == 0
    }
}

/// Format a claim announcement line for the swarm multicast frame.
///
/// Shape: `GITMIRRORCLAIM:<label>:<host1,host2,host3>`
///
/// Uses a single colon after the `GITMIRRORCLAIM:` prefix to
/// separate the label from the host list. Labels themselves
/// contain colons (`gitmirror:<seeder>:<user>:<project>`), so the
/// parser must use the LAST colon as the separator, not the
/// first. Labels also don't contain commas; hostnames don't
/// contain colons or commas by convention; that's what makes this
/// compact shape unambiguous.
pub fn format_claim_line(label: &str, hosts: &[String]) -> String {
    format!("GITMIRRORCLAIM:{}:{}", label, hosts.join(","))
}

/// Parse a `GITMIRRORCLAIM:` line back into its (label, hosts)
/// components. Returns `None` if the line is malformed — the
/// listener must not crash on bad input.
pub fn parse_claim_line(line: &str) -> Option<(String, Vec<String>)> {
    let rest = line.strip_prefix("GITMIRRORCLAIM:")?;
    // Labels contain colons, so we can't split on the first one.
    // The claim-list separator is the LAST colon on the line.
    let last_colon = rest.rfind(':')?;
    let label = &rest[..last_colon];
    let hosts_csv = &rest[last_colon + 1..];
    if label.is_empty() {
        return None;
    }
    let hosts: Vec<String> = hosts_csv
        .split(',')
        .map(|s| s.trim())
        .filter(|s| !s.is_empty())
        .map(|s| s.to_string())
        .collect();
    Some((label.to_string(), hosts))
}

#[cfg(test)]
mod tests {
    use super::*;

    // --- decide_emit ---

    // --- clear (gate off→on/off state management) ---

    #[test]
    fn clear_drops_all_labels() {
        // When the gitmirror gate flips off the reactor calls
        // clear() so the announcer stops emitting stale
        // GITMIRRORCLAIM lines. Verify the map is actually empty
        // after clear and that it's usable on re-publish.
        let pc = PendingClaims::new();
        pc.publish("gitmirror:danlap:dan:vigo", vec!["a".into(), "b".into()]);
        pc.publish("gitmirror:danlap:dan:web", vec!["c".into()]);
        assert_eq!(pc.len(), 2);
        pc.clear();
        assert!(pc.is_empty());
        pc.publish("gitmirror:danlap:dan:vigo", vec!["x".into()]);
        assert_eq!(pc.len(), 1);
    }

    // --- decide_emit ---

    #[test]
    fn decide_emit_fires_on_first_run() {
        let d = decide_emit(None, &["a".into()], None, 1000);
        assert_eq!(d, EmitDecision::Emit(vec!["a".into()]));
    }

    #[test]
    fn decide_emit_skips_when_unchanged_and_within_interval() {
        let prev = vec!["a".to_string(), "b".to_string()];
        let new = vec!["a".to_string(), "b".to_string()];
        let d = decide_emit(Some(&prev), &new, Some(1000), 1001);
        assert_eq!(d, EmitDecision::Skip);
    }

    #[test]
    fn decide_emit_fires_on_list_change() {
        let prev = vec!["a".to_string()];
        let new = vec!["a".to_string(), "b".to_string()];
        let d = decide_emit(Some(&prev), &new, Some(1000), 1001);
        assert_eq!(d, EmitDecision::Emit(new));
    }

    #[test]
    fn decide_emit_fires_on_order_change() {
        // Order matters: the selector produces a stable sort, so
        // a different order is a different fact about the
        // selection outcome.
        let prev = vec!["a".to_string(), "b".to_string()];
        let new = vec!["b".to_string(), "a".to_string()];
        let d = decide_emit(Some(&prev), &new, Some(1000), 1001);
        assert!(matches!(d, EmitDecision::Emit(_)));
    }

    #[test]
    fn decide_emit_fires_after_safety_interval() {
        let prev = vec!["a".to_string()];
        let new = vec!["a".to_string()];
        let d = decide_emit(
            Some(&prev),
            &new,
            Some(1000),
            1000 + RE_ANNOUNCE_INTERVAL_SECS,
        );
        assert_eq!(d, EmitDecision::Emit(new));
    }

    #[test]
    fn decide_emit_skips_just_before_safety_interval() {
        let prev = vec!["a".to_string()];
        let new = vec!["a".to_string()];
        let d = decide_emit(
            Some(&prev),
            &new,
            Some(1000),
            1000 + RE_ANNOUNCE_INTERVAL_SECS - 1,
        );
        assert_eq!(d, EmitDecision::Skip);
    }

    #[test]
    fn decide_emit_fires_on_empty_list_after_non_empty() {
        let prev = vec!["a".to_string()];
        let new: Vec<String> = Vec::new();
        let d = decide_emit(Some(&prev), &new, Some(1000), 1001);
        assert!(matches!(d, EmitDecision::Emit(_)));
    }

    // --- PendingClaims ---

    #[test]
    fn pending_claims_publish_and_snapshot_round_trip() {
        let p = PendingClaims::new();
        p.publish("gitmirror:h:u:a", vec!["peer1".into(), "peer2".into()]);
        p.publish("gitmirror:h:u:b", vec!["peer3".into()]);
        let snap = p.snapshot();
        assert_eq!(snap.len(), 2);
        // Sorted by label.
        assert_eq!(snap[0].0, "gitmirror:h:u:a");
        assert_eq!(snap[0].1, vec!["peer1".to_string(), "peer2".to_string()]);
        assert_eq!(snap[1].0, "gitmirror:h:u:b");
    }

    #[test]
    fn pending_claims_publish_overwrites_existing() {
        let p = PendingClaims::new();
        p.publish("label", vec!["old".into()]);
        p.publish("label", vec!["new".into()]);
        let snap = p.snapshot();
        assert_eq!(snap.len(), 1);
        assert_eq!(snap[0].1, vec!["new".to_string()]);
    }

    #[test]
    fn pending_claims_forget_removes_label() {
        let p = PendingClaims::new();
        p.publish("a", vec!["x".into()]);
        p.publish("b", vec!["y".into()]);
        p.forget("a");
        let snap = p.snapshot();
        assert_eq!(snap.len(), 1);
        assert_eq!(snap[0].0, "b");
    }

    #[test]
    fn pending_claims_forget_unknown_label_is_noop() {
        let p = PendingClaims::new();
        p.publish("a", vec!["x".into()]);
        p.forget("not-present");
        assert_eq!(p.len(), 1);
    }

    // --- format / parse ---

    #[test]
    fn format_claim_line_shape() {
        let line =
            format_claim_line("gitmirror:danlap:dan:vigo-web", &["plex".into(), "annlap".into()]);
        assert_eq!(line, "GITMIRRORCLAIM:gitmirror:danlap:dan:vigo-web:plex,annlap");
    }

    #[test]
    fn parse_claim_line_round_trips_format() {
        let label = "gitmirror:danlap:dan:vigo-web";
        let hosts = vec!["plex.home.arpa".to_string(), "annlap.home.arpa".to_string()];
        let line = format_claim_line(label, &hosts);
        let (parsed_label, parsed_hosts) = parse_claim_line(&line).unwrap();
        assert_eq!(parsed_label, label);
        assert_eq!(parsed_hosts, hosts);
    }

    #[test]
    fn parse_claim_line_single_host() {
        let line = "GITMIRRORCLAIM:gitmirror:h:u:p:only-host";
        let (label, hosts) = parse_claim_line(line).unwrap();
        assert_eq!(label, "gitmirror:h:u:p");
        assert_eq!(hosts, vec!["only-host".to_string()]);
    }

    #[test]
    fn parse_claim_line_empty_host_list() {
        // A source with zero eligible peers still broadcasts the
        // claim so receivers that previously thought they were
        // in the list can learn they've been removed.
        let line = "GITMIRRORCLAIM:gitmirror:h:u:p:";
        let (label, hosts) = parse_claim_line(line).unwrap();
        assert_eq!(label, "gitmirror:h:u:p");
        assert!(hosts.is_empty());
    }

    #[test]
    fn parse_claim_line_rejects_missing_prefix() {
        assert!(parse_claim_line("gitmirror:h:u:p:h1").is_none());
    }

    #[test]
    fn parse_claim_line_rejects_empty_label() {
        assert!(parse_claim_line("GITMIRRORCLAIM::h1").is_none());
    }

    #[test]
    fn parse_claim_line_rejects_no_colon() {
        assert!(parse_claim_line("GITMIRRORCLAIM:nothing-after").is_none());
    }
}
