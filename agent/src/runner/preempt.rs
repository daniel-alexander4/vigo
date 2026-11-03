// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: Apache-2.0

//! Preempt sentinel — allows `vigo run` to interrupt an in-progress daemon
//! convergence. The sentinel is a file at `<data_dir>/preempt`. When present,
//! the convergence loop checks it between resources and bails early.

use std::fs;
use std::path::{Path, PathBuf};

/// Sentinel file name within the data directory.
const SENTINEL: &str = "preempt";

/// Write the preempt sentinel file.
pub fn request(data_dir: &Path) -> std::io::Result<()> {
    let path = sentinel_path(data_dir);
    fs::write(&path, b"")
}

/// Remove the preempt sentinel file (best-effort).
pub fn clear(data_dir: &Path) {
    let _ = fs::remove_file(sentinel_path(data_dir));
}

/// Check if the preempt sentinel is present.
pub fn is_requested(data_dir: &Path) -> bool {
    sentinel_path(data_dir).exists()
}

fn sentinel_path(data_dir: &Path) -> PathBuf {
    data_dir.join(SENTINEL)
}

#[cfg(test)]
mod tests {
    use super::*;
    use tempfile::TempDir;

    #[test]
    fn request_and_clear() {
        let dir = TempDir::new().unwrap();
        assert!(!is_requested(dir.path()));
        request(dir.path()).unwrap();
        assert!(is_requested(dir.path()));
        clear(dir.path());
        assert!(!is_requested(dir.path()));
    }
}
