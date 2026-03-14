# Block Devices

Enumerates physical block devices with size, model, and read-only status.

## Trait Path
`blockdevices`

## Fields

| Path | Type | Example | Description |
|------|------|---------|-------------|
| `blockdevices.<name>.size_bytes` | integer | `512110190592` | Device size in bytes |
| `blockdevices.<name>.model` | string | `"Samsung SSD 970 EVO"` | Device model (physical devices only) |
| `blockdevices.<name>.readonly` | boolean | `false` | Whether the device is read-only |

The top-level keys are device names (e.g., `sda`, `nvme0n1`, `vda`).

## Collection Method
Reads entries from `/sys/block/`, skipping loop, ram, and device-mapper (`dm-`) devices. For each device:
- `size` is read from `/sys/block/<name>/size` (in 512-byte sectors, converted to bytes)
- `model` is read from `/sys/block/<name>/device/model` (only present for physical devices)
- `readonly` is read from `/sys/block/<name>/ro`

## Using in When Expressions
```yaml
- name: setup-raid
  type: exec
  command: /usr/local/bin/setup-raid
  when: "!is_container"
```

## Using in Templates
```yaml
- name: disk-inventory
  type: file
  targetpath: /var/lib/hobnob/disks.txt
  content: |
    {{ range $name, $dev := .Traits.blockdevices }}{{ $name }}: {{ $dev.size_bytes }} bytes{{ if $dev.model }} ({{ $dev.model }}){{ end }}
    {{ end }}
```

## Platform
Linux
