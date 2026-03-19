# host

Manages entries in `/etc/hosts` (or the system hosts file) idempotently.

## Parameters

| Parameter | Required | Default | Description |
|-----------|----------|---------|-------------|
| `hostname` | Yes | -- | The hostname to manage. |
| `state` | No | `present` | Desired state: `present` or `absent`. |
| `ip` | Yes (for present) | -- | IP address for the hosts entry. |
| `aliases` | No | -- | Space-separated list of hostname aliases. |

## States

- **`present`** -- Ensure an entry exists mapping the IP to the hostname (and optional aliases).
- **`absent`** -- Remove any entry for the hostname.

## Idempotency

Reads `/etc/hosts`, searches for an existing entry matching the hostname, and compares the IP and aliases. Only modifies when there is drift.

## Examples

### Basic

```yaml
resources:
  - name: db.internal
    type: host
    hostname: db.internal
    ip: 10.0.1.50
```

### With aliases

```yaml
resources:
  - name: app.internal
    type: host
    hostname: app.internal
    ip: 10.0.1.10
    aliases: "app app-server"
```

### Remove a host entry

```yaml
resources:
  - name: old-server.internal
    type: host
    hostname: old-server.internal
    state: absent
```

## Platform

Cross-platform.

## Notes

- Entries are appended to the hosts file if not found, or updated in place if the hostname exists but with a different IP/aliases.
- Comment lines and blank lines are preserved.
