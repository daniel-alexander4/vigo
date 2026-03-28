
# Network Segmentation

| | |
|---|---|
| **Machine name** | `network_segmentation` |
| **Controls** | PCI-DSS-1.3, NIST-AC-4 |
| **Platforms** | All |
| **Timeout** | 5m (default) |

## What it verifies

Proves that network segmentation controls are working by attempting TCP connections to hosts and ports that should be blocked, and confirming that allowed targets remain reachable. This is an active test -- it does not inspect firewall rules or ACLs, it verifies their effect on the wire.

## Parameters

| Parameter | Type | Default | Description |
|-----------|------|---------|-------------|
| `blocked_targets` | array of strings | `[]` | List of `host:port` targets that must be unreachable from this node. A successful TCP connection to any of these is a failure. |
| `allowed_targets` | array of strings | `[]` | List of `host:port` targets that must be reachable from this node. A failed TCP connection to any of these is a failure. |

## Thresholds

| Threshold | Type | Default | Description |
|-----------|------|---------|-------------|
| *(none)* | | | This capability has no configurable thresholds. |

## Verification logic

1. **Read target lists** -- loads `blocked_targets` and `allowed_targets` from params. If both lists are empty, the check is skipped with a message to configure targets.
2. **Test blocked targets** -- for each entry in `blocked_targets`, attempts a TCP connection with a 5-second timeout. DNS resolution is performed first; if resolution fails, the target is treated as unreachable. If the connection succeeds, the target is reachable and the test fails for that entry.
3. **Test allowed targets** -- for each entry in `allowed_targets`, attempts a TCP connection with a 5-second timeout. If the connection fails (timeout, refused, or DNS failure), the test fails for that entry.
4. **Summarize** -- records the total number of targets tested, how many passed, and how many failed.

### Result status

- **Pass** -- all blocked targets are unreachable and all allowed targets are reachable.
- **Fail** -- one or more targets did not match their expected state.
- **Skipped** -- no targets configured.

## Example config

```yaml
capabilities:
  network_segmentation:
    enabled: true
    params:
      blocked_targets:
        - "10.0.50.1:3306"    # DB server -- should not be reachable from web tier
        - "10.0.50.1:5432"    # Postgres -- same
        - "192.168.1.1:22"    # Management network SSH
      allowed_targets:
        - "10.0.10.1:443"     # API gateway -- must be reachable
        - "10.0.10.5:8443"    # Vigo server
```
---
*Confidential -- Alexander4, LLC. Not for redistribution. See [documentation-license](../../legal/documentation-license.md).*
