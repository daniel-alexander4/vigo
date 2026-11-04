// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: Apache-2.0

//! Receiver-side record of each project's founder-delegation chain
//! (ADR-019), root-owned and grow-only.
//!
//! `verify_chain` derives each founder's authority window from the
//! delegation chain it is handed: genesis holds `[MIN, first delegation)`,
//! each successor holds `[its delegation, next delegation)`. When the
//! chain came from the request (the `X-Vigo-Chain` header, a gossiped
//! revoke, or a user-writable `delegations.json`), a deposed founder key
//! could present `delegations: []` and get the genesis window back as
//! `[MIN, MAX)` — minting admin tokens dated today.
//!
//! This store closes that: every chain-verifying site on this envoy
//! verifies against the **longest chain it has ever verified** for the
//! project, kept at `<gitback_base>/delegations/<project_id_hex>.json`
//! (root-owned, 0600). A presented chain that is a prefix of the known
//! one is verified against the known one (the sender simply has not
//! learned of a later rekey); one that extends it is verified as
//! presented and, on success, becomes the new known chain; one that
//! diverges from it (a deposed key signing a different successor) is
//! refused.
//!
//! What this does not close: a deposed founder can still sign tokens
//! back-dated into its own historical window, because `issued_at` is
//! self-asserted. That needs a commitment to the set of tokens a founder
//! issued before it delegated, which is a protocol change.

use std::path::{Path, PathBuf};
use std::sync::Mutex;

use anyhow::{anyhow, bail, Context, Result};

use crate::swarm::anchor::envelopes::{DelegationEnvelope, DELEGATION_MAX_LEN};
use crate::swarm::anchor::tokens::{verify_chain, Token, ID_LEN};

/// Serializes read-modify-write of the store within this process (the
/// peer server, the reactor and the helper daemon all run in the agent).
static STORE_LOCK: Mutex<()> = Mutex::new(());

/// `<gitback_base>/delegations/<project_id_hex>.json`.
pub fn store_path(gitback_base: &Path, project_id: &[u8; ID_LEN]) -> PathBuf {
    gitback_base
        .join("delegations")
        .join(format!("{}.json", hex::encode(project_id)))
}

/// The known chain for `project_id`; empty when none was ever recorded.
/// An unreadable or corrupt store is an error, never an empty chain —
/// treating it as empty would hand the genesis window back.
pub fn load_known(gitback_base: &Path, project_id: &[u8; ID_LEN]) -> Result<Vec<DelegationEnvelope>> {
    let path = store_path(gitback_base, project_id);
    match std::fs::read(&path) {
        Ok(b) => serde_json::from_slice(&b).with_context(|| format!("parse {}", path.display())),
        Err(e) if e.kind() == std::io::ErrorKind::NotFound => Ok(Vec::new()),
        Err(e) => Err(e).with_context(|| format!("read {}", path.display())),
    }
}

/// The chain to verify against, given what this envoy knows and what the
/// request presented: the longer of the two when one is a prefix of the
/// other, an error when they diverge.
pub fn reconcile(
    known: &[DelegationEnvelope],
    presented: &[DelegationEnvelope],
) -> Result<Vec<DelegationEnvelope>> {
    let (short, long) = if presented.len() <= known.len() {
        (presented, known)
    } else {
        (known, presented)
    };
    if long[..short.len()] != *short {
        bail!(
            "presented founder-delegation chain diverges from the {}-link chain this envoy \
             has already verified for the project",
            known.len()
        );
    }
    Ok(long.to_vec())
}

fn record(gitback_base: &Path, project_id: &[u8; ID_LEN], chain: &[DelegationEnvelope]) -> Result<()> {
    if chain.len() > DELEGATION_MAX_LEN {
        bail!("delegation chain of length {} exceeds cap {}", chain.len(), DELEGATION_MAX_LEN);
    }
    let path = store_path(gitback_base, project_id);
    let dir = path.parent().ok_or_else(|| anyhow!("store path has no parent"))?;
    std::fs::create_dir_all(dir).with_context(|| format!("create {}", dir.display()))?;
    let json = serde_json::to_vec_pretty(chain).context("encode delegation chain")?;
    let tmp = dir.join(format!(".{}.tmp", hex::encode(project_id)));
    {
        use std::io::Write;
        let mut f = crate::platform::safe_path::create_new_file(&tmp, 0o600)
            .or_else(|_| {
                let _ = std::fs::remove_file(&tmp);
                crate::platform::safe_path::create_new_file(&tmp, 0o600)
            })
            .with_context(|| format!("create {}", tmp.display()))?;
        f.write_all(&json)?;
        f.sync_all()?;
    }
    std::fs::rename(&tmp, &path).with_context(|| format!("rename to {}", path.display()))?;
    Ok(())
}

