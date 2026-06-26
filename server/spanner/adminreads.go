// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

package spanner

import (
	"context"
	"fmt"
	"log/slog"
	"math"
	"sort"
	"sync"
	"time"

	pb "git.alexander4.io/vigo/generated.code/proto/vigo"
	"git.alexander4.io/vigo/server/cve"
	"git.alexander4.io/vigo/server/daemon"
	"git.alexander4.io/vigo/server/db"
	"git.alexander4.io/vigo/server/fleetpressure"
)

// Spanner Slice 2-ii-a — peer-equal fleet-observability admin reads
// (ADR-026 + the spanner-redesign plan). SpannerAdmin replaces the
// hub's gRPC fan-out for the three fleet-wide admin reads (ListEnvoys,
// GetConvergenceSummary, ListRuns) with two peer-equal paths selected
// by spanner.fallback_admin:
//
//   - local (default) — aggregate from the gossip-backed observability
//     cache: (local fleetIndex) + (every cached peer). No live RPC.
//   - fanout — a client-side parallel fan-out to every roster bolt's
//     surviving per-bolt admin gRPC (ListEnvoys / ListRuns /
//     GetConvergenceSummary), merged. Mirrors the hub's fanOut
//     concurrency, driven via the Slice 2-ii-a PeerDialer.
//
// Runs are NEVER gossiped (plan "What's refused on purpose" #3) — so
// ListRuns(envoy_id) always routes to the envoy's owning bolt via the
// cached roster, and global ListRuns() always fans out to every
// roster bolt. fallback_admin gates only the cacheable reads
// (envoys/convergence); runs have no cache.
//
// SpannerAdmin is the sole driver of fleet-wide admin reads in a
// spanner — the gRPC layer (server/grpc/admin.go) dispatches to it
// whenever this vigosrv is a spanner bolt, and to the pure-local path
// otherwise.

// FallbackAdminLocal / FallbackAdminFanout are the spanner.fallback_admin
// values. local aggregates from the observability cache; fanout queries
// every roster bolt's admin gRPC live.
const (
	FallbackAdminLocal  = "local"
	FallbackAdminFanout = "fanout"
)

// defaultBoltGRPCPort is the gRPC port the fanout path assumes every
// bolt's VigoAdmin surface listens on — the documented server.yaml
// default (server.grpc_listen: ":1530"). The roster carries each
// bolt's hostname; the same-port assumption holds for every supported
// deployment shape, same assumption the Gossiper makes for the REST
// port.
const defaultBoltGRPCPort = "1530"

// fanOutTimeout bounds a single bolt's admin RPC during a fanout so one
// slow or unreachable bolt cannot stall the whole merge.
const fanOutTimeout = 5 * time.Second

// LocalConvergenceSource is the local bolt's own convergence counters,
// supplied by the in-memory fleet index. SpannerAdmin folds it together
// with every cached peer's counters for the local-path aggregate.
type LocalConvergenceSource func() ConvergenceCounters

// LocalEnvoySource is the local bolt's own envoy roster, supplied by
// the in-memory fleet index. SpannerAdmin folds it together with every
// cached peer's roster for the local-path aggregate.
type LocalEnvoySource func() []EnvoyLite

// SpannerAdmin drives the peer-equal fleet-observability admin reads.
// Construct with NewSpannerAdmin; nil is a valid no-op receiver
// (standalone mode — the gRPC layer falls through to its pure-local
// path and never calls these methods).
type SpannerAdmin struct {
	queries       *db.Queries // for the ListBolts roster (fanout target list + owning-bolt routing)
	observ        *ObservabilityCache
	dialer        *PeerDialer
	ownPubkey     string // local bolt pubkey-hex — skipped in cache aggregation + fanout
	fallbackAdmin string // FallbackAdminLocal | FallbackAdminFanout
	grpcPort      string // gRPC port assumed for every peer bolt

	localConv   LocalConvergenceSource
	localEnvoys LocalEnvoySource

	// dialPeers is the roster-peer connection opener. Defaults to
	// dialRosterPeersReal (one PeerConn per non-revoked, non-self
	// roster bolt via the PeerDialer). A test injects a stub here to
	// exercise the fanout merge logic without standing up mTLS gRPC
	// servers — the same-port assumption the real dialer makes is not
	// satisfiable with two in-process test servers.
	dialPeers func(ctx context.Context) ([]*PeerConn, error)

	// dialBoltFn is the single-bolt connection opener for the Slice
	// 2-ii-b action-propagation paths (enrollment redirect, force-push,
	// reassign, drain). Defaults to nil — dialBolt then falls through to
	// the real PeerDialer. A test injects a stub here to exercise the
	// action orchestration without an mTLS harness.
	dialBoltFn func(boltHostname string) (*PeerConn, error)
}

// NewSpannerAdmin constructs the peer-equal admin-read driver.
//
//   - queries: the local DB, for the spanner_bolts roster.
//   - observ: the gossip-backed observability cache (from the Gossiper).
//   - dialer: the PeerDialer for the fanout path (Slice 2-ii-a peerconn).
//   - ownPubkeyHex: this bolt's pubkey-hex, excluded from aggregation.
//   - fallbackAdmin: spanner.fallback_admin ("local" default | "fanout").
//   - grpcPort: the gRPC port to dial peers on; "" → defaultBoltGRPCPort.
func NewSpannerAdmin(queries *db.Queries, observ *ObservabilityCache, dialer *PeerDialer, ownPubkeyHex, fallbackAdmin, grpcPort string) *SpannerAdmin {
	if fallbackAdmin == "" {
		fallbackAdmin = FallbackAdminLocal
	}
	if grpcPort == "" {
		grpcPort = defaultBoltGRPCPort
	}
	a := &SpannerAdmin{
		queries:       queries,
		observ:        observ,
		dialer:        dialer,
		ownPubkey:     ownPubkeyHex,
		fallbackAdmin: fallbackAdmin,
		grpcPort:      grpcPort,
	}
	a.dialPeers = a.dialRosterPeersReal
	return a
}

