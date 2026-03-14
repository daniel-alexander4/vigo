# service

Manages systemd services -- start, stop, restart, reload, and enable/disable at boot.

## Parameters

| Parameter | Required | Default | Description |
|-----------|----------|---------|-------------|
| `service` | Yes | -- | Service unit name (e.g., `nginx`). |
| `state` | No | `running` | Desired state: `running`, `stopped`, `restarted`, or `reloaded`. |
| `enabled` | No | -- | Boot-time enable: `true` to enable, `false` to disable. Empty means do not change. |
| `restart_cmd` | No | -- | Custom command to run instead of `systemctl restart` when `state: restarted`. |
| `reload_cmd` | No | -- | Custom command to run instead of `systemctl reload` when `state: reloaded`. |

## States

- **`running`** -- Ensure the service is active. If stopped, start it.
- **`stopped`** -- Ensure the service is inactive. If running, stop it.
- **`restarted`** -- Restart the service unconditionally. Always reports `changed: true`.
- **`reloaded`** -- Reload the service configuration. Always reports `changed: true`.

## Idempotency

Uses `systemctl is-active` to check the current state and `systemctl is-enabled` for boot configuration. For `running` and `stopped` states, only acts when the current state differs from desired. `restarted` and `reloaded` always trigger.

## Examples

### Basic

```yaml
resources:
  - name: Ensure nginx running
    type: service
    service: nginx
    state: running
    enabled: "true"
```

### Stop and disable

```yaml
resources:
  - name: Disable cups
    type: service
    service: cups
    state: stopped
    enabled: "false"
```

### As a notify target

```yaml
resources:
  - name: /etc/nginx/nginx.conf
    type: file
    targetpath: /etc/nginx/nginx.conf
    content: |
      worker_processes auto;
    notify: restart-nginx

  - name: restart-nginx
    type: service
    service: nginx
    state: restarted
    subscribes: /etc/nginx/nginx.conf
```

### Custom restart command

```yaml
resources:
  - name: reload-nginx
    type: service
    service: nginx
    state: restarted
    restart_cmd: "nginx -s reload"
```

### With when

```yaml
resources:
  - name: Start Docker
    type: service
    service: docker
    state: running
    enabled: "true"
    when: "!is_container"
```

## Platform

Linux only (systemd). On Windows, the same `type: service` maps to the [service_windows](service_windows.md) executor.

## Notes

- Uses `systemctl start`, `systemctl stop`, `systemctl restart`, and `systemctl reload`.
- The `enabled` parameter uses `systemctl enable` / `systemctl disable`.
- `restarted` and `reloaded` always apply (they are inherently non-idempotent).
