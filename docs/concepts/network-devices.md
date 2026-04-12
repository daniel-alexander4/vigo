
# Network Device Management

Vigo manages network devices (switches, routers, firewalls) that can't run the agent binary. These devices appear as first-class fleet members with compliance status, traits, and run history -- managed through a **gateway proxy** pattern over SSH. Five device types are supported: Cisco IOS, NX-OS, IOS-XR, Arista EOS, and Juniper Junos.

## Architecture

```
                    +-----------+
                    |  vigosrv     |
                    |  server   |
                    +-----+-----+
                          | gRPC/mTLS
                    +-----+-----+
                    | Gateway   |    SSH
                    | Envoy    +-----------> [Cisco Switch]
                    | (agent)   +-----------> [Juniper Router]
                    +-----------+-----------> [More devices...]
```

A **gateway envoy** is a normal Linux/macOS server running the Vigo agent. It acts as a proxy: during each check-in, the server sends the gateway its own policy *plus* a list of `ProxiedDevice` entries for all network devices assigned to it. The gateway then:

1. Connects to each device over SSH
2. Collects device traits (model, firmware, interfaces, VLANs, routes)
3. Runs convergence using SSH-backed executors
4. Reports results and traits back to the server under each device's virtual envoy ID

## Configuration

Network devices are defined in `envoys.vgo` alongside regular envoys. A device entry has a `gateway:` field pointing to the managing envoy and a `device_type:` field.

```yaml
envoys:
  # Gateway -- normal Linux server
  - match: "netmgmt01.example.com"
    roles: [bastion]

  # Cisco core switch
  - match: "core-sw01.example.com"
    device_type: cisco_ios
    gateway: "netmgmt01.example.com"
    credential: "secret:network/core-sw01/ssh"
    username: admin
    ip_address: "10.0.1.1"
    modules: [switch-base, switch-vlans]

  # Juniper edge router
  - match: "edge-rtr01.example.com"
    device_type: juniper_junos
    gateway: "netmgmt01.example.com"
    credential: "secret:network/edge-rtr01/ssh"
    username: admin
    ip_address: "10.0.1.2"
    modules: [router-base, bgp-peering]
```

### Device Entry Fields

| Field | Required | Default | Description |
|-------|----------|---------|-------------|

| `match` | Yes | -- | Device hostname (used for fleet identification) |
| `device_type` | Yes | -- | `cisco_ios`, `cisco_nxos`, or `juniper_junos` |
| `gateway` | Yes | -- | Hostname of the gateway envoy managing this device |
| `credential` | Yes | -- | SSH credential using `secret:` prefix |
| `username` | No | `admin` | SSH username |
| `port` | No | `22` | SSH port |
| `ip_address` | No | -- | Management IP (if different from DNS hostname) |
| `modules` | No | -- | List of modules to apply |
| `roles` | No | -- | List of roles to apply |

### Validation Rules

- `device_type` and `gateway` must be set together
- `gateway` must reference an existing envoy that is **not** itself a device
- `credential` must use the `secret:` prefix
- `device_type` must be one of: `cisco_ios`, `cisco_nxos`, `cisco_iosxr`, `arista_eos`, `juniper_junos`

## Supported Device Types

| Device Type | Platform | Config Mode | Tested Versions |
|-------------|----------|-------------|-----------------|
| `cisco_ios` | Cisco IOS/IOS-XE | CLI (configure terminal) | 15.x, 16.x, 17.x |
| `cisco_nxos` | Cisco NX-OS | CLI (configure terminal) | 9.x, 10.x |
| `cisco_iosxr` | Cisco IOS-XR | CLI (configure terminal/commit) | 7.x, 24.x |
| `arista_eos` | Arista EOS | CLI (configure terminal) | 4.28+, 4.30+ |
| `juniper_junos` | Juniper Junos | CLI (configure/commit) | 20.x, 21.x, 22.x |

## SSH Authentication

Devices authenticate via password or SSH key, determined automatically:

