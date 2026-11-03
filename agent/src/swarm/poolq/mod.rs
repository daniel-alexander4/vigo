// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: Apache-2.0

//! poolq — the sixth swarm content subsystem (ADR-029).
//!
//! A Kafka-style ordered log on top of the existing trait → mesh → check-in
//! propagation pattern. Each topic is keyed
//! `topic_id = sha256(founder_puddle_pubkey ‖ topic_name)` (the gitback
//! `project_id_for` / curator `artifact_id_for` shape), founder-only-writable
//! in v1, fleet-readable. Messages are immutable signed envelopes carrying a
//! founder-monotonic `seq`; the readable log is the merged G-Set sorted by
//! `seq`; retention is age-based with stale-on-ingest refusal (no tombstones).
//!
//! ### Module layout
//!
//! - [`message`] — the signed `Message` envelope, `canonical_bytes`,
//!   `POOLQ_MSG_DOMAIN`, `topic_id_for`, `validate_topic_name`. The on-wire
//!   trust primitive.
//! - [`allocator`] — per-topic gap-free `SeqAllocator`, persisted at
//!   `~/.vigo-poolq/<topic_id_hex>/allocator.json`, flock-protected so
//!   concurrent posts from the same user on the same envoy serialize cleanly.
//! - [`state`] — per-user / per-topic on-disk paths and atomic write helpers.
//!   The Slice-3 trait collector walks these paths across users to surface
//!   messages to the server's [`server::swarm::poolqmesh`] aggregator.
//! - [`post`] — `publish()`, the entry point the Slice-4 CLI verb calls:
//!   validates body + size, allocates `seq` under flock, signs via a caller-
//!   supplied closure (the Slice-4 CLI threads the puddle session helper
//!   through it; tests pass an in-memory key), writes the message file
//!   atomically.
//!
//! ### No helper daemon
//!
//! Unlike curator, which has a `/var/run/vigo/curator.sock` root daemon for
//! blob staging into `/var/lib/vigo/swarm/blobs/` + manifest CRDT recording,
//! poolq has no root-only work: per-user state lives under `~/.vigo-poolq/`,
//! signing rides the existing per-user puddle session helper at
//! `$XDG_RUNTIME_DIR/vigo/puddle.sock`, and allocator atomicity is one
//! `flock(2)` on `allocator.json`. The trait collector (Slice 3) walks every
//! user's home as root — same as it would have either way. See ADR-029's
//! "No root helper daemon" line in the Decision section.

pub mod allocator;
pub mod cli;
pub mod message;
pub mod post;
pub mod state;
pub mod subscriptions;

pub use allocator::SeqAllocator;
pub use message::{
    canonical_bytes, msg_id_for, topic_id_for, validate_topic_name, Message, MessageBody,
    HARD_MAX_MSG_BYTES, ID_LEN, MAX_TOPIC_NAME_LEN, POOLQ_MSG_DOMAIN, SCHEMA_VERSION, SIG_LEN,
};
pub use post::{publish, PublishParams};
