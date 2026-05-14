
# network_snmp

Manages SNMP community configuration on Cisco and Juniper network devices over SSH.

SSH connection parameters are auto-injected by the gateway proxy.

## Parameters

| Parameter | Required | Default | Description |
|-----------|----------|---------|-------------|
| `device_type` | Yes | -- | `cisco_ios`, `cisco_nxos`, or `juniper_junos` |
| `community` | Yes | -- | SNMP community string |
| `permission` | No | `RO` | `RO` (read-only) or `RW` (read-write) |
| `acl` | No | -- | Access list name to restrict access (Cisco only) |
| `state` | No | `present` | `present` to configure, `absent` to remove |
| `host` | Auto | -- | Target IP/hostname (auto-injected) |
| `port` | Auto | `22` | SSH port (auto-injected) |
| `username` | Auto | -- | SSH user (auto-injected) |
| `credential` | Auto | -- | Password or SSH key (auto-injected) |

## Examples

```yaml
resources:
  - name: monitoring-snmp
    type: network_snmp
    device_type: cisco_ios
    community: "monitoring-ro"
    permission: RO
    acl: MGMT-ACCESS
    state: present
```

## Platform

Available on all platforms. Runs on the gateway envoy.
---
*Confidential -- Alexander4, LLC. Not for redistribution. See [license](../../../legal/license.md).*
