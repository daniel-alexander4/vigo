// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: Apache-2.0

package ui

import (
	"context"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"git.alexander4.io/vigo/server/config"
	"git.alexander4.io/vigo/server/db"
	"git.alexander4.io/vigo/server/swarm/curatormesh"
)

// mockCurator implements [CuratorFleetSource] with injectable snapshots so
// the handler can be exercised without standing up the real aggregator + DB.
type mockCurator struct {
	cat     *curatormesh.Catalog
	blocked []db.CuratorBlockedArtifact
	err     error
}

func (m *mockCurator) FleetSnapshot(_ context.Context) (*curatormesh.Catalog, []db.CuratorBlockedArtifact, error) {
	return m.cat, m.blocked, m.err
}

// stubResolver is a minimal SwarmHostnameResolver for tests: callers set a
// canned `matches` map keyed by target glob and the stub returns the value
// for the matching key (treated as "envoys whose hostname matches this
// glob"). Resolver methods we don't exercise here return zero values.
type stubResolver struct {
	matches map[string][]string
}

func (s *stubResolver) ResolveHostname(_ string) string { return "" }
func (s *stubResolver) ResolveIP(_ string) string       { return "" }
func (s *stubResolver) MatchingEnvoys(target string) []string {
	return s.matches[target]
}

// pubkeyHex builds a deterministic 32-byte hex puddle pubkey from a seed.
func pubkeyHex(seed byte) string {
	var b [32]byte
	for i := range b {
		b[i] = seed
	}
	return hex.EncodeToString(b[:])
}

func sampleEntry(seed byte, name string, issuedMillis int64) curatormesh.CatalogEntry {
	return curatormesh.CatalogEntry{
		Body: curatormesh.CatalogEntryBody{
			SchemaVersion:       1,
			Name:                name,
			Kind:                "generic",
			FounderPuddlePubkey: pubkeyHex(seed),
			SigningPuddlePubkey: pubkeyHex(seed),
			Versions: map[string]curatormesh.ArtifactVersion{
				"1.0.0": {Platforms: []curatormesh.Platform{
					{OS: "linux", Arch: "amd64", BlobSha: strings.Repeat("a", 64), Size: 1024},
					{OS: "linux", Arch: "arm64", BlobSha: strings.Repeat("b", 64), Size: 1100},
				}},
				"2.0.0": {Platforms: []curatormesh.Platform{
					{OS: "linux", Arch: "amd64", BlobSha: strings.Repeat("c", 64), Size: 2048},
					{OS: "darwin", Arch: "arm64", BlobSha: strings.Repeat("d", 64), Size: 2200},
				}},
			},
			Tags:       map[string]string{"latest": "2.0.0", "stable": "1.0.0"},
			Recipients: []string{"*"},
			Target:     "*",
			IssuedAt:   issuedMillis,
		},
		Signature: "ff",
	}
}

// TestCurator_RBAC pins admin-only on both the list and detail pages.
// Viewers must never see fleet-wide artifact metadata + founder pubkeys.
func TestCurator_RBAC(t *testing.T) {
	h := newTestHandler(t)
	h.authEnabled = true
	h.curatorFleet = &mockCurator{cat: &curatormesh.Catalog{}}

	for _, path := range []string{"/swarm/curator", "/swarm/curator/" + strings.Repeat("a", 64)} {
		rec := httptest.NewRecorder()
		req := viewerRequest(http.MethodGet, path)
		if strings.Count(path, "/") > 2 {
			req.SetPathValue("artifact_id", path[len("/swarm/curator/"):])
		}
		h.Curator(rec, req)
		if rec.Code != http.StatusForbidden {
			t.Errorf("viewer GET %s = %d, want 403", path, rec.Code)
		}
	}
}

func TestCurator_ListEmpty(t *testing.T) {
	h := newTestHandler(t)
	h.curatorFleet = &mockCurator{cat: &curatormesh.Catalog{}}
	rec := httptest.NewRecorder()
	h.Curator(rec, adminRequest(http.MethodGet, "/swarm/curator"))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body: %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, "No curator artifacts observed") {
		t.Errorf("expected empty-state message, got: %s", body)
	}
}

