// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

package grpc

import (
	"context"
	"log/slog"
	"sync/atomic"
	"time"

	pb "git.alexander4.io/vigo/generated.code/proto/vigo"
	"git.alexander4.io/vigo/server/config"
	"git.alexander4.io/vigo/server/db"
	"git.alexander4.io/vigo/server/fleet"
	"git.alexander4.io/vigo/server/freshness"
	"git.alexander4.io/vigo/server/integrations"
	"git.alexander4.io/vigo/server/license"
	"git.alexander4.io/vigo/server/metrics"
	"git.alexander4.io/vigo/server/registry"
	"git.alexander4.io/vigo/server/spanner"
	"git.alexander4.io/vigo/server/swarm"
	"git.alexander4.io/vigo/server/task"
	"git.alexander4.io/vigo/server/traits"
)

// auditRecorder is the interface for the tamper-evident audit writer.
type auditRecorder interface {
	Record(ctx context.Context, eventType, actor, source, summary, payload string) error
}

// AgentService implements the VigoAgent gRPC service.
type AgentService struct {
	pb.UnimplementedVigoAgentServer
	registry      *registry.Registry
	configStore   *config.ConfigStore
	queries       *db.Queries
	runStore      db.RunStore
	integrations  *integrations.Dispatcher
	traitsScanner *traits.Scanner
	traitsBatch   *db.TraitsBatcher
	policyCache   *policyCache
	streamMgr     *streamManager
	taskRouter    *taskRouter
	queryRouter   *queryRouter
	taskStore     *task.Store
	// spannerAdmin drives the peer-equal enrollment routing (Slice
	// 2-ii-b). Non-nil when this vigosrv is in a spanner (mode !=
	// standalone). Register dispatches: spannerAdmin != nil →
	// OwningBolt redirect; else local-only.
	spannerAdmin     *spanner.SpannerAdmin
	licenseEnforcer  *license.Enforcer
	fleetIndex       *fleet.FleetIndex // in-memory index for hot-path operations
	runBatcher       *fleet.RunBatcher // async run persistence from delta events
	reassignStore    *ReassignStore    // pending envoy reassignments (bolt side)
	bundleSigner     *BundleSigner     // signs policy bundles for agent verification
	bundleMaxAge     uint32            // max_age_seconds for signed bundles (0 = forever)
	bundlePubKeyPEM  string            // cached PEM of bundle signing public key
	metrics          *metrics.Metrics  // per-instance prometheus collectors
	baseInterval     time.Duration     // base check-in interval (used for stale threshold)
	offlineThreshold time.Duration     // operator-configured override for the stale boundary (0 = default formula)
	jitterPercent    int32             // jitter percentage sent to agents
	fallbackServers  []string          // peer server addresses returned to agents on registration
	// serverVersions is a per-arch ("<goos>/<goarch>" → version) map of the
	// agent binary versions available for distribution. Auto-update fires only
	// when an agent reports a version that differs from the version known for
	// THAT agent's arch. See bootstrap.detectAgentVersions for population.
	serverVersions    map[string]string
	agentBinAvailable func() bool              // checks whether at least one agent binary exists for distribution
	auditWriter       auditRecorder            // tamper-evident audit writer (nil-safe)
	workflowTrigger   workflowTriggerer        // auto-trigger workflows on trait changes (nil-safe)
	streamEdit        config.StreamEditSection // stream_edit guardrails config
	globalObserveOnly bool                     // global observe-only from server.yaml
	tunnelMgr         *tunnelManager           // Scrier tunnel manager (nil when disabled)
	peerTracker       *swarm.PeerTracker       // swarm peer tracker (nil when swarm disabled)
	manifestStore     *swarm.ManifestStore     // swarm manifest CRDT (nil when swarm disabled)
	swarmCfg          config.SwarmSection      // swarm config section (subsystem blacklists, etc.)
	// curatorCatalog holds the JSON-encoded fleet artifact catalog (ADR-024) —
	// the same `[]curatormesh.CatalogEntry` the curator feed reactor (server/main.go)
	// produces every cycle for the swarm.ManifestStore feed. Attached verbatim to
	// every CheckInResponse so agents cache it and can resolve through a server
	// outage. Producer: the feed reactor via SetCuratorCatalog; consumers: the
	// check-in handler. nil until the first feed cycle (or when swarm is disabled).
	curatorCatalog atomic.Pointer[[]byte]
	// checkInSem caps concurrent CheckIn handlers. nil = unbounded (no
	// shedding). When the buffer is full, arrivals get ResourceExhausted
	// so the cascading-overload regime (where retries pile on the queue
	// faster than the server drains it) is prevented.
	checkInSem chan struct{}
	// reportResultSem is admission control for the ReportResult write path —
	// it caps how many result reports concurrently hit the single SQLite
	// writer, so a sustained result storm sheds fast (ResourceExhausted →
	// agent backs off) instead of growing an unbounded queue. Always set in
	// production (checkin.report_max_concurrent, or defaultReportResultConcurrency
	// when unset); nil only in tests that construct the service directly.
	reportResultSem chan struct{}
}

