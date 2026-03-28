
# network_banner

Manages login and MOTD banners on Cisco and Juniper network devices over SSH.

SSH connection parameters are auto-injected by the gateway proxy.

## Parameters

| Parameter | Required | Default | Description |
|-----------|----------|---------|-------------|
| `device_type` | Yes | -- | `cisco_ios`, `cisco_nxos`, or `juniper_junos` |
| `banner_type` | Yes | -- | `login` or `motd` |
| `text` | Yes | -- | Banner text |
| `state` | No | `present` | `present` to set banner, `absent` to remove |
| `host` | Auto | -- | Target IP/hostname (auto-injected) |
| `port` | Auto | `22` | SSH port (auto-injected) |
| `username` | Auto | -- | SSH user (auto-injected) |
| `credential` | Auto | -- | Password or SSH key (auto-injected) |

## Examples

```yaml
resources:
  - name: login-banner
    type: network_banner
    device_type: cisco_ios
    banner_type: login
    text: "Authorized access only. All activity is monitored."
    state: present

  - name: motd-banner
    type: network_banner
    device_type: cisco_ios
    banner_type: motd
    text: "Managed by Vigo. Do not make manual changes."
    state: present
```

## Platform

Available on all platforms. Runs on the gateway envoy.
---
*Confidential -- Alexander4, LLC. Not for redistribution. See [documentation-license](../../legal/documentation-license.md).*
