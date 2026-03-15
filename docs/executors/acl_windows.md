# acl (Windows)

Manages Windows file and directory access control lists (ACLs) idempotently via `icacls.exe`. Grants, removes, or enforces permissions for principals (users, groups, SIDs).

## Parameters

| Parameter | Required | Default | Description |
|-----------|----------|---------|-------------|
| `path` | Yes | -- | File or directory path to manage ACLs on. |
| `entries` | Yes (for present) | -- | Comma-separated ACL entries in `principal:access` format. |
| `inherit` | No | `true` | Enable (`true`) or disable (`false`) permission inheritance from parent. |
| `purge` | No | `false` | Remove all explicit ACL entries not in the `entries` list. |
| `state` | No | `present` | `present` to grant permissions, `absent` to remove them. |

## Access Levels

| Code | Meaning |
|------|---------|
| `F` | Full Control |
| `M` | Modify (read, write, execute, delete) |
| `RX` | Read and Execute |
| `R` | Read only |
| `W` | Write only |

## States

- **`present`** -- Ensure the specified principals have the specified access. Missing entries are added. Existing entries with different access levels are updated.
- **`absent`** -- Remove the specified principals from the ACL entirely.

## Examples

### Grant access to a directory

```yaml
resources:
  - name: app-data-permissions
    type: acl
    path: "C:\\AppData\\MyApp"
    entries: "BUILTIN\\Administrators:F,NT AUTHORITY\\SYSTEM:F,MyApp-Service:RX"
```

### Lock down a directory (purge unlisted entries)

```yaml
resources:
  - name: secure-config-dir
    type: acl
    path: "C:\\ProgramData\\MyApp\\config"
    entries: "BUILTIN\\Administrators:F,NT AUTHORITY\\SYSTEM:F"
    purge: "true"
    inherit: "false"
```

### Remove a principal's access

```yaml
resources:
  - name: remove-guest-access
    type: acl
    path: "C:\\Shares\\Internal"
    entries: "BUILTIN\\Guests:R"
    state: absent
```

### Web server content permissions

```yaml
resources:
  - name: web-content-acl
    type: acl
    path: "C:\\inetpub\\wwwroot\\mysite"
    entries: "BUILTIN\\Administrators:F,IIS_IUSRS:RX,IUSR:R"
    purge: "true"
    inherit: "false"
```

### Security hardening example

```yaml
resources:
  - name: restrict-system32-drivers
    type: acl
    path: "C:\\Windows\\System32\\drivers"
    entries: "BUILTIN\\Administrators:F,NT AUTHORITY\\SYSTEM:F,NT SERVICE\\TrustedInstaller:F"
    purge: "true"
    inherit: "false"

  - name: app-log-permissions
    type: acl
    path: "C:\\Logs\\MyApp"
    entries: "BUILTIN\\Administrators:F,NT AUTHORITY\\SYSTEM:F,MyApp-Service:M"
```

## Platform

Windows only.

## Notes

- Entries use `principal:access` format separated by commas. Backslashes in principal names must be doubled in YAML (`BUILTIN\\Administrators`).
- When `purge: true`, all explicit ACL entries not in the `entries` list are removed. Inherited entries (marked with `(I)` by icacls) are not affected by purge.
- When `inherit: false`, inheritance from the parent directory is disabled. Existing inherited entries are converted to explicit entries.
- Permissions are applied with `(OI)(CI)` flags (Object Inherit + Container Inherit) so they propagate to child objects in directories.
- The executor uses `icacls.exe` which is available on all supported Windows versions (Vista/Server 2008+).
