
# Filesystem

Reports per-mount-point disk usage including total, used, free bytes, and usage percentage.

## Trait Path
`filesystem`

## Fields

| Path | Type | Example | Description |
|------|------|---------|-------------|
| `filesystem.<mountpoint>.total` | integer | `107374182400` | Total size in bytes |
| `filesystem.<mountpoint>.used` | integer | `53687091200` | Used space in bytes |
| `filesystem.<mountpoint>.free` | integer | `53687091200` | Free space in bytes |
| `filesystem.<mountpoint>.percent` | number | `50.0` | Usage percentage (0-100) |

The top-level keys are mount point paths (e.g., `/`, `/data`).

## Collection Method
Iterates over physical mount points from `/proc/mounts` (excluding pseudo filesystems like tmpfs, proc, sysfs) and calls `statvfs` on each to compute total, free, and used bytes. The usage percentage is rounded to two decimal places.

## Using in When Expressions
```yaml
- name: disk-alert
  type: exec
  command: /usr/local/bin/alert-low-disk
  when: "os_family('debian')"
```

## Using in Templates
```yaml
- name: disk-report
  type: file
  target_path: /var/lib/vigo/disk-report.txt
  content: |
    Root filesystem usage: {{ (index .Traits.filesystem "/").percent }}%
```

## Platform
Linux
---
*Confidential -- Alexander4, LLC. Not for redistribution. See [documentation-license](../../legal/documentation-license.md).*
