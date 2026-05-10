// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

//! Per-envoy swarmgit sync triggered by puddle rekey (ADR-019).
//!
//! Sibling of [`crate::swarm::lockbox::identity_rewrap`] in shape and
//! intent: a localized state migration that runs during the
//! puddle `rekey-pair` / `rekey` ceremony, while the OLD signing
//! key is still unwrapped in process memory.
//!
//! ### Two halves
//!
//! 1. **Pre-flight cap check** ([`pre_flight_check_caps`]): scans every
//!    project the user founded, compares each project's delegation
//!    chain length against [`super::envelopes::DELEGATION_MAX_LEN`].
//!    Returns the list of projects at the cap. The caller (the
//!    `cmd_rekey_pair` verb) decides whether to abort the rekey or
//!    continue under `--force-orphan-projects`. Runs BEFORE any
//!    keypair mutation so a cap-blocked project doesn't leave the
//!    user mid-swap.
//!
//! 2. **Post-rotation sync** ([`apply_post_rotation_swarmgit_sync`]):
//!    runs alongside [`crate::swarm::lockbox::identity_rewrap`] in
//!    the rekey ceremony. For every project the user founded, signs
//!    a fresh delegation envelope under OLD and appends it to
//!    `~/.vigo-swarmgit/<project_id>/delegations.json`. For every
//!    project where the user is a member (founder OR not), rewrites
//!    `project.json::members[]` to swap OLD → NEW so the user's
//!    current puddle identity tracks across rotations.
//!
//! ### Founder predicate (ADR-019 cross-rekey membership)
//!
//! "Founded by me" means `project.founder_puddle_pubkey` is in the set
//! `{my current puddle pubkey} ∪ {old_puddle_pubkey for every entry
//! in retired_puddles.json}`. This recovers the full history
//! `genesis → rekey_1 → rekey_2 → … → current` from
//! `~/.vigo-puddle/retired_puddles.json` plus `identity.pub`. A project
//! founded at any past identity counts as "mine" for delegation
//! purposes.
//!
//! ### What this module does NOT do
//!
//! - Does not touch `project.json::founder_puddle_pubkey`. That field
//!   is the genesis pubkey baked into `project_id` via sha256 — it
//!   stays immutable across all rekeys. ADR-019's whole point is
//!   keeping `project_id` stable so URLs survive.
//! - Does not gossip delegations to peers. That's slice 4 — and even
//!   then the gossip is a status hint, never authority.
//! - Does not handle multi-puddle member re-invitation. Foreign-puddle
//!   members need a fresh OOB capability code (slice 4's `re-invite-all`
//!   + `rejoin` flow); this module only updates the local user's own
//!   member entry across all projects.

use std::path::Path;

use anyhow::{Context, Result};
use ed25519_dalek::SigningKey;

use super::envelopes::{DelegationEnvelope, DELEGATION_MAX_LEN};
use super::project_state::{
    list_projects, parse_hex_id, DelegationChain, ProjectConfig, TokenChain, PROJECT_STATE_VERSION,
};
use super::tokens::{Role, Token, TokenBody, ID_LEN};

/// Outcome of [`apply_post_rotation_swarmgit_sync`]. Surfaced to
/// stdout in the rekey verb so the operator can see exactly what
/// changed across their swarmgit state.
#[derive(Debug, Default, Clone, PartialEq, Eq)]
pub struct SwarmgitRekeyOutcome {
    /// Projects where the local user was the founder, and a fresh
    /// delegation envelope was signed and appended to
    /// `delegations.json`. Strings are the project handle for
    /// display — `project_id` available via the per-project state
    /// path if needed.
    pub delegations_signed: Vec<String>,
    /// Projects where the local user appeared in `members[]` and the
    /// entry was rewritten from OLD → NEW. Includes founded projects
    /// (the founder is a member of their own project) plus any
    /// projects the user joined as a foreign-puddle member.
    pub members_updated: Vec<String>,
    /// Projects skipped because the local user wasn't a member (the
    /// OLD pubkey wasn't in `members[]`, even though they may have
    /// `~/.vigo-swarmgit/<pid>/` state). These are stale or
    /// pre-leave residues; not failures.
    pub members_unchanged: Vec<String>,
    /// Projects where the local user is the founder and `tokens.json`
    /// was rebuilt with `[bootstrap, fresh_admin_under_NEW]` — keeps
    /// the user's authority leaf current under the new signing
    /// identity so `cmd_invite` / `cmd_re_invite_all` can mint
    /// further tokens. Always a subset of `delegations_signed`.
    pub tokens_rebuilt: Vec<String>,
    /// Projects where appending a delegation failed and the operator
    /// will need to investigate (chain at cap and `--force-orphan-projects`
    /// was passed; signature failure; disk write error). Each entry
    /// is `(handle, error_string)`.
    pub failures: Vec<(String, String)>,
    /// Projects whose post-append chain length is in the warn band
    /// (≥ [`super::envelopes::DELEGATION_WARN_LEN`] but ≤ cap). Status
    /// surface paints these yellow so the operator knows the chain is
    /// getting long.
    pub in_warn_band: Vec<String>,
}

