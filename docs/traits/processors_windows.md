# Processors (Windows)

Provides detailed CPU information including model, core counts, and clock speed on Windows.

## Trait Path
`processors`

## Fields

| Path | Type | Example | Description |
|------|------|---------|-------------|
| `processors.model` | string | `"Intel(R) Core(TM) i7-12700K"` | CPU model name |
| `processors.physical_cores` | integer | `12` | Number of physical CPU cores |
| `processors.logical_cores` | integer | `20` | Number of logical CPU cores |
| `processors.frequency_mhz` | integer | `3600` | Maximum clock speed in MHz |

## Collection Method
Runs `wmic cpu get Name,NumberOfCores,NumberOfLogicalProcessors,MaxClockSpeed /value` and parses the key=value output.

## Using in When Expressions
```yaml
- name: set-thread-pool
  type: file
  target_path: C:\ProgramData\app\config.yaml
  when: "os_family('windows')"
```

## Using in Templates
```yaml
- name: cpu-info
  type: file
  target_path: C:\ProgramData\hobnob\cpu.txt
  content: |
    CPU: {{ .Traits.processors.model }}
    Cores: {{ .Traits.processors.physical_cores }} physical, {{ .Traits.processors.logical_cores }} logical
    Speed: {{ .Traits.processors.frequency_mhz }} MHz
```

## Platform
Windows
