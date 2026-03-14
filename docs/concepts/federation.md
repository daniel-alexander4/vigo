# Federation

Federation enables hub-spoke scaling for large fleets. A hub server aggregates data from spoke servers, each of which handles a partition of thralls.

> **License requirement:** Federation requires a paid license. The built-in free license (25 thralls) does not support hub or spoke mode. The server will refuse to start if `federation.role` is set to `hub` or `spoke` on the free license.

## Architecture

```
                    ┌──────────────────────────┐
                    │         Hub Server        │
                    │                            │
                    │  Web UI + CLI + Webhooks   │
                    │  Bootstrap + License       │
                    │  Admin Query Fan-out       │
                    └──────┬──────────┬─────────┘
                           │          │
                    gRPC   │          │   gRPC
                           │          │
              ┌────────────┴──┐  ┌────┴────────────┐
              │  Spoke DC1     │  │  Spoke DC2       │
              │                │  │                  │
              │  Agent check-in│  │  Agent check-in  │
              │  Result storage│  │  Result storage  │
              │  Trait storage │  │  Trait storage   │
              └───────┬────────┘  └───────┬──────────┘
                      │                    │
              ┌───────┴───────┐    ┌──────┴────────┐
              │  Thralls      │    │  Thralls       │
              │  *.dc1.*      │    │  *.dc2.*       │
              └───────────────┘    └────────────────┘
```

## Roles

### standalone (default)

Single server handles everything. No federation config needed.

### hub

The central server that:
- Serves the web UI, webhooks, bootstrap
- Routes enrollment to the correct spoke based on hostname patterns
- Fans out admin queries to all spokes and aggregates results
- Aggregates compliance and fleet statistics
- Handles license enforcement

```yaml
federation:
  role: hub
  spokes:
    - id: dc1
      addr: "spoke1.example.com:1530"
      patterns: ["*.dc1.*"]
    - id: dc2
      addr: "spoke2.example.com:1530"
      patterns: ["*.dc2.*"]
```

### spoke

A partition server that:
- Handles agent check-ins for its assigned thralls
- Stores runs, traits, and results locally
- Responds to admin queries from the hub
- Does not serve the web UI, webhooks, or bootstrap

```yaml
federation:
  role: spoke
  spoke_id: "dc1"
```

## Enrollment Routing

When a thrall bootstraps through the hub, the hub routes the enrollment to the correct spoke based on hostname pattern matching:

```
Thrall: web3.dc1.example.com
    ↓
Hub: match "*.dc1.*" → route to spoke dc1
    ↓
Spoke dc1: complete enrollment, store pubkey
```

## Admin Query Fan-out

CLI and API queries go through the hub, which fans out to all spokes:

```bash
hobnobcli thralls list
```

```
Hub receives request
    ↓
Fan out to spoke dc1 + spoke dc2 (parallel)
    ↓
Merge results
    ↓
Return to CLI
```

## Spoke Health

The hub monitors spoke health via periodic gRPC health checks. The web UI shows:
- Spoke status (online/offline)
- Per-spoke compliance breakdown
- Per-spoke latency metrics
- Thrall count per spoke

## Thrall Reassignment

Move thralls between spokes:

```bash
hobnobcli federation reassign --hostname "web3.dc1.*" --to dc2
```

Drain all thralls from a spoke:

```bash
hobnobcli federation drain --spoke dc1 --to dc2
```

The agent handles reconnection automatically — on next check-in, it's redirected to the new spoke.

## Spoke Detail

View detailed spoke information:

```bash
hobnobcli federation spoke dc1
```

The web UI has a spoke detail page at `/federation/spokes/{id}` showing per-spoke compliance, recent runs, and thrall list.

## Related

- [Architecture](architecture.md) — System overview
- [Server Configuration](../getting-started/server-config.md) — federation: section
