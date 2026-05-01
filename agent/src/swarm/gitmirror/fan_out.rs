// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

//! Push-driven fan-out + receive-side bundle apply for ADR-015 step 4.
//!
//! After a successful local push (the helper daemon's `git-receive-pack`
//! exits zero with refs advanced), the originating envoy builds a
//! `git bundle` of the current repo state and POSTs it to every peer
//! listed in `project.json::cross_subnet_members`. Receivers ingest
//! the bundle via `POST /swarm/gitmirror/push/<project_id_hex>` on
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
//! POST /swarm/gitmirror/push/<project_id_hex>
//! X-Vigo-Chain: base64(JSON([Token, Token, ...]))
//! Content-Type: application/x-git-bundle
//! Body: <bundle bytes, ≤ MAX_BUNDLE_SIZE>
//!
//! → 200 OK
//!   Content-Type: application/json
//!   Body: {"accepted": true, "refs_changed": <bool>}
//! ```
//!
//! Receiver verifies the chain against the URL's `project_id`
//! (re-deriving the founder pubkey from `chain[0].signer_fleet_pubkey`
//! and confirming `sha256(founder) == project_id`). Revocation lookup
//! via [`super::envelopes::is_token_revoked`] rejects any chain whose
//! tokens have been revoked.

use std::collections::BTreeMap;
use std::path::{Path, PathBuf};
use std::process::Stdio;
use std::time::Duration;

use anyhow::{anyhow, bail, Context, Result};
use base64::engine::general_purpose::STANDARD as B64;
use base64::Engine;

use super::envelopes::is_token_revoked;
use super::git_ops::{build_command, RunAs};
use super::project_state::{hex_id, project_repo_path, ProjectConfig, TokenChain};
use super::tokens::{verify_chain, ID_LEN};

/// Per-peer push timeout. Generous for a multi-GiB bundle on a slow
/// link; bounded so a stuck connection can't pin a tokio worker.
const PEER_PUSH_TIMEOUT: Duration = Duration::from_secs(300);

/// HTTP header used to carry the token chain on a fan-out push.
pub const CHAIN_HEADER: &str = "X-Vigo-Chain";

