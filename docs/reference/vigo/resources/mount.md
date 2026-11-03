
# mount

Manages mount points and `/etc/fstab` entries. Can mount/unmount filesystems and manage their fstab persistence.

## Parameters

| Parameter | Required | Default | Description |
|-----------|----------|---------|-------------|
| `path` | Yes | -- | Mount point path. |
| `mountpoint` | BSD | -- | Mount point path on the BSD family (FreeBSD/OpenBSD/NetBSD), where the executor reads `mountpoint` rather than `path`. Use `path` on Linux. |
| `state` | Yes | -- | Desired state: `mounted`, `present`, `unmounted`, or `absent`. |
| `device` | Yes (for mounted/present) | -- | Block device or remote filesystem to mount (e.g., `/dev/sdb1`, `nfs-server:/export`). |
| `fstype` | Yes (for mounted/present) | -- | Filesystem type (e.g., `ext4`, `xfs`, `nfs`). |
| `options` | No | `defaults` | Mount options (e.g., `defaults,noatime`). |
| `dump` | No | `0` | fstab dump field. |
| `pass` | No | `0` | fstab pass field. |

## States

- **`mounted`** -- Ensure the fstab entry exists AND the filesystem is mounted. Creates the mount point directory if needed.
- **`present`** -- Ensure the fstab entry exists but do not mount. Useful for defining mounts that are activated on reboot.
- **`unmounted`** -- Unmount the filesystem if mounted. Does not remove the fstab entry.
- **`absent`** -- Unmount the filesystem (if mounted) AND remove the fstab entry.

## Idempotency

Uses `findmnt` to check if the filesystem is currently mounted and reads `/etc/fstab` to check for existing entries. Compares device, fstype, and options for drift. Only modifies when changes are needed.

## Examples

### Mount a data volume

```yaml
resources:
  - name: /mnt/data
    type: mount
    path: /mnt/data
    device: /dev/sdb1
    fstype: ext4
    options: "defaults,noatime"
```

### fstab only (no mount)

```yaml
resources:
  - name: /mnt/backup
    type: mount
    path: /mnt/backup
    device: nfs-server:/export/backup
    fstype: nfs
    options: "defaults,nofail"
    state: present
```

### Unmount without removing fstab

```yaml
resources:
  - name: /mnt/data
    type: mount
    path: /mnt/data
    state: unmounted
```

### Remove mount completely

```yaml
resources:
  - name: /mnt/old-data
    type: mount
    path: /mnt/old-data
    state: absent
```

## Platform

Linux only.

## Notes

- The mount point directory is created automatically with `create_dir_all` when mounting.
- For `state: mounted`, the executor first ensures the fstab entry, then runs `mount <path>` (which uses the fstab entry).
- fstab entries are compared field-by-field and updated in place when they drift.
