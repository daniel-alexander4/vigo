
# Guardrails

Vigo includes self-protection guardrails that prevent configs from accidentally breaking the agent or server. These are hardcoded in both the server (config validation) and agent (runtime enforcement).

## Protected Paths

Resources targeting these paths are blocked:

| Path | Protected Component |
|------|-------------------|
| `/usr/local/sbin/vigo` | Agent binary |
| `/etc/vigo-envoy/` | Agent config directory |
| `/etc/vigo/` | Agent config directory (alternate) |
| `/var/lib/vigo/` | Agent state store |
| `/etc/systemd/system/vigo-envoy.service` | Agent service unit |
| `/usr/local/sbin/vigosrv` | Server binary |
| `/usr/local/bin/vigocli` | CLI binary |
| `/usr/local/sbin/vigo-license-manager` | License tool binary |
| `/srv/vigo/` | Server config directory |
| `/etc/systemd/system/vigosrv.service` | Server service unit |

A `file` resource targeting `/etc/vigo-envoy/agent.yaml` will be rejected at config load time.

## Protected Services

These services cannot be stopped, disabled, or masked:

- `vigo-envoy` (agent)
- `vigosrv` (server)

A `service` resource with `state: stopped` targeting `vigo-envoy` will be rejected.

## Protected Packages

These packages cannot be removed:

- `vigo`
- `vigosrv`
- `vigocli`

A `package` resource with `state: absent` targeting `vigo` will be rejected.

## Dangerous Commands

`exec` resources are checked for dangerous command patterns:

- `systemctl stop/disable/mask vigo*`
- `systemctl stop/disable/mask vigosrv*`
- `service vigo-envoy stop`
- `service vigosrv stop`
- `pkill vigo` / `pkill vigosrv`
- `killall vigo` / `killall vigosrv`

## Validation

Guardrails are enforced at two levels:

### Server-side (config load time)


The `vigocli validate` command and the server's config loader check all resources against guardrail rules. Violations are reported as errors:

```bash
$ vigocli validate
ERROR: module "cleanup" resource "remove-agent": targets protected path /usr/local/sbin/vigo
ERROR: module "maintenance" resource "stop-agent": targets protected service vigo-envoy
```

### Agent-side (runtime)

The agent has a parallel set of guardrails. Even if a config somehow bypasses server validation, the agent will refuse to:
- Modify files in protected paths
- Stop/disable protected services
- Remove protected packages
- Execute dangerous commands

## Bypassing Guardrails

Guardrails cannot be bypassed. They are hardcoded protections against configuration mistakes that would break the management plane.

If you need to update the agent or server, use the standard upgrade mechanism (bootstrap re-enrollment or package upgrade outside of Vigo).

## Related

- [Config Format](config-format.md) — Resource definitions
- [Executors](../reference/executors/_index.md) — Resource types
---
*Confidential -- Alexander4, LLC. Not for redistribution. See [documentation-license](../legal/documentation-license.md).*
