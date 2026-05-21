// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

//! Vigo agent library crate — re-exports modules for use by fuzz targets
//! and integration tests.

pub mod auth;
pub mod bootstrap;
pub mod checkin;
pub mod config;
pub mod customtraits;
pub mod executors;
pub mod guardrails;
pub mod multicall;
pub mod platform;
pub mod policy;
pub mod runner;
pub mod snapshots;
pub mod state;
pub mod push;
pub mod swarm;
pub mod traits;
pub mod transport;
pub mod tunnel;
pub mod update;

pub mod proto {
    // Generated tonic/prost code; large_enum_variant is inherent to the
    // proto shape and cannot be addressed without editing generated code.
    #![allow(clippy::large_enum_variant)]
    tonic::include_proto!("vigo.v1");
}
