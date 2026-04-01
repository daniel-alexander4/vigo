
# Managing Network Devices

Vigo manages switches, routers, and firewalls as first-class fleet members through a gateway proxy pattern. Devices that can't run the agent binary are managed over SSH from a gateway envoy. This guide walks through setup and provides practical configuration examples for all supported platforms.

## Supported Platforms

| Device Type | Vendor | Config Model | Examples |
|-------------|--------|-------------|----------|
| `cisco_ios` | Cisco IOS / IOS-XE | configure terminal / write memory | Catalyst, ISR, ASR |
| `cisco_nxos` | Cisco NX-OS | configure terminal / write memory | Nexus 3K/5K/7K/9K |
| `cisco_iosxr` | Cisco IOS-XR | configure terminal / commit | ASR 9K, NCS, XRv |
| `arista_eos` | Arista EOS | configure terminal / write memory | 7050X, 7280R, 7500R |
| `juniper_junos` | Juniper Junos | configure / commit | EX, QFX, MX, SRX |

## Setup

### 1. Designate a gateway envoy

The gateway is a Linux server running the Vigo agent. It needs SSH connectivity to all managed devices.

```yaml
# envoys/network.vgo
envoys:
  - match: "netmgmt.example.com"
    roles: [bastion]
```

### 2. Store device credentials

```bash
vigocli secrets set network/core-sw01/ssh    # password or paste SSH private key
vigocli secrets set network/dist-sw01/ssh
vigocli secrets set network/edge-rtr01/ssh
```

### 3. Define device entries

```yaml
# envoys/network.vgo (continued)
envoys:
  # Gateway
  - match: "netmgmt.example.com"
    roles: [bastion]

  # Cisco IOS core switch
  - match: "core-sw01.example.com"
    device_type: cisco_ios
    gateway: "netmgmt.example.com"
    credential: "secret:network/core-sw01/ssh"
    username: admin
    ip_address: "10.0.1.1"
    roles: [switch-core]

  # Arista EOS leaf switch
  - match: "leaf-sw01.example.com"
    device_type: arista_eos
    gateway: "netmgmt.example.com"
    credential: "secret:network/leaf-sw01/ssh"
    username: admin
    ip_address: "10.0.1.10"
    roles: [switch-leaf]

  # Cisco NX-OS datacenter switch
  - match: "dc-sw01.example.com"
    device_type: cisco_nxos
    gateway: "netmgmt.example.com"
    credential: "secret:network/dc-sw01/ssh"
    username: admin
    ip_address: "10.0.2.1"
    roles: [switch-dc]

  # Juniper edge router
  - match: "edge-rtr01.example.com"
    device_type: juniper_junos
    gateway: "netmgmt.example.com"
    credential: "secret:network/edge-rtr01/ssh"
    username: admin
    ip_address: "10.0.3.1"
    roles: [router-edge]

  # Cisco IOS-XR service provider router
  - match: "pe-rtr01.example.com"
    device_type: cisco_iosxr
    gateway: "netmgmt.example.com"
    credential: "secret:network/pe-rtr01/ssh"
    username: admin
    ip_address: "10.0.4.1"
    roles: [router-pe]
```

### 4. Publish and verify

```bash
vigocli config publish
vigocli envoys push netmgmt.example.com    # gateway check-in triggers device convergence
vigocli nodes list                  # devices appear as envoys
```

## Example: Campus Switch Baseline

A module that applies to every campus switch regardless of vendor. Uses the cross-platform `network_*` executors — no vendor-specific commands needed.

