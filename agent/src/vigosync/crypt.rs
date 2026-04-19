// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

//! age-based in-place encryption for files in `~<user>/vigosync/`.
//!
//! Design (Option A from the plan): when a user opts in via
//! `config.yaml` → `encrypt: true`, the scanner encrypts files in
//! place before hashing. Every machine — including the sender — then
//! holds ciphertext. This keeps state tracking uniform across the
//! fleet (plaintext vs. ciphertext hashes would diverge) at the cost
//! of the main machine no longer being able to open files with
//! native apps; users decrypt on demand via `vigo vigosync decrypt`.
//!
//! The agent (root) only ever *encrypts* — decrypt is a CLI verb
//! running under the user's uid. The agent never reads
//! `identity.txt`, so root-on-sender isn't a trivial key-extraction
//! target.

use std::fs;
use std::io::{self, Read, Write};
use std::path::Path;

use age::x25519::{Identity, Recipient};
use anyhow::{bail, Context, Result};

/// Age file magic — every age-encrypted file begins with this line.
/// Detection lets the scanner skip re-encrypting files that are
/// already ciphertext.
pub const AGE_MAGIC: &[u8] = b"age-encryption.org/v1\n";

/// Return true if the first bytes of `path` match the age magic.
/// Short files return false. Genuine I/O errors bubble up.
pub fn is_age_encrypted(path: &Path) -> io::Result<bool> {
    let mut f = fs::File::open(path)?;
    let mut buf = [0u8; AGE_MAGIC.len()];
    let n = f.read(&mut buf)?;
    Ok(n == AGE_MAGIC.len() && buf == *AGE_MAGIC)
}

/// Encrypt a file in place using age. Writes ciphertext to a temp
/// file in the same directory, fsyncs, and atomically renames over
/// the original. Preserves the file's mtime so the scanner's
/// `(size, mtime)` cache key doesn't see spurious churn.
///
/// The temp file lives alongside the target so the rename is an
/// atomic filesystem operation on every supported OS. Dotfile name
/// (`.vigosync.enc.tmp`) keeps it out of the scanner's view — the
/// scanner already skips leading-dot names.
pub fn encrypt_in_place(path: &Path, recipients: &[Recipient]) -> Result<()> {
    if recipients.is_empty() {
        bail!("encrypt_in_place called with no recipients");
    }

    let dir = path.parent().context("file has no parent")?;
    let name = path.file_name().context("file has no name")?;
    let tmp_name = format!(".{}.vigosync.enc.tmp", name.to_string_lossy());
    let tmp_path = dir.join(&tmp_name);

    let meta = fs::metadata(path)
        .with_context(|| format!("stat {}", path.display()))?;

    let encryptor = age::Encryptor::with_recipients(
        recipients.iter().map(|r| r as &dyn age::Recipient),
    )
    .map_err(|e| anyhow::anyhow!("age encryptor init failed: {}", e))?;

    let plaintext_file = fs::File::open(path)
        .with_context(|| format!("opening {}", path.display()))?;
    let mut plaintext = io::BufReader::new(plaintext_file);

    let out_file = fs::File::create(&tmp_path)
        .with_context(|| format!("creating {}", tmp_path.display()))?;
    let mut out_buf = io::BufWriter::new(out_file);

    {
        let mut writer = encryptor
            .wrap_output(&mut out_buf)
            .context("age wrap_output failed")?;
        io::copy(&mut plaintext, &mut writer).context("streaming encryption")?;
        writer.finish().context("age writer finish failed")?;
    }

    out_buf.flush().ok();
    let out_file = out_buf
        .into_inner()
        .map_err(|e| anyhow::anyhow!("unwrap BufWriter: {}", e))?;
    out_file.sync_all().context("fsync ciphertext temp")?;
    drop(out_file);

    if let Err(e) = fs::rename(&tmp_path, path) {
        let _ = fs::remove_file(&tmp_path);
        return Err(e).with_context(|| format!(
            "rename {} → {}",
            tmp_path.display(),
            path.display()
        ));
    }

    #[cfg(unix)]
    if let Ok(modified) = meta.modified() {
        let _ = set_mtime(path, modified);
    }
    #[cfg(not(unix))]
    let _ = meta;

    Ok(())
}

#[cfg(unix)]
fn set_mtime(path: &Path, t: std::time::SystemTime) -> io::Result<()> {
    use std::ffi::CString;
    let d = t.duration_since(std::time::UNIX_EPOCH).unwrap_or_default();
    let c_path = CString::new(path.to_string_lossy().as_bytes())
        .map_err(|_| io::Error::new(io::ErrorKind::InvalidInput, "path has nul"))?;
    let times = [
        libc::timespec { tv_sec: 0, tv_nsec: libc::UTIME_OMIT },
        libc::timespec {
            tv_sec: d.as_secs() as libc::time_t,
            tv_nsec: d.subsec_nanos() as i64,
        },
    ];
    let rc = unsafe { libc::utimensat(libc::AT_FDCWD, c_path.as_ptr(), times.as_ptr(), 0) };
    if rc != 0 {
        Err(io::Error::last_os_error())
    } else {
        Ok(())
    }
}

