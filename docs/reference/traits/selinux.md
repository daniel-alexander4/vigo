
# SELinux

Reports SELinux enforcement status, mode, and active policy.

## Trait Path
`selinux`

## Fields

| Path | Type | Example | Description |
|------|------|---------|-------------|
| `selinux.enabled` | boolean | `true` | Whether SELinux is enabled |
| `selinux.mode` | string | `"enforcing"` | Current mode: enforcing, permissive, or disabled |
| `selinux.policy` | string | `"targeted"` | Active SELinux policy name |

## Collection Method
First checks if `/etc/selinux/config` exists. If not, returns `enabled: false` with empty strings. Otherwise:
- Runs `getenforce` to get the current mode (enforcing/permissive/disabled)
- Runs `sestatus` and parses "Loaded policy name:" or "Policy from config file:" to extract the policy name

SELinux is considered enabled if the mode is neither empty nor `"disabled"`.

## Using in When Expressions
```yaml
- name: set-selinux-contexts
  type: exec
  command: restorecon -Rv /var/www
  when: "!os_family('debian')"
```

## Using in Templates
```yaml
- name: selinux-report
  type: file
  target_path: /var/lib/vigo/selinux.txt
  content: |
    SELinux: {{ .Traits.selinux.mode }}
    Policy: {{ .Traits.selinux.policy }}
```

## Platform
Linux
---
*Confidential -- Alexander4, LLC. Not for redistribution. See [documentation-license](../../legal/documentation-license.md).*
