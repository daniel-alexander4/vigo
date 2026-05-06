// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

// Package gitmirrormesh builds the fleet-wide gitmirror observability
// view by aggregating per-envoy `gitmirror` trait payloads keyed on
// project_id (ADR-015).
//
// Post-ADR-015 there is no publisher/receiver distinction: every
// envoy holding a project announces it via the swarm, the helper
// daemon mediates push/fetch over a system Unix socket, and the
// server has zero coordination state. This package is the sole
// place the server reconstructs per-project mesh state, and it does
// so entirely from reports that already flow up via the standard
// trait pipeline — no new RPC.
//
// Lives in its own package so both `server/api` (REST) and `ui`
// (web) can consume it without creating an import cycle through
// `server/fleet`.
package gitmirrormesh

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"sync"
	"time"

	"git.alexander4.io/vigo/server/db"
)

// EnvoyInput is the minimal envoy descriptor the aggregator needs
// from its caller — (ID, Hostname). Accepting a slice of these
// instead of importing `server/fleet` keeps this package at the
// bottom of the import graph so both the UI and the REST handler
// can use it.
type EnvoyInput struct {
	ID       string
	Hostname string
	Revoked  bool
}

// Fleet is the top-level aggregated view.
type Fleet struct {
	GeneratedAt  time.Time        `json:"generated_at"`
	Projects     []ProjectSummary `json:"projects"`
	Participants []string         `json:"participants"`
	Totals       Totals           `json:"totals"`
}

// Totals carries pre-computed counts the UI can render without
// re-walking the projects slice.
type Totals struct {
	Projects      int `json:"projects"`
	Hosts         int `json:"hosts"`
	DivergentRefs int `json:"divergent_refs"`
	TotalRevokes  int `json:"total_revokes"`
	TotalLeaves   int `json:"total_leaves"`
}

// ProjectSummary aggregates one project across the fleet.
type ProjectSummary struct {
	ProjectID          string               `json:"project_id"`
	Handle             string               `json:"handle"`
	FounderPuddlePubkey string               `json:"founder_puddle_pubkey"`
	Members            []string             `json:"members"`
	DrFanout           string               `json:"dr_fanout"`

	Hosts            []HostPresence       `json:"hosts"`
	HeadRefs         map[string]RefStatus `json:"head_refs"`

	MemberFleetCount int      `json:"member_fleet_count"`
	HostCount        int      `json:"host_count"`
	LastUpdated      int64    `json:"last_updated"`
	DivergentRefs    []string `json:"divergent_refs,omitempty"`
	RevokesCount     int      `json:"revokes_count"`
	LeavesCount      int      `json:"leaves_count"`
	DigestUnanimous  bool     `json:"digest_unanimous"`
}

// LookupProject returns the summary for a project_id_hex, or nil if
// the fleet snapshot doesn't carry it. Used by the swarm detail
// page to attach gitmirror-specific context.
func (f *Fleet) LookupProject(projectID string) *ProjectSummary {
	if f == nil {
		return nil
	}
	for i := range f.Projects {
		if f.Projects[i].ProjectID == projectID {
			return &f.Projects[i]
		}
	}
	return nil
}

// LookupByHandle returns the first project with a matching handle.
// Handles are NOT cryptographically authoritative — two projects
// can share a handle — so this is a UX convenience only. Use
// LookupProject when uniqueness matters.
func (f *Fleet) LookupByHandle(handle string) *ProjectSummary {
	if f == nil || handle == "" {
		return nil
	}
	for i := range f.Projects {
		if f.Projects[i].Handle == handle {
			return &f.Projects[i]
		}
	}
	return nil
}

// HostPresence describes one envoy's view of a project.
type HostPresence struct {
	Host            string            `json:"host"`
	UpdatedAt       int64             `json:"updated_at"`
	HasBareRepo     bool              `json:"has_bare_repo"`
	SizeBytes       uint64            `json:"size_bytes,omitempty"`
	HeadRefs        map[string]string `json:"head_refs,omitempty"`
	EnvelopesDigest string            `json:"envelopes_digest,omitempty"`
	UsersWithState  []string          `json:"users_with_state,omitempty"`
	LeafRole        string            `json:"leaf_role,omitempty"`
}

