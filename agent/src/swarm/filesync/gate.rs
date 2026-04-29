// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

//! Runtime on/off switch for the filesync subsystem.
//!
//! Filesync is **default-on per-envoy**: every enrolled envoy
//! participates unless one of three signals from server.yaml flips it
//! off — `swarm.enabled: false`, `swarm.filesync.enabled: false`, or
//! the envoy's hostname matches a glob in `swarm.filesync.blacklist`.
//! All three arrive on every check-in inside `PolicyBundle.vars_json`
//! (`swarm_enabled`, `swarm_filesync_enabled`, `filesync_blacklisted`).
//! The agent re-reads the cached bundle and drives this gate.
//! [`crate::swarm::gates::substrate_active`] computes the
//! global-AND-per-substrate-AND-NOT-blacklisted product.
//!
//! Every subsystem consumer (multicast announcer, peer push handler,
//! reconciler) consults [`Gate::enabled`] at its own natural sample
//! point. When the gate is off the announcer stops broadcasting, the
//! push handler returns 403, and the reconciler skips its work on
//! every tick. Content under `~user/filesync/` is left in place — the
//! gate is a pause, not an evict. Filesync's blacklist is gate-only
//! by design (filesync content is plaintext user data; destructive
//! removal is a manual operator action, never automated).
//!
//! A `set` call returns the previous value so callers can detect
//! OFF→ON and ON→OFF transitions for transition-only logging.

use std::sync::Arc;
use std::sync::atomic::{AtomicBool, Ordering};

/// Shared on/off flag for the filesync subsystem.
///
/// Cheap to clone (`Arc<AtomicBool>`). Reads use `Ordering::Relaxed`
/// — single-bit eventually-consistent view, read on hot paths
/// (push handler runs once per peer request).
#[derive(Clone, Debug)]
pub struct Gate {
    inner: Arc<AtomicBool>,
}

impl Gate {
    /// Build a new gate with the given initial state.
    pub fn new(enabled: bool) -> Self {
        Self {
            inner: Arc::new(AtomicBool::new(enabled)),
        }
    }

    /// Current state. `true` means the subsystem is active.
    pub fn enabled(&self) -> bool {
        self.inner.load(Ordering::Relaxed)
    }

    /// Store a new state and return the previous one. Callers use the
    /// pair `(previous, new)` to detect OFF→ON / ON→OFF transitions.
    pub fn set(&self, enabled: bool) -> bool {
        self.inner.swap(enabled, Ordering::Relaxed)
    }
}

impl Default for Gate {
    fn default() -> Self {
        Self::new(true)
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn new_and_enabled_roundtrip() {
        let g = Gate::new(false);
        assert!(!g.enabled());
        let g = Gate::new(true);
        assert!(g.enabled());
    }

    #[test]
    fn set_returns_previous_value() {
        let g = Gate::new(true);
        assert!(g.set(false), "first set should return the initial true");
        assert!(!g.enabled());
        assert!(!g.set(true), "flip back returns false");
        assert!(g.enabled());
    }

    #[test]
    fn clone_shares_state() {
        let a = Gate::new(true);
        let b = a.clone();
        a.set(false);
        assert!(!b.enabled(), "cloned gate must see the same update");
    }
}
