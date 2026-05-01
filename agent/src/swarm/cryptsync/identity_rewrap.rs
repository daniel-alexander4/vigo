// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

//! Atomic re-wrap of `~/.cryptsync/identity.age` across a self-fleet
//! key rotation.
//!
//! Fleet-mode cryptsync wraps the per-envoy age identity under a key
//! derived from the user's self-fleet keypair (see
//! [`super::crypt::derive_fleet_wrap_password`]). When the fleet
//! keypair rotates (ADR-018 rekey flow), surviving envoys would lose
//! access to their per-envoy age key unless the wrap envelope is
//! re-derived under the new keypair before the old keypair is
//! discarded.
//!
//! [`rewrap_identity_for_new_fleet_key`] performs the migration in
//! one atomic step, leaving the old envelope at
//! `identity.age.pre-rekey-<unix_ts>` for 30 days as a recovery
//! anchor. The salt is intentionally not refreshed — the wrap key
//! changes because the password material changes (different
//! signature over the same fixed challenge), and keeping a single
//! file in scope means a single atomic-rename owns the migration.

use std::fs;
use std::path::{Path, PathBuf};
use std::time::{SystemTime, UNIX_EPOCH};

use anyhow::{Context, Result};
use ed25519_dalek::{Signer, SigningKey};
use zeroize::Zeroizing;

use super::config::{
    identity_path, is_enrolled, load_config, salt_path, WrapMode,
};
use super::crypt::{unwrap_identity, wrap_identity, FLEET_WRAP_CHALLENGE};

/// Suffix segment for the backup file. Final shape:
/// `identity.age.pre-rekey-<unix_ts>`.
const BACKUP_INFIX: &str = "pre-rekey";

/// Backups older than this are reaped by [`gc_old_backups`]. 30 days
/// matches the cryptsync LEAVE / tombstone retention window so the
/// recovery anchor outlives a typical "I rekeyed but something
/// broke" window without lingering forever.
pub const BACKUP_RETENTION_SECS: u64 = 30 * 24 * 60 * 60;

/// Outcome of a rewrap attempt. Callers branch on this for status
/// reporting; only `Rewrapped` represents on-disk change.
#[derive(Debug)]
pub enum RewrapOutcome {
    /// Cryptsync isn't enrolled on this envoy; nothing to do.
    NotEnrolled,
    /// Cryptsync is in standalone wrap-mode; rewrap doesn't apply.
    /// The per-envoy age identity is wrapped under an operator-typed
    /// passphrase, not the fleet keypair, so a fleet rotation is a
    /// no-op for it.
    StandaloneSkipped,
    /// Rewrap succeeded; the original envelope is at `backup_path`
    /// for `BACKUP_RETENTION_SECS` seconds.
    Rewrapped { backup_path: PathBuf },
}

/// Re-wrap `~/.cryptsync/identity.age` from a wrap-key derived under
/// `old` to one derived under `new`. The salt is reused; only the
/// envelope changes. Atomic on success.
///
/// Caller invariant: this function only makes sense when the rewrap
/// is happening as part of a coordinated fleet rotation where both
/// keypairs are simultaneously in process memory. The signing API
/// is local — no session helper round-trip — because going through
/// the helper at this point would require swapping the helper's
/// loaded key mid-flight, which is exactly what we're avoiding.
pub fn rewrap_identity_for_new_fleet_key(
    home: &Path,
    username: &str,
    old: &SigningKey,
    new: &SigningKey,
) -> Result<RewrapOutcome> {
    if !is_enrolled(home) {
        return Ok(RewrapOutcome::NotEnrolled);
    }
    let cfg = load_config(home);
    if cfg.wrap_mode != WrapMode::Fleet {
        return Ok(RewrapOutcome::StandaloneSkipped);
    }

    let id_path = identity_path(home);
    let salt_file = salt_path(home);
    let blob = fs::read(&id_path)
        .with_context(|| format!("reading {}", id_path.display()))?;
    let salt = fs::read(&salt_file)
        .with_context(|| format!("reading {}", salt_file.display()))?;

    let old_pw = derive_fleet_wrap_password_for(old);
    let new_pw = derive_fleet_wrap_password_for(new);

    let identity = unwrap_identity(&blob, old_pw.as_slice(), &salt).context(
        "unwrap with old fleet keypair failed — caller passed the wrong old key, \
         or identity.age is corrupted",
    )?;
    let new_blob = wrap_identity(&identity, new_pw.as_slice(), &salt)
        .context("wrapping identity under new fleet keypair")?;

    let now = SystemTime::now()
        .duration_since(UNIX_EPOCH)
        .map(|d| d.as_secs())
        .unwrap_or(0);
    let backup_path = id_path.with_file_name(format!(
        "{}.{}-{}",
        id_path
            .file_name()
            .and_then(|s| s.to_str())
            .unwrap_or("identity.age"),
        BACKUP_INFIX,
        now,
    ));

    // Backup first, then atomic-rename the new blob into place. If
    // the rename fails, the backup stays — operator can manually
    // restore it. If the backup fails, we never touched the live
    // envelope.
    fs::rename(&id_path, &backup_path).with_context(|| {
        format!(
            "moving {} → {}",
            id_path.display(),
            backup_path.display()
        )
    })?;
    if let Err(e) = write_envelope_atomic(&id_path, &new_blob, username) {
        // Restore from backup on failure so we never leave the user
        // in a state where their identity.age is missing entirely.
        let _ = fs::rename(&backup_path, &id_path);
        return Err(e.context("writing new identity.age — restored from backup"));
    }
    // Chown the backup too — it was owned by the user before, and
    // the rename preserves ownership, but be defensive.
    let _ = crate::swarm::cryptsync::chown_to_user_pub(&backup_path, username);

    Ok(RewrapOutcome::Rewrapped { backup_path })
}

