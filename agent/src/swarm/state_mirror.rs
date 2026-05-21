// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

//! Daemon-canonical, user-readable mirror of the agent's resolved swarm
//! state (0.41.1+).
//!
//! Written by the daemon at `/var/lib/vigo/swarm/state.json` (mode 0644)
//! after every successful bundle acceptance. The file mirrors the
//! `swarm_<sub>_enabled` keys from the just-accepted `PolicyBundle.vars_json`
//! plus a few derived fields the diagnostic CLI verbs surface
//! (`vigo swarm status`).
//!
//! Same daemon-canonical + user-readable mirror pattern already used by
//! peers.json / tombstones.json / leaves.json / puddle_members.json /
//! usercrate-policy/<user>.json.
//!
//! The schema is **private to the agent** — operators consume it via
//! `vigo swarm status` rather than parsing the JSON directly. Field
//! names + shape may change between releases. No version field carried;
//! tolerant decoding handles unknown fields silently.

use std::fs;
use std::path::{Path, PathBuf};
use std::time::{SystemTime, UNIX_EPOCH};

use anyhow::{Context, Result};
use serde::{Deserialize, Serialize};

/// Default location for the mirror file.
pub const STATE_MIRROR_PATH: &str = "/var/lib/vigo/swarm/state.json";

/// Resolved swarm state mirrored to disk after each bundle accept.
#[derive(Debug, Clone, PartialEq, Eq, Serialize, Deserialize, Default)]
pub struct AgentSwarmState {
    /// Agent build version (mirrors `env!("CARGO_PKG_VERSION")` at write time).
    #[serde(default)]
    pub agent_version: String,
    /// Unix-second timestamp of the bundle accept that produced this snapshot.
    #[serde(default)]
    pub last_checkin_ts: i64,
    /// `PolicyBundle.policy_version` of the accepted bundle.
    #[serde(default)]
    pub policy_version: u64,
    /// Resolved per-substrate gates from the accepted bundle's vars_json.
    #[serde(default)]
    pub gates: GateState,
}

/// Resolved gate booleans for the five content subsystems plus the
/// substrate-level kill switch, plus `hostdox`. `puddle` is the
/// single-source-of-truth for lockbox (ADR-020) — there is no
/// separate lockbox field. `hostdox` is the one default-OFF gate
/// (editing `/etc/hosts` is an operator decision — see
/// [`super::gates::hostdox_active`]).
///
/// `curator` (ADR-024) is a fail-open content gate like the rest; it
/// is intentionally **not** `#[serde(default)]`, so a pre-curator
/// `state.json` written by an older daemon (with a `gates` object that
/// lacks the key) fails to decode → [`read`] errors → the CLI's
/// `envoy_layer_allows` fails open — the correct behavior for the one
/// reconcile cycle between a strict-version upgrade and the first
/// check-in that rewrites this file in the new shape. (`hostdox` can
/// be `#[serde(default)]` because its `false` default matches its
/// fail-closed semantics; a `false` curator default would be a
/// wrong-direction refusal.)
#[derive(Debug, Clone, Copy, PartialEq, Eq, Serialize, Deserialize)]
pub struct GateState {
    pub swarm: bool,
    pub payload: bool,
    pub gitback: bool,
    pub curator: bool,
    pub longdrawer: bool,
    pub puddle: bool,
    #[serde(default)]
    pub hostdox: bool,
}

impl Default for GateState {
    /// Default is all-on EXCEPT `hostdox` — mirrors the fail-open
    /// semantic in [`super::gates::substrate_active`] for the content
    /// subsystems (a freshly-enrolled envoy with no policy bundle yet
    /// runs them until told otherwise) and the fail-*closed* semantic
    /// for hostdox (off until the server explicitly says on).
    fn default() -> Self {
        Self {
            swarm: true,
            payload: true,
            gitback: true,
            curator: true,
            longdrawer: true,
            puddle: true,
            hostdox: false,
        }
    }
}

