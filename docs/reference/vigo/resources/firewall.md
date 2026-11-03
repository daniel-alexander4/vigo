
# firewall

Manages UFW (Uncomplicated Firewall) rules idempotently.

## Parameters

| Parameter | Required | Default | Description |
|-----------|----------|---------|-------------|
| `port` | Yes | -- | Port number or range. |
| `state` | Yes | -- | Desired state: `present` or `absent`. |
| `proto` | No | `any` | Protocol: `tcp`, `udp`, or `any`. |
| `action` | Yes (for present) | -- | Firewall action: `allow` or `deny`. |
| `from` | No | `any` | Source address or `any`. |
| `direction` | No | `in` | Direction: `in` or `out`. |
| `comment` | No | -- | Comment to attach to the rule. |

## States

- **`present`** -- Ensure the firewall rule exists.
- **`absent`** -- Remove the firewall rule.

## Idempotency

Parses the output of `ufw status verbose` to check if a matching rule already exists. Only adds or removes rules when needed.

## Examples

### Allow HTTPS

```yaml
resources:
  - name: Allow HTTPS
    type: firewall
    port: "443"
    proto: tcp
    action: allow
```

### Allow from specific subnet

```yaml
resources:
  - name: Allow SSH from office
    type: firewall
    port: "22"
    proto: tcp
    action: allow
    from: 10.0.0.0/8
    comment: "Office SSH access"
```

### Deny a port

```yaml
resources:
  - name: Block telnet
    type: firewall
    port: "23"
    action: deny
```

### Remove a rule

```yaml
resources:
  - name: Remove old rule
    type: firewall
    port: "8080"
    proto: tcp
    action: allow
    state: absent
```

### With when

```yaml
resources:
  - name: Allow HTTP
    type: firewall
    port: "80"
    proto: tcp
    action: allow
    when: "!is_container"
```

## Platform

Linux only (UFW). On Windows, the same `type: firewall` maps to the [firewall_windows](firewall_windows.md) executor.

## Notes

- UFW must be installed and active on the system.
- The `action` parameter is required for `state: present` but also used for `state: absent` to identify the exact rule to delete.
- Basic inbound rules use simple UFW syntax (`ufw allow 22/tcp`). When `from`, `direction`, or `comment` is specified, the executor switches to verbose syntax (`ufw allow proto ... from ... to any port ...`).
