
# Swap (Windows)

Reports Windows pagefile total and current usage.

## Trait Path
`swap`

## Fields

| Path | Type | Example | Description |
|------|------|---------|-------------|
| `swap.total_bytes` | integer | `8589934592` | Total pagefile size in bytes |
| `swap.used_bytes` | integer | `1073741824` | Current pagefile usage in bytes |

## Collection Method
Runs `wmic pagefile get AllocatedBaseSize,CurrentUsage /value`. Both values are returned in MB by WMI and converted to bytes (multiplied by 1024 * 1024).

## Using in When Expressions
```yaml
- name: configure-pagefile
  type: exec
  command: wmic computersystem set AutomaticManagedPagefile=True
  when: "os_family('windows')"
```

## Using in Templates
```yaml
- name: swap-report
  type: file
  target_path: C:\ProgramData\vigo\swap.txt
  content: |
    Pagefile total: {{ .Traits.swap.total_bytes }} bytes
    Pagefile used: {{ .Traits.swap.used_bytes }} bytes
```

## Platform
Windows
---
*Confidential -- Alexander4, LLC. Not for redistribution. See [documentation-license](../../legal/documentation-license.md).*
