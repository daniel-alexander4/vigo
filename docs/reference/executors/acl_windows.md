# acl (Windows)

Manages NTFS file and directory access control lists (ACLs) idempotently via PowerShell `Get-Acl`/`Set-Acl`. Grants or removes access rules for specific identities (users, groups) with fine-grained control over rights, inheritance, and access type.

## Parameters

| Parameter | Required | Default | Description |
|-----------|----------|---------|-------------|
| `path` | Yes | -- | File or directory path to manage ACLs on. |
| `identity` | Yes | -- | User or group identity (e.g., `BUILTIN\Administrators`, `IIS_IUSRS`, `DOMAIN\AppService`). |
| `rights` | Yes | -- | Comma-separated .NET `FileSystemRights` values: `FullControl`, `Modify`, `ReadAndExecute`, `Read`, `Write`. |
| `type` | No | `Allow` | Access control type: `Allow` or `Deny`. |
| `inheritance` | No | auto | Inheritance flags. Defaults to `ContainerInherit,ObjectInherit` for directories, `None` for files. |
| `state` | No | `present` | `present` to ensure the ACL rule exists, `absent` to remove it. |

## States

- **`present`** -- Ensure the specified identity has an access rule with the given rights and type. If a matching rule already exists, no action is taken. Otherwise, the rule is added via `$acl.SetAccessRule()`.
- **`absent`** -- Remove the access rule matching the identity, rights, and type via `$acl.RemoveAccessRule()`.

## Idempotency

The executor queries the current ACL via `Get-Acl` and inspects the `Access` entries for a match on all three criteria: `IdentityReference`, `FileSystemRights`, and `AccessControlType`. Only when no matching entry is found does it make changes.

## Examples

### Grant full control to administrators

```yaml
resources:
  - name: admin-full-control
    type: acl
    path: "C:\\AppData\\MyApp"
    identity: "BUILTIN\\Administrators"
    rights: FullControl
```

### Grant read-execute to a service account

```yaml
resources:
  - name: app-service-access
    type: acl
    path: "C:\\inetpub\\wwwroot\\mysite"
    identity: IIS_IUSRS
    rights: ReadAndExecute
```

### Deny write access

```yaml
resources:
  - name: deny-guest-write
    type: acl
    path: "C:\\Shares\\Public"
    identity: "BUILTIN\\Guests"
    rights: Write
    type: Deny
```

### Remove an ACL entry

```yaml
resources:
  - name: remove-old-access
    type: acl
    path: "C:\\Data\\Archive"
    identity: "DOMAIN\\OldTeam"
    rights: Modify
    state: absent
```

### Set inheritance explicitly

```yaml
resources:
  - name: file-acl-no-inherit
    type: acl
    path: "C:\\Secrets\\config.json"
    identity: "NT AUTHORITY\\SYSTEM"
    rights: FullControl
    inheritance: None
```

### Web server content permissions

```yaml
resources:
  - name: web-admin-acl
    type: acl
    path: "C:\\inetpub\\wwwroot\\mysite"
    identity: "BUILTIN\\Administrators"
    rights: FullControl

  - name: web-iis-acl
    type: acl
    path: "C:\\inetpub\\wwwroot\\mysite"
    identity: IIS_IUSRS
    rights: ReadAndExecute

  - name: web-iusr-acl
    type: acl
    path: "C:\\inetpub\\wwwroot\\mysite"
    identity: IUSR
    rights: Read
```

## Platform

Windows only.

## Notes

- The `rights` parameter accepts .NET `FileSystemRights` enum values. Common values: `FullControl`, `Modify`, `ReadAndExecute`, `Read`, `Write`. Multiple rights can be combined with commas (e.g., `Read,Write`).
- Backslashes in identity names must be doubled in YAML (`BUILTIN\\Administrators`).
- The `inheritance` parameter maps to .NET `InheritanceFlags`. For directories, the default `ContainerInherit,ObjectInherit` propagates permissions to child objects and containers. For files, `None` is used since files have no children.
- The propagation flag is always set to `None` (no propagation-only rules). Inheritance flags control whether child objects inherit the rule.
- Matching is exact on all three fields (identity, rights, access type). If you change the rights for an identity, the old rule remains and a new one is added. Remove the old rule explicitly with `state: absent` if needed.