/// Result of the pre-flight cap check. Returned to the rekey verb
/// before any keypair mutation — if `at_cap` is non-empty and the
/// caller does not pass `--force-orphan-projects`, abort the rekey.
#[derive(Debug, Default, Clone, PartialEq, Eq)]
pub struct PreFlightCheck {
    /// Projects where the chain is already at [`DELEGATION_MAX_LEN`].
    /// One more rekey cannot append; operator must
    /// `swarmgit project re-found <handle>` (slice 5) or pass
    /// `--force-orphan-projects` to mark these projects frozen.
    pub at_cap: Vec<String>,
}

/// Run the pre-flight cap check. Walks every project the user
/// founded; collects those whose delegation chain is at
/// [`DELEGATION_MAX_LEN`]. Caller MUST run this before any keypair
/// mutation. Empty `at_cap` means it's safe to proceed; non-empty
/// is the caller's signal to abort (or honor `--force-orphan-projects`).
///
/// Returns `Ok` even when at-cap projects exist — the caller decides
/// the policy. Errors only on unexpected I/O.
pub fn pre_flight_check_caps(home: &Path, old_pubkey_text: &str) -> Result<PreFlightCheck> {
    let founded = founded_projects_for(home, old_pubkey_text)?;
    let mut at_cap = Vec::new();
    for cfg in founded {
        let pid = parse_hex_id(&cfg.project_id)
            .with_context(|| format!("project_id hex for {}", cfg.handle))?;
        let chain = DelegationChain::load(home, &pid)
            .with_context(|| format!("loading delegations for {}", cfg.handle))?
            .unwrap_or_default();
        if chain.is_at_cap() {
            at_cap.push(cfg.handle.clone());
        }
    }
    at_cap.sort();
    Ok(PreFlightCheck { at_cap })
}

/// Sign + append delegations for every founded project, then update
/// `members[]` entries OLD → NEW across every project the user is in.
/// Runs after the keypair has swapped on disk; the OLD signing key is
/// still in process memory because the caller passes it in here.
///
/// `force_orphan_projects` skips delegation signing for projects at
/// cap (returns success with the project flagged in `failures` so the
/// operator's status surface can show "frozen-at-rekey"). Without it,
/// at-cap projects fail this function — the pre-flight check should
/// have already caught them.
pub fn apply_post_rotation_swarmgit_sync(
    home: &Path,
    old_signing_key: &SigningKey,
    new_signing_key: &SigningKey,
    old_pubkey_text: &str,
    new_pubkey_text: &str,
    delegated_at: i64,
    force_orphan_projects: bool,
) -> Result<SwarmgitRekeyOutcome> {
    let mut outcome = SwarmgitRekeyOutcome::default();

    // Decode the wire forms once.
    let old_raw = decode_pubkey_text(old_pubkey_text)
        .context("decoding OLD puddle pubkey for delegation signing")?;
    let new_raw = decode_pubkey_text(new_pubkey_text)
        .context("decoding NEW puddle pubkey for delegation signing")?;
    // Sanity: caller's signing_key public halves must match the
    // pubkey-text inputs — otherwise we'd produce envelopes whose
    // signature doesn't match the embedded pubkey, failing
    // self-verification immediately.
    let old_signer_pub_bytes: [u8; ID_LEN] = old_signing_key.verifying_key().to_bytes();
    if old_signer_pub_bytes != old_raw {
        anyhow::bail!(
            "OLD signing key public half doesn't match old_pubkey_text — \
             refusing to sign delegations under a key/text mismatch"
        );
    }
    let new_signer_pub_bytes: [u8; ID_LEN] = new_signing_key.verifying_key().to_bytes();
    if new_signer_pub_bytes != new_raw {
        anyhow::bail!(
            "NEW signing key public half doesn't match new_pubkey_text — \
             refusing to mint a fresh admin token under a key/text mismatch"
        );
    }

    let projects = list_projects(home).context("listing projects under ~/.vigo-swarmgit")?;
    let founded_set = founded_pubkey_set(home, old_pubkey_text)?;
    let old_pubkey_hex = old_pubkey_text
        .strip_prefix("ed25519:")
        .unwrap_or(old_pubkey_text)
        .trim()
        .to_ascii_lowercase();
    let new_pubkey_hex = new_pubkey_text
        .strip_prefix("ed25519:")
        .unwrap_or(new_pubkey_text)
        .trim()
        .to_ascii_lowercase();

    for cfg in projects {
        let pid = match parse_hex_id(&cfg.project_id) {
            Ok(id) => id,
            Err(e) => {
                outcome
                    .failures
                    .push((cfg.handle.clone(), format!("project_id hex: {}", e)));
                continue;
            }
        };

        // 1. Founded-by-me: sign + append delegation, rebuild tokens.json.
        if founded_set.contains(&cfg.founder_puddle_pubkey) {
            let mut delegation_signed = false;
            match append_delegation(home, &pid, &cfg, old_signing_key, &old_raw, &new_raw, delegated_at, force_orphan_projects) {
                Ok(SignResult::Signed { in_warn_band }) => {
                    outcome.delegations_signed.push(cfg.handle.clone());
                    if in_warn_band {
                        outcome.in_warn_band.push(cfg.handle.clone());
                    }
                    delegation_signed = true;
                }
                Ok(SignResult::ForceOrphaned) => {
                    // chain at cap, force_orphan_projects honored —
                    // mark the project tombstoned on disk and record
                    // as a failure for visibility (doesn't propagate
                    // as Err so the rest of the sweep continues).
                    if let Err(e) = mark_tombstoned(home, &cfg, delegated_at) {
                        outcome.failures.push((
                            cfg.handle.clone(),
                            format!(
                                "project at delegation cap ({}/{}); failed to mark tombstoned: {:#}",
                                DELEGATION_MAX_LEN, DELEGATION_MAX_LEN, e
                            ),
                        ));
                    } else {
                        outcome.failures.push((
                            cfg.handle.clone(),
                            format!(
                                "project at delegation cap ({}/{}); marked tombstoned by --force-orphan-projects (run `swarmgit project re-found {}` to mint a fresh project_id and migrate)",
                                DELEGATION_MAX_LEN, DELEGATION_MAX_LEN, cfg.handle
                            ),
                        ));
                    }
                }
                Err(e) => {
                    outcome.failures.push((cfg.handle.clone(), format!("{:#}", e)));
                    continue;
                }
            }

            // Rebuild tokens.json so the founder's leaf authority is
            // a fresh admin token signed under NEW. Without this,
            // post-rekey `cmd_invite` / `cmd_re_invite_all` would
            // fail at the `parent.subject == local_pub` check (their
            // existing leaf was signed by genesis under the old
            // identity). Skipped when the delegation didn't actually
            // sign (force_orphan_projects branch above) — rebuilding
            // tokens.json against an unsigned delegation chain would
            // produce a leaf that fails verify_chain.
            if delegation_signed {
                if let Err(e) = rebuild_tokens_json(
                    home,
                    &pid,
                    &cfg,
                    new_signing_key,
                    &new_raw,
                    delegated_at,
                ) {
                    outcome
                        .failures
                        .push((cfg.handle.clone(), format!("tokens.json rebuild: {:#}", e)));
                } else {
                    outcome.tokens_rebuilt.push(cfg.handle.clone());
                }
            }
        }

        // 2. Member-of: rewrite project.json members entry OLD → NEW.
        // Applies to founded projects too (founder appears in members).
        // ADR-019 slice 5: tombstoned projects are frozen archives —
        // skip the member rewrite so we don't clobber the tombstone
        // marker mark_tombstoned just wrote (the in-memory `cfg` is
        // stale at this point).
        let still_active = ProjectConfig::load(home, &pid)
            .ok()
            .flatten()
            .map(|c| c.tombstoned_at.is_none())
            .unwrap_or(true);
        if still_active {
            match update_member_entry(home, &cfg, &old_pubkey_hex, &new_pubkey_hex) {
                Ok(true) => outcome.members_updated.push(cfg.handle.clone()),
                Ok(false) => outcome.members_unchanged.push(cfg.handle.clone()),
                Err(e) => outcome
                    .failures
                    .push((cfg.handle.clone(), format!("members[] rewrite: {:#}", e))),
            }
        }
    }

    Ok(outcome)
}

