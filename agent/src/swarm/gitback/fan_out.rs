// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

//! Push-driven fan-out + receive-side bundle apply for ADR-015 step 4.
//!
//! After a successful local push (the helper daemon's `git-receive-pack`
//! exits zero with refs advanced), the originating envoy builds a
//! `git bundle` of the current repo state and POSTs it to every peer
//! listed in `project.json::cross_subnet_members`. Receivers ingest
//! the bundle via `POST /swarm/gitback/push/<project_id_hex>` on
//! the swarm peer port (1531, mTLS), apply it to their local bare
//! repo, and report whether refs changed.
//!
//! Cycle prevention is the **delta check** on receive: an idempotent
//! ingest (no ref change) returns `refs_changed: false`. Auto-fan-out
//! from receive-side back out to peers is **not implemented in this
//! slice** — pushes propagate one hop, originating-envoy → cross-subnet
//! members. Multi-hop topologies need either auto LAN-discovery via the
//! announce extension (separate slice) or pairwise cross_subnet_members
//! configuration.
//!
//! ### Bundle build / apply
//!
//! - **Build:** `git -C <repo> bundle create - --all` streamed to a
//!   temp file (avoids holding a multi-GiB bundle entirely in RAM
//!   during a local push of a large repo).
//! - **Apply:** `git -C <repo> fetch <bundle_path> +refs/*:refs/*`.
//!   Fast-forward by default — git's own ref-update logic enforces
//!   the FF-only conflict policy from ADR-015 (no extra hooks
//!   needed for v1).
//!
//! ### Wire shape
//!
//! ```text
//! POST /swarm/gitback/push/<project_id_hex>
//! X-Vigo-Chain: base64(JSON([Token, Token, ...]))
//! Content-Type: application/x-git-bundle
//! Body: <bundle bytes, streamed; ≤ swarm.gitback.max_bundle_bytes (default 2 GiB)>
//!
//! → 200 OK
//!   Content-Type: application/json
//!   Body: {"accepted": true, "refs_changed": <bool>}
//! ```
//!
//! Receiver verifies the chain against the URL's `project_id`
//! (re-deriving the founder pubkey from `chain[0].signer_puddle_pubkey`
//! and confirming `sha256(founder) == project_id`). Revocation lookup
//! via [`super::envelopes::is_token_revoked`] rejects any chain whose
//! tokens have been revoked.

use std::collections::BTreeMap;
use std::fs::File;
use std::io::{self, Read};
use std::path::{Path, PathBuf};
use std::process::Stdio;
use std::time::Duration;

use anyhow::{anyhow, bail, Context, Result};
use base64::engine::general_purpose::STANDARD as B64;
use base64::Engine;
use serde::{Deserialize, Serialize};

use super::envelopes::is_token_revoked;
use super::git_ops::{build_command, RunAs};
use super::project_state::{hex_id, project_repo_path, ProjectConfig, TokenChain};
use crate::swarm::anchor::tokens::{verify_chain, ID_LEN};

/// Per-peer push timeout. Generous for a multi-GiB bundle on a slow
/// link; bounded so a stuck connection can't pin a tokio worker.
const PEER_PUSH_TIMEOUT: Duration = Duration::from_secs(300);

/// HTTP header used to carry the token chain on a fan-out push.
pub const CHAIN_HEADER: &str = "X-Vigo-Chain";

/// URL path prefix for the receiver-initiated DR catch-up GET — a
/// member that holds local project state but no bare repo pulls a
/// full `git bundle --all` snapshot of the project from any peer that
/// announces holding it. The `<project_id_hex>` follows.
pub const BUNDLE_GET_PREFIX: &str = "/swarm/gitback/bundle/";

/// Path under `<gitback_base>/incoming/` where inbound bundles are
/// staged before `git fetch`. Cleaned up on success or failure.
fn incoming_dir(gitback_base: &Path) -> PathBuf {
    gitback_base.join("incoming")
}

/// Snapshot every ref in a bare repo as `(refname, sha)` pairs. Used
/// pre/post-push to detect ref delta. Sorted for stable comparison.
pub fn snapshot_refs(repo: &Path) -> Result<BTreeMap<String, String>> {
    let mut cmd = build_command(RunAs::Root);
    cmd.arg("-C")
        .arg(repo)
        .arg("for-each-ref")
        .arg("--format=%(refname) %(objectname)")
        .stdin(Stdio::null())
        .stdout(Stdio::piped())
        .stderr(Stdio::piped());
    let out = cmd.output().context("git for-each-ref")?;
    if !out.status.success() {
        let stderr = String::from_utf8_lossy(&out.stderr);
        bail!("git for-each-ref failed: {}", stderr.trim());
    }
    let stdout = String::from_utf8(out.stdout).context("for-each-ref output not UTF-8")?;
    let mut map = BTreeMap::new();
    for line in stdout.lines() {
        if line.is_empty() {
            continue;
        }
        let mut parts = line.splitn(2, ' ');
        let refname = parts.next().ok_or_else(|| anyhow!("malformed ref line"))?;
        let sha = parts.next().ok_or_else(|| anyhow!("malformed ref line"))?;
        map.insert(refname.to_string(), sha.to_string());
    }
    Ok(map)
}

/// Build a git bundle of every ref in `repo` into `out_path`. Uses
/// `git bundle create <path> --all`. Caller is responsible for
/// unlinking `out_path` when done.
pub fn build_bundle(repo: &Path, out_path: &Path) -> Result<u64> {
    if let Some(parent) = out_path.parent() {
        std::fs::create_dir_all(parent)
            .with_context(|| format!("create {}", parent.display()))?;
    }
    let mut cmd = build_command(RunAs::Root);
    cmd.arg("-C")
        .arg(repo)
        .arg("bundle")
        .arg("create")
        .arg(out_path)
        .arg("--all")
        .stdin(Stdio::null())
        .stdout(Stdio::null())
        .stderr(Stdio::piped());
    let out = cmd.output().context("git bundle create")?;
    if !out.status.success() {
        let stderr = String::from_utf8_lossy(&out.stderr);
        bail!("git bundle create failed: {}", stderr.trim());
    }
    let size = std::fs::metadata(out_path)
        .with_context(|| format!("stat {}", out_path.display()))?
        .len();
    Ok(size)
}

/// Apply a bundle to a bare repo at `repo`. Uses `git fetch <bundle>
/// +refs/*:refs/*`. Returns Ok regardless of whether refs advanced;
/// caller compares pre/post snapshots to detect ref delta.
pub fn apply_bundle(repo: &Path, bundle_path: &Path) -> Result<()> {
    // First sanity-check the bundle so a malformed body doesn't
    // pollute the bare repo's object store.
    let mut verify = build_command(RunAs::Root);
    verify
        .arg("-C")
        .arg(repo)
        .arg("bundle")
        .arg("verify")
        .arg(bundle_path)
        .stdin(Stdio::null())
        .stdout(Stdio::null())
        .stderr(Stdio::piped());
    let v = verify.output().context("git bundle verify")?;
    if !v.status.success() {
        let stderr = String::from_utf8_lossy(&v.stderr);
        bail!("bundle failed verification: {}", stderr.trim());
    }

    let mut fetch = build_command(RunAs::Root);
    fetch
        .arg("-C")
        .arg(repo)
        .arg("fetch")
        .arg(bundle_path)
        .arg("+refs/*:refs/*")
        .stdin(Stdio::null())
        .stdout(Stdio::null())
        .stderr(Stdio::piped());
    let f = fetch.output().context("git fetch <bundle>")?;
    if !f.status.success() {
        let stderr = String::from_utf8_lossy(&f.stderr);
        bail!("git fetch <bundle> failed: {}", stderr.trim());
    }

    // The bundle's refs are in. A first ingest into a freshly
    // `git init --bare`d repo leaves `HEAD` at the system default
    // (`master`) even when the only branch pushed is `main`, so a
    // clone served from this envoy would warn "remote HEAD refers to
    // nonexistent ref" and skip the checkout. Repoint `HEAD` at a live
    // branch. Non-fatal — the refs are already updated; a still-stale
    // `HEAD` only costs the clone that one warning.
    if let Err(e) = set_head_to_default_branch(repo) {
        tracing::warn!(
            repo = %repo.display(),
            error = %e,
            "gitback: could not reconcile bare repo HEAD after bundle apply; continuing"
        );
    }
    Ok(())
}

