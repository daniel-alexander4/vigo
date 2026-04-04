// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

//! Backup executor — hardlink-based incremental directory snapshots.
//!
//! Creates dated snapshot directories under `backup_dir` using filesystem
//! hardlinks. Unchanged files share inodes with the previous snapshot,
//! consuming zero additional disk. Only changed or new files are copied.
//!
//! Directory naming: YYYY.JJJ.HH.MM (year.julian_day.hour.minute)
//!
//! Cross-platform: uses std::fs::hard_link (POSIX link() / Windows CreateHardLinkW).

use super::{Action, Executor, ResourceResult};
use std::collections::HashMap;
use std::fs;
use std::path::{Path, PathBuf};
use std::time::{Instant, SystemTime};
use tracing::{info, warn};

pub struct BackupExecutor;

impl BackupExecutor {
    pub fn new() -> Self {
        Self
    }
}

impl Executor for BackupExecutor {
    fn execute(&self, params: &HashMap<String, String>, dry_run: bool) -> ResourceResult {
        let start = Instant::now();
        let source_dir = params.get("source_dir").map(|s| s.as_str()).unwrap_or("");
        let mut result = ResourceResult::new("backup", source_dir);

        let res = execute_backup(params, dry_run, &mut result);
        result.duration_ms = start.elapsed().as_millis() as i32;
        if let Err(e) = res {
            result.error = Some(e);
        }
        result
    }
}