// SetLocalSources injects the local-bolt observability sources used by
// the local (cache-aggregate) path. Wired in main.go where the fleet
// index is in scope.
func (a *SpannerAdmin) SetLocalSources(conv LocalConvergenceSource, envoys LocalEnvoySource) {
	a.localConv = conv
	a.localEnvoys = envoys
}

// Fanout reports whether this bolt is configured for the fanout
// admin-read path.
func (a *SpannerAdmin) Fanout() bool {
	return a != nil && a.fallbackAdmin == FallbackAdminFanout
}

// --- local (cache-aggregate) path ---

// AggregateEnvoysLocal returns the fleet-wide envoy roster aggregated
// from (local fleetIndex) + (every cached peer), and the age of the
// oldest contributing peer snapshot. The local roster is always live;
// peer rosters come from the gossip-backed observability cache.
func (a *SpannerAdmin) AggregateEnvoysLocal() (envoys []EnvoyLite, oldest time.Time) {
	if a.localEnvoys != nil {
		envoys = append(envoys, a.localEnvoys()...)
	}
	for _, p := range a.observ.All() {
		envoys = append(envoys, p.Envoys...)
	}
	return envoys, a.observ.OldestUpdate()
}

// AggregateConvergenceLocal returns the fleet-wide convergence counters
// aggregated from (local fleetIndex) + (every cached peer), and the age
// of the oldest contributing peer snapshot.
func (a *SpannerAdmin) AggregateConvergenceLocal() (counters ConvergenceCounters, oldest time.Time) {
	if a.localConv != nil {
		counters = a.localConv()
	}
	for _, p := range a.observ.All() {
		counters = counters.Add(p.Convergence)
	}
	return counters, a.observ.OldestUpdate()
}

// LocalPubkey returns this bolt's own pubkey-hex. The gossip cache
// deliberately excludes self, so observability surfaces that want to
// present every bolt equally (the /spanner page) fold in a local row
// keyed by this value.
func (a *SpannerAdmin) LocalPubkey() string {
	if a == nil {
		return ""
	}
	return a.ownPubkey
}

// LocalEnvoys returns this bolt's OWN envoy roster from the live local
// fleet index (nil if no source is wired). Unlike AggregateEnvoysLocal
// it does not fold in peers — it is the local bolt's row alone.
func (a *SpannerAdmin) LocalEnvoys() []EnvoyLite {
	if a == nil || a.localEnvoys == nil {
		return nil
	}
	return a.localEnvoys()
}

// LocalConvergence returns this bolt's OWN convergence counters from the
// live local fleet index (zero value if no source is wired). Local-only,
// not aggregated with peers.
func (a *SpannerAdmin) LocalConvergence() ConvergenceCounters {
	if a == nil || a.localConv == nil {
		return ConvergenceCounters{}
	}
	return a.localConv()
}

// OwningBoltHostname resolves which bolt serves a given envoy via the
// observability cache's non-copying OwnerOf lookup. Returns the peer
// bolt's pubkey-hex + hostname and true if a remote peer serves the
// envoy; returns ("","",false) when no cached peer claims it (the
// envoy is local, or unknown — the caller falls back to the local run
// store).
func (a *SpannerAdmin) OwningBoltHostname(ctx context.Context, envoyID string) (pubkeyHex, hostname string, remote bool) {
	key, ok := a.observ.OwnerOf(envoyID)
	if !ok {
		return "", "", false
	}
	return key, hostnameOfPeer(ctx, a, key), true
}

// hostnameOfPeer resolves a peer bolt's hostname from the local
// spanner_bolts roster given its pubkey-hex. The hostname is read from
// the roster row, never from gossiped observability data — the roster
// is the authenticated source.
func hostnameOfPeer(ctx context.Context, a *SpannerAdmin, pubkeyHex string) string {
	roster, err := a.queries.ListBolts(ctx)
	if err != nil {
		return ""
	}
	for _, b := range roster {
		if b.Pubkey == pubkeyHex {
			return b.Hostname
		}
	}
	return ""
}

// PeerObservability returns the cached observability entry for one
// peer bolt by pubkey-hex, or nil if the peer has never reported. The
// REST bolt-drilldown surfaces use this in peer-equal mode.
func (a *SpannerAdmin) PeerObservability(boltPubkeyHex string) *PeerObservability {
	return a.observ.Get(boltPubkeyHex)
}

// AllPeerObservability returns every cached peer's observability
// entry, keyed by bolt pubkey-hex.
func (a *SpannerAdmin) AllPeerObservability() map[string]*PeerObservability {
	return a.observ.All()
}

// BoltIdentity is one admitted bolt's stable identity for UI labelling —
// the authenticated roster row (pubkey + hostname), with IsLocal marking
// this bolt. The UI scope tabs (ADR-037 "Fleet | bolt-N") build their
// per-bolt tabs from this; hostnames come from the roster, never from
// gossiped observability data.
type BoltIdentity struct {
	Pubkey   string
	Hostname string
	IsLocal  bool
}

