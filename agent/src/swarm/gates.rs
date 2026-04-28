// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

//! Shared gate-evaluation helper for the four swarm content subsystems
//! (payload, gitmirror, cryptsync, filesync).
//!
//! Every subsystem reads three bits from `PolicyBundle.vars_json`,
//! cached in the LMDB state store on each accepted check-in:
//!
//! - `swarm_enabled` — global kill switch. False dominates everything.
//! - `swarm_<substrate>_enabled` — per-substrate kill switch.
//! - `<substrate>_blacklisted` — per-envoy denylist hit.
//!
//! "Active" means: `swarm_enabled` AND `<substrate>_enabled` AND NOT
//! `<substrate>_blacklisted`. All three default-on (a missing or
//! ambiguous bit is treated as "not setting OFF") so a fresh-enrolled
//! envoy with no policy bundle yet runs every subsystem until the
//! server says otherwise. Filesync has no `<substrate>_blacklisted` —
//! it gets the global + per-substrate flags only.
//!
//! Centralizing the AND here means every consumer (multicast
//! announcer, peer-server route handler, reactor tick) goes through
//! one function and can't accidentally honor only its per-substrate
//! flag while ignoring the global kill.
//!
//! This module is a *helper* — the per-substrate `Gate` structs in
//! each subsystem still hold the `Arc<AtomicBool>` consumers consult
//! at their own sample points. The check-in loop's per-tick
//! `sync_*_gate` functions call into here to compute the new value
//! before flipping the gate.

use crate::state::StateStore;

/// Substrate identifier used to select the right per-substrate flag
/// names. Keeps the field-name lookup in one place so a typo in a
/// caller can't silently disable a subsystem.
#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub enum Substrate {
    Payload,
    Gitmirror,
    Cryptsync,
    Filesync,
}

impl Substrate {
    fn enabled_key(self) -> &'static str {
        match self {
            Substrate::Payload => "swarm_payload_enabled",
            Substrate::Gitmirror => "swarm_gitmirror_enabled",
            Substrate::Cryptsync => "swarm_cryptsync_enabled",
            Substrate::Filesync => "swarm_filesync_enabled",
        }
    }

    fn blacklist_key(self) -> Option<&'static str> {
        match self {
            Substrate::Payload => Some("payload_blacklisted"),
            Substrate::Gitmirror => Some("gitmirror_blacklisted"),
            Substrate::Cryptsync => Some("cryptsync_blacklisted"),
            // Filesync has no per-host blacklist by design.
            Substrate::Filesync => None,
        }
    }

    /// Human-readable name for log lines. Stable; do not change without
    /// updating any operator-facing diagnostics that match on it.
    pub fn name(self) -> &'static str {
        match self {
            Substrate::Payload => "payload",
            Substrate::Gitmirror => "gitmirror",
            Substrate::Cryptsync => "cryptsync",
            Substrate::Filesync => "filesync",
        }
    }
}

/// Compute whether `substrate` should be active for this envoy right
/// now. Reads from the cached `PolicyBundle.vars_json` if available.
/// Returns `true` (active) in every ambiguous case — fail-open is
/// the design (matches gitmirror's pre-existing semantics): a fresh
/// enrollment, an LMDB hiccup, or a parse failure all keep substrates
/// running until the server explicitly flips them off.
pub fn substrate_active(substrate: Substrate, state_store: Option<&StateStore>) -> bool {
    let Some(store) = state_store else {
        return true;
    };
    let rtxn = match store.read_txn() {
        Ok(r) => r,
        Err(_) => return true,
    };
    let bundle_bytes = match store.policy.load(&rtxn) {
        Ok(Some((_, b))) => b,
        Ok(None) => return true,
        Err(_) => return true,
    };
    if bundle_bytes.vars_json.is_empty() {
        return true;
    }
    let val: serde_json::Value = match serde_json::from_slice(&bundle_bytes.vars_json) {
        Ok(v) => v,
        Err(_) => return true,
    };

    if !is_truthy_or_absent(val.get("swarm_enabled")) {
        return false;
    }
    if !is_truthy_or_absent(val.get(substrate.enabled_key())) {
        return false;
    }
    if let Some(key) = substrate.blacklist_key() {
        if is_truthy(val.get(key)) {
            return false;
        }
    }
    true
}