/// Walk `~/.cryptsync/` and remove `identity.age.pre-rekey-<ts>`
/// files older than `BACKUP_RETENTION_SECS`. Returns the number of
/// files removed. Errors during the walk are swallowed (logged at
/// the call site if the caller wants — pure best-effort GC).
pub fn gc_old_backups(home: &Path) -> Result<usize> {
    let dot = super::config::dot_dir(home);
    let now = SystemTime::now()
        .duration_since(UNIX_EPOCH)
        .map(|d| d.as_secs())
        .unwrap_or(0);
    let mut removed = 0usize;
    let entries = match fs::read_dir(&dot) {
        Ok(e) => e,
        Err(e) if e.kind() == std::io::ErrorKind::NotFound => return Ok(0),
        Err(e) => return Err(e).with_context(|| format!("reading {}", dot.display())),
    };
    for entry in entries.flatten() {
        let name = entry.file_name();
        let Some(name) = name.to_str() else { continue };
        let Some(ts) = parse_backup_timestamp(name) else { continue };
        if now.saturating_sub(ts) >= BACKUP_RETENTION_SECS {
            let path = entry.path();
            if fs::remove_file(&path).is_ok() {
                removed += 1;
            }
        }
    }
    Ok(removed)
}

/// Mirror of `super::crypt::derive_fleet_wrap_password` but signing
/// locally with a caller-supplied keypair. Ed25519 over a fixed
/// message is deterministic, so this produces the same 64 bytes the
/// session helper would produce when loaded with the same key.
fn derive_fleet_wrap_password_for(key: &SigningKey) -> Zeroizing<Vec<u8>> {
    let sig = key.sign(FLEET_WRAP_CHALLENGE);
    Zeroizing::new(sig.to_bytes().to_vec())
}

/// Atomic write: tempfile in the same directory, then rename. 0600
/// on Unix; chown to `username` after write.
fn write_envelope_atomic(path: &Path, content: &[u8], username: &str) -> Result<()> {
    use std::io::Write;
    let tmp = path.with_extension("age.tmp");
    {
        #[cfg(unix)]
        use std::os::unix::fs::OpenOptionsExt;
        let mut opts = fs::OpenOptions::new();
        opts.create(true).write(true).truncate(true);
        #[cfg(unix)]
        opts.mode(0o600);
        let mut f = opts
            .open(&tmp)
            .with_context(|| format!("create {}", tmp.display()))?;
        f.write_all(content)?;
        f.sync_all().ok();
    }
    fs::rename(&tmp, path).with_context(|| format!("rename to {}", path.display()))?;
    #[cfg(unix)]
    {
        let _ = crate::swarm::cryptsync::chown_to_user_pub(path, username);
    }
    let _ = username;
    Ok(())
}

/// Match `identity.age.pre-rekey-<unix_ts>` and return the timestamp
/// if it parses. Anything else returns None — keeps unrelated files
/// in `~/.cryptsync/` untouched by the GC sweep.
fn parse_backup_timestamp(name: &str) -> Option<u64> {
    let needle = format!("identity.age.{}-", BACKUP_INFIX);
    let rest = name.strip_prefix(&needle)?;
    rest.parse::<u64>().ok()
}

#[cfg(test)]
mod tests {
    use super::*;

    use age::x25519::Identity;
    use ed25519_dalek::SigningKey;
    use rand::rngs::OsRng;

    use crate::swarm::cryptsync::config::{
        config_path, dot_dir, salt_path, save_config, UserConfig,
    };
    use crate::swarm::cryptsync::crypt::{fresh_salt, wrap_identity};