enum SignResult {
    Signed { in_warn_band: bool },
    ForceOrphaned,
}

fn append_delegation(
    home: &Path,
    project_id: &[u8; ID_LEN],
    cfg: &ProjectConfig,
    old_signing_key: &SigningKey,
    old_raw: &[u8; ID_LEN],
    new_raw: &[u8; ID_LEN],
    delegated_at: i64,
    force_orphan_projects: bool,
) -> Result<SignResult> {
    let mut chain = DelegationChain::load(home, project_id)
        .with_context(|| format!("loading delegations for {}", cfg.handle))?
        .unwrap_or_default();
    if chain.version == 0 {
        chain.version = PROJECT_STATE_VERSION;
    }

    if chain.is_at_cap() {
        if force_orphan_projects {
            return Ok(SignResult::ForceOrphaned);
        }
        anyhow::bail!(
            "delegation chain at cap ({}/{}); pre-flight check should have caught this",
            chain.len(),
            DELEGATION_MAX_LEN
        );
    }

    // Sanity: the predecessor of the new delegation must equal the OLD
    // pubkey we're retiring. If chain is empty, predecessor =
    // project.founder_puddle_pubkey. If non-empty, predecessor =
    // chain.last().new_founder_pubkey. Either way it MUST equal old_raw.
    let expected_predecessor: [u8; ID_LEN] = if let Some(last) = chain.delegations.last() {
        parse_hex_id(&last.new_founder_pubkey).with_context(|| {
            format!(
                "decoding last delegation's new_founder_pubkey for {}",
                cfg.handle
            )
        })?
    } else {
        parse_hex_id(&cfg.founder_puddle_pubkey).with_context(|| {
            format!("decoding founder_puddle_pubkey for {}", cfg.handle)
        })?
    };
    if &expected_predecessor != old_raw {
        anyhow::bail!(
            "delegation chain predecessor for {} ({}…) doesn't match OLD pubkey ({}…) — \
             local state diverged from the rekey ceremony",
            cfg.handle,
            hex::encode(&expected_predecessor[..4]),
            hex::encode(&old_raw[..4])
        );
    }

    let envelope = DelegationEnvelope::build(
        old_signing_key,
        project_id,
        new_raw,
        delegated_at,
    );
    chain.append(home, project_id, envelope)
        .with_context(|| format!("appending delegation for {}", cfg.handle))?;
    let in_warn_band = chain.is_in_warn_band();
    Ok(SignResult::Signed { in_warn_band })
}

