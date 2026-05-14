
# network_ntp

Manages NTP server configuration on Cisco and Juniper network devices over SSH. Supports both Cisco IOS/NX-OS and Juniper Junos.

SSH connection parameters are auto-injected by the gateway proxy.

## Parameters

| Parameter | Required | Default | Description |
|-----------|----------|---------|-------------|
| `device_type` | Yes | -- | `cisco_ios`, `cisco_nxos`, or `juniper_junos` |
| `server` | Yes | -- | NTP server address |
| `state` | No | `present` | `present` to configure, `absent` to remove |
| `host` | Auto | -- | Target IP/hostname (auto-injected) |
| `port` | Auto | `22` | SSH port (auto-injected) |
| `username` | Auto | -- | SSH user (auto-injected) |
| `credential` | Auto | -- | Password or SSH key (auto-injected) |

## Examples

```yaml
resources:
  - name: ntp-primary
    type: network_ntp
    device_type: cisco_ios
    server: "10.0.0.1"
    state: present

  - name: ntp-secondary
    type: network_ntp
    device_type: cisco_ios
    server: "10.0.0.2"
    state: present
```

## Platform

Available on all platforms. Runs on the gateway envoy.
---
*Confidential -- Alexander4, LLC. Not for redistribution. See [license](../../../legal/license.md).*
