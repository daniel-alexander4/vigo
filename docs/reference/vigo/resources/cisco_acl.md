
# cisco_acl

Manages extended access control lists (ACLs) on Cisco IOS/NX-OS devices over SSH.

SSH connection parameters are auto-injected by the gateway proxy.

## Parameters

| Parameter | Required | Default | Description |
|-----------|----------|---------|-------------|
| `acl_name` | Yes | -- | ACL name |
| `entries` | Yes | -- | Newline-separated ACL entries |
| `state` | No | `present` | `present` to create/update, `absent` to remove entire ACL |
| `host` | Auto | -- | Target IP/hostname (auto-injected) |
| `port` | Auto | `22` | SSH port (auto-injected) |
| `username` | Auto | -- | SSH user (auto-injected) |
| `credential` | Auto | -- | Password or SSH key (auto-injected) |

## Idempotency

1. Runs `show access-lists {acl_name}` on the device
2. If `state: present`, checks if all entries exist in the ACL
3. If `state: absent`, checks if the ACL exists at all

## Examples

### Create an ACL

```yaml
resources:
  - name: management-acl
    type: cisco_acl
    acl_name: MGMT-ACCESS
    entries: |
      permit ip 10.0.0.0 0.0.0.255 any
      permit ip 172.16.0.0 0.0.255.255 any
      deny ip any any
    state: present
```

### Remove an ACL

```yaml
resources:
  - name: remove-old-acl
    type: cisco_acl
    acl_name: OLD-FILTER
    state: absent
```

## Platform

This executor is available on all platforms. It runs on the gateway envoy and connects to the target Cisco device over SSH.
---
*Confidential -- Alexander4, LLC. Not for redistribution. See [documentation-license](../../../legal/license.md).*