// RefStatus carries the consensus view of one ref across hosts.
// Sha is non-empty when every host holding the project agrees;
// when hosts disagree, Sha is empty and the ref is added to
// ProjectSummary.DivergentRefs.
type RefStatus struct {
	Sha           string   `json:"sha,omitempty"`
	HostsAgreeing []string `json:"hosts_agreeing,omitempty"`
	HostsLagging  []string `json:"hosts_lagging,omitempty"`
}

// Aggregator holds the 30-second in-memory TTL cache so repeated
// calls within a burst (e.g. UI page load that fans into the HTMX
// filter fragment) don't re-parse every envoy's trait blob each
// time.
type Aggregator struct {
	queries *db.Queries

	mu       sync.Mutex
	cache    *Fleet
	cacheExp time.Time
	ttl      time.Duration
}

// NewAggregator builds an aggregator bound to the given query
// handle. The cache TTL is 30 seconds — matches the typical
// check-in window closely enough that an operator refreshing the
// page sees data no more than one check-in stale.
func NewAggregator(queries *db.Queries) *Aggregator {
	return &Aggregator{queries: queries, ttl: 30 * time.Second}
}

// Snapshot returns the cached fleet view or rebuilds it from the
// passed envoy list.
func (a *Aggregator) Snapshot(ctx context.Context, envoys []EnvoyInput) (*Fleet, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.cache != nil && time.Now().Before(a.cacheExp) {
		return a.cache, nil
	}
	fleet, err := a.build(ctx, envoys)
	if err != nil {
		return nil, err
	}
	a.cache = fleet
	a.cacheExp = time.Now().Add(a.ttl)
	return fleet, nil
}

// build fetches every envoy's `gitmirror` trait, joins per-envoy
// project entries by project_id, and computes per-ref consensus.
func (a *Aggregator) build(ctx context.Context, envoys []EnvoyInput) (*Fleet, error) {
	type envelopeRow struct {
		hostname string
		envelope *traitEnvelope
	}
	rows := make([]envelopeRow, 0, len(envoys))
	for _, e := range envoys {
		if e.Revoked {
			continue
		}
		env, ok := a.loadTrait(ctx, e.ID)
		if !ok {
			continue
		}
		rows = append(rows, envelopeRow{hostname: e.Hostname, envelope: env})
	}

	// Group per-envoy project entries by project_id.
	type projectSlot struct {
		entries []projectTrait
		hosts   []string // parallel to entries
		updated []int64
	}
	by := make(map[string]*projectSlot)
	participants := make(map[string]struct{})
	for _, row := range rows {
		participants[row.hostname] = struct{}{}
		for _, p := range row.envelope.Projects {
			slot := by[p.ProjectID]
			if slot == nil {
				slot = &projectSlot{}
				by[p.ProjectID] = slot
			}
			slot.entries = append(slot.entries, p)
			slot.hosts = append(slot.hosts, row.hostname)
			slot.updated = append(slot.updated, row.envelope.UpdatedAt)
		}
	}

	summaries := make([]ProjectSummary, 0, len(by))
	for projectID, slot := range by {
		summaries = append(summaries, summarizeProject(projectID, slot.entries, slot.hosts, slot.updated))
	}

	sort.Slice(summaries, func(i, j int) bool {
		// Sort by handle for human-readable output, ties broken by project_id.
		if summaries[i].Handle != summaries[j].Handle {
			return summaries[i].Handle < summaries[j].Handle
		}
		return summaries[i].ProjectID < summaries[j].ProjectID
	})

	partList := make([]string, 0, len(participants))
	for h := range participants {
		partList = append(partList, h)
	}
	sort.Strings(partList)

	fleet := &Fleet{
		GeneratedAt:  time.Now().UTC(),
		Projects:     summaries,
		Participants: partList,
	}
	fleet.Totals.Projects = len(summaries)
	hostsHoldingAny := make(map[string]struct{})
	for _, s := range summaries {
		for _, h := range s.Hosts {
			hostsHoldingAny[h.Host] = struct{}{}
		}
		fleet.Totals.DivergentRefs += len(s.DivergentRefs)
		fleet.Totals.TotalRevokes += s.RevokesCount
		fleet.Totals.TotalLeaves += s.LeavesCount
	}
	fleet.Totals.Hosts = len(hostsHoldingAny)
	return fleet, nil
}

