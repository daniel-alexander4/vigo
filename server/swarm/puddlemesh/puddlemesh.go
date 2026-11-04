// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: Apache-2.0

// Package puddlemesh builds the fleet-wide puddle observability view by
// aggregating per-envoy `puddle` trait payloads keyed on the puddle
// Ed25519 pubkey (ADR-014).
//
// The puddle is the foundational per-user swarm identity that gitback,
// curator, poolq, lockbox, and longdrawer all build on. This package is
// the single server-side home for two things the puddle trait reports
// and nothing else does:
//
//   - the **fleet puddle topology + health** — which puddles exist, who
//     owns each (its friendly name), which envoys hold it, and whether
//     it's healthy; and
//   - the **friendly-name map** (ADR-022) served at
//     `GET /api/v1/swarm/puddle/names`. gitbackmesh and the curator S3
//     facade are *consumers* of this map via [`ReverseNameMap`]; they no
//     longer own it.
//
// Like the other swarm mesh aggregators it reconstructs everything from
// reports that already flow up the standard trait pipeline — no new RPC
// — and lives at the bottom of the import graph (minimal `EnvoyInput`,
// no `server/fleet` import) so both `server/api` and `ui` can consume
// it. Per-envoy reachability uses the same trait-report `StaleAfter`
// rule as lockboxmesh/longdrawermesh, so "reporting/stale" means the
// same thing across every swarm view.
package puddlemesh

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"sort"
	"strings"
	"time"

	"git.alexander4.io/vigo/server/db"
	"git.alexander4.io/vigo/server/swarm"
)

// StaleAfter matches lockboxmesh/longdrawermesh — 5 minutes between the
// last reported trait and "now" flips an envoy's puddle entry to stale.
const StaleAfter = 5 * time.Minute

// Health band vocabulary for a puddle. Deliberately distinct from
// `convergence.Status` — this is identity health, not configcrate
// convergence.
const (
	HealthHealthy  = "healthy"
	HealthWarning  = "warning"
	HealthDegraded = "degraded"
)

// EnvoyInput is the minimal envoy descriptor the aggregator needs from
// its caller — (ID, Hostname, Revoked). Mirrors the other mesh
// aggregators so the package stays at the bottom of the import graph.
type EnvoyInput struct {
	ID       string
	Hostname string
	Revoked  bool
}

// Fleet is the top-level aggregated puddle view.
type Fleet struct {
	GeneratedAt time.Time       `json:"generated_at"`
	Puddles     []PuddleSummary `json:"puddles"`
	Totals      Totals          `json:"totals"`
}

// Totals carries pre-computed counts the UI can render without
// re-walking the puddles slice.
type Totals struct {
	Puddles   int `json:"puddles"`
	Envoys    int `json:"envoys"`
	Named     int `json:"named"`
	Unlocked  int `json:"unlocked"`
	Unhealthy int `json:"unhealthy"`
}

// PuddleSummary aggregates one puddle (one Ed25519 pubkey) across the
// fleet.
type PuddleSummary struct {
	PuddlePubkey string `json:"puddle_pubkey"`
	// Owner is the puddle's uncontested friendly name (ADR-022), or ""
	// when it has claimed none or its name is contested.
	Owner string `json:"owner,omitempty"`
	// OwnerContested is true when this puddle claims a name that another
	// puddle also claims — a split-identity signal that degrades health
	// to a warning and leaves Owner empty.
	OwnerContested bool `json:"owner_contested,omitempty"`

	Envoys     []EnvoyPresence `json:"envoys"`
	EnvoyCount int             `json:"envoy_count"`
	// Users is the sorted set of Unix usernames this puddle is held
	// under across the fleet (usually one).
	Users []string `json:"users"`

	SessionUnlocked bool   `json:"session_unlocked"`
	RetiredCount    int    `json:"retired_count"`
	RekeyPending    bool   `json:"rekey_pending"`
	LastUpdated     int64  `json:"last_updated"`
	Health          Health `json:"health"`
}

// Health is a puddle's computed health band plus the human-readable
// reasons behind a non-healthy verdict.
type Health struct {
	Band    string   `json:"band"`
	Reasons []string `json:"reasons,omitempty"`
}

