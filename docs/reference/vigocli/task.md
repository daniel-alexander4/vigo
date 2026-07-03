
# vigocli task

Dispatch and monitor ad-hoc commands or named tasks on envoys.

## Subcommands

| Subcommand | Description |
|------------|-------------|
| `run` | Run a command or named task on targeted envoys |
| `status` | Show status and results for a task run |
| `list` | List recent task runs |

## Usage

```bash
vigocli task [subcommand] [flags]
```

## run

Run an ad-hoc command or a named task on targeted envoys. The CLI polls until the task completes, then displays results.

```bash
vigocli task run <command-or-task-name> [flags]
```

### Flags

| Flag | Short | Default | Description |
|------|-------|---------|-------------|
| `--target` | | | Target spec: hostname, glob (`*.web.*`), UUID, or trait filter (`os.distro=ubuntu`) |
| `--timeout` | | `60` | Command timeout in seconds |
| `--batch-size` | | | Batch size: count (`5`) or percentage (`25%`) |
| `--max-failures` | | | Max failures before abort: count (`2`) or percentage (`10%`) |
| `--health-check-url` | | | URL to probe between batches (abort on non-2xx) |
| `--nice` | | `0` | CPU/IO priority 0–19 (0 = normal, 19 = lowest) |
| `--dry-run` | | `false` | Display batch plan without executing |
| `--force` | | `false` | Bypass the server-side guardrail check (agent guardrails still apply) |
| `--param` | | | Task parameter (`key=value`), repeatable |

### Guardrails

The server refuses to dispatch a command whose text matches the
hardcoded dangerous-pattern list — `systemctl stop vigo`, `pkill vigo`,
an `rm` / `unlink` targeting `/srv/vigo/`, etc. Without `--force` the
CLI prints the reason and prompts:

```text
warning: server-side guardrail blocked this command
  reason:  command matches dangerous pattern
  pattern: systemctl stop vigo
  note:    the agent's hardcoded guardrails still apply on each envoy.
Proceed anyway? [y/N]
```

`--force` skips the prompt and bypasses the server-side check in one
step. Bypassed dispatches land on the audit hash chain as `task.dispatch`
with `{"forced":true}` in the payload; refused dispatches (operator
declined, no `--force`) land as `task.blocked` with the matched pattern.

The audit actor for both events is the **authenticated caller** (the API
token or session that made the request), derived server-side — it is not a
client-supplied value, so it cannot be forged.

`--force` does **not** bypass the agent's guardrails on each envoy —
they are hardcoded (`agent/src/guardrails.rs`) and not server-overridable.
A `--force`'d command the agent still refuses comes back as `rejected`
per target. Note what those guardrails guarantee: the **structural**
checks (file / service / package / process / systemd-drop-in resources,
matched against canonically-resolved targets) are a hard stop. The scan
of **free-form shell commands** (`exec`, ad-hoc pushes, and the embedded
commands of resources like `backup`/`cron`) is **best-effort** — a
heuristic that catches accidents and unsophisticated abuse but is
trivially evadable by a determined command author, so don't rely on it
as a boundary for arbitrary shell.

### Examples

#### Run an ad-hoc command

```bash
vigocli task run "uptime" --target "*.web.*"
```

```
Task t1a2b3c4 dispatched
--- web-01.example.com [complete] exit=0 42ms
 12:30:01 up 45 days,  3:22,  0 users,  load average: 0.15, 0.10, 0.05
--- web-02.example.com [complete] exit=0 38ms
 12:30:01 up 30 days,  1:15,  0 users,  load average: 0.22, 0.18, 0.12
```

#### Run a named task with parameters

```bash
vigocli task run restart-app --target "*.web.*" --param env=staging
```

#### Rolling execution with batch control

```bash
vigocli task run "systemctl restart nginx" --target "*.web.*" \
  --batch-size 25% --max-failures 2 \
  --health-check-url "http://lb.example.com/health"
```

#### Dry-run to preview batch plan

```bash
vigocli task run "uptime" --target "*.web.*" --batch-size 2 --dry-run
```

```
Dry-run batch plan:

  Batch 0: web-01.example.com, web-02.example.com
  Batch 1: web-03.example.com, web-04.example.com

2 batch(es), 4 target(s), 0 offline
```

## status

Show status and results for an existing task run.

```bash
vigocli task status <id>
```

### Example

```bash
vigocli task status t1a2b3c4
```

```
Task:     t1a2b3c4
Command:  uptime
Target:   *.web.*
Operator: admin
Status:   complete
Targets:  2
Created:  2026-03-13 12:30
Started:  2026-03-13 12:30
Finished: 2026-03-13 12:30

--- web-01.example.com [complete] exit=0 42ms
 12:30:01 up 45 days,  3:22,  0 users,  load average: 0.15, 0.10, 0.05
```

## list

List recent task runs.

```bash
vigocli task list [flags]
```

### Flags

| Flag | Short | Default | Description |
|------|-------|---------|-------------|
| `--limit` | | `20` | Maximum number of tasks to display |

### Example

```bash
vigocli task list --limit 5
```

```
ID          COMMAND            TARGET      STATUS     TARGETS  CREATED
t1a2b3c4    uptime             *.web.*     complete   4        2026-03-13 12:30
t5e6f7g8    systemctl resta... *.web.*     complete   4        2026-03-13 11:00
```

## Related

- [workflow](workflow.md) -- multi-step workflow orchestration
- [query](query.md) -- query trait values from envoys
- [runs](runs.md) -- view convergence run history
---
*Confidential -- Alexander4, LLC. Not for redistribution. See [license](../../legal/license.md).*
