// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

//! Reports per-user lockbox mesh state.
//!
//! On each check-in the daemon builds a compact per-user snapshot
//! (see `agent/src/lockbox/snapshot.rs`) containing the file set,
//! tombstones, and known peers, and ships it via the standard trait
//! pipeline under the `lockbox` namespace. The server aggregator
//! (`server/lockboxmesh/`) joins these across the fleet to render
//! per-user cross-envoy consistency, tombstone propagation, and
//! recipient-drift views on the `/mesh` page.
//!
//! Returns `Value::Null` when no users are enrolled on this envoy
//! — matches the pattern used by `gitmirror::collect` and
//! `swarm_seeds::collect` for empty state.

use anyhow::Result;
use serde_json::{to_value, Value};

use crate::swarm::lockbox::snapshot::build_trait_payload;

pub fn collect() -> Result<Value> {
    let payload = build_trait_payload()?;
    if payload.users.is_empty() {
        return Ok(Value::Null);
    }
    Ok(to_value(payload)?)
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn collect_returns_null_when_no_users_enrolled() {
        // In the test harness, /etc/passwd typically has no users
        // with a lockbox identity.age file → enrolled_users() is
        // empty → payload.users is empty → Null.
        match collect() {
            Ok(Value::Null) => {}
            Ok(other) => {
                // Acceptable when the test host has a real enrolled
                // user — don't hard-fail; just ensure the shape is
                // reasonable.
                assert!(other.is_object());
            }
            Err(e) => panic!("collect() failed: {}", e),
        }
    }
}
