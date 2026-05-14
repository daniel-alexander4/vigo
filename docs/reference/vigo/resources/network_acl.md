
# network_acl

Manages access control list entries on network devices over SSH. Supports adding and removing individual ACL entries across Cisco IOS, NX-OS, IOS-XR, Arista EOS, and Juniper Junos.

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
| `acl` | Yes | -- | ACL name (e.g., `BLOCK_SSH`, `ALLOW_WEB`) |
| `entry` | Yes | -- | ACL entry line. Format varies by platform (see examples). |
| `state` | No | `present` | `present` to add the entry, `absent` to remove it |
| `host` | Auto | -- | Target IP/hostname (auto-injected) |
| `port` | Auto | `22` | SSH port (auto-injected) |
| `username` | Auto | -- | SSH user (auto-injected) |
| `credential` | Auto | -- | Password or SSH key (auto-injected) |
| `device_type` | Auto | -- | Device type (auto-injected from envoy config) |

### Entry Format by Platform

- **Cisco IOS/NX-OS/EOS**: Standard IOS ACL syntax (e.g., `permit ip 10.0.0.0 0.255.255.255 any`, `deny tcp any any eq 22`)
- **Cisco IOS-XR**: Sequence-numbered entries (e.g., `10 permit ipv4 10.0.0.0/8 any`, `20 deny tcp any any eq 22`)
- **Juniper Junos**: Firewall filter term syntax (e.g., `term BLOCK_SSH from protocol tcp destination-port 22`)

## Idempotency

The executor reads the device's running configuration for the specified ACL and checks whether the entry exists:

- On Cisco IOS/EOS: runs `show running-config | section access-list <name>` and searches for the entry text.
- On Cisco NX-OS: runs `show running-config | section ip access-list <name>` and searches for the entry text.
- On Cisco IOS-XR: runs `show running-config ipv4 access-list <name>` and searches for the entry text.
- On Juniper Junos: runs `show configuration firewall` and checks for the entry text.

No commands are sent if the entry is already present (for `state: present`) or already absent (for `state: absent`).

## Examples

### Cisco IOS -- Block SSH access

```yaml
resources:
  - name: block-ssh
    type: network_acl
    acl: "MGMT_ACCESS"
    entry: "deny tcp any any eq 22"
    state: present
```

### Arista EOS -- Allow web traffic


```yaml
resources:
  - name: allow-http
    type: network_acl
    acl: "ALLOW_WEB"
    entry: "permit tcp any any eq 80"
    state: present
```

### Cisco IOS-XR -- Add a numbered permit entry

```yaml
resources:
  - name: allow-internal
    type: network_acl
    acl: "INTERNAL_NETS"
    entry: "10 permit ipv4 10.0.0.0/8 any"
    state: present
```

### Juniper Junos -- Add a firewall filter term

```yaml
resources:
  - name: block-ssh-junos
    type: network_acl
    acl: "PROTECT"
    entry: "term BLOCK_SSH from protocol tcp destination-port 22"
    state: present
```

## Platform

Available on all platforms. Runs on the gateway envoy.
---
*Confidential -- Alexander4, LLC. Not for redistribution. See [license](../../../legal/license.md).*
