
# WORM Storage Verification

| | |
|---|---|
| **Machine name** | `worm_verify` |
| **Controls** | SEC-17a4-1, SEC-17a4-2 |
| **Platforms** | All |
| **Timeout** | default (5m) |

## What it verifies

Confirms that a Write Once Read Many (WORM) storage path is genuinely immutable -- meaning new files cannot be created, existing files cannot be modified, and (on Linux) the underlying mount enforces read-only semantics. Optionally verifies the integrity of specific files by comparing their SHA-256 hashes against known-good values, and can run a custom command to validate cloud-based WORM implementations (e.g., AWS S3 Object Lock, Azure immutable blob storage).

## Parameters

| Parameter | Type | Default | Description |
|-----------|------|---------|-------------|
| `worm_path` | string | *(required)* | Absolute path to the WORM storage directory. The capability is skipped if this is not set. |
| `expected_mount_options` | string | *(none)* | Linux only. Comma-separated mount options that must be present on the filesystem containing `worm_path` (e.g., `"ro,noexec"`). Each option is checked individually. |
| `verify_files` | array of objects | `[]` | List of files to verify by hash. Each object has `path` (string) and `sha256` (string, lowercase hex). |
| `cloud_check_command` | string | *(none)* | Shell command to validate cloud WORM configuration. Executed via `sh -c`. A zero exit code means the check passed. |

## Thresholds

This capability has no configurable thresholds.

## Verification logic

1. **Path existence** -- Check that `worm_path` exists on the filesystem. If it does not exist, the check fails immediately.

2. **Write test** -- Attempt to create and write to a test file (`.connwaer_worm_test`) inside the WORM directory. If the write succeeds, the file is cleaned up and a failure is recorded -- immutability is not enforced. If the write is blocked with `PermissionDenied` or `ReadOnlyFilesystem`, the test passes. Any other I/O error is treated as blocked but noted in evidence.

3. **Mount options (Linux only)** -- Read `/proc/mounts` and find the mount entry whose mount point is a prefix of `worm_path`. Record whether the mount is read-only (`ro` option present). If `expected_mount_options` is set, verify that every listed option appears in the actual mount options. Missing options are recorded as a failure.

4. **File integrity** -- For each entry in `verify_files`, compute the SHA-256 hash of the file at `path` and compare it to the expected `sha256` value. Mismatches or unreadable files are counted as tampered. Evidence records the total verified and tampered counts.

5. **Cloud WORM check** -- If `cloud_check_command` is set, execute it via `sh -c`. A zero exit code means the cloud WORM configuration is valid; stdout is captured as evidence. A non-zero exit code or execution error is recorded as a failure.

### Result

- **Pass** -- Write was blocked, all mount options match (if configured), all file hashes match (if configured), and cloud check passed (if configured).
- **Fail** -- Any of the checks above failed.
- **Skipped** -- `worm_path` is not configured.

## Example config

```yaml
capabilities:
  worm_verify:
    schedule: "daily"
    params:
      worm_path: "/mnt/worm"
      expected_mount_options: "ro,noexec"
```

With file integrity verification:

```yaml
capabilities:
  worm_verify:
    schedule: "daily"
    params:
      worm_path: "/mnt/worm"
      verify_files:
        - path: "/mnt/worm/audit-2024.log"
          sha256: "a3f2b8c1d4e5f6..."
        - path: "/mnt/worm/trades-2024.csv"
          sha256: "9b8c7d6e5f4a3b..."
```

With a cloud WORM validation command:

```yaml
capabilities:
  worm_verify:
    schedule: "daily"
    params:
      worm_path: "/mnt/worm"
      cloud_check_command: "aws s3api get-object-lock-configuration --bucket my-worm-bucket"
```
