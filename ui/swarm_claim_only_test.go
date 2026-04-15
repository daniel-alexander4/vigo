// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

package ui

import (
	"testing"

	"github.com/daniel-alexander4/vigo/server/swarm"
)

// fakeSwarmResolver is a minimal SwarmHostnameResolver for testing.
// ResolveHostname looks up a static id→hostname map; MatchingEnvoys
// is driven by the `matching` slice and is used only by the
// target-pattern code path (not the claim-only path).
type fakeSwarmResolver struct {
	idToHost map[string]string
	matching []string
}

func (f *fakeSwarmResolver) ResolveHostname(id string) string { return f.idToHost[id] }
func (f *fakeSwarmResolver) ResolveIP(ip string) string       { return "" }
func (f *fakeSwarmResolver) MatchingEnvoys(target string) []string {
	return append([]string(nil), f.matching...)
}

// fakeClaimLookup implements ClaimMirrorLookup with a static label→IDs map.
type fakeClaimLookup struct {
	mirrors map[string][]string
}

func (f *fakeClaimLookup) MirrorIDsForLabel(label string) []string {
	return append([]string(nil), f.mirrors[label]...)
}

// TestAddPendingEnvoys_VigorepoBlob_UsesClaimTableNotTarget is the
// regression test for the "raspberrypi still shows as pending in
// the swarm UI even though exclude_envoys drops it" symptom.
//
// For a claim-only label, the pending fan-out must come from the
// vigorepo ClaimTable's mirror list — not the wildcard target
// pattern. Envoys that the ClaimTable filtered out (raspberry* by
// exclude_envoys pattern, non-selected envoys past max_mirrors,
// etc.) must NOT appear in the UI.
func TestAddPendingEnvoys_VigorepoBlob_UsesClaimTableNotTarget(t *testing.T) {
	label := "vigorepo:danlap:dan:vigo-web"
	resolver := &fakeSwarmResolver{
		idToHost: map[string]string{
			"annlap-id":      "annlap",
			"plex-id":        "plex",
			"girlslaptop-id": "girlslaptop",
			"raspberrypi-id": "raspberrypi",
		},
		// If the code fell through to the target-pattern path, it
		// would include raspberrypi — the test would catch that.
		matching: []string{"annlap", "plex", "girlslaptop", "raspberrypi"},
	}
	claims := &fakeClaimLookup{
		mirrors: map[string][]string{
			label: {"annlap-id", "plex-id", "girlslaptop-id"},
		},
	}

	var statuses []swarm.EnvoySwarmStatus
	addPendingEnvoys(&statuses, label, "*", 1, resolver, claims)

	if len(statuses) != 3 {
		t.Fatalf("expected 3 pending entries (the 3 mirrors), got %d: %+v", len(statuses), statuses)
	}
	got := make(map[string]bool, len(statuses))
	for _, s := range statuses {
		got[s.Hostname] = true
		if s.Status != "pending" {
			t.Errorf("entry %q has status %q, want pending", s.Hostname, s.Status)
		}
	}
	for _, want := range []string{"annlap", "plex", "girlslaptop"} {
		if !got[want] {
			t.Errorf("expected %q in pending list, missing", want)
		}
	}
	if got["raspberrypi"] {
		t.Error("raspberrypi appeared in pending list — claim-only branch leaked to target fan-out")
	}
}

// TestAddPendingEnvoys_VigorepoBlob_NilClaimsIsSafe verifies that a
// vigorepo blob with a nil claims lookup is a clean no-op — no
// pending entries added, no panic, no fall-through to the target
// fan-out. Matches the documented nil-safe contract on the function.
func TestAddPendingEnvoys_VigorepoBlob_NilClaimsIsSafe(t *testing.T) {
	resolver := &fakeSwarmResolver{
		idToHost: map[string]string{"a-id": "a"},
		matching: []string{"a", "b", "c"},
	}
	var statuses []swarm.EnvoySwarmStatus
	addPendingEnvoys(&statuses, "vigorepo:x:y:z", "*", 1, resolver, nil)

	if len(statuses) != 0 {
		t.Errorf("expected no entries for claim-only label with nil ClaimTable, got %+v", statuses)
	}
}

// TestAddPendingEnvoys_VigorepoBlob_UnknownLabelNoFanout covers the
// case where a vigorepo label has no ClaimTable entry (shouldn't
// happen in practice — the flush hook always upserts — but the
// handler must NOT fall through to the target fan-out and list
// random envoys as pending).
func TestAddPendingEnvoys_VigorepoBlob_UnknownLabelNoFanout(t *testing.T) {
	resolver := &fakeSwarmResolver{
		idToHost: map[string]string{"a-id": "a"},
		matching: []string{"a", "b", "c"},
	}
	claims := &fakeClaimLookup{mirrors: map[string][]string{}}

	var statuses []swarm.EnvoySwarmStatus
	addPendingEnvoys(&statuses, "vigorepo:unknown:x:y", "*", 1, resolver, claims)

	if len(statuses) != 0 {
		t.Errorf("expected no fall-through for unknown vigorepo label, got %+v", statuses)
	}
}

// TestAddPendingEnvoys_StandardBlob_TargetFanoutUnchanged pins the
// pre-existing behavior for non-vigorepo labels: target pattern
// expansion via resolver.MatchingEnvoys. This is the full fleet
// behavior used by operator-distributed blobs like ISOs, and must
// not regress when claim-only branching is added for vigorepo.
func TestAddPendingEnvoys_StandardBlob_TargetFanoutUnchanged(t *testing.T) {
	resolver := &fakeSwarmResolver{
		idToHost: map[string]string{},
		matching: []string{"web-01", "web-02", "db-01"},
	}

	var statuses []swarm.EnvoySwarmStatus
	addPendingEnvoys(&statuses, "module-bundle", "*", 47, resolver, nil)

	if len(statuses) != 3 {
		t.Fatalf("expected 3 pending entries via target fan-out, got %d: %+v", len(statuses), statuses)
	}
	got := make(map[string]bool, len(statuses))
	for _, s := range statuses {
		got[s.Hostname] = true
	}
	for _, want := range []string{"web-01", "web-02", "db-01"} {
		if !got[want] {
			t.Errorf("expected %q in standard fan-out, missing", want)
		}
	}
}

// TestAddPendingEnvoys_StandardBlob_SkipsAlreadyReported verifies
// the dedup via `seen` still works after the refactor: envoys that
// already appear in the tracker's Status() result don't get
// duplicated by the pending fan-out.
func TestAddPendingEnvoys_StandardBlob_SkipsAlreadyReported(t *testing.T) {
	resolver := &fakeSwarmResolver{
		matching: []string{"web-01", "web-02"},
	}
	statuses := []swarm.EnvoySwarmStatus{
		{Hostname: "web-01", Status: "complete", ChunksHave: 47, ChunksNeed: 47},
	}
	addPendingEnvoys(&statuses, "module-bundle", "*", 47, resolver, nil)

	if len(statuses) != 2 {
		t.Fatalf("expected 2 entries total (complete + 1 pending), got %d", len(statuses))
	}
	var webOneStatus, webTwoStatus string
	for _, s := range statuses {
		switch s.Hostname {
		case "web-01":
			webOneStatus = s.Status
		case "web-02":
			webTwoStatus = s.Status
		}
	}
	if webOneStatus != "complete" {
		t.Errorf("web-01 status = %q, want complete (must not be overwritten by pending fan-out)", webOneStatus)
	}
	if webTwoStatus != "pending" {
		t.Errorf("web-02 status = %q, want pending", webTwoStatus)
	}
}