func TestCurator_ListRendersArtifactsAndBlockedFooter(t *testing.T) {
	h := newTestHandler(t)
	live := sampleEntry(0x01, "vigo-agent", 100)
	liveID, _ := curatormesh.ArtifactID(live)
	phantom := strings.Repeat("9", 64)
	h.curatorFleet = &mockCurator{
		cat: &curatormesh.Catalog{
			GeneratedAt:  time.Unix(1714521600, 0),
			Artifacts:    []curatormesh.CatalogEntry{live},
			Participants: []string{"plex", "danlap"},
		},
		blocked: []db.CuratorBlockedArtifact{
			{ArtifactID: phantom, BlockedAt: time.Unix(1714521000, 0), BlockedBy: "dan", Reason: "supply-chain incident"},
		},
	}
	rec := httptest.NewRecorder()
	h.Curator(rec, adminRequest(http.MethodGet, "/swarm/curator"))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body: %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	// Live row.
	if !strings.Contains(body, "vigo-agent") {
		t.Errorf("expected live artifact row, got: %s", body)
	}
	if !strings.Contains(body, liveID[:12]) {
		t.Errorf("expected truncated live artifact_id in body")
	}
	// Orphan footer.
	if !strings.Contains(body, "Operator blocks without a current catalog entry") {
		t.Errorf("expected orphan-blocks footer, got: %s", body)
	}
	if !strings.Contains(body, phantom) {
		t.Errorf("expected phantom artifact_id in orphan footer")
	}
	if !strings.Contains(body, "supply-chain incident") {
		t.Errorf("expected reason in orphan footer")
	}
}

func TestCurator_ListMarksBlockedLiveArtifact(t *testing.T) {
	h := newTestHandler(t)
	live := sampleEntry(0x01, "blocked-thing", 1)
	liveID, _ := curatormesh.ArtifactID(live)
	h.curatorFleet = &mockCurator{
		cat:     &curatormesh.Catalog{Artifacts: []curatormesh.CatalogEntry{live}},
		blocked: []db.CuratorBlockedArtifact{{ArtifactID: liveID, BlockedBy: "dan"}},
	}
	rec := httptest.NewRecorder()
	h.Curator(rec, adminRequest(http.MethodGet, "/swarm/curator"))
	body := rec.Body.String()
	if !strings.Contains(body, ">blocked<") {
		t.Errorf("expected blocked badge on the live row, got: %s", body)
	}
	// A live blocked artifact must NOT appear in the orphan-blocks footer.
	if strings.Contains(body, "Operator blocks without a current catalog entry") {
		t.Errorf("a live blocked artifact should not be in the orphan footer")
	}
}

func TestCurator_DetailByHexID(t *testing.T) {
	h := newTestHandler(t)
	e := sampleEntry(0x01, "vigo-agent", 1714521000000)
	aid, _ := curatormesh.ArtifactID(e)
	h.curatorFleet = &mockCurator{cat: &curatormesh.Catalog{Artifacts: []curatormesh.CatalogEntry{e}}}

	rec := httptest.NewRecorder()
	req := adminRequest(http.MethodGet, "/swarm/curator/"+aid)
	req.SetPathValue("artifact_id", aid)
	h.Curator(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body: %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, "vigo-agent") {
		t.Errorf("expected name in detail body, got: %s", body)
	}
	if !strings.Contains(body, aid) {
		t.Errorf("expected full artifact_id in detail body")
	}
	// Versions table renders for both 1.0.0 and 2.0.0.
	if !strings.Contains(body, "1.0.0") || !strings.Contains(body, "2.0.0") {
		t.Errorf("expected both versions in body, got: %s", body)
	}
	// Tag badges.
	if !strings.Contains(body, "latest") || !strings.Contains(body, "stable") {
		t.Errorf("expected tag badges in body")
	}
}

func TestCurator_DetailByNameFallback(t *testing.T) {
	h := newTestHandler(t)
	e := sampleEntry(0x01, "thing", 1)
	h.curatorFleet = &mockCurator{cat: &curatormesh.Catalog{Artifacts: []curatormesh.CatalogEntry{e}}}

	rec := httptest.NewRecorder()
	req := adminRequest(http.MethodGet, "/swarm/curator/thing")
	req.SetPathValue("artifact_id", "thing")
	h.Curator(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body: %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "thing") {
		t.Errorf("expected name-fallback to resolve")
	}
}

func TestCurator_DetailNotFound(t *testing.T) {
	h := newTestHandler(t)
	h.curatorFleet = &mockCurator{cat: &curatormesh.Catalog{}}
	rec := httptest.NewRecorder()
	bogus := strings.Repeat("f", 64)
	req := adminRequest(http.MethodGet, "/swarm/curator/"+bogus)
	req.SetPathValue("artifact_id", bogus)
	h.Curator(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", rec.Code)
	}
}

