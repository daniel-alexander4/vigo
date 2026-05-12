// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

//! Shared gate-evaluation helper for the four swarm content subsystems
//! (payload, swarmgit, lockbox, longdrawer).
//!
//! Every subsystem reads two bits from `PolicyBundle.vars_json`,
//! cached in the LMDB state store on each accepted check-in:
//!
//! - `swarm_enabled` — global kill switch. False dominates everything.
//! - `swarm_<substrate>_enabled` — per-substrate kill switch.
//!
//! "Active" means: `swarm_enabled` AND `swarm_<substrate>_enabled`.
//! Both default-on (a missing or ambiguous bit is treated as "not
//! setting OFF") so a fresh-enrolled envoy with no policy bundle yet
//! runs every subsystem until the server says otherwise.
//!
//! Lockbox's per-substrate key is `swarm_puddle_enabled` (ADR-020 +
//! 0.41.0): lockbox's authorization derives from puddle, single
//! source of truth. There is no `swarm_lockbox_enabled` key in
//! 0.41.0+; the server emits only `swarm_puddle_enabled`, and a host
//! denied puddle authorization cannot use lockbox.
//!
//! Per-host blacklists were retired in 0.41.0 in favor of negative
//! patterns inside the per-subsystem allow list (e.g.
//! `swarm.swarmgit.enabled: ["-rasp1", "*"]`). The server evaluates
//! the pattern list per envoy hostname and emits a single bool — the
//! agent never sees the patterns themselves.
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
//!
//! `swarmdns` (the agent-managed `/etc/hosts` block) is gated here
//! too, via [`swarmdns_active`] — but with the **opposite** fail
//! mode: default-OFF. The server emits `swarm_swarmdns_enabled` only
//! when the operator set `swarm.swarmdns: true` and the host matches
//! `swarm.enabled`; anything else — absent, explicit `false`, no
//! policy bundle yet — means off. Editing `/etc/hosts` is an operator
//! decision, not something a fresh-enrolled envoy does on its own.

use crate::state::StateStore;

/// Substrate identifier used to select the right per-substrate flag
/// names. Keeps the field-name lookup in one place so a typo in a
/// caller can't silently disable a subsystem.
#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub enum Substrate {
    Payload,
    Swarmgit,
    Lockbox,
    Longdrawer,
}

impl Substrate {
    fn enabled_key(self) -> &'static str {
        match self {
            Substrate::Payload => "swarm_swarmcast_enabled",
            Substrate::Swarmgit => "swarm_swarmgit_enabled",
            // Lockbox derives from puddle — single source of truth
            // per ADR-020. The server emits only `swarm_puddle_enabled`;
            // there is no `swarm_lockbox_enabled` in 0.41.0+.
            Substrate::Lockbox => "swarm_puddle_enabled",
            Substrate::Longdrawer => "swarm_longdrawer_enabled",
        }
    }

    /// Human-readable name for log lines. Stable; do not change without
    /// updating any operator-facing diagnostics that match on it.
    pub fn name(self) -> &'static str {
        match self {
            Substrate::Payload => "payload",
            Substrate::Swarmgit => "swarmgit",
            Substrate::Lockbox => "lockbox",
            Substrate::Longdrawer => "longdrawer",
        }
    }
}

/// Compute whether `substrate` should be active for this envoy right
/// now. Reads from the cached `PolicyBundle.vars_json` if available.
/// Returns `true` (active) in every ambiguous case — fail-open is
/// the design (matches swarmgit's pre-existing semantics): a fresh
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
    true
}

/// Permissive truthy check used for the `*_enabled` keys. Returns true
/// for an explicit truthy value AND for absence. Returns false ONLY
/// for an explicit falsy value (`false`, `"false"`, `"no"`, `"0"`,
/// number zero). The fail-open default — operators who don't set
/// the flag get the historical "subsystem on" behavior.
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

