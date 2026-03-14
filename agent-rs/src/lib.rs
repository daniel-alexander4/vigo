// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: Elastic-2.0

//! Hobnob agent library crate — re-exports modules for use by fuzz targets
//! and integration tests.

pub mod auth;
pub mod bootstrap;
pub mod checkin;
pub mod config;
pub mod customtraits;
pub mod executors;
pub mod guardrails;
pub mod platform;
pub mod policy;
pub mod runner;
pub mod snapshots;
pub mod state;
pub mod task;
pub mod traits;
pub mod transport;
pub mod update;

pub mod proto {
    tonic::include_proto!("hobnob.v1");
}
