// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// Build a 64-hex pubkey from a single-byte seed (deterministic; lets tests
// assert exact artifact_ids without dragging in ed25519_dalek).
func pubkeyFrom(b byte) string {
	out := make([]byte, 32)
	for i := range out {
		out[i] = b
	}
	return hex.EncodeToString(out)
}

func entryFor(founderByte byte, name string) curatorCatalogEntry {
	pk := pubkeyFrom(founderByte)
	return curatorCatalogEntry{
		Body: curatorCatalogEntryBody{
			SchemaVersion:       1,
			Name:                name,
			FounderPuddlePubkey: pk,
			SigningPuddlePubkey: pk,
			Versions: map[string]curatorArtifactVersion{
				"1.0.0": {
					Platforms: []curatorPlatform{
						{OS: "linux", Arch: "amd64", BlobSha: strings.Repeat("a", 64), Size: 1234},
					},
				},
				"2.0.0": {
					Platforms: []curatorPlatform{
						{OS: "linux", Arch: "amd64", BlobSha: strings.Repeat("b", 64), Size: 5678},
						{OS: "darwin", Arch: "arm64", BlobSha: strings.Repeat("c", 64), Size: 9012},
					},
				},
			},
			Tags:       map[string]string{"latest": "2.0.0", "stable": "1.0.0"},
			Recipients: []string{"*"},
			Target:     "*",
			IssuedAt:   time.Now().UnixMilli(),
		},
		Signature: strings.Repeat("ff", 64),
	}
}

func expectedID(founderByte byte, name string) string {
	pk := make([]byte, 32)
	for i := range pk {
		pk[i] = founderByte
	}
	h := sha256.New()
	h.Write(pk)
	h.Write([]byte(name))
	return hex.EncodeToString(h.Sum(nil))
}

func TestComputeArtifactIDMatchesServer(t *testing.T) {
	got, err := computeArtifactID(pubkeyFrom(0xab), "vigo-agent")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := expectedID(0xab, "vigo-agent")
	if got != want {
		t.Fatalf("artifact_id mismatch:\n  got  %s\n  want %s", got, want)
	}
}

func TestComputeArtifactIDRejectsShortPubkey(t *testing.T) {
	if _, err := computeArtifactID("abcd", "x"); err == nil {
		t.Fatal("expected error for short pubkey")
	}
	if _, err := computeArtifactID("not-hex", "x"); err == nil {
		t.Fatal("expected error for non-hex pubkey")
	}
}

func TestIsHex64CLI(t *testing.T) {
	cases := map[string]bool{
		strings.Repeat("a", 64):       true,
		strings.Repeat("A", 64):       true,
		strings.Repeat("0", 63) + "g": false, // 64 chars but non-hex char at the end
		strings.Repeat("0", 63):       false, // too short
		strings.Repeat("0", 65):       false, // too long
		"":                            false,
	}
	for in, want := range cases {
		if got := isHex64CLI(in); got != want {
			t.Errorf("isHex64CLI(%q) = %v, want %v", in, got, want)
		}
	}
}

func TestResolveCuratorTargetInExactName(t *testing.T) {
	list := &curatorListResponse{
		Catalog: &curatorCatalog{
			Artifacts: []curatorCatalogEntry{
				entryFor(0x01, "vigo-agent"),
				entryFor(0x02, "redis"),
			},
		},
	}
	got, err := resolveCuratorTargetIn(list, "redis")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := expectedID(0x02, "redis")
	if got != want {
		t.Errorf("got %s, want %s", got, want)
	}
}

func TestResolveCuratorTargetInAmbiguousName(t *testing.T) {
	// Two different founders, same artifact name → must be ambiguous.
	list := &curatorListResponse{
		Catalog: &curatorCatalog{
			Artifacts: []curatorCatalogEntry{
				entryFor(0x01, "redis"),
				entryFor(0x02, "redis"),
			},
		},
	}
	_, err := resolveCuratorTargetIn(list, "redis")
	if err == nil {
		t.Fatal("expected ambiguity error")
	}
	if !strings.Contains(err.Error(), "different founders") {
		t.Errorf("expected 'different founders' message, got: %v", err)
	}
}

func TestResolveCuratorTargetInUniquePrefix(t *testing.T) {
	list := &curatorListResponse{
		Catalog: &curatorCatalog{
			Artifacts: []curatorCatalogEntry{
				entryFor(0x01, "vigo-agent"),
				entryFor(0x02, "redis"),
			},
		},
	}
	got, err := resolveCuratorTargetIn(list, "redi")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != expectedID(0x02, "redis") {
		t.Errorf("prefix didn't resolve to redis; got %s", got)
	}
}

func TestResolveCuratorTargetInNoMatch(t *testing.T) {
	list := &curatorListResponse{
		Catalog: &curatorCatalog{Artifacts: []curatorCatalogEntry{entryFor(0x01, "x")}},
	}
	if _, err := resolveCuratorTargetIn(list, "nope"); err == nil {
		t.Fatal("expected no-match error")
	}
}

func TestResolveCuratorTargetInEmptyList(t *testing.T) {
	list := &curatorListResponse{Catalog: &curatorCatalog{}}
	if _, err := resolveCuratorTargetIn(list, "anything"); err == nil {
		t.Fatal("expected error for empty fleet + non-hex target")
	}
}

func TestRenderCuratorListEmpty(t *testing.T) {
	var buf bytes.Buffer
	if err := renderCuratorList(&curatorListResponse{Catalog: &curatorCatalog{}}, &buf); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(buf.String(), "No curator artifacts") {
		t.Errorf("expected empty-fleet message, got: %s", buf.String())
	}
}

