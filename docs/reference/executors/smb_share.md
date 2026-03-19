# smb_share

Manages SMB file shares idempotently via PowerShell. Creates, updates, and removes shares with granular access control for users and groups.

## Parameters

| Parameter | Required | Default | Description |
|-----------|----------|---------|-------------|
| `share` | Yes | -- | Share name (e.g., `Data`, `Public`). |
| `path` | When `state=present` | -- | Local directory path to share (e.g., `D:\Shares\Data`). |
| `state` | No | `present` | `present` to create/update the share, `absent` to remove it. |
| `description` | No | -- | Human-readable share description. |
| `full_access` | No | -- | Comma-separated users/groups granted full control (e.g., `Administrators,DOMAIN\Admins`). |
| `read_access` | No | -- | Comma-separated users/groups granted read-only access. |
| `change_access` | No | -- | Comma-separated users/groups granted change (read + write) access. |
| `no_access` | No | -- | Comma-separated users/groups explicitly denied all access. |

## States

- **`present`** -- Ensure the share exists with the specified path and access control. If the share exists but points to a different path, it is removed and recreated.
- **`absent`** -- Remove the share if it exists. The underlying directory is not deleted.

## Idempotency

The executor queries the share via `Get-SmbShare` before acting:

- **Share does not exist:** Creates it with `New-SmbShare` and the specified access parameters.
- **Share exists with correct path:** No action taken.
- **Share exists with different path:** Removes the share with `Remove-SmbShare -Force` and recreates it with the new path.
- **state=absent and share does not exist:** No action taken.

## Examples

### Create a shared data directory

```yaml
resources:
  - name: data-share
    type: smb_share
    share: Data
    path: "D:\\Shares\\Data"
    description: "Shared data directory"
    full_access: "BUILTIN\\Administrators"
    read_access: "DOMAIN\\DataReaders"
    change_access: "DOMAIN\\DataWriters"
```

### Public read-only share

```yaml
resources:
  - name: public-share
    type: smb_share
    share: Public
    path: "D:\\Shares\\Public"
    description: "Public read-only share"
    read_access: Everyone
    no_access: "BUILTIN\\Guests"
```

### Create a share with the backing directory

```yaml
resources:
  - name: share-dir
    type: directory
    target_path: "D:\\Shares\\Logs"

  - name: logs-share
    type: smb_share
    share: Logs
    path: "D:\\Shares\\Logs"
    full_access: "DOMAIN\\SysOps"
    read_access: "DOMAIN\\Developers"
    depends_on: [share-dir]
```

### Remove a share

```yaml
resources:
  - name: remove-legacy-share
    type: smb_share
    share: OldData
    state: absent
```

## Platform

Windows only.

## Notes

- Requires administrator privileges. The agent runs as SYSTEM when installed as a Windows Service.
- The `path` directory must exist before the share is created. Use a `directory` resource with `depends_on` to ensure it exists.
- If the share path changes, the share is removed and recreated. Clients connected to the old share will be disconnected.
- Access parameters are applied at share creation time. To update permissions on an existing share without changing the path, remove and recreate the share by changing any parameter.
- Domain users/groups use the `DOMAIN\User` format. Backslashes must be escaped in YAML (`DOMAIN\\User`).
