
# Swap

Enumerates active swap devices with type, size, usage, and priority.

## Trait Path
`swap`

## Fields

| Path | Type | Example | Description |
|------|------|---------|-------------|
| `swap.count` | integer | `1` | Number of active swap devices |
| `swap.devices` | array of objects | (see below) | List of swap device details |

Each device object contains:

| Path | Type | Example | Description |
|------|------|---------|-------------|
| `swap.devices[].path` | string | `"/dev/dm-1"` | Swap device path |
| `swap.devices[].type` | string | `"partition"` | Swap type (partition or file) |
| `swap.devices[].size_kb` | integer | `8388604` | Total size in kB |
| `swap.devices[].used_kb` | integer | `0` | Used space in kB |
| `swap.devices[].priority` | integer | `-2` | Swap priority |

## Collection Method
Parses `/proc/swaps`, skipping the header line. Each line provides the swap device path, type, size, usage, and priority in whitespace-separated columns.

## Using in When Expressions
```yaml
- name: create-swapfile
  type: exec
  command: /usr/local/bin/create-swap
  when: "os_family('debian')"
```

## Using in Templates
```yaml
- name: swap-report
  type: file
  target_path: /var/lib/vigo/swap.txt
  content: |
    Active swap devices: {{ .Traits.swap.count }}
```

## Platform
Linux
---
*Confidential -- Alexander4, LLC. Not for redistribution. See [documentation-license](../../legal/documentation-license.md).*
