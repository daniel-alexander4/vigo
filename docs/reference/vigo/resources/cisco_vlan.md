
# cisco_vlan

Manages VLANs on Cisco IOS/NX-OS switches over SSH. Checks the VLAN table before acting and only pushes configuration when drift is detected.

SSH connection parameters are auto-injected by the gateway proxy.

## Parameters

| Parameter | Required | Default | Description |
|-----------|----------|---------|-------------|
| `vlan_id` | Yes | -- | VLAN number (1-4094) |
| `name` | Yes | -- | VLAN name. Set by the resource's own `name:`; a second `name:` key in the same resource is a duplicate-key error. |
| `state` | Yes | -- | `present` to create/update, `absent` to remove |
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
  - name: Management
    type: cisco_vlan
    vlan_id: "100"
    state: present
```

### Remove a VLAN

```yaml
resources:
  - name: Deprecated
    type: cisco_vlan
    vlan_id: "999"
    state: absent
```

## Platform

This executor is available on all platforms. It runs on the gateway envoy and connects to the target Cisco device over SSH.
