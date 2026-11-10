// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: AGPL-3.0-only

package spanner

import (
	"context"
	"crypto/ed25519"
	"crypto/tls"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"golang.org/x/time/rate"

	vigoauth "git.alexander4.io/vigo/server/auth"
	"git.alexander4.io/vigo/server/daemon"
	"git.alexander4.io/vigo/server/db"
)

// Spanner Slice 2-i-a — the roster-replication gossiper (ADR-026 +
// the spanner-redesign plan). The Gossiper runs on every
// non-standalone bolt. On a Tier-2 timer it pulls every other roster
// peer's full Snapshot over REST (GET /api/v1/spanner/snapshot) and
// CRDT-merges it via MergeSnapshot, so all bolts converge on one
// admissions roster.
//
// This is the REST half of Tier-2. The UDP-multicast Tier-1 counter
// gossip is Slice 2-i-b — out of scope here. The Gossiper is purely
// additive: it never mutates anything but spanner_bolts /
// spanner_consumed_tokens, and a CRDT merge of an identical roster is
// a no-op.
//
// Best-effort, like Reporter: a peer that is unreachable, returns a
// non-2xx, or serves undecodable/bad data is logged and skipped — one
// bad peer never aborts the cycle and never crashes the goroutine.

// gossipSnapshotTimeout bounds a single peer's /snapshot GET so one
// slow peer cannot stall the whole cycle.
const gossipSnapshotTimeout = 30 * time.Second

// MaxPeerResponseBytes caps how much of a peer's 2xx response body
// (/snapshot, /fleet-snapshot, /peer-admit, /admit) is JSON-decoded.
// The peer is not yet authenticated at the transport (see NewGossiper),
// so without a cap only the timeout bounded the allocation. Sized for a
// full fleet-snapshot of a large partition (tens of thousands of envoys).
const MaxPeerResponseBytes = 256 << 20

// peerResponseLimit is the cap this package applies; a var only so tests
// can lower it.
var peerResponseLimit int64 = MaxPeerResponseBytes

// pullDebounceWindow is the minimum gap between two Tier-1-triggered
// pulls of the same peer. A convergence-window flurry of Tier-1
// digest mismatches collapses to at most one pull per peer per
// window, so the fast-path cannot cause a pull storm.
const pullDebounceWindow = 5 * time.Second

// Gossiper pulls peer roster snapshots on a timer and CRDT-merges
// them into the local DB. Construct with NewGossiper and run as a
// goroutine: go g.Run(ctx).
//
// Tier-2 (the REST ticker) and Tier-1 (the multicast fast-path, Slice
// 2-i-b) both ride this one Gossiper — Run spawns all three goroutines
// (Tier-2 ticker + Tier-1 sender + Tier-1 listener). Tier-1 is purely
// additive: it only triggers the same pullAndMerge Tier-2 uses.
type Gossiper struct {
	sqlDB      *sql.DB
	queries    *db.Queries
	ownPubkey  string // local bolt pubkey-hex; the gossiper skips its own roster row
	httpsPort  string // this server's configured HTTPS port; peers are assumed to run the same port
	interval   time.Duration
	httpClient *http.Client

	// Tier-1 multicast fast-path (Slice 2-i-b).
	spannerID     string             // federation anchor, stamped into + checked on every Tier-1 digest
	signingKey    ed25519.PrivateKey // bolt identity; signs outbound Tier-1 digests (nil disables the sender)
	mcastGroup    string             // IPv4 multicast group for Tier-1 (spanner.transport.multicast_group)
	mcastPort     int                // UDP port for Tier-1 (spanner.transport.multicast_port)
	tier1Interval time.Duration      // Tier-1 digest cadence (spanner.snapshot.tier1_interval)

	// tier1Limiter caps how many inbound Tier-1 datagrams the listener
	// processes per second before the sender-lookup DB read. spanner_id
	// is not secret, so a LAN attacker could otherwise flood cheap UDP
	// and force one ListBolts query per packet; this reduces a flood to
	// a token-bucket trickle of cheap rejects. Global (not per-source):
	// the source addr is discarded on read, and legitimate Tier-1
	// traffic is inherently low-rate.
	tier1Limiter *rate.Limiter

	// pullDebounce records the last Tier-1-triggered pull time per peer
	// pubkey-hex; allowTier1Pull enforces pullDebounceWindow against it.
	pullDebounceMu sync.Mutex
	pullDebounce   map[string]time.Time
	// tier1LastSentAt is the newest verified Tier-1 SentAt per sender
	// (guarded by pullDebounceMu); an older message is a replay. Keyed by
	// verified roster members only, so it is bounded by the roster.
	tier1LastSentAt map[string]int64

	// observ is the Slice 2-ii-a fleet-observability cache. The Tier-2
	// cycle pulls each peer's signed /fleet-snapshot alongside the
	// admissions /snapshot and stores the verified result here; the
	// admin-read aggregators read it. nil disables fleet-snapshot
	// pulling (standalone — there is no Gossiper there anyway).
	observ *ObservabilityCache

	// localCounters returns this bolt's own current convergence
	// counters — stamped into every outbound Tier-1 digest so peers
	// get 30s-fresh counters without waiting for the 5m Tier-2 pull.
	// nil → Tier-1 digests carry a zero-valued ConvergenceCounters
	// (still valid; the Tier-2 pull supplies the real counters).
	localCounters func() ConvergenceCounters

	// ADR-030 Slice 2-iii-d side-effect sinks for webusers merge.
	// nil → no-op (standalone, or wired only after auth is up).
	sessions *vigoauth.SessionManager
	audit    AuditRecorder
}

