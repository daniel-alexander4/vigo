# CLI Reference

`hobnobcli` is the administrative CLI for the Hobnob configuration management system. It communicates with the server's REST API to manage enrollment, thralls, operations, orchestration, secrets, and federation.

When invoked as `hnbvi`, it behaves as `hobnobcli vi <args>`.

## Global Flags

| Flag | Default | Description |
|------|---------|-------------|
| `--server` | `http://localhost:8181` | Base URL for the Hobnob REST API |

## Commands

| Command | Description |
|---------|-------------|
| [approved](approved.md) | Manage approved enrollment patterns |
| [thralls](thralls.md) | Manage enrolled thralls (alias: `nodes`) |
| [tokens](tokens.md) | Manage enrollment tokens |
| [push](push.md) | Trigger an immediate check-in for thralls |
| [runs](runs.md) | View run history |
| [task](task.md) | Dispatch and monitor ad-hoc commands on thralls |
| [query](query.md) | Query trait values from targeted thralls |
| [workflow](workflow.md) | Run and monitor multi-step workflows |
| [packages](packages.md) | Query package audit information |
| [env](env.md) | Manage environment tier operations |
| [export](export.md) | Export compliance data (SIEM, CMDB, OSCAL) |
| [webhooks](webhooks.md) | Manage webhook endpoints |
| [preview](preview.md) | Dry-run to preview planned changes |
| [rollback](rollback.md) | Rollback a thrall to a previous state |
| [validate](validate.md) | Check .hnb files for YAML correctness and idempotency |
| [test](test.md) | Run config tests against expected outcomes |
| [inventory](inventory.md) | Query thralls by traits |
| [license](license.md) | Manage the Hobnob license |
| [doctor](doctor.md) | Run health checks against the Hobnob server |
| [federation](federation.md) | Manage and inspect federation status |
| [ask](ask.md) | Ask the AI assistant a question about your fleet |
| [config](config.md) | Config inspection, publish history, rollback, and freeze |
| [secrets](secrets.md) | Manage local encrypted secrets |
| [users](users.md) | Manage web UI and API user accounts |
| [auth](auth.md) | Manage CLI authentication tokens |
| [audit](audit.md) | View unified audit log |
| [backup](backup.md) | Create and restore full server backups |
| [db](db.md) | Inspect and maintain the SQLite database |
| [vi](vi.md) | Edit a .hnb file with syntax highlighting and validation |
