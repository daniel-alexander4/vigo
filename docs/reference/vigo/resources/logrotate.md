
# logrotate

Manages logrotate configuration files in `/etc/logrotate.d/`.

## Parameters

| Parameter | Required | Default | Description |
|-----------|----------|---------|-------------|
| `name` | Yes | -- | The resource's own `name:` -- used as the filename (`/etc/logrotate.d/<name>`). Do not add a second `name:` key. |
| `state` | Yes | -- | Desired state: `present` or `absent`. |
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

## Choosing a path

Declare one resource per application whose logs nothing else already owns.

**Never use a catch-all pattern** such as `/var/log/*.log` or `/var/log/*/*.log`.
Distro packages ship their own configs in `/etc/logrotate.d/` (`apt`,
`alternatives`, `rsyslog`, ...), and logrotate treats a log claimed by two
configs as a fatal `duplicate log entry`. It then skips the **entire file**
containing the duplicate -- so the catch-all silently rotates nothing, and
`logrotate.service` exits non-zero on every run.

To find what is genuinely unowned on a host:

```bash
for f in /var/log/*.log /var/log/*/*.log; do
  grep -qlF -- "$f" /etc/logrotate.d/* 2>/dev/null || echo "unowned: $f"
done
```

Leave these to the tools that already manage them: `/var/log/audit/*` (auditd
rotates via `max_log_file`/`num_logs`; rotating it externally breaks the audit
trail) and `/var/log/Xorg.*.log` (Xorg keeps its own generations).

## Examples

### Basic

```yaml
resources:
  - name: myapp
    type: logrotate
    state: present
    path: /var/log/myapp/*.log
    frequency: daily
    rotate: "7"
```

### Nginx with postrotate

```yaml
resources:
  - name: nginx
    type: logrotate
    state: present
    path: /var/log/nginx/*.log
    frequency: daily
    rotate: "14"
    compress: "true"
    postrotate: "/usr/sbin/nginx -s reload"
```

### With copytruncate

Use `copytruncate` for a daemon that holds its log open and never reopens it.
A `create`-style rotate would leave the process writing to the unlinked inode,
so the new file would stay empty forever.

```yaml
resources:
  - name: myapp-service
    type: logrotate
    state: present
    path: /var/log/myapp.log
    copytruncate: "true"
    frequency: daily
    rotate: "30"
    size: "100M"
```

### Remove a config

```yaml
resources:
  - name: old-app
    type: logrotate
    state: absent
```

## Platform

Linux only.

## Notes

- Generates a standard logrotate configuration block with the `path { ... }` format.
- Boolean options default to common best practices: compress, delaycompress, missingok, and notifempty are on; copytruncate is off.
- The `postrotate` script is wrapped in `postrotate` / `endscript` directives.
---
*Confidential -- Alexander4, LLC. Not for redistribution. See [license](../../../legal/license.md).*