// BoltRoster returns every non-revoked bolt in the local spanner_bolts
// roster (including this bolt, flagged IsLocal), ordered local-first then
// by hostname. Drives the ADR-037 federated-UI scope tabs. Returns nil on
// a standalone (nil receiver) or a roster read error.
func (a *SpannerAdmin) BoltRoster(ctx context.Context) ([]BoltIdentity, error) {
	if a == nil {
		return nil, nil
	}
	roster, err := a.queries.ListBolts(ctx)
	if err != nil {
		return nil, fmt.Errorf("listing spanner roster: %w", err)
	}
	out := make([]BoltIdentity, 0, len(roster))
	for _, b := range roster {
		if b.RevokedAt.Valid {
			continue
		}
		out = append(out, BoltIdentity{
			Pubkey:   b.Pubkey,
			Hostname: b.Hostname,
			IsLocal:  b.Pubkey == a.ownPubkey,
		})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].IsLocal != out[j].IsLocal {
			return out[i].IsLocal // local bolt first
		}
		return out[i].Hostname < out[j].Hostname
	})
	return out, nil
}

// --- fanout path ---

// fanOutResult carries one bolt's admin-RPC outcome during a fanout.
type fanOutAdminResult struct {
	pubkey string
	value  any
	err    error
}

// dialRosterPeersReal dials every non-revoked, non-self bolt in the
// local spanner_bolts roster via the PeerDialer. The caller must Close
// every returned PeerConn. A dial that fails on a malformed target is
// skipped (logged) — a fanout over the rest still succeeds. This is
// the default a.dialPeers; tests swap in a stub.
func (a *SpannerAdmin) dialRosterPeersReal(ctx context.Context) ([]*PeerConn, error) {
	roster, err := a.queries.ListBolts(ctx)
	if err != nil {
		return nil, fmt.Errorf("listing spanner roster: %w", err)
	}
	conns := make([]*PeerConn, 0, len(roster))
	for _, b := range roster {
		if b.Pubkey == a.ownPubkey || b.RevokedAt.Valid {
			continue
		}
		addr := b.Hostname + ":" + a.grpcPort
		pc, dErr := a.dialer.Dial(b.Pubkey, addr, nil)
		if dErr != nil {
			slog.Warn("spanner fanout: dialing peer failed; skipping", "peer", b.Hostname, "error", dErr)
			continue
		}
		conns = append(conns, pc)
	}
	return conns, nil
}

// fanOut runs fn against every roster peer concurrently and collects
// the results — a client-side parallel fan-out driven via the
// PeerDialer. Each call gets its own short timeout so one slow bolt
// cannot stall the merge.
func (a *SpannerAdmin) fanOut(ctx context.Context, fn func(ctx context.Context, pc *PeerConn) (any, error)) ([]fanOutAdminResult, error) {
	conns, err := a.dialPeers(ctx)
	if err != nil {
		return nil, err
	}
	defer func() {
		for _, pc := range conns {
			pc.Close()
		}
	}()

	results := make([]fanOutAdminResult, len(conns))
	var wg sync.WaitGroup
	for i, pc := range conns {
		wg.Add(1)
		go func(idx int, conn *PeerConn) {
			defer wg.Done()
			defer daemon.Recover("spanner-adminread", func(r any) {
				results[idx] = fanOutAdminResult{pubkey: conn.ID, err: fmt.Errorf("peer admin read panicked: %v", r)}
			})
			callCtx, cancel := context.WithTimeout(ctx, fanOutTimeout)
			defer cancel()
			val, cErr := fn(callCtx, conn)
			results[idx] = fanOutAdminResult{pubkey: conn.ID, value: val, err: cErr}
		}(i, pc)
	}
	wg.Wait()
	return results, nil
}

// AggregateEnvoysFanout fans out ListEnvoys to every roster bolt's
// admin gRPC and merges the results with the local bolt's own envoy
// roster. fanout queries peers live so there is no snapshot age — the
// returned envoys are current.
func (a *SpannerAdmin) AggregateEnvoysFanout(ctx context.Context) ([]*pb.Envoy, error) {
	results, err := a.fanOut(ctx, func(ctx context.Context, pc *PeerConn) (any, error) {
		resp, rErr := pc.Admin.ListEnvoys(ctx, &pb.ListEnvoysRequest{})
		if rErr != nil {
			return nil, rErr
		}
		return resp.Envoys, nil
	})
	if err != nil {
		return nil, err
	}
	var out []*pb.Envoy
	for _, r := range results {
		if r.err != nil {
			slog.Warn("spanner fanout: peer ListEnvoys failed", "peer", r.pubkey, "error", r.err)
			continue
		}
		out = append(out, r.value.([]*pb.Envoy)...)
	}
	return out, nil
}

// AggregateConvergenceFanout fans out GetConvergenceSummary to every
// roster bolt and sums the results with the local bolt's own counters
// (the local counters are summed in by the caller via localConv —
// here only the peer sums are returned, so the gRPC layer can fold in
// the local-side numbers consistently with the local path).
func (a *SpannerAdmin) AggregateConvergenceFanout(ctx context.Context) (*pb.GetConvergenceSummaryResponse, error) {
	results, err := a.fanOut(ctx, func(ctx context.Context, pc *PeerConn) (any, error) {
		return pc.Admin.GetConvergenceSummary(ctx, &pb.GetConvergenceSummaryRequest{})
	})
	if err != nil {
		return nil, err
	}
	agg := &pb.GetConvergenceSummaryResponse{}
	for _, r := range results {
		if r.err != nil {
			slog.Warn("spanner fanout: peer GetConvergenceSummary failed", "peer", r.pubkey, "error", r.err)
			continue
		}
		resp := r.value.(*pb.GetConvergenceSummaryResponse)
		agg.Total += resp.Total
		agg.Converged += resp.Converged
		agg.Degraded += resp.Degraded
		agg.Failed += resp.Failed
		agg.NoData += resp.NoData
		agg.Changed += resp.Changed
		agg.Diverged += resp.Diverged
		agg.Offline += resp.Offline
	}
	return agg, nil
}

