// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

//! `dr_scope` enforcement for bundle ingest (ADR-025 Slice 3).
//!
//! The receiver-side gate that keeps `members-only` projects'
//! bytes off envoys that don't hold a member's puddle. v1 scope:
//! refuses bundle ingest when no local project state exists for
//! the incoming `project_id` under any puddle state root on this
//! host. The existence of state IS the v1 membership proof —
//! `init` / `join` is what creates it, so an envoy without state
//! has never been admitted.
//!
//! State roots checked:
//!   - `/srv/vigo/ops-puddle` (ops-puddle, ADR-025 Slice 2)
//!   - `~<user>/` for each user with `~<user>/.vigo-puddle/identity.pub`
//!     (per-user puddle, ADR-014)
//!
//! For ops-puddle stockpile projects: founder + every paired-in
//! vigosrv has `/srv/vigo/ops-puddle/.vigo-gitback/<id>/project.json`
//! after init/join — those pass. Non-member vigosrvs don't have the
//! ops-puddle dir at all — those refuse.
//!
//! For per-user projects: today's `mint_founder_project` and the
//! ADR-023 auto-adoption path both create state before bundles
//! arrive; the sender-side `fan_out_bundle` already skips
//! propagation when project.json is absent (see fan_out.rs:563-571),
//! so receiver-side state existence is symmetric with sender intent.
//!
//! Symmetric with sender behavior, so this gate does not introduce
//! a new break — it just catches the case where a sender DOES have
//! state but the receiver hasn't joined, which under members-only
//! semantics is exactly what we want to refuse.

use anyhow::{bail, Result};
use std::path::PathBuf;

use crate::swarm::anchor::tokens::ID_LEN;
use super::project_state::{hex_id, ProjectConfig};

const OPS_PUDDLE_DIR: &str = "/srv/vigo/ops-puddle";
const PER_USER_PUBKEY_REL: &str = ".vigo-puddle/identity.pub";

/// Refuses if `project_id` is not tracked under any puddle state
/// root on this host. Returns Ok(()) when at least one puddle root
/// has a loadable `project.json` for the project.
///
/// Test bypass: `VIGO_GITBACK_DR_SCOPE_BYPASS=1` short-circuits the
/// check so the existing bundle-mechanics tests in
/// `fan_out::tests` (which pre-date this gate and don't care about
/// membership) keep passing without per-test state scaffolding.
/// Production never sets this; the gate stays effective in every
/// real deployment.
pub fn ensure_locally_tracked(project_id: &[u8; ID_LEN]) -> Result<()> {
    if std::env::var_os("VIGO_GITBACK_DR_SCOPE_BYPASS").is_some() {
        return Ok(());
    }
    for root in puddle_state_roots() {
        match ProjectConfig::load(&root, project_id) {
            Ok(Some(_)) => return Ok(()),
            _ => continue,
        }
    }
    bail!(
        "gitback project {} is not locally tracked on this envoy; \
         refusing bundle ingest (members-only enforcement, ADR-025 Slice 3)",
        hex_id(project_id)
    );
}

/// Roots under which `project.json` files might live, in the order
/// they're consulted. Per-user roots come first to keep latency
/// proportional to the dominant case (per-user gitback today);
/// ops-puddle is checked last as a single extra stat.
fn puddle_state_roots() -> Vec<PathBuf> {
    let mut out = Vec::new();
    if let Ok(passwd) = std::fs::read_to_string("/etc/passwd") {
        for home in user_homes_from_passwd(&passwd) {
            if home.join(PER_USER_PUBKEY_REL).exists() {
                out.push(home);
            }
        }
    }
    let ops = PathBuf::from(OPS_PUDDLE_DIR);
    if ops.join("identity.pub").exists() {
        out.push(ops);
    }
    out
}