fn execute_backup(
    params: &HashMap<String, String>,
    dry_run: bool,
    result: &mut ResourceResult,
) -> Result<(), String> {
    let source_dir = params
        .get("source_dir")
        .filter(|s| !s.is_empty())
        .ok_or_else(|| "backup executor: source_dir param is required".to_string())?;

    let backup_dir = params
        .get("backup_dir")
        .filter(|s| !s.is_empty())
        .ok_or_else(|| "backup executor: backup_dir param is required".to_string())?;

    let retain_days: u64 = params
        .get("retain")
        .and_then(|s| s.parse().ok())
        .unwrap_or(30);

    let schedule = params.get("schedule").map(|s| s.as_str()).unwrap_or("daily");

    let cross_fs = params.get("cross_fs").map(|s| s.as_str()).unwrap_or("fail");

    let min_free = parse_size(
        params.get("min_free_space").map(|s| s.as_str()).unwrap_or("1G"),
    )
    .map_err(|e| format!("backup: invalid min_free_space: {}", e))?;

    let excludes: Vec<&str> = params
        .get("exclude")
        .map(|s| s.split(',').map(|e| e.trim()).collect())
        .unwrap_or_default();

    // Validate source exists.
    let source_path = Path::new(source_dir);
    if !source_path.is_dir() {
        return Err(format!("source directory does not exist: {}", source_dir));
    }

    // Check cross-filesystem.
    if cross_fs == "fail" || cross_fs == "copy" || cross_fs == "warn" {
        if let Some(cross) = check_cross_fs(source_dir, backup_dir) {
            if cross {
                match cross_fs {
                    "fail" => {
                        return Err(format!(
                            "source ({}) and backup_dir ({}) are on different filesystems — \
                             hardlink backups require the same filesystem. \
                             Set cross_fs: copy to allow full copies, or move backup_dir \
                             to the same filesystem.",
                            source_dir, backup_dir
                        ));
                    }
                    "copy" => {
                        warn!("cross-filesystem backup: using full copies (no dedup)");
                    }
                    "warn" => {
                        // First run logs, subsequent runs are silent.
                    }
                    _ => {}
                }
            }
        }
    }

    // Generate snapshot name for current time.
    let now = snapshot_name();
    let snapshot_path = Path::new(backup_dir).join(&now);

    // Check if a snapshot already exists within the schedule window.
    if let Some(latest) = find_latest_snapshot(backup_dir) {
        let latest_name = latest.file_name().unwrap_or_default().to_string_lossy();
        if same_schedule_window(&latest_name, &now, schedule) {
            result.action = Action::None;
            result.changed = false;
            return Ok(());
        }
    }

    if dry_run {
        result.action = Action::Created;
        result.changed = true;
        return Ok(());
    }

    // Check free space on the backup_dir filesystem.
    let backup_check_path = if Path::new(backup_dir).exists() {
        backup_dir.to_string()
    } else if let Some(parent) = Path::new(backup_dir).parent() {
        parent.to_string_lossy().to_string()
    } else {
        backup_dir.to_string()
    };
    if let Some(avail) = free_space(&backup_check_path) {
        if avail < min_free {
            return Err(format!(
                "backup skipped: {} has {} free (min_free_space: {})",
                backup_dir,
                format_size(avail),
                format_size(min_free),
            ));
        }
    }

    // Run pre_command (flush logs, stop service, dump DB, etc.).
    if let Some(pre) = params.get("pre_command").filter(|s| !s.is_empty()) {
        let output = std::process::Command::new("sh")
            .args(["-c", pre])
            .output()
            .map_err(|e| format!("pre_command failed to start: {}", e))?;
        if !output.status.success() {
            let stderr = String::from_utf8_lossy(&output.stderr);
            return Err(format!("pre_command failed: {}", stderr.trim()));
        }
    }

    // Ensure backup_dir exists.
    fs::create_dir_all(backup_dir)
        .map_err(|e| format!("create backup_dir {}: {}", backup_dir, e))?;

    // Find the latest existing snapshot as link reference.
    let reference = find_latest_snapshot(backup_dir);

    // Create the new snapshot directory.
    fs::create_dir_all(&snapshot_path)
        .map_err(|e| format!("create snapshot {}: {}", snapshot_path.display(), e))?;

    // Walk source and create the snapshot.
    let is_cross_fs = check_cross_fs(source_dir, backup_dir).unwrap_or(false);
    let stats = create_snapshot(
        source_path,
        &snapshot_path,
        reference.as_deref(),
        &excludes,
        is_cross_fs,
    )?;

    info!(
        source_dir = %source_dir,
        snapshot = %snapshot_path.display(),
        files = stats.files,
        hardlinked = stats.hardlinked,
        copied = stats.copied,
        dirs = stats.dirs,
        "backup snapshot created"
    );

    // Prune old snapshots.
    let pruned = prune_snapshots(backup_dir, retain_days)?;
    if pruned > 0 {
        info!(pruned = pruned, retain_days = retain_days, "pruned old snapshots");
    }

    // Run post_command (restart service, thaw, cleanup, etc.).
    if let Some(post) = params.get("post_command").filter(|s| !s.is_empty()) {
        let output = std::process::Command::new("sh")
            .args(["-c", post])
            .output()
            .map_err(|e| format!("post_command failed to start: {}", e))?;
        if !output.status.success() {
            let stderr = String::from_utf8_lossy(&output.stderr);
            warn!(error = %stderr.trim(), "post_command failed (backup succeeded)");
        }
    }

    result.action = Action::Created;
    result.changed = true;
    Ok(())
}

struct SnapshotStats {
    files: u64,
    hardlinked: u64,
    copied: u64,
    dirs: u64,
}

/// Walk the source directory and create a snapshot. For each file:
/// - If a reference snapshot exists and the file is unchanged (same size + mtime),
///   create a hardlink to the reference copy.
/// - Otherwise, copy the file.
fn create_snapshot(
    source: &Path,
    snapshot: &Path,
    reference: Option<&Path>,
    excludes: &[&str],
    force_copy: bool,
) -> Result<SnapshotStats, String> {
    let mut stats = SnapshotStats {
        files: 0,
        hardlinked: 0,
        copied: 0,
        dirs: 0,
    };

    walk_dir(source, source, snapshot, reference, excludes, force_copy, &mut stats)?;
    Ok(stats)
}

