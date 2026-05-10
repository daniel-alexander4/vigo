// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

//! Per-project state for ADR-015 unified swarmgit substrate.
//!
//! ### Layout
//!
//! Each project on each envoy has two state surfaces:
//!
//! **Per-user, under `~<user>/.vigo-swarmgit/<project_id_hex>/`:**
//!
//! - `project.json` — local view of the project: handle, founder puddle
//!   pubkey (TOFU anchor), member puddle pubkeys, dr_scope policy,
//!   optional explicit cross-subnet member hostnames. Founder writes
//!   it at `project init`; joiner writes it at `project join`. In step 4
//!   gossip-driven membership changes refresh this file.
//! - `tokens.json` — the operator's authorization chain. The leaf token
//!   names this user's puddle pubkey as subject; ancestors walk back to
//!   the founder's bootstrap token. Loaded by the helper daemon (step 4)
//!   when proxying pushes.
//!
//! Both are mode `0600`, owned by the calling user. The dot-dir itself
//! is `0700`. The token chain is sensitive (acts as a bearer credential
//! in combination with the puddle key); the project config is not, but
//! lives next to it for symmetry.
//!
//! **Per-envoy, root-owned, at
//! `/var/lib/vigo/swarm/swarmgit/projects/<project_id_hex>.git/`:**
//!
//! The bare repo. Created by the helper daemon (step 4) on the first
//! `git-receive-pack` for the project. Permissions `0700 root:root` —
//! non-root local users go through the helper, which checks puddle
//! membership against `project.json::members` before allowing fetch or
//! push. Root retains direct filesystem access by design (matches
//! Vigo's overall posture: root is privileged on the box).
//!
//! The bare repo's own metadata (canonical project_id, last fan-out
//! ts, etc.) lives in `git config swarmgit.*` rather than a parallel
//! JSON sidecar — keeps the bare repo self-describing for ops.
//!
//! ### What lives where, and why
//!
//! - **Token chain is per-user**, not per-envoy: each user on an envoy
//!   pushes under their own puddle identity (`puddle` is per-user,
//!   per ADR-014). Two users on the same host with separate fleets see
//!   independent swarmgit surfaces.
//! - **Project config is per-user**, not per-envoy: the canonical
//!   answer to "who is a member of project X" is what gossip says, and
//!   gossip arrives via the per-user puddle membership signal. Each user
//!   carrying their own copy keeps the puddle abstraction clean.
//! - **Bare repo is per-envoy**: storage scales O(1) per project on the
//!   envoy, not O(users-on-envoy). The helper daemon mediates access.
//!
//! ### Hex encoding
//!
//! `project_id` and puddle pubkeys serialize as 64-character lowercase
//! hex in JSON for human readability. Helpers
//! ([`hex_id`], [`parse_hex_id`]) convert to/from raw `[u8; 32]`.

use std::fs;
use std::io::Write;
use std::path::{Path, PathBuf};

use anyhow::{anyhow, bail, Context, Result};
use serde::{Deserialize, Serialize};

use super::envelopes::{DelegationEnvelope, DELEGATION_MAX_LEN};
use super::tokens::{Token, ID_LEN};

/// Schema version for `project.json` and `tokens.json` files. Bump on
/// incompatible changes; per zero-users doctrine the loader rejects
/// older versions and treats them as missing rather than migrating.
pub const PROJECT_STATE_VERSION: u32 = 1;

/// Per-user state directory: `~<user>/.vigo-swarmgit/`.
///
/// `~/.vigo-swarmgit/` (not `~/.swarmgit/`) — the `.vigo-` prefix
/// marks it as agent-managed state, distinct from any user-owned
/// `~/swarmgit/` working directory. Existing in step 1's name will
/// keep it disjoint from anything a user might have under `~/swarmgit/`.
pub fn user_dot_dir(home: &Path) -> PathBuf {
    home.join(".vigo-swarmgit")
}

/// Per-project user state directory:
/// `~<user>/.vigo-swarmgit/<project_id_hex>/`.
pub fn user_project_dir(home: &Path, project_id: &[u8; ID_LEN]) -> PathBuf {
    user_dot_dir(home).join(hex_id(project_id))
}

/// `project.json` path under the per-user project dir.
pub fn project_config_path(home: &Path, project_id: &[u8; ID_LEN]) -> PathBuf {
    user_project_dir(home, project_id).join("project.json")
}

/// `tokens.json` path under the per-user project dir.
pub fn token_chain_path(home: &Path, project_id: &[u8; ID_LEN]) -> PathBuf {
    user_project_dir(home, project_id).join("tokens.json")
}

/// `delegations.json` path under the per-user project dir (ADR-019).
/// Holds the ordered list of founder-rekey delegations bridging the
/// project's genesis pubkey to the founder's current signing key.
pub fn delegations_path(home: &Path, project_id: &[u8; ID_LEN]) -> PathBuf {
    user_project_dir(home, project_id).join("delegations.json")
}

/// `<swarmgit_dir>/projects/` — root-owned dir holding every bare
/// repo on this envoy. Mode `0700 root:root` once created.
pub fn projects_dir(swarmgit_base: &Path) -> PathBuf {
    swarmgit_base.join("projects")
}

/// Bare-repo path for a project:
/// `<swarmgit_dir>/projects/<project_id_hex>.git`.
pub fn project_repo_path(swarmgit_base: &Path, project_id: &[u8; ID_LEN]) -> PathBuf {
    projects_dir(swarmgit_base).join(format!("{}.git", hex_id(project_id)))
}

/// Encode a 32-byte ID as 64-character lowercase hex.
pub fn hex_id(id: &[u8; ID_LEN]) -> String {
    hex::encode(id)
}