// SetLocalCounters injects the source of this bolt's own convergence
// counters for outbound Tier-1 digests. Wired in main.go where the
// fleet index is in scope.
func (g *Gossiper) SetLocalCounters(fn func() ConvergenceCounters) {
	g.localCounters = fn
}

// SetSessions injects the local SCS session manager so post-merge
// role-change and tombstone effects (ADR-030 Slice 2-iii-d) can
// destroy affected sessions. Wired in server.go after the auth
// provider's session manager is in scope. nil leaves session
// destruction as a no-op (standalone; tests).
func (g *Gossiper) SetSessions(sm *vigoauth.SessionManager) {
	g.sessions = sm
}

// SetAuditWriter injects the audit recorder for replication events
// (webusers.role_changed_via_merge, webusers.tombstoned_via_merge).
// nil silences emission.
func (g *Gossiper) SetAuditWriter(w AuditRecorder) {
	g.audit = w
}

// ObservabilityCache returns the Gossiper's fleet-observability cache —
// the source the cache-aggregating admin reads (Slice 2-ii-a) consult.
// Never nil for a constructed Gossiper.
func (g *Gossiper) ObservabilityCache() *ObservabilityCache {
	return g.observ
}

// NewGossiper constructs a roster-replication Gossiper.
//
//   - sqlDB + queries: the local CRDT store.
//   - ownPubkeyHex: this bolt's pubkey-hex, so the gossiper does not
//     pull its own /snapshot.
//   - httpsPort: this server's configured HTTPS port (e.g. "8443").
//     Peer snapshot URLs are built as
//     https://<peer.hostname>:<httpsPort>/api/v1/spanner/snapshot —
//     ASSUMPTION: every bolt in the spanner runs the REST/UI listener
//     on the same port. Slice 2-i-b's discovery gossip will carry an
//     explicit per-peer address; until then the same-port assumption
//     holds for every supported deployment shape.
//   - interval: the Tier-2 pull cadence (snapshot.tier2_interval).
//     Non-positive falls back to 5m.
//   - spannerID: the federation anchor, stamped into every Tier-1
//     digest and checked on every inbound one.
//   - signingKey: this bolt's Ed25519 identity, used to sign outbound
//     Tier-1 digests. nil disables the Tier-1 sender (the listener
//     still runs; Tier-2 is unaffected).
//   - mcastGroup / mcastPort: the Tier-1 multicast group + UDP port
//     (spanner.transport.multicast_group / multicast_port).
//   - tier1Interval: the Tier-1 digest cadence (snapshot.tier1_interval).
//     Non-positive falls back to 30s.
//   - httpClient: nil installs an InsecureSkipVerify transport,
//     exactly like NewSpannerAdmissionHandler — separate vigosrv
//     deployments share no CA, so TLS chain verification cannot
//     succeed; the trust anchor is the per-row Ed25519 admission
//     signature verified inside MergeSnapshot, not the TLS cert.
func NewGossiper(sqlDB *sql.DB, queries *db.Queries, ownPubkeyHex, httpsPort string, interval time.Duration, spannerID string, signingKey ed25519.PrivateKey, mcastGroup string, mcastPort int, tier1Interval time.Duration, httpClient *http.Client) *Gossiper {
	if interval <= 0 {
		interval = 5 * time.Minute
	}
	if tier1Interval <= 0 {
		tier1Interval = 30 * time.Second
	}
	if httpClient == nil {
		httpClient = &http.Client{
			Timeout: gossipSnapshotTimeout,
			Transport: &http.Transport{
				TLSClientConfig: &tls.Config{InsecureSkipVerify: true}, //nolint:gosec // trust is the signed admission rows; see doc comment
			},
		}
	}
	return &Gossiper{
		sqlDB:           sqlDB,
		queries:         queries,
		ownPubkey:       ownPubkeyHex,
		httpsPort:       httpsPort,
		interval:        interval,
		httpClient:      httpClient,
		spannerID:       spannerID,
		signingKey:      signingKey,
		mcastGroup:      mcastGroup,
		mcastPort:       mcastPort,
		tier1Interval:   tier1Interval,
		tier1Limiter:    rate.NewLimiter(tier1RateLimit, tier1RateBurst),
		pullDebounce:    make(map[string]time.Time),
		tier1LastSentAt: make(map[string]int64),
		observ:          NewObservabilityCache(),
	}
}