func TestCurator_DetailFlagsRekey(t *testing.T) {
	h := newTestHandler(t)
	e := sampleEntry(0x01, "thing", 1)
	e.Body.SigningPuddlePubkey = pubkeyHex(0x02) // rekeyed: signing ≠ founder
	aid, _ := curatormesh.ArtifactID(e)
	h.curatorFleet = &mockCurator{cat: &curatormesh.Catalog{Artifacts: []curatormesh.CatalogEntry{e}}}

	rec := httptest.NewRecorder()
	req := adminRequest(http.MethodGet, "/swarm/curator/"+aid)
	req.SetPathValue("artifact_id", aid)
	h.Curator(rec, req)
	body := rec.Body.String()
	if !strings.Contains(body, "rekeyed") {
		t.Errorf("expected rekey badge when signing != founder, got: %s", body)
	}
	if !strings.Contains(body, pubkeyHex(0x02)) {
		t.Errorf("expected the new signing pubkey in body")
	}
}

func TestCurator_DetailFlagsDanglingTags(t *testing.T) {
	h := newTestHandler(t)
	e := sampleEntry(0x01, "thing", 1)
	e.Body.Tags["canary"] = "9.9.9" // dangling
	aid, _ := curatormesh.ArtifactID(e)
	h.curatorFleet = &mockCurator{cat: &curatormesh.Catalog{Artifacts: []curatormesh.CatalogEntry{e}}}

	rec := httptest.NewRecorder()
	req := adminRequest(http.MethodGet, "/swarm/curator/"+aid)
	req.SetPathValue("artifact_id", aid)
	h.Curator(rec, req)
	body := rec.Body.String()
	if !strings.Contains(body, "Dangling tags") {
		t.Errorf("expected dangling-tags section, got: %s", body)
	}
	if !strings.Contains(body, "canary") || !strings.Contains(body, "9.9.9") {
		t.Errorf("expected dangling tag pair in body")
	}
}

func TestCurator_AmbiguousNameRefuses(t *testing.T) {
	h := newTestHandler(t)
	e1 := sampleEntry(0x01, "redis", 1)
	e2 := sampleEntry(0x02, "redis", 1)
	h.curatorFleet = &mockCurator{cat: &curatormesh.Catalog{Artifacts: []curatormesh.CatalogEntry{e1, e2}}}

	rec := httptest.NewRecorder()
	req := adminRequest(http.MethodGet, "/swarm/curator/redis")
	req.SetPathValue("artifact_id", "redis")
	h.Curator(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Errorf("ambiguous name lookup should 404 (operator passes the hex); got %d", rec.Code)
	}
}

// TestBuildCuratorHealth pins the headline + problem flagging logic.
func TestBuildCuratorHealth(t *testing.T) {
	live := sampleEntry(0x01, "vigo-agent", 1)
	liveID, _ := curatormesh.ArtifactID(live)
	cat := &curatormesh.Catalog{
		Artifacts:    []curatormesh.CatalogEntry{live},
		Participants: []string{"a", "b"},
	}
	// Clean.
	hb := buildCuratorHealth("", cat, nil)
	if hb.Level != "ok" {
		t.Errorf("clean catalog should be ok, got level %s", hb.Level)
	}
	if !strings.Contains(hb.Headline, "1 artifact") {
		t.Errorf("expected artifact count in headline, got: %s", hb.Headline)
	}
	// One live block + one orphan.
	hb = buildCuratorHealth("", cat, []db.CuratorBlockedArtifact{
		{ArtifactID: liveID, Reason: "supply-chain"},
		{ArtifactID: strings.Repeat("9", 64), Reason: "stale"},
	})
	if hb.Level != "warn" {
		t.Errorf("blocked artifacts should warn, got level %s", hb.Level)
	}
	// Live blocked row has a link to the detail page.
	gotLink := false
	for _, p := range hb.Problems {
		if p.Kind == "blocked" && strings.Contains(p.Link, liveID) {
			gotLink = true
		}
	}
	if !gotLink {
		t.Errorf("expected blocked problem with detail-page link, got: %+v", hb.Problems)
	}
	// Orphan rollup line is single (not per-orphan).
	orphanProblems := 0
	for _, p := range hb.Problems {
		if p.Kind == "blocked-orphan" {
			orphanProblems++
		}
	}
	if orphanProblems != 1 {
		t.Errorf("expected exactly one blocked-orphan rollup, got %d", orphanProblems)
	}
}

// --- resolveCuratorTargetEnvoys ---

func TestResolveCuratorTargetEnvoys_IntersectsTargetWithEnabledList(t *testing.T) {
	h := newTestHandler(t)
	h.swarmFleetIndex = &stubResolver{
		matches: map[string][]string{
			"*": {"plex", "danlap", "girlslaptop", "annlap", "raspberrypi"},
		},
	}
	h.swarmConfig = config.SwarmSection{
		Curator: config.CuratorSection{Enabled: []string{"plex", "danlap", "girlslaptop"}},
	}

	got := h.resolveCuratorTargetEnvoys("*")
	want := []string{"plex", "danlap", "girlslaptop"}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v (intersection of `*` ∩ curator.enabled)", got, want)
	}
	for i, g := range got {
		if g != want[i] {
			t.Errorf("position %d: got %q, want %q", i, g, want[i])
		}
	}
}