```yaml
# modules/switch-base.vgo
name: switch-base

vars:
  ntp_server: "10.0.0.1"
  syslog_server: "10.0.0.50"
  snmp_community: "secret:network/snmp-readonly"
  dns_server: "10.0.0.10"
  domain_name: "example.com"
  admin_user: netops
  admin_password: "secret:network/admin-password"

resources:
  # Login banner — every switch gets this
  - name: login-banner
    type: network_banner
    banner_type: login
    text: |
      *** AUTHORIZED ACCESS ONLY ***
      This device is managed by Vigo. Unauthorized changes will be reverted.

  # NTP — consistent time across all devices
  - name: ntp-primary
    type: network_ntp
    server: "{{ .Vars.ntp_server }}"

  # Syslog — centralized logging
  - name: syslog-primary
    type: network_syslog
    syslog_host: "{{ .Vars.syslog_server }}"

  # SNMP — monitoring access
  - name: snmp-readonly
    type: network_snmp
    community: "{{ .Vars.snmp_community }}"
    access: ro

  # DNS — name resolution
  - name: dns-server
    type: network_dns
    server: "{{ .Vars.dns_server }}"
    domain: "{{ .Vars.domain_name }}"

  # Local admin account
  - name: admin-user
    type: network_user
    network_user: "{{ .Vars.admin_user }}"
    password: "{{ .Vars.admin_password }}"
    privilege: "15"
```

This single module works on Cisco IOS, NX-OS, IOS-XR, Arista EOS, and Juniper Junos. The `device_type` parameter is auto-injected from the envoy config — you don't specify it in the module.

## Example: VLAN Management

Manage VLANs across your switching fabric with `foreach`:

```yaml
# modules/datacenter-vlans.vgo
name: datacenter-vlans

vars:
  vlans:
    - id: 10
      name: Management
    - id: 20
      name: Servers
    - id: 30
      name: Storage
    - id: 40
      name: DMZ
    - id: 100
      name: Users
    - id: 999
      name: Native
resources:
  - name: "vlan-{{ .Item.id }}"
    type: network_vlan
    foreach: vlans
    vlan: "{{ .Item.id }}"
    vlan_name: "{{ .Item.name }}"
```

Add a VLAN to the list, publish — every switch gets it on the next convergence.

## Example: Interface Configuration

Standardize interface descriptions and state across a rack of switches:

```yaml
# modules/leaf-interfaces.vgo
name: leaf-interfaces

vars:
  uplinks:
    - interface: Ethernet49
      description: "Uplink to spine-01"
    - interface: Ethernet50
      description: "Uplink to spine-02"
  server_ports:
    - interface: Ethernet1
      description: "web-01 eth0"
    - interface: Ethernet2
      description: "web-02 eth0"
    - interface: Ethernet3
      description: "db-01 eth0"

resources:
  # Uplinks — ensure they're up with correct descriptions
  - name: "uplink-{{ .Item.interface }}"
    type: network_interface
    foreach: uplinks
    interface: "{{ .Item.interface }}"
    description: "{{ .Item.description }}"
    state: up

  # Server ports — descriptions for documentation
  - name: "server-{{ .Item.interface }}"
    type: network_interface
    foreach: server_ports
    interface: "{{ .Item.interface }}"
    description: "{{ .Item.description }}"
    state: up

  # Shutdown unused ports (security hardening)
  - name: shutdown-unused
    type: ssh_exec
    command: |
      show interfaces status | include notconnect
    unless: "show interfaces status | include notconnect | count"
```

## Example: Access Control Lists

Manage firewall rules on every border device:

---
*Confidential -- Alexander4, LLC. Not for redistribution. See [documentation-license](../../legal/documentation-license.md).*

---

```yaml
# modules/border-acl.vgo
name: border-acl

vars:
  acl_name: BORDER-IN
  allowed_entries:
    - "permit tcp any any eq 80"
    - "permit tcp any any eq 443"
    - "permit tcp any any eq 22"
    - "permit icmp any any echo"
    - "permit icmp any any echo-reply"
  denied_entries:
    - "deny ip 10.0.0.0 0.255.255.255 any"
    - "deny ip 172.16.0.0 0.15.255.255 any"
    - "deny ip 192.168.0.0 0.0.255.255 any"

resources:
  # Permit rules
  - name: "acl-permit-{{ .ItemIndex }}"
    type: network_acl
    foreach: allowed_entries
    acl: "{{ .Vars.acl_name }}"
    entry: "{{ .Item }}"

  # Deny RFC1918 from outside
  - name: "acl-deny-{{ .ItemIndex }}"
    type: network_acl
    foreach: denied_entries
    acl: "{{ .Vars.acl_name }}"
    entry: "{{ .Item }}"
```

