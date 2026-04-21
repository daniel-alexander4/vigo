
# Observe Mode

Observe mode puts envoys into a permanent dry-run state: the agent enrolls, collects traits, receives policy, and reports drift — but never writes changes. This is useful for:

- **Migration from other tools** — run Vigo alongside Salt, Puppet, or Chef without conflicts while you validate policy
- **Audit before enforcement** — see what Vigo would change before letting it act
- **Staging new modules** — test policy on production nodes without risk

## How it works

When observe mode is active, the server sets `observe_only: true` in the check-in response. The agent ORs this with its local `dry_run` flag. Every executor detects drift and reports what it *would* change, but applies nothing. Results flow back normally — you see full drift reporting in the UI, CLI, and API.

## Global observe mode

Set `observe_only: true` under `checkin:` in `server.yaml` to put the entire fleet into observe mode:

```yaml
checkin:
  interval: "5m"
  observe_only: true
```

All envoys will report drift only, regardless of per-entry settings. Remove the line (or set `false`) and restart to resume enforcement.

## Per-entry observe mode

Set `observe_only: true` on individual match blocks in your envoys.vgo to observe specific envoys:

```yaml
- match: "*.staging.example.com"
  observe_only: true
  roles: [base, web]
```

## Precedence

Either flag being `true` triggers observe mode. The global flag ORs with the per-entry flag:

| Global (`server.yaml`) | Per-entry (`envoys.vgo`) | Result |
|------------------------|------------------------|--------|
| `false` | `false` | Enforce |
| `false` | `true` | Observe |
| `true` | `false` | Observe |
| `true` | `true` | Observe |

## UI

Envoys in observe mode show an **observing** status badge instead of their convergence status.
---
*Confidential -- Alexander4, LLC. Not for redistribution. See [documentation-license](../legal/documentation-license.md).*