/// Set `project.json::tombstoned_at` on a project that the rekey
/// flow has decided to leave behind (chain-at-cap +
/// `--force-orphan-projects`). The bare repo + URL stay on disk; the
/// helper daemon and CLI verbs honor the tombstone marker by
/// refusing new admin operations until the operator runs
/// `swarmgit project re-found <handle>`.
fn mark_tombstoned(home: &Path, cfg: &ProjectConfig, when: i64) -> Result<()> {
    let mut updated = cfg.clone();
    updated.tombstoned_at = Some(when);
    updated.updated_at = when;
    updated
        .save(home)
        .with_context(|| format!("saving tombstoned project.json for {}", cfg.handle))?;
    Ok(())
}

fn update_member_entry(
    home: &Path,
    cfg: &ProjectConfig,
    old_pubkey_hex: &str,
    new_pubkey_hex: &str,
) -> Result<bool> {
    // We only rewrite the entry; if the OLD pubkey isn't in members[],
    // the user is not currently a member and there's nothing to update.
    let mut updated = cfg.clone();
    let mut changed = false;
    for m in updated.members.iter_mut() {
        let m_lower = m.to_ascii_lowercase();
        if m_lower == old_pubkey_hex {
            *m = new_pubkey_hex.to_string();
            changed = true;
        }
    }
    if !changed {
        return Ok(false);
    }
    updated.updated_at = now_secs();
    updated
        .save(home)
        .with_context(|| format!("saving project.json for {}", cfg.handle))?;
    Ok(true)
}

/// Build the set of historical puddle pubkeys for the local user
/// (genesis through current). Reads `identity.pub` for current and
/// `retired_puddles.json` for the chain of retired keys. Pubkeys are
/// returned in `ed25519:<hex>` lowercase form so they compare directly
/// against `project.json::founder_puddle_pubkey` (also stored as hex
/// without the prefix in current code) — wait, ProjectConfig stores
/// `founder_puddle_pubkey` as bare hex, no `ed25519:` prefix. Caller
/// must compare appropriately.
fn founded_pubkey_set(
    home: &Path,
    old_pubkey_text: &str,
) -> Result<std::collections::HashSet<String>> {
    use std::collections::HashSet;
    let mut set: HashSet<String> = HashSet::new();
    // The OLD pubkey (current at this rekey moment) — strip prefix
    // to match project.json's bare-hex form.
    let old_bare = old_pubkey_text
        .strip_prefix("ed25519:")
        .unwrap_or(old_pubkey_text)
        .trim()
        .to_ascii_lowercase();
    set.insert(old_bare);
    // Retired-fleets log holds (old, new) pairs in `ed25519:<hex>` form;
    // the chain `genesis → rekey_1 → … → current` has each old being a
    // historical pubkey and each new appearing as the next entry's old
    // (or as `current`, which we already inserted). Adding every old +
    // every new covers the full history defensively.
    let retired_path = crate::swarm::puddle::rekey::retired_puddles_path_for(home);
    if let Ok(s) = std::fs::read_to_string(&retired_path) {
        if let Ok(entries) = serde_json::from_str::<Vec<crate::swarm::puddle::rekey::RetiredPuddleEntry>>(&s) {
            for e in entries {
                let old_bare = e
                    .old_puddle_pubkey
                    .strip_prefix("ed25519:")
                    .unwrap_or(&e.old_puddle_pubkey)
                    .trim()
                    .to_ascii_lowercase();
                let new_bare = e
                    .new_puddle_pubkey
                    .strip_prefix("ed25519:")
                    .unwrap_or(&e.new_puddle_pubkey)
                    .trim()
                    .to_ascii_lowercase();
                set.insert(old_bare);
                set.insert(new_bare);
            }
        }
    }
    Ok(set)
}

/// List every project whose `project.json::founder_puddle_pubkey` is
/// in the local user's historical pubkey set. Includes both
/// just-founded and previously-rotated projects.
fn founded_projects_for(home: &Path, old_pubkey_text: &str) -> Result<Vec<ProjectConfig>> {
    let founded_set = founded_pubkey_set(home, old_pubkey_text)?;
    let mut out = Vec::new();
    for cfg in list_projects(home)? {
        if founded_set.contains(&cfg.founder_puddle_pubkey.to_ascii_lowercase()) {
            out.push(cfg);
        }
    }
    Ok(out)
}

/// Decode a `ed25519:<hex>` puddle pubkey text into raw bytes.
fn decode_pubkey_text(text: &str) -> Result<[u8; ID_LEN]> {
    let bare = text
        .strip_prefix("ed25519:")
        .unwrap_or(text)
        .trim()
        .to_ascii_lowercase();
    parse_hex_id(&bare).with_context(|| format!("parsing pubkey hex from {}", text))
}