/// Initialize a bare repo if it does not already exist. Mode `0700`
/// `root:root`. Used by both the helper daemon (first push) and the
/// receive-side handler (first incoming bundle).
pub fn ensure_bare_repo(repo: &Path) -> Result<()> {
    if repo.exists() {
        return Ok(());
    }
    if let Some(parent) = repo.parent() {
        std::fs::create_dir_all(parent)
            .with_context(|| format!("create {}", parent.display()))?;
        #[cfg(unix)]
        {
            use std::os::unix::fs::PermissionsExt;
            let _ = std::fs::set_permissions(parent, std::fs::Permissions::from_mode(0o700));
        }
    }
    let mut cmd = build_command(RunAs::Root);
    cmd.arg("init")
        .arg("--bare")
        .arg(repo)
        .stdin(Stdio::null())
        .stdout(Stdio::null())
        .stderr(Stdio::piped());
    let out = cmd.output().context("git init --bare")?;
    if !out.status.success() {
        let stderr = String::from_utf8_lossy(&out.stderr);
        bail!("git init --bare failed: {}", stderr.trim());
    }
    #[cfg(unix)]
    {
        use std::os::unix::fs::PermissionsExt;
        let _ = std::fs::set_permissions(repo, std::fs::Permissions::from_mode(0o700));
    }
    Ok(())
}

/// Ensure the bare repo's `HEAD` symref names a branch that exists, so
/// a fresh `git clone` checks out a working tree instead of warning
/// "remote HEAD refers to nonexistent ref, unable to checkout".
///
/// `git init --bare` leaves `HEAD` at git's compiled-in default
/// (`refs/heads/master`); the first push or bundle ingest establishes
/// the real branches but never repoints `HEAD`. This reconciles it,
/// preferring `main`, then `master`, then the alphabetically-first
/// branch. Idempotent: a no-op when `HEAD` already resolves to a live
/// branch, and `Ok(())` when the repo has no branches yet. Called
/// wherever a bare repo's refs can change (first push's post-receive,
/// bundle ingest) and before serving a clone, so repos created before
/// this shipped self-heal on next access. The same deterministic
/// preference on every envoy means receivers converge on the same
/// `HEAD` the originator picked.
pub fn set_head_to_default_branch(repo: &Path) -> Result<()> {
    // Branch refnames; `for-each-ref` returns them sorted by name.
    let branches = {
        let mut cmd = build_command(RunAs::Root);
        cmd.arg("-C")
            .arg(repo)
            .arg("for-each-ref")
            .arg("--format=%(refname)")
            .arg("refs/heads/")
            .stdin(Stdio::null())
            .stdout(Stdio::piped())
            .stderr(Stdio::piped());
        let out = cmd.output().context("git for-each-ref refs/heads")?;
        if !out.status.success() {
            let stderr = String::from_utf8_lossy(&out.stderr);
            bail!("git for-each-ref failed: {}", stderr.trim());
        }
        let stdout = String::from_utf8(out.stdout).context("for-each-ref output not UTF-8")?;
        stdout
            .lines()
            .map(|l| l.trim().to_string())
            .filter(|l| !l.is_empty())
            .collect::<Vec<_>>()
    };
    if branches.is_empty() {
        return Ok(()); // empty bare repo — nothing to point HEAD at yet
    }

    // Current HEAD target if it's a symref; `-q` makes a detached HEAD
    // exit non-zero with empty stdout rather than erroring.
    let current = {
        let mut cmd = build_command(RunAs::Root);
        cmd.arg("-C")
            .arg(repo)
            .arg("symbolic-ref")
            .arg("-q")
            .arg("HEAD")
            .stdin(Stdio::null())
            .stdout(Stdio::piped())
            .stderr(Stdio::null());
        let out = cmd.output().context("git symbolic-ref HEAD")?;
        if out.status.success() {
            String::from_utf8_lossy(&out.stdout).trim().to_string()
        } else {
            String::new()
        }
    };
    if branches.iter().any(|b| b == &current) {
        return Ok(()); // HEAD already names a live branch
    }

    let pick = if branches.iter().any(|b| b == "refs/heads/main") {
        "refs/heads/main"
    } else if branches.iter().any(|b| b == "refs/heads/master") {
        "refs/heads/master"
    } else {
        branches[0].as_str() // alphabetically first (for-each-ref is sorted)
    };
    let mut cmd = build_command(RunAs::Root);
    cmd.arg("-C")
        .arg(repo)
        .arg("symbolic-ref")
        .arg("HEAD")
        .arg(pick)
        .stdin(Stdio::null())
        .stdout(Stdio::null())
        .stderr(Stdio::piped());
    let out = cmd.output().context("git symbolic-ref HEAD <branch>")?;
    if !out.status.success() {
        let stderr = String::from_utf8_lossy(&out.stderr);
        bail!("git symbolic-ref HEAD {} failed: {}", pick, stderr.trim());
    }
    tracing::info!(
        repo = %repo.display(),
        head = pick,
        "gitback: repointed bare repo HEAD to a live branch"
    );
    Ok(())
}

/// Wire-only envelope for the `X-Vigo-Chain` header. Wraps the
/// authority chain plus the founder's delegation chain (ADR-019)
/// so receive-side verification can walk a post-rekey leaf back to
/// the genesis founder pubkey baked into `project_id`.
///
/// Distinct from on-disk `TokenChain` and `DelegationChain` so the
/// wire format can evolve independently of the persistent files.
#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct PushChain {
    #[serde(default = "default_push_chain_version")]
    pub version: u32,
    /// On-the-wire serialization of the user's `tokens.json`
    /// chain. Must verify against the project_id baked into the URL
    /// once `delegations` are walked.
    pub chain: Vec<crate::swarm::anchor::tokens::Token>,
    /// Optional founder rekey delegation chain (ADR-019). Empty for
    /// senders who haven't rekeyed; capped at `DELEGATION_MAX_LEN`
    /// by the verifier even when the sender presents more.
    #[serde(default, skip_serializing_if = "Vec::is_empty")]
    pub delegations: Vec<crate::swarm::anchor::envelopes::DelegationEnvelope>,
}

fn default_push_chain_version() -> u32 {
    1
}

const PUSH_CHAIN_VERSION: u32 = 1;

/// Encode the authority chain + founder delegations for the
/// `X-Vigo-Chain` header. Sender side.
pub fn encode_chain_header(
    chain: &TokenChain,
    delegations: &[crate::swarm::anchor::envelopes::DelegationEnvelope],
) -> Result<String> {
    let envelope = PushChain {
        version: PUSH_CHAIN_VERSION,
        chain: chain.chain.clone(),
        delegations: delegations.to_vec(),
    };
    let json = serde_json::to_vec(&envelope).context("serialize push chain")?;
    Ok(B64.encode(&json))
}

/// Decode the chain + delegations from the `X-Vigo-Chain` header.
pub fn decode_chain_header(value: &str) -> Result<PushChain> {
    let bytes = B64.decode(value.trim()).context("base64-decode chain header")?;
    serde_json::from_slice::<PushChain>(&bytes).context("parse PushChain JSON from chain header")
}

