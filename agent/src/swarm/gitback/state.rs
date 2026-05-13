// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

//! Small shared helper for the gitback subsystem's wire/state code.
//!
//! This module used to own the bundle-era publisher/receiver state
//! schemas and the `TraitsSnapshot` the trait collector read from
//! `<base>/traits.json`. Both are gone: the publisher/receiver
//! pipeline was retired in 0.38.16 with ADR-015 step 4, and the
//! trait collector was rebuilt to walk live state directly
//! (`agent/src/traits/gitback.rs`) rather than read a snapshot file
//! the reactor never actually wrote. All that's left is the unix-time
//! helper a couple of the transport code paths
//! (`crate::swarm::{discovery, server}`) still reach for.

use std::time::SystemTime;

/// Return the current time as unix seconds. Falls back to `0` if
/// the system clock is before the epoch (ie. never in practice).
pub fn unix_seconds_now() -> u64 {
    SystemTime::now()
        .duration_since(std::time::UNIX_EPOCH)
        .map(|d| d.as_secs())
        .unwrap_or(0)
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn unix_seconds_now_is_reasonable() {
        let s = unix_seconds_now();
        // After 2026-01-01 and before 2100-01-01 — sanity bounds.
        assert!(s > 1_767_225_600);
        assert!(s < 4_102_444_800);
    }
}