fn walk_dir(
    root: &Path,
    current: &Path,
    snapshot_base: &Path,
    reference_base: Option<&Path>,
    excludes: &[&str],
    force_copy: bool,
    stats: &mut SnapshotStats,
) -> Result<(), String> {
    let entries = fs::read_dir(current)
        .map_err(|e| format!("read_dir {}: {}", current.display(), e))?;

    for entry in entries {
        let entry = entry.map_err(|e| format!("dir entry: {}", e))?;
        let path = entry.path();
        let rel = path
            .strip_prefix(root)
            .map_err(|e| format!("strip prefix: {}", e))?;

        // Check excludes.
        let rel_str = rel.to_string_lossy();
        if excludes.iter().any(|pat| matches_glob(pat, &rel_str)) {
            continue;
        }

        let dest = snapshot_base.join(rel);
        let file_type = entry
            .file_type()
            .map_err(|e| format!("file_type {}: {}", path.display(), e))?;

        if file_type.is_dir() {
            fs::create_dir_all(&dest)
                .map_err(|e| format!("mkdir {}: {}", dest.display(), e))?;
            // Preserve directory permissions on Unix.
            #[cfg(unix)]
            {
                if let Ok(meta) = fs::metadata(&path) {
                    use std::os::unix::fs::PermissionsExt;
                    let _ = fs::set_permissions(&dest, meta.permissions());
                }
            }
            stats.dirs += 1;
            walk_dir(root, &path, snapshot_base, reference_base, excludes, force_copy, stats)?;
        } else if file_type.is_file() {
            stats.files += 1;

            // Try hardlink from reference snapshot if file is unchanged.
            if !force_copy {
                if let Some(ref_base) = reference_base {
                    let ref_path = ref_base.join(rel);
                    if can_hardlink(&path, &ref_path) {
                        match fs::hard_link(&ref_path, &dest) {
                            Ok(()) => {
                                stats.hardlinked += 1;
                                continue;
                            }
                            Err(_) => {
                                // Fall through to copy.
                            }
                        }
                    }
                }
            }

            // Copy the file.
            fs::copy(&path, &dest)
                .map_err(|e| format!("copy {} → {}: {}", path.display(), dest.display(), e))?;
            stats.copied += 1;

            // Preserve permissions and mtime on Unix. Mtime preservation is
            // critical: can_hardlink() compares source mtime against the
            // reference snapshot — without this, every file is re-copied.
            #[cfg(unix)]
            {
                if let Ok(meta) = fs::metadata(&path) {
                    use std::os::unix::fs::PermissionsExt;
                    let _ = fs::set_permissions(&dest, meta.permissions());
                    if let Ok(mtime) = meta.modified() {
                        let _ = set_mtime(&dest, mtime);
                    }
                }
            }
        }
        // Skip symlinks, sockets, etc.
    }
    Ok(())
}

/// Check if a source file is unchanged compared to the reference copy.
/// Compares size and modification time.
fn can_hardlink(source: &Path, reference: &Path) -> bool {
    let (src_meta, ref_meta) = match (fs::metadata(source), fs::metadata(reference)) {
        (Ok(s), Ok(r)) => (s, r),
        _ => return false,
    };

    if src_meta.len() != ref_meta.len() {
        return false;
    }

    match (src_meta.modified(), ref_meta.modified()) {
        (Ok(s), Ok(r)) => s == r,
        _ => false,
    }
}

/// Simple glob matching — supports * and ? only.
fn matches_glob(pattern: &str, text: &str) -> bool {
    let mut pi = pattern.chars().peekable();
    let mut ti = text.chars().peekable();

    while pi.peek().is_some() {
        match pi.peek() {
            Some('*') => {
                pi.next();
                if pi.peek().is_none() {
                    return true;
                }
                while ti.peek().is_some() {
                    if matches_glob(&pi.clone().collect::<String>(), &ti.clone().collect::<String>()) {
                        return true;
                    }
                    ti.next();
                }
                return false;
            }
            Some('?') => {
                pi.next();
                if ti.next().is_none() {
                    return false;
                }
            }
            Some(&pc) => {
                pi.next();
                match ti.next() {
                    Some(tc) if tc == pc => {}
                    _ => return false,
                }
            }
            None => break,
        }
    }

    ti.peek().is_none()
}