/// Verify a chain server-side against a project_id from the URL.
/// Computes `founder = chain[0].signer_puddle_pubkey`; verify_chain
/// checks `sha256(founder) == project_id` and rejects mismatches.
/// Also rejects any chain whose tokens have been revoked.
///
/// **ADR-019:** the `delegations` parameter (lifted from the wire
/// PushChain envelope) is threaded into `verify_chain` so a
/// post-rekey founder's leaf chains back to the genesis founder
/// pubkey baked into `project_id`. Empty list = pre-rekey,
/// behaviorally identical to pre-ADR-019. The receive side here
/// trusts sender-presented delegations to verify the chain
/// mathematically; this is acceptable because gitback is for
/// *availability*, not confidentiality (an attacker who has
/// compromised a retired founder key could push junk to bare
/// repos but cannot exfiltrate user-readable bytes — lockbox
/// owns confidentiality).
pub fn verify_chain_for_push(
    push: &PushChain,
    project_id: &[u8; ID_LEN],
    gitback_base: &Path,
    now: i64,
) -> Result<()> {
    let root = push
        .chain
        .first()
        .ok_or_else(|| anyhow!("chain is empty"))?;
    let founder = root.body.signer_puddle_pubkey;
    verify_chain(project_id, &founder, &push.delegations, &push.chain, now)
        .map_err(|e| anyhow!("chain verification: {}", e))?;
    for (i, token) in push.chain.iter().enumerate() {
        let tid = token.id();
        if is_token_revoked(gitback_base, project_id, &tid) {
            bail!(
                "token at chain depth {} ({}) is revoked",
                i,
                hex::encode(&tid[..8])
            );
        }
    }
    Ok(())
}

/// Ingest a bundle that's already staged on disk into the project's
/// bare repo: ensure the repo exists, snapshot refs before/after,
/// `git bundle verify` + `git fetch` the bundle. Returns whether refs
/// changed. The caller owns `bundle_path`'s lifecycle — this never
/// unlinks it.
///
/// `dr_scope` enforcement (ADR-025 Slice 3): refuses ingest if the
/// project isn't locally tracked under any puddle state root on
/// this envoy. The existence of local state IS the v1 membership
/// proof, so an envoy that never ran `init`/`join` (and therefore
/// never wrote `project.json`) gets clean refusal instead of
/// silently materializing a bare repo it shouldn't hold.
pub fn ingest_bundle_file(
    gitback_base: &Path,
    project_id: &[u8; ID_LEN],
    bundle_path: &Path,
) -> Result<bool> {
    super::dr_scope::ensure_locally_tracked(project_id)?;
    let repo = project_repo_path(gitback_base, project_id);
    ensure_bare_repo(&repo)?;
    let before = snapshot_refs(&repo).unwrap_or_default();
    apply_bundle(&repo, bundle_path)?;
    let after = snapshot_refs(&repo).unwrap_or_default();
    Ok(before != after)
}

/// Receive-side bundle ingestion from a streaming source. Stages `src`
/// to a temp file under `incoming/` — refusing it if it exceeds
/// `max_bytes` (so a runaway peer can't fill the disk) — then
/// [`ingest_bundle_file`]s it. Never holds the whole bundle in RAM.
/// Unlinks the temp file regardless of outcome. The cap is passed in
/// (callers read it from [`crate::swarm::runtime_limits::gitback_max_bundle_bytes`])
/// so this stays pure.
pub fn ingest_bundle<R: Read>(
    gitback_base: &Path,
    project_id: &[u8; ID_LEN],
    mut src: R,
    max_bytes: u64,
) -> Result<bool> {
    let staging = incoming_dir(gitback_base);
    std::fs::create_dir_all(&staging)
        .with_context(|| format!("create {}", staging.display()))?;
    #[cfg(unix)]
    {
        use std::os::unix::fs::PermissionsExt;
        let _ = std::fs::set_permissions(&staging, std::fs::Permissions::from_mode(0o700));
    }

    let nonce: u128 = rand::random();
    let bundle_path = staging.join(format!("{}-{:032x}.bundle", hex_id(project_id), nonce));

    let result = (|| -> Result<bool> {
        // `take(max_bytes + 1)` so a copied count of `max_bytes + 1`
        // distinguishes "exactly at the cap" (OK) from "over it" (reject).
        let written = {
            let mut f = File::create(&bundle_path)
                .with_context(|| format!("create {}", bundle_path.display()))?;
            let mut limited = (&mut src).take(max_bytes.saturating_add(1));
            let n = io::copy(&mut limited, &mut f).context("staging bundle to disk")?;
            f.sync_all().context("sync staged bundle")?;
            n
        };
        if written > max_bytes {
            bail!(
                "bundle exceeds the {}-byte cap (swarm.gitback.max_bundle_bytes)",
                max_bytes
            );
        }
        ingest_bundle_file(gitback_base, project_id, &bundle_path)
    })();

    let _ = std::fs::remove_file(&bundle_path);
    result
}

/// Receiver-initiated DR catch-up. Pull a `git bundle --all` of
/// `project_id` from `url_host` (an IP or hostname; the swarm peer
/// port is appended) and ingest it into the local bare repo, creating
/// the repo if it doesn't exist. The user's authority chain +
/// delegations ride the `X-Vigo-Chain` header so the serving peer can
/// verify membership exactly as it does on a push. Returns whether
/// refs changed locally.
///
/// This is the path that closes the bootstrap gap in swarm-wide DR:
/// `project init` / `join` only writes per-user state, and the
/// post-receive fan-out only fires on a ref delta, so a freshly-
/// enrolled or re-joined member never received the bytes until the
/// founder happened to push. Now the catch-up reactor pulls them.
pub fn fetch_bundle_from_peer(
    gitback_base: &Path,
    project_id: &[u8; ID_LEN],
    chain: &TokenChain,
    delegations: &[crate::swarm::anchor::envelopes::DelegationEnvelope],
    url_host: &str,
    tls: &crate::update::TlsCerts,
) -> Result<bool> {
    let chain_header = encode_chain_header(chain, delegations)?;
    let agent = build_push_agent(tls)?;
    let pid_hex = hex_id(project_id);
    let url = format!(
        "https://{}:{}{}{}",
        url_host,
        crate::swarm::server::DEFAULT_PORT,
        BUNDLE_GET_PREFIX,
        pid_hex
    );
    let resp = agent
        .get(&url)
        .header(CHAIN_HEADER, &chain_header)
        .send()
        .with_context(|| format!("GET {}", url))?;
    let status = resp.status();
    if !status.is_success() {
        bail!("peer returned {}", status);
    }
    // Stream the response body straight to disk through `ingest_bundle`
    // (which caps it and runs `git bundle verify` before touching the
    // object store) — never buffer a multi-hundred-MB bundle in RAM.
    // `reqwest::blocking::Response` is itself a `Read` over the socket.
    ingest_bundle(
        gitback_base,
        project_id,
        resp,
        crate::swarm::runtime_limits::gitback_max_bundle_bytes(),
    )
}