/// Strict truthy check used for `swarm_swarmdns_enabled`. Returns true
/// **only** for an explicit truthy value (`true`, `"true"`/`"yes"`/`"1"`,
/// nonzero number); absence, `null`, `false`, and anything else are
/// off. This is the fail-*closed* counterpart of [`is_truthy_or_absent`]
/// — swarmdns must not turn itself on by default.
fn is_explicitly_true(v: Option<&serde_json::Value>) -> bool {
    match v {
        Some(serde_json::Value::Bool(b)) => *b,
        Some(serde_json::Value::String(s)) => {
            matches!(s.to_ascii_lowercase().as_str(), "true" | "yes" | "1")
        }
        Some(serde_json::Value::Number(n)) => n.as_i64().map_or(false, |i| i != 0),
        _ => false,
    }
}

/// Compute whether the swarmdns `/etc/hosts` writer should be active
/// for this envoy right now — `swarm_enabled` (fail-open) AND
/// `swarm_swarmdns_enabled` (fail-closed, see [`is_explicitly_true`]).
/// Returns `false` in every ambiguous case (no state store, no policy
/// bundle yet, parse failure): the agent does not touch `/etc/hosts`
/// until the server explicitly says swarmdns is on.
pub fn swarmdns_active(state_store: Option<&StateStore>) -> bool {
    let Some(store) = state_store else {
        return false;
    };
    let rtxn = match store.read_txn() {
        Ok(r) => r,
        Err(_) => return false,
    };
    let bundle_bytes = match store.policy.load(&rtxn) {
        Ok(Some((_, b))) => b,
        _ => return false,
    };
    if bundle_bytes.vars_json.is_empty() {
        return false;
    }
    let val: serde_json::Value = match serde_json::from_slice(&bundle_bytes.vars_json) {
        Ok(v) => v,
        Err(_) => return false,
    };
    if !is_truthy_or_absent(val.get("swarm_enabled")) {
        return false;
    }
    is_explicitly_true(val.get("swarm_swarmdns_enabled"))
}

#[cfg(test)]
mod tests {
    use super::*;

    fn val(json: &str) -> serde_json::Value {
        serde_json::from_str(json).unwrap()
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
    fn explicitly_true_is_fail_closed() {
        // On only for an explicit truthy value.
        assert!(is_explicitly_true(Some(&val("true"))));
        assert!(is_explicitly_true(Some(&val("\"true\""))));
        assert!(is_explicitly_true(Some(&val("\"yes\""))));
        assert!(is_explicitly_true(Some(&val("\"1\""))));
        assert!(is_explicitly_true(Some(&val("1"))));
        // Off for absence, null, false, and anything ambiguous —
        // the opposite of is_truthy_or_absent.
        assert!(!is_explicitly_true(None));
        assert!(!is_explicitly_true(Some(&val("null"))));
        assert!(!is_explicitly_true(Some(&val("false"))));
        assert!(!is_explicitly_true(Some(&val("\"false\""))));
        assert!(!is_explicitly_true(Some(&val("\"maybe\""))));
        assert!(!is_explicitly_true(Some(&val("0"))));
    }

    #[test]
    fn swarmdns_active_off_without_state_store() {
        assert!(!swarmdns_active(None));
    }

    #[test]
    fn lockbox_derives_from_puddle() {
        // The whole point of ADR-020: lockbox's gate must consult
        // swarm_puddle_enabled, not a separate lockbox key.
        assert_eq!(Substrate::Lockbox.enabled_key(), "swarm_puddle_enabled");
    }

    #[test]
    fn enabled_keys_are_distinct_except_lockbox_aliases_puddle() {
        // Payload, Swarmgit, Longdrawer each have their own key.
        // Lockbox intentionally aliases the puddle key — single
        // source of truth.
        assert_ne!(
            Substrate::Payload.enabled_key(),
            Substrate::Swarmgit.enabled_key()
        );
        assert_ne!(
            Substrate::Payload.enabled_key(),
            Substrate::Longdrawer.enabled_key()
        );
        assert_ne!(
            Substrate::Swarmgit.enabled_key(),
            Substrate::Longdrawer.enabled_key()
        );
    }

    #[test]
    fn substrate_names_are_distinct() {
        let subs = [
            Substrate::Payload,
            Substrate::Swarmgit,
            Substrate::Lockbox,
            Substrate::Longdrawer,
        ];
        for (i, a) in subs.iter().enumerate() {
            for b in &subs[i + 1..] {
                assert_ne!(a.name(), b.name());
            }
        }
    }
}
