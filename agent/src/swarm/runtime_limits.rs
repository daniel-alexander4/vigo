// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

//! Server-distributed swarm runtime limits.
//!
//! The server packs a [`SwarmRuntimeLimits`][crate::proto::SwarmRuntimeLimits]
//! message into every `CheckInResponse` and the agent stashes the values
//! here so subsystem-level enforcement sites can read the operator's
//! current intent without re-plumbing the response through every
//! call path. This is the agent-side end of the
//! `server.yaml swarm.{filecast,lockbox,gitback,curator,poolq,longdrawer}.*` →
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
    pub filecast_max_size_bytes: Option<u64>,
    pub filecast_default_retention: Option<String>,
    pub lockbox_max_file_bytes: Option<u64>,
    pub lockbox_envelope_retention_secs: Option<u64>,
    pub gitback_max_bundle_bytes: Option<u64>,
    pub gitback_envelope_retention_secs: Option<u64>,
    pub longdrawer_max_file_bytes: Option<u64>,
    pub curator_max_bytes: Option<u64>,
    pub curator_local_port: Option<u32>,
    pub poolq_max_msg_bytes: Option<u64>,
    pub poolq_retention_secs: Option<u64>,
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
        filecast_max_size_bytes: opt_nonzero_u64(msg.filecast_max_size_bytes),
        filecast_default_retention: opt_nonempty_string(&msg.filecast_default_retention),
        lockbox_max_file_bytes: opt_nonzero_u64(msg.lockbox_max_file_bytes),
        lockbox_envelope_retention_secs: opt_nonzero_u64(msg.lockbox_envelope_retention_secs),
        gitback_max_bundle_bytes: opt_nonzero_u64(msg.gitback_max_bundle_bytes),
        gitback_envelope_retention_secs: opt_nonzero_u64(msg.gitback_envelope_retention_secs),
        longdrawer_max_file_bytes: opt_nonzero_u64(msg.longdrawer_max_file_bytes),
        curator_max_bytes: opt_nonzero_u64(msg.curator_max_bytes),
        curator_local_port: opt_nonzero_u32(msg.curator_local_port),
        poolq_max_msg_bytes: opt_nonzero_u64(msg.poolq_max_msg_bytes),
        poolq_retention_secs: opt_nonzero_u64(msg.poolq_retention_secs),
    };
    let mut w = cell().write().unwrap_or_else(|e| e.into_inner());
    *w = new;
}

/// Read a copy of the current cache. Cheap (small struct of 7
/// `Option`s); callers can match on individual fields without holding
/// the lock.
pub fn current() -> SwarmLimits {
    cell().read().unwrap_or_else(|e| e.into_inner()).clone()
}

/// Reset the cache. Tests only.
#[cfg(test)]
pub fn reset_for_tests() {
    let mut w = cell().write().expect("swarm runtime limits cache poisoned");
    *w = SwarmLimits::default();
}

/// Test-only RAII guard: installs the given runtime limits into the
/// process-global cell and resets to defaults on drop. Combined with
/// `#[serial_test::serial(runtime_limits)]` on every mutating test, the
/// cell can never leak state into a downstream test — the guard
/// guarantees a clean cell across test boundaries, and the serial
/// annotation guarantees mutating tests don't stomp each other's state
/// while they run.
///
/// Without this, the pre-fix code path was: a test called
/// `update_from_proto(...)` to install non-default limits, never
/// reset, and the next test in the same module that *read* the limits
/// (e.g. through `curator_max_bytes()` or `curator_local_port()`)
/// saw the leaked value. Cargo's parallel test runner exposed it as a
/// race: tests passed in isolation but failed when run together. The
/// observable symptoms were `curator::helper::tests::rejects_oversize_blob`
/// leaking `curator_max_bytes: 4` into `happy_path_stages_blob_…` and
/// `rejects_blob_sha_mismatch` running afterward.
#[cfg(test)]
#[must_use = "the guard resets runtime limits on drop; bind it to a variable"]
pub struct TestGuard;

#[cfg(test)]
impl Drop for TestGuard {
    fn drop(&mut self) {
        reset_for_tests();
    }
}