/// Fan out a bundle to every peer that has advertised holding this
/// project. Targets come from two sources, deduplicated:
///
/// 1. **LAN auto-discovery** — peers that emitted
///    `GITBACKPROJECT:<host>:<pid_hex>` lines on the swarm
///    multicast within the [`super::project_peers::STALE_SECS`]
///    window, indexed by [`super::project_peers::ProjectPeers`].
/// 2. **`cross_subnet_members:`** — explicit hostnames listed in the
///    user's local `project.json`. Useful for peers that aren't on
///    the same LAN as the originator (no multicast reachability).
///
/// Returns `(attempted, succeeded)`. Errors talking to a single peer
/// don't abort the sweep — fan-out is best-effort, gossip / catch-up
/// will reconcile.
pub fn fan_out_bundle(
    home: &Path,
    project_id: &[u8; ID_LEN],
    chain: &TokenChain,
    delegations: &[crate::swarm::anchor::envelopes::DelegationEnvelope],
    bundle_path: &Path,
    tls: &crate::update::TlsCerts,
) -> (usize, usize) {
    let cfg = match ProjectConfig::load(home, project_id) {
        Ok(Some(c)) => c,
        Ok(None) => {
            tracing::warn!(
                project_id = %hex_id(project_id),
                home = %home.display(),
                "fan-out skipped: no project.json on disk"
            );
            return (0, 0);
        }
        Err(e) => {
            tracing::warn!(
                project_id = %hex_id(project_id),
                error = %e,
                "fan-out skipped: error loading project.json"
            );
            return (0, 0);
        }
    };

    let pid_hex = hex_id(project_id);
    let local_hostname = local_hostname();

    // Build the deduplicated target list. LAN-auto-discovered hosts
    // are addressed by IP (we have the source addr from the announce
    // datagram); cross_subnet_members are addressed by hostname (DNS
    // resolves at connect time). De-dupe by string identity of the
    // URL hostpart so the same host listed in both buckets only
    // receives one push.
    use std::collections::BTreeMap;
    let mut targets: BTreeMap<String, String> = BTreeMap::new(); // url-host → display-host
    if let Some(g) = super::project_peers::global() {
        let now = std::time::SystemTime::now()
            .duration_since(std::time::UNIX_EPOCH)
            .map(|d| d.as_secs() as i64)
            .unwrap_or(0);
        for entry in g.peers_for(&pid_hex, now) {
            // Don't push to ourselves.
            if let Some(my) = local_hostname.as_deref() {
                if entry.host.eq_ignore_ascii_case(my) {
                    continue;
                }
            }
            let url_host = match entry.addr {
                Some(std::net::IpAddr::V4(v4)) => v4.to_string(),
                Some(std::net::IpAddr::V6(v6)) => format!("[{}]", v6),
                None => entry.host.clone(),
            };
            targets.insert(url_host, entry.host);
        }
    }
    for hostname in &cfg.cross_subnet_members {
        if let Some(my) = local_hostname.as_deref() {
            if hostname.eq_ignore_ascii_case(my) {
                continue;
            }
        }
        targets.entry(hostname.clone()).or_insert_with(|| hostname.clone());
    }

    if targets.is_empty() {
        tracing::debug!(
            project_id = %pid_hex,
            "fan-out: no peers known (no LAN announces seen, no cross_subnet_members configured)"
        );
        return (0, 0);
    }

    let chain_header = match encode_chain_header(chain, delegations) {
        Ok(s) => s,
        Err(e) => {
            tracing::warn!(error = %e, "fan-out: encode chain header failed");
            return (0, 0);
        }
    };

    let agent = match build_push_agent(tls) {
        Ok(a) => a,
        Err(e) => {
            tracing::warn!(error = %e, "fan-out: cannot build mTLS HTTP agent");
            return (0, 0);
        }
    };

    // Cap check from the file size — the bundle stays on disk and is
    // streamed per peer, never buffered. Operator-tunable via
    // server.yaml `swarm.gitback.max_bundle_bytes` (default 2 GiB);
    // refusing here is a publisher-side courtesy — the receivers' POST
    // handlers enforce the same cap independently.
    let bundle_size = match std::fs::metadata(bundle_path) {
        Ok(m) => m.len(),
        Err(e) => {
            tracing::warn!(
                bundle = %bundle_path.display(),
                error = %e,
                "fan-out: cannot stat bundle for upload"
            );
            return (0, 0);
        }
    };
    let cap = crate::swarm::runtime_limits::gitback_max_bundle_bytes();
    if bundle_size > cap {
        tracing::warn!(
            project_id = %pid_hex,
            bundle = %bundle_path.display(),
            size = bundle_size,
            cap,
            "fan-out: bundle exceeds swarm.gitback.max_bundle_bytes; skipping fan-out"
        );
        return (0, 0);
    }

    let mut attempted = 0;
    let mut succeeded = 0;
    for (url_host, display_host) in &targets {
        attempted += 1;
        let url = format!(
            "https://{}:{}/swarm/gitback/push/{}",
            url_host,
            crate::swarm::server::DEFAULT_PORT,
            pid_hex
        );
        match push_to_peer(&agent, &url, &chain_header, bundle_path) {
            Ok(refs_changed) => {
                tracing::info!(
                    project_id = %pid_hex,
                    peer = %display_host,
                    via = %url_host,
                    refs_changed,
                    "fan-out: peer accepted bundle"
                );
                succeeded += 1;
            }
            Err(e) => {
                tracing::warn!(
                    project_id = %pid_hex,
                    peer = %display_host,
                    via = %url_host,
                    error = %e,
                    "fan-out: peer push failed"
                );
            }
        }
    }
    (attempted, succeeded)
}

fn local_hostname() -> Option<String> {
    std::fs::read_to_string("/etc/hostname")
        .ok()
        .map(|s| s.trim().to_string())
        .filter(|s| !s.is_empty())
}

// ============================================================
// Envelope gossip (ADR-015 step 4 fourth slice)
//
// After save_revoke / save_leave persists an envelope locally, the
// reactor fans the envelope out to every known peer for the project
// so a token revoked on one envoy is refused on every envoy within
// one gossip cycle. Simple POST + on-receive idempotent save.
//
// For peers that were offline during gossip, periodic
// digest-reconciliation closes the gap: once per reactor sweep we
// fetch each peer's /digest and pull /all on mismatch. The local
// `save_*` calls are idempotent so a re-merge of overlapping data
// is cheap.
// ============================================================

/// Gossip a revocation envelope to every known peer for the project.
/// Best-effort — errors per peer are logged at warn but never abort
/// the sweep.
pub fn gossip_revoke_to_peers(
    home: &Path,
    project_id: &[u8; ID_LEN],
    envelope: &super::envelopes::RevokeEnvelope,
    tls: &crate::update::TlsCerts,
) -> (usize, usize) {
    gossip_envelope_inner(home, project_id, "revokes", envelope, tls)
}

/// Gossip a leave envelope to every known peer for the project.
pub fn gossip_leave_to_peers(
    home: &Path,
    project_id: &[u8; ID_LEN],
    envelope: &super::envelopes::LeaveEnvelope,
    tls: &crate::update::TlsCerts,
) -> (usize, usize) {
    gossip_envelope_inner(home, project_id, "leaves", envelope, tls)
}

fn gossip_envelope_inner<T: serde::Serialize>(
    home: &Path,
    project_id: &[u8; ID_LEN],
    kind: &str,
    envelope: &T,
    tls: &crate::update::TlsCerts,
) -> (usize, usize) {
    let cfg = match ProjectConfig::load(home, project_id) {
        Ok(Some(c)) => c,
        _ => return (0, 0),
    };
    let pid_hex = hex_id(project_id);
    let local = local_hostname();
    let mut targets: std::collections::BTreeMap<String, String> =
        std::collections::BTreeMap::new();
    if let Some(g) = super::project_peers::global() {
        let now = std::time::SystemTime::now()
            .duration_since(std::time::UNIX_EPOCH)
            .map(|d| d.as_secs() as i64)
            .unwrap_or(0);
        for entry in g.peers_for(&pid_hex, now) {
            if let Some(my) = local.as_deref() {
                if entry.host.eq_ignore_ascii_case(my) {
                    continue;
                }
            }
            let url_host = match entry.addr {
                Some(std::net::IpAddr::V4(v4)) => v4.to_string(),
                Some(std::net::IpAddr::V6(v6)) => format!("[{}]", v6),
                None => entry.host.clone(),
            };
            targets.insert(url_host, entry.host);
        }
    }
    for hostname in &cfg.cross_subnet_members {
        if let Some(my) = local.as_deref() {
            if hostname.eq_ignore_ascii_case(my) {
                continue;
            }
        }
        targets
            .entry(hostname.clone())
            .or_insert_with(|| hostname.clone());
    }
    if targets.is_empty() {
        return (0, 0);
    }

    let agent = match build_push_agent(tls) {
        Ok(a) => a,
        Err(e) => {
            tracing::warn!(error = %e, "envelope gossip: cannot build mTLS HTTP agent");
            return (0, 0);
        }
    };
    let body = match serde_json::to_vec(envelope) {
        Ok(b) => b,
        Err(e) => {
            tracing::warn!(error = %e, "envelope gossip: serialize failed");
            return (0, 0);
        }
    };

    let mut attempted = 0;
    let mut succeeded = 0;
    for (url_host, display_host) in &targets {
        attempted += 1;
        let url = format!(
            "https://{}:{}/swarm/gitback/envelopes/{}/{}",
            url_host,
            crate::swarm::server::DEFAULT_PORT,
            pid_hex,
            kind,
        );
        match agent
            .post(&url)
            .header("Content-Type", "application/json")
            .body(body.clone())
            .send()
        {
            Ok(resp) if resp.status().is_success() => {
                succeeded += 1;
                tracing::debug!(
                    project_id = %pid_hex,
                    peer = %display_host,
                    kind,
                    "envelope gossip: peer accepted"
                );
            }
            Ok(resp) => {
                tracing::warn!(
                    project_id = %pid_hex,
                    peer = %display_host,
                    kind,
                    status = %resp.status(),
                    "envelope gossip: peer rejected"
                );
            }
            Err(e) => {
                tracing::debug!(
                    project_id = %pid_hex,
                    peer = %display_host,
                    kind,
                    error = %e,
                    "envelope gossip: peer push failed (offline / unreachable)"
                );
            }
        }
    }
    (attempted, succeeded)
}