/// Verify `chain` for `project_id` against the founder-delegation chain
/// this envoy knows, reconciled with the one the caller `presented`
/// (see the module docs). On success, a presented extension is recorded
/// so every later verification on this envoy holds it. The single
/// chain-verifying entry point for everything that crosses a trust
/// boundary: push, bundle GET, gossiped revokes and the helper daemon.
pub fn verify_chain_known(
    gitback_base: &Path,
    project_id: &[u8; ID_LEN],
    founder: &[u8; ID_LEN],
    presented: &[DelegationEnvelope],
    chain: &[Token],
    now: i64,
) -> Result<()> {
    let _g = STORE_LOCK.lock().unwrap_or_else(|p| p.into_inner());
    let known = load_known(gitback_base, project_id)?;
    let effective = reconcile(&known, presented)?;
    verify_chain(project_id, founder, &effective, chain, now)
        .map_err(|e| anyhow!("chain verification: {}", e))?;
    if effective.len() > known.len() {
        record(gitback_base, project_id, &effective)?;
    }
    Ok(())
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::swarm::anchor::tokens::{issue_bootstrap, project_id_for, Role, TokenBody};
    use ed25519_dalek::SigningKey;
    use rand::rngs::OsRng;

    fn key() -> SigningKey {
        SigningKey::generate(&mut OsRng)
    }
    fn pb(k: &SigningKey) -> [u8; ID_LEN] {
        k.verifying_key().to_bytes()
    }
    /// chain[1] under the bootstrap, signed by whichever founder key the
    /// test claims (the ADR-019 founder-window rule decides validity).
    fn child(signer: &SigningKey, bootstrap: &Token, subject: &SigningKey, role: Role, at: i64) -> Token {
        Token::issue(signer, TokenBody {
            project_id: bootstrap.body.project_id,
            handle: "proj".into(),
            subject_puddle_pubkey: pb(subject),
            role,
            issued_at: at,
            expires_at: None,
            signer_puddle_pubkey: pb(signer),
            parent_token_id: Some(bootstrap.id()),
        })
        .unwrap()
    }

    /// The ADR-019 deposed-founder attack: genesis rekeyed to `new`, and
    /// this envoy saw the delegation once. The retired genesis key then
    /// mints an admin token dated after the rekey and presents it with
    /// `delegations: []` — it must not get the genesis window back.
    #[test]
    fn deposed_founder_cannot_drop_known_delegations() {
        let base = tempfile::tempdir().unwrap();
        let genesis = key();
        let new = key();
        let member = key();
        let pid = project_id_for(&pb(&genesis), "proj");
        let bootstrap = issue_bootstrap(&genesis, "proj", 100, None).unwrap();
        let d0 = DelegationEnvelope::build(&genesis, &pid, &pb(&new), 200);

        // A legitimate push carrying the delegation teaches the store.
        let legit = child(&new, &bootstrap, &member, Role::Member, 300);
        verify_chain_known(base.path(), &pid, &pb(&genesis), &[d0.clone()], &[bootstrap.clone(), legit.clone()], 400)
            .expect("legit post-rekey chain verifies");
        assert_eq!(load_known(base.path(), &pid).unwrap(), vec![d0.clone()]);

        let forged = child(&genesis, &bootstrap, &key(), Role::Admin, 500);
        let err = verify_chain_known(base.path(), &pid, &pb(&genesis), &[], &[bootstrap.clone(), forged], 600)
            .unwrap_err();
        assert!(format!("{err:#}").contains("chain verification"), "{err:#}");

        // A sender that has not learned of the rekey still verifies a
        // token issued inside the window it holds.
        verify_chain_known(base.path(), &pid, &pb(&genesis), &[], &[bootstrap, legit], 700)
            .expect("prefix-presented chain verifies against the known one");
    }

    /// A retired key signing a different successor (a fork) is refused once
    /// the real chain is known.
    #[test]
    fn divergent_delegation_chain_is_refused() {
        let base = tempfile::tempdir().unwrap();
        let genesis = key();
        let new = key();
        let rogue = key();
        let pid = project_id_for(&pb(&genesis), "proj");
        let bootstrap = issue_bootstrap(&genesis, "proj", 100, None).unwrap();
        let d0 = DelegationEnvelope::build(&genesis, &pid, &pb(&new), 200);
        verify_chain_known(base.path(), &pid, &pb(&genesis), &[d0], &[bootstrap.clone()], 300).unwrap();

        let fork = DelegationEnvelope::build(&genesis, &pid, &pb(&rogue), 250);
        let admin = child(&rogue, &bootstrap, &key(), Role::Admin, 260);
        let err = verify_chain_known(base.path(), &pid, &pb(&genesis), &[fork], &[bootstrap, admin], 300)
            .unwrap_err();
        assert!(format!("{err:#}").contains("diverges"), "{err:#}");
    }

    #[test]
    fn reconcile_takes_the_longer_consistent_chain() {
        let genesis = key();
        let a = key();
        let b = key();
        let pid = project_id_for(&pb(&genesis), "proj");
        let d0 = DelegationEnvelope::build(&genesis, &pid, &pb(&a), 200);
        let d1 = DelegationEnvelope::build(&a, &pid, &pb(&b), 300);
        assert_eq!(reconcile(&[d0.clone()], &[]).unwrap(), vec![d0.clone()]);
        assert_eq!(reconcile(&[d0.clone()], &[d0.clone(), d1.clone()]).unwrap().len(), 2);
        assert_eq!(reconcile(&[d0.clone(), d1.clone()], &[d0.clone()]).unwrap().len(), 2);
        assert!(reconcile(&[d1], &[d0]).is_err());
    }

    #[test]
    fn corrupt_store_fails_closed() {
        let base = tempfile::tempdir().unwrap();
        let pid = [7u8; ID_LEN];
        let p = store_path(base.path(), &pid);
        std::fs::create_dir_all(p.parent().unwrap()).unwrap();
        std::fs::write(&p, b"not json").unwrap();
        assert!(load_known(base.path(), &pid).is_err());
    }
}
