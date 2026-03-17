# Audit Log

Hobnob provides a unified audit view across config publishes, task runs, and workflow executions.

## Querying

### CLI

```bash
# List recent events
hobnobcli audit list

# Filter by time
hobnobcli audit list --since 24h

# Filter by operator and type
hobnobcli audit list --operator admin --type config_publish
```

### API

```
GET /api/v1/audit?since=24h&operator=admin&type=config_publish&limit=50
```

### Web UI

The Audit page (`/audit`) shows a filterable table of events with auto-refresh.

## Tamper-Evident Trail

Security-sensitive events (config changes, enrollment, revocation, task dispatch) are also recorded in a [tamper-evident hash chain](tamper-evident-audit.md). Each entry includes the SHA-256 hash of the previous entry, making modifications or deletions detectable. Verify chain integrity with `hobnobcli audit verify`, the REST API (`GET /api/v1/audit/verify`), or the web UI.

## Event Types

| Type | Source | Description |
|------|--------|-------------|
| `config_publish` | `config_publishes` table | Config publish with operator, files, and diff |
| `task_run` | `task_runs` table | Ad-hoc command dispatch |
| `workflow_run` | `workflow_runs` table | Multi-step workflow execution |

## Config Publish Attribution

Every `hobnobcli config publish` records:
- **Operator**: `--operator` flag or `$USER`
- **Changed files**: List of new/modified files
- **Removed files**: List of deleted files
- **Diff summary**: Line-level unified diff

View publish history:

```bash
hobnobcli config history
hobnobcli config history show <id>
```
