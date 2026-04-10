---
title: "ADR-011: Agent-driven swarm"
---

# ADR-011: Agent-driven swarm

**Status:** Accepted
**Date:** 2026-04-09

## Context

The original swarm design had the server as the central coordinator: the server assigned blobs to envoys during check-in, built per-envoy peer lists, and tracked assignment state in-memory. This created several failure modes:

1. If the server's peer list was stale or empty (PeerTracker TTL expiry, server restart), agents received `peers=0` and couldn't download.
2. Assignment state was in-memory only — lost on server restart, rebuilt gradually as envoys checked in.
3. The check-in handler performed per-envoy assignment loops and peer list construction on every cycle, adding work to the hot path.
4. Agents spawned download tasks per-checkin with ephemeral deduplication guards, causing duplicate downloads (observed: 521 concurrent tasks, 1.8G RAM on a single envoy).

## Decision

Shift the swarm architecture from "server routes, agents download" to "server seeds, agents self-assign and route."

**Server role reduced to:**
- Recording seeds in the manifest CRDT
- Gossipping manifest entries via check-in responses
- Providing peer hints (supplementary, not required)
- Tracking completion for the status API (via trait ingestion)

**Agent gains:**
- **SwarmManager** — long-lived tokio task that owns all download state. Receives assignments via channel, deduplicates, bounds concurrency (4 blobs, each with 8-chunk parallelism).
- **Self-assignment** — evaluates manifest target specs against local hostname and traits. No server assignment needed.
- **Persistent peer registry** — `peers.json` survives restarts. 5-minute TTL (up from 60s).
- **Active probing** — `GET /blob/{sha}/meta` to known peer addresses when no peers available.
- **Multicast solicitation** — `WANT:<sha256>` lines in multicast announcements.

## Alternatives Considered

**Keep server-mediated assignment, fix the bugs individually.** This would fix the immediate symptoms (stale peers, duplicate spawning) but leave the architectural dependency on the server for peer routing. The server restart and cross-subnet discovery problems would persist.

**Full DHT for peer discovery.** Overkill at fleet scale. The combination of multicast + peer exchange + active probing + server hints provides equivalent discovery without the complexity of maintaining a distributed hash table.

## Consequences

- The server's check-in handler is simpler — no more `collectSwarmAssignments()`, `PopAssignments()`, or per-envoy peer list construction.
- `vigocli swarm status` no longer shows a "pending" state (there are no server-side assignments). Envoys are either complete, downloading, or skipped.
- Vigosync (per-user file sync) needs migration to manifest-based self-assignment — it currently relies on per-envoy assignment for user-scoped targeting.
- The `SwarmAssignment` and `SwarmPeer` proto messages can be removed once the transition is verified.
