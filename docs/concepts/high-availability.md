# High availability

**What happens to the fleet if the server dies?** Less than you'd fear. Envoys keep converging from cache, and you have two independent ways to keep the control plane itself alive.

## First: the agent doesn't need the server moment-to-moment

This is the foundation. Each agent caches its last [PolicyBundle](architecture.md), its traits, and a results queue in an embedded LMDB store. When the server is unreachable, the agent **re-applies the cached bundle against fresh traits, queues results, and flushes on reconnect** — with exponential backoff and no crash. A server outage degrades observability and new pushes, not enforcement.

## Then: two ways to keep the server up

These solve different problems — don't pick one thinking it does the other's job.

| | **Peer replication** (`server/peer/`) | **[Spanner](spanner-federation.md)** (`server/spanner/`) |
|---|---|---|
| Problem | The server goes down | One server can't hold the whole fleet |
| Shape | Primary → standby replicas of the **same** fleet | Write-equal peers, **different** fleet slices |
| Replicates | `stack/`, `secrets/`, `tls/`, `server.yaml` | the admissions roster (CRDT); **not** secrets/TLS |
| Failover | `vigocli server promote` | n/a — peers are already equal |

You can run both: HA *within* a partition, spanner *across* partitions.

## Durable state underneath

The server's SQLite database runs in WAL mode with optional **Litestream** WAL replication to S3-compatible storage — so even a total host loss restores to a recent point. Backups (`vigocli backup`) capture the database, configs, secrets, and TLS as one unit.

## The recovery story, end to end

1. Server dies → envoys keep converging from cache, queuing results.
2. A standby is promoted (peer) or a sibling bolt already covers the slice (spanner).
3. The new primary restores state (replicated, or from Litestream/backup).
4. Envoys reconnect, flush their queued results, resume normal check-ins.

No envoy was ever "down" — only un-observed.

## Where this shows up

- [Backup and recovery](../howto/operate/backup-and-recovery.md) · [Disaster recovery](../howto/operate/disaster-recovery.md).
- [Spanner federation](spanner-federation.md) — the scale-out axis.
- [`vigocli server`](../reference/vigocli/server.md) · [`vigocli backup`](../reference/vigocli/backup.md).

---
*Confidential — Alexander4, LLC. Not for redistribution.*