/// JSON shape of `GET /swarm/gitback/envelopes/<pid>/digest`.
#[derive(Debug, Clone, serde::Serialize, serde::Deserialize, PartialEq, Eq)]
pub struct DigestResponse {
    pub digest_hex: String,
    pub revokes: usize,
    pub leaves: usize,
}

impl DigestResponse {
    pub fn for_project(gitback_base: &Path, project_id: &[u8; ID_LEN]) -> Result<Self> {
        let digest = super::envelopes::project_digest(gitback_base, project_id)?;
        let revokes = super::envelopes::load_revokes(gitback_base, project_id)?.len();
        let leaves = super::envelopes::load_leaves(gitback_base, project_id)?.len();
        Ok(Self {
            digest_hex: hex::encode(digest),
            revokes,
            leaves,
        })
    }
}

/// JSON shape of `GET /swarm/gitback/envelopes/<pid>/all`.
#[derive(Debug, Clone, serde::Serialize, serde::Deserialize)]
pub struct EnvelopesBundle {
    pub revokes: Vec<super::envelopes::RevokeEnvelope>,
    pub leaves: Vec<super::envelopes::LeaveEnvelope>,
}

impl EnvelopesBundle {
    pub fn for_project(gitback_base: &Path, project_id: &[u8; ID_LEN]) -> Result<Self> {
        Ok(Self {
            revokes: super::envelopes::load_revokes(gitback_base, project_id)?,
            leaves: super::envelopes::load_leaves(gitback_base, project_id)?,
        })
    }

    /// Merge into the local envelopes store. Returns
    /// `(revokes_applied, leaves_applied)`. Idempotent — counts only
    /// envelopes that actually changed disk state. Skips any
    /// envelope whose signature fails verification.
    pub fn merge_into(
        &self,
        gitback_base: &Path,
    ) -> (usize, usize) {
        let mut revokes_applied = 0;
        let mut leaves_applied = 0;
        for r in &self.revokes {
            // A1: authorize before persisting a gossiped revoke — the
            // signer must hold Admin authority chaining back to the
            // founder. An unauthorized (forged) revoke is dropped here.
            if let Err(e) = r.verify_revoke_authority() {
                tracing::warn!(
                    token_id = %r.token_id,
                    error = %e,
                    "envelope merge: dropping unauthorized revoke"
                );
                continue;
            }
            match super::envelopes::save_revoke(gitback_base, r) {
                Ok(true) => revokes_applied += 1,
                Ok(false) => {}
                Err(e) => {
                    tracing::warn!(
                        token_id = %r.token_id,
                        error = %e,
                        "envelope merge: save_revoke failed"
                    );
                }
            }
        }
        for l in &self.leaves {
            match super::envelopes::save_leave(gitback_base, l) {
                Ok(true) => leaves_applied += 1,
                Ok(false) => {}
                Err(e) => {
                    tracing::warn!(
                        leaving = %l.leaving_puddle_pubkey,
                        error = %e,
                        "envelope merge: save_leave failed"
                    );
                }
            }
        }
        (revokes_applied, leaves_applied)
    }
}

/// Reconcile envelopes for a single project against every known
/// peer: hit `GET /digest`, compare, pull `GET /all` on mismatch,
/// merge. Returns total `(revokes_applied, leaves_applied)` across
/// all peers. Best-effort.
pub fn reconcile_envelopes(
    gitback_base: &Path,
    home: &Path,
    project_id: &[u8; ID_LEN],
    tls: &crate::update::TlsCerts,
) -> (usize, usize) {
    let pid_hex = hex_id(project_id);
    let local_digest = match DigestResponse::for_project(gitback_base, project_id) {
        Ok(d) => d,
        Err(_) => return (0, 0),
    };

    let cfg = match ProjectConfig::load(home, project_id) {
        Ok(Some(c)) => c,
        _ => return (0, 0),
    };
    let local = local_hostname();
    let mut targets: std::collections::BTreeMap<String, String> =
        std::collections::BTreeMap::new();
    if let Some(g) = super::project_peers::global() {
        let now = std::time::SystemTime::now()
            .duration_since(std::time::UNIX_EPOCH)
            .map(|d| d.as_secs() as i64)
            .unwrap_or(0);
        for entry in g.peers_for(&pid_hex, now) {
            if let Some(my) = local.as_deref() {
                if entry.host.eq_ignore_ascii_case(my) {
                    continue;
                }
            }
            let url_host = match entry.addr {
                Some(std::net::IpAddr::V4(v4)) => v4.to_string(),
                Some(std::net::IpAddr::V6(v6)) => format!("[{}]", v6),
                None => entry.host.clone(),
            };
            targets.insert(url_host, entry.host);
        }
    }
    for hostname in &cfg.cross_subnet_members {
        if let Some(my) = local.as_deref() {
            if hostname.eq_ignore_ascii_case(my) {
                continue;
            }
        }
        targets
            .entry(hostname.clone())
            .or_insert_with(|| hostname.clone());
    }
    if targets.is_empty() {
        return (0, 0);
    }

    let agent = match build_push_agent(tls) {
        Ok(a) => a,
        Err(e) => {
            tracing::debug!(error = %e, "reconcile: cannot build mTLS HTTP agent");
            return (0, 0);
        }
    };
    let mut total_revokes = 0;
    let mut total_leaves = 0;
    for (url_host, display_host) in &targets {
        let digest_url = format!(
            "https://{}:{}/swarm/gitback/envelopes/{}/digest",
            url_host,
            crate::swarm::server::DEFAULT_PORT,
            pid_hex,
        );
        let peer_digest: DigestResponse = match agent.get(&digest_url).send() {
            Ok(resp) if resp.status().is_success() => {
                let bytes = match resp.bytes() {
                    Ok(b) => b,
                    Err(_) => continue,
                };
                match serde_json::from_slice(&bytes) {
                    Ok(d) => d,
                    Err(_) => continue,
                }
            }
            _ => continue,
        };
        if peer_digest.digest_hex == local_digest.digest_hex {
            continue;
        }
        // Mismatch — pull /all and merge. Symmetric: peer also sees a
        // mismatch and will pull from us next cycle, so missing-on-our-
        // side records get filled in too.
        let all_url = format!(
            "https://{}:{}/swarm/gitback/envelopes/{}/all",
            url_host,
            crate::swarm::server::DEFAULT_PORT,
            pid_hex,
        );
        let bundle: EnvelopesBundle = match agent.get(&all_url).send() {
            Ok(resp) if resp.status().is_success() => {
                let bytes = match resp.bytes() {
                    Ok(b) => b,
                    Err(_) => continue,
                };
                match serde_json::from_slice(&bytes) {
                    Ok(b) => b,
                    Err(_) => continue,
                }
            }
            _ => continue,
        };
        let (rev, lev) = bundle.merge_into(gitback_base);
        if rev > 0 || lev > 0 {
            tracing::info!(
                project_id = %pid_hex,
                peer = %display_host,
                revokes_applied = rev,
                leaves_applied = lev,
                "envelope reconcile: merged peer state"
            );
        }
        total_revokes += rev;
        total_leaves += lev;
    }
    (total_revokes, total_leaves)
}