/// Parse a 64-character lowercase hex string back to a 32-byte ID.
/// Rejects any string of the wrong length, non-hex characters, or
/// uppercase hex (canonical form is lowercase).
pub fn parse_hex_id(s: &str) -> Result<[u8; ID_LEN]> {
    if s.len() != ID_LEN * 2 {
        bail!("expected {}-char hex id, got {} chars", ID_LEN * 2, s.len());
    }
    if s.chars().any(|c| c.is_ascii_uppercase()) {
        bail!("project id must be lowercase hex");
    }
    let bytes = hex::decode(s).map_err(|e| anyhow!("invalid hex: {}", e))?;
    let mut out = [0u8; ID_LEN];
    out.copy_from_slice(&bytes);
    Ok(out)
}

/// DR distribution scope for a project.
///
/// `MembersOnly` keeps the bare repo on member envoys only — the
/// project is invisible to other puddle members.
///
/// `SwarmWide` distributes the bare repo to every swarmgit-authorized
/// envoy in the swarm as a backup. Non-member receivers hold the bytes;
/// the helper daemon refuses non-root reads on those envoys (root
/// retains direct filesystem access by design — swarmgit is for
/// *availability*, not bytes-on-disk confidentiality; lockbox owns
/// confidentiality).
#[derive(Debug, Clone, Copy, PartialEq, Eq, Serialize, Deserialize)]
#[serde(rename_all = "kebab-case")]
pub enum DrScope {
    /// Replica only on envoys whose puddle is in `members:`.
    MembersOnly,
    /// Replica on every swarmgit-authorized envoy for DR purposes.
    SwarmWide,
}

impl DrScope {
    /// Display-friendly string used in CLI output and the trait
    /// payload. Matches the kebab-case JSON form.
    pub fn as_str(&self) -> &'static str {
        match self {
            DrScope::MembersOnly => "members-only",
            DrScope::SwarmWide => "swarm-wide",
        }
    }
}

impl std::str::FromStr for DrScope {
    type Err = anyhow::Error;
    fn from_str(s: &str) -> Result<Self> {
        match s {
            "members-only" => Ok(DrScope::MembersOnly),
            "swarm-wide" => Ok(DrScope::SwarmWide),
            // Retired in 0.41.32: `dr_fanout: fleet-wide` → `dr_scope: swarm-wide`.
            "fleet-wide" => bail!(
                "dr_scope value 'fleet-wide' was renamed to 'swarm-wide' (and the field \
                 'dr_fanout' to 'dr_scope', the flag '--fanout' to '--scope') in 0.41.32"
            ),
            other => bail!(
                "unknown dr_scope '{}': expected 'members-only' or 'swarm-wide'",
                other
            ),
        }
    }
}

/// Local view of a project's identity and membership. Lives at
/// `~/.vigo-swarmgit/<project_id_hex>/project.json`. The founder
/// writes this at `project init`; the joiner writes it at
/// `project join`. In step 4 gossip-driven membership updates refresh
/// the file in place.
///
/// `project_id` is encoded redundantly with the directory name to keep
/// the file self-describing — operators inspecting `project.json` in
/// isolation can verify it belongs where it sits.
#[derive(Debug, Clone, PartialEq, Eq, Serialize, Deserialize)]
pub struct ProjectConfig {
    /// Schema version. Bump on incompatible changes.
    #[serde(default = "default_version")]
    pub version: u32,
    /// Hex-encoded `sha256(founder_puddle_pubkey)`. Matches the parent
    /// directory name and the URL: `vigo://<project_id>/<handle>`.
    pub project_id: String,
    /// Human-readable project handle, operator-chosen at `project init`.
    /// Free-form (no uniqueness guarantee — the cryptographic identity
    /// is `project_id`); used for display and as the URL handle suffix.
    pub handle: String,
    /// Hex-encoded founder puddle pubkey. The TOFU anchor for the
    /// project: `project_id == sha256(founder_puddle_pubkey)`.
    pub founder_puddle_pubkey: String,
    /// Hex-encoded puddle pubkeys of every member puddle, including the
    /// founder. Stable order — first entry is always the founder. For
    /// single-user projects (`{my-puddle}`) the list has one entry.
    pub members: Vec<String>,
    /// Distribution scope.
    pub dr_scope: DrScope,
    /// Optional explicit cross-subnet member envoy hostnames. Receivers
    /// not reachable via the LAN multicast discovery layer can be named
    /// here — the helper daemon (step 4) pushes to them directly over
    /// mTLS on the swarm peer port (1531).
    #[serde(default, skip_serializing_if = "Vec::is_empty")]
    pub cross_subnet_members: Vec<String>,
    /// Unix seconds when this config was last updated locally. Not a
    /// causal clock — gossip in step 4 will use a richer ordering when
    /// reconciling concurrent membership edits.
    #[serde(default)]
    pub updated_at: i64,
    /// ADR-019 slice 5: unix seconds when this project was tombstoned.
    /// Set by:
    /// - `--force-orphan-projects` on `puddle rekey-pair` / `rekey`
    ///   when a founded project's delegation chain is at cap and the
    ///   operator chose to proceed with the rotation rather than
    ///   re-found beforehand.
    /// - `vigo swarm swarmgit project re-found <handle>` on the OLD
    ///   project after a fresh project_id has been minted.
    ///
    /// Tombstoned projects keep their bare repo + URL on disk (the
    /// data is still valuable as an archive), but the helper daemon
    /// refuses new admin operations and `cmd_invite` /
    /// `cmd_re_invite_all` skip them. Absent / null = active project.
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub tombstoned_at: Option<i64>,
}

fn default_version() -> u32 {
    PROJECT_STATE_VERSION
}