/// Generate a snapshot directory name: YYYY.JJJ.HH.MM
pub fn snapshot_name() -> String {
    let now = SystemTime::now()
        .duration_since(SystemTime::UNIX_EPOCH)
        .unwrap_or_default()
        .as_secs();

    // Convert Unix timestamp to year, ordinal day, hour, minute.
    let secs_per_day = 86400u64;
    let days_since_epoch = now / secs_per_day;
    let time_of_day = now % secs_per_day;
    let hour = time_of_day / 3600;
    let minute = (time_of_day % 3600) / 60;

    // Calculate year and ordinal day from days since 1970-01-01.
    let (year, ordinal) = days_to_year_ordinal(days_since_epoch);

    format!("{}.{:03}.{:02}.{:02}", year, ordinal, hour, minute)
}

/// Convert days since Unix epoch to (year, ordinal_day).
fn days_to_year_ordinal(mut days: u64) -> (i32, u32) {
    let mut year = 1970i32;
    loop {
        let days_in_year = if is_leap(year) { 366 } else { 365 };
        if days < days_in_year {
            return (year, days as u32 + 1); // ordinal is 1-based
        }
        days -= days_in_year;
        year += 1;
    }
}

fn is_leap(year: i32) -> bool {
    (year % 4 == 0 && year % 100 != 0) || (year % 400 == 0)
}

/// Check if two snapshot names fall within the same schedule window.
pub fn same_schedule_window(existing: &str, current: &str, schedule: &str) -> bool {
    match schedule {
        // Daily: same year + julian day
        "daily" => existing.len() >= 8 && current.len() >= 8 && existing[..8] == current[..8],
        // Hourly: same year + julian day + hour
        "hourly" => existing.len() >= 11 && current.len() >= 11 && existing[..11] == current[..11],
        // Weekly: same year + same week (julian day / 7)
        "weekly" => {
            if let (Some(e), Some(c)) = (parse_julian_day(existing), parse_julian_day(current)) {
                existing[..4] == current[..4] && (e / 7) == (c / 7)
            } else {
                false
            }
        }
        _ => existing[..8] == current[..8], // default to daily
    }
}

fn parse_julian_day(name: &str) -> Option<u32> {
    if name.len() < 8 {
        return None;
    }
    name[5..8].parse().ok()
}

/// Find the most recent snapshot directory in backup_dir.
pub fn find_latest_snapshot(backup_dir: &str) -> Option<PathBuf> {
    let entries = fs::read_dir(backup_dir).ok()?;
    let mut snapshots: Vec<PathBuf> = entries
        .filter_map(|e| e.ok())
        .filter(|e| e.path().is_dir())
        .filter(|e| {
            // Must match YYYY.JJJ.HH.MM format.
            let name = e.file_name();
            let s = name.to_string_lossy();
            s.len() >= 13 && s.chars().next().map(|c| c.is_ascii_digit()).unwrap_or(false)
        })
        .map(|e| e.path())
        .collect();

    snapshots.sort();
    snapshots.pop()
}

/// Remove snapshot directories older than retain_days.
pub fn prune_snapshots(backup_dir: &str, retain_days: u64) -> Result<u64, String> {
    let entries = fs::read_dir(backup_dir)
        .map_err(|e| format!("read backup_dir for pruning: {}", e))?;

    let cutoff = SystemTime::now()
        - std::time::Duration::from_secs(retain_days * 86400);

    let mut pruned = 0u64;
    for entry in entries.filter_map(|e| e.ok()) {
        let path = entry.path();
        if !path.is_dir() {
            continue;
        }

        // Check directory creation time.
        let meta = match fs::metadata(&path) {
            Ok(m) => m,
            Err(_) => continue,
        };

        let created = meta.modified().unwrap_or(SystemTime::UNIX_EPOCH);
        if created < cutoff {
            if let Err(e) = fs::remove_dir_all(&path) {
                warn!(path = %path.display(), error = %e, "failed to prune snapshot");
            } else {
                pruned += 1;
            }
        }
    }

    Ok(pruned)
}

