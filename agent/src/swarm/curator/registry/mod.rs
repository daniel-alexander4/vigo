// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

//! Localhost Docker Registry v2 shim over curator artifacts (ADR-024 slice 7).
//!
//! Lets `docker pull 127.0.0.1:<port>/<puddle-name>/<artifact>:<tag>` and
//! docker-compose `image:` references resolve against curator's content store.
//!
//! ### Surface
//!
//! - [`oci_archive`] — read the OCI image-layout tarball that *is* a curator
//!   artifact blob: pull out `index.json`, look up manifests by ref-name, stream
//!   any blob by sha256 digest. Sniffs and rejects legacy `docker save` format.
//! - `routes` (slice 7c) — the subset of [Docker Registry HTTP v2 API] we serve:
//!   `GET /v2/`, `GET /v2/<name>/manifests/<ref>`, `GET /v2/<name>/blobs/<digest>`,
//!   `GET /v2/<name>/tags/list`. Everything else → 405.
//! - lifecycle (slice 7d) — the listener binds `127.0.0.1:<port>` only,
//!   gated by `swarm.curator.{enabled,registry_port}` on the envoy.
//!
//! [Docker Registry HTTP v2 API]: https://distribution.github.io/distribution/spec/api/

pub mod listener;
pub mod oci_archive;
pub mod routes;
