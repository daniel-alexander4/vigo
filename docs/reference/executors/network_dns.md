
# network_dns

Manages DNS configuration (nameservers and domain name) on network devices over SSH. Supports Cisco IOS, NX-OS, IOS-XR, Arista EOS, and Juniper Junos.

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
| `server` | No* | -- | DNS server IP address |
| `domain` | No* | -- | Domain name for the device |
| `state` | No | `present` | `present` to configure, `absent` to remove |
| `host` | Auto | -- | Target IP/hostname (auto-injected) |
| `port` | Auto | `22` | SSH port (auto-injected) |
| `username` | Auto | -- | SSH user (auto-injected) |
| `credential` | Auto | -- | Password or SSH key (auto-injected) |
| `device_type` | Auto | -- | Device type (auto-injected from node config) |

*At least one of `server` or `domain` is required. Both can be specified together to configure nameserver and domain in a single resource.

## Idempotency

The executor reads the device's running configuration and independently checks whether the DNS server and/or domain name are configured:

- On Cisco IOS/NX-OS/EOS: runs `show running-config | include name-server|domain` and matches `name-server <ip>` and `domain-name <name>` lines.
- On Cisco IOS-XR: runs `show running-config | include domain` and matches `name-server <ip>` and `domain name <name>` lines.
- On Juniper Junos: runs `show configuration system` and checks for `name-server <ip>` and `domain-name <name>`.

Each field (`server` and `domain`) is evaluated independently. If both are specified and only one needs changing, only the necessary commands are issued. No commands are sent if the current configuration already matches the desired state for all specified fields.

## Examples

### Cisco IOS -- Set DNS server and domain

```yaml
resources:
  - name: dns-config
    type: network_dns
    server: "8.8.8.8"
    domain: "example.com"
    state: present
```

### Arista EOS -- Add a nameserver

```yaml
resources:
  - name: dns-server
    type: network_dns
    server: "10.0.0.53"
    state: present
```

### Cisco IOS-XR -- Configure domain name

```yaml
resources:
  - name: domain-name
    type: network_dns
    domain: "corp.example.com"
    state: present
```

### Juniper Junos -- Remove a DNS server

```yaml
resources:
  - name: remove-old-dns
    type: network_dns
    server: "1.1.1.1"
    state: absent
```

## Platform

Available on all platforms. Runs on the gateway envoy.
---
*Confidential -- Alexander4, LLC. Not for redistribution. See [documentation-license](../../legal/documentation-license.md).*
