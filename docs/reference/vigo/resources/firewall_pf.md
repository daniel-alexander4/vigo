
# firewall_pf

Manages PF (Packet Filter) firewall rules and state on macOS and BSD systems.

## Parameters

| Parameter | Required | Default | Description |
|-----------|----------|---------|-------------|
| `name` | No | `pf-rule` | Resource name for identification. |
| `state` | Yes | -- | Desired state: `present` or `absent`. |
| `pf_rule` | No | -- | Raw PF rule string (e.g., `pass in on egress proto tcp to port 443`). |
| `enabled` | No | -- | Enable or disable PF entirely: `true` or `false`. Mutually exclusive with `pf_rule`. |

## States

- **`present`** -- Ensure the PF rule exists in the `vigo` anchor.
- **`absent`** -- Remove the PF rule by flushing the `vigo` anchor.

## Idempotency

Parses the output of `pfctl -s rules` to check if the rule already exists. Only adds or removes rules when needed. When managing PF state (`enabled`), checks `pfctl -s info` for the current status.

## Examples

### Enable PF

```yaml
resources:
  - name: Enable PF
    type: firewall
    enabled: "true"
    when: "os_family('bsd')"
```

### Add a rule

```yaml
resources:
  - name: Allow HTTPS
    type: firewall
    pf_rule: "pass in on egress proto tcp to port 443"
    when: "os_family('bsd')"
```

### Remove a rule

```yaml
resources:
  - name: Remove old rule
    type: firewall
    pf_rule: "pass in on egress proto tcp to port 8080"
    state: absent
    when: "os_family('bsd')"
```

## Platform

macOS and BSD (FreeBSD, OpenBSD). On Linux, the same `type: firewall` maps to the [firewall](firewall.md) executor (UFW). On Windows, it maps to [firewall_windows](firewall_windows.md).

## Notes

- Rules are managed via the `vigo` anchor (`pfctl -a vigo`).
- Flushing a rule (`state: absent`) flushes all rules in the `vigo` anchor.
- For complex rulesets, consider using a `file` resource for `/etc/pf.conf` with a `notify` to reload via `exec`.
- The `enabled` parameter controls PF itself and returns immediately without processing rules.
