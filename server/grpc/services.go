// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

package grpc

import (
	"context"
	"log/slog"
	"time"

	pb "github.com/daniel-alexander4/vigo/gen/proto/vigo"
	"github.com/daniel-alexander4/vigo/server/config"
	"github.com/daniel-alexander4/vigo/server/db"
	"github.com/daniel-alexander4/vigo/server/spanner"
	"github.com/daniel-alexander4/vigo/server/fleet"
	"github.com/daniel-alexander4/vigo/server/license"
	"github.com/daniel-alexander4/vigo/server/registry"
	vigosmtp "github.com/daniel-alexander4/vigo/server/smtp"
	"github.com/daniel-alexander4/vigo/server/push"
	"github.com/daniel-alexander4/vigo/server/traits"
	"github.com/daniel-alexander4/vigo/server/webhook"
)

// auditRecorder is the interface for the tamper-evident audit writer.
type auditRecorder interface {
	Record(ctx context.Context, eventType, actor, source, summary, payload string) error
}

// AgentService implements the VigoAgent gRPC service.
type AgentService struct {
	pb.UnimplementedVigoAgentServer
	registry       *registry.Registry
	configStore    *config.ConfigStore
	queries        *db.Queries
	runStore       db.RunStore
	dispatcher     *webhook.Dispatcher
	smtpNotifier   *vigosmtp.Notifier
	traitsScanner   *traits.Scanner
	traitsBatch    *db.TraitsBatcher
	traitsPruner   *db.TraitsPruner
	policyCache    *policyCache
	streamMgr        *streamManager
	pushRouter       *pushRouter
	queryRouter      *queryRouter
	pushStore        *push.Store
	hub              *spanner.Hub    // non-nil when role=hub; routes enrollment to spokes
	licenseEnforcer  *license.Enforcer
	fleetIndex       *fleet.FleetIndex  // in-memory index for hot-path operations
	runBatcher       *fleet.RunBatcher  // async run persistence from delta events
	reassignStore    *ReassignStore     // pending envoy reassignments (spoke side)
	bundleSigner     *BundleSigner     // signs policy bundles for agent verification
	bundleMaxAge     uint32            // max_age_seconds for signed bundles (0 = forever)
	bundlePubKeyPEM  string            // cached PEM of bundle signing public key
	baseInterval     time.Duration     // base check-in interval (used for stale threshold)
	jitterPercent    int32             // jitter percentage sent to agents
	fallbackServers  []string          // peer server addresses returned to agents on registration
	serverVersion      string            // server/agent binary version — agents reporting a different version get force_update
	agentBinAvailable  func() bool // checks whether at least one agent binary exists for distribution
	auditWriter        auditRecorder     // tamper-evident audit writer (nil-safe)
	workflowTrigger    workflowTriggerer // auto-trigger workflows on trait changes (nil-safe)
}

// workflowTriggerer is the interface for auto-triggering workflows on trait changes.
type workflowTriggerer interface {
	TriggeredWorkflows(oldTraits, newTraits map[string]string) []string
	RunWorkflow(ctx context.Context, name, operator string) (string, error)
}

// SetAuditWriter attaches the tamper-evident audit writer.
func (s *AgentService) SetAuditWriter(w auditRecorder) {
	s.auditWriter = w
}

// SetWorkflowTrigger attaches the workflow engine for auto-triggered workflows.
func (s *AgentService) SetWorkflowTrigger(wt workflowTriggerer) {
	s.workflowTrigger = wt
}

// isRecentlyActive returns true if the envoy has checked in within 3× the check-in interval.
// Used to distinguish promotable (recently polling) from truly stale agents.
// The 3× margin covers one full interval + jitter + clock skew, ensuring an
// actively polling agent isn't misclassified as offline between check-ins.
func (s *AgentService) isRecentlyActive(envoyID string) bool {
	if s.fleetIndex == nil {
		return false
	}
	ts, ok, _ := s.fleetIndex.GetEnvoyLite(envoyID)
	if !ok {
		return false
	}
	staleThreshold := 3 * s.baseInterval
	if staleThreshold == 0 {
		staleThreshold = 15 * time.Minute // fallback
	}
	return time.Since(ts.LastSeen) <= staleThreshold
}

// recordAudit is a nil-safe helper that records an audit event.
func recordAudit(ctx context.Context, w auditRecorder, eventType, actor, source, summary, payload string) {
	if w == nil {
		return
	}
	if err := w.Record(ctx, eventType, actor, source, summary, payload); err != nil {
		slog.Error("audit record failed", "type", eventType, "error", err)
	}
}

// AdminService implements the VigoAdmin gRPC service.
type AdminService struct {
	pb.UnimplementedVigoAdminServer
	registry      *registry.Registry
	queries       *db.Queries
	runStore      db.RunStore
	configStore   *config.ConfigStore
	baseInterval  time.Duration
	hub           *spanner.Hub   // non-nil when role=hub; aggregates from spokes
	fleetIndex    *fleet.FleetIndex // in-memory index for hot-path operations
	reassignStore *ReassignStore    // pending envoy reassignments (spoke side)
	auditWriter   auditRecorder    // tamper-evident audit writer (nil-safe)
}

// SetAuditWriter attaches the tamper-evident audit writer.
func (s *AdminService) SetAuditWriter(w auditRecorder) {
	s.auditWriter = w
}

// formatDuration formats a time.Duration as a Go-style string (e.g. "5m0s").
func formatDuration(d time.Duration) string {
	return d.String()
}
