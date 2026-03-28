
# Hardware

Reports logical CPU count, total physical memory, and disk sizes per mount point.

## Trait Path
`hardware`

## Fields

| Path | Type | Example | Description |
|------|------|---------|-------------|
| `hardware.cpu_count` | integer | `8` | Number of logical CPUs |
| `hardware.memory_mb` | integer | `16384` | Total physical memory in megabytes |
| `hardware.disk_gb` | object | `{"/": 100, "/data": 500}` | Total disk size in GB per mount point |

## Collection Method
CPU count is determined by counting `processor` entries in `/proc/cpuinfo`. Total memory is parsed from the `MemTotal` line in `/proc/meminfo` and converted from kB to MB. Disk sizes are gathered by calling `statvfs` on each physical mount point (from `/proc/mounts`, excluding pseudo filesystems) and converting block counts to gigabytes.

## Using in When Expressions
```yaml
- name: install-monitoring
  type: package
  package: prometheus-node-exporter
  when: "has_enough_ram"
```

## Using in Templates
```yaml
- name: system-info
  type: file
  target_path: /etc/system-info
  content: |
    CPUs: {{ .Traits.hardware.cpu_count }}
    Memory: {{ .Traits.hardware.memory_mb }} MB
```

## Platform
Linux
---
*Confidential -- Alexander4, LLC. Not for redistribution. See [documentation-license](../../legal/documentation-license.md).*
