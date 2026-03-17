// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

package grpc

import (
	"time"

	pb "github.com/hobnob/hobnob/gen/proto/hobnob"
	"github.com/hobnob/hobnob/server/config"
	"github.com/hobnob/hobnob/server/db"
	"github.com/hobnob/hobnob/server/spanner"
	"github.com/hobnob/hobnob/server/fleet"
	"github.com/hobnob/hobnob/server/license"
	"github.com/hobnob/hobnob/server/registry"
	hobnobsmtp "github.com/hobnob/hobnob/server/smtp"
	"github.com/hobnob/hobnob/server/tasks"
	"github.com/hobnob/hobnob/server/traits"
	"github.com/hobnob/hobnob/server/webhook"
)

// AgentService implements the HobnobAgent gRPC service.
type AgentService struct {
	pb.UnimplementedHobnobAgentServer
	registry       *registry.Registry
	configStore    *config.ConfigStore
	queries        *db.Queries
	runStore       db.RunStore
	dispatcher     *webhook.Dispatcher
	smtpNotifier   *hobnobsmtp.Notifier
	traitsScanner   *traits.Scanner
	traitsBatch    *db.TraitsBatcher
	traitsPruner   *db.TraitsPruner
	policyCache    *policyCache
	streamMgr        *streamManager
	taskRouter       *taskRouter
	queryRouter      *queryRouter
	taskStore        *tasks.Store
	hub              *spanner.Hub    // non-nil when role=hub; routes enrollment to spokes
	licenseEnforcer  *license.Enforcer
	fleetIndex       *fleet.FleetIndex  // in-memory index for hot-path operations
	runBatcher       *fleet.RunBatcher  // async run persistence from delta events
	reassignStore    *ReassignStore     // pending thrall reassignments (spoke side)
	bundleSigner     *BundleSigner     // signs policy bundles for agent verification
	bundleMaxAge     uint32            // max_age_seconds for signed bundles (0 = forever)
	bundlePubKeyPEM  string            // cached PEM of bundle signing public key
	baseInterval     time.Duration     // base check-in interval (used for stale threshold)
	jitterPercent    int32             // jitter percentage sent to agents
}

// isRecentlyActive returns true if the thrall has checked in within 2× the check-in interval.
// Used to distinguish promotable (recently polling) from truly stale agents.
func (s *AgentService) isRecentlyActive(thrallID string) bool {
	if s.fleetIndex == nil {
		return false
	}
	ts, ok, _ := s.fleetIndex.GetThrallLite(thrallID)
	if !ok {
		return false
	}
	staleThreshold := 2 * s.baseInterval
	if staleThreshold == 0 {
		staleThreshold = 10 * time.Minute // fallback
	}
	return time.Since(ts.LastSeen) <= staleThreshold
}

// AdminService implements the HobnobAdmin gRPC service.
type AdminService struct {
	pb.UnimplementedHobnobAdminServer
	registry      *registry.Registry
	queries       *db.Queries
	runStore      db.RunStore
	configStore   *config.ConfigStore
	baseInterval  time.Duration
	hub           *spanner.Hub   // non-nil when role=hub; aggregates from spokes
	fleetIndex    *fleet.FleetIndex // in-memory index for hot-path operations
	reassignStore *ReassignStore    // pending thrall reassignments (spoke side)
}

// formatDuration formats a time.Duration as a Go-style string (e.g. "5m0s").
func formatDuration(d time.Duration) string {
	return d.String()
}
