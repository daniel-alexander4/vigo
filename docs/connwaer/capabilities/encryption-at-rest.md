
# Encryption at Rest

| | |
|---|---|
| **Machine name** | `encryption_at_rest` |
| **Controls** | HIPAA-164.312(a)(2)(iv), PCI-DSS-3.4 |
| **Platforms** | Linux, Windows |
| **Timeout** | default (5m) |

## What it verifies

Confirms that data volumes are encrypted at rest. On Linux this checks for LUKS-encrypted partitions; on Windows it checks BitLocker status. Optionally runs a custom command to verify Transparent Data Encryption (TDE) for database engines. This satisfies HIPAA and PCI DSS requirements for protecting stored data.

## Parameters

| Parameter | Type | Default | Description |
|-----------|------|---------|-------------|
| `volumes` | array of strings | *(auto-discover)* | List of volume paths or device names to check (e.g., `["/dev/sda1", "/dev/mapper/data"]` on Linux, `["C:", "D:"]` on Windows). If empty, Linux auto-discovers all block devices via `lsblk` and Windows defaults to `C:`. |
| `tde_check_command` | string | *(none)* | Shell command to verify Transparent Data Encryption. Executed as a subprocess. A zero exit code means TDE is active. Use this for database-level encryption checks (e.g., querying PostgreSQL `pg_catalog` or SQL Server `sys.dm_database_encryption_keys`). |

## Thresholds

This capability has no configurable thresholds.

## Verification logic

### Linux

1. **Volume discovery** -- If `volumes` is empty, run `lsblk --fs --json` and recursively walk the device tree. Partitions with `fstype` of `crypto_LUKS` are classified as encrypted. Partitions with a data filesystem (excluding `swap` and `vfat`) that are not LUKS are classified as unencrypted. If no LUKS partitions are found at all, record a failure.

2. **Configured volume check** -- If `volumes` is populated, check each volume individually:
   - Run `cryptsetup status <volume>` and look for `"is active"` in the output. If active, extract the cipher name and key size from the status output.
   - If `cryptsetup status` fails, fall back to `lsblk --noheadings -o FSTYPE <volume>` and check whether the filesystem type is `crypto_LUKS`.
   - Volumes that are neither active LUKS nor have `crypto_LUKS` as their filesystem type are recorded as failures.

3. **TDE check** -- If `tde_check_command` is set, execute it as a subprocess. A successful exit (exit code 0) means TDE is active. Failure is recorded as an issue.

### Windows

1. **BitLocker status** -- For each drive in `volumes` (default: `C:`), run `manage-bde -status <drive>` and extract:
   - **Protection status** -- Whether BitLocker protection is `On`. If off, record a failure.
   - **Encryption percentage** -- Must be 100%. Partially encrypted drives are recorded as failures.
   - **Encryption method** -- Recorded as evidence (e.g., `XTS-AES 256`).
   - **Key protectors** -- Count of key protectors configured.
   - **Recovery key** -- Check for the presence of a `Numerical Password` or `External Key` protector. If no recovery key protector is found, record a failure.

2. **TDE check** -- Same as Linux: if `tde_check_command` is set, execute it and check the exit code.

### Result

- **Pass** -- All volumes are encrypted (and TDE check passed, if configured).
- **Fail** -- Any volume is unencrypted, BitLocker protection is off, encryption is incomplete, or TDE check failed.
- **Skipped** -- Platform is not Linux or Windows.

## Example config

Auto-discover all volumes on Linux:

```yaml
capabilities:
  encryption_at_rest:
    schedule: "daily"
```

Check specific Linux volumes:

```yaml
capabilities:
  encryption_at_rest:
    schedule: "daily"
    params:
      volumes:
        - "/dev/sda2"
        - "/dev/mapper/data-root"
```

Windows with multiple drives and a SQL Server TDE check:

```yaml
capabilities:
  encryption_at_rest:
    schedule: "daily"
    params:
      volumes:
        - "C:"
        - "D:"
      tde_check_command: >-
        sqlcmd -Q "SELECT db_name(database_id), encryption_state
        FROM sys.dm_database_encryption_keys
        WHERE encryption_state = 3"
```

Linux with a PostgreSQL TDE check:

```yaml
capabilities:
  encryption_at_rest:
    schedule: "daily"
    params:
      volumes:
        - "/dev/sda2"
      tde_check_command: >-
        sudo -u postgres psql -tAc
        "SELECT count(*) FROM pg_catalog.pg_tablespace
        WHERE spcname != 'pg_global'"
```
---
*Confidential -- Alexander4, LLC. Not for redistribution. See [documentation-license](../../legal/documentation-license.md).*
