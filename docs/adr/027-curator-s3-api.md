---
title: "ADR-027: curator S3 object-storage API"
---

# ADR-027: curator S3 object-storage API

**Status:** Approved 2026-05-22 (`/discuss` → `/scrutinize` ×2, 2026-05-21; re-verified 2026-05-22). Slices 0–3 implemented 2026-05-22 — config + credential table, the derive-don't-store credential store, the SigV4 verifier, and the S3 endpoint (listener + read handlers); the API goes live at 0.66.28. Slice 4 (docs) follows.
**Date:** 2026-05-22

## Context

curator (ADR-024) distributes artifacts to *envoys* — every consumer so far is a Vigo agent (the `artifact:` executor param, the Docker Registry v2 shim, the package-repo facade, the self-update accelerator). There is no way for a **non-envoy** client — a CI runner, a developer laptop, anything holding an S3 SDK — to fetch a curator artifact. Those clients aren't Vigo nodes; `vigocli swarm curator pull` and the mTLS agent endpoints don't apply to them.

curator's content model — immutable, versioned, named artifacts addressed by content hash — *is* the S3 object model. And curator has already shown its read surface re-projects cleanly through a standard HTTP object protocol: the agent-side Docker Registry v2 shim (ADR-024 slice 7) does exactly that for container images. An S3-compatible API is the same move, server-side, for the general case. SMB/NFS was considered and dropped — an eventually-consistent P2P-synced store cannot honestly back a POSIX mount.

## Decision

A **read-only, S3-compatible HTTP API** on vigosrv exposing curator artifacts as S3 objects, so any S3 SDK works against curator unmodified.

### It is composition, not new protocol

curator already owns the read surface: `curatormesh.Aggregator.Snapshot()` is the catalog source of truth, `ResolveVersion`/`ResolvePlatform` already do key→blob resolution, and the existing blob relay serves bytes via `http.ServeContent` — which does HTTP Range natively, so ranged GET of 2 GiB blobs is free. The only genuinely new code is SigV4 verification.

### Surface

- **Read-only.** `GetObject` / `HeadObject` / `ListObjectsV2` / `ListBuckets` (+ minimal `HeadBucket` / `GetBucketLocation` init stubs). Every write and management verb returns `405 MethodNotAllowed` as a proper S3 XML error. Publishing stays `vigo swarm curator push` (puddle-signed).
- **Addressing.** One fixed bucket `vigo-curator`; object key `<publisher>/<artifact>/<version>/<os>-<arch>`. `<publisher>` is the puddle friendly-name (ADR-022) when the founder pubkey has an unambiguous claim, else the founder-pubkey hex. The version is explicit in the key (curator versions are named + immutable, so each key is a permanent pointer — no S3 `versionId`); tags (`stable`/`latest`) are alias keys. Explicit-key only — no os/arch auto-select on the S3 surface (that stays in the envoy resolver). The facade is `kind`-agnostic: it serves the raw blob bytes for any artifact kind.
- **Placement.** A dedicated, opt-in HTTPS listener — `swarm.curator.s3_port` (`0` = disabled, the default; the operator sets e.g. `9000`). Path-style addressing; reuses the server TLS cert/key. Not co-hosted on `:8443` — it carries its own SigV4 middleware and its own XML error format, incompatible with 8443's session-auth/JSON-error stack.

### Two auth paths — deliberately not unified

mTLS stays unchanged for the existing envoy↔server curator reads (`/api/v1/swarm/curator/resolve`, `/blob/`). **SigV4 authenticates only the new non-envoy S3 surface.** Envoys are not migrated to SigV4 — mTLS already authenticates them; a second per-envoy credential would be pure cost.

### SigV4 credentials — derive, don't store

The `secret_access_key` is **derived**, never persisted: `LocalProvider.DeriveKey("vigo-curator-s3-cred-v1", access_key_id, 32)` — recomputed at mint and again at every verify. The credential row (`curator_s3_credentials`) is pure metadata: `access_key_id` (PK), `scope`, `name`, `created_at`. This reuses Vigo's existing `DeriveKey` HKDF identity pattern (puddle / bolt / ops-puddle). `scope` is a `<publisher>`-prefix the credential may read (`*` = every publisher). Minted via `vigocli swarm curator s3-credentials {create,list,revoke}`; the secret is shown once at create; revoke = delete the row.

