# tmpfile

Manages systemd-tmpfiles configuration files in `/etc/tmpfiles.d/`. Used to declare temporary files, directories, and cleanup rules that systemd-tmpfiles enforces.

## Parameters

| Parameter | Required | Default | Description |
|-----------|----------|---------|-------------|
| `name` | Yes | -- | Configuration name (used as the filename: `/etc/tmpfiles.d/<name>.conf`). |
| `state` | No | `present` | Desired state: `present` or `absent`. |
| `path` | Yes (for present) | -- | Filesystem path managed by this tmpfile rule. |
| `entry_type` | No | `d` | tmpfiles.d entry type: `d` (directory), `f` (file), `D` (directory with cleanup), etc. |
| `mode` | No | `0755` | Permission mode. |
| `user` | No | `-` | Owner user. `-` means default. |
| `group` | No | `-` | Owner group. `-` means default. |
| `age` | No | `-` | Cleanup age (e.g., `10d`, `1w`). `-` means no age-based cleanup. |

## States

- **`present`** -- Ensure the tmpfiles.d config exists with the specified content. Runs `systemd-tmpfiles --create` to apply immediately.
- **`absent`** -- Remove the config file.

## Idempotency

Computes the SHA-256 hash of the desired configuration content and compares it against the existing file. Only writes and applies when the content differs.

## Examples

### Create a runtime directory

```yaml
resources:
  - name: myapp-runtime
    type: tmpfile
    name: myapp-runtime
    path: /run/myapp
    entry_type: d
    mode: "0755"
    user: myapp
    group: myapp
```

### Directory with age-based cleanup

```yaml
resources:
  - name: myapp-cache
    type: tmpfile
    name: myapp-cache
    path: /tmp/myapp-cache
    entry_type: D
    mode: "0755"
    age: "7d"
```

### Remove a tmpfile config

```yaml
resources:
  - name: old-tmpfile
    type: tmpfile
    name: old-tmpfile
    state: absent
```

## Platform

Linux only (systemd).

## Notes

- Configuration files are written to `/etc/tmpfiles.d/<name>.conf`.
- After writing, `systemd-tmpfiles --create <path>` is run to apply the configuration immediately.
- The file format follows the tmpfiles.d specification: `<type> <path> <mode> <user> <group> <age>`.
