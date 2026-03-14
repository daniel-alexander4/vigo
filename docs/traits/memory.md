# Memory

Reports detailed memory and swap usage statistics.

## Trait Path
`memory`

## Fields

| Path | Type | Example | Description |
|------|------|---------|-------------|
| `memory.total` | integer | `16777216000` | Total physical memory in bytes |
| `memory.available` | integer | `8388608000` | Available memory in bytes |
| `memory.used` | integer | `8388608000` | Used memory in bytes (total - available) |
| `memory.free` | integer | `2097152000` | Free memory in bytes (excludes buffers/cache) |
| `memory.percent` | number | `50.0` | Memory usage percentage (0-100) |
| `memory.swap_total` | integer | `8388608000` | Total swap space in bytes |
| `memory.swap_used` | integer | `0` | Used swap space in bytes |
| `memory.swap_free` | integer | `8388608000` | Free swap space in bytes |
| `memory.swap_percent` | number | `0.0` | Swap usage percentage (0-100) |

## Collection Method
Parses `/proc/meminfo` for `MemTotal`, `MemFree`, `MemAvailable`, `Buffers`, `Cached`, `SwapTotal`, and `SwapFree`. All values are converted from kB to bytes. If `MemAvailable` is not present (old kernels), it is approximated as `MemFree + Buffers + Cached`. Used memory is calculated as `total - available`. Percentages are rounded to two decimal places.

## Using in When Expressions
```yaml
- name: install-swap
  type: exec
  command: /usr/local/bin/create-swapfile
  when: "has_enough_ram"
```

## Using in Templates
```yaml
- name: memory-report
  type: file
  targetpath: /var/lib/hobnob/memory.txt
  content: |
    Memory: {{ .Traits.memory.percent }}% used
    Swap: {{ .Traits.memory.swap_percent }}% used
```

## Platform
Linux
