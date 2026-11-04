// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: Apache-2.0

// Package gitbackmesh builds the fleet-wide gitback observability
// view by aggregating per-envoy `gitback` trait payloads keyed on
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
package gitbackmesh

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"git.alexander4.io/vigo/server/db"
	"git.alexander4.io/vigo/server/swarm"
	"git.alexander4.io/vigo/server/swarm/puddlemesh"
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
	ProjectID           string `json:"project_id"`
	Handle              string `json:"handle"`
	FounderPuddlePubkey string `json:"founder_puddle_pubkey"`
	// FriendlyName is the founder puddle's claimed name (ADR-022),
	// or "" when the puddle hasn't claimed one or the name it
	// claimed is contested. When set, `gitback://<FriendlyName>/<Handle>`
	// resolves to this project alongside the canonical hex form. It's a
	// display hint only — the project_id is the cryptographic identity.
	FriendlyName string   `json:"friendly_name,omitempty"`
	Members      []string `json:"members"`
	DrScope      string   `json:"dr_scope"`

	Hosts    []HostPresence       `json:"hosts"`
	HeadRefs map[string]RefStatus `json:"head_refs"`

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
// page to attach gitback-specific context.
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
	Host        string `json:"host"`
	UpdatedAt   int64  `json:"updated_at"`
	HasBareRepo bool   `json:"has_bare_repo"`
	// SizeBytes is int64 (not the uint64 the wire trait uses) so the
	// `humanBytes` template helper — which takes int64 — renders it
	// without a per-row cast. Bare-repo footprints stay within int64
	// range indefinitely in practice.
	SizeBytes       int64             `json:"size_bytes,omitempty"`
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
	// puddle resolves the founder puddle's friendly name (ADR-022) for
	// the `gitback://<name>/<handle>` display hint on each project row.
	// The friendly-name map is owned by puddlemesh; gitbackmesh is a
	// consumer. May be nil — callers that don't need the display hint
	// (e.g. the fleet-summary snapshot) pass nil and FriendlyName stays
	// empty.
	puddle *puddlemesh.Aggregator

	// cache rebuilds outside its lock (swarm.MeshCache): a cache hit never
	// waits behind a fleet-wide trait scan.
	cache *swarm.MeshCache[*Fleet]
}

// NewAggregator builds an aggregator bound to the given query handle.
// `puddle` (nullable) supplies the friendly-name map for project-row
// display; pass nil when the friendly name isn't needed. The cache TTL
// is 30 seconds — matches the typical check-in window closely enough
// that an operator refreshing the page sees data no more than one
// check-in stale.
func NewAggregator(queries *db.Queries, puddle *puddlemesh.Aggregator) *Aggregator {
	return &Aggregator{queries: queries, puddle: puddle, cache: swarm.NewMeshCache[*Fleet](swarm.MeshCacheTTL)}
}

// Snapshot returns the cached fleet view or rebuilds it from the
// passed envoy list.
func (a *Aggregator) Snapshot(ctx context.Context, envoys []EnvoyInput) (*Fleet, error) {
	return a.cache.Get(func() (*Fleet, error) { return a.build(ctx, envoys) })
}

// build fetches every envoy's `gitback` trait, joins per-envoy
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

	// Founder puddle display names (ADR-022). The friendly-name map is
	// owned by puddlemesh; gitbackmesh consumes it as a display hint
	// only — the hex project_id is always the cryptographic identity. A
	// nil resolver (or a query error) leaves FriendlyName empty.
	var nameByPubkey map[string]string
	if a.puddle != nil {
		if fn, err := a.puddle.NamesSnapshot(ctx, toPuddleInputs(envoys)); err == nil {
			nameByPubkey = puddlemesh.ReverseNameMap(fn)
		}
	}

	summaries := make([]ProjectSummary, 0, len(by))
	for projectID, slot := range by {
		s := summarizeProject(projectID, slot.entries, slot.hosts, slot.updated)
		s.FriendlyName = nameByPubkey[strings.ToLower(s.FounderPuddlePubkey)]
		summaries = append(summaries, s)
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
// and dr_scope fields are taken from the most-recently-updated
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
			SizeBytes:       int64(e.BareRepoSizeBytes),
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
		ProjectID:           projectID,
		Handle:              canonical.Handle,
		FounderPuddlePubkey: canonical.FounderPuddlePubkey,
		Members:             append([]string(nil), canonical.Members...),
		DrScope:             canonical.DrScope,
		Hosts:               hostsList,
		HeadRefs:            headRefs,
		MemberFleetCount:    len(canonical.Members),
		HostCount:           len(hostsList),
		LastUpdated:         lastUpdated,
		DivergentRefs:       divergent,
		RevokesCount:        revokes,
		LeavesCount:         leaves,
		DigestUnanimous:     digestUnanimous,
	}
}

