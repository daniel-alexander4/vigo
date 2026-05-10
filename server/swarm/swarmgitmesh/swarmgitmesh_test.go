// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

package swarmgitmesh

import (
	"reflect"
	"sort"
	"testing"
)

func TestSummarizeProjectAllAgree(t *testing.T) {
	pid := "00112233445566778899aabbccddeeff00112233445566778899aabbccddeeff"
	founder := "f00dface" + "00000000000000000000000000000000000000000000000000000000"
	entries := []projectTrait{
		{
			ProjectID:          pid,
			Handle:             "vigo-web",
			FounderPuddlePubkey: founder,
			Members:            []string{founder},
			DrScope:            "swarm-wide",
			HasBareRepo:        true,
			BareRepoSizeBytes:  4096,
			HeadRefs:           map[string]string{"refs/heads/main": "abc123"},
			Envelopes:          envelopeCountsTrait{Revokes: 0, Leaves: 0, DigestHex: "00"},
			LeafRole:           "Admin",
			TokenChainDepth:    1,
		},
		{
			ProjectID:          pid,
			Handle:             "vigo-web",
			FounderPuddlePubkey: founder,
			Members:            []string{founder},
			DrScope:            "swarm-wide",
			HasBareRepo:        true,
			BareRepoSizeBytes:  4096,
			HeadRefs:           map[string]string{"refs/heads/main": "abc123"},
			Envelopes:          envelopeCountsTrait{Revokes: 0, Leaves: 0, DigestHex: "00"},
			LeafRole:           "Admin",
			TokenChainDepth:    1,
		},
	}
	hosts := []string{"plex", "danlap"}
	updated := []int64{100, 200}

	got := summarizeProject(pid, entries, hosts, updated)
	if got.ProjectID != pid {
		t.Errorf("project_id = %q, want %q", got.ProjectID, pid)
	}
	if got.Handle != "vigo-web" {
		t.Errorf("handle = %q", got.Handle)
	}
	if got.HostCount != 2 {
		t.Errorf("host_count = %d, want 2", got.HostCount)
	}
	if got.LastUpdated != 200 {
		t.Errorf("last_updated = %d, want 200", got.LastUpdated)
	}
	if !got.DigestUnanimous {
		t.Error("digest_unanimous should be true when all hosts have the same digest")
	}
	if len(got.DivergentRefs) != 0 {
		t.Errorf("expected zero divergent refs, got %v", got.DivergentRefs)
	}
	rs, ok := got.HeadRefs["refs/heads/main"]
	if !ok {
		t.Fatal("missing main ref status")
	}
	if rs.Sha != "abc123" {
		t.Errorf("ref sha = %q, want abc123", rs.Sha)
	}
	if !sliceContainsAll(rs.HostsAgreeing, []string{"plex", "danlap"}) {
		t.Errorf("agreeing hosts = %v", rs.HostsAgreeing)
	}
}

func TestSummarizeProjectDivergent(t *testing.T) {
	pid := "11" + "00112233445566778899aabbccddeeff00112233445566778899aabbccddee"
	entries := []projectTrait{
		{
			ProjectID:   pid,
			Handle:      "team-notes",
			HasBareRepo: true,
			HeadRefs:    map[string]string{"refs/heads/main": "old"},
			Envelopes:   envelopeCountsTrait{DigestHex: "aa"},
		},
		{
			ProjectID:   pid,
			Handle:      "team-notes",
			HasBareRepo: true,
			HeadRefs:    map[string]string{"refs/heads/main": "new"},
			Envelopes:   envelopeCountsTrait{DigestHex: "aa"},
		},
		{
			ProjectID:   pid,
			Handle:      "team-notes",
			HasBareRepo: true,
			HeadRefs:    map[string]string{"refs/heads/main": "new"},
			Envelopes:   envelopeCountsTrait{DigestHex: "aa"},
		},
	}
	hosts := []string{"old-host", "fresh-1", "fresh-2"}
	updated := []int64{100, 200, 250}

	got := summarizeProject(pid, entries, hosts, updated)
	if len(got.DivergentRefs) != 1 || got.DivergentRefs[0] != "refs/heads/main" {
		t.Errorf("divergent_refs = %v, want [refs/heads/main]", got.DivergentRefs)
	}
	rs := got.HeadRefs["refs/heads/main"]
	if rs.Sha != "" {
		t.Errorf("divergent ref Sha must be empty, got %q", rs.Sha)
	}
	if !sliceContainsAll(rs.HostsAgreeing, []string{"fresh-1", "fresh-2"}) {
		t.Errorf("majority agreeing = %v", rs.HostsAgreeing)
	}
	if len(rs.HostsLagging) != 1 || rs.HostsLagging[0] != "old-host" {
		t.Errorf("lagging = %v, want [old-host]", rs.HostsLagging)
	}
}

