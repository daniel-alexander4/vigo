
# Backup Restore Testing

| | |
|---|---|
| **Machine name** | `backup_restore` |
| **Controls** | FINRA-BCP-1, FINRA-BCP-2 |
| **Platforms** | All |
| **Timeout** | 1 hour |

## What it verifies

Executes a real backup restore operation and measures the recovery time against an RTO (Recovery Time Objective) target. Optionally runs a verification command afterward to confirm data integrity (e.g., checksum validation). This is an active test -- it actually runs the restore, not just a configuration check. Results are recorded to the trends database for RTO trending over time.

## Parameters

| Parameter | Type | Default | Description |
|-----------|------|---------|-------------|
| `restore_command` | string | (required) | Shell command to execute the restore operation. Runs via `sh -c` on Unix or `cmd.exe /C` on Windows. The capability is skipped if this is not set. |
| `verify_command` | string | (optional) | Shell command to verify restored data integrity (e.g., checksum comparison, database consistency check). Runs after a successful restore. |

## Thresholds

| Threshold | Type | Default | Description |
|-----------|------|---------|-------------|
| `rto_target_seconds` | integer | `3600` | Maximum acceptable time for the restore to complete, in seconds. The restore still runs to completion regardless -- this threshold only affects the pass/fail result. |

## Verification logic

1. **Check configuration** -- If `restore_command` is not set, the capability returns `Skipped` with an explanation.

2. **Execute restore** -- Run the `restore_command` via the system shell and time the execution. Record the exit code, duration, and last 10 lines of stdout as evidence. If the command exits non-zero, record a failure with the stderr output.

3. **RTO check** -- Compare the actual restore duration against `rto_target_seconds`. If the restore took longer, flag that the RTO was exceeded.

4. **Verification** (if `verify_command` is set) -- Run the verification command and time it. Record the duration and last 5 lines of output. If the command exits non-zero, record a verification failure.

5. **Trend recording** -- Store `rto_seconds`, `rto_target_seconds`, and `restore_success` in the trends database.

6. **RTO trend analysis** -- If at least 3 historical data points exist, compute the RTO trend slope (seconds per run). A positive slope indicates degrading restore performance; a slope near zero indicates stable performance.

### Result

- **Pass** -- Restore completed successfully, RTO target was met, and verification (if configured) passed.
- **Warn** -- Restore completed but the RTO target was exceeded, or verification failed.
- **Fail** -- The restore command itself failed (non-zero exit).

## Example config

```yaml
capabilities:
  backup_restore:
    schedule: "weekly"
    params:
      restore_command: "/opt/backup/restore-test.sh --target /mnt/restore-test"
      verify_command: "sha256sum -c /opt/backup/checksums.sha256"
    thresholds:
      rto_target_seconds: 1800
```

Database restore with a 15-minute RTO:

```yaml
capabilities:
  backup_restore:
    schedule: "weekly"
    params:
      restore_command: "pg_restore --dbname=restore_test /var/backup/latest.dump"
      verify_command: "psql restore_test -c 'SELECT count(*) FROM critical_table'"
    thresholds:
      rto_target_seconds: 900
```