/// Walk a `/etc/passwd`-style body, returning each user's home dir.
/// Pulled out for testability; the v1 enumeration filters to the
/// conventional home prefixes that gitback already targets so we
/// don't pick up service accounts (sshd, nobody, etc.) whose home
/// is `/var/empty` or similar.
fn user_homes_from_passwd(body: &str) -> Vec<PathBuf> {
    let mut out = Vec::new();
    for line in body.lines() {
        let fields: Vec<&str> = line.split(':').collect();
        if fields.len() < 6 {
            continue;
        }
        let home = PathBuf::from(fields[5]);
        let path_str = home.to_string_lossy();
        if path_str.starts_with("/home/")
            || path_str.starts_with("/Users/")
            || path_str.starts_with("/root")
        {
            out.push(home);
        }
    }
    out
}

#[cfg(test)]
mod tests {
    use super::*;
    use std::fs;
    use tempfile::TempDir;

    #[test]
    fn user_homes_from_passwd_filters_service_accounts() {
        let body = "\
root:x:0:0:root:/root:/bin/bash
dan:x:1000:1000:dan,,,:/home/dan:/bin/bash
sshd:x:74:74:Privilege-separated SSH:/var/empty/sshd:/sbin/nologin
nobody:x:65534:65534:Nobody:/nonexistent:/sbin/nologin
me:x:501:20:Me:/Users/me:/bin/zsh
";
        let homes: Vec<String> = user_homes_from_passwd(body)
            .into_iter()
            .map(|p| p.display().to_string())
            .collect();
        assert!(homes.contains(&"/root".to_string()));
        assert!(homes.contains(&"/home/dan".to_string()));
        assert!(homes.contains(&"/Users/me".to_string()));
        // Service accounts (sshd, nobody) get filtered:
        assert!(!homes.iter().any(|h| h.starts_with("/var/empty")));
        assert!(!homes.iter().any(|h| h == "/nonexistent"));
    }

    #[test]
    #[serial_test::serial]
    fn ensure_locally_tracked_refuses_unknown_project() {
        // Serialized so other tests in the same binary aren't
        // mid-set when we clear the bypass var. No /srv/vigo/
        // ops-puddle on the test host (assumed) and a random
        // project_id won't be tracked under /home/* either.
        // SAFETY: serial_test serializes against any other test
        // tagged #[serial], including the fan_out bypass setters.
        unsafe {
            std::env::remove_var("VIGO_GITBACK_DR_SCOPE_BYPASS");
        }
        let pid: [u8; ID_LEN] = [0xab; ID_LEN];
        let err = ensure_locally_tracked(&pid).unwrap_err();
        assert!(
            err.to_string().contains("not locally tracked"),
            "expected refusal, got: {}",
            err
        );
    }

    #[test]
    fn project_config_load_path_matches_expected_layout() {
        // Sanity that we look in <root>/.vigo-gitback/<id>/project.json
        // — ops-puddle reuses this convention so /srv/vigo/ops-puddle/
        // .vigo-gitback/<id>/project.json is the lookup target.
        let temp = TempDir::new().unwrap();
        let root = temp.path();
        let pid: [u8; ID_LEN] = [0xcd; ID_LEN];
        let pid_hex = hex_id(&pid);
        let project_dir = root.join(".vigo-gitback").join(&pid_hex);
        fs::create_dir_all(&project_dir).unwrap();
        let json = format!(
            r#"{{
  "version": 2,
  "project_id": "{}",
  "handle": "test",
  "founder_puddle_pubkey": "{}",
  "members": ["{}"],
  "dr_scope": "members-only",
  "cross_subnet_members": [],
  "updated_at": 0,
  "tombstoned_at": null
}}"#,
            pid_hex, pid_hex, pid_hex
        );
        fs::write(project_dir.join("project.json"), json).unwrap();

        let cfg = ProjectConfig::load(root, &pid).unwrap();
        assert!(cfg.is_some(), "ProjectConfig::load should find state");
    }
}