impl ProjectConfig {
    /// Load the per-user project config for `project_id` from the
    /// given home dir. Returns `Ok(None)` when the file does not exist
    /// (this user has not joined the project) or when its schema
    /// version differs from [`PROJECT_STATE_VERSION`]. Returns `Err`
    /// only on unexpected I/O — corruption or version mismatch resolves
    /// to `None` so a stale file can't block the rest of the CLI.
    pub fn load(home: &Path, project_id: &[u8; ID_LEN]) -> Result<Option<Self>> {
        let path = project_config_path(home, project_id);
        let bytes = match fs::read(&path) {
            Ok(b) => b,
            Err(e) if e.kind() == std::io::ErrorKind::NotFound => return Ok(None),
            Err(e) => return Err(e).with_context(|| format!("read {}", path.display())),
        };
        let cfg: ProjectConfig = match serde_json::from_slice(&bytes) {
            Ok(c) => c,
            Err(_) => return Ok(None),
        };
        if cfg.version != PROJECT_STATE_VERSION {
            return Ok(None);
        }
        // The file's project_id field must match the directory name —
        // catches a misplaced or hand-edited file before its contents
        // can mislead the caller.
        let expected = hex_id(project_id);
        if cfg.project_id != expected {
            bail!(
                "project.json at {} has project_id {:.12}… but directory says {:.12}…",
                path.display(),
                cfg.project_id,
                expected
            );
        }
        Ok(Some(cfg))
    }

    /// Persist the config under the per-user project dir via
    /// tmpfile + rename. Mode `0600`, dot-dir created `0700` if absent.
    pub fn save(&self, home: &Path) -> Result<()> {
        let project_id = parse_hex_id(&self.project_id)
            .context("project_id field is not valid hex")?;
        let dir = user_project_dir(home, &project_id);
        ensure_user_dir(home, &dir)?;
        let final_path = project_config_path(home, &project_id);
        let tmp_path = dir.join("project.json.tmp");

        let json = serde_json::to_vec_pretty(self)
            .context("encode project config")?;
        write_user_file(&tmp_path, &json, 0o600)
            .with_context(|| format!("write tmp {}", tmp_path.display()))?;
        fs::rename(&tmp_path, &final_path)
            .with_context(|| format!("rename to {}", final_path.display()))?;
        Ok(())
    }
}

/// Operator's authorization chain for a project. Persisted at
/// `~/.vigo-swarmgit/<project_id_hex>/tokens.json`. The leaf
/// (`chain[chain.len()-1]`) names the local user's puddle pubkey as
/// subject; ancestors walk back to the founder's bootstrap token. The
/// helper daemon (step 4) loads + verifies this chain before
/// authorizing a push.
///
/// Verification logic lives in `super::tokens::verify_chain`; this
/// struct is a thin persistence envelope.
#[derive(Debug, Clone, PartialEq, Eq, Serialize, Deserialize)]
pub struct TokenChain {
    /// Schema version.
    #[serde(default = "default_version")]
    pub version: u32,
    /// Tokens in chain order: root (founder bootstrap) first, leaf
    /// (this user's puddle) last. Empty in transient pre-init states is
    /// rejected by `save()`.
    pub chain: Vec<Token>,
}

impl TokenChain {
    /// Load the user's token chain for `project_id`. Returns `Ok(None)`
    /// on missing file, version mismatch, or unparseable JSON — same
    /// graceful-degradation rule as [`ProjectConfig::load`].
    pub fn load(home: &Path, project_id: &[u8; ID_LEN]) -> Result<Option<Self>> {
        let path = token_chain_path(home, project_id);
        let bytes = match fs::read(&path) {
            Ok(b) => b,
            Err(e) if e.kind() == std::io::ErrorKind::NotFound => return Ok(None),
            Err(e) => return Err(e).with_context(|| format!("read {}", path.display())),
        };
        let chain: TokenChain = match serde_json::from_slice(&bytes) {
            Ok(c) => c,
            Err(_) => return Ok(None),
        };
        if chain.version != PROJECT_STATE_VERSION {
            return Ok(None);
        }
        Ok(Some(chain))
    }

    /// Persist the chain via tmpfile + rename. Mode `0600`. Refuses
    /// to write an empty chain — that would corrupt the helper
    /// daemon's "no auth" check (which expects "missing file" to mean
    /// "user has not joined").
    pub fn save(&self, home: &Path, project_id: &[u8; ID_LEN]) -> Result<()> {
        if self.chain.is_empty() {
            bail!("refusing to save an empty token chain");
        }
        let dir = user_project_dir(home, project_id);
        ensure_user_dir(home, &dir)?;
        let final_path = token_chain_path(home, project_id);
        let tmp_path = dir.join("tokens.json.tmp");

        let json = serde_json::to_vec_pretty(self)
            .context("encode token chain")?;
        write_user_file(&tmp_path, &json, 0o600)
            .with_context(|| format!("write tmp {}", tmp_path.display()))?;
        fs::rename(&tmp_path, &final_path)
            .with_context(|| format!("rename to {}", final_path.display()))?;
        Ok(())
    }
}

/// Ordered append-only list of founder-rekey delegations for a
/// project (ADR-019). Persisted at
/// `~/.vigo-swarmgit/<project_id_hex>/delegations.json`.
///
/// Empty (or missing file) is the steady-state for projects that
/// have not yet seen a founder rekey — `verify_chain` walks zero
/// delegations and the bootstrap token chains directly. A non-empty
/// list bridges the founder's genesis pubkey (anchored in
/// `project_id`) through one or more rekey hops to the
/// currently-active founder pubkey.
///
/// Each entry is signed by the previous link's `new_founder_pubkey`
/// (or, for the first entry, by the genesis pubkey baked into
/// `project_id`). `verify_chain` enforces this linkage and the
/// [`DELEGATION_MAX_LEN`] hard cap.
#[derive(Debug, Clone, PartialEq, Eq, Serialize, Deserialize, Default)]
pub struct DelegationChain {
    /// Schema version.
    #[serde(default = "default_version")]
    pub version: u32,
    /// Delegations in chain order: first entry signed by the genesis
    /// founder pubkey, last entry's `new_founder_pubkey` is the
    /// currently-active founder signing identity.
    #[serde(default)]
    pub delegations: Vec<DelegationEnvelope>,
}