// Run blocks until ctx is cancelled. It spawns the Tier-1 multicast
// fast-path (sender + listener, Slice 2-i-b) as goroutines, then runs
// the Tier-2 REST ticker inline. Call Run itself as a goroutine.
func (g *Gossiper) Run(ctx context.Context) {
	slog.Info("spanner roster gossiper started", "interval", g.interval, "https_port", g.httpsPort)

	// Tier-1 multicast fast-path — additive accelerators that only
	// trigger the same pullAndMerge the Tier-2 ticker uses.
	go g.runTier1Sender(ctx)
	go g.runTier1Listener(ctx, g.resolveFounderPubkey)

	// Tier-2 REST ticker — the universal fallback.
	ticker := time.NewTicker(g.interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			slog.Info("spanner roster gossiper stopped")
			return
		case <-ticker.C:
			daemon.Guard("spanner-gossip", func() { g.cycle(ctx) })
		}
	}
}

// resolveFounderPubkey loads the local roster and resolves the founder
// pubkey from it — the per-pull verification anchor. Shared by the
// Tier-1 listener; the Tier-2 cycle resolves its own copy per cycle.
func (g *Gossiper) resolveFounderPubkey(ctx context.Context) (ed25519.PublicKey, error) {
	roster, err := g.queries.ListBolts(ctx)
	if err != nil {
		return nil, fmt.Errorf("listing local roster: %w", err)
	}
	return FounderPubkey(roster)
}

// cycle runs one pull-and-merge pass over every roster peer. Errors
// resolving the founder pubkey skip the whole cycle (logged); a
// per-peer failure skips that peer and continues.
func (g *Gossiper) cycle(ctx context.Context) {
	roster, err := g.queries.ListBolts(ctx)
	if err != nil {
		slog.Warn("spanner gossip: listing local roster failed; skipping cycle", "error", err)
		return
	}
	founderPubkey, err := FounderPubkey(roster)
	if err != nil {
		// No founder row locally yet (e.g. this bolt has not finished
		// the join ceremony). Nothing to verify against — skip.
		slog.Warn("spanner gossip: cannot resolve founder pubkey; skipping cycle", "error", err)
		return
	}

	merged := 0
	peers := 0
	livePeers := make(map[string]bool, len(roster))
	for _, b := range roster {
		// Skip self and revoked peers — a revoked bolt is not a roster
		// source.
		if b.Pubkey == g.ownPubkey || b.RevokedAt.Valid {
			continue
		}
		livePeers[b.Pubkey] = true
		peers++
		n, err := g.pullAndMerge(ctx, b.Hostname, founderPubkey)
		if err != nil {
			// One bad peer never aborts the cycle.
			slog.Warn("spanner gossip: peer pull failed; continuing to next peer",
				"peer", b.Hostname, "error", err)
		} else {
			merged += n
		}

		// Slice 2-ii-a: pull the peer's signed fleet-observability
		// snapshot too — same peer, same cadence as the admissions
		// pull. Independent of the admissions merge: a /snapshot
		// failure must not skip the /fleet-snapshot pull and vice
		// versa, so this is a separate best-effort call.
		if err := g.pullFleetSnapshot(ctx, b.Hostname, b.Pubkey); err != nil {
			slog.Warn("spanner gossip: peer fleet-snapshot pull failed; continuing",
				"peer", b.Hostname, "error", err)
		}
	}

	// Drop observability entries for bolts no longer live in the
	// roster (revoked or removed) so their stale facts stop
	// contributing to fleet-wide admin aggregates.
	for _, peerKey := range g.observPeerKeys() {
		if !livePeers[peerKey] {
			g.observ.Forget(peerKey)
		}
	}
	slog.Debug("spanner gossip: cycle complete", "peers", peers, "rows_merged", merged)
}