// tryAcquireCheckInSlot returns true when the caller may proceed and is
// responsible for releaseCheckInSlot. Returns false when the cap is
// reached — caller should return ResourceExhausted to the client.
func (s *AgentService) tryAcquireCheckInSlot() bool {
	if s.checkInSem == nil {
		return true
	}
	select {
	case s.checkInSem <- struct{}{}:
		return true
	default:
		s.metrics.CheckinsTotal.WithLabelValues("shed").Inc()
		return false
	}
}

func (s *AgentService) releaseCheckInSlot() {
	if s.checkInSem == nil {
		return
	}
	<-s.checkInSem
}

// defaultReportResultConcurrency is the ReportResult admission cap when
// checkin.report_max_concurrent is unset (0). It's a queue-depth bound in front
// of the single SQLite writer, far above normal in-flight concurrency, so it
// only trips under sustained overload where shedding is the correct response.
// Operators can override it (higher admits more before shedding — deeper queue,
// higher tail latency; lower sheds sooner), but it's never disabled.
const defaultReportResultConcurrency = 2048

// tryAcquireReportSlot mirrors tryAcquireCheckInSlot for the ReportResult
// write path. Returns false when the cap is reached — caller returns
// ResourceExhausted.
func (s *AgentService) tryAcquireReportSlot() bool {
	if s.reportResultSem == nil {
		return true
	}
	select {
	case s.reportResultSem <- struct{}{}:
		return true
	default:
		s.metrics.ReportsTotal.WithLabelValues("shed").Inc()
		return false
	}
}