func TestRenderCuratorListShowsBlockedOrphans(t *testing.T) {
	// Catalog with one entry; blocked has both that entry's id AND a phantom
	// (a sha for an artifact the publisher already rm'd). Operator must see
	// the phantom in the footer.
	live := entryFor(0x01, "vigo-agent")
	liveID, _ := curatorArtifactID(live)
	phantom := strings.Repeat("9", 64)
	list := &curatorListResponse{
		Catalog: &curatorCatalog{Artifacts: []curatorCatalogEntry{live}},
		Blocked: []curatorBlockedArtifact{
			{ArtifactID: liveID, BlockedBy: "dan"},
			{ArtifactID: phantom, BlockedBy: "dan", Reason: "stale"},
		},
	}
	var buf bytes.Buffer
	if err := renderCuratorList(list, &buf); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	out := buf.String()
	if !strings.Contains(out, "vigo-agent") {
		t.Errorf("expected live row, got: %s", out)
	}
	if !strings.Contains(out, "block(s) without a current catalog entry") {
		t.Errorf("expected orphan footer, got: %s", out)
	}
	if !strings.Contains(out, shortSha(phantom)) {
		t.Errorf("expected phantom artifact_id in orphan list, got: %s", out)
	}
	// Phantom is the *blocked* row but isn't a live row, so it shouldn't
	// appear in the main table.
	if strings.Count(out, shortSha(phantom)) != 1 {
		t.Errorf("phantom artifact should appear only once (in the orphan footer), got: %s", out)
	}
}

func TestRenderCuratorInspectFlagsRekey(t *testing.T) {
	e := entryFor(0x01, "thing")
	e.Body.SigningPuddlePubkey = pubkeyFrom(0x02) // different from founder
	resp := &curatorInspectResponse{
		ArtifactID: expectedID(0x01, "thing"),
		Entry:      &e,
	}
	var buf bytes.Buffer
	if err := renderCuratorInspect(resp, &buf); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(buf.String(), "rekeyed") {
		t.Errorf("expected rekey hint when signing != founder, got: %s", buf.String())
	}
}

func TestRenderCuratorInspectFlagsDanglingTags(t *testing.T) {
	e := entryFor(0x01, "x")
	e.Body.Tags["nightly"] = "9.9.9" // version doesn't exist
	resp := &curatorInspectResponse{
		ArtifactID: expectedID(0x01, "x"),
		Entry:      &e,
	}
	var buf bytes.Buffer
	if err := renderCuratorInspect(resp, &buf); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(buf.String(), "dangling tag") {
		t.Errorf("expected dangling-tag footer, got: %s", buf.String())
	}
	if !strings.Contains(buf.String(), "nightly → 9.9.9") {
		t.Errorf("expected dangling tag detail, got: %s", buf.String())
	}
}

func TestRenderCuratorInspectBlockedNoEntry(t *testing.T) {
	resp := &curatorInspectResponse{
		ArtifactID: strings.Repeat("a", 64),
		Entry:      nil,
		Blocked:    true,
	}
	var buf bytes.Buffer
	if err := renderCuratorInspect(resp, &buf); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(buf.String(), "blocked") {
		t.Errorf("expected blocked status, got: %s", buf.String())
	}
}

// --- API plumbing tests (the only "live wire" path slice 4a opens) ---

func curatorFixture(t *testing.T, handler http.HandlerFunc) func() {
	t.Helper()
	ts := httptest.NewServer(handler)
	prev := serverURL
	serverURL = ts.URL
	return func() {
		ts.Close()
		serverURL = prev
	}
}

func TestFetchCuratorListHitsCanonicalEndpoint(t *testing.T) {
	cleanup := curatorFixture(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/swarm/curator/list" {
			t.Errorf("expected /api/v1/swarm/curator/list, got %s", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(curatorListResponse{
			Catalog: &curatorCatalog{
				Artifacts:    []curatorCatalogEntry{entryFor(0x01, "vigo-agent")},
				Participants: []string{"envoy-a"},
			},
		})
	})
	defer cleanup()
	got, err := fetchCuratorList()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.Catalog == nil || len(got.Catalog.Artifacts) != 1 {
		t.Fatalf("unexpected payload: %+v", got)
	}
	if got.Catalog.Artifacts[0].Body.Name != "vigo-agent" {
		t.Errorf("got %q, want vigo-agent", got.Catalog.Artifacts[0].Body.Name)
	}
}

func TestFetchCuratorFleetHitsCanonicalEndpoint(t *testing.T) {
	cleanup := curatorFixture(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/swarm/curator/fleet" {
			t.Errorf("expected /api/v1/swarm/curator/fleet, got %s", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(curatorFleetResponse{
			Participants:  []string{"a", "b"},
			ArtifactCount: 3,
		})
	})
	defer cleanup()
	got, err := fetchCuratorFleet()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.ArtifactCount != 3 || len(got.Participants) != 2 {
		t.Errorf("unexpected payload: %+v", got)
	}
}

func TestFetchCuratorInspectURLEncodesArtifactID(t *testing.T) {
	aid := expectedID(0x01, "vigo-agent")
	cleanup := curatorFixture(t, func(w http.ResponseWriter, r *http.Request) {
		want := "/api/v1/swarm/curator/inspect/" + aid
		if r.URL.Path != want {
			t.Errorf("expected %s, got %s", want, r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		e := entryFor(0x01, "vigo-agent")
		_ = json.NewEncoder(w).Encode(curatorInspectResponse{
			ArtifactID: aid,
			Entry:      &e,
		})
	})
	defer cleanup()
	got, err := fetchCuratorInspect(aid)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.ArtifactID != aid {
		t.Errorf("got %s, want %s", got.ArtifactID, aid)
	}
}