/// Strict truthy check used for the `*_blacklisted` keys. Only an
/// explicit `true`/`"true"`/`"yes"`/`"1"`/non-zero number counts as
/// blacklisted; absence or unknown shapes are NOT blacklisted.
fn is_truthy(v: Option<&serde_json::Value>) -> bool {
    match v {
        Some(serde_json::Value::Bool(b)) => *b,
        Some(serde_json::Value::String(s)) => matches!(
            s.to_ascii_lowercase().as_str(),
            "true" | "yes" | "1"
        ),
        Some(serde_json::Value::Number(n)) => n.as_i64().map_or(false, |i| i != 0),
        _ => false,
    }
}

/// Permissive truthy check used for the `*_enabled` keys. Returns true
/// for an explicit truthy value AND for absence. Returns false ONLY
/// for an explicit falsy value (`false`, `"false"`, `"no"`, `"0"`,
/// number zero). This is the inverse of [`is_truthy`] for the
/// fail-open default — operators who don't set the flag get the
/// historical "subsystem on" behavior.
fn is_truthy_or_absent(v: Option<&serde_json::Value>) -> bool {
    match v {
        None | Some(serde_json::Value::Null) => true,
        Some(serde_json::Value::Bool(b)) => *b,
        Some(serde_json::Value::String(s)) => !matches!(
            s.to_ascii_lowercase().as_str(),
            "false" | "no" | "0" | ""
        ),
        Some(serde_json::Value::Number(n)) => n.as_i64().map_or(true, |i| i != 0),
        _ => true,
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    fn val(json: &str) -> serde_json::Value {
        serde_json::from_str(json).unwrap()
    }

    #[test]
    fn truthy_recognizes_canonical_shapes() {
        assert!(is_truthy(Some(&val("true"))));
        assert!(is_truthy(Some(&val("\"true\""))));
        assert!(is_truthy(Some(&val("\"YES\""))));
        assert!(is_truthy(Some(&val("\"1\""))));
        assert!(is_truthy(Some(&val("1"))));
        assert!(is_truthy(Some(&val("42"))));
    }

    #[test]
    fn truthy_rejects_falsy_and_absent() {
        assert!(!is_truthy(None));
        assert!(!is_truthy(Some(&val("null"))));
        assert!(!is_truthy(Some(&val("false"))));
        assert!(!is_truthy(Some(&val("0"))));
        assert!(!is_truthy(Some(&val("\"no\""))));
        assert!(!is_truthy(Some(&val("\"\""))));
    }

    #[test]
    fn truthy_or_absent_treats_missing_as_on() {
        assert!(is_truthy_or_absent(None));
        assert!(is_truthy_or_absent(Some(&val("null"))));
        assert!(is_truthy_or_absent(Some(&val("true"))));
        assert!(is_truthy_or_absent(Some(&val("\"true\""))));
    }

    #[test]
    fn truthy_or_absent_rejects_explicit_falsy() {
        assert!(!is_truthy_or_absent(Some(&val("false"))));
        assert!(!is_truthy_or_absent(Some(&val("\"false\""))));
        assert!(!is_truthy_or_absent(Some(&val("\"no\""))));
        assert!(!is_truthy_or_absent(Some(&val("\"0\""))));
        assert!(!is_truthy_or_absent(Some(&val("0"))));
    }

    #[test]
    fn substrate_keys_are_distinct() {
        let subs = [
            Substrate::Payload,
            Substrate::Gitmirror,
            Substrate::Cryptsync,
            Substrate::Filesync,
        ];
        for (i, a) in subs.iter().enumerate() {
            for b in &subs[i + 1..] {
                assert_ne!(a.enabled_key(), b.enabled_key());
                assert_ne!(a.name(), b.name());
            }
        }
    }

    #[test]
    fn filesync_has_no_blacklist_key() {
        assert!(Substrate::Filesync.blacklist_key().is_none());
    }
}
