
# logrotate

Manages logrotate configuration files in `/etc/logrotate.d/`.

## Parameters

| Parameter | Required | Default | Description |
|-----------|----------|---------|-------------|
| `name` | Yes | -- | Configuration name (used as the filename: `/etc/logrotate.d/<name>`). |
| `state` | No | `present` | Desired state: `present` or `absent`. |
| `path` | Yes (for present) | -- | Log file path or glob pattern (e.g., `/var/log/myapp/*.log`). |
| `frequency` | No | `weekly` | Rotation frequency: `daily`, `weekly`, `monthly`, `yearly`. |
| `rotate` | No | `4` | Number of rotated files to keep. |
| `compress` | No | `true` | Compress rotated files. |
| `delaycompress` | No | `true` | Delay compression of the most recent rotated file. |
| `missingok` | No | `true` | Do not error if the log file is missing. |
| `notifempty` | No | `true` | Do not rotate empty files. |
| `copytruncate` | No | `false` | Copy the log and truncate instead of moving (for apps that hold open file handles). |
| `size` | No | -- | Rotate based on file size (e.g., `100M`, `1G`). |
| `maxage` | No | -- | Maximum age in days for rotated files. |
| `create` | No | -- | Create replacement file after rotation (e.g., `0644 root root`). |
| `postrotate` | No | -- | Command to run after rotation (e.g., `systemctl reload nginx`). |

## States

- **`present`** -- Ensure the logrotate configuration exists with the specified directives.
- **`absent`** -- Remove the logrotate configuration file.

## Idempotency

Computes the SHA-256 hash of the desired configuration content and compares it against the existing file. Only writes when the content differs.

## Examples

### Basic

```yaml
resources:
  - name: myapp-logs
    type: logrotate
    name: myapp
    path: /var/log/myapp/*.log
    frequency: daily
    rotate: "7"
```

### Nginx with postrotate

```yaml
resources:
  - name: nginx-logs
    type: logrotate
    name: nginx
    path: /var/log/nginx/*.log
    frequency: daily
    rotate: "14"
    compress: "true"
    postrotate: "/usr/sbin/nginx -s reload"
```

---
*Confidential -- Alexander4, LLC. Not for redistribution. See [documentation-license](../../legal/documentation-license.md).*

---

### With copytruncate

```yaml
resources:
  - name: app-logs
    type: logrotate
    name: myapp
    path: /var/log/myapp.log
    copytruncate: "true"
    frequency: daily
    rotate: "30"
    size: "100M"
```

### Remove a config

```yaml
resources:
  - name: old-logrotate
    type: logrotate
    name: old-app
    state: absent
```

## Platform

Linux only.

## Notes

- Generates a standard logrotate configuration block with the `path { ... }` format.
- Boolean options default to common best practices: compress, delaycompress, missingok, and notifempty are on; copytruncate is off.
- The `postrotate` script is wrapped in `postrotate` / `endscript` directives.
---
*Confidential -- Alexander4, LLC. Not for redistribution. See [documentation-license](../../legal/documentation-license.md).*
