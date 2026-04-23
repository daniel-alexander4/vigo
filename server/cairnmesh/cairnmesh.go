// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

// Package cairnmesh builds the fleet-wide cairn observability
// view by joining publisher-side `cairn.sources[].selected_mirrors`
// against receiver-side `cairn.mirrors[]` across every envoy's
// latest trait payload.
//
// Post-0.23.0 cairn is pure P2P: there is no server-side
// coordination state. This package is the sole place the server
// reconstructs per-label mesh state, and it does so entirely from
// reports that already flow up via the standard trait pipeline — no
// new RPC.
//
// Lives in its own package so both `server/api` (REST) and `ui`
// (web) can consume it without creating an import cycle through
// `server/fleet`.
package cairnmesh

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
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
	GeneratedAt  time.Time      `json:"generated_at"`
	Labels       []LabelSummary `json:"labels"`
	Participants []string       `json:"participants"`
	Totals       Totals         `json:"totals"`
}

// Totals carries pre-computed counts the UI can render without
// re-walking the labels slice.
type Totals struct {
	Sources       int `json:"sources"`
	Labels        int `json:"labels"`
	Mirrors       int `json:"mirrors"`
	OrphanMirrors int `json:"orphan_mirrors"`
	StaleMirrors  int `json:"stale_mirrors"`
}

// LabelSummary aggregates one cairn label across the fleet.
type LabelSummary struct {
	Label             string           `json:"label"`
	Publisher         string           `json:"publisher"`
	User              string           `json:"user"`
	Project           string           `json:"project"`
	Paused            bool             `json:"paused"`
	MaxMirrors        int              `json:"max_mirrors"`
	CrossSubnetMirrors      []string         `json:"cross_subnet_mirrors,omitempty"`
	Tags              []string         `json:"tags,omitempty"`
	Description       string           `json:"description,omitempty"`
	PublisherSeen     int64            `json:"publisher_seen"`
	// BlobSha is the swarm content seed sha256 of the publisher's
	// most recent bundle. Empty if the publisher hasn't succeeded
	// yet, or if the label is receiver-only (orphan). Lets the UI
	// link a label directly to its `/swarm/<sha256>` detail page.
	BlobSha           string           `json:"blob_sha,omitempty"`
	GitSha            string           `json:"git_sha,omitempty"`
	// LastPublishFull reports whether the publisher's most recent
	// bundle was a full (`true`) or incremental (`false`) bundle.
	// Nil when the publisher has never succeeded, or when reported
	// by a pre-0.24.1 agent that doesn't carry the field. Consumed
	// by `/swarm` UI and `vigocli repo` to explain why a cairn
	// seed is only a few KB — an incremental is expected to be
	// small; a full bundle should be repo-sized.
	LastPublishFull   *bool            `json:"last_publish_full,omitempty"`
	// LastBundleSize is the size in bytes of the most recent
	// bundle. Zero when unknown. Paired with LastPublishFull.
	LastBundleSize    int64            `json:"last_bundle_size,omitempty"`
	Mirrors           []MirrorPresence `json:"mirrors"`
	CountSelected     int              `json:"count_selected"`
	CountMaterialized int              `json:"count_materialized"`
	CountOrphan       int              `json:"count_orphan"`
	CountStale        int              `json:"count_stale"`
}

// LookupLabel returns the summary for a specific label, or nil if
// the fleet snapshot doesn't carry it. Used by the swarm detail
// page to attach cairn-specific context when a blob's label is
// cairn-shaped.
func (f *Fleet) LookupLabel(label string) *LabelSummary {
	if f == nil {
		return nil
	}
	for i := range f.Labels {
		if f.Labels[i].Label == label {
			return &f.Labels[i]
		}
	}
	return nil
}

// LookupBlobSha returns the summary for a label identified by the
// publisher's current bundle sha256. Lets the swarm detail page
// (which is keyed on sha256, not label) recover cairn context
// without a full fleet scan on every render.
func (f *Fleet) LookupBlobSha(sha string) *LabelSummary {
	if f == nil || sha == "" {
		return nil
	}
	for i := range f.Labels {
		if f.Labels[i].BlobSha == sha {
			return &f.Labels[i]
		}
	}
	return nil
}