### Operations (v1)

- `GetObject` — blob bytes via `http.ServeContent` (Range supported, not required).
- `HeadObject` — `ETag` = the blob sha256 (curator is content-addressed, so this is free), `Content-Length` = blob size.
- `ListObjectsV2` — walks `curatormesh.Snapshot()`; `prefix` filter, `max-keys` + `continuation-token` pagination, `delimiter=/` common-prefixes.
- `ListBuckets` — the one fixed bucket. `HeadBucket` / `GetBucketLocation` — minimal init stubs S3 SDKs probe.
- Proper S3 XML errors with the correct `<Code>` for `NoSuchKey` / `NoSuchBucket` / `AccessDenied` / `SignatureDoesNotMatch` / `MethodNotAllowed` / `InvalidRange`.

Operator moderation is honored end-to-end: a `block`ed artifact is invisible and unfetchable over S3, exactly as on the resolve/blob paths.

## Consequences / tradeoffs

- **Requires the `local` secrets backend.** The SigV4 secret derivation roots in `LocalProvider`'s master key, so `isopass` / `vault` / cloud-KMS deployments do not get the S3 API in v1 — the same gating `compliancedocs` already does. With `swarm.curator.s3_port` set under a non-`local` backend, the listener refuses to start with a clear log line.
- **Master-key rotation invalidates every S3 credential at once.** A consequence of derive-don't-store — operators re-mint. Defensible (arguably a feature: one rotation revokes the whole credential set), but operators must know it. Documented.
- **The server owns S3 resolution.** A hard server outage stops S3 fetches — accepted, the same posture as the rest of the curator read path.
- **Two auth surfaces on curator.** mTLS for envoys, SigV4 for S3 clients. The duplication is deliberate and shallow — each authenticates the population it fits; unifying them would force a per-envoy SigV4 credential for no gain.

## Implementation notes

New package `server/curators3/` — the listener, the SigV4 verifier, the S3 read handlers, the XML error format, and the credential store. The `s3-credentials` management REST handler lives in `server/api/` (on `:8443`, admin-gated) and calls into `server/curators3`. Keeping the S3 *object* handlers out of `server/api/` also keeps them clear of the `TestRESTEndpointsDocumented` audit — the S3 object API is a separate protocol surface, documented in this ADR and the howto rather than `api.md`.

### v1 slices

- **Slice 0** — this ADR + `_index.md`; `CuratorSection.S3Port` (`server/config/types.go` + `server.yaml.example`); the `curator_s3_credentials` metadata table (migration + `server/db/queries/` + `make sqlc`).
- **Slice 1** — the SigV4 credential store (derive-don't-store, modeled on `server/auth/apitoken.go`'s `TokenStore` shape minus the secret column); `vigocli swarm curator s3-credentials {create,list,revoke}` + the backing admin-gated REST.
- **Slice 2** — **SigV4 verification, the security-critical slice.** `AWS4-HMAC-SHA256`: canonical request, the signing-key HMAC chain, constant-time signature compare, a 15-minute skew window. A faithful port of a known-good verifier, **gated by AWS's published SigV4 test vectors** — not a from-scratch reading of the spec. Runs as the S3 listener's auth middleware.
- **Slice 3** — the S3 endpoint: a dedicated opt-in HTTPS listener on `S3Port` (the `startHTTPServer` pattern, server TLS reused, goroutine + graceful shutdown alongside `:8443`); the read handlers over curator's read surface; path-style routing; writes/management → `405`; one structured slog line per request + a couple of Prometheus counters.
- **Slice 4** — docs (`docs/howto/set-up-curator.md` — the S3 API, minting credentials, pointing an SDK at it; the `vigocli swarm curator` reference) + CLAUDE.md Architecture-section update.

### Post-v1

Presigned-URL auth (`?X-Amz-…`); S3-endpoint rate-limiting; `isopass` / vault / cloud-KMS support (store/resolve the SigV4 secret through the secrets vault instead of rooting it in `LocalProvider`'s master key).