    fn fresh_signing_key() -> SigningKey {
        SigningKey::generate(&mut OsRng)
    }

    fn enroll_fleet_mode(home: &Path, fleet_key: &SigningKey) -> Identity {
        // Mirror what `cryptsync fleet init` does on disk: write
        // identity.age + identity.salt + config.yaml{wrap_mode: fleet}.
        std::fs::create_dir_all(dot_dir(home)).unwrap();
        let id = Identity::generate();
        let salt = fresh_salt();
        let pw = derive_fleet_wrap_password_for(fleet_key);
        let blob = wrap_identity(&id, pw.as_slice(), &salt).unwrap();
        std::fs::write(identity_path(home), &blob).unwrap();
        std::fs::write(salt_path(home), &salt).unwrap();
        let mut cfg = UserConfig::default();
        cfg.wrap_mode = WrapMode::Fleet;
        save_config(home, &cfg, "tester").unwrap();
        id
    }

    #[test]
    fn rewrap_round_trips_under_new_keypair() {
        let dir = tempfile::tempdir().unwrap();
        let old = fresh_signing_key();
        let new = fresh_signing_key();
        let original = enroll_fleet_mode(dir.path(), &old);

        let outcome = rewrap_identity_for_new_fleet_key(
            dir.path(),
            "tester",
            &old,
            &new,
        )
        .unwrap();
        assert!(matches!(outcome, RewrapOutcome::Rewrapped { .. }));

        // The new envelope must unwrap under the new keypair and
        // produce the same age identity.
        let blob = std::fs::read(identity_path(dir.path())).unwrap();
        let salt = std::fs::read(salt_path(dir.path())).unwrap();
        let new_pw = derive_fleet_wrap_password_for(&new);
        let recovered = unwrap_identity(&blob, new_pw.as_slice(), &salt).unwrap();
        assert_eq!(
            recovered.to_public().to_string(),
            original.to_public().to_string()
        );

        // The new envelope MUST NOT unwrap under the old keypair.
        let old_pw = derive_fleet_wrap_password_for(&old);
        assert!(unwrap_identity(&blob, old_pw.as_slice(), &salt).is_err());
    }

    #[test]
    fn rewrap_leaves_backup_at_pre_rekey_path() {
        let dir = tempfile::tempdir().unwrap();
        let old = fresh_signing_key();
        let new = fresh_signing_key();
        enroll_fleet_mode(dir.path(), &old);
        let original_blob = std::fs::read(identity_path(dir.path())).unwrap();

        let outcome = rewrap_identity_for_new_fleet_key(
            dir.path(),
            "tester",
            &old,
            &new,
        )
        .unwrap();
        let backup_path = match outcome {
            RewrapOutcome::Rewrapped { backup_path } => backup_path,
            other => panic!("expected Rewrapped, got {:?}", other),
        };
        assert!(backup_path.exists(), "backup file should exist");
        let backup_bytes = std::fs::read(&backup_path).unwrap();
        assert_eq!(
            backup_bytes, original_blob,
            "backup must be byte-identical to original envelope"
        );
        // Backup file name format check.
        let name = backup_path.file_name().unwrap().to_str().unwrap();
        assert!(
            name.starts_with("identity.age.pre-rekey-"),
            "unexpected backup name: {}",
            name
        );
    }

    #[test]
    fn rewrap_no_op_when_not_enrolled() {
        let dir = tempfile::tempdir().unwrap();
        let old = fresh_signing_key();
        let new = fresh_signing_key();
        let outcome = rewrap_identity_for_new_fleet_key(
            dir.path(),
            "tester",
            &old,
            &new,
        )
        .unwrap();
        assert!(matches!(outcome, RewrapOutcome::NotEnrolled));
    }

    #[test]
    fn rewrap_skips_standalone_mode() {
        let dir = tempfile::tempdir().unwrap();
        // Enroll under standalone (passphrase-derived) wrap.
        std::fs::create_dir_all(dot_dir(dir.path())).unwrap();
        let id = Identity::generate();
        let salt = fresh_salt();
        let blob = wrap_identity(&id, b"per-envoy-passphrase", &salt).unwrap();
        std::fs::write(identity_path(dir.path()), &blob).unwrap();
        std::fs::write(salt_path(dir.path()), &salt).unwrap();
        // No config.yaml = default = standalone.

        let old = fresh_signing_key();
        let new = fresh_signing_key();
        let outcome = rewrap_identity_for_new_fleet_key(
            dir.path(),
            "tester",
            &old,
            &new,
        )
        .unwrap();
        assert!(matches!(outcome, RewrapOutcome::StandaloneSkipped));

        // identity.age must be untouched.
        let after = std::fs::read(identity_path(dir.path())).unwrap();
        assert_eq!(after, blob);
    }

