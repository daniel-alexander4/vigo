
# CLI Reference

Vigo has two CLI binaries:

- **`vigo`** -- the Rust agent that runs on managed machines. See [Agent CLI](agent.md) for daemon, register, bootstrap, run, status, traits, and version commands. The agent binary also houses the read-only [`vigo swarm` diagnostic verbs](agent-swarm.md) (`status` / `seeds {list,show}` / `peers list`) for envoy-side substrate inspection, the [`vigo swarm gitback` recovery CLI](agent-swarm-gitback.md) for inspecting and recovering personal-DR git mirrors, the [`vigo swarm lockbox` CLI](agent-swarm-lockbox.md) for managing [lockbox](../../howto/swarm/set-up-lockbox.md) — the per-user encrypted P2P directory, the [`vigo swarm puddle` CLI](agent-swarm-puddle.md) for managing [puddle](../../howto/swarm/set-up-puddle.md) — the per-user puddle identity primitive, the [`vigo swarm curator` CLI](agent-swarm-curator.md) for publishing + consuming artifacts in the [curator](../../howto/swarm/set-up-curator.md) P2P artifact registry, and the [`vigo swarm poolq` CLI](agent-swarm-poolq.md) for posting + consuming messages on the [poolq](../../howto/swarm/set-up-poolq.md) P2P ordered-log queue.
- **`vigocli`** -- the admin CLI that talks to the server's REST API (documented below).

---

## vigocli

`vigocli` communicates with the server's REST API to manage enrollment, envoys, operations, orchestration, secrets, and spanner.

## Global Flags

| Flag | Default | Description |
|------|---------|-------------|
| `--server` | `https://localhost:8443` | Base URL for the Vigo REST API |
| `--yes` | `false` | Skip the confirmation prompt on destructive commands (revoke, delete, purge, rollback, restore, drain, …). **Required for non-interactive use** — without a TTY and without `--yes`, a destructive command errors loudly instead of silently doing nothing. |

## Commands

Top-level commands, grouped by area. Several commands are **subcommands** of a
group (e.g. `vigocli security audit`, `vigocli traits inventory`); the detail
pages are linked under their parent.

### Enrollment & fleet
| Command | Description |
|---------|-------------|
| [approved](approved.md) | Manage approved enrollment patterns |
| [tokens](tokens.md) | Generate one-time enrollment tokens |
| [envoys](envoys.md) | Inspect + manage enrolled envoys — `list`, `show`, [`preview`](preview.md), `push`, [`revert`](rollback.md), `revoke`, `delete`, `purge`, `rebootstrap`, `maintenance` |
| [runs](runs.md) | View convergence run history |

### Config
| Command | Description |
|---------|-------------|
| [config](config.md) | Publish, inspect, and manage `.vgo` config — `publish`, `edit`, [`validate`](validate.md), `trace`, `tree`, `history`, `rollback`, `promote`, `freeze`, `search`, `stacks`, `server.yaml` |
| [test](test.md) | Run config tests against expected outcomes |

### Traits & inventory
| Command | Description |
|---------|-------------|
| [traits](traits.md) | Search + explore trait dot-paths — [`inventory`](inventory.md), [`livequery`](query.md), `search` |

### Operations
| Command | Description |
|---------|-------------|
| [task](task.md) | Dispatch + monitor ad-hoc commands on envoys |
| [workflow](workflow.md) | Run + monitor multi-step workflows |
| [scrier](scrier.md) | Remote access — user mappings and interactive sessions |
| [ask](ask.md) | Ask the AI assistant about your fleet |

### Security & compliance
| Command | Description |
|---------|-------------|
| [security](security.md) | Security posture — [`audit`](audit.md), [`cve`](cve.md), [`risk`](risk.md) |
| [compliance](compliance.md) | Compliance — [`report`](report.md), [`export`](export.md), `remediation`, `bundles`, `provisions` |

### Server administration
| Command | Description |
|---------|-------------|
| [doctor](doctor.md) | Server health checks (config, TLS, secrets, DB, license, spanner) |
| [secrets](secrets.md) | Manage AES-256-GCM encrypted secrets |
| [webusers](webusers.md) | Manage web UI + API user accounts |
| [auth](auth.md) | Manage this CLI's auth token |
| [backup](backup.md) | Create + restore full server backups |
| [server](server.md) | Peer replication — promote to primary, check status |
| [db](db.md) | Inspect + maintain the SQLite database |
| [license](license.md) | View + install the server license |
| [spanner](spanner.md) | Federated control-plane operations |
| [sandgorgon](sandgorgon.md) | Bare-metal lifecycle — commission, provision, decommission |
| `completion` | Generate shell completion scripts (bash, zsh, fish, powershell) |

### Swarm
`vigocli swarm` views the envoy P2P substrate and each content subsystem's fleet state.

| Command | Description |
|---------|-------------|
| [swarm](swarm.md) | Substrate-level view of the envoy P2P network (status + peers) |
| [swarm filecast](swarm-filecast.md) | Admin-pushed file distribution on the substrate |
| [swarm gitback](swarm-gitback.md) | Fleet-wide read-only view of gitback projects |
| [swarm longdrawer](swarm-longdrawer.md) | Fleet view of the longdrawer per-user directory sync |
| [swarm lockbox](swarm-lockbox.md) | Fleet view of the lockbox per-user encrypted directory sync |
| [swarm puddle](swarm-puddle.md) | Puddle fleet view + cross-envoy ops (eviction, names) |
| [swarm curator](swarm-curator.md) | Fleet view of the [curator](../../howto/swarm/set-up-curator.md) artifact registry + admin block (ADR-024) |
| [swarm poolq](swarm-poolq.md) | Fleet view of the [poolq](../../howto/swarm/set-up-poolq.md) ordered-log queue + admin block (ADR-029) |
