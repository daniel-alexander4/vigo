// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

//! Scrier tunnel relay — opens a dedicated gRPC TunnelStream on a separate
//! connection and relays bytes between the server and a localhost TCP service
//! (SSH, RDP, etc.).

pub mod relay;
