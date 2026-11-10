// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: AGPL-3.0-only

package swarm

import "time"

// MeshCacheTTL is the default time-to-live for the per-aggregator
// snapshot cache shared by the five fleet-mesh aggregator packages
// (server/swarm/{gitback,curator,longdrawer,lockbox,poolq}mesh).
//
// Each Aggregator caches the joined fleet trait + manifest view for
// this duration so the dashboards + REST endpoints under
// /api/v1/swarm/* aren't recomputing the join on every read. The 30 s
// cadence matches the agent check-in floor — a fresher cache would
// race agent updates, a longer one would stale-display on the
// operator side.
//
// Lives in the parent `swarm` package as the single source of truth
// so any future tuning lands in one place and the five aggregators
// can't drift. Each consumer reads it as `swarm.MeshCacheTTL` in its
// constructor's `Aggregator{ ..., ttl: swarm.MeshCacheTTL }` init.
const MeshCacheTTL time.Duration = 30 * time.Second