/// Path to the on-disk state mirror under [`STATE_MIRROR_PATH`].
pub fn default_path() -> PathBuf {
    PathBuf::from(STATE_MIRROR_PATH)
}

/// Snapshot the resolved gates from `vars_json` (a freshly-accepted
/// bundle's `vars_json` field) and persist them to `path`. Atomic
/// temp+rename, mode 0644 so user-mode CLI verbs can read without
/// elevation.
///
/// Best-effort: a failure here never blocks bundle acceptance. The
/// caller logs at WARN.
pub fn write(path: &Path, agent_version: &str, policy_version: u64, vars_json: &[u8]) -> Result<()> {
    let gates = decode_gates(vars_json);
    let state = AgentSwarmState {
        agent_version: agent_version.to_string(),
        last_checkin_ts: now_secs(),
        policy_version,
        gates,
    };
    if let Some(parent) = path.parent() {
        fs::create_dir_all(parent).with_context(|| format!("creating {}", parent.display()))?;
    }
    let tmp = path.with_extension("json.tmp");
    let bytes = serde_json::to_vec_pretty(&state).context("serializing swarm state mirror")?;
    fs::write(&tmp, &bytes).with_context(|| format!("writing {}", tmp.display()))?;
    #[cfg(unix)]
    {
        use std::os::unix::fs::PermissionsExt;
        let _ = fs::set_permissions(&tmp, fs::Permissions::from_mode(0o644));
    }
    fs::rename(&tmp, path)
        .with_context(|| format!("renaming {} -> {}", tmp.display(), path.display()))?;
    Ok(())
}

/// Read the on-disk mirror. Returns an explicit error when the file
/// is missing or unparseable so the CLI can surface a clear message
/// pointing at the path. Tolerant of missing fields (struct uses
/// `#[serde(default)]` throughout).
pub fn read(path: &Path) -> Result<AgentSwarmState> {
    let bytes = fs::read(path)
        .with_context(|| format!("reading swarm state mirror at {}", path.display()))?;
    serde_json::from_slice(&bytes)
        .with_context(|| format!("parsing swarm state mirror at {}", path.display()))
}

/// Decode the gate keys from `vars_json`, matching the agent's
/// runtime evaluation so the disk mirror reflects what the gates
/// actually do: the content subsystems use the fail-open semantics of
/// [`super::gates::substrate_active`] (explicit `false` → off; absent
/// / missing / malformed → on), while `hostdox` uses the
/// fail-closed semantics of [`super::gates::hostdox_active`] (only
/// an explicit truthy value → on).
fn decode_gates(vars_json: &[u8]) -> GateState {
    if vars_json.is_empty() {
        return GateState::default();
    }
    let val: serde_json::Value = match serde_json::from_slice(vars_json) {
        Ok(v) => v,
        Err(_) => return GateState::default(),
    };
    GateState {
        swarm: gate_truthy(val.get("swarm_enabled")),
        payload: gate_truthy(val.get("swarm_filecast_enabled")),
        gitback: gate_truthy(val.get("swarm_gitback_enabled")),
        curator: gate_truthy(val.get("swarm_curator_enabled")),
        longdrawer: gate_truthy(val.get("swarm_longdrawer_enabled")),
        puddle: gate_truthy(val.get("swarm_puddle_enabled")),
        hostdox: gate_explicitly_true(val.get("swarm_hostdox_enabled")),
    }
}

/// Permissive truthy check matching [`super::gates::is_truthy_or_absent`]
/// but inlined here so the mirror writer doesn't take a dependency
/// on the gates module's private helpers.
fn gate_truthy(v: Option<&serde_json::Value>) -> bool {
    match v {
        None | Some(serde_json::Value::Null) => true,
        Some(serde_json::Value::Bool(b)) => *b,
        Some(serde_json::Value::String(s)) => !matches!(
            s.to_ascii_lowercase().as_str(),
            "false" | "no" | "0" | ""
        ),
        Some(serde_json::Value::Number(n)) => n.as_i64() != Some(0),
        _ => true,
    }
}

