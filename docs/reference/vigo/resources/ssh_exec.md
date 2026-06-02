
# ssh_exec

Runs commands on remote network devices over SSH. This is the network equivalent of the [`exec`](exec.md) executor -- it executes arbitrary commands on devices that can't run the Vigo agent (switches, routers, firewalls).

SSH connection parameters (`host`, `port`, `username`, `credential`) are auto-injected by the gateway proxy -- configcrate authors don't need to specify them.

## Parameters

| Parameter | Required | Default | Description |
|-----------|----------|---------|-------------|
| `command` | Yes | -- | Command to execute on the remote device |
| `unless` | No | -- | Skip if this command succeeds (exit 0) on the device |
| `onlyif` | No | -- | Only run if this command succeeds (exit 0) on the device |
| `timeout` | No | `30` | Per-command timeout in seconds |
| `host` | Auto | -- | Target IP/hostname (auto-injected from device config) |
| `port` | Auto | `22` | SSH port (auto-injected) |
| `username` | Auto | -- | SSH user (auto-injected) |
| `credential` | Auto | -- | Password or SSH key (auto-injected) |

## Idempotency

Idempotency is achieved through guard conditions, identical to the local `exec` executor:

- **`unless`** -- The guard command is run on the device first. If it exits zero, the main command is skipped.
- **`onlyif`** -- The guard command is run on the device first. If it exits non-zero, the main command is skipped.

Without guards, the command runs on every convergence.

## Examples

### Set hostname on a Cisco switch

```yaml
resources:
  - name: set-hostname
    type: ssh_exec
    command: "hostname core-sw01"
    unless: "show running-config | include ^hostname | grep core-sw01"
```

### Enable SSH v2

```yaml
resources:
  - name: enable-ssh-v2
    type: ssh_exec
    command: "ip ssh version 2"
    unless: "show ip ssh | include SSH.*2.0"
```

### Check NTP synchronization

```yaml
resources:
  - name: verify-ntp
    type: ssh_exec
    command: "show ntp status"
    onlyif: "show ntp associations | include ~"
```

## Platform

This executor is available on all platforms. It runs on the gateway envoy and connects to the target device over SSH.
---
*Confidential -- Alexander4, LLC. Not for redistribution. See [license](../../../legal/license.md).*
