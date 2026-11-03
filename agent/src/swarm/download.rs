// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: Apache-2.0

//! Download utilities for swarm blob transfers.
//!
//! The swarm is purely peer-to-peer — there is no server download path.
//! Blob chunks are fetched from peers via the scheduler in `scheduler.rs`.
//! This module is reserved for shared download helpers if needed in the future.
