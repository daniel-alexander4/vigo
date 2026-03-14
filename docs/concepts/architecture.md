# Architecture

Hobnob has three binaries, two network ports, and a filesystem-only config path.

## Three Binaries

```
┌─────────────────────────────────────────────────────────┐
│                    Admin Workstation                      │
│                                                          │
│  ┌──────────┐                                           │
│  │hobnobcli │──── REST API (HTTPS :8181) ──────┐       │
│  └──────────┘                                   │       │
└─────────────────────────────────────────────────┼───────┘
                                                  │
┌─────────────────────────────────────────────────┼───────┐
│                    Server                        │       │
│                                                  ▼       │
│  ┌──────────────────────────────────────────────────┐   │
│  │                    hobd                           │   │
│  │                                                    │   │
│  │  ┌──────────┐  ┌──────────┐  ┌──────────────┐   │   │
│  │  │ gRPC     │  │ REST API │  │  Web UI      │   │   │
│  │  │ :1530    │  │ :8181    │  │  :8181       │   │   │
│  │  │ (mTLS)   │  │          │  │  (HTMX)     │   │   │
│  │  └─────┬────┘  └──────────┘  └──────────────┘   │   │
│  │        │                                          │   │
│  │  ┌─────┴──────────────────────────────────────┐  │   │
│  │  │  FleetIndex │ ConfigStore │ RunStore       │  │   │
│  │  └─────┬──────────────────────────────────────┘  │   │
│  │        │                                          │   │
│  │  ┌─────┴────┐  ┌──────────┐  ┌──────────────┐   │   │
│  │  │ SQLite   │  │ Secrets  │  │ YAML Config  │   │   │
│  │  │ (WAL)    │  │ Provider │  │ (fsnotify)   │   │   │
│  │  └──────────┘  └──────────┘  └──────────────┘   │   │
│  └──────────────────────────────────────────────────┘   │
└─────────────────────────────────────────────────────────┘

┌─────────────────────────┐  ┌─────────────────────────┐
│     Thrall 1             │  │     Thrall 2             │
│                          │  │                          │
│  ┌──────────────────┐   │  │  ┌──────────────────┐   │
│  │     hobnob        │   │  │  │     hobnob        │   │
│  │   (Rust agent)    │   │  │  │   (Rust agent)    │   │
│  │                    │   │  │  │                    │   │
│  │  ┌──────────────┐ │   │  │  │  ┌──────────────┐ │   │
│  │  │ 42 Executors │ │   │  │  │  │ 42 Executors │ │   │
│  │  │ 38 Traits    │ │   │  │  │  │ 38 Traits    │ │   │
│  │  │ LMDB State   │ │   │  │  │  │ LMDB State   │ │   │
│  │  └──────────────┘ │   │  │  │  └──────────────┘ │   │
│  └────────┬─────────┘   │  │  └────────┬─────────┘   │
│           │              │  │           │              │
│     gRPC mTLS :1530      │  │     gRPC mTLS :1530      │
└───────────┼──────────────┘  └───────────┼──────────────┘
            │                             │
            └──────────┬──────────────────┘
                       │
                   To server
```

### hobd (server)

The central server written in Go. It:
- Accepts agent check-ins via gRPC over mTLS (port 1530)
- Serves the REST API, web UI, metrics, and bootstrap endpoint (port 8181)
- Reads YAML config from disk (`thrall-configs/`) with hot-reload via fsnotify
- Stores state in SQLite (default) or PostgreSQL
- Resolves secrets through a pluggable backend (local, isopass)

### hobnob (agent)

The Rust agent that runs on each managed machine (thrall). It:
- Checks in with the server on a configurable interval (default 5m)
- Receives desired state (modules + resources) from the server
- Applies resources idempotently using 59 built-in executors
- Reports results back to the server
- Collects 38 traits (hardware, OS, network, packages, etc.)
- Caches policy bundles in LMDB for offline convergence

### hobnobcli (CLI)

The admin CLI written in Go. It:
- Talks to the server's REST API
- Manages enrollment, tokens, approved patterns
- Dispatches ad-hoc tasks and live queries
- Runs workflows
- Views runs, compliance, thralls, packages, inventory

## Data Flows

### Check-in Flow

```
Agent                          Server
  │                              │
  │  1. CheckIn(UUID, traits,    │
  │     fingerprint)             │
  │─────────────────────────────>│
  │                              │  2. Match hostname → nodes.hnb
  │                              │  3. Resolve roles → modules
  │                              │  4. Merge vars + env_vars
  │                              │  5. Resolve secret: refs
  │                              │  6. Build module DAG
  │                              │  7. Render templates
  │  8. Return modules +         │
  │     resources + desired state│
  │<─────────────────────────────│
  │                              │
  │  9. Apply resources          │
  │     (check → act if drift)   │
  │                              │
  │  10. ReportResult(outcomes)  │
  │─────────────────────────────>│
  │                              │  11. Store run + update
  │                              │      compliance status
```

### Config Resolution

```
nodes.hnb                    roles/*.hnb              modules/*.hnb
┌───────────────┐           ┌──────────────┐         ┌──────────────┐
│ match: "web*" │───roles──>│ name: web    │──mods──>│ name: nginx  │
│ roles: [web]  │           │ modules:     │         │ resources:   │
│ vars:         │           │   - nginx    │         │   - package  │
│   port: 8080  │           │   - logrotate│         │   - file     │
└───────────────┘           └──────────────┘         │   - service  │
       │                                              └──────────────┘
       │ vars override
       ▼
  Module vars:     Node vars:      Final:
  port: 80    +    port: 8080  =   port: 8080
```

### Offline Convergence

```
Agent                          Server
  │                              │
  │  CheckIn ──────────────────X │  (server unreachable)
  │                              │
  │  Use cached policy bundle    │
  │  from LMDB state store       │
  │                              │
  │  Apply resources locally     │
  │                              │
  │  Queue results in LMDB       │
  │  pending queue               │
  │                              │
  │  ... server comes back ...   │
  │                              │
  │  Drain pending results ─────>│
  │  Resume normal check-in      │
```

## Key Subsystems

| Subsystem | Package | Purpose |
|-----------|---------|---------|
| FleetIndex | `server/fleet/` | In-memory thrall index, zero DB on check-in hot path |
| ConfigStore | `server/config/` | YAML loader with fsnotify hot-reload |
| RunStore | `server/db/` | Run history (DBRunStore or MemRunStore) |
| BundleSigner | `server/grpc/` | ED25519 signing for compiled promises |
| Registry | `server/registry/` | Enrollment: pattern match + token validation |
| Secrets | `server/secrets/` | Pluggable secret backend (local, isopass) |
| Compliance | `server/compliance/` | Per-thrall status: compliant/drifted/failing/stale/unknown |
| Federation | `server/federation/` | Hub-spoke scaling with enrollment routing |

## Related

- [Check-in Lifecycle](check-in-lifecycle.md) — Detailed check-in flow
- [Config Format](config-format.md) — Module, role, and node definitions
- [Compiled Promises](compiled-promises.md) — Bundle signing and offline convergence
- [FleetIndex](../operations/fleet-index.md) — In-memory index internals
