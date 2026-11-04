
# firewall_windows

Manages Windows Firewall rules via `netsh advfirewall firewall`.

## Parameters

| Parameter | Required | Default | Description |
|-----------|----------|---------|-------------|
| `name` | Yes | -- | Rule name. Set by the resource's own `name:`; a second `name:` key in the same resource is a duplicate-key error. |
| `port` | No | -- | Port number. At least one of `name` or `port` is required. |
| `state` | Yes | -- | Desired state: `present` or `absent`. |
| `protocol` | No | `tcp` | Protocol: `tcp` or `udp`. |
| `action` | No | `allow` | Firewall action: `allow` or `block`. |
| `direction` | No | `in` | Direction: `in` or `out`. |

## States

- **`present`** -- Ensure the firewall rule exists.
- **`absent`** -- Remove the firewall rule.

## Idempotency

Uses `netsh advfirewall firewall show rule name=<name>` to check if the rule exists. Only adds or removes when needed.

## Examples

### Allow HTTPS inbound

```yaml
resources:
  - name: Allow-HTTPS
    type: firewall
    port: "443"
    protocol: tcp
    action: allow
```

### Allow custom port

```yaml
resources:
  - name: Allow app port
    type: firewall
    port: "8080"
    protocol: tcp
    action: allow
    direction: in
```

### Remove a rule

```yaml
resources:
  - name: Old-App-Rule
    type: firewall
    state: absent
```

## Platform

Windows only. On Linux, `type: firewall` maps to the [firewall](firewall.md) executor (UFW).

## Notes

- When `name` is not provided, the rule name is auto-generated as `vigo-<port>-<protocol>-<direction>`.
- The rule is created with `netsh advfirewall firewall add rule` and deleted with `netsh advfirewall firewall delete rule`.
- Direction must be `in` or `out`. Other values produce an error.