fn push_to_peer(
    agent: &crate::swarm::tls::SwarmAgent,
    url: &str,
    chain_header: &str,
    bundle_path: &Path,
) -> Result<bool> {
    let file = File::open(bundle_path)
        .with_context(|| format!("open {}", bundle_path.display()))?;
    // `File: Into<reqwest::blocking::Body>` streams the bundle straight
    // off disk — nothing buffered in RAM, matching the previous
    // `SendBody::from_owned_reader`.
    let resp = agent
        .post(url)
        .header(CHAIN_HEADER, chain_header)
        .header("Content-Type", "application/x-git-bundle")
        .body(file)
        .send()
        .with_context(|| format!("POST {}", url))?;
    let status = resp.status();
    if !status.is_success() {
        bail!("peer returned {}", status);
    }
    let body_bytes = resp.bytes().context("read response body")?;
    let parsed: serde_json::Value =
        serde_json::from_slice(&body_bytes).context("parse JSON response")?;
    let refs_changed = parsed
        .get("refs_changed")
        .and_then(|v| v.as_bool())
        .unwrap_or(false);
    Ok(refs_changed)
}

/// Build the mTLS-configured client used for gitback bundle fan-out.
///
/// Server-cert verification chains the peer's cert to the fleet CA but
/// skips the hostname/SAN check — envoy certs are CN'd by agent UUID, not
/// the dialed IP. See `swarm::tls::FleetCaVerifier`. Peer identity is the
/// CA (mTLS in both directions); the gitback objects pushed/pulled are
/// additionally chain-verified (signatures over the bundle / commit
/// objects), so this path is doubly protected.
fn build_push_agent(tls: &crate::update::TlsCerts) -> Result<crate::swarm::tls::SwarmAgent> {
    crate::swarm::tls::build_client(tls, Duration::from_secs(5), PEER_PUSH_TIMEOUT)
}

#[cfg(test)]
mod tests {
    use super::*;
    use ed25519_dalek::SigningKey;
    use rand::rngs::OsRng;

    use crate::swarm::anchor::tokens::{issue_bootstrap, issue_child, project_id_for, Role};

    fn fresh_key() -> SigningKey {
        SigningKey::generate(&mut OsRng)
    }

    fn pub_bytes(k: &SigningKey) -> [u8; ID_LEN] {
        k.verifying_key().to_bytes()
    }

    fn seed_repo(dir: &Path, name: &str) -> PathBuf {
        let repo = dir.join(format!("{}.git", name));
        ensure_bare_repo(&repo).unwrap();
        // Add a single commit via plumbing so for-each-ref returns at
        // least one row. Need a working tree; make a sibling clone.
        let work = dir.join(format!("{}-work", name));
        std::fs::create_dir_all(&work).unwrap();
        run_git(&work, &["init", "-q", "-b", "main"]);
        run_git(&work, &["config", "user.email", "t@example"]);
        run_git(&work, &["config", "user.name", "t"]);
        std::fs::write(work.join("README.md"), "hello\n").unwrap();
        run_git(&work, &["add", "README.md"]);
        run_git(&work, &["commit", "-q", "-m", "init"]);
        run_git(
            &work,
            &["push", "-q", repo.to_str().unwrap(), "HEAD:refs/heads/main"],
        );
        repo
    }

    fn run_git(cwd: &Path, args: &[&str]) {
        let mut cmd = build_command(RunAs::Root);
        cmd.arg("-C").arg(cwd).args(args);
        let out = cmd.output().expect("spawn git");
        if !out.status.success() {
            let stderr = String::from_utf8_lossy(&out.stderr);
            panic!("git {:?} failed: {}", args, stderr);
        }
    }

    /// `git symbolic-ref HEAD` against a (bare) repo, trimmed.
    fn read_head(repo: &Path) -> String {
        let mut cmd = build_command(RunAs::Root);
        cmd.arg("-C").arg(repo).arg("symbolic-ref").arg("HEAD");
        let out = cmd.output().expect("spawn git symbolic-ref");
        assert!(out.status.success(), "symbolic-ref HEAD failed");
        String::from_utf8_lossy(&out.stdout).trim().to_string()
    }

    /// Seed a bare repo with branches `develop` + `master` (no `main`).
    fn seed_repo_with_branches(dir: &Path, name: &str, branches: &[&str]) -> PathBuf {
        let repo = dir.join(format!("{}.git", name));
        ensure_bare_repo(&repo).unwrap();
        let work = dir.join(format!("{}-work", name));
        std::fs::create_dir_all(&work).unwrap();
        run_git(&work, &["init", "-q", "-b", branches[0]]);
        run_git(&work, &["config", "user.email", "t@example"]);
        run_git(&work, &["config", "user.name", "t"]);
        std::fs::write(work.join("f"), "x\n").unwrap();
        run_git(&work, &["add", "f"]);
        run_git(&work, &["commit", "-q", "-m", "c"]);
        for b in &branches[1..] {
            run_git(&work, &["branch", b]);
        }
        let mut push_args = vec!["push", "-q", repo.to_str().unwrap()];
        let specs: Vec<String> = branches
            .iter()
            .map(|b| format!("refs/heads/{b}:refs/heads/{b}"))
            .collect();
        for s in &specs {
            push_args.push(s);
        }
        run_git(&work, &push_args);
        repo
    }

    fn git_available() -> bool {
        std::process::Command::new("git")
            .arg("--version")
            .output()
            .map(|o| o.status.success())
            .unwrap_or(false)
    }

    #[test]
    fn snapshot_empty_repo_is_empty() {
        if !git_available() {
            return;
        }
        let dir = tempfile::tempdir().unwrap();
        let repo = dir.path().join("empty.git");
        ensure_bare_repo(&repo).unwrap();
        let snap = snapshot_refs(&repo).unwrap();
        assert!(snap.is_empty());
    }

    #[test]
    fn build_and_apply_bundle_roundtrip() {
        if !git_available() {
            return;
        }
        let dir = tempfile::tempdir().unwrap();
        let src = seed_repo(dir.path(), "src");
        let bundle = dir.path().join("test.bundle");
        let size = build_bundle(&src, &bundle).unwrap();
        assert!(size > 0);

        let dst = dir.path().join("dst.git");
        ensure_bare_repo(&dst).unwrap();
        let before = snapshot_refs(&dst).unwrap();
        assert!(before.is_empty());
        apply_bundle(&dst, &bundle).unwrap();
        let after = snapshot_refs(&dst).unwrap();
        assert!(!after.is_empty(), "applied bundle should populate refs");
        assert!(after.contains_key("refs/heads/main"));
    }

    #[test]
    #[serial_test::serial]
    fn ingest_bundle_file_idempotent_returns_no_change() {
        if !git_available() {
            return;
        }
        // SAFETY: serialized against the dr_scope refusal test that
        // explicitly clears this var; pre-existing bundle-mechanics
        // tests opt out of the gate to keep their setup minimal.
        unsafe {
            std::env::set_var("VIGO_GITBACK_DR_SCOPE_BYPASS", "1");
        }
        let dir = tempfile::tempdir().unwrap();
        let base = dir.path().to_path_buf();
        let pid = [0x77u8; ID_LEN];

        let src = seed_repo(dir.path(), "src");
        let bundle_tmp = dir.path().join("first.bundle");
        build_bundle(&src, &bundle_tmp).unwrap();

        // Pre-seed the destination bare repo at the canonical path from
        // the src so the next ingest of the same bundle is a no-op.
        let dst = project_repo_path(&base, &pid);
        ensure_bare_repo(&dst).unwrap();
        apply_bundle(&dst, &bundle_tmp).unwrap();

        let changed = ingest_bundle_file(&base, &pid, &bundle_tmp).unwrap();
        assert!(!changed, "idempotent re-ingest should report refs_changed=false");
    }

