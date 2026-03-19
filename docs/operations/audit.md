# Audit Log

Vigo provides a unified audit view across config publishes, push runs, and workflow executions.

## Querying

### CLI

```bash
# List recent events
vigogo audit list

# Filter by time
vigogo audit list --since 24h

# Filter by operator and type
vigogo audit list --operator admin --type config_publish
```

### API

```
GET /api/v1/audit?since=24h&operator=admin&type=config_publish&limit=50
```

### Web UI

The Audit page (`/audit`) shows a filterable table of events with auto-refresh.

## Tamper-Evident Trail

Security-sensitive events (config changes, enrollment, revocation, push dispatch) are also recorded in a [tamper-evident hash chain](tamper-evident-audit.md). Each entry includes the SHA-256 hash of the previous entry, making modifications or deletions detectable. Verify chain integrity with `vigogo audit verify`, the REST API (`GET /api/v1/audit/verify`), or the web UI.

## Event Types

| Type | Source | Description |
|------|--------|-------------|
| `config_publish` | `config_publishes` table | Config publish with operator, files, and diff |
| `push_run` | `push_runs` table | Ad-hoc command dispatch |
| `workflow_run` | `workflow_runs` table | Multi-step workflow execution |

## Config Publish Attribution

Every `vigogo config publish` records:
- **Operator**: `--operator` flag or `$USER`
- **Changed files**: List of new/modified files
- **Removed files**: List of deleted files
- **Diff summary**: Line-level unified diff

View publish history:

```bash
vigogo config history
vigogo config history show <id>
```
