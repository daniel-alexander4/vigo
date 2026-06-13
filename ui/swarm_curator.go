// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

package ui

import (
	"fmt"
	"net/http"
	"sort"
	"strings"

	"git.alexander4.io/vigo/server/config"
	"git.alexander4.io/vigo/server/db"
	"git.alexander4.io/vigo/server/swarm/curatormesh"
)

// CuratorPageData drives `ui/templates/pages/curator.html`. Carries both
// the list view and the per-artifact detail view; the template picks
// which to render based on whether Artifact is non-nil. The Blocked
// table is only consulted on the list page (it's a footer); per-
// artifact blocked status comes from the catalog snapshot's
// per-entry pairing with the table.
type CuratorPageData struct {
	PageData
	Health   HealthBanner
	Catalog  *curatormesh.Catalog
	Blocked  []db.CuratorBlockedArtifact
	Artifact *curatorArtifactView // non-nil for the detail page
	Error    string
}

// curatorArtifactView is the assembled per-entry shape the detail
// template renders. Pulled out so all the cross-cuts the template
// would otherwise do (reverse tag map, dangling tag detection,
// platform aggregation, signing-vs-founder hint) happen once in Go.
type curatorArtifactView struct {
	ArtifactID          string
	Name                string
	Kind                string
	FounderPuddlePubkey string
	SigningPuddlePubkey string
	Target              string
	// TargetEnvoys is the resolved hostname list: envoys whose hostname
	// matches the entry's `target` glob AND `swarm.curator.enabled`. The
	// raw glob (often `*`) is kept in [Target] to preserve operator intent;
	// this expansion shows the actual envoys that hold the P2P replica —
	// what `*` resolves to on this fleet, not what it could resolve to on
	// any fleet. Empty when no live envoys match (publish-only glob, or
	// curator.enabled = []).
	TargetEnvoys []string
	Recipients   []string
	IssuedAt     int64
	Versions     []curatorVersionRow
	DanglingTags []curatorTagBinding
	Rekeyed      bool
	Blocked      bool
}

type curatorVersionRow struct {
	Version   string
	Tags      []string
	Platforms []curatorPlatformRow
}

type curatorPlatformRow struct {
	OS      string
	Arch    string
	BlobSha string
	Size    uint64
}

type curatorTagBinding struct {
	Tag     string
	Version string
}

// Curator serves `GET /swarm/curator` (list) and
// `GET /swarm/curator/{artifact_id}` (detail). Admin-only — the
// fleet artifact catalog exposes founder puddle pubkeys and target
// globs that aren't viewer-appropriate. (Reading individual artifacts
// is ungated through `vigo swarm curator pull`; this is the
// fleet-aggregator view.)
//
// `{artifact_id}` accepts the canonical 64-char-lowercase-hex form OR
// a unique artifact-name prefix (UX convenience — the cryptographic
// identity remains the artifact_id).
func (h *Handler) Curator(w http.ResponseWriter, r *http.Request) {
	if !h.adminOnly(w, r) {
		return
	}
	id := r.PathValue("artifact_id")
	if id != "" {
		h.curatorDetail(w, r, id)
		return
	}
	h.curatorList(w, r)
}

func (h *Handler) curatorList(w http.ResponseWriter, r *http.Request) {
	data := CuratorPageData{
		PageData: PageData{Title: "Curator", ActiveNav: "curator"},
	}
	h.setCommonWithRequest(&data.PageData, r)

	if h.curatorFleet == nil {
		data.Error = "curator aggregator not available"
		data.Health = finalizeHealthBanner("Curator", nil, "Aggregator unavailable")
		data.Health.Level = "warn"
		data.Health.Locale = data.Locale
		h.renderer.Render(w, "curator", data)
		return
	}

	cat, blocked, err := h.curatorFleet.FleetSnapshot(r.Context())
	if err != nil {
		data.Error = err.Error()
		data.Health = finalizeHealthBanner("Curator", nil, "Snapshot failed")
		data.Health.Level = "warn"
		data.Health.Locale = data.Locale
		h.renderer.Render(w, "curator", data)
		return
	}
	data.Catalog = cat
	data.Blocked = blocked
	data.Health = buildCuratorHealth(cat, blocked)
	data.Health.Locale = data.Locale
	h.renderer.Render(w, "curator", data)
}

func (h *Handler) curatorDetail(w http.ResponseWriter, r *http.Request, id string) {
	data := CuratorPageData{
		PageData: PageData{Title: "Curator", ActiveNav: "curator"},
	}
	h.setCommonWithRequest(&data.PageData, r)

	if h.curatorFleet == nil {
		http.Error(w, "curator aggregator unavailable", http.StatusServiceUnavailable)
		return
	}
	cat, blocked, err := h.curatorFleet.FleetSnapshot(r.Context())
	if err != nil {
		http.Error(w, "snapshot failed", http.StatusInternalServerError)
		return
	}
	// Hex id wins; name lookup is a fallback so an operator who typed a
	// name into the URL bar doesn't 404.
	entry := cat.LookupByID(id)
	matchedID := strings.ToLower(id)
	if entry == nil {
		entry, matchedID = lookupCuratorByName(cat, id)
	}
	if entry == nil {
		http.NotFound(w, r)
		return
	}
	blockedSet := false
	for _, b := range blocked {
		if strings.EqualFold(b.ArtifactID, matchedID) {
			blockedSet = true
			break
		}
	}
	targetEnvoys := h.resolveCuratorTargetEnvoys(entry.Body.Target)
	view := buildCuratorArtifactView(matchedID, entry, blockedSet, targetEnvoys)
	data.Artifact = view
	data.Health = buildCuratorArtifactHealth(view)
	data.Health.Locale = data.Locale
	h.renderer.Render(w, "curator", data)
}