// EnvoyPresence describes one envoy's view of a puddle (one user on one
// host).
type EnvoyPresence struct {
	Hostname        string `json:"hostname"`
	User            string `json:"user"`
	Status          string `json:"status"` // reporting | stale
	UpdatedAt       int64  `json:"updated_at,omitempty"`
	Initialized     bool   `json:"initialized"`
	SessionUnlocked bool   `json:"session_unlocked"`
	PairPending     bool   `json:"pair_pending,omitempty"`
	RekeyPending    bool   `json:"rekey_pending,omitempty"`
}

// LookupPuddle returns the summary for a puddle pubkey (hex), or nil if
// the snapshot doesn't carry it. Accepts either case; compares
// lowercase.
func (f *Fleet) LookupPuddle(pubkey string) *PuddleSummary {
	if f == nil {
		return nil
	}
	pk := strings.ToLower(pubkey)
	for i := range f.Puddles {
		if strings.ToLower(f.Puddles[i].PuddlePubkey) == pk {
			return &f.Puddles[i]
		}
	}
	return nil
}

// LookupByOwner returns the puddle whose uncontested owner name matches.
// Names are not cryptographically authoritative, so this is a UX
// convenience; use LookupPuddle when identity matters.
func (f *Fleet) LookupByOwner(name string) *PuddleSummary {
	if f == nil || name == "" {
		return nil
	}
	for i := range f.Puddles {
		if f.Puddles[i].Owner == name {
			return &f.Puddles[i]
		}
	}
	return nil
}

// Aggregator holds the 30-second in-memory TTL caches (independent for
// the topology view and the name map, same window) so repeated calls
// within a burst don't re-parse every envoy's trait blob.
type Aggregator struct {
	queries *db.Queries

	// Both caches rebuild outside their locks (swarm.MeshCache): a cache hit
	// never waits behind a fleet-wide trait scan.
	cache *swarm.MeshCache[*Fleet]
	names *swarm.MeshCache[*FleetNames]
}

// NewAggregator builds an aggregator bound to the given query handle.
func NewAggregator(queries *db.Queries) *Aggregator {
	return &Aggregator{
		queries: queries,
		cache:   swarm.NewMeshCache[*Fleet](swarm.MeshCacheTTL),
		names:   swarm.NewMeshCache[*FleetNames](swarm.MeshCacheTTL),
	}
}

// Snapshot returns the cached puddle view or rebuilds it from the passed
// envoy list.
func (a *Aggregator) Snapshot(ctx context.Context, envoys []EnvoyInput) (*Fleet, error) {
	return a.cache.Get(func() (*Fleet, error) { return a.build(ctx, envoys), nil })
}

// NamesSnapshot returns the cached friendly-name map or rebuilds it.
// Independent TTL from the topology view, same window. This is the
// single producer of the fleet name map (ADR-022); gitbackmesh and the
// curator S3 facade consume it via [`ReverseNameMap`].
func (a *Aggregator) NamesSnapshot(ctx context.Context, envoys []EnvoyInput) (*FleetNames, error) {
	return a.names.Get(func() (*FleetNames, error) {
		return &FleetNames{
			UpdatedAt: time.Now().UTC().Unix(),
			Names:     groupNameClaims(a.loadAll(ctx, envoys)),
		}, nil
	})
}

// loadAll fetches every non-revoked envoy's `puddle` trait envelope.
func (a *Aggregator) loadAll(ctx context.Context, envoys []EnvoyInput) []*traitEnvelope {
	envs := make([]*traitEnvelope, 0, len(envoys))
	for _, e := range envoys {
		if e.Revoked {
			continue
		}
		if env, ok := a.loadTrait(ctx, e.ID); ok {
			envs = append(envs, env)
		}
	}
	return envs
}