// AggregateRunsFanout fans out ListRuns to every roster bolt and merges
// the results. envoyID may be empty (all runs) or a specific envoy.
func (a *SpannerAdmin) AggregateRunsFanout(ctx context.Context, envoyID string, limit int32) ([]*pb.Run, error) {
	results, err := a.fanOut(ctx, func(ctx context.Context, pc *PeerConn) (any, error) {
		resp, rErr := pc.Admin.ListRuns(ctx, &pb.ListRunsRequest{EnvoyId: envoyID, Limit: limit})
		if rErr != nil {
			return nil, rErr
		}
		return resp.Runs, nil
	})
	if err != nil {
		return nil, err
	}
	var out []*pb.Run
	for _, r := range results {
		if r.err != nil {
			slog.Warn("spanner fanout: peer ListRuns failed", "peer", r.pubkey, "error", r.err)
			continue
		}
		out = append(out, r.value.([]*pb.Run)...)
	}
	return out, nil
}

// --- security summary (ADR-037 slice 2, fan-out-only — never gossiped) ---

// CVEIdent is one deduped CVE on a bolt: the CVE ID and the highest
// severity any host on that bolt reported it at. The federated rollup
// unions these by ID across bolts (max severity wins) so the fleet
// unique-CVE count is correct rather than a double-counting sum.
type CVEIdent struct {
	ID       string
	Severity string
}

// SecuritySummary is a bolt's security-posture rollup for the federated
// dashboard Security Posture column. UniqueCVEs is the per-bolt deduped
// CVE set (unioned across bolts at merge time); every other field is an
// additive per-host count (a host belongs to one bolt → summable).
type SecuritySummary struct {
	UniqueCVEs              []CVEIdent
	CertsExpiringSoon       int
	CertsExpired            int
	CertsExpiringIn7        int
	CertsExpiringIn30       int
	CertHostsAffected       int
	RootkitWarnings         int
	RunningCVEsTotal        int
	RunningCVEsCritical     int
	RunningCVEsHigh         int
	RunningCVEsMedium       int
	RunningCVEsLow          int
	RunningCVEHostsAffected int
	// Scanner rollups for the federated /security PAGE Fleet view (ADR-037
	// slice 5). Additive across bolts except the Lynis average (carried as
	// LynisScoreSum + LynisScoredCount for a correct re-mean).
	LynisScoreSum    int64
	LynisScoredCount int
	LynisWarnings    int
	RootkitClean     int
	ClamavClean      int
	ClamavInfected   int
	AideClean        int
	AideChanged      int
	ScapScanned      int
	ScapClean        int
	ScapHighFail     int
	ScannedHosts     int
	TotalHosts       int
}

// LynisAvg is the fleet (or per-bolt) mean Lynis hardening score, re-meaned
// from the un-divided sum/count so an average-of-averages can't skew it.
func (s SecuritySummary) LynisAvg() int {
	if s.LynisScoredCount == 0 {
		return 0
	}
	return int(s.LynisScoreSum / int64(s.LynisScoredCount))
}

// UniqueCVECount is the deduped unique-CVE total (len of the set after any
// merge). Surfaced as the dashboard's headline unique-CVE number.
func (s SecuritySummary) UniqueCVECount() int { return len(s.UniqueCVEs) }

// MergeSecuritySummaries unions the per-bolt deduped CVE sets (max severity
// per CVE ID) and sums the additive counts. The result's UniqueCVEs is the
// fleet-deduped set — summing per-bolt unique counts would double-count a
// CVE present on hosts across two bolts.
func MergeSecuritySummaries(parts []SecuritySummary) SecuritySummary {
	cveMax := make(map[string]string)
	var out SecuritySummary
	for _, p := range parts {
		for _, c := range p.UniqueCVEs {
			if cur, ok := cveMax[c.ID]; !ok || cve.Classify(c.Severity) > cve.Classify(cur) {
				cveMax[c.ID] = c.Severity
			}
		}
		out.CertsExpiringSoon += p.CertsExpiringSoon
		out.CertsExpired += p.CertsExpired
		out.CertsExpiringIn7 += p.CertsExpiringIn7
		out.CertsExpiringIn30 += p.CertsExpiringIn30
		out.CertHostsAffected += p.CertHostsAffected
		out.RootkitWarnings += p.RootkitWarnings
		out.RunningCVEsTotal += p.RunningCVEsTotal
		out.RunningCVEsCritical += p.RunningCVEsCritical
		out.RunningCVEsHigh += p.RunningCVEsHigh
		out.RunningCVEsMedium += p.RunningCVEsMedium
		out.RunningCVEsLow += p.RunningCVEsLow
		out.RunningCVEHostsAffected += p.RunningCVEHostsAffected
		out.LynisScoreSum += p.LynisScoreSum
		out.LynisScoredCount += p.LynisScoredCount
		out.LynisWarnings += p.LynisWarnings
		out.RootkitClean += p.RootkitClean
		out.ClamavClean += p.ClamavClean
		out.ClamavInfected += p.ClamavInfected
		out.AideClean += p.AideClean
		out.AideChanged += p.AideChanged
		out.ScapScanned += p.ScapScanned
		out.ScapClean += p.ScapClean
		out.ScapHighFail += p.ScapHighFail
		out.ScannedHosts += p.ScannedHosts
		out.TotalHosts += p.TotalHosts
	}
	for id, sev := range cveMax {
		out.UniqueCVEs = append(out.UniqueCVEs, CVEIdent{ID: id, Severity: sev})
	}
	return out
}