// observPeerKeys returns the pubkey-hex of every peer currently in the
// observability cache — used by cycle to evict entries for bolts that
// have left the roster.
func (g *Gossiper) observPeerKeys() []string {
	all := g.observ.All()
	keys := make([]string, 0, len(all))
	for k := range all {
		keys = append(keys, k)
	}
	return keys
}

// pullFleetSnapshot GETs one peer's signed /fleet-snapshot, verifies
// the signature against that peer's roster pubkey (peerPubkeyHex —
// NEVER a pubkey from the wire), and stores the verified result in the
// observability cache. A bad signature is dropped and logged, not
// cached.
func (g *Gossiper) pullFleetSnapshot(ctx context.Context, peerHostname, peerPubkeyHex string) error {
	url := fmt.Sprintf("https://%s:%s/api/v1/spanner/fleet-snapshot", peerHostname, g.httpsPort)

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return fmt.Errorf("building fleet-snapshot request: %w", err)
	}
	SignPullRequest(req, g.signingKey, g.ownPubkey)
	resp, err := g.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("calling peer /fleet-snapshot: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		// best-effort: body only enriches the error we already return.
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return fmt.Errorf("peer /fleet-snapshot returned %d: %s", resp.StatusCode, string(body))
	}

	var snap FleetSnapshot
	if err := json.NewDecoder(io.LimitReader(resp.Body, peerResponseLimit)).Decode(&snap); err != nil {
		return fmt.Errorf("decoding peer /fleet-snapshot: %w", err)
	}

	if err := g.acceptFleetSnapshot(peerPubkeyHex, &snap); err != nil {
		return err
	}
	return nil
}

// acceptFleetSnapshot verifies a received FleetSnapshot against the
// peer's roster pubkey and, on success, stores it in the observability
// cache. Isolated from the HTTP plumbing so unit tests can exercise the
// verify + store decision without a socket. A bad signature returns an
// error and the snapshot is NOT cached.
func (g *Gossiper) acceptFleetSnapshot(peerPubkeyHex string, snap *FleetSnapshot) error {
	peerPub, err := hex.DecodeString(peerPubkeyHex)
	if err != nil || len(peerPub) != ed25519.PublicKeySize {
		return fmt.Errorf("peer roster pubkey %q is not a 32-byte hex key", peerPubkeyHex)
	}
	if err := VerifyFleetSnapshot(ed25519.PublicKey(peerPub), snap); err != nil {
		return fmt.Errorf("fleet-snapshot signature rejected: %w", err)
	}
	g.observ.UpdateFull(
		peerPubkeyHex,
		snap.Envoys,
		snap.Convergence,
		time.UnixMilli(snap.ServedAt),
	)
	return nil
}

// pullAndMerge GETs one peer's /snapshot, decodes it, and CRDT-merges
// it. Returns the number of bolt rows the LWW merge mutated.
func (g *Gossiper) pullAndMerge(ctx context.Context, peerHostname string, founderPubkey ed25519.PublicKey) (int, error) {
	url := fmt.Sprintf("https://%s:%s/api/v1/spanner/snapshot", peerHostname, g.httpsPort)

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return 0, fmt.Errorf("building snapshot request: %w", err)
	}
	SignPullRequest(req, g.signingKey, g.ownPubkey)
	resp, err := g.httpClient.Do(req)
	if err != nil {
		return 0, fmt.Errorf("calling peer /snapshot: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		// best-effort: body only enriches the error we already return.
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return 0, fmt.Errorf("peer /snapshot returned %d: %s", resp.StatusCode, string(body))
	}

	var snap Snapshot
	if err := json.NewDecoder(io.LimitReader(resp.Body, peerResponseLimit)).Decode(&snap); err != nil {
		return 0, fmt.Errorf("decoding peer /snapshot: %w", err)
	}

	merged, err := MergeSnapshot(ctx, g.queries, g.sqlDB, snap, founderPubkey, g.sessions, g.audit)
	if err != nil {
		return 0, fmt.Errorf("merging peer snapshot: %w", err)
	}
	return merged, nil
}
