
# network_config

Declarative config-block management for network devices over SSH. Ensures that specified configuration lines are present (or absent) in the device's running configuration, using device-specific config mode commands.

SSH connection parameters are auto-injected by the gateway proxy.

## Parameters

| Parameter | Required | Default | Description |
|-----------|----------|---------|-------------|
| `device_type` | Yes | -- | Device platform: `cisco_ios`, `cisco_nxos`, `juniper_junos` |
| `config_lines` | Yes | -- | Newline-separated config lines to ensure |
| `state` | Yes | -- | `present` to ensure lines exist, `absent` to ensure removal |
| `check_command` | No | `show running-config` | Command to check current config state |
| `save` | No | `true` | Whether to save config after change |
| `host` | Auto | -- | Target IP/hostname (auto-injected) |
| `port` | Auto | `22` | SSH port (auto-injected) |
| `username` | Auto | -- | SSH user (auto-injected) |
| `credential` | Auto | -- | Password or SSH key (auto-injected) |

## Device-Specific Behavior

| Device Type | Enter Config | Exit Config | Save | Negate Prefix |
|-------------|-------------|-------------|------|---------------|
| `cisco_ios` | `configure terminal` | `end` | `write memory` | `no ` |
| `cisco_nxos` | `configure terminal` | `end` | `copy running-config startup-config` | `no ` |
| `juniper_junos` | `configure` | `commit` | (commit is save) | `delete ` |

## Idempotency

The executor runs the `check_command` (default: `show running-config`) and checks whether each `config_lines` entry is present in the output.

- **`state: present`** -- If all lines are found, no change. If any are missing, all lines are pushed.
- **`state: absent`** -- If none of the lines are found, no change. If any are found, they are negated using the device-specific prefix (`no ` for Cisco, `delete ` for Junos).

## Examples

### Ensure VLANs exist

```yaml
resources:
  - name: vlan-100
    type: network_config
    device_type: cisco_ios
    config_lines: |
      vlan 100
       name Production
    state: present
```

### Remove a deprecated VLAN

```yaml
resources:
  - name: remove-vlan-999
    type: network_config
    device_type: cisco_ios
    config_lines: |
      vlan 999
    state: absent
```

### Junos interface config

```yaml
resources:
  - name: loopback-config
    type: network_config
    device_type: juniper_junos
    config_lines: |
      set interfaces lo0 unit 0 family inet address 10.255.0.1/32
    check_command: "show configuration interfaces lo0"
    state: present
```

## Platform

This executor is available on all platforms. It runs on the gateway envoy and connects to the target device over SSH.
---
*Confidential -- Alexander4, LLC. Not for redistribution. See [license](../../../legal/license.md).*