impl DelegationChain {
    /// Load the user's delegation chain for `project_id`. Returns
    /// `Ok(None)` on missing file, version mismatch, or unparseable
    /// JSON — same graceful-degradation rule as [`ProjectConfig::load`]
    /// and [`TokenChain::load`]. Missing file is the steady state for
    /// any project whose founder hasn't yet rekeyed.
    pub fn load(home: &Path, project_id: &[u8; ID_LEN]) -> Result<Option<Self>> {
        let path = delegations_path(home, project_id);
        let bytes = match fs::read(&path) {
            Ok(b) => b,
            Err(e) if e.kind() == std::io::ErrorKind::NotFound => return Ok(None),
            Err(e) => return Err(e).with_context(|| format!("read {}", path.display())),
        };
        let chain: DelegationChain = match serde_json::from_slice(&bytes) {
            Ok(c) => c,
            Err(_) => return Ok(None),
        };
        if chain.version != PROJECT_STATE_VERSION {
            return Ok(None);
        }
        Ok(Some(chain))
    }

    /// Persist the chain via tmpfile + rename. Mode `0600`. Refuses
    /// to write a chain whose length exceeds [`DELEGATION_MAX_LEN`]
    /// — past the cap the operator must `swarmgit project re-found`
    /// the project (which mints a fresh project_id) rather than
    /// extend the chain further.
    pub fn save(&self, home: &Path, project_id: &[u8; ID_LEN]) -> Result<()> {
        if self.delegations.len() > DELEGATION_MAX_LEN {
            bail!(
                "refusing to save delegation chain of length {} > cap {}; \
                 run `swarmgit project re-found <handle>` to mint a fresh \
                 project_id, or pass --force-orphan-projects on `rekey-pair`",
                self.delegations.len(),
                DELEGATION_MAX_LEN
            );
        }
        let dir = user_project_dir(home, project_id);
        ensure_user_dir(home, &dir)?;
        let final_path = delegations_path(home, project_id);
        let tmp_path = dir.join("delegations.json.tmp");

        let json = serde_json::to_vec_pretty(self)
            .context("encode delegation chain")?;
        write_user_file(&tmp_path, &json, 0o600)
            .with_context(|| format!("write tmp {}", tmp_path.display()))?;
        fs::rename(&tmp_path, &final_path)
            .with_context(|| format!("rename to {}", final_path.display()))?;
        Ok(())
    }

    /// Append a new delegation to the chain and persist. Refuses if
    /// the post-append length would exceed [`DELEGATION_MAX_LEN`] —
    /// the pre-rotation hook in `cmd_rekey_pair` should check
    /// [`Self::is_at_cap`] before signing so the failure surfaces
    /// before the rekey commits, not at save-time after the
    /// signature has been generated.
    pub fn append(
        &mut self,
        home: &Path,
        project_id: &[u8; ID_LEN],
        delegation: DelegationEnvelope,
    ) -> Result<()> {
        if self.delegations.len() >= DELEGATION_MAX_LEN {
            bail!(
                "delegation chain at cap ({}/{}); cannot append further",
                self.delegations.len(),
                DELEGATION_MAX_LEN
            );
        }
        self.delegations.push(delegation);
        self.save(home, project_id)
    }

    /// Length of the chain. `0` for projects that haven't yet seen a
    /// founder rekey. Equal to [`DELEGATION_MAX_LEN`] means one
    /// further rekey will be rejected.
    pub fn len(&self) -> usize {
        self.delegations.len()
    }

    /// True iff a further rekey would push the chain past
    /// [`DELEGATION_MAX_LEN`]. Pre-rotation hook checks this before
    /// signing.
    pub fn is_at_cap(&self) -> bool {
        self.delegations.len() >= DELEGATION_MAX_LEN
    }

    /// True iff the chain is in the warning band ([`DELEGATION_WARN_LEN`]
    /// and above, but still below [`DELEGATION_MAX_LEN`]). Status
    /// surface flags affected projects. See
    /// [`super::envelopes::DELEGATION_WARN_LEN`].
    pub fn is_in_warn_band(&self) -> bool {
        self.delegations.len() >= super::envelopes::DELEGATION_WARN_LEN
            && self.delegations.len() <= DELEGATION_MAX_LEN
    }
}

/// List every project this user has joined locally — one entry per
/// directory found under `~/.vigo-swarmgit/`. Entries with a missing
/// or unparseable `project.json` are skipped silently (they may be
/// transiently mid-init or left over from a prior schema). Sorted by
/// handle for stable CLI output.
pub fn list_projects(home: &Path) -> Result<Vec<ProjectConfig>> {
    let dot = user_dot_dir(home);
    let entries = match fs::read_dir(&dot) {
        Ok(e) => e,
        Err(e) if e.kind() == std::io::ErrorKind::NotFound => return Ok(Vec::new()),
        Err(e) => return Err(e).with_context(|| format!("read {}", dot.display())),
    };
    let mut out = Vec::new();
    for entry in entries.flatten() {
        let name = entry.file_name();
        let name_str = match name.to_str() {
            Some(s) => s,
            None => continue,
        };
        let project_id = match parse_hex_id(name_str) {
            Ok(id) => id,
            Err(_) => continue,
        };
        match ProjectConfig::load(home, &project_id) {
            Ok(Some(cfg)) => out.push(cfg),
            _ => continue,
        }
    }
    out.sort_by(|a, b| a.handle.cmp(&b.handle).then(a.project_id.cmp(&b.project_id)));
    Ok(out)
}

/// Drop all per-user state for `project_id` — removes the dot-dir
/// recursively. Used by `project leave`. Idempotent.
pub fn purge_user_state(home: &Path, project_id: &[u8; ID_LEN]) -> Result<()> {
    let dir = user_project_dir(home, project_id);
    if !dir.exists() {
        return Ok(());
    }
    fs::remove_dir_all(&dir)
        .with_context(|| format!("remove {}", dir.display()))
}