// MirrorPresence describes one (label × host) cell of the join.
type MirrorPresence struct {
	Host          string `json:"host"`
	Status        string `json:"status"` // active | pending | orphan
	Materialized  bool   `json:"materialized"`
	Selected      bool   `json:"selected"`
	Stale         bool   `json:"stale,omitempty"`
	GitSha        string `json:"git_sha,omitempty"`
	BlobSha       string `json:"blob_sha,omitempty"`
	LastRefreshed int64  `json:"last_refreshed,omitempty"`
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

// build fetches every envoy's `cairn` trait, joins publisher
// sources against receiver mirrors by label, and computes per-cell
// status.
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

	// Receiver-side index: label → host → mirror trait entry.
	labelHolders := make(map[string]map[string]mirrorTrait)
	for _, row := range rows {
		for _, m := range row.envelope.Mirrors {
			hosts := labelHolders[m.Label]
			if hosts == nil {
				hosts = make(map[string]mirrorTrait)
				labelHolders[m.Label] = hosts
			}
			hosts[row.hostname] = m
		}
	}

	participants := make(map[string]struct{})
	summaries := make([]LabelSummary, 0)
	seenLabels := make(map[string]struct{})
	for _, row := range rows {
		participants[row.hostname] = struct{}{}
		for _, src := range row.envelope.Sources {
			summaries = append(summaries, summaryFromSource(
				row.hostname, src, row.envelope.UpdatedAt, labelHolders[src.Label]))
			seenLabels[src.Label] = struct{}{}
		}
	}

	// Receiver-only labels (publisher offline or disabled).
	for label, holders := range labelHolders {
		if _, seen := seenLabels[label]; seen {
			continue
		}
		summaries = append(summaries, orphanSummary(label, holders))
		for host := range holders {
			participants[host] = struct{}{}
		}
	}

	sort.Slice(summaries, func(i, j int) bool {
		return summaries[i].Label < summaries[j].Label
	})

	partList := make([]string, 0, len(participants))
	for h := range participants {
		partList = append(partList, h)
	}
	sort.Strings(partList)

	fleet := &Fleet{
		GeneratedAt:  time.Now().UTC(),
		Labels:       summaries,
		Participants: partList,
	}
	fleet.Totals.Labels = len(summaries)
	for _, s := range summaries {
		fleet.Totals.Mirrors += s.CountMaterialized
		fleet.Totals.OrphanMirrors += s.CountOrphan
		fleet.Totals.StaleMirrors += s.CountStale
	}
	for _, row := range rows {
		fleet.Totals.Sources += len(row.envelope.Sources)
	}
	return fleet, nil
}

// loadTrait pulls the latest traits row for an envoy and extracts
// the `cairn` namespace. Missing rows, malformed JSON, and a
// missing `cairn` namespace all collapse to `(nil, false)` —
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
		Cairn *traitEnvelope `json:"cairn"`
	}
	if err := json.Unmarshal([]byte(tr.Traits), &env); err != nil {
		return nil, false
	}
	if env.Cairn == nil {
		return nil, false
	}
	return env.Cairn, true
}

// --- trait payload shapes (deserialized from agent JSON) ---

type sourceTrait struct {
	Label           string   `json:"label"`
	Paused          bool     `json:"paused"`
	MaxMirrors      int      `json:"max_mirrors"`
	Tags            []string `json:"tags,omitempty"`
	Description     string   `json:"description,omitempty"`
	IncludeEnvoys   []string `json:"include_envoys,omitempty"`
	ExcludeEnvoys   []string `json:"exclude_envoys,omitempty"`
	When            string   `json:"when,omitempty"`
	CrossSubnetMirrors    []string `json:"cross_subnet_mirrors,omitempty"`
	GitSha          string   `json:"git_sha,omitempty"`
	BlobSha         string   `json:"blob_sha,omitempty"`
	LastPublishFull *bool    `json:"last_publish_full,omitempty"`
	LastBundleSize  int64    `json:"last_bundle_size,omitempty"`
	SelectedMirrors []string `json:"selected_mirrors,omitempty"`
}

type mirrorTrait struct {
	Label         string `json:"label"`
	Seeder        string `json:"seeder"`
	User          string `json:"user"`
	Project       string `json:"project"`
	GitSha        string `json:"git_sha"`
	BlobSha       string `json:"blob_sha"`
	LastRefreshed int64  `json:"last_refreshed"`
	Stale         bool   `json:"stale"`
	NextGcAt      int64  `json:"next_gc_at"`
}