- If the credential contains `PRIVATE KEY`, key-based authentication is used
- Otherwise, password authentication is used

Host keys are verified using Trust On First Use (TOFU): the first connection accepts and caches the device's host key. Subsequent connections verify against the cached key. A mismatch is a hard error (possible MITM attack).

## Network Executors

Two transport-level executors handle arbitrary device operations:

| Executor | Description |
|----------|-------------|
| [`ssh_exec`](../reference/executors/ssh_exec.md) | Run commands on devices (analog of `exec`) |
| [`network_config`](../reference/executors/network_config.md) | Declarative config-block management |

Fourteen device-specific executors provide structured, idempotent management:

| Executor | Description |
|----------|-------------|
| [`cisco_vlan`](../reference/executors/cisco_vlan.md) | Manage VLANs on Cisco switches |
| [`cisco_interface`](../reference/executors/cisco_interface.md) | Manage Cisco interface config |
| [`cisco_acl`](../reference/executors/cisco_acl.md) | Manage Cisco access control lists |
| [`junos_interface`](../reference/executors/junos_interface.md) | Manage Junos interface config |
| [`network_ntp`](../reference/executors/network_ntp.md) | Cross-platform NTP server config |
| [`network_syslog`](../reference/executors/network_syslog.md) | Cross-platform syslog config |
| [`network_snmp`](../reference/executors/network_snmp.md) | Cross-platform SNMP community config |
| [`network_user`](../reference/executors/network_user.md) | Cross-platform local user management |
| [`network_banner`](../reference/executors/network_banner.md) | Cross-platform login/MOTD banners |
| [`network_interface`](../reference/executors/network_interface.md) | Cross-platform interface state and description |
| [`network_vlan`](../reference/executors/network_vlan.md) | Cross-platform VLAN management |

| [`network_acl`](../reference/executors/network_acl.md) | Cross-platform access control list entries |
| [`network_static_route`](../reference/executors/network_static_route.md) | Cross-platform static route management |
| [`network_dns`](../reference/executors/network_dns.md) | Cross-platform DNS server and domain config |

## Device Traits

The gateway collects traits from each device over SSH and reports them under the `device` namespace:

| Trait | Source | Description |
|-------|--------|-------------|
| `device.type` | Config | Device type (cisco_ios, etc.) |
| `device.model` | `show version` | Hardware model |
| `device.firmware` | `show version` | Software version |
| `device.serial` | `show version` | Serial number |
| `device.hostname` | `show version` | Device hostname |
| `device.uptime` | `show version` | Uptime string |
| `device.interfaces` | `show interfaces` | Interface list with status |
| `device.vlans` | `show vlan brief` | VLAN table (Cisco only) |
| `device.routes` | `show route summary` | Route count summary |

Traits are available in templates (`{{ .Traits.device.model }}`) and `when:` expressions (`when: "trait('device.firmware', '>= 16.12')"`).

## Example Module

```yaml
# stockpile/modules/switch-base.vgo
name: switch-base
resources:
  - name: login-banner
    type: network_banner
    device_type: cisco_ios
    banner_type: login
    text: "Authorized access only."
    state: present

  - name: ntp-server
    type: network_ntp
    device_type: cisco_ios
    server: "10.0.0.1"
    state: present

  - name: syslog-server
    type: network_syslog
    device_type: cisco_ios
    syslog_host: "10.0.0.50"
    state: present
```

## Compliance and Visibility

Network devices participate fully in Vigo's compliance system:

- **Status tracking** -- converged, relapsed, diverged, degraded, failed, offline, or no data
- **Run history** -- every convergence run is recorded with per-resource outcomes
- **Fleet views** -- devices appear in `vigocli nodes list` and the web UI dashboard
- **Trait queries** -- `vigocli nodes traits core-sw01.example.com` shows device facts
---
*Confidential -- Alexander4, LLC. Not for redistribution. See [documentation-license](../legal/documentation-license.md).*
