// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

//! Shared handle for claim announcements pending multicast emission.
//!
//! The swarmgit reactor owns the authoritative view of "for each
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

use std::collections::HashMap;
use std::sync::RwLock;

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
    /// reactor calls this after running the LAN selector.
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

    /// Drop every label. Called when the swarmgit gate flips off so
    /// the announcer stops emitting stale SWARMGITCLAIM lines for a
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
/// Shape: `SWARMGITCLAIM:<label>:<host1,host2,host3>`
///
/// Uses a single colon after the `SWARMGITCLAIM:` prefix to
/// separate the label from the host list. Labels themselves
/// contain colons (`swarmgit:<seeder>:<user>:<project>`), so the
/// parser must use the LAST colon as the separator, not the
/// first. Labels also don't contain commas; hostnames don't
/// contain colons or commas by convention; that's what makes this
/// compact shape unambiguous.
pub fn format_claim_line(label: &str, hosts: &[String]) -> String {
    format!("SWARMGITCLAIM:{}:{}", label, hosts.join(","))
}

/// Parse a `SWARMGITCLAIM:` line back into its (label, hosts)
/// components. Returns `None` if the line is malformed — the
/// listener must not crash on bad input.
pub fn parse_claim_line(line: &str) -> Option<(String, Vec<String>)> {
    let rest = line.strip_prefix("SWARMGITCLAIM:")?;
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

    // --- clear (gate off→on/off state management) ---

    #[test]
    fn clear_drops_all_labels() {
        // When the swarmgit gate flips off the reactor calls
        // clear() so the announcer stops emitting stale
        // SWARMGITCLAIM lines. Verify the map is actually empty
        // after clear and that it's usable on re-publish.
        let pc = PendingClaims::new();
        pc.publish("swarmgit:danlap:dan:vigo", vec!["a".into(), "b".into()]);
        pc.publish("swarmgit:danlap:dan:web", vec!["c".into()]);
        assert_eq!(pc.len(), 2);
        pc.clear();
        assert!(pc.is_empty());
        pc.publish("swarmgit:danlap:dan:vigo", vec!["x".into()]);
        assert_eq!(pc.len(), 1);
    }

    // --- PendingClaims ---

    #[test]
    fn pending_claims_publish_and_snapshot_round_trip() {
        let p = PendingClaims::new();
        p.publish("swarmgit:h:u:a", vec!["peer1".into(), "peer2".into()]);
        p.publish("swarmgit:h:u:b", vec!["peer3".into()]);
        let snap = p.snapshot();
        assert_eq!(snap.len(), 2);
        // Sorted by label.
        assert_eq!(snap[0].0, "swarmgit:h:u:a");
        assert_eq!(snap[0].1, vec!["peer1".to_string(), "peer2".to_string()]);
        assert_eq!(snap[1].0, "swarmgit:h:u:b");
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
            format_claim_line("swarmgit:danlap:dan:vigo-web", &["plex".into(), "annlap".into()]);
        assert_eq!(line, "SWARMGITCLAIM:swarmgit:danlap:dan:vigo-web:plex,annlap");
    }

    #[test]
    fn parse_claim_line_round_trips_format() {
        let label = "swarmgit:danlap:dan:vigo-web";
        let hosts = vec!["plex.home.arpa".to_string(), "annlap.home.arpa".to_string()];
        let line = format_claim_line(label, &hosts);
        let (parsed_label, parsed_hosts) = parse_claim_line(&line).unwrap();
        assert_eq!(parsed_label, label);
        assert_eq!(parsed_hosts, hosts);
    }

    #[test]
    fn parse_claim_line_single_host() {
        let line = "SWARMGITCLAIM:swarmgit:h:u:p:only-host";
        let (label, hosts) = parse_claim_line(line).unwrap();
        assert_eq!(label, "swarmgit:h:u:p");
        assert_eq!(hosts, vec!["only-host".to_string()]);
    }

    #[test]
    fn parse_claim_line_empty_host_list() {
        // A source with zero eligible peers still broadcasts the
        // claim so receivers that previously thought they were
        // in the list can learn they've been removed.
        let line = "SWARMGITCLAIM:swarmgit:h:u:p:";
        let (label, hosts) = parse_claim_line(line).unwrap();
        assert_eq!(label, "swarmgit:h:u:p");
        assert!(hosts.is_empty());
    }

    #[test]
    fn parse_claim_line_rejects_missing_prefix() {
        assert!(parse_claim_line("swarmgit:h:u:p:h1").is_none());
    }

    #[test]
    fn parse_claim_line_rejects_empty_label() {
        assert!(parse_claim_line("SWARMGITCLAIM::h1").is_none());
    }

    #[test]
    fn parse_claim_line_rejects_no_colon() {
        assert!(parse_claim_line("SWARMGITCLAIM:nothing-after").is_none());
    }
}