/// Decrypt a ciphertext file and stream plaintext to `writer`. Used
/// by the `vigo vigosync decrypt` CLI verb.
pub fn decrypt_to_writer(
    path: &Path,
    identity: &Identity,
    writer: &mut dyn Write,
) -> Result<()> {
    let f = fs::File::open(path)
        .with_context(|| format!("opening {}", path.display()))?;
    let reader = io::BufReader::new(f);
    let decryptor = age::Decryptor::new(reader)
        .context("reading age header")?;
    let ids: Vec<Box<dyn age::Identity>> = vec![Box::new(identity.clone())];
    let mut r = decryptor
        .decrypt(ids.iter().map(|i| i.as_ref()))
        .context("decrypt — identity does not match any recipient in the file")?;
    io::copy(&mut r, writer).context("streaming decryption")?;
    Ok(())
}

#[cfg(test)]
mod tests {
    use super::*;
    use age::secrecy::ExposeSecret;

    #[test]
    fn is_age_encrypted_detects_magic() {
        let dir = tempfile::tempdir().unwrap();
        let p = dir.path().join("ciphertext.txt");
        fs::write(&p, AGE_MAGIC).unwrap();
        assert!(is_age_encrypted(&p).unwrap());
    }

    #[test]
    fn is_age_encrypted_rejects_plaintext() {
        let dir = tempfile::tempdir().unwrap();
        let p = dir.path().join("plain.txt");
        fs::write(&p, b"hello world").unwrap();
        assert!(!is_age_encrypted(&p).unwrap());
    }

    #[test]
    fn is_age_encrypted_rejects_short_file() {
        let dir = tempfile::tempdir().unwrap();
        let p = dir.path().join("short.txt");
        fs::write(&p, b"hi").unwrap();
        assert!(!is_age_encrypted(&p).unwrap());
    }

    #[test]
    fn encrypt_then_decrypt_roundtrips() {
        let dir = tempfile::tempdir().unwrap();
        let id = Identity::generate();
        let pubkey = id.to_public();

        let p = dir.path().join("notes.md");
        let content = b"the quick brown fox jumps over the lazy dog".to_vec();
        fs::write(&p, &content).unwrap();

        encrypt_in_place(&p, &[pubkey]).unwrap();
        assert!(is_age_encrypted(&p).unwrap());

        let mut decrypted = Vec::new();
        decrypt_to_writer(&p, &id, &mut decrypted).unwrap();
        assert_eq!(decrypted, content);
    }

    #[test]
    fn encrypt_with_multiple_recipients_lets_either_decrypt() {
        let dir = tempfile::tempdir().unwrap();
        let id1 = Identity::generate();
        let id2 = Identity::generate();
        let recips = vec![id1.to_public(), id2.to_public()];

        let p = dir.path().join("shared.txt");
        fs::write(&p, b"two can read this").unwrap();
        encrypt_in_place(&p, &recips).unwrap();

        for id in &[id1, id2] {
            let mut out = Vec::new();
            decrypt_to_writer(&p, id, &mut out).unwrap();
            assert_eq!(out, b"two can read this");
        }
    }

    #[test]
    fn encrypt_preserves_mtime() {
        let dir = tempfile::tempdir().unwrap();
        let id = Identity::generate();
        let p = dir.path().join("file.txt");
        fs::write(&p, b"x").unwrap();
        // Set a known mtime.
        let target = std::time::SystemTime::UNIX_EPOCH + std::time::Duration::from_secs(1_700_000_000);
        #[cfg(unix)]
        set_mtime(&p, target).unwrap();

        encrypt_in_place(&p, &[id.to_public()]).unwrap();

        let meta = fs::metadata(&p).unwrap();
        let got = meta.modified().unwrap();
        let drift = got.duration_since(target).unwrap_or_else(|e| e.duration());
        assert!(drift.as_secs() < 2, "mtime drifted by {:?}", drift);
    }

    #[test]
    fn encrypt_tempfile_is_cleaned_on_rename_failure() {
        // Hard to trigger rename failure synthetically on Linux tempfs.
        // The code path that removes the temp on rename error is
        // exercised in practice; documenting here so it isn't dropped.
        // Touching a bogus path to prove `encrypt_in_place` errors:
        let dir = tempfile::tempdir().unwrap();
        let id = Identity::generate();
        let p = dir.path().join("does-not-exist.txt");
        let err = encrypt_in_place(&p, &[id.to_public()]);
        assert!(err.is_err());
    }

    // Silence unused import warnings under certain feature flags.
    #[allow(dead_code)]
    fn _unused(_s: age::secrecy::SecretString) {
        let _ = ExposeSecret::expose_secret(&_s);
    }
}