/// Strict truthy check matching [`super::gates::is_explicitly_true`]
/// (used for `swarm_hostdox_enabled`): on only for an explicit
/// truthy value — absence / `null` / `false` are off.
fn gate_explicitly_true(v: Option<&serde_json::Value>) -> bool {
    match v {
        Some(serde_json::Value::Bool(b)) => *b,
        Some(serde_json::Value::String(s)) => {
            matches!(s.to_ascii_lowercase().as_str(), "true" | "yes" | "1")
        }
        Some(serde_json::Value::Number(n)) => n.as_i64().is_some_and(|i| i != 0),
        _ => false,
    }
}

fn now_secs() -> i64 {
    SystemTime::now()
        .duration_since(UNIX_EPOCH)
        .map(|d| d.as_secs() as i64)
        .unwrap_or(0)
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn round_trips_through_disk() {
        let dir = tempfile::TempDir::new().unwrap();
        let path = dir.path().join("state.json");
        let vars = br#"{"swarm_enabled": false, "swarm_puddle_enabled": false}"#;
        write(&path, "0.41.1", 42, vars).unwrap();
        let got = read(&path).unwrap();
        assert_eq!(got.agent_version, "0.41.1");
        assert_eq!(got.policy_version, 42);
        assert!(!got.gates.swarm);
        assert!(!got.gates.puddle);
        // Defaults stay on for keys not in vars_json.
        assert!(got.gates.payload);
        assert!(got.gates.gitback);
        assert!(got.gates.curator);
        assert!(got.gates.longdrawer);
    }

    #[test]
    fn empty_vars_json_means_all_on() {
        let g = decode_gates(b"");
        assert!(g.swarm && g.payload && g.gitback && g.curator && g.longdrawer && g.puddle);
    }

    #[test]
    fn malformed_vars_json_means_all_on() {
        let g = decode_gates(b"not json");
        assert!(g.swarm && g.payload && g.gitback && g.curator && g.longdrawer && g.puddle);
    }

    #[test]
    fn missing_keys_default_to_on() {
        let g = decode_gates(b"{}");
        assert!(g.swarm && g.payload && g.gitback && g.curator && g.longdrawer && g.puddle);
    }

    #[test]
    fn explicit_false_disables() {
        let g = decode_gates(br#"{"swarm_filecast_enabled": false}"#);
        assert!(g.swarm);
        assert!(!g.payload);
        assert!(g.gitback && g.curator && g.longdrawer && g.puddle);
        // Curator gates off the same way.
        let g = decode_gates(br#"{"swarm_curator_enabled": false}"#);
        assert!(g.swarm && g.payload && g.gitback && g.longdrawer && g.puddle);
        assert!(!g.curator);
    }

    #[test]
    fn read_missing_file_errors_clearly() {
        let dir = tempfile::TempDir::new().unwrap();
        let err = read(&dir.path().join("nope.json")).unwrap_err();
        assert!(format!("{:#}", err).contains("nope.json"));
    }

    #[test]
    fn hostdox_gate_defaults_off_and_is_strict() {
        // Default-off — the opposite of the content subsystems.
        assert!(!decode_gates(b"").hostdox);
        assert!(!decode_gates(b"{}").hostdox);
        assert!(!decode_gates(b"not json").hostdox);
        assert!(!GateState::default().hostdox);
        // Off for explicit false / null / anything ambiguous.
        assert!(!decode_gates(br#"{"swarm_hostdox_enabled": false}"#).hostdox);
        assert!(!decode_gates(br#"{"swarm_hostdox_enabled": null}"#).hostdox);
        assert!(!decode_gates(br#"{"swarm_hostdox_enabled": "maybe"}"#).hostdox);
        // On only for an explicit truthy value.
        assert!(decode_gates(br#"{"swarm_hostdox_enabled": true}"#).hostdox);
        assert!(decode_gates(br#"{"swarm_hostdox_enabled": "true"}"#).hostdox);
        assert!(decode_gates(br#"{"swarm_hostdox_enabled": 1}"#).hostdox);
    }
}
