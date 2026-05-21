// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

package spanner

import (
	"context"
	"crypto/ed25519"
	"crypto/tls"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"time"

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

// Gossiper pulls peer roster snapshots on a timer and CRDT-merges
// them into the local DB. Construct with NewGossiper and run as a
// goroutine: go g.Run(ctx).
type Gossiper struct {
	sqlDB      *sql.DB
	queries    *db.Queries
	ownPubkey  string // local bolt pubkey-hex; the gossiper skips its own roster row
	httpsPort  string // this server's configured HTTPS port; peers are assumed to run the same port
	interval   time.Duration
	httpClient *http.Client
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
//   - httpClient: nil installs an InsecureSkipVerify transport,
//     exactly like NewSpannerAdmissionHandler — separate vigosrv
//     deployments share no CA, so TLS chain verification cannot
//     succeed; the trust anchor is the per-row Ed25519 admission
//     signature verified inside MergeSnapshot, not the TLS cert.
func NewGossiper(sqlDB *sql.DB, queries *db.Queries, ownPubkeyHex, httpsPort string, interval time.Duration, httpClient *http.Client) *Gossiper {
	if interval <= 0 {
		interval = 5 * time.Minute
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
		sqlDB:      sqlDB,
		queries:    queries,
		ownPubkey:  ownPubkeyHex,
		httpsPort:  httpsPort,
		interval:   interval,
		httpClient: httpClient,
	}
}

// Run blocks until ctx is cancelled, pulling + merging peer roster
// snapshots on the Tier-2 ticker. Call as a goroutine.
func (g *Gossiper) Run(ctx context.Context) {
	slog.Info("spanner roster gossiper started", "interval", g.interval, "https_port", g.httpsPort)
	ticker := time.NewTicker(g.interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			slog.Info("spanner roster gossiper stopped")
			return
		case <-ticker.C:
			g.cycle(ctx)
		}
	}
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
	for _, b := range roster {
		// Skip self and revoked peers — a revoked bolt is not a roster
		// source.
		if b.Pubkey == g.ownPubkey || b.RevokedAt.Valid {
			continue
		}
		peers++
		n, err := g.pullAndMerge(ctx, b.Hostname, founderPubkey)
		if err != nil {
			// One bad peer never aborts the cycle.
			slog.Warn("spanner gossip: peer pull failed; continuing to next peer",
				"peer", b.Hostname, "error", err)
			continue
		}
		merged += n
	}
	slog.Debug("spanner gossip: cycle complete", "peers", peers, "rows_merged", merged)
}

// pullAndMerge GETs one peer's /snapshot, decodes it, and CRDT-merges
// it. Returns the number of bolt rows the LWW merge mutated.
func (g *Gossiper) pullAndMerge(ctx context.Context, peerHostname string, founderPubkey ed25519.PublicKey) (int, error) {
	url := fmt.Sprintf("https://%s:%s/api/v1/spanner/snapshot", peerHostname, g.httpsPort)

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return 0, fmt.Errorf("building snapshot request: %w", err)
	}
	resp, err := g.httpClient.Do(req)
	if err != nil {
		return 0, fmt.Errorf("calling peer /snapshot: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return 0, fmt.Errorf("peer /snapshot returned %d: %s", resp.StatusCode, string(body))
	}

	var snap Snapshot
	if err := json.NewDecoder(resp.Body).Decode(&snap); err != nil {
		return 0, fmt.Errorf("decoding peer /snapshot: %w", err)
	}

	merged, err := MergeSnapshot(ctx, g.queries, g.sqlDB, snap, founderPubkey)
	if err != nil {
		return 0, fmt.Errorf("merging peer snapshot: %w", err)
	}
	return merged, nil
}
