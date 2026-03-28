
# cisco_interface

Manages interface configuration on Cisco IOS/NX-OS devices over SSH. Supports description, IP address, switchport mode, access VLAN, and shutdown state.

SSH connection parameters are auto-injected by the gateway proxy.

## Parameters

| Parameter | Required | Default | Description |
|-----------|----------|---------|-------------|
| `interface` | Yes | -- | Interface name (e.g., `GigabitEthernet0/1`) |
| `description` | No | -- | Interface description |
| `ip_address` | No | -- | IP address with mask (e.g., `10.0.1.1 255.255.255.0`) |
| `shutdown` | No | -- | `true` to shutdown, `false` to enable |
| `switchport_mode` | No | -- | `access` or `trunk` |
| `access_vlan` | No | -- | VLAN number for access mode ports |
| `host` | Auto | -- | Target IP/hostname (auto-injected) |
| `port` | Auto | `22` | SSH port (auto-injected) |
| `username` | Auto | -- | SSH user (auto-injected) |
| `credential` | Auto | -- | Password or SSH key (auto-injected) |

## Idempotency

1. Runs `show running-config interface {interface}` on the device
2. Compares each specified parameter against current config
3. Only pushes changes for parameters that differ

## Examples

### Configure a trunk port

```yaml
resources:
  - name: trunk-to-router
    type: cisco_interface
    interface: GigabitEthernet0/1
    description: "Uplink to edge-rtr01"
    switchport_mode: trunk
```

### Configure an access port

```yaml
resources:
  - name: server-port
    type: cisco_interface
    interface: GigabitEthernet0/24
    description: "Database Server"
    switchport_mode: access
    access_vlan: "200"
```

### Disable an unused port

```yaml
resources:
  - name: unused-port
    type: cisco_interface
    interface: GigabitEthernet0/48
    description: "UNUSED"
    shutdown: "true"
```

## Platform

This executor is available on all platforms. It runs on the gateway envoy and connects to the target Cisco device over SSH.
---
*Confidential -- Alexander4, LLC. Not for redistribution. See [documentation-license](../../legal/documentation-license.md).*