/// Ensure `~/.vigo-swarmgit/` and the per-project subdir both exist
/// with mode `0700`. Caller is the user (CLI runs as the calling user
/// for step 3 verbs).
fn ensure_user_dir(home: &Path, project_dir: &Path) -> Result<()> {
    let dot = user_dot_dir(home);
    fs::create_dir_all(&dot)
        .with_context(|| format!("create {}", dot.display()))?;
    set_dir_mode(&dot, 0o700)?;
    fs::create_dir_all(project_dir)
        .with_context(|| format!("create {}", project_dir.display()))?;
    set_dir_mode(project_dir, 0o700)?;
    Ok(())
}

#[cfg(unix)]
fn set_dir_mode(path: &Path, mode: u32) -> Result<()> {
    use std::os::unix::fs::PermissionsExt;
    fs::set_permissions(path, fs::Permissions::from_mode(mode))
        .with_context(|| format!("chmod {} to {:o}", path.display(), mode))
}

#[cfg(not(unix))]
fn set_dir_mode(_path: &Path, _mode: u32) -> Result<()> {
    Ok(())
}

#[cfg(unix)]
fn write_user_file(path: &Path, bytes: &[u8], mode: u32) -> std::io::Result<()> {
    use std::os::unix::fs::PermissionsExt;
    let mut f = fs::OpenOptions::new()
        .create(true)
        .write(true)
        .truncate(true)
        .open(path)?;
    f.set_permissions(fs::Permissions::from_mode(mode))?;
    f.write_all(bytes)?;
    f.sync_all()?;
    Ok(())
}