// SecuritySummaryToProto / SecuritySummaryFromProto bridge the wire form.
func SecuritySummaryToProto(s SecuritySummary) *pb.GetSecuritySummaryResponse {
	resp := &pb.GetSecuritySummaryResponse{
		CertsExpiringSoon:       int32(s.CertsExpiringSoon),
		CertsExpired:            int32(s.CertsExpired),
		CertsExpiringIn7:        int32(s.CertsExpiringIn7),
		CertsExpiringIn30:       int32(s.CertsExpiringIn30),
		CertHostsAffected:       int32(s.CertHostsAffected),
		RootkitWarnings:         int32(s.RootkitWarnings),
		RunningCvesTotal:        int32(s.RunningCVEsTotal),
		RunningCvesCritical:     int32(s.RunningCVEsCritical),
		RunningCvesHigh:         int32(s.RunningCVEsHigh),
		RunningCvesMedium:       int32(s.RunningCVEsMedium),
		RunningCvesLow:          int32(s.RunningCVEsLow),
		RunningCveHostsAffected: int32(s.RunningCVEHostsAffected),
		LynisScoreSum:           s.LynisScoreSum,
		LynisScoredCount:        int32(s.LynisScoredCount),
		LynisWarnings:           int32(s.LynisWarnings),
		RootkitClean:            int32(s.RootkitClean),
		ClamavClean:             int32(s.ClamavClean),
		ClamavInfected:          int32(s.ClamavInfected),
		AideClean:               int32(s.AideClean),
		AideChanged:             int32(s.AideChanged),
		ScapScanned:             int32(s.ScapScanned),
		ScapClean:               int32(s.ScapClean),
		ScapHighFail:            int32(s.ScapHighFail),
		ScannedHosts:            int32(s.ScannedHosts),
		TotalHosts:              int32(s.TotalHosts),
	}
	resp.UniqueCves = make([]*pb.CveSeverity, 0, len(s.UniqueCVEs))
	for _, c := range s.UniqueCVEs {
		resp.UniqueCves = append(resp.UniqueCves, &pb.CveSeverity{Id: c.ID, Severity: c.Severity})
	}
	return resp
}

func SecuritySummaryFromProto(resp *pb.GetSecuritySummaryResponse) SecuritySummary {
	s := SecuritySummary{
		CertsExpiringSoon:       int(resp.CertsExpiringSoon),
		CertsExpired:            int(resp.CertsExpired),
		CertsExpiringIn7:        int(resp.CertsExpiringIn7),
		CertsExpiringIn30:       int(resp.CertsExpiringIn30),
		CertHostsAffected:       int(resp.CertHostsAffected),
		RootkitWarnings:         int(resp.RootkitWarnings),
		RunningCVEsTotal:        int(resp.RunningCvesTotal),
		RunningCVEsCritical:     int(resp.RunningCvesCritical),
		RunningCVEsHigh:         int(resp.RunningCvesHigh),
		RunningCVEsMedium:       int(resp.RunningCvesMedium),
		RunningCVEsLow:          int(resp.RunningCvesLow),
		RunningCVEHostsAffected: int(resp.RunningCveHostsAffected),
		LynisScoreSum:           resp.LynisScoreSum,
		LynisScoredCount:        int(resp.LynisScoredCount),
		LynisWarnings:           int(resp.LynisWarnings),
		RootkitClean:            int(resp.RootkitClean),
		ClamavClean:             int(resp.ClamavClean),
		ClamavInfected:          int(resp.ClamavInfected),
		AideClean:               int(resp.AideClean),
		AideChanged:             int(resp.AideChanged),
		ScapScanned:             int(resp.ScapScanned),
		ScapClean:               int(resp.ScapClean),
		ScapHighFail:            int(resp.ScapHighFail),
		ScannedHosts:            int(resp.ScannedHosts),
		TotalHosts:              int(resp.TotalHosts),
	}
	for _, c := range resp.UniqueCves {
		s.UniqueCVEs = append(s.UniqueCVEs, CVEIdent{ID: c.Id, Severity: c.Severity})
	}
	return s
}

// AggregateSecurityFanout fans out GetSecuritySummary to every roster bolt
// and merges the results (union deduped CVEs, sum additive counts). It
// returns the merged PEER summaries; the caller folds in the local bolt's
// own summary (the local bolt is not a peer of itself). Fan-out-only:
// security posture is never gossiped, so unlike convergence there is no
// cache-aggregate path.
func (a *SpannerAdmin) AggregateSecurityFanout(ctx context.Context) (SecuritySummary, error) {
	results, err := a.fanOut(ctx, func(ctx context.Context, pc *PeerConn) (any, error) {
		return pc.Admin.GetSecuritySummary(ctx, &pb.GetSecuritySummaryRequest{})
	})
	if err != nil {
		return SecuritySummary{}, err
	}
	parts := make([]SecuritySummary, 0, len(results))
	for _, r := range results {
		if r.err != nil {
			slog.Warn("spanner fanout: peer GetSecuritySummary failed", "peer", r.pubkey, "error", r.err)
			continue
		}
		parts = append(parts, SecuritySummaryFromProto(r.value.(*pb.GetSecuritySummaryResponse)))
	}
	return MergeSecuritySummaries(parts), nil
}

