# Self-Protection Guardrails

Vigo includes hardcoded, non-configurable guardrails that prevent configs, pushes, and workflows from destroying the agent, server, or their dependencies on managed nodes. These guardrails are enforced at two layers and cannot be bypassed — even in dangerous mode.

## Two-Layer Defense

### Server-Side (Config Load Time)

The server validates all module resources when config is published (`vigogo config publish`). Resources that violate guardrails are rejected with an error before they reach any agent. Ad-hoc push commands are also validated before dispatch.

**Enforcement points:**
- `vigogo config publish` — module validation
- `vigogo push run` — command validation
- Config reload after `vigogo config publish`

### Agent-Side (Runtime)

The agent checks every resource against guardrails before applying it. This is the last line of defense — even if a malformed config somehow bypasses server validation, the agent will refuse to apply it. Returns an error result for the resource without executing it.

**Enforcement point:** Before every resource executor dispatch in the convergence loop.

## Protected Paths

Resources of type `file`, `directory`, `symlink`, `file_line`, `blockinfile`, `ini`, `json_file`, `replace`, and `field_edit` are blocked from targeting these paths or any child path beneath them.

### Unix (Linux, macOS, FreeBSD, OpenBSD, NetBSD, illumos)

| Path | Purpose |
|------|---------|
| `/usr/local/sbin/vigo` | Agent binary |
| `/etc/vigo-envoy/` | Agent configuration |
| `/etc/vigo/` | Agent configuration (alternate) |
| `/var/lib/vigo/` | Agent state (LMDB) |
| `/etc/systemd/system/vigo-envoy.service` | Agent systemd unit (Linux) |
| `/usr/local/etc/vigo-envoy/` | Agent configuration (macOS/FreeBSD) |
| `/Library/Vigo/` | Agent installation (macOS) |
| `/Library/LaunchDaemons/com.vigo.envoy.plist` | Agent launchd plist (macOS) |
| `/usr/local/etc/rc.d/vigo_envoy` | Agent rc.d script (FreeBSD/NetBSD) |
| `/var/svc/manifest/system/vigo-envoy.xml` | Agent SMF manifest (illumos) |
| `/usr/local/sbin/vigosrv` | Server binary |
| `/usr/local/bin/vigogo` | CLI binary |
| `/usr/local/sbin/vigo-license-manager` | License manager binary |
| `/srv/vigo/` | Server data directory (config, secrets, database, TLS) |
| `/etc/systemd/system/vigosrv.service` | Server systemd unit (Linux) |

### Windows

| Path | Purpose |
|------|---------|
| `C:\Program Files\Vigo\` | Agent and server installation |
| `C:\ProgramData\Vigo\` | Agent and server data |

### Path Matching

A resource targets a protected path if its `target_path` (or `path`) attribute:
- Exactly matches a protected path, OR
- Is a child of a protected path (starts with the protected path followed by `/`)

Symlinks are resolved before matching — a symlink pointing into a protected directory is blocked.

## Protected Services

Service resources are blocked from setting these services to `stopped`, `disabled`, `masked`, or `absent`:

| Service | Purpose |
|---------|---------|
| `vigo-envoy` | Agent daemon |
| `vigosrv` | Server daemon |

Starting, enabling, or restarting these services is allowed.

## Protected Packages

Package resources are blocked from setting these packages to `state: absent`:

| Package | Purpose |
|---------|---------|
| `vigo` | Agent package |
| `vigosrv` | Server package |
| `vigogo` | CLI package |

Installing or upgrading these packages is allowed.

## Blocked Command Patterns

Exec resources and ad-hoc push commands are scanned for these patterns (case-insensitive substring match):

### Linux (systemd)

| Pattern | Action Blocked |
|---------|---------------|
| `systemctl stop vigo` | Stopping agent service |
| `systemctl disable vigo` | Disabling agent service |
| `systemctl mask vigo` | Masking agent service |
| `systemctl stop vigosrv` | Stopping server service |
| `systemctl disable vigosrv` | Disabling server service |
| `systemctl mask vigosrv` | Masking server service |

### BSD (rc.d / service)

| Pattern | Action Blocked |
|---------|---------------|
| `service vigo-envoy stop` | Stopping agent (FreeBSD/NetBSD) |
| `service vigo_envoy stop` | Stopping agent (alternate name) |
| `service vigosrv stop` | Stopping server |

### OpenBSD (rcctl)

| Pattern | Action Blocked |
|---------|---------------|
| `rcctl stop vigo` | Stopping agent |
| `rcctl disable vigo` | Disabling agent |

### macOS (launchctl)

| Pattern | Action Blocked |
|---------|---------------|
| `launchctl unload com.vigo` | Unloading agent plist |
| `launchctl remove com.vigo` | Removing agent service |
| `launchctl bootout com.vigo` | Booting out agent service |

### illumos (SMF)

| Pattern | Action Blocked |
|---------|---------------|
| `svcadm disable vigo` | Disabling agent SMF service |
| `svccfg delete vigo` | Deleting agent SMF manifest |

### Process Termination (all Unix)

| Pattern | Action Blocked |
|---------|---------------|
| `pkill vigo` | Killing agent process |
| `pkill vigosrv` | Killing server process |
| `killall vigo` | Killing agent process |
| `killall vigosrv` | Killing server process |
| `kill -9 vigo` | Force-killing agent |
| `kill -9 vigosrv` | Force-killing server |

### File Removal (all Unix)

| Pattern | Action Blocked |
|---------|---------------|
| `rm /usr/local/sbin/vigo` | Deleting agent binary |
| `rm /usr/local/sbin/vigosrv` | Deleting server binary |
| `rm /usr/local/bin/vigogo` | Deleting CLI binary |
| `rm /usr/local/sbin/vigo-license-manager` | Deleting license manager |
| `rm -rf /etc/vigo` | Deleting agent config |
| `rm -rf /var/lib/vigo` | Deleting agent state |
| `rm -rf /srv/vigo` | Deleting server data |
| `rm -rf /etc/vigo-envoy` | Deleting agent config |
| `rm -rf /library/vigo` | Deleting agent (macOS) |

### Windows

| Pattern | Action Blocked |
|---------|---------------|
| `sc stop vigo` | Stopping agent service |
| `sc delete vigo` | Deleting agent service |
| `taskkill /im vigo` | Killing agent process |
| `del /f vigo.exe` | Deleting agent binary |
| `rmdir /s vigo` | Deleting agent directory |

### Additional Push/Exec Protection

Beyond pattern matching, any `rm` or `unlink` command that references a protected path (from the Protected Paths table above) is blocked. This catches variations like `rm -f /srv/vigo/server.yaml` or `unlink /etc/vigo/config`.

## Dangerous Mode

The `dangerous` flag in the config loader allows exec resources without idempotency guards (`creates`, `onlyif`, `unless`). However, **dangerous mode does not bypass self-protection guardrails**. A resource that targets a protected path, service, package, or command pattern is always blocked regardless of the dangerous flag.

## Idempotency Requirements

Separately from self-protection, Vigo enforces idempotency on exec resources. Exec resources must include at least one guard unless they are triggered by `subscribes` or `watch_secret`:

| Guard | Behavior |
|-------|----------|
| `creates: /path/to/marker` | Skip if marker file exists |
| `onlyif: command` | Skip if guard command exits non-zero |
| `unless: command` | Skip if guard command exits zero |

This prevents exec resources from running unconditionally on every convergence cycle.
