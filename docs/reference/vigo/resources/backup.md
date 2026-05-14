
# backup

Hardlink-based incremental directory snapshots. Creates dated snapshot directories where unchanged files share inodes with the previous snapshot, consuming zero additional disk. Each snapshot is a complete, browsable directory tree.

## Parameters

| Parameter | Required | Default | Description |
|-----------|----------|---------|-------------|
| `source_dir` | Yes | -- | Directory to back up. |
| `target_path` | Yes | -- | Base directory for dated snapshots. Snapshots are created as subdirectories named `YYYY.JJJ.HH.MM` (year, Julian day, hour, minute). |
| `retain` | No | `30` | Days of snapshots to keep. Older snapshots are pruned after each run. |
| `schedule` | No | `daily` | Dedup window: `daily`, `hourly`, or `weekly`. Only one snapshot is created per window. |
| `exclude` | No | -- | Comma-separated glob patterns to skip (e.g., `*.swp,*.tmp,cache/*`). |
| `cross_fs` | No | `fail` | Behavior when source and target_path are on different filesystems. See below. |

## cross_fs Values

| Value | Behavior |
|-------|----------|
| `fail` | Refuse to run. Error message explains the constraint and how to fix it. |
| `copy` | Full copy instead of hardlinks. Logs a warning on every run. |
| `warn` | Same as `copy` but only warns on first run. |

Hardlinks cannot span filesystems. If `source` and `target_path` are on different mount points, the executor cannot deduplicate. The default `fail` prevents accidental disk usage. Set `cross_fs: copy` only after understanding the storage impact.

## Idempotency

The executor checks if a snapshot already exists within the current schedule window (same day for `daily`, same hour for `hourly`, same week for `weekly`). If yes, it skips. Only runs once per window regardless of convergence frequency.

## How It Works

1. Check if today's snapshot exists. If yes, skip.
2. Find the most recent existing snapshot as a reference.
3. Walk the source directory tree.
4. For each file: compare size and mtime against the reference snapshot.
   - **Unchanged**: create a hardlink to the reference copy (zero disk cost).
   - **Changed or new**: copy the file.
5. Prune snapshots older than `retain` days.

## Disk Usage Example

100 GB source directory, 1% daily change rate:
- 30 daily snapshots with hardlinks: ~100 GB + (30 x 1 GB) = ~130 GB
- 30 daily full copies: 3,000 GB

## Examples

### Daily /etc backup with 30-day retention

```yaml
resources:
  - name: etc-backup
    type: backup
    source_dir: /etc
    target_path: /var/backups/etc
    retain: "30"
```

Produces:
```
/var/backups/etc/
  2026.088.06.00/
  2026.089.06.00/
  2026.090.06.00/    <-- today
```

### Hourly config backup

```yaml
resources:
  - name: app-config
    type: backup
    source_dir: /opt/app/config
    target_path: /var/backups/app
    retain: "7"
    schedule: hourly
    exclude: "*.log,tmp/*"
```

### Cross-filesystem with explicit opt-in

```yaml
resources:
  - name: remote-backup
    type: backup
    source_dir: /data
    target_path: /mnt/nas/backups/data
    cross_fs: copy
    retain: "14"
```

## Platform

Linux, macOS, FreeBSD, Windows (NTFS). Uses `std::fs::hard_link` which maps to POSIX `link()` on Unix and `CreateHardLinkW` on Windows.

## Notes

- Source and target_path must be on the same filesystem for hardlink dedup. Use `cross_fs: copy` to override with full copies.
- Symlinks in the source tree are skipped (not followed or copied).
- Directory permissions are preserved on Unix.
- The executor runs with `nice`/`ionice` priorities inherited from the agent process.
- Snapshot directory names use UTC time.

---
*Confidential -- Alexander4, LLC. Not for redistribution. See [documentation-license](../../../legal/license.md).*
