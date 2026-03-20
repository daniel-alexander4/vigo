
# network_interface

Manages interface state and description on network devices over SSH. Supports enabling, disabling, describing, and removing interfaces across Cisco IOS, NX-OS, IOS-XR, Arista EOS, and Juniper Junos.

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
| `interface` | Yes | -- | Interface name (e.g., `GigabitEthernet0/1`, `Ethernet1`, `ge-0/0/1`) |
| `description` | No | -- | Interface description string |
| `state` | No | `present` | `present`, `up`, `down`, or `absent` |
| `host` | Auto | -- | Target IP/hostname (auto-injected) |
| `port` | Auto | `22` | SSH port (auto-injected) |
| `username` | Auto | -- | SSH user (auto-injected) |
| `credential` | Auto | -- | Password or SSH key (auto-injected) |
| `device_type` | Auto | -- | Device type (auto-injected from node config) |

### State Values

- **`present`** -- Ensure the interface exists with the specified description (if given). Does not change admin status.
- **`up`** -- Ensure the interface exists and is administratively enabled (`no shutdown` / `delete disable`).
- **`down`** -- Ensure the interface exists and is administratively disabled (`shutdown` / `set disable`).
- **`absent`** -- Remove the interface configuration entirely.

## Idempotency

The executor reads the device's running configuration for the target interface and compares it against the desired state:

- On Cisco IOS/NX-OS/EOS/IOS-XR: runs `show running-config interface <name>` and checks for the `interface` line, `shutdown` presence, and description text.
- On Juniper Junos: runs `show configuration interfaces <name>` and checks for non-empty output, `disable` keyword, and description text.

No commands are sent if the current configuration already matches the desired state. Description drift is also detected -- if a `description` is specified but not present in the running config, the executor applies the change.

## Examples

### Cisco IOS -- Enable an interface with description

```yaml
resources:
  - name: uplink-port
    type: network_interface
    interface: "GigabitEthernet0/1"
    description: "Uplink to core"
    state: up
```

### Arista EOS -- Disable a server-facing port

```yaml
resources:
  - name: decom-server-port
    type: network_interface
    interface: "Ethernet1"
    state: down
```

### Cisco IOS-XR -- Configure a WAN interface

```yaml
resources:
  - name: wan-interface
    type: network_interface
    interface: "GigabitEthernet0/0/0/0"
    description: "WAN uplink"
    state: up
```

### Juniper Junos -- Remove an unused interface

```yaml
resources:
  - name: cleanup-ge3
    type: network_interface
    interface: "ge-0/0/3"
    state: absent
```

## Platform

Available on all platforms. Runs on the gateway envoy.