/// Install `msg` into the cell and return a [`TestGuard`] that resets
/// to defaults when it goes out of scope. Tests bind it to `_g` and
/// the cell is clean when the test returns. Use alongside
/// `#[serial_test::serial(runtime_limits)]` on the test fn for
/// race-safety against other mutating tests.
#[cfg(test)]
pub fn install_for_test(msg: &crate::proto::SwarmRuntimeLimits) -> TestGuard {
    reset_for_tests();
    update_from_proto(msg);
    TestGuard
}

fn opt_nonzero_u64(v: u64) -> Option<u64> {
    if v == 0 {
        None
    } else {
        Some(v)
    }
}

fn opt_nonzero_u32(v: u32) -> Option<u32> {
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

/// Gitback: per-bundle size cap, in bytes. Falls back to the
/// compile-time [`crate::swarm::gitback::DEFAULT_MAX_BUNDLE_SIZE`]
/// (2 GiB) when the server hasn't published a limit yet, or when the
/// operator left `swarm.gitback.max_bundle_bytes` at 0/unset (which
/// the proto→struct mapping turns into `None`). Enforced on every
/// path that moves a bundle: publisher fan-out, the push-receive
/// POST handlers, and the DR catch-up GET.
pub fn gitback_max_bundle_bytes() -> u64 {
    current()
        .gitback_max_bundle_bytes
        .unwrap_or(crate::swarm::gitback::DEFAULT_MAX_BUNDLE_SIZE)
}

/// Gitback: envelope retention in seconds. Falls back to 90 days.
pub fn gitback_envelope_retention_secs() -> i64 {
    current()
        .gitback_envelope_retention_secs
        .map(|v| v as i64)
        .unwrap_or(90 * 24 * 60 * 60)
}

/// Longdrawer: per-file LAN sync size cap. `None` = uncapped.
pub fn longdrawer_max_file_bytes() -> Option<u64> {
    current().longdrawer_max_file_bytes
}

/// Curator: per-blob (one `{name, version, os, arch}` artifact) size cap, in
/// bytes. Falls back to [`crate::swarm::curator::DEFAULT_MAX_BYTES`] (2 GiB)
/// when the server hasn't published a limit yet, or when the operator left
/// `swarm.curator.max_bytes` at 0/unset (the proto→struct mapping turns that
/// into `None`). Enforced on the publish path — the curator helper daemon
/// rejects an artifact blob over this size. There is no uncapped mode.
pub fn curator_max_bytes() -> u64 {
    current()
        .curator_max_bytes
        .unwrap_or(crate::swarm::curator::DEFAULT_MAX_BYTES)
}

/// Curator: localhost HTTP port for the agent-local facades — the OCI
/// Distribution v2 registry shim and the apt/dnf/apk package-repo server.
/// `None` here = the server hasn't published yet *or* the operator set
/// `swarm.curator.local_port: 0` to disable both. Callers decide whether to
/// skip binding the listener (port disabled) or use a default — there's no
/// single right answer at this layer.
pub fn curator_local_port() -> Option<u32> {
    current().curator_local_port
}

/// Poolq: per-message body cap, in bytes. Falls back to the compile-time
/// default (16 KiB) when the server hasn't published a limit yet or the
/// operator left `swarm.poolq.max_msg_bytes` at 0/unset. The hard ceiling
/// (64 KiB) is enforced at the helper-daemon publish path independently of
/// this cap — operators tune within `[0, 64 KiB]`; bigger payloads belong
/// in a curator artifact.
pub fn poolq_max_msg_bytes() -> u64 {
    current().poolq_max_msg_bytes.unwrap_or(16 * 1024)
}

/// Poolq: per-topic retention window, in seconds. Falls back to the
/// compile-time default (7d) when the server hasn't published a value yet
/// or the operator left `swarm.poolq.retention` empty. Messages older than
/// this are pruned by the mesh aggregator, and stale-on-ingest messages
/// are refused — together that's "flap-stable pruning, no tombstones."
pub fn poolq_retention_secs() -> i64 {
    current()
        .poolq_retention_secs
        .map(|v| v as i64)
        .unwrap_or(7 * 24 * 60 * 60)
}

#[cfg(test)]
mod tests {
    use super::*;
    use serial_test::serial;

    #[test]
    #[serial(runtime_limits)]
    fn defaults_when_unset() {
        reset_for_tests();
        let _g = TestGuard; // restores defaults on drop
        assert_eq!(lockbox_max_file_bytes(), 1024 * 1024 * 1024);
        assert_eq!(lockbox_envelope_retention_secs(), 90 * 24 * 60 * 60);
        assert_eq!(gitback_envelope_retention_secs(), 90 * 24 * 60 * 60);
        assert_eq!(
            gitback_max_bundle_bytes(),
            crate::swarm::gitback::DEFAULT_MAX_BUNDLE_SIZE
        );
        assert_eq!(longdrawer_max_file_bytes(), None);
        assert_eq!(curator_max_bytes(), crate::swarm::curator::DEFAULT_MAX_BYTES);
        assert_eq!(curator_local_port(), None);
        assert_eq!(poolq_max_msg_bytes(), 16 * 1024);
        assert_eq!(poolq_retention_secs(), 7 * 24 * 60 * 60);
    }

    #[test]
    #[serial(runtime_limits)]
    fn server_values_propagate() {
        let _g = install_for_test(&crate::proto::SwarmRuntimeLimits {
            filecast_max_size_bytes: 100,
            filecast_default_retention: "30d".to_string(),
            lockbox_max_file_bytes: 200,
            lockbox_envelope_retention_secs: 300,
            gitback_max_bundle_bytes: 400,
            gitback_envelope_retention_secs: 500,
            longdrawer_max_file_bytes: 600,
            curator_max_bytes: 700,
            curator_local_port: 5050,
            poolq_max_msg_bytes: 8192,
            poolq_retention_secs: 86400,
        });
        assert_eq!(lockbox_max_file_bytes(), 200);
        assert_eq!(lockbox_envelope_retention_secs(), 300);
        assert_eq!(gitback_max_bundle_bytes(), 400);
        assert_eq!(gitback_envelope_retention_secs(), 500);
        assert_eq!(longdrawer_max_file_bytes(), Some(600));
        assert_eq!(curator_max_bytes(), 700);
        assert_eq!(curator_local_port(), Some(5050));
        assert_eq!(poolq_max_msg_bytes(), 8192);
        assert_eq!(poolq_retention_secs(), 86400);
        assert_eq!(current().filecast_max_size_bytes, Some(100));
        assert_eq!(current().filecast_default_retention.as_deref(), Some("30d"));
    }

    #[test]
    #[serial(runtime_limits)]
    fn zero_fields_fall_back_to_defaults() {
        let _g = install_for_test(&crate::proto::SwarmRuntimeLimits {
            filecast_max_size_bytes: 0,
            filecast_default_retention: String::new(),
            lockbox_max_file_bytes: 0,
            lockbox_envelope_retention_secs: 0,
            gitback_max_bundle_bytes: 0,
            gitback_envelope_retention_secs: 0,
            longdrawer_max_file_bytes: 0,
            curator_max_bytes: 0,
            curator_local_port: 0,
            poolq_max_msg_bytes: 0,
            poolq_retention_secs: 0,
        });
        // Zero / empty server values become None internally — read
        // helpers return their compile-time defaults.
        assert_eq!(lockbox_max_file_bytes(), 1024 * 1024 * 1024);
        assert_eq!(
            gitback_max_bundle_bytes(),
            crate::swarm::gitback::DEFAULT_MAX_BUNDLE_SIZE
        );
        assert_eq!(curator_max_bytes(), crate::swarm::curator::DEFAULT_MAX_BYTES);
        // Port 0 means "disabled" — distinct from "unset". Both surface
        // as None to callers; they decide whether to bind a default.
        assert_eq!(curator_local_port(), None);
        assert_eq!(poolq_max_msg_bytes(), 16 * 1024);
        assert_eq!(poolq_retention_secs(), 7 * 24 * 60 * 60);
    }
}