// resolveCuratorTargetEnvoys intersects an artifact's target glob with
// `swarm.curator.enabled`. The catalog entry's `target` says "which envoys
// would be eligible to hold the replica if curator were on everywhere";
// `swarm.curator.enabled` (server.yaml) says "which envoys actually run
// curator at all". The envoys that hold the P2P DR replica are the
// intersection — so the UI shows the resolved set, not just the literal
// glob (which is often `*` and misleading on a partially-enabled fleet).
//
// Returns `nil` (not an empty slice) when the swarm-config or fleet-index
// dependencies aren't wired — the template treats nil and empty the same
// way ("no envoys match"), but nil lets a future caller distinguish
// "unknown — UI handler can't compute it" from "known empty".
func (h *Handler) resolveCuratorTargetEnvoys(target string) []string {
	if h.swarmFleetIndex == nil {
		return nil
	}
	matchedByTarget := h.swarmFleetIndex.MatchingEnvoys(target)
	if len(matchedByTarget) == 0 {
		return matchedByTarget
	}
	patterns := h.swarmConfig.Curator.Enabled
	if len(patterns) == 0 {
		// Empty enabled list = curator disabled fleet-wide. Returning an
		// empty slice (not nil) — the data IS known: zero envoys hold the
		// replica because nobody is curator-enabled.
		return []string{}
	}
	out := make([]string, 0, len(matchedByTarget))
	for _, host := range matchedByTarget {
		if config.HostMatchesPatterns(host, patterns) {
			out = append(out, host)
		}
	}
	return out
}

// lookupCuratorByName tries an exact name match, then a unique
// name-prefix match across the fleet catalog. Returns the entry and
// the resolved artifact_id (hex) on success; nil + empty otherwise.
// Name collisions (two founders publishing the same name) refuse —
// pass the hex artifact_id to disambiguate.
func lookupCuratorByName(cat *curatormesh.Catalog, target string) (*curatormesh.CatalogEntry, string) {
	if cat == nil || len(cat.Artifacts) == 0 {
		return nil, ""
	}
	var exact []*curatormesh.CatalogEntry
	for i := range cat.Artifacts {
		if cat.Artifacts[i].Body.Name == target {
			exact = append(exact, &cat.Artifacts[i])
		}
	}
	if len(exact) == 1 {
		aid, _ := curatormesh.ArtifactID(*exact[0])
		return exact[0], aid
	}
	if len(exact) > 1 {
		// Ambiguous; refuse the name lookup. The operator can paste
		// the full hex id from the list page.
		return nil, ""
	}
	var prefix []*curatormesh.CatalogEntry
	for i := range cat.Artifacts {
		if strings.HasPrefix(cat.Artifacts[i].Body.Name, target) {
			prefix = append(prefix, &cat.Artifacts[i])
		}
	}
	if len(prefix) == 1 {
		aid, _ := curatormesh.ArtifactID(*prefix[0])
		return prefix[0], aid
	}
	return nil, ""
}