fn now_secs() -> i64 {
    std::time::SystemTime::now()
        .duration_since(std::time::UNIX_EPOCH)
        .map(|d| d.as_secs() as i64)
        .unwrap_or(0)
}

/// Just for tests/external callers needing to identify "founded by me"
/// without re-implementing the predicate.
#[allow(dead_code)]
pub fn is_founded_by_local_user(
    home: &Path,
    old_pubkey_text: &str,
    cfg: &ProjectConfig,
) -> Result<bool> {
    let set = founded_pubkey_set(home, old_pubkey_text)?;
    Ok(set.contains(&cfg.founder_puddle_pubkey.to_ascii_lowercase()))
}

// Re-export for convenience — callers don't need to reach into the
// internals when they want the project-id hex form for diagnostic
// printing.
pub use super::project_state::hex_id as project_id_hex;

/// Replace `~/.vigo-swarmgit/<pid>/tokens.json` with `[bootstrap,
/// fresh_admin_under_NEW]`. Called after a successful delegation
/// append so the founder's local authority leaf reflects their NEW
/// signing identity. The bootstrap is loaded from the *existing*
/// tokens.json (it's content-addressed, so the bootstrap is
/// identical across rekeys; loading rather than synthesizing keeps
/// us tied to the actual on-disk state).
fn rebuild_tokens_json(
    home: &Path,
    project_id: &[u8; ID_LEN],
    cfg: &ProjectConfig,
    new_signing_key: &SigningKey,
    new_pubkey: &[u8; ID_LEN],
    issued_at: i64,
) -> Result<()> {
    let existing = TokenChain::load(home, project_id)
        .with_context(|| format!("loading tokens.json for {}", cfg.handle))?
        .ok_or_else(|| {
            anyhow::anyhow!(
                "tokens.json missing for {} — founded-by-me predicate matched but no chain on disk",
                cfg.handle
            )
        })?;
    let bootstrap = existing
        .chain
        .first()
        .cloned()
        .ok_or_else(|| anyhow::anyhow!("tokens.json for {} has empty chain", cfg.handle))?;

    // Sanity: bootstrap.parent_token_id must be None and signer must
    // be the genesis founder pubkey baked into project_id. We're
    // about to extend the chain off this bootstrap; if it's malformed,
    // refuse rather than write a chain that won't verify.
    if bootstrap.body.parent_token_id.is_some() {
        anyhow::bail!(
            "tokens.json for {}: chain[0] has parent_token_id set; not a bootstrap",
            cfg.handle
        );
    }

    let body = TokenBody {
        project_id: *project_id,
        subject_puddle_pubkey: *new_pubkey,
        role: Role::Admin,
        issued_at,
        expires_at: None,
        signer_puddle_pubkey: *new_pubkey,
        parent_token_id: Some(bootstrap.id()),
    };
    let admin = Token::issue(new_signing_key, body)
        .with_context(|| format!("minting fresh admin under NEW for {}", cfg.handle))?;

    let rebuilt = TokenChain {
        version: PROJECT_STATE_VERSION,
        chain: vec![bootstrap, admin],
    };
    rebuilt
        .save(home, project_id)
        .with_context(|| format!("saving rebuilt tokens.json for {}", cfg.handle))?;
    Ok(())
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::swarm::swarmgit::project_state::hex_id;
    use crate::swarm::swarmgit::tokens::{issue_bootstrap, project_id_for};
    use crate::swarm::puddle::rekey::{record_retirement, REKEY_GRACEFUL_SECS};
    use ed25519_dalek::SigningKey;
    use rand::rngs::OsRng;
    use std::fs;
    use tempfile::tempdir;

    fn fresh_key() -> SigningKey {
        SigningKey::generate(&mut OsRng)
    }

    fn pub_bytes(k: &SigningKey) -> [u8; ID_LEN] {
        k.verifying_key().to_bytes()
    }

    fn pub_text(k: &SigningKey) -> String {
        format!("ed25519:{}", hex::encode(pub_bytes(k)))
    }

    fn sample_founder_project(
        home: &std::path::Path,
        founder: &SigningKey,
        handle: &str,
    ) -> ProjectConfig {
        let pid = project_id_for(&pub_bytes(founder));
        let founder_hex = hex::encode(pub_bytes(founder));
        let cfg = ProjectConfig {
            version: PROJECT_STATE_VERSION,
            project_id: hex_id(&pid),
            handle: handle.to_string(),
            founder_puddle_pubkey: founder_hex.clone(),
            members: vec![founder_hex],
            dr_scope: super::super::project_state::DrScope::SwarmWide,
            cross_subnet_members: vec![],
            updated_at: 1_700_000_000,
            tombstoned_at: None,
        };
        cfg.save(home).unwrap();
        // Persist a real bootstrap token chain so rebuild_tokens_json
        // (slice 4) can load chain[0] back. The rebuild path is part
        // of apply_post_rotation_swarmgit_sync's contract for
        // founded projects.
        let bootstrap = issue_bootstrap(founder, 100, None).unwrap();
        let chain = TokenChain {
            version: PROJECT_STATE_VERSION,
            chain: vec![bootstrap],
        };
        chain.save(home, &pid).unwrap();
        cfg
    }

    fn write_identity_pub(home: &std::path::Path, key: &SigningKey) {
        let dot = home.join(".vigo-puddle");
        fs::create_dir_all(&dot).unwrap();
        fs::write(dot.join("identity.pub"), pub_text(key)).unwrap();
    }

    #[test]
    fn pre_flight_check_clean_when_no_projects() {
        let home = tempdir().unwrap();
        let founder = fresh_key();
        let result = pre_flight_check_caps(home.path(), &pub_text(&founder)).unwrap();
        assert!(result.at_cap.is_empty());
    }

    #[test]
    fn pre_flight_check_clean_when_chain_below_cap() {
        let home = tempdir().unwrap();
        let founder = fresh_key();
        sample_founder_project(home.path(), &founder, "alpha");
        let result = pre_flight_check_caps(home.path(), &pub_text(&founder)).unwrap();
        assert!(result.at_cap.is_empty());
    }

    #[test]
    fn pre_flight_check_flags_at_cap() {
        let home = tempdir().unwrap();
        let founder = fresh_key();
        let cfg = sample_founder_project(home.path(), &founder, "alpha");
        let pid = parse_hex_id(&cfg.project_id).unwrap();
        // Build a chain at exactly DELEGATION_MAX_LEN.
        let mut chain = DelegationChain {
            version: PROJECT_STATE_VERSION,
            delegations: Vec::new(),
        };
        let mut prev = founder.clone();
        for i in 0..DELEGATION_MAX_LEN {
            let next = fresh_key();
            chain.delegations.push(DelegationEnvelope::build(
                &prev,
                &pid,
                &pub_bytes(&next),
                1_700_000_000 + i as i64,
            ));
            prev = next;
        }
        chain.save(home.path(), &pid).unwrap();

        let result = pre_flight_check_caps(home.path(), &pub_text(&founder)).unwrap();
        assert_eq!(result.at_cap, vec!["alpha".to_string()]);
    }

    #[test]
    fn apply_post_rotation_signs_delegation_for_founder() {
        let home = tempdir().unwrap();
        let old = fresh_key();
        let new = fresh_key();
        let cfg = sample_founder_project(home.path(), &old, "alpha");
        write_identity_pub(home.path(), &old);

        let outcome = apply_post_rotation_swarmgit_sync(
            home.path(),
            &old,
            &new,
            &pub_text(&old),
            &pub_text(&new),
            1_700_000_500,
            false,
        )
        .unwrap();

        assert_eq!(outcome.delegations_signed, vec!["alpha".to_string()]);
        assert_eq!(outcome.members_updated, vec!["alpha".to_string()]);
        assert_eq!(outcome.tokens_rebuilt, vec!["alpha".to_string()]);
        assert!(outcome.failures.is_empty());

        // On-disk: delegation chain length 1, signed by old, names new.
        let pid = parse_hex_id(&cfg.project_id).unwrap();
        let chain = DelegationChain::load(home.path(), &pid).unwrap().unwrap();
        assert_eq!(chain.delegations.len(), 1);
        assert_eq!(
            chain.delegations[0].old_founder_pubkey,
            hex::encode(pub_bytes(&old))
        );
        assert_eq!(
            chain.delegations[0].new_founder_pubkey,
            hex::encode(pub_bytes(&new))
        );
        // members[] entry rewritten OLD → NEW.
        let reloaded = ProjectConfig::load(home.path(), &pid).unwrap().unwrap();
        assert_eq!(reloaded.members, vec![hex::encode(pub_bytes(&new))]);
        // founder_puddle_pubkey untouched (genesis stays).
        assert_eq!(reloaded.founder_puddle_pubkey, hex::encode(pub_bytes(&old)));
        // tokens.json rebuilt: chain length 2, leaf signed by NEW.
        let rebuilt = TokenChain::load(home.path(), &pid).unwrap().unwrap();
        assert_eq!(rebuilt.chain.len(), 2);
        assert_eq!(rebuilt.chain[1].body.signer_puddle_pubkey, pub_bytes(&new));
        assert_eq!(rebuilt.chain[1].body.subject_puddle_pubkey, pub_bytes(&new));
        assert_eq!(rebuilt.chain[1].body.role, Role::Admin);
    }

    #[test]
    fn apply_post_rotation_skips_projects_user_isnt_member_of() {
        let home = tempdir().unwrap();
        let old = fresh_key();
        let new = fresh_key();
        let other_user = fresh_key();
        // Project founded by someone else; local user not a member.
        let pid = project_id_for(&pub_bytes(&other_user));
        let cfg = ProjectConfig {
            version: PROJECT_STATE_VERSION,
            project_id: hex_id(&pid),
            handle: "other".to_string(),
            founder_puddle_pubkey: hex::encode(pub_bytes(&other_user)),
            members: vec![hex::encode(pub_bytes(&other_user))],
            dr_scope: super::super::project_state::DrScope::MembersOnly,
            cross_subnet_members: vec![],
            updated_at: 1_700_000_000,
            tombstoned_at: None,
        };
        cfg.save(home.path()).unwrap();
        write_identity_pub(home.path(), &old);

        let outcome = apply_post_rotation_swarmgit_sync(
            home.path(),
            &old,
            &new,
            &pub_text(&old),
            &pub_text(&new),
            1_700_000_500,
            false,
        )
        .unwrap();

        // No delegation signed (not founded by us), no member entry
        // updated (we're not in members[]).
        assert!(outcome.delegations_signed.is_empty());
        assert!(outcome.members_updated.is_empty());
        assert_eq!(outcome.members_unchanged, vec!["other".to_string()]);
        assert!(outcome.failures.is_empty());
    }

    #[test]
    fn apply_post_rotation_updates_member_entry_for_foreign_fleet_project() {
        // I'm a member (not founder) of a project. After rekey,
        // members[] should reflect my new pubkey but no delegation
        // is signed (only the founder signs delegations).
        let home = tempdir().unwrap();
        let old = fresh_key();
        let new = fresh_key();
        let other_founder = fresh_key();
        let pid = project_id_for(&pub_bytes(&other_founder));
        let cfg = ProjectConfig {
            version: PROJECT_STATE_VERSION,
            project_id: hex_id(&pid),
            handle: "shared".to_string(),
            founder_puddle_pubkey: hex::encode(pub_bytes(&other_founder)),
            members: vec![
                hex::encode(pub_bytes(&other_founder)),
                hex::encode(pub_bytes(&old)),
            ],
            dr_scope: super::super::project_state::DrScope::MembersOnly,
            cross_subnet_members: vec![],
            updated_at: 1_700_000_000,
            tombstoned_at: None,
        };
        cfg.save(home.path()).unwrap();
        write_identity_pub(home.path(), &old);

        let outcome = apply_post_rotation_swarmgit_sync(
            home.path(),
            &old,
            &new,
            &pub_text(&old),
            &pub_text(&new),
            1_700_000_500,
            false,
        )
        .unwrap();

        assert!(outcome.delegations_signed.is_empty());
        assert_eq!(outcome.members_updated, vec!["shared".to_string()]);
        assert!(outcome.failures.is_empty());

        // Reloaded: my entry is now NEW, other founder unchanged.
        let reloaded = ProjectConfig::load(home.path(), &pid).unwrap().unwrap();
        assert!(reloaded.members.contains(&hex::encode(pub_bytes(&new))));
        assert!(reloaded
            .members
            .contains(&hex::encode(pub_bytes(&other_founder))));
        assert!(!reloaded.members.contains(&hex::encode(pub_bytes(&old))));
    }

    #[test]
    fn apply_post_rotation_signs_two_chained_delegations_across_two_rekeys() {
        // Founder rekeys twice. After the second rekey, the project's
        // delegation chain has two entries: genesis→r1, then r1→r2.
        // Verifies the predecessor sanity check accepts each.
        let home = tempdir().unwrap();
        let genesis = fresh_key();
        let r1 = fresh_key();
        let r2 = fresh_key();
        let cfg = sample_founder_project(home.path(), &genesis, "alpha");
        let pid = parse_hex_id(&cfg.project_id).unwrap();

        // Simulate first rekey: identity.pub is genesis → record
        // retirement → run sync with old=genesis, new=r1.
        write_identity_pub(home.path(), &genesis);
        record_retirement(
            home.path(),
            "tester",
            &pub_text(&genesis),
            &pub_text(&r1),
            1_700_000_100,
            REKEY_GRACEFUL_SECS,
        )
        .unwrap();
        apply_post_rotation_swarmgit_sync(
            home.path(),
            &genesis,
            &r1,
            &pub_text(&genesis),
            &pub_text(&r1),
            1_700_000_100,
            false,
        )
        .unwrap();
        // Identity is now r1 — overwrite identity.pub.
        write_identity_pub(home.path(), &r1);

        // Second rekey: old=r1, new=r2.
        record_retirement(
            home.path(),
            "tester",
            &pub_text(&r1),
            &pub_text(&r2),
            1_700_000_200,
            REKEY_GRACEFUL_SECS,
        )
        .unwrap();
        let outcome = apply_post_rotation_swarmgit_sync(
            home.path(),
            &r1,
            &r2,
            &pub_text(&r1),
            &pub_text(&r2),
            1_700_000_200,
            false,
        )
        .unwrap();
        assert_eq!(outcome.delegations_signed, vec!["alpha".to_string()]);

        let chain = DelegationChain::load(home.path(), &pid).unwrap().unwrap();
        assert_eq!(chain.delegations.len(), 2);
        assert_eq!(
            chain.delegations[0].new_founder_pubkey,
            hex::encode(pub_bytes(&r1))
        );
        assert_eq!(
            chain.delegations[1].old_founder_pubkey,
            hex::encode(pub_bytes(&r1))
        );
        assert_eq!(
            chain.delegations[1].new_founder_pubkey,
            hex::encode(pub_bytes(&r2))
        );
        let reloaded = ProjectConfig::load(home.path(), &pid).unwrap().unwrap();
        assert!(reloaded.members.contains(&hex::encode(pub_bytes(&r2))));
    }

    /// Simulate a user who has rekeyed `DELEGATION_MAX_LEN` times in a
    /// row: builds a project chain at cap, populates retired_puddles.json
    /// to match (so the founder predicate recognizes every historical
    /// pubkey as "mine"), and updates project.json::members[] to track
    /// the most recent identity. Returns (founder_initial_key,
    /// last_signing_key, project_id) so the test can drive the next
    /// rekey attempt against the prefilled state.
    fn setup_chain_at_cap(
        home: &std::path::Path,
        handle: &str,
    ) -> (SigningKey, SigningKey, [u8; ID_LEN]) {
        let founder = fresh_key();
        let cfg = sample_founder_project(home, &founder, handle);
        let pid = parse_hex_id(&cfg.project_id).unwrap();

        let mut chain = DelegationChain {
            version: PROJECT_STATE_VERSION,
            delegations: Vec::new(),
        };
        let mut prev_key = founder.clone();
        for i in 0..DELEGATION_MAX_LEN {
            let next = fresh_key();
            chain.delegations.push(DelegationEnvelope::build(
                &prev_key,
                &pid,
                &pub_bytes(&next),
                1_700_000_000 + i as i64,
            ));
            // Record this rekey hop in the founder's retired_puddles log.
            record_retirement(
                home,
                "tester",
                &pub_text(&prev_key),
                &pub_text(&next),
                1_700_000_000 + i as i64,
                REKEY_GRACEFUL_SECS,
            )
            .unwrap();
            prev_key = next;
        }
        chain.save(home, &pid).unwrap();
        // members[] should reflect the most-recent identity so the
        // post-rotation member-rewrite step finds something to update.
        let mut updated_cfg = cfg.clone();
        updated_cfg.members = vec![hex::encode(pub_bytes(&prev_key))];
        updated_cfg.save(home).unwrap();
        // identity.pub is the current key.
        write_identity_pub(home, &prev_key);
        (founder, prev_key, pid)
    }

    #[test]
    fn apply_post_rotation_at_cap_without_force_orphan_fails_for_that_project() {
        let home = tempdir().unwrap();
        let (_founder, last_signing_key, _pid) =
            setup_chain_at_cap(home.path(), "alpha");
        let new = fresh_key();
        let outcome = apply_post_rotation_swarmgit_sync(
            home.path(),
            &last_signing_key,
            &new,
            &pub_text(&last_signing_key),
            &pub_text(&new),
            1_700_000_999,
            false,
        )
        .unwrap();
        assert_eq!(outcome.delegations_signed.len(), 0);
        assert_eq!(outcome.failures.len(), 1);
        assert_eq!(outcome.failures[0].0, "alpha");
    }

    #[test]
    fn apply_post_rotation_at_cap_with_force_orphan_marks_tombstoned() {
        let home = tempdir().unwrap();
        let (_founder, last_signing_key, pid) =
            setup_chain_at_cap(home.path(), "alpha");
        let new = fresh_key();
        let outcome = apply_post_rotation_swarmgit_sync(
            home.path(),
            &last_signing_key,
            &new,
            &pub_text(&last_signing_key),
            &pub_text(&new),
            1_700_000_999,
            true, // force_orphan_projects
        )
        .unwrap();
        // ADR-019 slice 5: --force-orphan-projects now writes the
        // tombstone marker to project.json. Failure message points
        // the operator at `re-found` as the next step.
        assert_eq!(outcome.failures.len(), 1);
        assert!(
            outcome.failures[0].1.contains("tombstoned"),
            "expected 'tombstoned' in failure msg, got: {}",
            outcome.failures[0].1
        );
        assert!(
            outcome.failures[0].1.contains("re-found"),
            "expected 're-found' hint in failure msg, got: {}",
            outcome.failures[0].1
        );
        // Chain unchanged on disk.
        let chain_now = DelegationChain::load(home.path(), &pid).unwrap().unwrap();
        assert_eq!(chain_now.delegations.len(), DELEGATION_MAX_LEN);
        // project.json now carries the tombstone marker.
        let cfg = ProjectConfig::load(home.path(), &pid).unwrap().unwrap();
        assert_eq!(cfg.tombstoned_at, Some(1_700_000_999));
    }

    #[test]
    fn pre_flight_check_flags_at_cap_after_real_rekey_history() {
        // Sanity check: the predicate-with-retired-puddles path also
        // surfaces at-cap projects in pre-flight. Mirrors the
        // structure of the post-rotation tests above.
        let home = tempdir().unwrap();
        let (_founder, last_signing_key, _pid) =
            setup_chain_at_cap(home.path(), "alpha");
        let result =
            pre_flight_check_caps(home.path(), &pub_text(&last_signing_key)).unwrap();
        assert_eq!(result.at_cap, vec!["alpha".to_string()]);
    }

    #[test]
    fn apply_post_rotation_signing_key_pubkey_mismatch_errors() {
        let home = tempdir().unwrap();
        let founder = fresh_key();
        let other = fresh_key(); // signing key won't match old_pubkey_text
        sample_founder_project(home.path(), &founder, "alpha");
        write_identity_pub(home.path(), &founder);
        // Pass `other` as the signing key but `founder`'s pubkey as
        // old_pubkey_text — must reject loudly.
        let new = fresh_key();
        let result = apply_post_rotation_swarmgit_sync(
            home.path(),
            &other,
            &new,
            &pub_text(&founder),
            &pub_text(&new),
            1_700_000_500,
            false,
        );
        assert!(result.is_err());
    }
}
