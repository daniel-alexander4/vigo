
# systemd_dropin

Manages systemd unit drop-in override files. Creates or removes files under `/etc/systemd/system/<unit>.d/` and automatically runs `systemctl daemon-reload` when changes are made.

## Parameters

| Parameter | Required | Default | Description |
|-----------|----------|---------|-------------|
| `unit` | Yes | -- | Systemd unit name (e.g., `nginx.service`, `docker.service`). |
| `dropin` | No | `override` | Drop-in file name (without `.conf` extension). |
| `content` | Yes (for present) | -- | Contents of the drop-in file. |
| `state` | No | `present` | Desired state: `present` or `absent`. |

## States

- **`present`** -- Ensure the drop-in file exists with the specified content. Creates the drop-in directory if needed.
- **`absent`** -- Remove the drop-in file. Cleans up the drop-in directory if empty.

## Idempotency

Reads the existing drop-in file and compares against the desired content. If they match, no action is taken. `systemctl daemon-reload` is only called when the file is actually created, modified, or removed.

## Examples

### Basic override

```yaml
resources:
  - name: docker-limits
    type: systemd_dropin
    unit: docker.service
    content: |
      [Service]
      LimitNOFILE=1048576
      LimitNPROC=infinity
```

### Named drop-in

```yaml
resources:
  - name: nginx-env
    type: systemd_dropin
    unit: nginx.service
    dropin: environment
    content: |
      [Service]
      Environment="NGINX_WORKER_PROCESSES=auto"
      Environment="NGINX_WORKER_CONNECTIONS=4096"
```

### Restart after override

```yaml
resources:
  - name: docker-limits
    type: systemd_dropin
    unit: docker.service
    content: |
      [Service]
      LimitNOFILE=1048576
    notify: restart-docker

  - name: restart-docker
    type: service
    service: docker
    state: restarted
    subscribes: docker-limits
```

### Remove a drop-in

```yaml
resources:
  - name: remove-old-override
    type: systemd_dropin
    unit: nginx.service
    dropin: old-tuning
    state: absent
```

### Multiple drop-ins for one unit

```yaml
resources:
  - name: app-limits
    type: systemd_dropin
    unit: myapp.service
    dropin: limits
    content: |
      [Service]
      LimitNOFILE=65536
      MemoryMax=2G

  - name: app-restart-policy
    type: systemd_dropin
    unit: myapp.service
    dropin: restart
    content: |
      [Service]
      Restart=on-failure
      RestartSec=5
```

## Platform

Linux only (systemd).

## Notes

- Drop-in files are written to `/etc/systemd/system/<unit>.d/<dropin>.conf`.
- The default drop-in name is `override`, matching the convention used by `systemctl edit`.
- `systemctl daemon-reload` runs automatically after any change -- you do not need a separate exec resource for this.
- When removing a drop-in with `state: absent`, the empty `.d` directory is cleaned up automatically.
- Drop-in file permissions are set to `0644` (readable by all, writable by root).
---
*Confidential -- Alexander4, LLC. Not for redistribution. See [license](../../../legal/license.md).*
