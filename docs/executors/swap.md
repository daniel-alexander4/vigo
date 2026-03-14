# swap

Manages swap files -- creates, activates, and persists swap files, or removes them.

## Parameters

| Parameter | Required | Default | Description |
|-----------|----------|---------|-------------|
| `path` | Yes | -- | Path to the swap file. Also accepts `name`. |
| `state` | No | `present` | Desired state: `present` or `absent`. |
| `size` | Yes (for new files) | -- | Swap file size in megabytes (e.g., `512`, `2048`). Only required when the file does not exist. |
| `priority` | No | -- | Swap priority (passed to `swapon -p`). |

## States

- **`present`** -- Ensure the swap file exists, is active, and has an fstab entry. Creates the file with `fallocate`, formats with `mkswap`, activates with `swapon`, and adds to fstab.
- **`absent`** -- Deactivate the swap, remove the fstab entry, and delete the file.

## Idempotency

Checks three conditions: (1) swap file exists on disk, (2) swap is active in `/proc/swaps`, (3) entry exists in `/etc/fstab`. Only acts on the conditions that need changing.

## Examples

### Basic

```yaml
resources:
  - name: /swapfile
    type: swap
    path: /swapfile
    size: "2048"
```

### With priority

```yaml
resources:
  - name: /mnt/fast-swap
    type: swap
    path: /mnt/fast-swap
    size: "1024"
    priority: "10"
```

### Remove swap

```yaml
resources:
  - name: /old-swapfile
    type: swap
    path: /old-swapfile
    state: absent
```

## Platform

Linux only.

## Notes

- The swap file is created with `fallocate -l <size>M`, set to mode 0600, then formatted with `mkswap`.
- The fstab entry uses the format: `<path> none swap sw 0 0`.
- When `state: absent`, the executor runs `swapoff`, removes the fstab entry, then deletes the file.
- The `size` parameter is only required when the swap file does not already exist.
