// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

//! Server-distributed swarm runtime limits.
//!
//! The server packs a [`SwarmRuntimeLimits`][crate::proto::SwarmRuntimeLimits]
//! message into every `CheckInResponse` and the agent stashes the values
//! here so subsystem-level enforcement sites can read the operator's
//! current intent without re-plumbing the response through every
//! call path. This is the agent-side end of the
//! `server.yaml swarm.{swarmcast,lockbox,swarmgit,longdrawer}.*` →
//! `CheckInResponse.swarm_runtime_limits` → enforcement-point flow.
//!
//! ### Design
//!
//! Single process-global cache, RwLock-protected. Reads dominate
//! (every encrypt / push / envelope-reap / fan-out consults the cache);
//! writes happen at most once per CheckIn cycle. Each field is
//! `Option<...>`: `None` means "the server hasn't told us yet" (early
//! startup, pre-enrollment) and the caller falls back to its
//! compile-time default. A zero or empty value from the server means
//! "no cap" / "use compile-time default" — the wire-format semantic
//! is preserved here.
//!
//! ### Strict-equal version handshake
//!
//! Per CLAUDE.md the server forces agents to match its version exactly,
//! so agents that don't know about this proto field never reach
//! production for long. The `None` fallback exists to cover the brief
//! cutover window (server new, agent still booting up the new binary)
//! and the local-test setup where the agent runs without a server.

use std::sync::{OnceLock, RwLock};

/// Snapshot of the server-distributed swarm caps. All fields are
/// `Option` so a partial server payload (e.g., older server populating
/// only some fields) doesn't reset other fields to zero.
#[derive(Debug, Clone, Default)]
pub struct SwarmLimits {
    pub swarmcast_max_size_bytes: Option<u64>,
    pub swarmcast_default_retention: Option<String>,
    pub lockbox_max_file_bytes: Option<u64>,
    pub lockbox_envelope_retention_secs: Option<u64>,
    pub swarmgit_max_bundle_bytes: Option<u64>,
    pub swarmgit_envelope_retention_secs: Option<u64>,
    pub longdrawer_max_file_bytes: Option<u64>,
}

fn cell() -> &'static RwLock<SwarmLimits> {
    static CELL: OnceLock<RwLock<SwarmLimits>> = OnceLock::new();
    CELL.get_or_init(|| RwLock::new(SwarmLimits::default()))
}

/// Update the cache from a [`SwarmRuntimeLimits`][crate::proto::SwarmRuntimeLimits]
/// message. Zero / empty fields on the wire become `None` here so
/// fallbacks at the read sites stay simple.
pub fn update_from_proto(msg: &crate::proto::SwarmRuntimeLimits) {
    let new = SwarmLimits {
        swarmcast_max_size_bytes: opt_nonzero_u64(msg.swarmcast_max_size_bytes),
        swarmcast_default_retention: opt_nonempty_string(&msg.swarmcast_default_retention),
        lockbox_max_file_bytes: opt_nonzero_u64(msg.lockbox_max_file_bytes),
        lockbox_envelope_retention_secs: opt_nonzero_u64(msg.lockbox_envelope_retention_secs),
        swarmgit_max_bundle_bytes: opt_nonzero_u64(msg.swarmgit_max_bundle_bytes),
        swarmgit_envelope_retention_secs: opt_nonzero_u64(msg.swarmgit_envelope_retention_secs),
        longdrawer_max_file_bytes: opt_nonzero_u64(msg.longdrawer_max_file_bytes),
    };
    let mut w = cell().write().expect("swarm runtime limits cache poisoned");
    *w = new;
}

/// Read a copy of the current cache. Cheap (small struct of 7
/// `Option`s); callers can match on individual fields without holding
/// the lock.
pub fn current() -> SwarmLimits {
    cell().read().expect("swarm runtime limits cache poisoned").clone()
}

/// Reset the cache. Tests only.
#[cfg(test)]
pub fn reset_for_tests() {
    let mut w = cell().write().expect("swarm runtime limits cache poisoned");
    *w = SwarmLimits::default();
}

