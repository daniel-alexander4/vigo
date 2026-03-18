# Identity (Windows)

Reports the agent process's user identity and administrator privilege status on Windows.

## Trait Path
`identity`

## Fields

| Path | Type | Example | Description |
|------|------|---------|-------------|
| `identity.uid` | integer | `0` | Always 0 (no Unix UID on Windows) |
| `identity.gid` | integer | `0` | Always 0 (no Unix GID on Windows) |
| `identity.username` | string | `"Administrator"` | Current username (domain prefix stripped) |
| `identity.privileged` | boolean | `true` | Whether running with administrator privileges |

## Collection Method
Runs `whoami` to get the current username, stripping the `DOMAIN\` prefix if present. Administrator status is detected by running `whoami /groups` and checking for the well-known Administrators group SID (`S-1-5-32-544`).

## Using in When Expressions
```yaml
- name: set-permissions
  type: exec
  command: icacls C:\Secrets /grant Administrators:F
  when: "os_family('windows')"
```

## Using in Templates
```yaml
- name: agent-info
  type: file
  target_path: C:\ProgramData\hobnob\agent-info.txt
  content: |
    Running as: {{ .Traits.identity.username }}
    Admin: {{ .Traits.identity.privileged }}
```

## Platform
Windows
