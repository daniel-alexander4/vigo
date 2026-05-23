// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

package spanner

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"

	pb "git.alexander4.io/vigo/generated.code/proto/vigo"
	"git.alexander4.io/vigo/server/db"
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
