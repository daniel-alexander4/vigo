
# cisco_vlan

Manages VLANs on Cisco IOS/NX-OS switches over SSH. Checks the VLAN table before acting and only pushes configuration when drift is detected.

SSH connection parameters are auto-injected by the gateway proxy.

## Parameters

| Parameter | Required | Default | Description |
|-----------|----------|---------|-------------|
| `vlan_id` | Yes | -- | VLAN number (1-4094) |
| `name` | Yes | -- | VLAN name |
| `state` | No | `present` | `present` to create/update, `absent` to remove |
| `host` | Auto | -- | Target IP/hostname (auto-injected) |
| `port` | Auto | `22` | SSH port (auto-injected) |
| `username` | Auto | -- | SSH user (auto-injected) |
| `credential` | Auto | -- | Password or SSH key (auto-injected) |

## Idempotency

1. Runs `show vlan brief` on the device
2. Parses the VLAN table for the target `vlan_id`
3. If `state: present` and the VLAN exists with the correct name, no change
4. If `state: absent` and the VLAN does not exist, no change

## Examples

### Create a VLAN

```yaml
resources:
  - name: mgmt-vlan
    type: cisco_vlan
    vlan_id: "100"
    name: Management
    state: present
```

### Remove a VLAN

```yaml
resources:
  - name: remove-old-vlan
    type: cisco_vlan
    vlan_id: "999"
    name: Deprecated
    state: absent
```

## Platform

This executor is available on all platforms. It runs on the gateway envoy and connects to the target Cisco device over SSH.
---
*Confidential -- Alexander4, LLC. Not for redistribution. See [documentation-license](../../../legal/license.md).*