/// Set the modification time of a file to match the source.
#[cfg(unix)]
fn set_mtime(path: &Path, mtime: SystemTime) -> Result<(), String> {
    use std::ffi::CString;
    let dur = mtime
        .duration_since(SystemTime::UNIX_EPOCH)
        .map_err(|e| e.to_string())?;
    let ts = libc::timespec {
        tv_sec: dur.as_secs() as libc::time_t,
        tv_nsec: dur.subsec_nanos() as libc::c_long,
    };
    // UTIME_OMIT for atime — only set mtime.
    let times = [
        libc::timespec {
            tv_sec: 0,
            tv_nsec: libc::UTIME_OMIT,
        },
        ts,
    ];
    let c_path =
        CString::new(path.to_string_lossy().as_bytes()).map_err(|e| e.to_string())?;
    let rc =
        unsafe { libc::utimensat(libc::AT_FDCWD, c_path.as_ptr(), times.as_ptr(), 0) };
    if rc == 0 {
        Ok(())
    } else {
        Err("utimensat failed".into())
    }
}

/// Check if source and dest are on different filesystems.
/// Returns None if the check can't be performed.
#[cfg(unix)]
fn check_cross_fs(source: &str, dest: &str) -> Option<bool> {
    use std::os::unix::fs::MetadataExt;
    let src_dev = fs::metadata(source).ok()?.dev();
    // dest might not exist yet — check parent.
    let dest_path = Path::new(dest);
    let dest_check = if dest_path.exists() {
        dest_path.to_path_buf()
    } else {
        dest_path.parent()?.to_path_buf()
    };
    let dest_dev = fs::metadata(&dest_check).ok()?.dev();
    Some(src_dev != dest_dev)
}

#[cfg(not(unix))]
fn check_cross_fs(_source: &str, _dest: &str) -> Option<bool> {
    // On Windows, skip the check — hardlinks will fail at runtime if cross-volume.
    None
}

/// Parse a human-readable size string (e.g., "1G", "500M", "100K", "1073741824").
pub fn parse_size(s: &str) -> Result<u64, String> {
    let s = s.trim();
    if s.is_empty() {
        return Ok(0);
    }
    let (num_str, multiplier) = match s.as_bytes().last() {
        Some(b'G' | b'g') => (&s[..s.len() - 1], 1024 * 1024 * 1024u64),
        Some(b'M' | b'm') => (&s[..s.len() - 1], 1024 * 1024u64),
        Some(b'K' | b'k') => (&s[..s.len() - 1], 1024u64),
        Some(b'T' | b't') => (&s[..s.len() - 1], 1024 * 1024 * 1024 * 1024u64),
        _ => (s, 1u64),
    };
    let num: u64 = num_str
        .trim()
        .parse()
        .map_err(|e| format!("{:?}: {}", s, e))?;
    Ok(num * multiplier)
}

/// Format bytes as a human-readable string.
pub fn format_size(bytes: u64) -> String {
    if bytes >= 1024 * 1024 * 1024 {
        format!("{:.1} GB", bytes as f64 / (1024.0 * 1024.0 * 1024.0))
    } else if bytes >= 1024 * 1024 {
        format!("{:.1} MB", bytes as f64 / (1024.0 * 1024.0))
    } else if bytes >= 1024 {
        format!("{:.1} KB", bytes as f64 / 1024.0)
    } else {
        format!("{} B", bytes)
    }
}

