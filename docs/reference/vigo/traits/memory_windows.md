
# Memory (Windows)

Reports physical memory total, free, used, and available amounts with usage percentage.

## Trait Path
`memory`

## Fields

| Path | Type | Example | Description |
|------|------|---------|-------------|
| `memory.total_bytes` | integer | `17179869184` | Total physical memory in bytes |
| `memory.free_bytes` | integer | `8589934592` | Free physical memory in bytes |
| `memory.used_bytes` | integer | `8589934592` | Used physical memory in bytes |
| `memory.available_bytes` | integer | `8589934592` | Available memory in bytes (equals free on Windows) |
| `memory.percent_used` | number | `50.0` | Memory usage percentage (0-100) |

## Collection Method
Runs `wmic os get TotalVisibleMemorySize,FreePhysicalMemory /value` and parses the key=value output. Values are returned in kB by WMI and converted to bytes. On Windows, available memory equals free memory (no buffers/cached distinction like Linux).

## Using in When Expressions
```yaml
- name: install-monitoring
  type: package
  package: prometheus-windows-exporter
  when: "os_family('windows')"
```

## Using in Templates
```yaml
- name: memory-report
  type: file
  target_path: C:\ProgramData\vigo\memory.txt
  content: |
    Memory: {{ .Traits.memory.percent_used }}% used
```

## Platform
Windows
---
*Confidential -- Alexander4, LLC. Not for redistribution. See [license](../../../legal/license.md).*
