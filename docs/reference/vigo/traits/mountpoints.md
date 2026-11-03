
# Mountpoints

Enumerates physical mount points with device, filesystem type, size, and inode usage.

## Trait Path
`mountpoints`

## Fields

| Path | Type | Example | Description |
|------|------|---------|-------------|
| `mountpoints.mounts.<path>.device` | string | `"/dev/sda1"` | Block device path |
| `mountpoints.mounts.<path>.fstype` | string | `"ext4"` | Filesystem type |
| `mountpoints.mounts.<path>.total_gb` | integer | `100` | Total size in GB |
| `mountpoints.mounts.<path>.used_gb` | integer | `45` | Used space in GB |
| `mountpoints.mounts.<path>.avail_gb` | integer | `50` | Available space in GB |
| `mountpoints.mounts.<path>.used_pct` | number | `47.5` | Disk usage percentage |
| `mountpoints.mounts.<path>.inode_pct` | number | `5.2` | Inode usage percentage |

The `<path>` key is the mount point (e.g., `/`, `/data`, `/home`).

## Collection Method
Parses `/proc/mounts` to identify physical mount points, filtering out pseudo filesystems (proc, sysfs, tmpfs, cgroup, overlay, etc.). For each mount, calls `statvfs` to retrieve block counts, fragment size, and inode counts. Sizes are converted to GB by integer division.

## Using in When Expressions
```yaml
- name: cleanup-logs
  type: exec
  command: /usr/local/bin/rotate-logs
  when: "os_family('debian')"
```

## Using in Templates
```yaml
- name: mount-report
  type: file
  target_path: /var/lib/vigo/mounts.txt
  content: |
    {{ range $path, $mount := .Traits.mountpoints.mounts }}{{ $path }}: {{ $mount.used_pct }}% used ({{ $mount.fstype }})
    {{ end }}
```

## Platform
Linux