// build fetches every envoy's `puddle` trait, groups per-(envoy, user)
// entries by puddle pubkey, resolves owner names, and classifies health.
func (a *Aggregator) build(ctx context.Context, envoys []EnvoyInput) *Fleet {
	byPubkey := make(map[string][]entryFor)
	allEnvs := make([]*traitEnvelope, 0, len(envoys))
	now := time.Now()
	for _, e := range envoys {
		if e.Revoked {
			continue
		}
		env, ok := a.loadTrait(ctx, e.ID)
		if !ok {
			continue
		}
		allEnvs = append(allEnvs, env)
		stale := now.Unix()-env.UpdatedAt > int64(StaleAfter.Seconds())
		for _, p := range env.Puddles {
			pk := strings.ToLower(p.PuddlePubkey)
			if pk == "" {
				continue
			}
			byPubkey[pk] = append(byPubkey[pk], entryFor{
				hostname: e.Hostname, updated: env.UpdatedAt, stale: stale, p: p,
			})
		}
	}

	// Owner names + contested set, from the same envelopes.
	grouped := groupNameClaims(allEnvs)
	nameByPubkey := unambiguousNameByPubkey(grouped)
	contested := contestedPubkeys(grouped)

	hostsHolding := make(map[string]struct{})
	summaries := make([]PuddleSummary, 0, len(byPubkey))
	for pk, entries := range byPubkey {
		s := summarizePuddle(pk, entries, nameByPubkey[pk], contested[pk])
		for _, ep := range s.Envoys {
			hostsHolding[ep.Hostname] = struct{}{}
		}
		summaries = append(summaries, s)
	}

	sort.Slice(summaries, func(i, j int) bool {
		// Named puddles first (by name), then unnamed by pubkey.
		if (summaries[i].Owner == "") != (summaries[j].Owner == "") {
			return summaries[i].Owner != ""
		}
		if summaries[i].Owner != summaries[j].Owner {
			return summaries[i].Owner < summaries[j].Owner
		}
		return summaries[i].PuddlePubkey < summaries[j].PuddlePubkey
	})

	fleet := &Fleet{GeneratedAt: now.UTC(), Puddles: summaries}
	fleet.Totals.Puddles = len(summaries)
	fleet.Totals.Envoys = len(hostsHolding)
	for _, s := range summaries {
		if s.Owner != "" {
			fleet.Totals.Named++
		}
		if s.SessionUnlocked {
			fleet.Totals.Unlocked++
		}
		if s.Health.Band != HealthHealthy {
			fleet.Totals.Unhealthy++
		}
	}
	return fleet
}

// summarizePuddle collapses one puddle's per-(envoy, user) entries into a
// PuddleSummary and classifies its health.
func summarizePuddle(pubkey string, entries []entryFor, owner string, ownerContested bool) PuddleSummary {
	presences := make([]EnvoyPresence, 0, len(entries))
	users := make(map[string]struct{})
	var lastUpdated int64
	var retired int
	unlocked := false
	rekeyPending := false
	reporting := 0
	for _, e := range entries {
		status := "reporting"
		if e.stale {
			status = "stale"
		} else {
			reporting++
		}
		if e.updated > lastUpdated {
			lastUpdated = e.updated
		}
		if e.p.RetiredCount > retired {
			retired = e.p.RetiredCount
		}
		if !e.stale && e.p.SessionUnlocked {
			unlocked = true
		}
		if e.p.RekeyPending {
			rekeyPending = true
		}
		users[e.p.User] = struct{}{}
		presences = append(presences, EnvoyPresence{
			Hostname:        e.hostname,
			User:            e.p.User,
			Status:          status,
			UpdatedAt:       e.updated,
			Initialized:     e.p.Initialized,
			SessionUnlocked: e.p.SessionUnlocked,
			PairPending:     e.p.PairPending,
			RekeyPending:    e.p.RekeyPending,
		})
	}
	sort.Slice(presences, func(i, j int) bool {
		if presences[i].Hostname != presences[j].Hostname {
			return presences[i].Hostname < presences[j].Hostname
		}
		return presences[i].User < presences[j].User
	})
	userList := make([]string, 0, len(users))
	for u := range users {
		userList = append(userList, u)
	}
	sort.Strings(userList)

	s := PuddleSummary{
		PuddlePubkey:    pubkey,
		Owner:           owner,
		OwnerContested:  ownerContested,
		Envoys:          presences,
		EnvoyCount:      len(presences),
		Users:           userList,
		SessionUnlocked: unlocked,
		RetiredCount:    retired,
		RekeyPending:    rekeyPending,
		LastUpdated:     lastUpdated,
	}
	s.Health = classifyHealth(&s, reporting)
	return s
}