// --- risk summary (ADR-037 slice 3, fan-out-only — never gossiped) ---

// RiskSummary is a bolt's risk-posture rollup for the federated dashboard
// Risk column. ScoreSum + ScoredCount carry the un-divided average so the
// viewer re-means across bolts (averaging per-bolt averages mis-weights a
// small bolt equal to a large one); the severity buckets are additive.
// RiskTopEnvoy is one candidate in a bolt's top-N riskiest envoys, shipped
// for re-ranking into a global top-N at the federated viewer (ADR-037
// slice 5). RiskFactorRollup is a per-factor points/count rollup, summed by
// name across bolts.
type RiskTopEnvoy struct {
	ID        string
	Hostname  string
	Score     int
	Level     string
	TopFactor string
}

type RiskFactorRollup struct {
	Name        string
	TotalPoints int
	EnvoyCount  int
}

// riskTopN bounds the per-bolt candidate list and the merged Fleet list.
// Shipping N per bolt guarantees the global top-N is recoverable (worst
// case: all N live on one bolt, whose own top-N already holds them).
const riskTopN = 20

type RiskSummary struct {
	ScoreSum    int64
	ScoredCount int
	Critical    int
	High        int
	Medium      int
	Low         int
	// /risk PAGE Fleet view (ADR-037 slice 5): max-score, the bolt's top-N
	// riskiest envoys (re-ranked at the viewer), and its factor rollup
	// (summed by name).
	MaxScore   int
	TopEnvoys  []RiskTopEnvoy
	TopFactors []RiskFactorRollup
}

// AvgScore is the fleet (or per-bolt) mean risk score, truncated to int to
// match the local dashboard's int(float64(sum)/count) rendering.
func (s RiskSummary) AvgScore() int {
	if s.ScoredCount == 0 {
		return 0
	}
	return int(s.ScoreSum / int64(s.ScoredCount))
}

// MergeRiskSummaries sums score totals, scored counts, and severity buckets
// across bolts. The merged AvgScore is then ScoreSum/ScoredCount over the
// union — a correctly fleet-weighted mean.
func MergeRiskSummaries(parts []RiskSummary) RiskSummary {
	var out RiskSummary
	factorIdx := make(map[string]int) // factor name → index in out.TopFactors
	for _, p := range parts {
		out.ScoreSum += p.ScoreSum
		out.ScoredCount += p.ScoredCount
		out.Critical += p.Critical
		out.High += p.High
		out.Medium += p.Medium
		out.Low += p.Low
		if p.MaxScore > out.MaxScore {
			out.MaxScore = p.MaxScore
		}
		out.TopEnvoys = append(out.TopEnvoys, p.TopEnvoys...)
		for _, f := range p.TopFactors {
			if i, ok := factorIdx[f.Name]; ok {
				out.TopFactors[i].TotalPoints += f.TotalPoints
				out.TopFactors[i].EnvoyCount += f.EnvoyCount
			} else {
				factorIdx[f.Name] = len(out.TopFactors)
				out.TopFactors = append(out.TopFactors, f)
			}
		}
	}
	// Re-rank the unioned candidates to a global top-N (by score desc).
	sort.Slice(out.TopEnvoys, func(i, j int) bool { return out.TopEnvoys[i].Score > out.TopEnvoys[j].Score })
	if len(out.TopEnvoys) > riskTopN {
		out.TopEnvoys = out.TopEnvoys[:riskTopN]
	}
	sort.Slice(out.TopFactors, func(i, j int) bool { return out.TopFactors[i].TotalPoints > out.TopFactors[j].TotalPoints })
	return out
}

func RiskSummaryToProto(s RiskSummary) *pb.GetRiskSummaryResponse {
	resp := &pb.GetRiskSummaryResponse{
		ScoreSum:    s.ScoreSum,
		ScoredCount: int32(s.ScoredCount),
		Critical:    int32(s.Critical),
		High:        int32(s.High),
		Medium:      int32(s.Medium),
		Low:         int32(s.Low),
		MaxScore:    int32(s.MaxScore),
	}
	for _, e := range s.TopEnvoys {
		resp.TopEnvoys = append(resp.TopEnvoys, &pb.RiskTopEnvoy{
			Id: e.ID, Hostname: e.Hostname, Score: int32(e.Score), Level: e.Level, TopFactor: e.TopFactor,
		})
	}
	for _, f := range s.TopFactors {
		resp.TopFactors = append(resp.TopFactors, &pb.RiskFactorRollup{
			Name: f.Name, TotalPoints: int32(f.TotalPoints), EnvoyCount: int32(f.EnvoyCount),
		})
	}
	return resp
}

func RiskSummaryFromProto(resp *pb.GetRiskSummaryResponse) RiskSummary {
	s := RiskSummary{
		ScoreSum:    resp.ScoreSum,
		ScoredCount: int(resp.ScoredCount),
		Critical:    int(resp.Critical),
		High:        int(resp.High),
		Medium:      int(resp.Medium),
		Low:         int(resp.Low),
		MaxScore:    int(resp.MaxScore),
	}
	for _, e := range resp.TopEnvoys {
		s.TopEnvoys = append(s.TopEnvoys, RiskTopEnvoy{
			ID: e.Id, Hostname: e.Hostname, Score: int(e.Score), Level: e.Level, TopFactor: e.TopFactor,
		})
	}
	for _, f := range resp.TopFactors {
		s.TopFactors = append(s.TopFactors, RiskFactorRollup{
			Name: f.Name, TotalPoints: int(f.TotalPoints), EnvoyCount: int(f.EnvoyCount),
		})
	}
	return s
}

