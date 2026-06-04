
# ssh_exec

Runs commands on remote network devices over SSH. This is the network equivalent of the [`exec`](exec.md) executor -- it executes arbitrary commands on devices that can't run the Vigo agent (switches, routers, firewalls).

SSH connection parameters (`host`, `port`, `username`, `credential`) are auto-injected by the gateway proxy -- configcrate authors don't need to specify them.

## Parameters

| Parameter | Required | Default | Description |
|-----------|----------|---------|-------------|
| `command` | Yes | -- | Command to execute on the remote device |
| `state` | No | `present` | Accepted for interface consistency with the other network resources but has no effect — `ssh_exec` always runs `command`. Gate execution with `onlyif`/`unless` instead. |
| `unless` | No | -- | Skip if this command succeeds (exit 0) on the device |
| `onlyif` | No | -- | Only run if this command succeeds (exit 0) on the device |
| `timeout` | No | `30` | Per-command timeout in seconds |
| `revert` | No | `false` | When `true`, run `on_revert` on the device to undo this resource instead of applying. See [Reversal](#reversal). |
| `on_revert` | No | -- | Command run **on the remote device over the same SSH connection** to reverse this resource. Required when `revert: true`. |
| `host` | Auto | -- | Target IP/hostname (auto-injected from device config) |
| `port` | Auto | `22` | SSH port (auto-injected) |
| `username` | Auto | -- | SSH user (auto-injected) |
| `credential` | Auto | -- | Password or SSH key (auto-injected) |

## Idempotency

Idempotency is achieved through guard conditions, identical to the local `exec` executor:

- **`unless`** -- The guard command is run on the device first. If it exits zero, the main command is skipped.
- **`onlyif`** -- The guard command is run on the device first. If it exits non-zero, the main command is skipped.

Without guards, the command runs on every convergence.

## Reversal

`ssh_exec` runs an arbitrary command with no inferable inverse, so reversal is **operator-declared**: pair the resource with an `on_revert:` command. Unlike local [`exec`](exec.md), the inverse runs **on the remote device** — the agent re-dispatches over the same SSH connection (with the idempotency guards stripped so it runs unconditionally), not via local `sh -c`.

- `on_revert:` — a command, run on the device, that undoes the resource. Inert until triggered.
- `revert: true` runs `on_revert:` once, then reports settled on subsequent runs (idempotent — it never re-runs). `revert: true` with no `on_revert:` is rejected at `config publish`.
- A normal (non-revert) apply clears a spent revert, re-arming it.

Removing the resource from config does **not** undo it — that only stops enforcement. Use `revert: true` with a declared `on_revert:` to actively undo.

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