    #[test]
    #[serial_test::serial]
    fn ingest_bundle_file_advances_empty_repo() {
        if !git_available() {
            return;
        }
        // SAFETY: serialized against the dr_scope refusal test.
        unsafe {
            std::env::set_var("VIGO_GITBACK_DR_SCOPE_BYPASS", "1");
        }
        let dir = tempfile::tempdir().unwrap();
        let base = dir.path().to_path_buf();
        let pid = [0x88u8; ID_LEN];

        let src = seed_repo(dir.path(), "src");
        let bundle_tmp = dir.path().join("first.bundle");
        build_bundle(&src, &bundle_tmp).unwrap();

        let changed = ingest_bundle_file(&base, &pid, &bundle_tmp).unwrap();
        assert!(changed, "first ingest into empty repo should report refs_changed=true");
        let dst = project_repo_path(&base, &pid);
        let snap = snapshot_refs(&dst).unwrap();
        assert!(snap.contains_key("refs/heads/main"));
    }

    #[test]
    #[serial_test::serial]
    fn ingest_bundle_streams_to_disk_and_enforces_cap() {
        if !git_available() {
            return;
        }
        // SAFETY: serialized against the dr_scope refusal test.
        unsafe {
            std::env::set_var("VIGO_GITBACK_DR_SCOPE_BYPASS", "1");
        }
        let dir = tempfile::tempdir().unwrap();
        let src = seed_repo(dir.path(), "src");
        let bundle_tmp = dir.path().join("first.bundle");
        let sz = build_bundle(&src, &bundle_tmp).unwrap();
        assert!(sz > 1);

        // Under the cap: streams in, refs advance.
        let base_ok = dir.path().join("ok-base");
        let pid_ok = [0x91u8; ID_LEN];
        let changed = ingest_bundle(
            &base_ok,
            &pid_ok,
            File::open(&bundle_tmp).unwrap(),
            crate::swarm::gitback::DEFAULT_MAX_BUNDLE_SIZE,
        )
        .unwrap();
        assert!(changed);
        assert!(snapshot_refs(&project_repo_path(&base_ok, &pid_ok))
            .unwrap()
            .contains_key("refs/heads/main"));

        // Over the cap: rejected, nothing left behind in `incoming/`.
        let base_cap = dir.path().join("cap-base");
        let pid_cap = [0x92u8; ID_LEN];
        let err = ingest_bundle(&base_cap, &pid_cap, File::open(&bundle_tmp).unwrap(), sz - 1)
            .unwrap_err();
        assert!(err.to_string().contains("cap"), "got: {err}");
        let leftovers: Vec<_> = std::fs::read_dir(incoming_dir(&base_cap))
            .map(|it| it.flatten().collect())
            .unwrap_or_default();
        assert!(leftovers.is_empty(), "staged bundle should be cleaned up");
    }

    #[test]
    fn chain_header_round_trips() {
        let founder = fresh_key();
        let bootstrap = issue_bootstrap(&founder, "proj", 100, None).unwrap();
        let chain = TokenChain {
            version: super::super::project_state::PROJECT_STATE_VERSION,
            chain: vec![bootstrap],
        };
        let header = encode_chain_header(&chain, &[]).unwrap();
        let parsed = decode_chain_header(&header).unwrap();
        assert_eq!(parsed.chain.len(), 1);
        assert_eq!(parsed.chain, chain.chain);
        assert!(parsed.delegations.is_empty());
    }

    #[test]
    fn verify_chain_for_push_accepts_well_formed_chain() {
        let dir = tempfile::tempdir().unwrap();
        let founder = fresh_key();
        let bootstrap = issue_bootstrap(&founder, "proj", 100, None).unwrap();
        let pid = project_id_for(&pub_bytes(&founder), "proj");
        let push = PushChain {
            version: PUSH_CHAIN_VERSION,
            chain: vec![bootstrap],
            delegations: Vec::new(),
        };
        verify_chain_for_push(&push, &pid, dir.path(), 200).unwrap();
    }

    #[test]
    fn verify_chain_for_push_rejects_wrong_project_id() {
        let dir = tempfile::tempdir().unwrap();
        let founder = fresh_key();
        let other = fresh_key();
        let bootstrap = issue_bootstrap(&founder, "proj", 100, None).unwrap();
        let push = PushChain {
            version: PUSH_CHAIN_VERSION,
            chain: vec![bootstrap],
            delegations: Vec::new(),
        };
        let wrong_pid = project_id_for(&pub_bytes(&other), "proj");
        let err = verify_chain_for_push(&push, &wrong_pid, dir.path(), 200).unwrap_err();
        assert!(err.to_string().contains("chain verification"));
    }

    #[test]
    fn digest_response_for_project_returns_zero_counts_when_empty() {
        let dir = tempfile::tempdir().unwrap();
        let pid = [0x55u8; ID_LEN];
        let d = DigestResponse::for_project(dir.path(), &pid).unwrap();
        assert_eq!(d.revokes, 0);
        assert_eq!(d.leaves, 0);
        // digest is sha256 over an empty hash chain — fixed-shape, but
        // we just assert it's valid hex of the expected width.
        assert_eq!(d.digest_hex.len(), super::super::envelopes::DIGEST_LEN * 2);
    }

    #[test]
    fn digest_response_changes_when_envelope_added() {
        let dir = tempfile::tempdir().unwrap();
        let pid = [0x55u8; ID_LEN];
        let admin = fresh_key();
        let d_before = DigestResponse::for_project(dir.path(), &pid).unwrap();
        let env = super::super::envelopes::RevokeEnvelope::build(
            &admin,
            &pid,
            &[0x42u8; ID_LEN],
            100,
            "",
        );
        super::super::envelopes::save_revoke(dir.path(), &env).unwrap();
        let d_after = DigestResponse::for_project(dir.path(), &pid).unwrap();
        assert_ne!(d_before.digest_hex, d_after.digest_hex);
        assert_eq!(d_after.revokes, 1);
    }

    #[test]
    fn envelopes_bundle_merge_is_idempotent() {
        let src = tempfile::tempdir().unwrap();
        let dst = tempfile::tempdir().unwrap();
        // Founder-signed revoke so it passes the A1 authorization check
        // in merge_into. The founder bootstrap is itself an Admin token.
        let founder = fresh_key();
        let leaver = fresh_key();
        let pid = project_id_for(&pub_bytes(&founder), "merge-proj");
        let bootstrap = issue_bootstrap(&founder, "merge-proj", 99, None).unwrap();

        let r1 = super::super::envelopes::RevokeEnvelope::build(
            &founder,
            &pid,
            &[0x01u8; ID_LEN],
            100,
            "",
        )
        .with_authority(vec![bootstrap], vec![]);
        let l1 = super::super::envelopes::LeaveEnvelope::build(&leaver, &pid, 200);
        super::super::envelopes::save_revoke(src.path(), &r1).unwrap();
        super::super::envelopes::save_leave(src.path(), &l1).unwrap();

        let bundle = EnvelopesBundle::for_project(src.path(), &pid).unwrap();
        let (r, l) = bundle.merge_into(dst.path());
        assert_eq!(r, 1);
        assert_eq!(l, 1);

        // Second merge of the same bundle: idempotent — counts zero.
        let (r, l) = bundle.merge_into(dst.path());
        assert_eq!(r, 0);
        assert_eq!(l, 0);
    }

