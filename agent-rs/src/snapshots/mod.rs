// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: Elastic-2.0

//! File snapshot manager for rollback support.
//!
//! Snapshots are stored as `<sha256_of_path>.<run_id>.bak` in a configurable
//! base directory. Each source path retains at most `max_per_path` snapshots;
//! older snapshots are pruned after every save.

use sha2::{Digest, Sha256};
use std::fs;
use std::io;
use std::path::{Path, PathBuf};

/// Manages saving, restoring, and pruning file snapshots.
pub struct Manager {
    pub base_dir: PathBuf,
    pub max_per_path: usize,
}

impl Manager {
    pub fn new(base_dir: impl Into<PathBuf>, max_per_path: usize) -> Self {
        Self {
            base_dir: base_dir.into(),
            max_per_path,
        }
    }

    /// Save a snapshot of `file_path` tagged with `run_id`.
    pub fn save(&self, file_path: &str, run_id: &str) -> io::Result<()> {
        fs::create_dir_all(&self.base_dir)?;

        let src = fs::read(file_path)?;
        let dst_path = self.base_dir.join(snapshot_name(file_path, run_id));
        fs::write(&dst_path, &src)?;

        self.prune(file_path)
    }

    /// Restore a snapshot back to the original file path (atomic via rename).
    pub fn restore(&self, file_path: &str, snapshot_path: &str) -> io::Result<()> {
        let data = fs::read(snapshot_path)?;

        let dir = Path::new(file_path)
            .parent()
            .unwrap_or_else(|| Path::new("."));
        fs::create_dir_all(dir)?;

        let tmp = dir.join(format!(".vigo-restore-{}", uuid::Uuid::new_v4()));
        fs::write(&tmp, &data)?;

        if let Err(e) = fs::rename(&tmp, file_path) {
            let _ = fs::remove_file(&tmp);
            return Err(e);
        }

        Ok(())
    }

    /// Remove old snapshots beyond `max_per_path` for the given file.
    pub fn prune(&self, file_path: &str) -> io::Result<()> {
        let prefix = path_hash(file_path);

        let mut snaps: Vec<String> = fs::read_dir(&self.base_dir)?
            .filter_map(|e| e.ok())
            .filter_map(|e| {
                let name = e.file_name().to_string_lossy().to_string();
                if name.starts_with(&prefix) && name.ends_with(".bak") {
                    Some(name)
                } else {
                    None
                }
            })
            .collect();

        snaps.sort();

        if snaps.len() <= self.max_per_path {
            return Ok(());
        }

        let to_remove = snaps.len() - self.max_per_path;
        for name in &snaps[..to_remove] {
            fs::remove_file(self.base_dir.join(name))?;
        }

        Ok(())
    }
}

/// SHA-256 hex digest of a file path, used as snapshot filename prefix.
fn path_hash(file_path: &str) -> String {
    let hash = Sha256::digest(file_path.as_bytes());
    hash.iter().map(|b| format!("{:02x}", b)).collect()
}

/// Build snapshot filename: `<hash>.<run_id>.bak`.
fn snapshot_name(file_path: &str, run_id: &str) -> String {
    format!("{}.{}.bak", path_hash(file_path), run_id)
}

#[cfg(test)]
mod tests {
    use super::*;
    use tempfile::TempDir;

    #[test]
    fn save_and_restore() {
        let dir = TempDir::new().unwrap();
        let snap_dir = dir.path().join("snapshots");
        let mgr = Manager::new(&snap_dir, 3);

        let src = dir.path().join("test.txt");
        fs::write(&src, "original content\n").unwrap();

        mgr.save(src.to_str().unwrap(), "run-1").unwrap();

        // Modify source.
        fs::write(&src, "modified content\n").unwrap();

        // Restore.
        let snap_path = snap_dir.join(snapshot_name(src.to_str().unwrap(), "run-1"));
        mgr.restore(src.to_str().unwrap(), snap_path.to_str().unwrap())
            .unwrap();

        assert_eq!(fs::read_to_string(&src).unwrap(), "original content\n");
    }

    #[test]
    fn prune_old_snapshots() {
        let dir = TempDir::new().unwrap();
        let snap_dir = dir.path().join("snapshots");
        let mgr = Manager::new(&snap_dir, 2);

        let src = dir.path().join("prune.txt");
        fs::write(&src, "data").unwrap();

        mgr.save(src.to_str().unwrap(), "run-1").unwrap();
        mgr.save(src.to_str().unwrap(), "run-2").unwrap();
        mgr.save(src.to_str().unwrap(), "run-3").unwrap();

        // Should have pruned to 2.
        let prefix = path_hash(src.to_str().unwrap());
        let count = fs::read_dir(&snap_dir)
            .unwrap()
            .filter_map(|e| e.ok())
            .filter(|e| {
                let name = e.file_name().to_string_lossy().to_string();
                name.starts_with(&prefix) && name.ends_with(".bak")
            })
            .count();

        assert_eq!(count, 2);
    }

    #[test]
    fn path_hash_deterministic() {
        let h1 = path_hash("/etc/app.conf");
        let h2 = path_hash("/etc/app.conf");
        assert_eq!(h1, h2);
        assert_eq!(h1.len(), 64); // SHA-256 hex = 64 chars
    }
}
