# network_vlan

Manages VLANs on network devices over SSH. Supports creating, naming, and removing VLANs across Cisco IOS, NX-OS, IOS-XR, Arista EOS, and Juniper Junos.

SSH connection parameters (`host`, `username`, `credential`, `port`, `device_type`) are auto-injected by the gateway proxy.

## Supported Device Types

- `cisco_ios` -- Cisco IOS / IOS-XE
- `cisco_nxos` -- Cisco NX-OS
- `cisco_iosxr` -- Cisco IOS-XR
- `arista_eos` -- Arista EOS
- `juniper_junos` -- Juniper Junos

## Parameters

| Parameter | Required | Default | Description |
|-----------|----------|---------|-------------|
| `vlan` | Yes | -- | VLAN ID (1-4094) |
| `vlan_name` | No | -- | VLAN name. On Junos, defaults to `vlan<id>` if omitted. |
| `state` | No | `present` | `present` to create, `absent` to remove |
| `host` | Auto | -- | Target IP/hostname (auto-injected) |
| `port` | Auto | `22` | SSH port (auto-injected) |
| `username` | Auto | -- | SSH user (auto-injected) |
| `credential` | Auto | -- | Password or SSH key (auto-injected) |
| `device_type` | Auto | -- | Device type (auto-injected from node config) |

## Idempotency

The executor reads the device's running configuration and checks whether the specified VLAN ID exists:

- On Cisco IOS/NX-OS/EOS: runs `show running-config | include ^vlan <id>` and matches the `vlan <id>` line.
- On Cisco IOS-XR: runs `show running-config vlan <id>` and checks for the `vlan <id>` string.
- On Juniper Junos: runs `show configuration vlans` and checks for `vlan-id <id>`.

No commands are sent if the VLAN is already in the desired state. The VLAN ID is validated to be in the 1-4094 range before any SSH connection is made.

## Examples

### Cisco IOS -- Create a server VLAN

```yaml
resources:
  - name: server-vlan
    type: network_vlan
    vlan: "100"
    vlan_name: "servers"
    state: present
```

### Arista EOS -- Create a management VLAN

```yaml
resources:
  - name: mgmt-vlan
    type: network_vlan
    vlan: "200"
    vlan_name: "mgmt"
    state: present
```

### Cisco IOS-XR -- Remove an unused VLAN

```yaml
resources:
  - name: remove-old-vlan
    type: network_vlan
    vlan: "999"
    state: absent
```

### Juniper Junos -- Create a VLAN with custom name

```yaml
resources:
  - name: guest-vlan
    type: network_vlan
    vlan: "300"
    vlan_name: "guest-wifi"
    state: present
```

## Platform

Available on all platforms. Runs on the gateway envoy.
