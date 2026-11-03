// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: Apache-2.0

//! Localhost HTTP facades over curator artifacts (ADR-024).
//!
//! One agent-local listener on `127.0.0.1:<swarm.curator.local_port>` serves
//! two read-only facades over curator's content store, dispatched by URL
//! prefix:
//!
//! - the **Docker Registry v2 shim** (slice 7) — `docker pull
//!   127.0.0.1:<port>/<puddle-name>/<artifact>:<tag>` and docker-compose
//!   `image:` references against `oci-archive`-kind artifacts; and
//! - the **package-repo server** (slice 10) —
//!   `http://127.0.0.1:<port>/repo/<name>/...` serving an apt/dnf/apk repo
//!   tree out of an `apt-repo`/`dnf-repo`/`apk-repo`-kind artifact, consumed
//!   by a `repository` resource.
//!
//! ### Surface
//!
//! - [`oci_archive`] — read the OCI image-layout tarball that *is* an
//!   `oci-archive` curator artifact blob: pull out `index.json`, look up
//!   manifests by ref-name, stream any blob by sha256 digest. Sniffs and
//!   rejects legacy `docker save` format. Also hosts the generic
//!   read-a-file-from-a-tar helper both facades share.
//! - [`repo`] — serve a file by path out of a package-repo tarball blob.
//! - `routes` — request dispatch: `/v2/*` → the Docker shim, `/repo/*` → the
//!   package-repo facade; everything else → 404 / 405. Both facades are
//!   read-only (every mutating verb / non-GET-HEAD method → 405).
//! - lifecycle (`listener`) — binds `127.0.0.1:<port>` only, gated by
//!   `swarm.curator.{enabled,local_port}` on the envoy.
//!
//! [Docker Registry HTTP v2 API]: https://distribution.github.io/distribution/spec/api/

pub mod listener;
pub mod oci_archive;
pub mod repo;
pub mod routes;