// AggregateRiskFanout fans out GetRiskSummary to every roster bolt and
// merges the results. Returns the merged PEER summaries; the caller folds
// in the local bolt's own summary. Fan-out-only (risk is never gossiped).
func (a *SpannerAdmin) AggregateRiskFanout(ctx context.Context) (RiskSummary, error) {
	results, err := a.fanOut(ctx, func(ctx context.Context, pc *PeerConn) (any, error) {
		return pc.Admin.GetRiskSummary(ctx, &pb.GetRiskSummaryRequest{})
	})
	if err != nil {
		return RiskSummary{}, err
	}
	parts := make([]RiskSummary, 0, len(results))
	for _, r := range results {
		if r.err != nil {
			slog.Warn("spanner fanout: peer GetRiskSummary failed", "peer", r.pubkey, "error", r.err)
			continue
		}
		parts = append(parts, RiskSummaryFromProto(r.value.(*pb.GetRiskSummaryResponse)))
	}
	return MergeRiskSummaries(parts), nil
}

// --- compliance summary (ADR-037 slice 4, fan-out-only — never gossiped) ---

// ComplianceSummary is a bolt's standards-compliance rollup for the
// federated dashboard Standards Compliance column. The control buckets are
// additive across bolts (per-framework buckets are disjoint, so on each
// bolt they sum to that framework's catalog; across bolts they accumulate
// as partition-scoped instances). PctSum + PctCount carry the un-divided
// unweighted-mean numerator/denominator so the viewer re-means over the
// bolt union rather than averaging per-bolt averages.
type ComplianceSummary struct {
	Satisfied    int
	Waived       int
	Partial      int
	NotSatisfied int
	Total        int
	PctSum       float64
	PctCount     int
}

// Pct is the unweighted mean of per-framework percents over the union,
// rounded to one decimal to match the local dashboard's rendering.
func (s ComplianceSummary) Pct() float64 {
	if s.PctCount == 0 {
		return 0
	}
	return math.Round(s.PctSum/float64(s.PctCount)*10) / 10
}

// MergeComplianceSummaries sums every field across bolts; the merged Pct is
// then ΣPctSum/ΣPctCount over the union — a correctly re-meaned percent.
func MergeComplianceSummaries(parts []ComplianceSummary) ComplianceSummary {
	var out ComplianceSummary
	for _, p := range parts {
		out.Satisfied += p.Satisfied
		out.Waived += p.Waived
		out.Partial += p.Partial
		out.NotSatisfied += p.NotSatisfied
		out.Total += p.Total
		out.PctSum += p.PctSum
		out.PctCount += p.PctCount
	}
	return out
}

func ComplianceSummaryToProto(s ComplianceSummary) *pb.GetComplianceSummaryResponse {
	return &pb.GetComplianceSummaryResponse{
		Satisfied:    int32(s.Satisfied),
		Waived:       int32(s.Waived),
		Partial:      int32(s.Partial),
		NotSatisfied: int32(s.NotSatisfied),
		Total:        int32(s.Total),
		PctSum:       s.PctSum,
		PctCount:     int32(s.PctCount),
	}
}

func ComplianceSummaryFromProto(resp *pb.GetComplianceSummaryResponse) ComplianceSummary {
	return ComplianceSummary{
		Satisfied:    int(resp.Satisfied),
		Waived:       int(resp.Waived),
		Partial:      int(resp.Partial),
		NotSatisfied: int(resp.NotSatisfied),
		Total:        int(resp.Total),
		PctSum:       resp.PctSum,
		PctCount:     int(resp.PctCount),
	}
}

// AggregateComplianceFanout fans out GetComplianceSummary to every roster
// bolt and merges the results. Returns the merged PEER summaries; the
// caller folds in the local bolt's own summary. Fan-out-only (compliance
// is never gossiped).
func (a *SpannerAdmin) AggregateComplianceFanout(ctx context.Context) (ComplianceSummary, error) {
	results, err := a.fanOut(ctx, func(ctx context.Context, pc *PeerConn) (any, error) {
		return pc.Admin.GetComplianceSummary(ctx, &pb.GetComplianceSummaryRequest{})
	})
	if err != nil {
		return ComplianceSummary{}, err
	}
	parts := make([]ComplianceSummary, 0, len(results))
	for _, r := range results {
		if r.err != nil {
			slog.Warn("spanner fanout: peer GetComplianceSummary failed", "peer", r.pubkey, "error", r.err)
			continue
		}
		parts = append(parts, ComplianceSummaryFromProto(r.value.(*pb.GetComplianceSummaryResponse)))
	}
	return MergeComplianceSummaries(parts), nil
}

// ScopedPageDataFromBolt fetches one peer bolt's JSON-encoded page data via
// a single admin gRPC call — the route-to-owner path for a per-bolt page
// tab (ADR-037 slice 5). boltPubkey comes straight from the active scope
// tab's Key; the hostname is resolved from the authenticated roster. Leaf
// call — the peer's GetScopedPageData never fans out, so there's no
// recursion. Pull-on-view only.
func (a *SpannerAdmin) ScopedPageDataFromBolt(ctx context.Context, boltPubkey, pageID string) ([]byte, error) {
	host := hostnameOfPeer(ctx, a, boltPubkey)
	if host == "" {
		return nil, fmt.Errorf("no roster hostname for bolt %s", boltPubkey)
	}
	pc, err := a.dialBolt(host)
	if err != nil {
		return nil, fmt.Errorf("dialing bolt %s: %w", host, err)
	}
	defer pc.Close()

	callCtx, cancel := context.WithTimeout(ctx, fanOutTimeout)
	defer cancel()
	resp, err := pc.Admin.GetScopedPageData(callCtx, &pb.GetScopedPageDataRequest{PageId: pageID})
	if err != nil {
		return nil, fmt.Errorf("GetScopedPageData(%s) on bolt %s: %w", pageID, host, err)
	}
	return resp.Json, nil
}

