<!--
Copyright (c) 2026 Alexander4, LLC
SPDX-License-Identifier: LicenseRef-Proprietary
-->

# ADR-035: Application-layer gzip for large policy-bundle responses

**Status:** Accepted — implemented in 0.74.19.
**Date:** 2026-06-06

## Context

ADR-033 cut the *request* (agent→server traits) wire cost with the
vitals/inventory split and explicitly named gRPC-response compression as an
"orthogonal follow-on." The remaining wire cost on the server→agent side is the
**policy bundle**: large only on config change (a fresh agent, or a changed
configcrate carrying file content), absent on the steady-state `no_change`
heartbeat.

The obvious mechanism — register the gzip codec and call
`grpc.SetSendCompressor` — does **not** work selectively here. Bundles ride the
persistent `AgentStream` as a `CheckInResponse` inside a `ServerStreamMessage`,
which is the *same gRPC wire message* as every tiny ack (`no_change`
`check_in_response`, `report_result_ack`, `delta_ack`). grpc-go's
`SetSendCompressor` is set-once-at-stream-open with no per-message or size
threshold, so turning it on would gzip **every heartbeat and ack** fleet-wide at
1s cadence — adding framing bytes (gzip overhead exceeds savings on
tens-of-bytes messages) and per-message CPU on both ends for negative benefit,
on exactly the convergence-critical path CLAUDE.md protects. That is a
steady-state regression, not a win.

## Decision

Compress at the **application layer**, selectively, on the bundle payload only:

- New `CheckInResponse.policy_bundle_gzip` (`bytes`, field 26). The server
  marshals the signed `PolicyBundle`, and when the marshaled size exceeds
  `bundleCompressThreshold` (4 KiB) **and** gzip actually shrinks it, ships the
  gzipped bytes in this field and leaves the structured `policy_bundle` (field 7)
  nil. Below the threshold (or if compression doesn't pay) the structured field
  is sent as-is. Exactly one of the two fields is ever populated.
- The compression lives in `compressBundle`, called from `buildCheckInResponse`
  — the single builder shared by the unary `CheckIn` RPC and both stream send
  sites (handshake + per-checkin). One chokepoint covers every path.
- The agent inflates `policy_bundle_gzip` back into `policy_bundle` at every
  reception point (`inflate_policy_bundle`, called from the unary path, the
  stream handshake, and the stream read loop), so all downstream code stays
  bundle-shaped and unchanged.

## Consequences

- **Zero steady-state cost.** Bundles ship only on config change (the
  `no_change` path returns before the bundle is built), so compression never
  touches the heartbeat/ack hot path. This is the property the gRPC-codec
  approach could not provide.
- **Signature-transparent.** The bundle signature is over the bundle's *content
  fields* (`marshalBundleContent`, ADR's bundlesign), not the wire bytes, so the
  marshal→gzip→gunzip→unmarshal→verify round-trip preserves it. ADR-034's
  per-message verification is agent→server only and adds no server→agent
  envelope signature, so there is nothing else for compression to disturb.
- `flate2` becomes a direct agent dependency (already present transitively via
  `age`); the server uses Go stdlib `compress/gzip`. No tonic `gzip` feature is
  enabled.
- `StreamPolicyPush` is left untouched — it is dead code (config-change pushes go
  via `ForcePush` + agent re-check-in, not `StreamPolicyPush`), so the only live
  bundle carrier is `CheckInResponse`.

## Rejected alternatives

- **gRPC `SetSendCompressor` on the stream** — the mechanism trap above: per-
  stream, all-or-nothing, taxes every heartbeat/ack. Rejected on efficiency
  grounds (a steady-state regression), not customer demand.
- **Compress the whole `ServerStreamMessage` at the application layer** — would
  require compressing acks too (same selectivity problem one layer up) or a
  per-message-type branch; compressing just the bundle field is the minimal
  correct surface.
- **Bundle delta-transfer** (send only changed configcrates via
  `cached_configcrate_hashes`) — orthogonal and larger; compression composes on
  top of whatever is sent and is the smaller, complete win for this concern.