// buildCuratorArtifactView assembles the per-entry render shape: an
// ordered version list, each with its tag bindings and sorted platforms.
// `targetEnvoys` is the resolved hostname list — see
// [Handler.resolveCuratorTargetEnvoys] — passed in by the handler so the
// view builder stays free of fleet/config dependencies (also keeps tests
// hermetic).
func buildCuratorArtifactView(artifactID string, e *curatormesh.CatalogEntry, blocked bool, targetEnvoys []string) *curatorArtifactView {
	tagsByVersion := make(map[string][]string, len(e.Body.Tags))
	for tag, v := range e.Body.Tags {
		tagsByVersion[v] = append(tagsByVersion[v], tag)
	}
	for _, ts := range tagsByVersion {
		sort.Strings(ts)
	}

	verKeys := make([]string, 0, len(e.Body.Versions))
	for v := range e.Body.Versions {
		verKeys = append(verKeys, v)
	}
	sort.Strings(verKeys)

	versions := make([]curatorVersionRow, 0, len(verKeys))
	for _, v := range verKeys {
		av := e.Body.Versions[v]
		plats := make([]curatorPlatformRow, 0, len(av.Platforms))
		for _, p := range av.Platforms {
			plats = append(plats, curatorPlatformRow{
				OS: p.OS, Arch: p.Arch, BlobSha: p.BlobSha, Size: p.Size,
			})
		}
		sort.Slice(plats, func(i, j int) bool {
			if plats[i].OS != plats[j].OS {
				return plats[i].OS < plats[j].OS
			}
			return plats[i].Arch < plats[j].Arch
		})
		versions = append(versions, curatorVersionRow{
			Version: v, Tags: tagsByVersion[v], Platforms: plats,
		})
	}

	// Dangling tags — point at a version that doesn't exist.
	var dangling []curatorTagBinding
	for tag, v := range e.Body.Tags {
		if _, ok := e.Body.Versions[v]; !ok {
			dangling = append(dangling, curatorTagBinding{Tag: tag, Version: v})
		}
	}
	sort.Slice(dangling, func(i, j int) bool { return dangling[i].Tag < dangling[j].Tag })

	return &curatorArtifactView{
		ArtifactID:          artifactID,
		Name:                e.Body.Name,
		Kind:                e.Body.Kind,
		FounderPuddlePubkey: e.Body.FounderPuddlePubkey,
		SigningPuddlePubkey: e.Body.SigningPuddlePubkey,
		Target:              e.Body.Target,
		TargetEnvoys:        targetEnvoys,
		Recipients:          e.Body.Recipients,
		IssuedAt:            e.Body.IssuedAt,
		Versions:            versions,
		DanglingTags:        dangling,
		Rekeyed:             !strings.EqualFold(e.Body.SigningPuddlePubkey, e.Body.FounderPuddlePubkey),
		Blocked:             blocked,
	}
}

// buildCuratorHealth flags blocked artifacts and orphan blocks (block
// rows whose entry the publisher has since `rm`'d). Dangling tags are
// per-artifact warnings, surfaced on the detail page rather than the
// fleet rollup — a fleet-wide tally would just be noise.
func buildCuratorHealth(cat *curatormesh.Catalog, blocked []db.CuratorBlockedArtifact) HealthBanner {
	if cat == nil {
		return finalizeHealthBanner("Curator", nil, "No data")
	}
	var problems []HealthProblem
	blockedSet := make(map[string]db.CuratorBlockedArtifact, len(blocked))
	for _, b := range blocked {
		blockedSet[strings.ToLower(b.ArtifactID)] = b
	}
	// Live blocks (catalog entry still present + blocked) get a
	// per-artifact warning row so the operator can drill in. Orphan
	// blocks (no current entry) get a single rollup row — there's no
	// detail page to link to.
	var orphans []db.CuratorBlockedArtifact
	for _, b := range blocked {
		live := false
		for i := range cat.Artifacts {
			aid, err := curatormesh.ArtifactID(cat.Artifacts[i])
			if err == nil && strings.EqualFold(aid, b.ArtifactID) {
				live = true
				problems = append(problems, HealthProblem{
					Kind:     "blocked",
					Subject:  cat.Artifacts[i].Body.Name,
					Detail:   blockReasonText(b),
					Link:     "/swarm/curator/" + strings.ToLower(b.ArtifactID),
					Severity: "warn",
				})
				break
			}
		}
		if !live {
			orphans = append(orphans, b)
		}
	}
	if n := len(orphans); n > 0 {
		problems = append(problems, HealthProblem{
			Kind:     "blocked-orphan",
			Subject:  fmt.Sprintf("%d artifact_id(s)", n),
			Detail:   "operator-blocked but no current catalog entry — the publisher rm'd it",
			Severity: "warn",
		})
	}

	headline := fmt.Sprintf(
		"%d artifact(s) across %d participating envoy(s); %d operator-block(s) in effect.",
		len(cat.Artifacts),
		len(cat.Participants),
		len(blocked),
	)
	return finalizeHealthBanner("Curator", problems, headline)
}

func buildCuratorArtifactHealth(v *curatorArtifactView) HealthBanner {
	var problems []HealthProblem
	if v.Blocked {
		problems = append(problems, HealthProblem{
			Kind:     "blocked",
			Subject:  v.Name,
			Detail:   "operator-blocked — the server refuses to resolve or relay this artifact",
			Severity: "warn",
		})
	}
	if len(v.DanglingTags) > 0 {
		var s []string
		for _, b := range v.DanglingTags {
			s = append(s, fmt.Sprintf("%s → %s", b.Tag, b.Version))
		}
		problems = append(problems, HealthProblem{
			Kind:     "dangling-tag",
			Subject:  v.Name,
			Detail:   fmt.Sprintf("%d tag(s) point at a non-existent version: %s", len(v.DanglingTags), strings.Join(s, ", ")),
			Severity: "warn",
		})
	}
	platCount := 0
	for _, ver := range v.Versions {
		platCount += len(ver.Platforms)
	}
	headline := fmt.Sprintf("%s: %d version(s), %d platform build(s).",
		v.Name, len(v.Versions), platCount)
	return finalizeHealthBanner(v.Name, problems, headline)
}

func blockReasonText(b db.CuratorBlockedArtifact) string {
	if b.Reason != "" {
		return b.Reason
	}
	return "operator-blocked"
}
