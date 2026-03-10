# Database Migrations

Database schema migrations for PostgreSQL 16, managed by golang-migrate.

- **Tool:** golang-migrate
- **Database:** PostgreSQL 16
- **Convention:** `000XXX_description.up.sql` / `000XXX_description.down.sql`
- **Migrations:** 18 total, defining the complete schema

## Running Migrations

```bash
# Apply all pending migrations
migrate -path migrations -database "postgres://localhost/hobnob?sslmode=disable" up

# Rollback last migration
migrate -path migrations -database "postgres://localhost/hobnob?sslmode=disable" down 1

# Check current version
migrate -path migrations -database "postgres://localhost/hobnob?sslmode=disable" version
```

Migrations run automatically on server startup.

## Creating New Migrations

```bash
migrate create -ext sql -dir migrations -seq <description>
```

## Migration History

| # | Name | Description |
|---|------|-------------|
| 001 | create_approved_patterns | Enrollment patterns with bcrypt-hashed tokens |
| 002 | create_clients | Managed machines (later renamed to nodes) |
| 003 | create_runs | Execution run records |
| 004 | create_run_results | Per-resource execution results |
| 005 | create_client_facts | Collected facts (later renamed) |
| 006 | create_plans | Dry-run plan records |
| 007 | create_plan_results | Dry-run planned actions |
| 008 | create_file_snapshots | File content for rollback |
| 009 | create_secret_rotation_events | Secret rotation audit trail |
| 010 | create_package_audit | Package version tracking |
| 011 | create_webhooks | Webhook subscriptions |
| 012 | add_force_push_to_clients | Force push flag on nodes |
| 013 | create_compliance_history | Daily compliance snapshots |
| 014 | create_env_promotions | Environment promotion audit |
| 015 | rename_facts_to_nails | Rename client_facts to client_nails |
| 016 | rename_clients_to_nodes | Rename clients to nodes, client_id to node_id |
| 017 | cascade_node_deletes | Add ON DELETE CASCADE to all node FKs |
| 018 | rename_nails_to_traits | Rename node_nails to node_traits |

## Current Schema

### Core Tables

- **nodes** -- id (UUID), hostname, ip_address, pubkey_pem, enrolled_at, last_seen, revoked, revoked_at, force_push
- **node_traits** -- id, node_id (FK CASCADE), traits (JSONB), collected_at
- **runs** -- id, node_id (FK CASCADE), started_at, finished_at, status, module/changed/failed/skipped counts
- **run_results** -- id, run_id (FK CASCADE), module_name, resource_type, resource_name, action_taken, changed, error_message, duration_ms

### Support Tables

- **approved_patterns** -- enrollment patterns with one-time bcrypt token hashes
- **file_snapshots** -- file content snapshots for rollback
- **plans** / **plan_results** -- dry-run plans and actions
- **webhooks** -- webhook subscriptions (url, secret, events[], active)
- **compliance_history** -- daily compliance snapshot counts
- **package_audit** -- package version tracking per node
- **secret_rotation_events** -- rotation audit trail
- **env_promotions** -- environment promotion records

All tables use UUID primary keys and TIMESTAMPTZ timestamps. All node-dependent tables have ON DELETE CASCADE.
