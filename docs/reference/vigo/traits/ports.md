
# ports

The `ports` trait collector discovers listening TCP ports on the system.

## Trait Structure

```json
{
  "ports": {
    "listening": [22, 80, 443, 3306, 5432, 8080]
  }
}
```

## Fields

| Field | Type | Description |
|-------|------|-------------|
| `listening` | int[] | Sorted list of TCP ports in LISTEN state |

## Platform Support

| Platform | Method |
|----------|--------|
| Linux | Parses `/proc/net/tcp` and `/proc/net/tcp6` for state `0A` (LISTEN) |
| macOS | `sockstat -l` or `netstat -tln` fallback |
| FreeBSD | `sockstat -l` |
| OpenBSD | `netstat -tln` |
| NetBSD | `netstat -tln` |
| illumos | `sockstat -l` |
| Windows | Not supported (returns null) |

## When Expression Builtin

The `port_listening()` builtin checks whether a specific port is in the `ports.listening` array:

```yaml
- name: nginx-config
  type: file
  target_path: /etc/nginx/nginx.conf
  content: "..."
  when: "port_listening(80)"
```

See [When Expressions](../../../reference/writing-modules.md) for details.

## Classification

**Volatile** -- listening ports change as services start and stop. Collected every cycle.
---
*Confidential -- Alexander4, LLC. Not for redistribution. See [license](../../../legal/license.md).*
