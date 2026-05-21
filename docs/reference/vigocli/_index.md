
# CLI Reference

Vigo has two CLI binaries:

- **`vigo`** -- the Rust agent that runs on managed machines. See [Agent CLI](agent.md) for daemon, register, bootstrap, run, status, traits, and version commands. The agent binary also houses the read-only [`vigo swarm` diagnostic verbs](agent-swarm.md) (`status` / `seeds {list,show}` / `peers list`) for envoy-side substrate inspection, the [`vigo swarm gitback` recovery CLI](agent-swarm-gitback.md) for inspecting and recovering personal-DR git mirrors, the [`vigo swarm lockbox` CLI](agent-swarm-lockbox.md) for managing [lockbox](../../howto/set-up-lockbox.md) — the per-user encrypted P2P directory, the [`vigo swarm puddle` CLI](agent-swarm-puddle.md) for managing [puddle](../../howto/set-up-puddle.md) — the per-user puddle identity primitive, and the [`vigo swarm curator` CLI](agent-swarm-curator.md) for publishing + consuming artifacts in the [curator](../../howto/set-up-curator.md) P2P artifact registry.
- **`vigocli`** -- the admin CLI that talks to the server's REST API (documented below).

---

## vigocli

`vigocli` communicates with the server's REST API to manage enrollment, envoys, operations, orchestration, secrets, and spanner.

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
| [preview](preview.md) | Dry-run to preview planned changes |
| [rollback](rollback.md) | Rollback a envoy to a previous state |
| [validate](validate.md) | Check .vgo files for YAML correctness and idempotency |
| [test](test.md) | Run config tests against expected outcomes |
| [inventory](inventory.md) | Query envoys by traits |
| [doctor](doctor.md) | Run health checks against the Vigo server |
| [risk](risk.md) | Show fleet-wide or per-envoy risk scores |
| [spanner](spanner.md) | Federated control-plane operations |
| [ask](ask.md) | Ask the AI assistant a question about your fleet |
| [config](config.md) | Config editing, inspection, publish history, rollback, and freeze |
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
| [swarm](../../howto/set-up-swarm.md) | Substrate-level view of the envoy P2P network (status + peers) |
| [payload](swarm-filecast.md) | Admin-pushed file distribution on the swarm substrate |
| [gitback](swarm-gitback.md) | Fleet-wide read-only view of gitback personal-DR mirrors |
| [longdrawer](swarm-longdrawer.md) | Fleet-wide read-only view of the longdrawer per-user directory sync mesh |
| [lockbox](swarm-lockbox.md) | Fleet-wide read-only view of the lockbox per-user encrypted directory sync mesh |
| [puddle](swarm-puddle.md) | Cross-envoy puddle ops — eviction + the fleet friendly-name map (ADR-022) |
| [curator](swarm-curator.md) | Fleet-wide read-only view of the [curator](../../howto/set-up-curator.md) P2P artifact registry + admin block / unblock (ADR-024) |
| [scrier](scrier.md) | Remote access — user mappings and interactive shell sessions |
| [sandgorgon](sandgorgon.md) | Bare-metal lifecycle — commission, provision, decommission |
| [security](security.md) | Fleet-wide security posture summary and findings |
| [compliance](compliance.md) | Fleet-wide compliance status by framework |
| `completion` | Generate shell completion scripts (bash, zsh, fish, powershell) |
---
*Confidential -- Alexander4, LLC. Not for redistribution. See [license](../../legal/license.md).*
