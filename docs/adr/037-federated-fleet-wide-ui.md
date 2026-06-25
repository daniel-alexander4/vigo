---
title: "ADR-037: Federated fleet-wide UI for spanner (any-bolt-federates read model)"
---

# ADR-037: Federated fleet-wide UI for spanner (any-bolt-federates read model)

**Status:** Approved — not yet implemented. Interim labeling shipped in `00a040f5` (the dashboard + envoys page now mark "this bolt's partition · fleet-wide: N" in spanner mode). This ADR covers the full federated-read arc that the labeling is a stopgap for.
**Date:** 2026-06-24.

## Context

In spanner mode every rich UI page — dashboard cards, envoys, CVE, risk, compliance, cert-expiry, agent-health — reads the local `DataSource` (this bolt's own FleetIndex/DB), so it shows only **this bolt's partition**. The only federation-wide view is `/spanner`, and it can aggregate solely because count + convergence are the only things peers gossip (`FleetSnapshot{Envoys []EnvoyLite, Convergence ConvergenceCounters}` — no traits/CVE/risk/compliance). The visible symptom: a 2-bolt fleet where the dashboard reads 1038 and `/spanner` reads 2056, with nothing telling the operator which is which.

This breaks the product's headline deliverables. "What is our PCI coverage / CVE exposure / risk posture / cert-expiry exposure?" are inherently **org-level** questions; with per-bolt-only views the operator must open N tabs and hand-merge — and not by simple addition (compliance % is a weighted average, CVE exposure needs dedup, risk is a distribution). The compliance report / cyber-insurance PDF are sold as organization-wide; generated per-bolt they are partial reports an auditor cannot use. Federating to handle *more* envoys would therefore degrade the product at its primary job — self-defeating. The hostname-pattern partition is an arbitrary sharding detail the operator never chose; a security question has no reason to be scoped to it.

## Decision

A centralized fleet-wide UI, built on a **peer-equal "any-bolt-federates" read model** — no hub, no aggregator node. Whichever bolt the operator reaches renders the fleet view by fanning out to its peers on demand over the existing `PeerDialer` + cpauth/mTLS channel (the admission roster pins each peer's pubkey). This preserves ADR-026 peer-equality: there is no special node, one login / one session / one cert / one tunnel reaches the whole fleet.

### Data model — on-demand fan-out of per-page summaries

When a fleet-scoped page loads, the viewing bolt fans out to each peer asking for **that page's summary**, computed locally by each bolt from its own data, and merges the returned summaries. What crosses the wire is kilobytes of rollup per bolt per page view, **only when a human is on the page**. The raw ~150 KB/envoy inventory is **never** replicated to peers — that would erase the partition's memory scale-out (the whole reason to federate).

This is **not** a generic query envelope. The cross-bolt channel is hard-typed gRPC `VigoAdmin` over `PeerDialer` (`server/spanner/peerconn.go`), so each fleet page needs **one typed RPC** (request/response in `protobuf/vigo.proto`, a handler on `AdminService` that dispatches to a local summary computation, and an `Aggregate<Thing>Fanout` wrapper over the generic `fanOut` driver, `server/spanner/adminreads.go`). Auth is free: the interceptor blanket-gates the whole `/vigo.VigoAdmin/` prefix through `cpauth.Verify` + roster-pin (`server/grpc/interceptor.go`), so a new read RPC rides the same mechanism as the existing mutating RPCs with no new auth code.

### Per-page class rules

- **Aggregate/reporting pages** (dashboard top-line, CVE, risk, compliance, cert-expiry, convergence): fan-out summaries + merge.
- **List pages** (envoys): fan-out + concatenate, with an **owning-bolt column**. Cheap columns can ride the already-gossiped `EnvoyLite`.
- **Detail pages** (one envoy / one run): **route to the owning bolt**, no merge — the roster resolves the owner (`OwningBoltHostname`).
- **Write actions** (push / revoke / delete / reassign): route to the owning bolt, reusing the existing spanner admin routing (`ForcePushToBolt`, `ReassignEnvoy`). **Note:** `DeleteEnvoy` currently has *no* such routing (silent no-op on the wrong bolt) — that gap must be closed as part of this work (tracked in `pending.md`).
- **Stateful/interactive pages** (scrier sessions, AI chat streaming): route to the owning bolt — they are inherently single-target and never merge.

### UX — Fleet + per-bolt tabs, federated-rendered

Top tabs `Fleet | bolt-1 | … | bolt-N`, all rendered **in the viewing bolt's own chrome** via federated reads (a `bolt-N` tab is the viewing bolt fetching bolt-N's data scoped to one bolt — the same RPCs, fanned to one peer instead of all). It is **not** a reverse-proxy of each bolt's literal served UI: proxying a full authenticated app (sessions, locked CSP `'self'`, scrier websockets, AI streaming) is heavy and fragile, and iframe/link-out is blocked by CSP, the per-bolt self-signed certs, and needing a tunnel per bolt. The tabs framing is strictly better than a binary "this-bolt ⇄ fleet" toggle — you can focus *any* bolt, not just the one you landed on. Default scope in spanner mode is **Fleet**.

Graceful partial results: the `fanOut` driver already gives this — a 5s per-peer timeout + panic isolation, returning partial results. Reachable bolts render; unreachable bolts are flagged ("N bolts unreachable") so a merged number is never *silently* undercounted.

## What is already solid (verified) vs. what must be built

**Solid — reuse as-is:**
- Transport: long-lived mTLS gRPC `VigoAdmin` via `PeerDialer`.
- The `fanOut` scatter driver + per-field merge + local fold-in is the canonical pattern (proven by `ListEnvoys` / `GetConvergenceSummary` / `ListRuns`). Do not invent a new aggregator.
- Auth: any new `VigoAdmin` read RPC is auto-gated by cpauth + mTLS + roster-pin.
- Convergence is already federated end-to-end.
- The scoring kernels (`server/cve`, `server/risk`, `server/certexpiry`, `server/convergence`) are pure and reusable; precedent for trusting cross-bolt aggregation already exists (license `aggregate` mode).

**Must be built — per-page extraction (the real work), sized by difficulty:**
1. **Convergence** — done (already fanned-out + merged).
2. **CVE summary, cert-expiry summary** — *small.* The summary fns exist inline in `server/fleet/datasource.go` (`RunningCVESummary`, `CertExpirySummary`); extract a callable + a proto message. Merge is trivial summation.
3. **Risk summary** — *medium.* `risk.ScoreFleet`/`ScoreEnvoy` are pure, but the per-bolt summary is embedded in `DataSource.RiskData`. The merge is non-trivial: averages and top-N can't be summed — each bolt must ship per-bolt **count + sum** (and its top-N candidates) and the viewer re-ranks. Extract accordingly.
4. **Compliance coverage** — *largest.* The aggregate exists (`aggregateFleetPartitions`/`BuildFrameworkReport`/`buildFrameworkCoverage`) but is tied to `ReportHandler`'s local `fleetIndex` + `configStore` + audit chain inside the ~4400-line `server/api/report.go`. A per-bolt summary struct (covered/waived/partial/missing per control are additive) must be extracted; **per-bolt audit-chain status does not aggregate cleanly** — see Open Questions.
5. **Fleet + per-bolt tabs UX** + the `DeleteEnvoy` owning-bolt routing fix.

Optionally, a `local`/no-RPC path for these pages (like convergence's gossip-cache aggregate) would require extending the gossiped `FleetSnapshot` beyond `Envoys`+`Convergence`. Not needed for v1 — fan-out-only is correct for on-demand viewing and avoids the continuous bandwidth/memory cost.

## Rejected alternatives

- **Designated aggregator / console node** — reintroduces a hub: single point of failure, one box holding everything, violates the peer-equal axiom.
- **Gossip everything (CVE/risk/compliance pushed continuously)** — continuous bandwidth + memory even when nobody's looking, always stale, and tempts replicating traits → defeats the partition's scale-out.
- **Reverse-proxy each bolt's literal UI behind a tab** — heaviest, most fragile surface (cross-bolt sessions/CSP/websockets/streaming).
- **iframe / link-out per bolt** — blocked by CSP `'self'`, per-bolt self-signed certs, and per-bolt tunnels.
- **Binary "this-bolt ⇄ fleet" toggle** — strictly worse than per-bolt tabs (can't focus an arbitrary bolt).

## Open questions / residual

- **Fleet-wide compliance is the hard slice.** Each bolt has its own tamper-evident audit hash chain; there is no single fleet audit chain, so a merged compliance view cannot present one aggregate "audit status." Resolution (deferred to the compliance slice): present coverage % merged across bolts but surface audit-chain status **per bolt** (not aggregated), with the merged report stating that the audit chain is per-bolt by construction. This is a fact about the design, not a defect — but it shapes how the compliance slice renders.

## Consequences

Multi-slice arc; each page is one typed RPC + a small-to-large extraction. No new architecture — the federated-read machinery, auth, and merge pattern already exist and are proven for convergence/envoys/runs. The interim labeling (`00a040f5`) keeps the per-bolt views honest until the Fleet tab lands.