// entryFor is the per-(envoy, user) row summarizePuddle consumes. Named
// separately from the closure type in build so summarizePuddle is
// independently testable.
type entryFor struct {
	hostname string
	updated  int64
	stale    bool
	p        puddleTrait
}

// classifyHealth derives a puddle's health band from its presences.
//
//   - degraded: no envoy currently reporting (every holder stale → the
//     puddle is unreachable), or it isn't initialized on any reporting
//     holder.
//   - warning: the owner name is contested (split identity), a rekey is
//     in progress, or some — but not all — holders are stale.
//   - healthy: at least one reporting, initialized holder; name
//     uncontested; nothing mid-rotation.
func classifyHealth(s *PuddleSummary, reporting int) Health {
	var reasons []string
	initializedSomewhere := false
	staleCount := 0
	for _, ep := range s.Envoys {
		if ep.Status == "stale" {
			staleCount++
			continue
		}
		if ep.Initialized {
			initializedSomewhere = true
		}
	}

	if reporting == 0 {
		return Health{Band: HealthDegraded, Reasons: []string{"no envoy currently reporting this puddle"}}
	}
	if !initializedSomewhere {
		return Health{Band: HealthDegraded, Reasons: []string{"not initialized on any reporting envoy"}}
	}

	if s.OwnerContested {
		reasons = append(reasons, "name claimed by more than one puddle")
	}
	if s.RekeyPending {
		reasons = append(reasons, "key rotation in progress")
	}
	if staleCount > 0 {
		reasons = append(reasons, "some holders are stale")
	}
	if len(reasons) > 0 {
		return Health{Band: HealthWarning, Reasons: reasons}
	}
	return Health{Band: HealthHealthy}
}

// --- friendly-name map (ADR-022) — single producer ---

// FleetNames is the aggregated friendly-name map. Also the JSON shape of
// `GET /api/v1/swarm/puddle/names` and of the agent's local cache at
// `/var/lib/vigo/swarm/puddle/names.json`. A name with one claim is
// unambiguous; a name with several is either a collision (the resolver
// errors, pointing at the hex form) or a `re-found` supersession (the
// resolver follows the newer claim's `supersedes` chain). The server
// emits all of them and decides nothing.
type FleetNames struct {
	UpdatedAt int64                  `json:"updated_at"` // unix seconds
	Names     map[string][]NameClaim `json:"names"`
}

// NameClaim mirrors the agent's `puddle::name::NameClaim` wire shape.
// Used both for parsing the trait and for the REST / cache output, so
// the claim bytes flow through unchanged and resolvers can verify the
// signature + `supersedes` chain end-to-end.
type NameClaim struct {
	Body      NameClaimBody `json:"body"`
	Signature string        `json:"signature"`
}

// NameClaimBody is the signed body of a name claim. `Supersedes` is kept
// as raw JSON since the server only needs `Name` / `PuddlePubkey` /
// `ClaimedAt` for grouping + dedup, and re-emitting the chain verbatim
// preserves its signatures.
type NameClaimBody struct {
	PuddlePubkey string          `json:"puddle_pubkey"`
	Name         string          `json:"name"`
	ClaimedAt    int64           `json:"claimed_at"`
	Supersedes   json.RawMessage `json:"supersedes,omitempty"`
}

// groupNameClaims collapses the name claims carried on every envoy's
// puddle trait into a `name → claims` map, deduped by puddle pubkey (the
// freshest `claimed_at` wins when several envoys report different
// revisions of the same puddle's claim during a propagation window). No
// arbitration: every distinct claimant for a name is kept, sorted by
// pubkey for stable output.
func groupNameClaims(envs []*traitEnvelope) map[string][]NameClaim {
	byName := make(map[string]map[string]NameClaim) // name → pubkey(lower) → freshest
	for _, env := range envs {
		for _, p := range env.Puddles {
			if p.NameClaim == nil {
				continue
			}
			nc := *p.NameClaim
			name := nc.Body.Name
			pk := strings.ToLower(nc.Body.PuddlePubkey)
			if name == "" || pk == "" {
				continue
			}
			m := byName[name]
			if m == nil {
				m = make(map[string]NameClaim)
				byName[name] = m
			}
			if existing, seen := m[pk]; !seen || nc.Body.ClaimedAt > existing.Body.ClaimedAt {
				m[pk] = nc
			}
		}
	}
	names := make(map[string][]NameClaim, len(byName))
	for name, m := range byName {
		list := make([]NameClaim, 0, len(m))
		for _, c := range m {
			list = append(list, c)
		}
		sort.Slice(list, func(i, j int) bool {
			return list[i].Body.PuddlePubkey < list[j].Body.PuddlePubkey
		})
		names[name] = list
	}
	return names
}

