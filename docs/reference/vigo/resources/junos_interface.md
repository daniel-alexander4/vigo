
# junos_interface

Manages interface configuration on Juniper Junos devices over SSH. Uses Junos set-style configuration commands with commit semantics.

SSH connection parameters are auto-injected by the gateway proxy.

## Parameters

| Parameter | Required | Default | Description |
|-----------|----------|---------|-------------|
| `interface` | Yes | -- | Interface name (e.g., `ge-0/0/0`) |
| `description` | No | -- | Interface description |
| `ip_address` | No | -- | IP address with prefix length (e.g., `10.0.1.1/24`) |
| `unit` | No | `0` | Logical unit number |
| `family` | No | `inet` | Address family (`inet`, `inet6`) |
| `host` | Auto | -- | Target IP/hostname (auto-injected) |
| `port` | Auto | `22` | SSH port (auto-injected) |
| `username` | Auto | -- | SSH user (auto-injected) |
| `credential` | Auto | -- | Password or SSH key (auto-injected) |

## Idempotency

1. Runs `show configuration interfaces {interface}` on the device
2. Compares each specified parameter against current config
3. Only pushes changes for parameters that differ
4. Uses `commit` to apply (Junos commit is atomic)

## Examples

### Configure an interface

```yaml
resources:
  - name: uplink-interface
    type: junos_interface
    interface: ge-0/0/0
    description: "WAN Uplink"
    ip_address: "10.0.1.1/24"
    unit: "0"
    family: inet
```

### Configure a loopback

```yaml
resources:
  - name: loopback
    type: junos_interface
    interface: lo0
    ip_address: "10.255.0.1/32"
    unit: "0"
```

## Platform

This executor is available on all platforms. It runs on the gateway envoy and connects to the target Junos device over SSH.
---
*Confidential -- Alexander4, LLC. Not for redistribution. See [documentation-license](../../../legal/license.md).*