    #[test]
    fn envelopes_bundle_merge_skips_invalid_signatures() {
        let dst = tempfile::tempdir().unwrap();
        let admin = fresh_key();
        let pid = [0xAAu8; ID_LEN];
        let mut r = super::super::envelopes::RevokeEnvelope::build(
            &admin,
            &pid,
            &[0x01u8; ID_LEN],
            100,
            "",
        );
        // Tamper with the signature so verify_signature fails.
        r.signature = "00".repeat(crate::swarm::anchor::tokens::SIG_LEN);
        let bundle = EnvelopesBundle {
            revokes: vec![r],
            leaves: vec![],
        };
        let (applied, _) = bundle.merge_into(dst.path());
        assert_eq!(applied, 0, "tampered envelope must not be persisted");
        let stored = super::super::envelopes::load_revokes(dst.path(), &pid).unwrap();
        assert!(stored.is_empty());
    }

    #[test]
    fn digest_response_json_round_trips() {
        let d = DigestResponse {
            digest_hex: "abcd".to_string(),
            revokes: 3,
            leaves: 1,
        };
        let json = serde_json::to_string(&d).unwrap();
        let parsed: DigestResponse = serde_json::from_str(&json).unwrap();
        assert_eq!(parsed, d);
    }

    #[test]
    fn verify_chain_for_push_rejects_revoked_token() {
        let dir = tempfile::tempdir().unwrap();
        let founder = fresh_key();
        let team = fresh_key();
        let bootstrap = issue_bootstrap(&founder, "proj", 100, None).unwrap();
        let member =
            issue_child(&founder, &bootstrap, &pub_bytes(&team), Role::Member, 150, None).unwrap();
        let pid = project_id_for(&pub_bytes(&founder), "proj");
        let push = PushChain {
            version: PUSH_CHAIN_VERSION,
            chain: vec![bootstrap, member.clone()],
            delegations: Vec::new(),
        };

        // Drop a revocation envelope for the member token.
        let revoke = super::super::envelopes::RevokeEnvelope::build(
            &founder,
            &pid,
            &member.id(),
            200,
            "leaked",
        );
        super::super::envelopes::save_revoke(dir.path(), &revoke).unwrap();

        let err = verify_chain_for_push(&push, &pid, dir.path(), 250).unwrap_err();
        assert!(err.to_string().contains("revoked"));
    }

    // ADR-019 slice 4: PushChain wire format with delegations.

    #[test]
    fn push_chain_header_roundtrip_with_delegations() {
        let founder = fresh_key();
        let new_founder = fresh_key();
        let bootstrap = issue_bootstrap(&founder, "proj", 100, None).unwrap();
        let pid = project_id_for(&pub_bytes(&founder), "proj");
        let chain = TokenChain {
            version: super::super::project_state::PROJECT_STATE_VERSION,
            chain: vec![bootstrap],
        };
        let delegation = crate::swarm::anchor::envelopes::DelegationEnvelope::build(
            &founder,
            &pid,
            &pub_bytes(&new_founder),
            200,
        );
        let header = encode_chain_header(&chain, std::slice::from_ref(&delegation)).unwrap();
        let parsed = decode_chain_header(&header).unwrap();
        assert_eq!(parsed.chain.len(), 1);
        assert_eq!(parsed.delegations.len(), 1);
        assert_eq!(parsed.delegations[0], delegation);
    }

    #[test]
    fn verify_chain_for_push_accepts_post_rekey_chain_via_delegations() {
        // Receive-side accepts a post-rekey founder's leaf when the
        // chain header carries the corresponding delegation chain.
        let dir = tempfile::tempdir().unwrap();
        let genesis = fresh_key();
        let new_founder = fresh_key();
        let new_admin_subject = fresh_key();
        let bootstrap = issue_bootstrap(&genesis, "proj", 100, None).unwrap();
        let pid = project_id_for(&pub_bytes(&genesis), "proj");
        let delegation = crate::swarm::anchor::envelopes::DelegationEnvelope::build(
            &genesis,
            &pid,
            &pub_bytes(&new_founder),
            200,
        );
        // Mint a fresh admin under NEW chained off bootstrap.
        let body = crate::swarm::anchor::tokens::TokenBody {
            project_id: pid,
            handle: "proj".to_string(),
            subject_puddle_pubkey: pub_bytes(&new_admin_subject),
            role: crate::swarm::anchor::tokens::Role::Admin,
            issued_at: 250,
            expires_at: None,
            signer_puddle_pubkey: pub_bytes(&new_founder),
            parent_token_id: Some(bootstrap.id()),
        };
        let admin = crate::swarm::anchor::tokens::Token::issue(&new_founder, body).unwrap();
        let push = PushChain {
            version: PUSH_CHAIN_VERSION,
            chain: vec![bootstrap, admin],
            delegations: vec![delegation],
        };
        verify_chain_for_push(&push, &pid, dir.path(), 300)
            .expect("post-rekey chain with delegation should verify");
    }

    // --- set_head_to_default_branch ---

    #[test]
    fn set_head_repoints_phantom_head_to_main() {
        if !git_available() {
            return;
        }
        let dir = tempfile::tempdir().unwrap();
        let repo = seed_repo(dir.path(), "p"); // refs/heads/main only
        // Make HEAD point at a branch that doesn't exist (the bug shape).
        run_git(&repo, &["symbolic-ref", "HEAD", "refs/heads/__phantom__"]);
        set_head_to_default_branch(&repo).unwrap();
        assert_eq!(read_head(&repo), "refs/heads/main");
    }

    #[test]
    fn set_head_is_noop_when_already_valid() {
        if !git_available() {
            return;
        }
        let dir = tempfile::tempdir().unwrap();
        let repo = seed_repo(dir.path(), "v");
        run_git(&repo, &["symbolic-ref", "HEAD", "refs/heads/main"]);
        set_head_to_default_branch(&repo).unwrap();
        assert_eq!(read_head(&repo), "refs/heads/main");
    }

    #[test]
    fn set_head_prefers_master_when_no_main() {
        if !git_available() {
            return;
        }
        let dir = tempfile::tempdir().unwrap();
        let repo = seed_repo_with_branches(dir.path(), "m", &["develop", "master"]);
        run_git(&repo, &["symbolic-ref", "HEAD", "refs/heads/__phantom__"]);
        set_head_to_default_branch(&repo).unwrap();
        assert_eq!(read_head(&repo), "refs/heads/master");
    }

    #[test]
    fn set_head_picks_first_branch_when_no_main_or_master() {
        if !git_available() {
            return;
        }
        let dir = tempfile::tempdir().unwrap();
        let repo = seed_repo_with_branches(dir.path(), "f", &["zeta", "alpha"]);
        run_git(&repo, &["symbolic-ref", "HEAD", "refs/heads/__phantom__"]);
        set_head_to_default_branch(&repo).unwrap();
        // for-each-ref sorts by name → alphabetically first.
        assert_eq!(read_head(&repo), "refs/heads/alpha");
    }

    #[test]
    fn set_head_is_noop_on_empty_bare_repo() {
        if !git_available() {
            return;
        }
        let dir = tempfile::tempdir().unwrap();
        let repo = dir.path().join("empty.git");
        ensure_bare_repo(&repo).unwrap();
        let before = read_head(&repo);
        set_head_to_default_branch(&repo).unwrap(); // must not error
        assert_eq!(read_head(&repo), before); // and must not change HEAD
    }

    #[test]
    fn apply_bundle_reconciles_phantom_head() {
        if !git_available() {
            return;
        }
        let dir = tempfile::tempdir().unwrap();
        let src = seed_repo(dir.path(), "src"); // refs/heads/main only
        let bundle = dir.path().join("b.bundle");
        build_bundle(&src, &bundle).unwrap();
        let dst = dir.path().join("dst.git");
        ensure_bare_repo(&dst).unwrap();
        run_git(&dst, &["symbolic-ref", "HEAD", "refs/heads/__phantom__"]);
        apply_bundle(&dst, &bundle).unwrap();
        assert_eq!(read_head(&dst), "refs/heads/main");
    }
}
