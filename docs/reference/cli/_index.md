
# CLI Reference

Vigo has two CLI binaries:

- **`vigo`** -- the Rust agent that runs on managed machines. See [Agent CLI](agent.md) for daemon, register, bootstrap, run, status, traits, and version commands.
- **`vigogo`** -- the admin CLI that talks to the server's REST API (documented below).

---

## vigogo

`vigogo` communicates with the server's REST API to manage enrollment, envoys, operations, orchestration, secrets, and spanner.

## Global Flags

| Flag | Default | Description |
|------|---------|-------------|
| `--server` | `https://localhost:8443` | Base URL for the Vigo REST API |

## Commands

| Command | Description |
|---------|-------------|
| [approved](approved.md) | Manage approved enrollment patterns |
| [envoys](envoys.md) | Manage enrolled envoys (alias: `nodes`) |
| [tokens](tokens.md) | Manage enrollment tokens |
| [runs](runs.md) | View run history |
| [task](task.md) | Dispatch and monitor ad-hoc commands on envoys |
| [query](query.md) | Query trait values from targeted envoys |
| [workflow](workflow.md) | Run and monitor multi-step workflows |
| [packages](packages.md) | Query package audit information |
| [env](env.md) | Manage environment tier operations |
| [export](export.md) | Export compliance data (SIEM, CMDB, OSCAL) |
| [report](report.md) | Generate HIPAA compliance reports |
| [remediation](remediation.md) | Automated drift remediation |
| [webhooks](webhooks.md) | Manage webhook endpoints |
| [preview](preview.md) | Dry-run to preview planned changes |
| [rollback](rollback.md) | Rollback a envoy to a previous state |
| [validate](validate.md) | Check .vgo files for YAML correctness and idempotency |
| [test](test.md) | Run config tests against expected outcomes |
| [inventory](inventory.md) | Query envoys by traits |
| [doctor](doctor.md) | Run health checks against the Vigo server |
| [risk](risk.md) | Show fleet-wide or per-envoy risk scores |
| [spanner](spanner.md) | Manage and inspect spanner status |
| [ask](ask.md) | Ask the AI assistant a question about your fleet |
| [config](config.md) | Config inspection, publish history, rollback, and freeze |
| [secrets](secrets.md) | Manage local encrypted secrets |
| [webusers](webusers.md) | Manage web UI and API user accounts |
| [auth](auth.md) | Manage CLI authentication tokens |
| [audit](audit.md) | View unified audit log |
| [backup](backup.md) | Create and restore full server backups |
| [server](server.md) | Peer replication: promote a server to primary, check status |
| [db](db.md) | Inspect and maintain the SQLite database |
| [vi](vi.md) | Edit a .vgo file with syntax highlighting and validation |
| [cve](cve.md) | Search the fleet for hosts affected by a specific CVE |
| [license](license.md) | View license status and manage license keys |