func TestSummarizeProjectDigestUnanimousFlagsMismatch(t *testing.T) {
	pid := "22" + "00112233445566778899aabbccddeeff00112233445566778899aabbccddee"
	entries := []projectTrait{
		{ProjectID: pid, Envelopes: envelopeCountsTrait{DigestHex: "aaaa"}},
		{ProjectID: pid, Envelopes: envelopeCountsTrait{DigestHex: "bbbb"}},
	}
	got := summarizeProject(pid, entries, []string{"a", "b"}, []int64{1, 2})
	if got.DigestUnanimous {
		t.Error("digest_unanimous should be false when hosts disagree")
	}
}

func TestSummarizeProjectFreshestHostWinsCanonicalFields(t *testing.T) {
	pid := "55" + "00112233445566778899aabbccddeeff00112233445566778899aabbccddee"
	// Older host claims one set of members; fresher host claims another.
	// The fresher one should win.
	entries := []projectTrait{
		{ProjectID: pid, Handle: "old-name", Members: []string{"old"}, DrScope: "members-only"},
		{ProjectID: pid, Handle: "new-name", Members: []string{"new"}, DrScope: "swarm-wide"},
	}
	got := summarizeProject(pid, entries, []string{"a", "b"}, []int64{100, 200})
	if got.Handle != "new-name" {
		t.Errorf("handle = %q, want new-name (freshest host wins)", got.Handle)
	}
	if got.DrScope != "swarm-wide" {
		t.Errorf("dr_scope = %q", got.DrScope)
	}
	if !sliceContainsAll(got.Members, []string{"new"}) {
		t.Errorf("members = %v", got.Members)
	}
}

func TestLookupHelpers(t *testing.T) {
	pid := "33" + "00112233445566778899aabbccddeeff00112233445566778899aabbccddee"
	f := &Fleet{
		Projects: []ProjectSummary{
			{ProjectID: pid, Handle: "alpha"},
			{ProjectID: "44ff", Handle: "beta"},
		},
	}
	if got := f.LookupProject(pid); got == nil || got.Handle != "alpha" {
		t.Errorf("LookupProject(%q) = %v", pid, got)
	}
	if got := f.LookupProject("nonexistent"); got != nil {
		t.Errorf("LookupProject('nonexistent') = %v, want nil", got)
	}
	if got := f.LookupByHandle("beta"); got == nil || got.ProjectID != "44ff" {
		t.Errorf("LookupByHandle('beta') = %v", got)
	}
	if got := f.LookupByHandle(""); got != nil {
		t.Error("LookupByHandle('') should be nil")
	}
}

func TestNilFleetSafe(t *testing.T) {
	var f *Fleet
	if f.LookupProject("anything") != nil {
		t.Error("nil receiver should return nil from LookupProject")
	}
	if f.LookupByHandle("x") != nil {
		t.Error("nil receiver should return nil from LookupByHandle")
	}
}

func sliceContainsAll(have, want []string) bool {
	a := append([]string(nil), have...)
	b := append([]string(nil), want...)
	sort.Strings(a)
	sort.Strings(b)
	return reflect.DeepEqual(a, b)
}
