
# network_syslog

Manages syslog server configuration on Cisco and Juniper network devices over SSH.

SSH connection parameters are auto-injected by the gateway proxy.

## Parameters

| Parameter | Required | Default | Description |
|-----------|----------|---------|-------------|
| `device_type` | Yes | -- | `cisco_ios`, `cisco_nxos`, or `juniper_junos` |
| `syslog_host` | Yes | -- | Syslog server address |
| `syslog_port` | No | -- | Syslog server port |
| `facility` | No | -- | Syslog facility (Cisco only) |
| `state` | No | `present` | `present` to configure, `absent` to remove |
| `host` | Auto | -- | Target IP/hostname (auto-injected) |
| `port` | Auto | `22` | SSH port (auto-injected) |
| `username` | Auto | -- | SSH user (auto-injected) |
| `credential` | Auto | -- | Password or SSH key (auto-injected) |

## Examples

```yaml
resources:
  - name: central-syslog
    type: network_syslog
    device_type: cisco_ios
    syslog_host: "10.0.0.50"
    facility: local7
    state: present
```

## Platform

Available on all platforms. Runs on the gateway envoy.
---
*Confidential -- Alexander4, LLC. Not for redistribution. See [license](../../../legal/license.md).*