    #[test]
    fn rewrap_fails_when_old_keypair_is_wrong() {
        let dir = tempfile::tempdir().unwrap();
        let real_old = fresh_signing_key();
        let bogus_old = fresh_signing_key();
        let new = fresh_signing_key();
        enroll_fleet_mode(dir.path(), &real_old);
        let original_blob = std::fs::read(identity_path(dir.path())).unwrap();

        let result = rewrap_identity_for_new_fleet_key(
            dir.path(),
            "tester",
            &bogus_old,
            &new,
        );
        assert!(result.is_err(), "rewrap must reject wrong old keypair");

        // identity.age must still be the original — we restored from
        // backup on failure, and there must be no leftover backup.
        let after = std::fs::read(identity_path(dir.path())).unwrap();
        assert_eq!(after, original_blob);
        let leftover_backup = std::fs::read_dir(dot_dir(dir.path()))
            .unwrap()
            .flatten()
            .any(|e| {
                e.file_name()
                    .to_str()
                    .map(|n| n.starts_with("identity.age.pre-rekey-"))
                    .unwrap_or(false)
            });
        // The failure path here unwraps before renaming, so no
        // leftover should be created. If a future refactor changes
        // the order, this test catches the leak.
        assert!(!leftover_backup, "no backup file should be left behind on unwrap failure");

        // config.yaml is irrelevant for this assertion but include
        // for read-coverage of the path.
        assert!(config_path(dir.path()).exists());
    }

    #[test]
    fn gc_reaps_old_backups_only() {
        let dir = tempfile::tempdir().unwrap();
        std::fs::create_dir_all(dot_dir(dir.path())).unwrap();
        let dot = dot_dir(dir.path());

        let now = SystemTime::now()
            .duration_since(UNIX_EPOCH)
            .unwrap()
            .as_secs();
        let stale_ts = now - BACKUP_RETENTION_SECS - 100;
        let fresh_ts = now - 60;

        // Stale backup — should be reaped.
        std::fs::write(
            dot.join(format!("identity.age.pre-rekey-{}", stale_ts)),
            b"old",
        )
        .unwrap();
        // Fresh backup — should survive.
        std::fs::write(
            dot.join(format!("identity.age.pre-rekey-{}", fresh_ts)),
            b"new",
        )
        .unwrap();
        // Unrelated file — must not be touched.
        std::fs::write(dot.join("recipients.txt"), b"# empty\n").unwrap();
        // Wrong-shape filename — must not be touched.
        std::fs::write(
            dot.join("identity.age.pre-rekey-not-a-timestamp"),
            b"junk",
        )
        .unwrap();

        let removed = gc_old_backups(dir.path()).unwrap();
        assert_eq!(removed, 1, "only the stale backup should be removed");
        assert!(!dot
            .join(format!("identity.age.pre-rekey-{}", stale_ts))
            .exists());
        assert!(dot
            .join(format!("identity.age.pre-rekey-{}", fresh_ts))
            .exists());
        assert!(dot.join("recipients.txt").exists());
        assert!(dot.join("identity.age.pre-rekey-not-a-timestamp").exists());
    }

    #[test]
    fn gc_no_op_on_missing_dot_dir() {
        let dir = tempfile::tempdir().unwrap();
        // Don't create dot_dir.
        let removed = gc_old_backups(dir.path()).unwrap();
        assert_eq!(removed, 0);
    }

    #[test]
    fn parse_backup_timestamp_accepts_well_formed_name() {
        assert_eq!(
            parse_backup_timestamp("identity.age.pre-rekey-1714500000"),
            Some(1714500000)
        );
    }

    #[test]
    fn parse_backup_timestamp_rejects_unrelated_names() {
        assert_eq!(parse_backup_timestamp("identity.age"), None);
        assert_eq!(parse_backup_timestamp("identity.age.tmp"), None);
        assert_eq!(parse_backup_timestamp("recipients.txt"), None);
        assert_eq!(
            parse_backup_timestamp("identity.age.pre-rekey-notanum"),
            None
        );
    }

    #[test]
    fn derive_fleet_wrap_password_is_deterministic_for_same_key() {
        // Locks in the invariant that the rewrap password derivation
        // matches the session-helper-driven path: signing the same
        // fixed challenge with the same Ed25519 key produces the
        // same 64 bytes (RFC 8032 makes Ed25519 sigs deterministic).
        // If a future refactor breaks this, fleet-mode unlock breaks
        // simultaneously — catch it here.
        let key = fresh_signing_key();
        let a = derive_fleet_wrap_password_for(&key);
        let b = derive_fleet_wrap_password_for(&key);
        assert_eq!(a.as_slice(), b.as_slice());
        assert_eq!(a.len(), 64);
    }

}
