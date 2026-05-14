
# Mountpoints (Windows)

Enumerates logical drives with filesystem type, total/used/free sizes, and usage percentage.

## Trait Path
`mountpoints`

## Fields

| Path | Type | Example | Description |
|------|------|---------|-------------|
| `mountpoints.mounts.<drive>.filesystem` | string | `"NTFS"` | Filesystem type |
| `mountpoints.mounts.<drive>.total_gb` | integer | `500` | Total size in GB |
| `mountpoints.mounts.<drive>.used_gb` | integer | `200` | Used space in GB |
| `mountpoints.mounts.<drive>.free_gb` | integer | `300` | Free space in GB |
| `mountpoints.mounts.<drive>.percent_used` | number | `40.0` | Usage percentage (0-100) |

The `<drive>` key is the drive letter (e.g., `C:`, `D:`).

## Collection Method
Runs `wmic logicaldisk get DeviceID,FileSystem,Size,FreeSpace,Description /value` and parses the output into per-record blocks. Drives with zero size (empty/unmounted) are skipped. Sizes are converted from bytes to GB by integer division.

## Using in When Expressions
```yaml
- name: cleanup-temp
  type: exec
  command: del /q C:\Windows\Temp\*
  when: "os_family('windows')"
```

## Using in Templates
```yaml
- name: disk-report
  type: file
  target_path: C:\ProgramData\vigo\disks.txt
  content: |
    {{ range $drive, $mount := .Traits.mountpoints.mounts }}{{ $drive }} {{ $mount.filesystem }}: {{ $mount.percent_used }}% used
    {{ end }}
```

## Platform
Windows
---
*Confidential -- Alexander4, LLC. Not for redistribution. See [documentation-license](../../../legal/license.md).*
