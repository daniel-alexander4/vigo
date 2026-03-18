# Hardware (Windows)

Reports logical CPU count, total physical memory, and total disk size on Windows.

## Trait Path
`hardware`

## Fields

| Path | Type | Example | Description |
|------|------|---------|-------------|
| `hardware.cpu_count` | integer | `8` | Number of logical processors |
| `hardware.memory_mb` | integer | `16384` | Total physical memory in megabytes |
| `hardware.disk_gb` | integer | `512` | Total disk size across all fixed drives in GB |

## Collection Method
Uses three `wmic` commands:
- `wmic cpu get NumberOfLogicalProcessors /value` for CPU count
- `wmic os get TotalVisibleMemorySize /value` for total memory (returned in kB, converted to MB)
- `wmic logicaldisk where DriveType=3 get Size /value` for fixed disk sizes (summed and converted from bytes to GB)

## Using in When Expressions
```yaml
- name: install-monitoring
  type: package
  package: prometheus-windows-exporter
  when: "os_family('windows')"
```

## Using in Templates
```yaml
- name: hardware-info
  type: file
  target_path: C:\ProgramData\hobnob\hardware.txt
  content: |
    CPUs: {{ .Traits.hardware.cpu_count }}
    Memory: {{ .Traits.hardware.memory_mb }} MB
    Disk: {{ .Traits.hardware.disk_gb }} GB
```

## Platform
Windows
