# Guardrails

Hobnob includes self-protection guardrails that prevent configs from accidentally breaking the agent or server. These are hardcoded in both the server (config validation) and agent (runtime enforcement).

## Protected Paths

Resources targeting these paths are blocked:

| Path | Protected Component |
|------|-------------------|
| `/usr/local/sbin/hobnob` | Agent binary |
| `/etc/hobnob-thrall/` | Agent config directory |
| `/etc/hobnob/` | Agent config directory (alternate) |
| `/var/lib/hobnob/` | Agent state store |
| `/etc/systemd/system/hobnob-thrall.service` | Agent service unit |
| `/usr/local/sbin/hobd` | Server binary |
| `/usr/local/bin/hobnobcli` | CLI binary |
| `/usr/local/sbin/hobnob-license-manager` | License tool binary |
| `/srv/hobnob/` | Server config directory |
| `/etc/systemd/system/hobd.service` | Server service unit |

A `file` resource targeting `/etc/hobnob-thrall/agent.yaml` will be rejected at config load time.

## Protected Services

These services cannot be stopped, disabled, or masked:

- `hobnob-thrall` (agent)
- `hobd` (server)

A `service` resource with `state: stopped` targeting `hobnob-thrall` will be rejected.

## Protected Packages

These packages cannot be removed:

- `hobnob`
- `hobd`
- `hobnobcli`

A `package` resource with `state: absent` targeting `hobnob` will be rejected.

## Dangerous Commands

`exec` resources are checked for dangerous command patterns:

- `systemctl stop/disable/mask hobnob*`
- `systemctl stop/disable/mask hobd*`
- `service hobnob-thrall stop`
- `service hobd stop`
- `pkill hobnob` / `pkill hobd`
- `killall hobnob` / `killall hobd`

## Validation

Guardrails are enforced at two levels:

### Server-side (config load time)

The `hobnobcli validate` command and the server's config loader check all resources against guardrail rules. Violations are reported as errors:

```bash
$ hobnobcli validate
ERROR: module "cleanup" resource "remove-agent": targets protected path /usr/local/sbin/hobnob
ERROR: module "maintenance" resource "stop-agent": targets protected service hobnob-thrall
```

### Agent-side (runtime)

The agent has a parallel set of guardrails. Even if a config somehow bypasses server validation, the agent will refuse to:
- Modify files in protected paths
- Stop/disable protected services
- Remove protected packages
- Execute dangerous commands

## Bypassing Guardrails

Guardrails cannot be bypassed. They are hardcoded protections against configuration mistakes that would break the management plane.

If you need to update the agent or server, use the standard upgrade mechanism (bootstrap re-enrollment or package upgrade outside of Hobnob).

## Related

- [Config Format](config-format.md) — Resource definitions
- [Executors](../executors/_index.md) — Resource types
