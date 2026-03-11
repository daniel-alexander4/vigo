# Database Migrations

Database schema migrations for SQLite, managed by golang-migrate.

- **Tool:** golang-migrate
- **Database:** SQLite (WAL mode)
- **Convention:** `000XXX_description.up.sql` / `000XXX_description.down.sql`
- **Migrations:** 1 consolidated migration defining the complete schema

## Running Migrations

```bash
# Apply all pending migrations
migrate -path migrations -database "sqlite3://db/hobnob.db" up

# Rollback last migration
migrate -path migrations -database "sqlite3://db/hobnob.db" down 1

# Check current version
migrate -path migrations -database "sqlite3://db/hobnob.db" version
```

Migrations run automatically on server startup.

## Creating New Migrations

```bash
migrate create -ext sql -dir migrations -seq <description>
```

## Current Schema

### Core Tables

- **nodes** -- id (UUID TEXT), hostname, ip_address, pubkey_pem, enrolled_at, last_seen, revoked, revoked_at, force_push
- **node_traits** -- id, node_id (FK CASCADE), traits (JSON TEXT), collected_at
- **runs** -- id, node_id (FK CASCADE), started_at, finished_at, status, module/changed/failed/skipped counts
- **run_results** -- id, run_id (FK CASCADE), module_name, resource_type, resource_name, action_taken, changed, error_message, duration_ms

### Support Tables

- **approved_patterns** -- enrollment patterns with one-time bcrypt token hashes
- **file_snapshots** -- file content snapshots for rollback
- **plans** / **plan_results** -- dry-run plans and actions
- **webhooks** -- webhook subscriptions (url, secret, events JSON, active)
- **compliance_history** -- daily compliance snapshot counts
- **package_audit** -- package version tracking per node
- **secret_rotation_events** -- rotation audit trail
- **env_promotions** -- environment promotion records

All tables use UUID TEXT primary keys and DATETIME timestamps. All node-dependent tables have ON DELETE CASCADE. Foreign keys enabled via `PRAGMA foreign_keys = ON`.