// summarizeProject collapses one project's per-host trait entries
// into a ProjectSummary. The project_id, handle, founder, members,
// and dr_fanout fields are taken from the most-recently-updated
// host's report (gossip should have converged identical values
// across the fleet, but if any field disagrees we surface the
// freshest as the canonical view).
func summarizeProject(projectID string, entries []projectTrait, hosts []string, updated []int64) ProjectSummary {
	if len(entries) == 0 {
		return ProjectSummary{ProjectID: projectID}
	}

	// Pick the freshest host's view for the static project metadata.
	freshIdx := 0
	for i := 1; i < len(updated); i++ {
		if updated[i] > updated[freshIdx] {
			freshIdx = i
		}
	}
	canonical := entries[freshIdx]

	// Build per-host presence + ref union.
	refToShas := make(map[string]map[string][]string) // ref → sha → hosts
	hostsList := make([]HostPresence, 0, len(entries))
	for i, e := range entries {
		hp := HostPresence{
			Host:            hosts[i],
			UpdatedAt:       updated[i],
			HasBareRepo:     e.HasBareRepo,
			SizeBytes:       e.BareRepoSizeBytes,
			HeadRefs:        e.HeadRefs,
			EnvelopesDigest: e.envelopesDigestHex(),
			UsersWithState:  append([]string(nil), e.UsersWithState...),
			LeafRole:        e.LeafRole,
		}
		hostsList = append(hostsList, hp)
		for ref, sha := range e.HeadRefs {
			byRef := refToShas[ref]
			if byRef == nil {
				byRef = make(map[string][]string)
				refToShas[ref] = byRef
			}
			byRef[sha] = append(byRef[sha], hosts[i])
		}
	}
	sort.Slice(hostsList, func(i, j int) bool {
		return hostsList[i].Host < hostsList[j].Host
	})

	// Collapse into RefStatus + DivergentRefs.
	headRefs := make(map[string]RefStatus, len(refToShas))
	divergent := make([]string, 0)
	for ref, byRef := range refToShas {
		switch len(byRef) {
		case 1:
			// All hosts agree on this ref.
			for sha, agreeing := range byRef {
				sort.Strings(agreeing)
				headRefs[ref] = RefStatus{
					Sha:           sha,
					HostsAgreeing: agreeing,
				}
			}
		default:
			// Two or more distinct shas — pick the majority as
			// the consensus, list the others as lagging.
			var majoritySha string
			var majorityHosts []string
			lagging := make([]string, 0)
			for sha, hostsHere := range byRef {
				if len(hostsHere) > len(majorityHosts) {
					if majoritySha != "" {
						lagging = append(lagging, majorityHosts...)
					}
					majoritySha = sha
					majorityHosts = hostsHere
				} else {
					lagging = append(lagging, hostsHere...)
				}
			}
			sort.Strings(majorityHosts)
			sort.Strings(lagging)
			headRefs[ref] = RefStatus{
				Sha:           "", // empty signals divergence
				HostsAgreeing: majorityHosts,
				HostsLagging:  lagging,
			}
			divergent = append(divergent, ref)
		}
	}
	sort.Strings(divergent)

	// Digest unanimity: every host's envelopes_digest matches.
	digestUnanimous := true
	if len(hostsList) > 0 {
		first := hostsList[0].EnvelopesDigest
		for _, h := range hostsList[1:] {
			if h.EnvelopesDigest != first {
				digestUnanimous = false
				break
			}
		}
	}

	// Envelope counts: take the canonical (freshest) host's report.
	revokes := canonical.Envelopes.Revokes
	leaves := canonical.Envelopes.Leaves

	// Last updated: max across hosts.
	var lastUpdated int64
	for _, ts := range updated {
		if ts > lastUpdated {
			lastUpdated = ts
		}
	}

	return ProjectSummary{
		ProjectID:          projectID,
		Handle:             canonical.Handle,
		FounderPuddlePubkey: canonical.FounderPuddlePubkey,
		Members:            append([]string(nil), canonical.Members...),
		DrFanout:           canonical.DrFanout,
		Hosts:              hostsList,
		HeadRefs:           headRefs,
		MemberFleetCount:   len(canonical.Members),
		HostCount:          len(hostsList),
		LastUpdated:        lastUpdated,
		DivergentRefs:      divergent,
		RevokesCount:       revokes,
		LeavesCount:        leaves,
		DigestUnanimous:    digestUnanimous,
	}
}

