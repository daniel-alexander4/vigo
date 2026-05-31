
# Block Devices

Enumerates physical block devices with size, model, and read-only status.

## Trait Path
`blockdevices`

## Fields

| Path | Type | Example | Description |
|------|------|---------|-------------|
| `blockdevices.<name>.size_bytes` | integer | `512110190592` | Device size in bytes |
| `blockdevices.<name>.vendor` | string | `"Samsung"` | Device vendor (SCSI/SATA; omitted when absent) |
| `blockdevices.<name>.model` | string | `"Samsung SSD 970 EVO"` | Device model (physical devices only) |
| `blockdevices.<name>.serial` | string | `"S5GXNX0R123456"` | Device serial number (present on NVMe and many SCSI/SATA disks; omitted when sysfs doesn't expose it) |
| `blockdevices.<name>.firmware_rev` | string | `"2B2QEXM7"` | Device firmware revision (omitted when absent) |
| `blockdevices.<name>.readonly` | boolean | `false` | Whether the device is read-only |

The top-level keys are device names (e.g., `sda`, `nvme0n1`, `vda`).

## Collection Method
Reads entries from `/sys/block/`, skipping loop, ram, and device-mapper (`dm-`) devices. For each device:
- `size` is read from `/sys/block/<name>/size` (in 512-byte sectors, converted to bytes)
- `vendor`, `model`, `serial`, `firmware_rev` are read from `/sys/block/<name>/device/{vendor,model,serial,firmware_rev}` (physical devices only; each omitted when sysfs doesn't expose it — `vendor` is typical on SCSI/SATA, `serial` on NVMe and many SCSI/SATA disks)
- `readonly` is read from `/sys/block/<name>/ro`

Pure sysfs reads; no `lsblk`/`smartctl` subprocess.

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
  target_path: /var/lib/vigo/disks.txt
  content: |
    {{ range $name, $dev := .Traits.blockdevices }}{{ $name }}: {{ $dev.size_bytes }} bytes{{ if $dev.model }} ({{ $dev.model }}){{ end }}
    {{ end }}
```

## Platform
Linux
---
*Confidential -- Alexander4, LLC. Not for redistribution. See [license](../../../legal/license.md).*
