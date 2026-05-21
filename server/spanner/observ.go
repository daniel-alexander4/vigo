// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

package spanner

import (
	"sync"
	"time"
)

// Spanner Slice 2-ii-a — the observability cache (ADR-026 + the
// spanner-redesign plan). Slices 2-i-a/2-i-b gossip the *admissions
// roster* peer-to-peer; 2-ii-a adds a parallel, lighter gossip channel
// for *fleet observability* — each bolt's envoy list + convergence
// counters — so fleet-wide admin reads (ListEnvoys,
// GetConvergenceSummary) can aggregate from gossip instead of fanning
// out gRPC to every bolt.
//
// This is deliberately NOT a CRDT. The admissions roster is a CRDT
// because admission/revocation entries from many bolts must converge.
// Observability is the opposite shape: each bolt's entry is a wholesale
// snapshot of per-bolt *facts* (its own envoys, its own counters) that
// only that bolt can author. There is nothing to merge — a peer's new
// snapshot REPLACES its old one entirely. The only ordering rule is
// last-writer-wins by the snapshot's own ServedAt timestamp: a stale
// out-of-order delivery (UDP reorder, a slow Tier-2 pull racing a fast
// Tier-1 counter update) is dropped rather than allowed to overwrite a
// newer entry.
//
// The LOCAL bolt is never in this cache. Admin reads aggregate
// (local fleetIndex) + (every cached peer) — the local bolt's own
// fleet state is always read live from its in-memory index, never
// round-tripped through gossip.

// EnvoyLite is the per-envoy wire shape carried in a fleet snapshot —
// the minimal projection ListEnvoys and the REST bolt-drilldown
// surfaces need. Fields mirror pb.Envoy so the gRPC handler can
// project an EnvoyLite straight into a *pb.Envoy without a lookup.
// Timestamps are ms-since-epoch (JSON-friendly, unambiguous on the
// wire); the gRPC handler converts to timestamppb.
type EnvoyLite struct {
	ID                string `json:"id"`
	Hostname          string `json:"hostname"`
	IP                string `json:"ip"`
	AgentVersion      string `json:"agent_version,omitempty"`
	EnrolledAtMs      int64  `json:"enrolled_at_ms,omitempty"`
	LastSeenMs        int64  `json:"last_seen_ms,omitempty"`
	Revoked           bool   `json:"revoked,omitempty"`
	ConvergenceStatus string `json:"convergence_status,omitempty"`
}

// ConvergenceCounters is the convergence breakdown a bolt publishes —
// the same axes server/fleet.ConvergenceCounters exposes. Defined here
// (rather than imported from server/fleet) so server/spanner does not
// take a dependency on server/fleet and the wire shape is owned by the
// gossip layer. The gRPC + REST aggregators convert in both directions.
type ConvergenceCounters struct {
	Total int `json:"total"`

	// Failure axis (mutually exclusive; sums to Total).
	Converged int `json:"converged"`
	Degraded  int `json:"degraded"`
	Failed    int `json:"failed"`
	NoData    int `json:"no_data"`

	// Drift axis (orthogonal; Diverged ⊆ Changed).
	Changed  int `json:"changed"`
	Diverged int `json:"diverged"`

	// Reachability axis (orthogonal).
	Offline int `json:"offline"`
}

// Add returns the element-wise sum of two ConvergenceCounters — used
// to fold the local bolt's counters together with every cached peer's.
func (c ConvergenceCounters) Add(o ConvergenceCounters) ConvergenceCounters {
	return ConvergenceCounters{
		Total:     c.Total + o.Total,
		Converged: c.Converged + o.Converged,
		Degraded:  c.Degraded + o.Degraded,
		Failed:    c.Failed + o.Failed,
		NoData:    c.NoData + o.NoData,
		Changed:   c.Changed + o.Changed,
		Diverged:  c.Diverged + o.Diverged,
		Offline:   c.Offline + o.Offline,
	}
}

// PeerObservability is one peer bolt's last-known fleet state. It is
// replaced wholesale on each gossip update — never merged. UpdatedAt
// is the snapshot's ServedAt (the publishing bolt's clock), the
// last-writer-wins ordering key.
type PeerObservability struct {
	Envoys      []EnvoyLite
	Convergence ConvergenceCounters
	UpdatedAt   time.Time
}

// ObservabilityCache is the in-memory, mutex-guarded store of every
// peer bolt's last fleet snapshot, keyed by bolt pubkey-hex. The local
// bolt is never stored here. Construct with NewObservabilityCache.
type ObservabilityCache struct {
	mu    sync.RWMutex
	peers map[string]*PeerObservability // boltPubkeyHex → facts
}

