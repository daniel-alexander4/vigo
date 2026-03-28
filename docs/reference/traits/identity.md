
# Identity

Reports the agent process's user identity, group, and privilege level.

## Trait Path
`identity`

## Fields

| Path | Type | Example | Description |
|------|------|---------|-------------|
| `identity.uid` | integer | `0` | Numeric user ID |
| `identity.gid` | integer | `0` | Numeric group ID |
| `identity.user` | string | `"root"` | Username of the running process |
| `identity.group` | string | `"root"` | Primary group name of the running process |
| `identity.privileged` | boolean | `true` | Whether the effective UID is 0 (root) |

## Collection Method
Uses `nix::unistd` to call `getuid()`, `getgid()`, and `geteuid()`. Resolves the numeric UID and GID to names via `User::from_uid` and `Group::from_gid`. The `privileged` flag is true when the effective UID is 0.

## Using in When Expressions
```yaml
- name: set-permissions
  type: exec
  command: chmod 600 /etc/ssl/private/server.key
  when: "os_family('debian')"
```

## Using in Templates
```yaml
- name: agent-info
  type: file
  target_path: /var/lib/vigo/agent-info.txt
  content: |
    Running as: {{ .Traits.identity.user }} (uid={{ .Traits.identity.uid }})
    Privileged: {{ .Traits.identity.privileged }}
```

## Platform
Linux
---
*Confidential -- Alexander4, LLC. Not for redistribution. See [documentation-license](../../legal/documentation-license.md).*