// unambiguousNameByPubkey reverses the grouped name map into
// puddle_pubkey(lower) → friendly name. Only names with a single
// claimant survive — a contested name resolves to nothing and the hex
// project_id stays the proof (ADR-022). If a puddle has claimed several
// distinct uncontested names, the freshest `claimed_at` wins.
func unambiguousNameByPubkey(grouped map[string][]NameClaim) map[string]string {
	type pick struct {
		name string
		at   int64
	}
	best := make(map[string]pick)
	for name, claims := range grouped {
		if len(claims) != 1 {
			continue // contested — not a usable display name
		}
		c := claims[0]
		pk := strings.ToLower(c.Body.PuddlePubkey)
		if pk == "" {
			continue
		}
		if cur, ok := best[pk]; !ok || c.Body.ClaimedAt > cur.at {
			best[pk] = pick{name: name, at: c.Body.ClaimedAt}
		}
	}
	out := make(map[string]string, len(best))
	for pk, p := range best {
		out[pk] = p.name
	}
	return out
}

// contestedPubkeys returns the set of puddle pubkeys (lowercase) that
// participate in a contested name — a name claimed by more than one
// distinct pubkey. Used to flag split-identity health on the topology
// view.
func contestedPubkeys(grouped map[string][]NameClaim) map[string]bool {
	out := make(map[string]bool)
	for _, claims := range grouped {
		if len(claims) <= 1 {
			continue
		}
		for _, c := range claims {
			out[strings.ToLower(c.Body.PuddlePubkey)] = true
		}
	}
	return out
}

// ReverseNameMap returns the puddle-pubkey-hex → friendly-name map for
// every unambiguously-claimed name in the fleet (ADR-022); a contested
// name resolves to nothing. The single exported home of the
// unambiguous-claim rule for callers outside this package — gitbackmesh
// uses it for the `gitback://<name>/<handle>` display hint and the
// curator S3 facade for the `<publisher>` object-key segment.
func ReverseNameMap(fn *FleetNames) map[string]string {
	if fn == nil {
		return map[string]string{}
	}
	return unambiguousNameByPubkey(fn.Names)
}

// loadTrait pulls the latest traits row for an envoy and extracts the
// `puddle` namespace. Missing rows, malformed JSON, and a missing
// `puddle` namespace all collapse to `(nil, false)` — "no puddle
// reported".
func (a *Aggregator) loadTrait(ctx context.Context, envoyID string) (*traitEnvelope, bool) {
	tr, err := a.queries.GetLatestEnvoyTraits(ctx, envoyID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, false
		}
		return nil, false
	}
	var outer struct {
		Puddle *traitEnvelope `json:"puddle"`
	}
	if err := json.Unmarshal([]byte(tr.Traits), &outer); err != nil {
		return nil, false
	}
	if outer.Puddle == nil {
		return nil, false
	}
	return outer.Puddle, true
}

// --- trait payload shapes (deserialized from agent JSON) ---

type traitEnvelope struct {
	UpdatedAt   int64         `json:"updated_at"`
	PuddleCount int           `json:"puddle_count"`
	Puddles     []puddleTrait `json:"puddles"`
}

type puddleTrait struct {
	User            string     `json:"user"`
	PuddlePubkey    string     `json:"puddle_pubkey"`
	Initialized     bool       `json:"initialized"`
	SessionUnlocked bool       `json:"session_unlocked"`
	PairPending     bool       `json:"pair_pending"`
	RekeyPending    bool       `json:"rekey_pending"`
	RetiredCount    int        `json:"retired_count"`
	NameClaim       *NameClaim `json:"name_claim,omitempty"`
}