type traitEnvelope struct {
	UpdatedAt     int64         `json:"updated_at"`
	ActiveSources int           `json:"active_sources"`
	PausedSources int           `json:"paused_sources"`
	Sources       []sourceTrait `json:"sources"`
	Mirrors       []mirrorTrait `json:"mirrors"`
	StaleMirrors  []string      `json:"stale_mirrors"`
}

// --- summary builders ---

func summaryFromSource(publisher string, src sourceTrait, publisherSeen int64, holders map[string]mirrorTrait) LabelSummary {
	_, user, project := ParseLabel(src.Label)
	selected := make(map[string]struct{}, len(src.SelectedMirrors))
	for _, h := range src.SelectedMirrors {
		selected[h] = struct{}{}
	}
	hosts := unionHosts(selected, holders)

	sum := LabelSummary{
		Label:           src.Label,
		Publisher:       publisher,
		User:            user,
		Project:         project,
		Paused:          src.Paused,
		MaxMirrors:      src.MaxMirrors,
		CrossSubnetMirrors:    src.CrossSubnetMirrors,
		Tags:            src.Tags,
		Description:     src.Description,
		PublisherSeen:   publisherSeen,
		BlobSha:         src.BlobSha,
		GitSha:          src.GitSha,
		LastPublishFull: src.LastPublishFull,
		LastBundleSize:  src.LastBundleSize,
	}
	for _, host := range hosts {
		mirror, hasMirror := holders[host]
		_, isSelected := selected[host]
		status := ""
		switch {
		case isSelected && hasMirror:
			status = "active"
		case isSelected && !hasMirror:
			status = "pending"
		case !isSelected && hasMirror:
			status = "orphan"
		}
		presence := MirrorPresence{
			Host:         host,
			Status:       status,
			Materialized: hasMirror,
			Selected:     isSelected,
		}
		if hasMirror {
			presence.Stale = mirror.Stale
			presence.GitSha = mirror.GitSha
			presence.BlobSha = mirror.BlobSha
			presence.LastRefreshed = mirror.LastRefreshed
		}
		sum.Mirrors = append(sum.Mirrors, presence)
		if presence.Selected {
			sum.CountSelected++
		}
		if presence.Materialized {
			sum.CountMaterialized++
		}
		if presence.Status == "orphan" {
			sum.CountOrphan++
		}
		if presence.Stale {
			sum.CountStale++
		}
	}
	return sum
}

func orphanSummary(label string, holders map[string]mirrorTrait) LabelSummary {
	seeder, user, project := ParseLabel(label)
	sum := LabelSummary{
		Label:     label,
		Publisher: seeder,
		User:      user,
		Project:   project,
	}
	hosts := make([]string, 0, len(holders))
	for h := range holders {
		hosts = append(hosts, h)
	}
	sort.Strings(hosts)
	for _, host := range hosts {
		mirror := holders[host]
		presence := MirrorPresence{
			Host:          host,
			Status:        "orphan",
			Materialized:  true,
			Selected:      false,
			Stale:         mirror.Stale,
			GitSha:        mirror.GitSha,
			BlobSha:       mirror.BlobSha,
			LastRefreshed: mirror.LastRefreshed,
		}
		sum.Mirrors = append(sum.Mirrors, presence)
		sum.CountMaterialized++
		sum.CountOrphan++
		if presence.Stale {
			sum.CountStale++
		}
	}
	return sum
}

func unionHosts(selected map[string]struct{}, holders map[string]mirrorTrait) []string {
	all := make(map[string]struct{}, len(selected)+len(holders))
	for h := range selected {
		all[h] = struct{}{}
	}
	for h := range holders {
		all[h] = struct{}{}
	}
	hosts := make([]string, 0, len(all))
	for h := range all {
		hosts = append(hosts, h)
	}
	sort.Strings(hosts)
	return hosts
}

// ParseLabel splits a cairn label of shape
// `cairn:<seeder>:<user>:<project>` into its segments.
// Malformed labels return empty strings.
func ParseLabel(label string) (seeder, user, project string) {
	const prefix = "cairn:"
	if len(label) <= len(prefix) || label[:len(prefix)] != prefix {
		return "", "", ""
	}
	rest := label[len(prefix):]
	a := -1
	b := -1
	for i := 0; i < len(rest); i++ {
		if rest[i] == ':' {
			if a == -1 {
				a = i
			} else if b == -1 {
				b = i
				break
			}
		}
	}
	if a == -1 || b == -1 {
		return "", "", ""
	}
	return rest[:a], rest[a+1 : b], rest[b+1:]
}
