
# Set up network devices

You'll finish this page with switches, routers, and firewalls under the same convergence model as your servers — each device appears in `vigocli envoys list` with traits, compliance status, and run history. The trick: a designated **gateway envoy** (a regular Linux/macOS host running the agent) does the SSH work on Vigo's behalf, so the network device itself runs no Vigo software. Five device families today: Cisco IOS, NX-OS, IOS-XR, Arista EOS, Juniper Junos.

**When you'd use this:** you have devices that can't (or shouldn't) run the agent binary but need to be on the same compliance, audit, and convergence rails as the rest of the fleet. The classic case is a fleet of access switches whose VLAN / interface / ACL state you want declarative.

**When you'd skip this:** devices that already run the agent (route them as normal envoys); one-off devices you'd rather manage with a domain-specific tool (NSO, NAPALM, vendor controllers); read-only inventory (use the inventory query path instead — no need to introduce a gateway).

## Enable it

Pick one envoy on the same management network as the device(s) — call it the **gateway envoy**. It'll SSH to each network device on Vigo's behalf. Declare the devices as envoys via the network-device hostcrate shape, with the gateway specified:

```yaml
# /srv/vigo/stacks/stockpile/envoys.vgo
envoys:
  - match: "switch-*.lab"
    network_device:
      gateway: gateway-envoy.lab
      type: cisco_ios
      ssh_user: "secret:vigo/net/ssh-user"
      ssh_password: "secret:vigo/net/ssh-pass"
    configcrates: [switch-baseline]
```

The gateway envoy itself needs the matching `network_device` capability enabled in `server.yaml` (or via the gateway envoy's role).

## Use it

Once the hostcrate lands and the gateway envoy has converged, the network devices appear in `vigocli envoys list` like any other envoy. Configcrates targeting them run their resources via the gateway: e.g., a `cisco_vlan` resource declares "VLAN 100 should exist on this switch"; the gateway SSHs in, checks current state, applies the diff, reports back.

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
    configcrates: [switch-base, switch-vlans]

  # Juniper edge router
  - match: "edge-rtr01.example.com"
    device_type: juniper_junos
    gateway: "netmgmt01.example.com"
    credential: "secret:network/edge-rtr01/ssh"
    username: admin
    ip_address: "10.0.1.2"
    configcrates: [router-base, bgp-peering]
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
| `configcrates` | No | -- | List of configcrates to apply |
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
| [`ssh_exec`](../reference/vigo/resources/) | Run commands on devices (analog of `exec`) |
| [`network_config`](../reference/vigo/resources/) | Declarative config-block management |

Fourteen device-specific executors provide structured, idempotent management:

| Executor | Description |
|----------|-------------|
| [`cisco_vlan`](../reference/vigo/resources/) | Manage VLANs on Cisco switches |
| [`cisco_interface`](../reference/vigo/resources/) | Manage Cisco interface config |
| [`cisco_acl`](../reference/vigo/resources/) | Manage Cisco access control lists |
| [`junos_interface`](../reference/vigo/resources/) | Manage Junos interface config |
| [`network_ntp`](../reference/vigo/resources/) | Cross-platform NTP server config |
| [`network_syslog`](../reference/vigo/resources/) | Cross-platform syslog config |
| [`network_snmp`](../reference/vigo/resources/) | Cross-platform SNMP community config |
| [`network_user`](../reference/vigo/resources/) | Cross-platform local user management |
| [`network_banner`](../reference/vigo/resources/) | Cross-platform login/MOTD banners |
| [`network_interface`](../reference/vigo/resources/) | Cross-platform interface state and description |
| [`network_vlan`](../reference/vigo/resources/) | Cross-platform VLAN management |

| [`network_acl`](../reference/vigo/resources/) | Cross-platform access control list entries |
| [`network_static_route`](../reference/vigo/resources/) | Cross-platform static route management |
| [`network_dns`](../reference/vigo/resources/) | Cross-platform DNS server and domain config |

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

## Example Configcrate

```yaml
# stockpile/configcrates/switch-base.vgo
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
- **Fleet views** -- devices appear in `vigocli envoys list` and the web UI dashboard
- **Trait queries** -- `vigocli traits show <device-hostname>` shows device facts

## What's next

- **Write configcrates against the device executors** → see `cisco_vlan`, `network_ntp`, `network_syslog`, `network_user` in the resource reference. Cross-platform executors abstract the device-type differences where possible.
- **A device's SSH host key changed** → the TOFU cache hard-errors; investigate before clearing. If the change is legitimate, remove the cached key on the gateway and re-converge.
- **The gateway can't reach a device** → [Troubleshoot common issues](troubleshoot.md) covers the gateway-proxy connectivity matrix.

---
*Verified on Vigo 0.51.6 · 2026-05-13.*

*Confidential — Alexander4, LLC. Not for redistribution. See [../legal/license.md](../legal/license.md).*