## Example: Static Routes

Manage routing tables declaratively:

```yaml
# modules/site-routes.vgo
name: site-routes

vars:
  routes:
    - network: "10.1.0.0/16"
      next_hop: "10.0.0.1"
    - network: "10.2.0.0/16"
      next_hop: "10.0.0.1"
    - network: "172.16.0.0/12"
      next_hop: "10.0.0.254"

resources:
  - name: "route-{{ .Item.network }}"
    type: network_static_route
    foreach: routes
    network: "{{ .Item.network }}"
    next_hop: "{{ .Item.next_hop }}"
```

## Example: Vendor-Specific Config Blocks

---
*Confidential -- Alexander4, LLC. Not for redistribution. See [documentation-license](../../legal/documentation-license.md).*

---

When the structured executors don't cover your use case, `network_config` pushes raw config blocks with idempotency (checks running-config before pushing):

```yaml
# modules/ios-hardening.vgo
name: ios-hardening

resources:
  # Disable HTTP server
  - name: no-http-server
    type: network_config
    config: |
      no ip http server
      no ip http secure-server

  # Enable SSH v2 only
  - name: ssh-v2-only
    type: network_config
    config: |
      ip ssh version 2
      ip ssh time-out 60
      ip ssh authentication-retries 3

  # Console and VTY hardening
  - name: vty-hardening
    type: network_config
    config: |
      line vty 0 15
       transport input ssh
       exec-timeout 15 0
       login local
```

## Example: Multi-Vendor Role with Conditional Modules

Use `when:` on module refs to apply vendor-specific modules alongside the shared baseline:

```yaml
# roles/switch-core.vgo
name: switch-core
modules:
  - switch-base             # cross-platform baseline (works everywhere)
  - datacenter-vlans        # cross-platform VLANs
  - name: ios-hardening     # Cisco IOS-specific
    when: "trait('device.type', 'cisco_ios')"
  - name: eos-hardening     # Arista-specific
    when: "trait('device.type', 'arista_eos')"
  - name: junos-hardening   # Juniper-specific
    when: "trait('device.type', 'juniper_junos')"
```

## Example: Ad-Hoc Commands

Run commands across your network fleet:

```bash
# Show version on all Cisco switches
vigocli task run "show version" --target "*.example.com" \
  --device-type cisco_ios
# Check interface status on a specific device
vigocli query --target "core-sw01.example.com" \
  --command "show interfaces status"
```

## How Idempotency Works

Network executors check the device's running configuration before making changes. The pattern:

1. Run `show running-config` (or equivalent) over SSH
2. Check if the desired config line exists in the output
3. If already present: skip (Action::None)
4. If missing (or present when `state: absent`): push the config commands

This is **config-line matching** — the executor generates the expected config line from your structured parameters and searches for it in the running config. No vendor-specific output parsing needed.

```
Your config:                    Generated config line:         Check running-config:
  type: network_ntp       →     "ntp server 10.0.0.1"    →    Present? Skip.
  server: "10.0.0.1"                                          Missing? Push config.
```

After pushing, the executor runs the save command (`write memory` for IOS/EOS, `commit` for Junos/IOS-XR).

## Compliance and Visibility

Network devices are full fleet members:

```bash
vigocli nodes list              # devices appear alongside servers
vigocli nodes traits core-sw01  # show device model, firmware, VLANs, interfaces
vigocli runs list               # convergence runs visible per device
```

The web UI dashboard shows device compliance alongside servers. Drifted devices (where running-config doesn't match desired state) appear as non-compliant.

## Related

- [Network Device Concepts](../../concepts/network-devices.md) — Architecture, gateway proxy, device types
- [Executor Reference](../executors/_index.md) — All network executors
- [ssh_exec](../executors/ssh_exec.md) — Ad-hoc command execution
- [network_config](../executors/network_config.md) — Raw config block management
---
*Confidential -- Alexander4, LLC. Not for redistribution. See [documentation-license](../../legal/documentation-license.md).*
