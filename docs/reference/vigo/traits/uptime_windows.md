
# Uptime (Windows)

Reports system uptime in seconds and the last boot time on Windows.

## Trait Path
`uptime`

## Fields

| Path | Type | Example | Description |
|------|------|---------|-------------|
| `uptime.seconds` | integer | `86400` | System uptime in seconds |
| `uptime.boot_time` | string | `"20260312083045.123456+000"` | Last boot time in WMI datetime format |

## Collection Method
Runs `wmic os get LastBootUpTime /value` and parses the WMI datetime format (`yyyyMMddHHmmss.ffffff+UUU`). The boot epoch is computed from the datetime string, accounting for the UTC offset suffix. Uptime is calculated by subtracting the boot epoch from the current Unix time.

## Using in When Expressions
```yaml
- name: post-reboot-check
  type: exec
  command: C:\scripts\post-reboot.ps1
  when: "os_family('windows')"
```

## Using in Templates
```yaml
- name: uptime-report
  type: file
  target_path: C:\ProgramData\vigo\uptime.txt
  content: |
    Uptime: {{ .Traits.uptime.seconds }} seconds
```

## Platform
Windows
---
*Confidential -- Alexander4, LLC. Not for redistribution. See [license](../../../legal/license.md).*
