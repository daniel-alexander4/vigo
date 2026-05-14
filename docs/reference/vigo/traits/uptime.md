
# Uptime

Reports system uptime in seconds and the last boot time as an RFC 3339 timestamp.

## Trait Path
`uptime`

## Fields

| Path | Type | Example | Description |
|------|------|---------|-------------|
| `uptime.seconds` | integer | `86400` | System uptime in seconds |
| `uptime.boot_time` | string | `"2026-03-12T14:30:00Z"` | Last boot time in RFC 3339 UTC format |

## Collection Method
Parses the first field of `/proc/uptime` (total seconds since boot). The boot time is computed by subtracting the uptime from the current Unix epoch time, then formatted as an RFC 3339 UTC string using `gmtime_r`.

## Using in When Expressions
```yaml
- name: post-reboot-check
  type: exec
  command: /usr/local/bin/post-reboot-verify
  when: "os_family('debian')"
```

## Using in Templates
```yaml
- name: uptime-report
  type: file
  target_path: /var/lib/vigo/uptime.txt
  content: |
    System booted: {{ .Traits.uptime.boot_time }}
    Uptime: {{ .Traits.uptime.seconds }} seconds
```

## Platform
Linux
---
*Confidential -- Alexander4, LLC. Not for redistribution. See [documentation-license](../../../legal/license.md).*
