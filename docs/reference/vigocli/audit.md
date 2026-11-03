
# audit

View the unified audit log across config publishes, task runs, and workflow executions.

## Subcommands

| Command | Description |
|---------|-------------|
| `audit list` | List recent audit events |
| `audit verify` | Verify audit log hash chain integrity |

## audit list

List recent audit events with optional filtering.

```bash
vigocli security audit list [--since <duration|RFC3339>] [--operator <name>] [--type <type>] [--limit <n>]
```

### Flags

| Flag | Default | Description |
|------|---------|-------------|
| `--since` | — | Time filter (duration like `24h` or RFC3339 timestamp) |
| `--operator` | — | Filter by operator name |
| `--type` | — | Filter by event type: `config_publish`, `task_run`, `workflow_run` |
| `--limit` | `50` | Maximum events to return |

### Example

```bash
$ vigocli security audit list --since 24h --type config_publish
TIMESTAMP              TYPE              OPERATOR  SUMMARY
2026-03-13 14:30:00    config_publish    admin     3 files changed
2026-03-13 09:15:00    config_publish    deploy    1 files changed
```

```bash
$ vigocli security audit list --operator admin --limit 10
TIMESTAMP              TYPE              OPERATOR  SUMMARY
2026-03-13 14:30:00    config_publish    admin     3 files changed
2026-03-13 12:00:00    task_run          admin     uptime on *.web.*
2026-03-13 10:00:00    workflow_run      admin     deploy-app (complete)
```

## audit verify

Verify the SHA-256 hash chain integrity of the audit log.

```bash
vigocli security audit verify
```

### Example

```bash
$ vigocli security audit verify
Audit chain: valid (1,234 entries)
```

If the chain is broken:

```bash
$ vigocli security audit verify
Audit chain: BROKEN at entry #567 — hash mismatch at entry 567: expected a1b2c3d4... got e5f6a7b8...
```
