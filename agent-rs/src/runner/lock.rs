// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: Elastic-2.0

//! File-based convergence lock using flock(2). Prevents two agent processes
//! (e.g. daemon + manual `hobnob run`) from converging simultaneously.
//!
//! The lock is automatically released when the guard is dropped or the
//! process exits (including crashes).

use std::fs::{self, File};
use std::path::{Path, PathBuf};

/// RAII guard — holds an exclusive flock on the convergence lock file.
/// The lock is released when this value is dropped.
pub struct ConvergenceLock {
    _file: File,
    path: PathBuf,
}

impl ConvergenceLock {
    /// Try to acquire the convergence lock (non-blocking).
    ///
    /// Returns `Ok(Some(guard))` if acquired, `Ok(None)` if another process
    /// holds it, or `Err` on I/O failure.
    pub fn try_acquire(data_dir: &Path) -> anyhow::Result<Option<Self>> {
        let path = data_dir.join("convergence.lock");

        // Ensure the data directory exists.
        if let Some(parent) = path.parent() {
            fs::create_dir_all(parent)?;
        }

        let file = File::options()
            .create(true)
            .truncate(false)
            .write(true)
            .open(&path)?;

        if try_flock_exclusive(&file) {
            Ok(Some(Self { _file: file, path }))
        } else {
            Ok(None)
        }
    }

    /// Path to the lock file (for logging).
    pub fn path(&self) -> &Path {
        &self.path
    }
}

// flock is released automatically when the File is dropped.

/// Try a non-blocking exclusive flock. Returns true if acquired.
#[cfg(unix)]
fn try_flock_exclusive(file: &File) -> bool {
    use std::os::unix::io::AsRawFd;
    // LOCK_EX | LOCK_NB = exclusive, non-blocking.
    let ret = unsafe { libc::flock(file.as_raw_fd(), libc::LOCK_EX | libc::LOCK_NB) };
    ret == 0
}

#[cfg(windows)]
fn try_flock_exclusive(file: &File) -> bool {
    use std::os::windows::io::AsRawHandle;
    use windows_sys::Win32::Storage::FileSystem::{
        LockFileEx, LOCKFILE_EXCLUSIVE_LOCK, LOCKFILE_FAIL_IMMEDIATELY,
    };
    use windows_sys::Win32::System::IO::OVERLAPPED;

    let handle = file.as_raw_handle() as isize;
    let mut overlapped: OVERLAPPED = unsafe { std::mem::zeroed() };
    let ret = unsafe {
        LockFileEx(
            handle,
            LOCKFILE_EXCLUSIVE_LOCK | LOCKFILE_FAIL_IMMEDIATELY,
            0,
            1,
            0,
            &mut overlapped,
        )
    };
    ret != 0
}

#[cfg(test)]
mod tests {
    use super::*;
    use tempfile::TempDir;

    #[test]
    fn acquire_and_release() {
        let dir = TempDir::new().unwrap();
        let guard = ConvergenceLock::try_acquire(dir.path()).unwrap();
        assert!(guard.is_some(), "should acquire lock");
        drop(guard);

        // After release, should be able to re-acquire.
        let guard2 = ConvergenceLock::try_acquire(dir.path()).unwrap();
        assert!(guard2.is_some(), "should re-acquire after release");
    }

    #[test]
    fn double_acquire_fails() {
        let dir = TempDir::new().unwrap();
        let _guard = ConvergenceLock::try_acquire(dir.path()).unwrap().unwrap();

        // Second acquire from same process — flock is per-fd, so opening
        // a new fd and trying LOCK_NB should fail.
        let guard2 = ConvergenceLock::try_acquire(dir.path()).unwrap();
        // On Linux, flock is per-open-file-description. A second open() in
        // the same process gets a new file description, so LOCK_NB will fail.
        assert!(guard2.is_none(), "second acquire should fail");
    }
}
