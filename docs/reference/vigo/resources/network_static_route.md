
# network_static_route

Manages static routes on network devices over SSH. Supports adding and removing IPv4 static routes across Cisco IOS, NX-OS, IOS-XR, Arista EOS, and Juniper Junos.

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
| `network` | Yes | -- | Destination network in CIDR (`10.0.0.0/8`) or dotted-decimal (`10.0.0.0 255.0.0.0`) notation |
| `next_hop` | Yes | -- | Next-hop IP address or exit interface |
| `route_name` | No | -- | Route name or tag (used by IOS-XR and Junos) |
| `state` | Yes | -- | `present` to add, `absent` to remove |
| `host` | Auto | -- | Target IP/hostname (auto-injected) |
| `port` | Auto | `22` | SSH port (auto-injected) |
| `username` | Auto | -- | SSH user (auto-injected) |
| `credential` | Auto | -- | Password or SSH key (auto-injected) |
| `device_type` | Auto | -- | Device type (auto-injected from envoy config) |

### Network Format

The `network` parameter accepts three formats:

- **CIDR**: `10.0.0.0/8` -- automatically converted to dotted-decimal mask for Cisco IOS/NX-OS/EOS
- **Dotted-decimal**: `10.0.0.0 255.0.0.0` -- used as-is for Cisco IOS/NX-OS/EOS
- **Host route**: `10.0.0.1` -- treated as /32

IOS-XR and Junos use the CIDR notation directly in their configuration commands.

## Idempotency

The executor reads the device's running configuration and checks whether the static route (network + next-hop pair) exists:

- On Cisco IOS/EOS: runs `show running-config | include ip route <network>` and matches the full `ip route <net> <mask> <next_hop>` line.
- On Cisco NX-OS: runs `show running-config | include ip route` and matches the full route line.
- On Cisco IOS-XR: runs `show running-config router static` and checks for both the network address and next-hop.
- On Juniper Junos: runs `show configuration routing-options` and checks for both the network and next-hop.

No commands are sent if the route is already in the desired state.

## Examples

### Cisco IOS -- Add a default route

```yaml
resources:
  - name: default-route
    type: network_static_route
    network: "0.0.0.0/0"
    next_hop: "10.0.0.1"
    state: present
```

### Arista EOS -- Route to a remote site

```yaml
resources:
  - name: remote-site
    type: network_static_route
    network: "172.16.0.0/12"
    next_hop: "10.0.0.254"
    state: present
```

### Cisco IOS-XR -- Add a route with CIDR notation

```yaml
resources:
  - name: datacenter-route
    type: network_static_route
    network: "10.0.0.0/8"
    next_hop: "192.168.1.1"
    state: present
```

### Juniper Junos -- Remove an obsolete route

```yaml
resources:
  - name: remove-old-route
    type: network_static_route
    network: "192.168.100.0/24"
    next_hop: "10.0.0.1"
    state: absent
```

## Platform

Available on all platforms. Runs on the gateway envoy.
---
*Confidential -- Alexander4, LLC. Not for redistribution. See [license](../../../legal/license.md).*
