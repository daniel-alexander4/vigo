// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

//! Runtime on/off switch for the gitmirror subsystem.
//!
//! Gitmirror is opt-in per-envoy via the matchfile var `gitmirror: true`.
//! The var is delivered on every check-in inside `PolicyBundle.vars_json`
//! and cached in the LMDB state store. The agent re-reads the cache after
//! every accepted bundle and drives this gate; every subsystem component
//! that cares about the flag (discovery announcer, peer push handler,
//! reactor) consults [`Gate::enabled`] at its own natural sample point.
//!
//! All the gitmirror data structures (`PeerTable`, `PendingClaims`,
//! `ClaimsHandle`, `GitmirrorServerCtx`) are constructed unconditionally
//! and remain inert while the gate is off — a dormant envoy never emits
//! GITMIRROR / GITMIRRORCLAIM announcements, never accepts `/gitmirror/push`
//! requests, and the reactor skips its publish / receive work on every
//! tick. This matches the "always-on-but-inert" pattern already used by
//! the multicast listener.
//!
//! A `set` call returns the previous value so callers can detect
//! OFF→ON and ON→OFF transitions; the reactor uses that signal to run
//! its per-label state reset on activation and to clear the peer table
//! and pending claims on deactivation.

use std::sync::Arc;
use std::sync::atomic::{AtomicBool, Ordering};

/// Shared on/off flag for the gitmirror subsystem.
///
/// Cheap to clone (it's an `Arc<AtomicBool>`). All reads use
/// `Ordering::Relaxed` — we only need a single-bit eventually-consistent
/// view, and reads happen on hot paths (the peer push handler reads once
/// per request).
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
        Self::new(false)
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
        let g = Gate::new(false);
        assert!(!g.set(true), "first set should return the initial false");
        assert!(g.enabled());
        assert!(g.set(true), "redundant set returns the current true");
        assert!(g.set(false), "flip back returns true");
        assert!(!g.enabled());
    }

    #[test]
    fn clone_shares_state() {
        let a = Gate::new(false);
        let b = a.clone();
        a.set(true);
        assert!(b.enabled(), "cloned gate must see the same update");
    }
}