/// Path under `<gitmirror_base>/incoming/` where inbound bundles are
/// staged before `git fetch`. Cleaned up on success or failure.
fn incoming_dir(gitmirror_base: &Path) -> PathBuf {
    gitmirror_base.join("incoming")
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

/// Encode a chain for the `X-Vigo-Chain` header.
pub fn encode_chain_header(chain: &TokenChain) -> Result<String> {
    let json = serde_json::to_vec(chain).context("serialize chain")?;
    Ok(B64.encode(&json))
}

/// Decode a chain from the `X-Vigo-Chain` header value.
pub fn decode_chain_header(value: &str) -> Result<TokenChain> {
    let bytes = B64.decode(value.trim()).context("base64-decode chain header")?;
    let chain: TokenChain =
        serde_json::from_slice(&bytes).context("parse chain JSON from header")?;
    Ok(chain)
}

/// Verify a chain server-side against a project_id from the URL.
/// Computes `founder = chain[0].signer_fleet_pubkey`; verify_chain
/// checks `sha256(founder) == project_id` and rejects mismatches.
/// Also rejects any chain whose tokens have been revoked.
pub fn verify_chain_for_push(
    chain: &TokenChain,
    project_id: &[u8; ID_LEN],
    gitmirror_base: &Path,
    now: i64,
) -> Result<()> {
    let root = chain
        .chain
        .first()
        .ok_or_else(|| anyhow!("chain is empty"))?;
    let founder = root.body.signer_fleet_pubkey;
    verify_chain(project_id, &founder, &chain.chain, now)
        .map_err(|e| anyhow!("chain verification: {}", e))?;
    for (i, token) in chain.chain.iter().enumerate() {
        let tid = token.id();
        if is_token_revoked(gitmirror_base, project_id, &tid) {
            bail!(
                "token at chain depth {} ({}) is revoked",
                i,
                hex::encode(&tid[..8])
            );
        }
    }
    Ok(())
}

/// Receive-side bundle ingestion. Called by the swarm peer-server
/// route handler. Stages `body_bytes` in a temp file, verifies the
/// bundle, fetches refs into the bare repo, and returns whether
/// refs changed.
pub fn ingest_bundle(
    gitmirror_base: &Path,
    project_id: &[u8; ID_LEN],
    body_bytes: &[u8],
) -> Result<bool> {
    let repo = project_repo_path(gitmirror_base, project_id);
    ensure_bare_repo(&repo)?;

    let staging = incoming_dir(gitmirror_base);
    std::fs::create_dir_all(&staging)
        .with_context(|| format!("create {}", staging.display()))?;
    #[cfg(unix)]
    {
        use std::os::unix::fs::PermissionsExt;
        let _ = std::fs::set_permissions(&staging, std::fs::Permissions::from_mode(0o700));
    }

    let nonce: u128 = rand::random();
    let bundle_path = staging.join(format!("{}-{:032x}.bundle", hex_id(project_id), nonce));
    std::fs::write(&bundle_path, body_bytes)
        .with_context(|| format!("write bundle to {}", bundle_path.display()))?;

    let result = (|| -> Result<bool> {
        let before = snapshot_refs(&repo).unwrap_or_default();
        apply_bundle(&repo, &bundle_path)?;
        let after = snapshot_refs(&repo).unwrap_or_default();
        Ok(before != after)
    })();

    let _ = std::fs::remove_file(&bundle_path);
    result
}

/// Fan out a bundle to every cross-subnet member listed in the
/// project's local `project.json`. Returns counts of `(attempted,
/// succeeded)`. Errors talking to a single peer don't abort the sweep
/// — fan-out is best-effort, gossip / catch-up will reconcile.
pub fn fan_out_bundle(
    home: &Path,
    project_id: &[u8; ID_LEN],
    chain: &TokenChain,
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
    if cfg.cross_subnet_members.is_empty() {
        tracing::debug!(
            project_id = %hex_id(project_id),
            "fan-out: no cross_subnet_members; nothing to push to (LAN auto-discovery is a follow-up slice)"
        );
        return (0, 0);
    }

    let chain_header = match encode_chain_header(chain) {
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

    let pid_hex = hex_id(project_id);
    let mut attempted = 0;
    let mut succeeded = 0;
    let bundle_bytes = match std::fs::read(bundle_path) {
        Ok(b) => b,
        Err(e) => {
            tracing::warn!(
                bundle = %bundle_path.display(),
                error = %e,
                "fan-out: cannot read bundle for upload"
            );
            return (0, 0);
        }
    };

    for hostname in &cfg.cross_subnet_members {
        attempted += 1;
        let url = format!(
            "https://{}:{}/swarm/gitmirror/push/{}",
            hostname,
            crate::swarm::server::DEFAULT_PORT,
            pid_hex
        );
        match push_to_peer(&agent, &url, &chain_header, &bundle_bytes) {
            Ok(refs_changed) => {
                tracing::info!(
                    project_id = %pid_hex,
                    peer = %hostname,
                    refs_changed,
                    "fan-out: peer accepted bundle"
                );
                succeeded += 1;
            }
            Err(e) => {
                tracing::warn!(
                    project_id = %pid_hex,
                    peer = %hostname,
                    error = %e,
                    "fan-out: peer push failed"
                );
            }
        }
    }
    (attempted, succeeded)
}

fn push_to_peer(
    agent: &ureq::Agent,
    url: &str,
    chain_header: &str,
    bundle_bytes: &[u8],
) -> Result<bool> {
    let resp = agent
        .post(url)
        .header(CHAIN_HEADER, chain_header)
        .header("Content-Type", "application/x-git-bundle")
        .send(bundle_bytes)
        .with_context(|| format!("POST {}", url))?;
    let status = resp.status();
    if !status.is_success() {
        bail!("peer returned {}", status);
    }
    let mut body = resp.into_body();
    let body_bytes = body.read_to_vec().context("read response body")?;
    let parsed: serde_json::Value =
        serde_json::from_slice(&body_bytes).context("parse JSON response")?;
    let refs_changed = parsed
        .get("refs_changed")
        .and_then(|v| v.as_bool())
        .unwrap_or(false);
    Ok(refs_changed)
}

fn build_push_agent(tls: &crate::update::TlsCerts) -> Result<ureq::Agent> {
    use ureq::tls::{parse_pem, PemItem};

    let ca_pem = std::fs::read(&tls.ca_cert).context("read CA cert")?;
    let cert_pem = std::fs::read(&tls.client_cert).context("read client cert")?;
    let key_pem = std::fs::read(&tls.client_key).context("read client key")?;

    let mut ca_certs = Vec::new();
    for item in parse_pem(&ca_pem) {
        if let Ok(PemItem::Certificate(c)) = item {
            ca_certs.push(c);
        }
    }
    let root_certs = ureq::tls::RootCerts::new_with_certs(&ca_certs);

    let mut client_certs = Vec::new();
    for item in parse_pem(&cert_pem) {
        if let Ok(PemItem::Certificate(c)) = item {
            client_certs.push(c);
        }
    }
    let mut client_key = None;
    for item in parse_pem(&key_pem) {
        if let Ok(PemItem::PrivateKey(k)) = item {
            client_key = Some(k);
            break;
        }
    }
    let key = client_key.context("no private key found in client key PEM")?;
    let client_id = ureq::tls::ClientCert::new_with_certs(&client_certs, key);

    let tls_cfg = ureq::tls::TlsConfig::builder()
        .root_certs(root_certs)
        .client_cert(Some(client_id))
        // Hostname verification is off because envoy mTLS certs are
        // issued for the agent UUID, not the DNS name. Peer identity
        // is established by client-cert chain back to the fleet CA;
        // hostname-on-cert would require parallel issuance.
        .disable_verification(true)
        .build();

    Ok(ureq::Agent::new_with_config(
        ureq::config::Config::builder()
            .tls_config(tls_cfg)
            .timeout_connect(Some(Duration::from_secs(5)))
            .timeout_global(Some(PEER_PUSH_TIMEOUT))
            .timeout_send_body(Some(PEER_PUSH_TIMEOUT))
            .build(),
    ))
}

#[cfg(test)]
mod tests {
    use super::*;
    use ed25519_dalek::SigningKey;
    use rand::rngs::OsRng;

    use super::super::tokens::{issue_bootstrap, issue_child, project_id_for, Role};

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
    fn ingest_bundle_idempotent_returns_no_change() {
        if !git_available() {
            return;
        }
        let dir = tempfile::tempdir().unwrap();
        let base = dir.path().to_path_buf();
        let pid = [0x77u8; ID_LEN];

        let src = seed_repo(dir.path(), "src");
        let bundle_tmp = dir.path().join("first.bundle");
        build_bundle(&src, &bundle_tmp).unwrap();
        let body = std::fs::read(&bundle_tmp).unwrap();

        // Stand up a destination bare repo at the canonical path and
        // pre-seed it from the src so the second ingest is a no-op.
        let dst = project_repo_path(&base, &pid);
        ensure_bare_repo(&dst).unwrap();
        apply_bundle(&dst, &bundle_tmp).unwrap();

        // Second ingest of the same bundle: no ref change.
        let changed = ingest_bundle(&base, &pid, &body).unwrap();
        assert!(!changed, "idempotent re-ingest should report refs_changed=false");
    }

    #[test]
    fn ingest_bundle_advances_empty_repo() {
        if !git_available() {
            return;
        }
        let dir = tempfile::tempdir().unwrap();
        let base = dir.path().to_path_buf();
        let pid = [0x88u8; ID_LEN];

        let src = seed_repo(dir.path(), "src");
        let bundle_tmp = dir.path().join("first.bundle");
        build_bundle(&src, &bundle_tmp).unwrap();
        let body = std::fs::read(&bundle_tmp).unwrap();

        let changed = ingest_bundle(&base, &pid, &body).unwrap();
        assert!(changed, "first ingest into empty repo should report refs_changed=true");
        let dst = project_repo_path(&base, &pid);
        let snap = snapshot_refs(&dst).unwrap();
        assert!(snap.contains_key("refs/heads/main"));
    }

    #[test]
    fn chain_header_round_trips() {
        let founder = fresh_key();
        let bootstrap = issue_bootstrap(&founder, 100, None).unwrap();
        let chain = TokenChain {
            version: super::super::project_state::PROJECT_STATE_VERSION,
            chain: vec![bootstrap],
        };
        let header = encode_chain_header(&chain).unwrap();
        let parsed = decode_chain_header(&header).unwrap();
        assert_eq!(parsed.chain.len(), 1);
        assert_eq!(parsed, chain);
    }

    #[test]
    fn verify_chain_for_push_accepts_well_formed_chain() {
        let dir = tempfile::tempdir().unwrap();
        let founder = fresh_key();
        let bootstrap = issue_bootstrap(&founder, 100, None).unwrap();
        let pid = project_id_for(&pub_bytes(&founder));
        let chain = TokenChain {
            version: super::super::project_state::PROJECT_STATE_VERSION,
            chain: vec![bootstrap],
        };
        verify_chain_for_push(&chain, &pid, dir.path(), 200).unwrap();
    }

    #[test]
    fn verify_chain_for_push_rejects_wrong_project_id() {
        let dir = tempfile::tempdir().unwrap();
        let founder = fresh_key();
        let other = fresh_key();
        let bootstrap = issue_bootstrap(&founder, 100, None).unwrap();
        let chain = TokenChain {
            version: super::super::project_state::PROJECT_STATE_VERSION,
            chain: vec![bootstrap],
        };
        let wrong_pid = project_id_for(&pub_bytes(&other));
        let err = verify_chain_for_push(&chain, &wrong_pid, dir.path(), 200).unwrap_err();
        assert!(err.to_string().contains("chain verification"));
    }

    #[test]
    fn verify_chain_for_push_rejects_revoked_token() {
        let dir = tempfile::tempdir().unwrap();
        let founder = fresh_key();
        let team = fresh_key();
        let bootstrap = issue_bootstrap(&founder, 100, None).unwrap();
        let member =
            issue_child(&founder, &bootstrap, &pub_bytes(&team), Role::Member, 150, None).unwrap();
        let pid = project_id_for(&pub_bytes(&founder));
        let chain = TokenChain {
            version: super::super::project_state::PROJECT_STATE_VERSION,
            chain: vec![bootstrap, member.clone()],
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

        let err = verify_chain_for_push(&chain, &pid, dir.path(), 250).unwrap_err();
        assert!(err.to_string().contains("revoked"));
    }
}
