# network_user

Manages local user accounts on Cisco and Juniper network devices over SSH.

SSH connection parameters are auto-injected by the gateway proxy.

## Parameters

| Parameter | Required | Default | Description |
|-----------|----------|---------|-------------|
| `device_type` | Yes | -- | `cisco_ios`, `cisco_nxos`, or `juniper_junos` |
| `device_username` | Yes | -- | Username to manage on the device |
| `privilege` | No | -- | Privilege level (Cisco: 0-15) |
| `secret` | No | -- | Password for the user |
| `state` | No | `present` | `present` to create/update, `absent` to remove |
| `host` | Auto | -- | Target IP/hostname (auto-injected) |
| `port` | Auto | `22` | SSH port (auto-injected) |
| `username` | Auto | -- | SSH user (auto-injected) |
| `credential` | Auto | -- | Password or SSH key (auto-injected) |

## Examples

```yaml
resources:
  - name: admin-user
    type: network_user
    device_type: cisco_ios
    device_username: netadmin
    privilege: "15"
    secret: "secret:network/users/netadmin"
    state: present

  - name: remove-old-user
    type: network_user
    device_type: cisco_ios
    device_username: oldadmin
    state: absent
```

## Platform

Available on all platforms. Runs on the gateway thrall.