/// Get available free space on the filesystem containing the given path.
#[cfg(unix)]
pub fn free_space(path: &str) -> Option<u64> {
    use std::ffi::CString;
    let c_path = CString::new(path).ok()?;
    unsafe {
        let mut stat: libc::statvfs = std::mem::zeroed();
        if libc::statvfs(c_path.as_ptr(), &mut stat) == 0 {
            Some(stat.f_bavail as u64 * stat.f_frsize as u64)
        } else {
            None
        }
    }
}

#[cfg(not(unix))]
pub fn free_space(_path: &str) -> Option<u64> {
    // TODO: Windows — use GetDiskFreeSpaceExW
    None
}

#[cfg(test)]
mod tests {
    use super::*;
    use std::fs;
    use tempfile::TempDir;

    #[test]
    fn test_snapshot_name_format() {
        let name = snapshot_name();
        assert_eq!(name.len(), 14); // YYYY.JJJ.HH.MM
        assert_eq!(&name[4..5], ".");
        assert_eq!(&name[8..9], ".");
        assert_eq!(&name[11..12], ".");
    }

    #[test]
    fn test_same_schedule_window() {
        assert!(same_schedule_window("2026.090.06.00", "2026.090.18.30", "daily"));
        assert!(!same_schedule_window("2026.090.06.00", "2026.091.06.00", "daily"));
        assert!(same_schedule_window("2026.090.06.00", "2026.090.06.30", "hourly"));
        assert!(!same_schedule_window("2026.090.06.00", "2026.090.07.00", "hourly"));
    }

    #[test]
    fn test_glob_matching() {
        assert!(matches_glob("*.log", "app.log"));
        assert!(matches_glob("*.log", "server.log"));
        assert!(!matches_glob("*.log", "app.txt"));
        assert!(matches_glob("cache/*", "cache/data"));
        assert!(!matches_glob("cache/*", "logs/data"));
    }

    #[test]
    fn test_hardlink_snapshot() {
        let tmp = TempDir::new().unwrap();
        let source = tmp.path().join("source");
        let backup = tmp.path().join("backups");
        fs::create_dir_all(&source).unwrap();
        fs::create_dir_all(&backup).unwrap();

        // Create source files.
        fs::write(source.join("a.txt"), "hello").unwrap();
        fs::write(source.join("b.txt"), "world").unwrap();

        // First snapshot — all copies, no reference.
        let snap1 = backup.join("2026.090.06.00");
        fs::create_dir_all(&snap1).unwrap();
        let stats = create_snapshot(&source, &snap1, None, &[], false).unwrap();
        assert_eq!(stats.files, 2);
        assert_eq!(stats.copied, 2);
        assert_eq!(stats.hardlinked, 0);

        // Second snapshot — files unchanged, should hardlink.
        let snap2 = backup.join("2026.091.06.00");
        fs::create_dir_all(&snap2).unwrap();
        let stats = create_snapshot(&source, &snap2, Some(&snap1), &[], false).unwrap();
        assert_eq!(stats.files, 2);
        assert_eq!(stats.hardlinked, 2);
        assert_eq!(stats.copied, 0);

        // Modify one file.
        fs::write(source.join("a.txt"), "changed").unwrap();

        // Third snapshot — one hardlinked, one copied.
        let snap3 = backup.join("2026.092.06.00");
        fs::create_dir_all(&snap3).unwrap();
        let stats = create_snapshot(&source, &snap3, Some(&snap2), &[], false).unwrap();
        assert_eq!(stats.files, 2);
        assert_eq!(stats.hardlinked, 1);
        assert_eq!(stats.copied, 1);

        // All three snapshots are complete.
        assert_eq!(fs::read_to_string(snap1.join("a.txt")).unwrap(), "hello");
        assert_eq!(fs::read_to_string(snap2.join("a.txt")).unwrap(), "hello");
        assert_eq!(fs::read_to_string(snap3.join("a.txt")).unwrap(), "changed");
    }
}