// NewObservabilityCache returns an empty cache.
func NewObservabilityCache() *ObservabilityCache {
	return &ObservabilityCache{peers: make(map[string]*PeerObservability)}
}

// UpdateFull replaces a peer's entire observability entry — both the
// envoy roster and the convergence counters — with a freshly received
// Tier-2 fleet snapshot. The update is applied only when its servedAt
// is at least as recent as the stored entry's UpdatedAt: a stale,
// out-of-order delivery is dropped (returns false) so it cannot
// overwrite newer facts. boltPubkeyHex is the publishing peer's
// identity; it is never the local bolt.
func (c *ObservabilityCache) UpdateFull(boltPubkeyHex string, envoys []EnvoyLite, conv ConvergenceCounters, servedAt time.Time) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	if existing, ok := c.peers[boltPubkeyHex]; ok && servedAt.Before(existing.UpdatedAt) {
		return false
	}
	c.peers[boltPubkeyHex] = &PeerObservability{
		Envoys:      envoys,
		Convergence: conv,
		UpdatedAt:   servedAt,
	}
	return true
}

// UpdateCounters replaces ONLY a peer's convergence counters, from a
// fast-cadence Tier-1 multicast digest (the 30s counters gossip). The
// envoy roster — large, slow Tier-2 data — is left untouched. Like
// UpdateFull this is last-writer-wins by servedAt and the local bolt
// is never the subject. If the peer has no entry yet, one is created
// with an empty envoy roster (the next Tier-2 pull fills it in).
func (c *ObservabilityCache) UpdateCounters(boltPubkeyHex string, conv ConvergenceCounters, servedAt time.Time) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	existing, ok := c.peers[boltPubkeyHex]
	if ok && servedAt.Before(existing.UpdatedAt) {
		return false
	}
	if !ok {
		c.peers[boltPubkeyHex] = &PeerObservability{
			Convergence: conv,
			UpdatedAt:   servedAt,
		}
		return true
	}
	existing.Convergence = conv
	existing.UpdatedAt = servedAt
	return true
}

// Get returns a copy of one peer's observability entry, or nil if the
// peer has never reported. The returned envoy slice is a fresh copy so
// callers cannot mutate cache state.
func (c *ObservabilityCache) Get(boltPubkeyHex string) *PeerObservability {
	c.mu.RLock()
	defer c.mu.RUnlock()
	p, ok := c.peers[boltPubkeyHex]
	if !ok {
		return nil
	}
	return copyPeer(p)
}

// All returns a copy of every cached peer entry, keyed by bolt
// pubkey-hex. Used by the admin aggregators to fold all peers into a
// fleet-wide answer.
func (c *ObservabilityCache) All() map[string]*PeerObservability {
	c.mu.RLock()
	defer c.mu.RUnlock()
	out := make(map[string]*PeerObservability, len(c.peers))
	for k, p := range c.peers {
		out[k] = copyPeer(p)
	}
	return out
}

// OwnerOf reports which cached peer bolt serves the given envoy, by
// bolt pubkey-hex, scanning under the read lock without copying — the
// lookup ListRuns(envoy_id) owning-bolt routing needs. Returns
// ("", false) when no cached peer claims the envoy (it is local, or
// unknown).
func (c *ObservabilityCache) OwnerOf(envoyID string) (string, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	for key, p := range c.peers {
		for _, e := range p.Envoys {
			if e.ID == envoyID {
				return key, true
			}
		}
	}
	return "", false
}

// Forget drops a peer's entry — used when a bolt is revoked from the
// roster so its stale facts stop contributing to fleet aggregates.
func (c *ObservabilityCache) Forget(boltPubkeyHex string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.peers, boltPubkeyHex)
}

// OldestUpdate returns the UpdatedAt of the least-recently-refreshed
// cached peer — the staleness signal surfaced in admin responses so
// operators see how fresh a gossip-aggregated answer is. Returns the
// zero time when the cache is empty (no peers contributing).
func (c *ObservabilityCache) OldestUpdate() time.Time {
	c.mu.RLock()
	defer c.mu.RUnlock()
	var oldest time.Time
	for _, p := range c.peers {
		if oldest.IsZero() || p.UpdatedAt.Before(oldest) {
			oldest = p.UpdatedAt
		}
	}
	return oldest
}

// copyPeer deep-copies a PeerObservability so cache state never leaks
// a mutable reference to a caller.
func copyPeer(p *PeerObservability) *PeerObservability {
	envoys := make([]EnvoyLite, len(p.Envoys))
	copy(envoys, p.Envoys)
	return &PeerObservability{
		Envoys:      envoys,
		Convergence: p.Convergence,
		UpdatedAt:   p.UpdatedAt,
	}
}
