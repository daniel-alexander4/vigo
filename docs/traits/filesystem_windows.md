# Filesystem (Windows)

Reports per-drive disk usage including total, used, free bytes, and usage percentage.

## Trait Path
`filesystem`

## Fields

| Path | Type | Example | Description |
|------|------|---------|-------------|
| `filesystem.<drive>.total_bytes` | integer | `512110190592` | Total drive size in bytes |
| `filesystem.<drive>.used_bytes` | integer | `256055095296` | Used space in bytes |
| `filesystem.<drive>.free_bytes` | integer | `256055095296` | Free space in bytes |
| `filesystem.<drive>.percent_used` | number | `50.0` | Usage percentage (0-100) |

The top-level keys are drive letters (e.g., `C:`, `D:`).

## Collection Method
Runs `wmic logicaldisk where DriveType=3 get DeviceID,Size,FreeSpace /value` to enumerate fixed drives. Parses the output into per-record blocks and computes used bytes and usage percentage. Only fixed drives (DriveType=3) are included.

## Using in When Expressions
```yaml
- name: disk-cleanup
  type: exec
  command: cleanmgr /d C /sagerun:1
  when: "os_family('windows')"
```

## Using in Templates
```yaml
- name: disk-report
  type: file
  targetpath: C:\ProgramData\hobnob\disks.txt
  content: |
    {{ range $drive, $info := .Traits.filesystem }}{{ $drive }}: {{ $info.percent_used }}% used
    {{ end }}
```

## Platform
Windows