func (s *AgentService) releaseReportSlot() {
	if s.reportResultSem == nil {
		return
	}
	<-s.reportResultSem
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

// SetStreamEditConfig sets the stream_edit guardrail configuration.
func (s *AgentService) SetStreamEditConfig(se config.StreamEditSection) {
	s.streamEdit = se
}

// StreamMgr returns the stream manager for use by the tunnel manager.
func (s *AgentService) StreamMgr() *streamManager {
	return s.streamMgr
}

// ForcePush sends a force-push to a specific streaming agent (or all if envoyID is empty),
// causing them to send a fresh CheckIn and receive any pending swarm assignments.
func (s *AgentService) ForcePush(envoyID string) int {
	if s.streamMgr == nil {
		return 0
	}
	return s.streamMgr.forcePush(envoyID)
}

// SetTunnelManager attaches the Scrier tunnel manager.
func (s *AgentService) SetTunnelManager(tm *tunnelManager) {
	s.tunnelMgr = tm
}

// TunnelManager returns the tunnel manager (nil when Scrier is disabled).
func (s *AgentService) TunnelManager() *tunnelManager {
	return s.tunnelMgr
}

// SetSwarm attaches the swarm peer tracker and manifest store.
func (s *AgentService) SetSwarm(pt *swarm.PeerTracker, ms *swarm.ManifestStore) {
	s.peerTracker = pt
	s.manifestStore = ms
}

// SetSwarmConfig stores the swarm config section so subsystem-level
// gates (e.g., gitback/lockbox blacklists) can be consulted at
// check-in time.
func (s *AgentService) SetSwarmConfig(sc config.SwarmSection) {
	s.swarmCfg = sc
}

// SetCuratorCatalog stores the JSON-encoded fleet artifact catalog the curator
// feed reactor produces each cycle; the check-in handler ships it to agents
// verbatim. Passing nil or an empty slice clears it. Cheap (atomic pointer
// swap) so the feed reactor can call it freely.
func (s *AgentService) SetCuratorCatalog(b []byte) {
	if len(b) == 0 {
		s.curatorCatalog.Store(nil)
		return
	}
	bb := b
	s.curatorCatalog.Store(&bb)
}

// curatorCatalogBytes returns the latest JSON-encoded catalog, or nil if the
// feed reactor hasn't produced one yet (or swarm is disabled).
func (s *AgentService) curatorCatalogBytes() []byte {
	if b := s.curatorCatalog.Load(); b != nil {
		return *b
	}
	return nil
}

// DispatchCleanup fans `vigo swarm _apply-cleanup` out to every envoy
// matching `target` (filepath.Match-style hostname pattern; "*" for
// the whole fleet). Returns the task-run ID and the number of envoys
// the dispatch reached. Implements api.CleanupDispatcher so the REST
// handler can consume it without importing the grpc package.
func (s *AgentService) DispatchCleanup(ctx context.Context, target string) (string, int, error) {
	if target == "" {
		target = "*"
	}
	res, err := s.DispatchPush(ctx, DispatchParams{
		Command:     "vigo swarm _apply-cleanup",
		TargetSpec:  target,
		Operator:    "swarm.cleanup",
		TimeoutSecs: 120,
	})
	if err != nil {
		return "", 0, err
	}
	return res.ID, res.Dispatched, nil
}

// isRecentlyActive returns true if the envoy has checked in recently
// enough that admin work (task / query dispatch) is worth trying.
// Delegates to `freshness.IsResponsive`, which threads the per-envoy
// observed CheckIn cadence (introduced in 0.38.0) through the same
// override + clamp rules the stale predicate uses, so admin-query
// routing and the UI's offline classification share one source of
// truth.
func (s *AgentService) isRecentlyActive(envoyID string) bool {
	if s.fleetIndex == nil {
		return false
	}
	ts, ok, _ := s.fleetIndex.GetEnvoyLite(envoyID)
	if !ok {
		return false
	}
	return freshness.IsResponsive(ts.LastSeen, s.baseInterval, ts.ObservedCycle(), s.offlineThreshold)
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
	registry         *registry.Registry
	queries          *db.Queries
	runStore         db.RunStore
	configStore      *config.ConfigStore
	metrics          *metrics.Metrics // per-instance prometheus collectors
	baseInterval     time.Duration
	offlineThreshold time.Duration
	// spannerAdmin drives the peer-equal fleet-observability admin
	// reads (Slice 2-ii-a). Non-nil when this vigosrv is in a spanner
	// (mode != standalone). The three fleet-wide reads dispatch:
	// spannerAdmin != nil → cache-aggregate (local) or fanout per
	// fallback_admin; else → pure-local path.
	spannerAdmin  *spanner.SpannerAdmin
	fleetIndex    *fleet.FleetIndex // in-memory index for hot-path operations
	reassignStore *ReassignStore    // pending envoy reassignments (bolt side)
	auditWriter   auditRecorder     // tamper-evident audit writer (nil-safe)
	integrations  *integrations.Dispatcher
	// swarmCleanup, when set, is invoked from RevokeEnvoy / DeleteEnvoy
	// after the DB update lands and before the response returns. It
	// dispatches lockbox recipient-list cleanup to every envoy so the
	// retired host's pubkey is dropped fleet-wide. Best-effort — failure
	// here logs a warning but doesn't fail the revoke/delete (the audit
	// chain has already recorded the operator action). Wired up in
	// main.go where AgentService.DispatchPush is in scope. Nil when
	// no agent dispatcher is available (rare; mostly tests).
	swarmCleanup func(ctx context.Context, hostname string) error
}

// SetAuditWriter attaches the tamper-evident audit writer.
func (s *AdminService) SetAuditWriter(w auditRecorder) {
	s.auditWriter = w
}

// SetSwarmCleanup attaches the cross-envoy lockbox cleanup callback
// fired after RevokeEnvoy / DeleteEnvoy unless the request opts out.
func (s *AdminService) SetSwarmCleanup(fn func(ctx context.Context, hostname string) error) {
	s.swarmCleanup = fn
}

// SetSpannerAdmin attaches the peer-equal admin-read driver (Slice
// 2-ii-a). Wired in main.go after the Gossiper is constructed (the
// driver shares the Gossiper's observability cache). nil leaves the
// three fleet-wide admin reads on their pure-local path.
func (s *AdminService) SetSpannerAdmin(a *spanner.SpannerAdmin) {
	s.spannerAdmin = a
}

// SetSpannerAdmin attaches the peer-equal admin driver to the agent
// service so Register can route enrollment via OwningBolt (Slice
// 2-ii-b). Wired alongside the AdminService setter. nil leaves
// enrollment on its pure-local path.
func (s *AgentService) SetSpannerAdmin(a *spanner.SpannerAdmin) {
	s.spannerAdmin = a
}

// formatDuration formats a time.Duration as a Go-style string (e.g. "5m0s").
func formatDuration(d time.Duration) string {
	return d.String()
}
