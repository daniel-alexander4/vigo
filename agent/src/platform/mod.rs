// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

//! Platform abstraction layer for cross-platform agent support.
//!
//! Provides unified interfaces for paths, permissions, process management,
//! and user/group lookup that compile to native implementations on each OS.

pub mod os_info;
pub mod paths;
pub mod permissions;
pub mod process;
pub mod users;