fn opt_nonzero_u64(v: u64) -> Option<u64> {
    if v == 0 {
        None
    } else {
        Some(v)
    }
}

fn opt_nonempty_string(s: &str) -> Option<String> {
    if s.is_empty() {
        None
    } else {
        Some(s.to_string())
    }
}

// ---------- typed read helpers ----------

/// Lockbox: per-file ciphertext cap. Falls back to the historical
/// 1 GiB compile-time default when the server hasn't published a
/// limit yet.
pub fn lockbox_max_file_bytes() -> u64 {
    current()
        .lockbox_max_file_bytes
        .unwrap_or(1024 * 1024 * 1024)
}

/// Lockbox: envelope retention in seconds. Falls back to 90 days.
pub fn lockbox_envelope_retention_secs() -> i64 {
    current()
        .lockbox_envelope_retention_secs
        .map(|v| v as i64)
        .unwrap_or(90 * 24 * 60 * 60)
}

/// Swarmgit: per-bundle fan-out size cap. `None` = uncapped.
pub fn swarmgit_max_bundle_bytes() -> Option<u64> {
    current().swarmgit_max_bundle_bytes
}

/// Swarmgit: envelope retention in seconds. Falls back to 90 days.
pub fn swarmgit_envelope_retention_secs() -> i64 {
    current()
        .swarmgit_envelope_retention_secs
        .map(|v| v as i64)
        .unwrap_or(90 * 24 * 60 * 60)
}

/// Longdrawer: per-file LAN sync size cap. `None` = uncapped.
pub fn longdrawer_max_file_bytes() -> Option<u64> {
    current().longdrawer_max_file_bytes
}

#[cfg(test)]
mod tests {
    use super::*;

    fn fresh() {
        reset_for_tests();
    }

    #[test]
    fn defaults_when_unset() {
        fresh();
        assert_eq!(lockbox_max_file_bytes(), 1024 * 1024 * 1024);
        assert_eq!(lockbox_envelope_retention_secs(), 90 * 24 * 60 * 60);
        assert_eq!(swarmgit_envelope_retention_secs(), 90 * 24 * 60 * 60);
        assert_eq!(swarmgit_max_bundle_bytes(), None);
        assert_eq!(longdrawer_max_file_bytes(), None);
    }

    #[test]
    fn server_values_propagate() {
        fresh();
        let msg = crate::proto::SwarmRuntimeLimits {
            swarmcast_max_size_bytes: 100,
            swarmcast_default_retention: "30d".to_string(),
            lockbox_max_file_bytes: 200,
            lockbox_envelope_retention_secs: 300,
            swarmgit_max_bundle_bytes: 400,
            swarmgit_envelope_retention_secs: 500,
            longdrawer_max_file_bytes: 600,
        };
        update_from_proto(&msg);
        assert_eq!(lockbox_max_file_bytes(), 200);
        assert_eq!(lockbox_envelope_retention_secs(), 300);
        assert_eq!(swarmgit_max_bundle_bytes(), Some(400));
        assert_eq!(swarmgit_envelope_retention_secs(), 500);
        assert_eq!(longdrawer_max_file_bytes(), Some(600));
        assert_eq!(current().swarmcast_max_size_bytes, Some(100));
        assert_eq!(current().swarmcast_default_retention.as_deref(), Some("30d"));
    }

    #[test]
    fn zero_fields_fall_back_to_defaults() {
        fresh();
        let msg = crate::proto::SwarmRuntimeLimits {
            swarmcast_max_size_bytes: 0,
            swarmcast_default_retention: String::new(),
            lockbox_max_file_bytes: 0,
            lockbox_envelope_retention_secs: 0,
            swarmgit_max_bundle_bytes: 0,
            swarmgit_envelope_retention_secs: 0,
            longdrawer_max_file_bytes: 0,
        };
        update_from_proto(&msg);
        // Zero / empty server values become None internally — read
        // helpers return their compile-time defaults.
        assert_eq!(lockbox_max_file_bytes(), 1024 * 1024 * 1024);
        assert_eq!(swarmgit_max_bundle_bytes(), None);
    }
}