func TestResolveCuratorTargetEnvoys_HonorsDenyPattern(t *testing.T) {
	// `swarm.curator.enabled = ["-rasp*", "*"]` denies rasp-prefixed hosts
	// even though they match the artifact's `*` target.
	h := newTestHandler(t)
	h.swarmFleetIndex = &stubResolver{
		matches: map[string][]string{
			"*": {"plex", "raspberrypi", "danlap"},
		},
	}
	h.swarmConfig = config.SwarmSection{
		Curator: config.CuratorSection{Enabled: []string{"-rasp*", "*"}},
	}

	got := h.resolveCuratorTargetEnvoys("*")
	want := []string{"plex", "danlap"} // raspberrypi denied
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}

func TestResolveCuratorTargetEnvoys_NarrowTargetGlobIntersects(t *testing.T) {
	// Artifact published with `target: "*lap"` (only laptops). The
	// fleet-index resolver returns those that match; the curator-enabled
	// pattern list is `*` (every host eligible). Result: just the laptops.
	h := newTestHandler(t)
	h.swarmFleetIndex = &stubResolver{
		matches: map[string][]string{
			"*lap": {"danlap", "girlslaptop"},
		},
	}
	h.swarmConfig = config.SwarmSection{
		Curator: config.CuratorSection{Enabled: []string{"*"}},
	}
	got := h.resolveCuratorTargetEnvoys("*lap")
	if len(got) != 2 || got[0] != "danlap" || got[1] != "girlslaptop" {
		t.Errorf("got %v, want [danlap girlslaptop]", got)
	}
}

func TestResolveCuratorTargetEnvoys_EmptyEnabledListReturnsEmpty(t *testing.T) {
	// curator.enabled == [] means curator is disabled fleet-wide. Even if
	// the target glob matches every envoy, the intersection is empty —
	// the data is KNOWN (return empty slice), not unknown (would be nil).
	h := newTestHandler(t)
	h.swarmFleetIndex = &stubResolver{
		matches: map[string][]string{"*": {"plex", "danlap"}},
	}
	h.swarmConfig = config.SwarmSection{Curator: config.CuratorSection{Enabled: nil}}

	got := h.resolveCuratorTargetEnvoys("*")
	if got == nil {
		t.Errorf("empty enabled list must return empty slice (known: zero matches), not nil (unknown)")
	}
	if len(got) != 0 {
		t.Errorf("got %v, want empty", got)
	}
}

func TestResolveCuratorTargetEnvoys_NilFleetIndexReturnsNil(t *testing.T) {
	// Pre-wiring: handler doesn't have a fleet resolver yet. Returning nil
	// signals "unknown" so the template can render "(resolver not wired)"
	// rather than incorrectly claiming "no envoys match".
	h := newTestHandler(t)
	h.swarmFleetIndex = nil
	got := h.resolveCuratorTargetEnvoys("*")
	if got != nil {
		t.Errorf("nil swarmFleetIndex must yield nil (unknown), got %v", got)
	}
}

// --- end-to-end: detail page renders the resolved list ---

func TestCurator_DetailPageShowsResolvedTargetEnvoys(t *testing.T) {
	h := newTestHandler(t)
	h.swarmFleetIndex = &stubResolver{
		matches: map[string][]string{
			"*": {"plex", "danlap", "girlslaptop", "raspberrypi"},
		},
	}
	h.swarmConfig = config.SwarmSection{
		Curator: config.CuratorSection{Enabled: []string{"plex", "danlap", "girlslaptop"}},
	}

	e := sampleEntry(0x01, "vigo-agent", 1714521000000)
	aid, _ := curatormesh.ArtifactID(e)
	h.curatorFleet = &mockCurator{cat: &curatormesh.Catalog{Artifacts: []curatormesh.CatalogEntry{e}}}

	rec := httptest.NewRecorder()
	req := adminRequest(http.MethodGet, "/swarm/curator/"+aid)
	req.SetPathValue("artifact_id", aid)
	h.Curator(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body: %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	for _, want := range []string{"plex", "danlap", "girlslaptop"} {
		if !strings.Contains(body, ">"+want+"<") {
			t.Errorf("expected %q in resolved-envoys list, body: %s", want, body)
		}
	}
	if strings.Contains(body, ">raspberrypi<") {
		t.Errorf("raspberrypi is not curator-enabled and must not appear")
	}
	if !strings.Contains(body, "3 envoys") {
		t.Errorf("expected envoy count summary in body")
	}
}