// EnvoyProject is one envoy's local-state view of a project,
// projected out of the gitback trait for the per-envoy Swarm Load
// page. Replaces the legacy `EnvoyMirror` shape.
type EnvoyProject struct {
	ProjectID       string
	Handle          string
	HasBareRepo     bool
	SizeBytes       uint64
	BareRepoPath    string
	HeadRefs        map[string]string
	UsersWithState  []string
	LeafRole        string
	TokenChainDepth int
	RevokesCount    int
	LeavesCount     int
	DrScope         string
}

// EnvoyProjects returns the project entries the envoy reported in
// its most recent `gitback` trait. Returns nil when the envoy
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
			BareRepoPath:    fmt.Sprintf("/var/lib/vigo/swarm/gitback/projects/%s.git", p.ProjectID),
			HeadRefs:        p.HeadRefs,
			UsersWithState:  p.UsersWithState,
			LeafRole:        p.LeafRole,
			TokenChainDepth: p.TokenChainDepth,
			RevokesCount:    p.Envelopes.Revokes,
			LeavesCount:     p.Envelopes.Leaves,
			DrScope:         p.DrScope,
		})
	}
	return out
}

// loadTrait pulls the latest traits row for an envoy and extracts
// the `gitback` namespace. Missing rows, malformed JSON, and a
// missing `gitback` namespace all collapse to `(nil, false)` —
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
		Gitback *traitEnvelope `json:"gitback"`
	}
	if err := json.Unmarshal([]byte(tr.Traits), &env); err != nil {
		return nil, false
	}
	if env.Gitback == nil {
		return nil, false
	}
	return env.Gitback, true
}

// --- trait payload shapes (deserialized from agent JSON) ---

// toPuddleInputs converts gitbackmesh's envoy tuples to puddlemesh's
// equivalent so the friendly-name map (owned by puddlemesh) can be
// resolved over the same fleet.
func toPuddleInputs(envoys []EnvoyInput) []puddlemesh.EnvoyInput {
	out := make([]puddlemesh.EnvoyInput, 0, len(envoys))
	for _, e := range envoys {
		out = append(out, puddlemesh.EnvoyInput{ID: e.ID, Hostname: e.Hostname, Revoked: e.Revoked})
	}
	return out
}

type traitEnvelope struct {
	UpdatedAt    int64          `json:"updated_at"`
	ProjectCount int            `json:"project_count"`
	Projects     []projectTrait `json:"projects"`
}

type projectTrait struct {
	ProjectID           string              `json:"project_id"`
	Handle              string              `json:"handle"`
	FounderPuddlePubkey string              `json:"founder_puddle_pubkey"`
	Members             []string            `json:"members"`
	DrScope             string              `json:"dr_scope"`
	UsersWithState      []string            `json:"users_with_state"`
	HasBareRepo         bool                `json:"has_bare_repo"`
	BareRepoSizeBytes   uint64              `json:"bare_repo_size_bytes"`
	HeadRefs            map[string]string   `json:"head_refs"`
	Envelopes           envelopeCountsTrait `json:"envelopes"`
	LeafRole            string              `json:"leaf_role,omitempty"`
	TokenChainDepth     int                 `json:"token_chain_depth"`
	TokenExpiresAt      *int64              `json:"token_expires_at"`
}

type envelopeCountsTrait struct {
	Revokes   int    `json:"revokes"`
	Leaves    int    `json:"leaves"`
	DigestHex string `json:"digest_hex"`
}

func (p projectTrait) envelopesDigestHex() string {
	return p.Envelopes.DigestHex
}