// EnvoyProject is one envoy's local-state view of a project,
// projected out of the gitmirror trait for the per-envoy Swarm Load
// page. Replaces the legacy `EnvoyMirror` shape.
type EnvoyProject struct {
	ProjectID         string
	Handle            string
	HasBareRepo       bool
	SizeBytes         uint64
	BareRepoPath      string
	HeadRefs          map[string]string
	UsersWithState    []string
	LeafRole          string
	TokenChainDepth   int
	RevokesCount      int
	LeavesCount       int
	DrFanout          string
}

// EnvoyProjects returns the project entries the envoy reported in
// its most recent `gitmirror` trait. Returns nil when the envoy
// hasn't reported yet (or has no projects).
func (a *Aggregator) EnvoyProjects(ctx context.Context, envoyID string) []EnvoyProject {
	env, ok := a.loadTrait(ctx, envoyID)
	if !ok {
		return nil
	}
	out := make([]EnvoyProject, 0, len(env.Projects))
	for _, p := range env.Projects {
		out = append(out, EnvoyProject{
			ProjectID:       p.ProjectID,
			Handle:          p.Handle,
			HasBareRepo:     p.HasBareRepo,
			SizeBytes:       p.BareRepoSizeBytes,
			BareRepoPath:    fmt.Sprintf("/var/lib/vigo/swarm/gitmirror/projects/%s.git", p.ProjectID),
			HeadRefs:        p.HeadRefs,
			UsersWithState:  p.UsersWithState,
			LeafRole:        p.LeafRole,
			TokenChainDepth: p.TokenChainDepth,
			RevokesCount:    p.Envelopes.Revokes,
			LeavesCount:     p.Envelopes.Leaves,
			DrFanout:        p.DrFanout,
		})
	}
	return out
}

// loadTrait pulls the latest traits row for an envoy and extracts
// the `gitmirror` namespace. Missing rows, malformed JSON, and a
// missing `gitmirror` namespace all collapse to `(nil, false)` —
// treated the same as "no participation reported".
func (a *Aggregator) loadTrait(ctx context.Context, envoyID string) (*traitEnvelope, bool) {
	tr, err := a.queries.GetLatestEnvoyTraits(ctx, envoyID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, false
		}
		return nil, false
	}
	var env struct {
		Gitmirror *traitEnvelope `json:"gitmirror"`
	}
	if err := json.Unmarshal([]byte(tr.Traits), &env); err != nil {
		return nil, false
	}
	if env.Gitmirror == nil {
		return nil, false
	}
	return env.Gitmirror, true
}

// --- trait payload shapes (deserialized from agent JSON) ---

type traitEnvelope struct {
	UpdatedAt    int64          `json:"updated_at"`
	ProjectCount int            `json:"project_count"`
	Projects     []projectTrait `json:"projects"`
}

type projectTrait struct {
	ProjectID          string                 `json:"project_id"`
	Handle             string                 `json:"handle"`
	FounderPuddlePubkey string                 `json:"founder_puddle_pubkey"`
	Members            []string               `json:"members"`
	DrFanout           string                 `json:"dr_fanout"`
	UsersWithState     []string               `json:"users_with_state"`
	HasBareRepo        bool                   `json:"has_bare_repo"`
	BareRepoSizeBytes  uint64                 `json:"bare_repo_size_bytes"`
	HeadRefs           map[string]string      `json:"head_refs"`
	Envelopes          envelopeCountsTrait    `json:"envelopes"`
	LeafRole           string                 `json:"leaf_role,omitempty"`
	TokenChainDepth    int                    `json:"token_chain_depth"`
	TokenExpiresAt     *int64                 `json:"token_expires_at"`
}

type envelopeCountsTrait struct {
	Revokes   int    `json:"revokes"`
	Leaves    int    `json:"leaves"`
	DigestHex string `json:"digest_hex"`
}

func (p projectTrait) envelopesDigestHex() string {
	return p.Envelopes.DigestHex
}
