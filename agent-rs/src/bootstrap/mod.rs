// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: BUSL-1.1

pub mod client;
#[cfg(target_os = "linux")]
pub mod systemd;
#[cfg(windows)]
pub mod windows_service;