#[cfg(not(unix))]
fn write_user_file(path: &Path, bytes: &[u8], _mode: u32) -> std::io::Result<()> {
    let mut f = fs::OpenOptions::new()
        .create(true)
        .write(true)
        .truncate(true)
        .open(path)?;
    f.write_all(bytes)?;
    f.sync_all()?;
    Ok(())
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::swarm::swarmgit::tokens::{issue_bootstrap, issue_child, project_id_for, Role};
    use ed25519_dalek::SigningKey;
    use rand::rngs::OsRng;
    use tempfile::tempdir;

    fn fresh_key() -> SigningKey {
        SigningKey::generate(&mut OsRng)
    }

    fn pub_bytes(k: &SigningKey) -> [u8; ID_LEN] {
        k.verifying_key().to_bytes()
    }

    fn sample_config() -> ProjectConfig {
        let founder = fresh_key();
        let pid = project_id_for(&pub_bytes(&founder));
        ProjectConfig {
            version: PROJECT_STATE_VERSION,
            project_id: hex_id(&pid),
            handle: "vigo-web".to_string(),
            founder_puddle_pubkey: hex_id(&pub_bytes(&founder)),
            members: vec![hex_id(&pub_bytes(&founder))],
            dr_scope: DrScope::SwarmWide,
            cross_subnet_members: vec![],
            updated_at: 1_744_560_000,
            tombstoned_at: None,
        }
    }

    #[test]
    fn hex_id_roundtrip() {
        let id = [0xABu8; ID_LEN];
        let s = hex_id(&id);
        assert_eq!(s.len(), 64);
        assert_eq!(s, "ab".repeat(32));
        let back = parse_hex_id(&s).unwrap();
        assert_eq!(back, id);
    }

    #[test]
    fn parse_hex_id_rejects_wrong_length() {
        assert!(parse_hex_id("deadbeef").is_err());
        assert!(parse_hex_id(&"a".repeat(63)).is_err());
        assert!(parse_hex_id(&"a".repeat(65)).is_err());
    }

    #[test]
    fn parse_hex_id_rejects_uppercase() {
        let upper = "AB".repeat(32);
        let err = parse_hex_id(&upper).unwrap_err();
        assert!(err.to_string().contains("lowercase"));
    }

    #[test]
    fn parse_hex_id_rejects_non_hex() {
        assert!(parse_hex_id(&"z".repeat(64)).is_err());
    }

    #[test]
    fn dr_scope_str_roundtrip() {
        for f in [DrScope::SwarmWide, DrScope::MembersOnly] {
            let s = f.as_str();
            let back: DrScope = s.parse().unwrap();
            assert_eq!(back, f);
        }
    }

    #[test]
    fn dr_scope_from_str_rejects_garbage() {
        assert!("everyone".parse::<DrScope>().is_err());
        assert!("".parse::<DrScope>().is_err());
    }

    #[test]
    fn dr_scope_from_str_rejects_retired_fleet_wide() {
        let err = "fleet-wide".parse::<DrScope>().unwrap_err().to_string();
        assert!(err.contains("swarm-wide"), "retired-value error should name the new value: {err}");
    }

    #[test]
    fn dr_scope_serializes_kebab_case() {
        let cfg = sample_config();
        let s = serde_json::to_string(&cfg).unwrap();
        assert!(s.contains("\"swarm-wide\""), "expected kebab-case in {}", s);
        assert!(s.contains("\"dr_scope\""), "expected dr_scope field key in {}", s);
    }

    #[test]
    fn project_config_save_then_load_roundtrip() {
        let home = tempdir().unwrap();
        let cfg = sample_config();
        cfg.save(home.path()).unwrap();

        let pid = parse_hex_id(&cfg.project_id).unwrap();
        let loaded = ProjectConfig::load(home.path(), &pid).unwrap().unwrap();
        assert_eq!(loaded, cfg);
    }

    #[test]
    fn project_config_load_missing_returns_none() {
        let home = tempdir().unwrap();
        let pid = [0u8; ID_LEN];
        let got = ProjectConfig::load(home.path(), &pid).unwrap();
        assert!(got.is_none());
    }

    #[test]
    fn project_config_load_corrupt_returns_none() {
        let home = tempdir().unwrap();
        let cfg = sample_config();
        let pid = parse_hex_id(&cfg.project_id).unwrap();
        let dir = user_project_dir(home.path(), &pid);
        fs::create_dir_all(&dir).unwrap();
        fs::write(project_config_path(home.path(), &pid), b"not json").unwrap();
        let got = ProjectConfig::load(home.path(), &pid).unwrap();
        assert!(got.is_none());
    }

    #[test]
    fn project_config_load_version_mismatch_returns_none() {
        let home = tempdir().unwrap();
        let mut cfg = sample_config();
        cfg.version = 99;
        cfg.save(home.path()).unwrap();
        let pid = parse_hex_id(&cfg.project_id).unwrap();
        let got = ProjectConfig::load(home.path(), &pid).unwrap();
        assert!(got.is_none());
    }

    #[test]
    fn project_config_load_rejects_directory_id_mismatch() {
        // If a project.json's project_id field disagrees with its
        // parent directory name, load() must error out — that's a
        // misplaced or hand-edited file and we don't want to silently
        // serve its contents to the helper daemon.
        let home = tempdir().unwrap();
        let cfg = sample_config();
        cfg.save(home.path()).unwrap();
        // Now manually move the file into a directory keyed by a
        // *different* project_id and see if load(other_pid) catches it.
        let other_pid = [0xFFu8; ID_LEN];
        let other_dir = user_project_dir(home.path(), &other_pid);
        fs::create_dir_all(&other_dir).unwrap();
        let src = project_config_path(home.path(), &parse_hex_id(&cfg.project_id).unwrap());
        let dst = project_config_path(home.path(), &other_pid);
        fs::copy(&src, &dst).unwrap();
        let err = ProjectConfig::load(home.path(), &other_pid).unwrap_err();
        assert!(err.to_string().contains("project_id"));
    }

    #[test]
    #[cfg(unix)]
    fn project_config_save_writes_mode_0600() {
        use std::os::unix::fs::PermissionsExt;
        let home = tempdir().unwrap();
        let cfg = sample_config();
        cfg.save(home.path()).unwrap();
        let pid = parse_hex_id(&cfg.project_id).unwrap();
        let mode = fs::metadata(project_config_path(home.path(), &pid))
            .unwrap()
            .permissions()
            .mode();
        assert_eq!(mode & 0o777, 0o600);
    }

    #[test]
    #[cfg(unix)]
    fn user_dot_dir_created_with_mode_0700() {
        use std::os::unix::fs::PermissionsExt;
        let home = tempdir().unwrap();
        sample_config().save(home.path()).unwrap();
        let mode = fs::metadata(user_dot_dir(home.path()))
            .unwrap()
            .permissions()
            .mode();
        assert_eq!(mode & 0o777, 0o700);
    }

    #[test]
    fn project_config_save_atomic_no_tmp_left_behind() {
        let home = tempdir().unwrap();
        let cfg = sample_config();
        cfg.save(home.path()).unwrap();
        let pid = parse_hex_id(&cfg.project_id).unwrap();
        let dir = user_project_dir(home.path(), &pid);
        let tmp = dir.join("project.json.tmp");
        assert!(!tmp.exists(), "tmp file should be renamed away");
    }

    fn sample_chain() -> TokenChain {
        let founder = fresh_key();
        let team = fresh_key();
        let bootstrap = issue_bootstrap(&founder, 100, None).unwrap();
        let member = issue_child(
            &founder,
            &bootstrap,
            &pub_bytes(&team),
            Role::Member,
            150,
            None,
        )
        .unwrap();
        TokenChain {
            version: PROJECT_STATE_VERSION,
            chain: vec![bootstrap, member],
        }
    }

    #[test]
    fn token_chain_save_then_load_roundtrip() {
        let home = tempdir().unwrap();
        let chain = sample_chain();
        let pid = chain.chain[0].body.project_id;
        chain.save(home.path(), &pid).unwrap();
        let loaded = TokenChain::load(home.path(), &pid).unwrap().unwrap();
        assert_eq!(loaded, chain);
    }

    #[test]
    fn token_chain_load_missing_returns_none() {
        let home = tempdir().unwrap();
        let pid = [0u8; ID_LEN];
        let got = TokenChain::load(home.path(), &pid).unwrap();
        assert!(got.is_none());
    }

    #[test]
    fn token_chain_save_refuses_empty_chain() {
        let home = tempdir().unwrap();
        let empty = TokenChain {
            version: PROJECT_STATE_VERSION,
            chain: vec![],
        };
        let pid = [0u8; ID_LEN];
        let err = empty.save(home.path(), &pid).unwrap_err();
        assert!(err.to_string().contains("empty"));
    }

    #[test]
    #[cfg(unix)]
    fn token_chain_save_writes_mode_0600() {
        use std::os::unix::fs::PermissionsExt;
        let home = tempdir().unwrap();
        let chain = sample_chain();
        let pid = chain.chain[0].body.project_id;
        chain.save(home.path(), &pid).unwrap();
        let mode = fs::metadata(token_chain_path(home.path(), &pid))
            .unwrap()
            .permissions()
            .mode();
        assert_eq!(mode & 0o777, 0o600);
    }

    #[test]
    fn token_chain_load_corrupt_returns_none() {
        let home = tempdir().unwrap();
        let pid = [0u8; ID_LEN];
        let dir = user_project_dir(home.path(), &pid);
        fs::create_dir_all(&dir).unwrap();
        fs::write(token_chain_path(home.path(), &pid), b"garbage").unwrap();
        let got = TokenChain::load(home.path(), &pid).unwrap();
        assert!(got.is_none());
    }

    #[test]
    fn list_projects_empty_when_dot_dir_missing() {
        let home = tempdir().unwrap();
        let projects = list_projects(home.path()).unwrap();
        assert!(projects.is_empty());
    }

    #[test]
    fn list_projects_returns_saved_configs_sorted() {
        let home = tempdir().unwrap();
        let mut a = sample_config();
        a.handle = "zeta".to_string();
        a.save(home.path()).unwrap();

        let mut b = sample_config();
        // Different founder → different project_id → separate dir.
        let other_founder = fresh_key();
        let other_pid = project_id_for(&pub_bytes(&other_founder));
        b.project_id = hex_id(&other_pid);
        b.founder_puddle_pubkey = hex_id(&pub_bytes(&other_founder));
        b.handle = "alpha".to_string();
        b.members = vec![hex_id(&pub_bytes(&other_founder))];
        b.save(home.path()).unwrap();

        let projects = list_projects(home.path()).unwrap();
        assert_eq!(projects.len(), 2);
        assert_eq!(projects[0].handle, "alpha");
        assert_eq!(projects[1].handle, "zeta");
    }

    #[test]
    fn list_projects_skips_non_hex_dirs() {
        // A stray directory under ~/.vigo-swarmgit/ that isn't a hex
        // project ID (e.g. a user-created scratch folder) must not
        // crash list_projects — it's silently skipped.
        let home = tempdir().unwrap();
        sample_config().save(home.path()).unwrap();
        fs::create_dir_all(user_dot_dir(home.path()).join("not-a-project")).unwrap();
        let projects = list_projects(home.path()).unwrap();
        assert_eq!(projects.len(), 1);
    }

    #[test]
    fn list_projects_skips_missing_project_json() {
        // A hex-named directory with no project.json (mid-init crash)
        // must not appear in the list.
        let home = tempdir().unwrap();
        let pid = [0xCDu8; ID_LEN];
        fs::create_dir_all(user_project_dir(home.path(), &pid)).unwrap();
        let projects = list_projects(home.path()).unwrap();
        assert!(projects.is_empty());
    }

    #[test]
    fn purge_user_state_removes_dir() {
        let home = tempdir().unwrap();
        let cfg = sample_config();
        cfg.save(home.path()).unwrap();
        let pid = parse_hex_id(&cfg.project_id).unwrap();
        assert!(user_project_dir(home.path(), &pid).exists());
        purge_user_state(home.path(), &pid).unwrap();
        assert!(!user_project_dir(home.path(), &pid).exists());
    }

    #[test]
    fn purge_user_state_idempotent_on_missing_dir() {
        let home = tempdir().unwrap();
        let pid = [0u8; ID_LEN];
        purge_user_state(home.path(), &pid).unwrap();
        // Second call also fine.
        purge_user_state(home.path(), &pid).unwrap();
    }

    #[test]
    fn project_repo_path_uses_hex_id() {
        let id = [0xABu8; ID_LEN];
        let base = Path::new("/var/lib/vigo/swarm/swarmgit");
        let p = project_repo_path(base, &id);
        assert!(p.to_string_lossy().ends_with(".git"));
        assert!(p.to_string_lossy().contains(&hex_id(&id)));
        assert!(p.starts_with("/var/lib/vigo/swarm/swarmgit/projects"));
    }

    #[test]
    fn projects_dir_under_swarmgit_base() {
        let base = Path::new("/srv/test");
        let p = projects_dir(base);
        assert_eq!(p, PathBuf::from("/srv/test/projects"));
    }

    #[test]
    fn user_project_dir_under_dot_dir() {
        let home = Path::new("/home/dan");
        let id = [0u8; ID_LEN];
        let p = user_project_dir(home, &id);
        assert!(p.starts_with("/home/dan/.vigo-swarmgit"));
        assert!(p.to_string_lossy().ends_with(&hex_id(&id)));
    }

    // ---- delegation chain (ADR-019 slice 1) ----

    fn sample_delegation(
        old_key: &SigningKey,
        new_pubkey: &[u8; ID_LEN],
        project_id: &[u8; ID_LEN],
        delegated_at: i64,
    ) -> DelegationEnvelope {
        DelegationEnvelope::build(old_key, project_id, new_pubkey, delegated_at)
    }

    #[test]
    fn delegation_chain_load_missing_returns_none() {
        let home = tempdir().unwrap();
        let pid = [0u8; ID_LEN];
        // No founder rekey has happened — file simply doesn't exist;
        // verify_chain (slice 2) treats `None` as "zero delegations,
        // bootstrap chains directly to founder."
        let got = DelegationChain::load(home.path(), &pid).unwrap();
        assert!(got.is_none());
    }

    #[test]
    fn delegation_chain_save_then_load_roundtrip() {
        let home = tempdir().unwrap();
        let founder = fresh_key();
        let rekey1 = fresh_key();
        let pid = project_id_for(&pub_bytes(&founder));
        let chain = DelegationChain {
            version: PROJECT_STATE_VERSION,
            delegations: vec![sample_delegation(
                &founder,
                &pub_bytes(&rekey1),
                &pid,
                1_700_000_000,
            )],
        };
        chain.save(home.path(), &pid).unwrap();
        let loaded = DelegationChain::load(home.path(), &pid).unwrap().unwrap();
        assert_eq!(loaded, chain);
    }

    #[test]
    fn delegation_chain_append_grows_in_order() {
        let home = tempdir().unwrap();
        let founder = fresh_key();
        let rekey1 = fresh_key();
        let rekey2 = fresh_key();
        let pid = project_id_for(&pub_bytes(&founder));

        let mut chain = DelegationChain::default();
        chain.version = PROJECT_STATE_VERSION;

        chain
            .append(
                home.path(),
                &pid,
                sample_delegation(&founder, &pub_bytes(&rekey1), &pid, 1_700_000_000),
            )
            .unwrap();
        chain
            .append(
                home.path(),
                &pid,
                sample_delegation(&rekey1, &pub_bytes(&rekey2), &pid, 1_700_000_001),
            )
            .unwrap();

        let loaded = DelegationChain::load(home.path(), &pid).unwrap().unwrap();
        assert_eq!(loaded.delegations.len(), 2);
        assert_eq!(loaded.delegations[0].new_founder_pubkey, hex_id(&pub_bytes(&rekey1)));
        assert_eq!(loaded.delegations[1].new_founder_pubkey, hex_id(&pub_bytes(&rekey2)));
    }

    #[test]
    fn delegation_chain_append_refuses_at_cap() {
        // Build a chain at exactly DELEGATION_MAX_LEN, attempt one more
        // append — pre-rotation hook in slice 3 surfaces this as
        // "project at delegation cap; run re-found." Bypass the in-memory
        // cap check would just mean save() refuses on disk.
        let home = tempdir().unwrap();
        let founder = fresh_key();
        let pid = project_id_for(&pub_bytes(&founder));
        let mut chain = DelegationChain::default();
        chain.version = PROJECT_STATE_VERSION;

        let mut prev_signer = founder.clone();
        for i in 0..DELEGATION_MAX_LEN {
            let next = fresh_key();
            chain
                .append(
                    home.path(),
                    &pid,
                    sample_delegation(
                        &prev_signer,
                        &pub_bytes(&next),
                        &pid,
                        1_700_000_000 + i as i64,
                    ),
                )
                .unwrap();
            prev_signer = next;
        }
        assert!(chain.is_at_cap());
        let next = fresh_key();
        let err = chain
            .append(
                home.path(),
                &pid,
                sample_delegation(
                    &prev_signer,
                    &pub_bytes(&next),
                    &pid,
                    1_700_000_999,
                ),
            )
            .unwrap_err();
        assert!(
            err.to_string().contains("at cap"),
            "unexpected error message: {}",
            err
        );
        // On-disk state must still reflect the pre-attempted-append
        // chain — failed append must not corrupt persisted state.
        let loaded = DelegationChain::load(home.path(), &pid).unwrap().unwrap();
        assert_eq!(loaded.delegations.len(), DELEGATION_MAX_LEN);
    }

    #[test]
    fn delegation_chain_save_refuses_oversized_chain() {
        // Direct save() of an over-cap chain (e.g. constructed in
        // memory without going through append) must still refuse.
        let home = tempdir().unwrap();
        let founder = fresh_key();
        let pid = project_id_for(&pub_bytes(&founder));
        let mut delegations = Vec::new();
        let mut prev_signer = founder.clone();
        for i in 0..(DELEGATION_MAX_LEN + 1) {
            let next = fresh_key();
            delegations.push(sample_delegation(
                &prev_signer,
                &pub_bytes(&next),
                &pid,
                1_700_000_000 + i as i64,
            ));
            prev_signer = next;
        }
        let chain = DelegationChain {
            version: PROJECT_STATE_VERSION,
            delegations,
        };
        let err = chain.save(home.path(), &pid).unwrap_err();
        assert!(err.to_string().contains("cap"));
    }

    #[test]
    fn delegation_chain_warn_band_predicates() {
        let mut chain = DelegationChain::default();
        chain.version = PROJECT_STATE_VERSION;
        // Empty chain → not in warn band.
        assert!(!chain.is_in_warn_band());
        // Synthesize entries by pushing zero-data delegations — we're
        // exercising length predicates only, not signatures.
        let founder = fresh_key();
        let pid = project_id_for(&pub_bytes(&founder));
        for _ in 0..2 {
            chain.delegations.push(sample_delegation(
                &founder,
                &pub_bytes(&fresh_key()),
                &pid,
                0,
            ));
        }
        // Length 2 < DELEGATION_WARN_LEN (3) → not yet warning.
        assert!(!chain.is_in_warn_band());
        chain.delegations.push(sample_delegation(
            &founder,
            &pub_bytes(&fresh_key()),
            &pid,
            0,
        ));
        // Length 3 == DELEGATION_WARN_LEN → in warn band.
        assert!(chain.is_in_warn_band());
        assert!(!chain.is_at_cap());
    }

    #[test]
    fn delegation_chain_load_corrupt_returns_none() {
        let home = tempdir().unwrap();
        let pid = [0u8; ID_LEN];
        let dir = user_project_dir(home.path(), &pid);
        fs::create_dir_all(&dir).unwrap();
        fs::write(delegations_path(home.path(), &pid), b"not json").unwrap();
        let got = DelegationChain::load(home.path(), &pid).unwrap();
        assert!(got.is_none());
    }

    #[test]
    fn delegation_chain_load_version_mismatch_returns_none() {
        let home = tempdir().unwrap();
        let founder = fresh_key();
        let pid = project_id_for(&pub_bytes(&founder));
        let chain = DelegationChain {
            version: 99,
            delegations: vec![sample_delegation(&founder, &pub_bytes(&fresh_key()), &pid, 0)],
        };
        let dir = user_project_dir(home.path(), &pid);
        fs::create_dir_all(&dir).unwrap();
        fs::write(
            delegations_path(home.path(), &pid),
            serde_json::to_vec(&chain).unwrap(),
        )
        .unwrap();
        let got = DelegationChain::load(home.path(), &pid).unwrap();
        assert!(got.is_none());
    }

    #[test]
    #[cfg(unix)]
    fn delegation_chain_save_writes_mode_0600() {
        use std::os::unix::fs::PermissionsExt;
        let home = tempdir().unwrap();
        let founder = fresh_key();
        let pid = project_id_for(&pub_bytes(&founder));
        let chain = DelegationChain {
            version: PROJECT_STATE_VERSION,
            delegations: vec![sample_delegation(
                &founder,
                &pub_bytes(&fresh_key()),
                &pid,
                1_700_000_000,
            )],
        };
        chain.save(home.path(), &pid).unwrap();
        let mode = fs::metadata(delegations_path(home.path(), &pid))
            .unwrap()
            .permissions()
            .mode();
        assert_eq!(mode & 0o777, 0o600);
    }

    #[test]
    fn delegations_path_is_under_user_project_dir() {
        let home = Path::new("/home/dan");
        let pid = [0xAAu8; ID_LEN];
        let p = delegations_path(home, &pid);
        assert!(p.starts_with("/home/dan/.vigo-swarmgit"));
        assert!(p.to_string_lossy().ends_with("/delegations.json"));
    }
}