// --- agent-health summary (ADR-037 slice 5, fan-out-only) ---

// MergeHealthSummaries folds per-bolt fleetpressure rollups into one fleet
// rollup: counts sum, the worst severity and the highest open-FD host win
// (reductions), and example hostnames take the first non-empty.
func MergeHealthSummaries(parts []fleetpressure.FleetSummary) fleetpressure.FleetSummary {
	var out fleetpressure.FleetSummary
	for _, p := range parts {
		if p.WorstSeverity > out.WorstSeverity {
			out.WorstSeverity = p.WorstSeverity
		}
		out.DegradedCount += p.DegradedCount
		if p.MaxOpenFDs > out.MaxOpenFDs {
			out.MaxOpenFDs = p.MaxOpenFDs
			out.MaxOpenFDsHost = p.MaxOpenFDsHost
			out.MaxOpenFDsID = p.MaxOpenFDsID
			out.MaxOpenFDsSeverity = p.MaxOpenFDsSeverity
		}
		out.HighFDPctCount += p.HighFDPctCount
		if out.HighFDPctExample == "" {
			out.HighFDPctExample = p.HighFDPctExample
		}
		out.RecentOomCount += p.RecentOomCount
		if out.RecentOomExample == "" {
			out.RecentOomExample = p.RecentOomExample
		}
		out.TotalReporting += p.TotalReporting
	}
	return out
}

func HealthSummaryToProto(s fleetpressure.FleetSummary) *pb.GetHealthSummaryResponse {
	return &pb.GetHealthSummaryResponse{
		WorstSeverity:      int32(s.WorstSeverity),
		DegradedCount:      int32(s.DegradedCount),
		MaxOpenFds:         s.MaxOpenFDs,
		MaxOpenFdsHost:     s.MaxOpenFDsHost,
		MaxOpenFdsId:       s.MaxOpenFDsID,
		MaxOpenFdsSeverity: int32(s.MaxOpenFDsSeverity),
		HighFdPctCount:     int32(s.HighFDPctCount),
		HighFdPctExample:   s.HighFDPctExample,
		RecentOomCount:     int32(s.RecentOomCount),
		RecentOomExample:   s.RecentOomExample,
		TotalReporting:     int32(s.TotalReporting),
	}
}

func HealthSummaryFromProto(resp *pb.GetHealthSummaryResponse) fleetpressure.FleetSummary {
	return fleetpressure.FleetSummary{
		WorstSeverity:      fleetpressure.Severity(resp.WorstSeverity),
		DegradedCount:      int(resp.DegradedCount),
		MaxOpenFDs:         resp.MaxOpenFds,
		MaxOpenFDsHost:     resp.MaxOpenFdsHost,
		MaxOpenFDsID:       resp.MaxOpenFdsId,
		MaxOpenFDsSeverity: fleetpressure.Severity(resp.MaxOpenFdsSeverity),
		HighFDPctCount:     int(resp.HighFdPctCount),
		HighFDPctExample:   resp.HighFdPctExample,
		RecentOomCount:     int(resp.RecentOomCount),
		RecentOomExample:   resp.RecentOomExample,
		TotalReporting:     int(resp.TotalReporting),
	}
}

// AggregateHealthFanout fans out GetHealthSummary to every roster bolt and
// merges. Returns the merged PEER rollup; the caller folds in local.
func (a *SpannerAdmin) AggregateHealthFanout(ctx context.Context) (fleetpressure.FleetSummary, error) {
	results, err := a.fanOut(ctx, func(ctx context.Context, pc *PeerConn) (any, error) {
		return pc.Admin.GetHealthSummary(ctx, &pb.GetHealthSummaryRequest{})
	})
	if err != nil {
		return fleetpressure.FleetSummary{}, err
	}
	parts := make([]fleetpressure.FleetSummary, 0, len(results))
	for _, r := range results {
		if r.err != nil {
			slog.Warn("spanner fanout: peer GetHealthSummary failed", "peer", r.pubkey, "error", r.err)
			continue
		}
		parts = append(parts, HealthSummaryFromProto(r.value.(*pb.GetHealthSummaryResponse)))
	}
	return MergeHealthSummaries(parts), nil
}

// RunsFromBolt pulls one bolt's runs for a specific envoy via a single
// admin gRPC call — the owning-bolt routing path for ListRuns(envoy_id)
// when the cached roster shows the envoy lives on a remote peer.
func (a *SpannerAdmin) RunsFromBolt(ctx context.Context, boltHostname, envoyID string, limit int32) ([]*pb.Run, error) {
	pc, err := a.dialer.Dial(boltHostname, boltHostname+":"+a.grpcPort, nil)
	if err != nil {
		return nil, fmt.Errorf("dialing owning bolt %s: %w", boltHostname, err)
	}
	defer pc.Close()

	callCtx, cancel := context.WithTimeout(ctx, fanOutTimeout)
	defer cancel()
	resp, err := pc.Admin.ListRuns(callCtx, &pb.ListRunsRequest{EnvoyId: envoyID, Limit: limit})
	if err != nil {
		return nil, fmt.Errorf("ListRuns on owning bolt %s: %w", boltHostname, err)
	}
	return resp.Runs, nil
}
