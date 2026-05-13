// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

package gitbackmesh

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
			ProjectID:           pid,
			Handle:              "vigo-web",
			FounderPuddlePubkey: founder,
			Members:             []string{founder},
			DrScope:             "swarm-wide",
			HasBareRepo:         true,
			BareRepoSizeBytes:   4096,
			HeadRefs:            map[string]string{"refs/heads/main": "abc123"},
			Envelopes:           envelopeCountsTrait{Revokes: 0, Leaves: 0, DigestHex: "00"},
			LeafRole:            "Admin",
			TokenChainDepth:     1,
		},
		{
			ProjectID:           pid,
			Handle:              "vigo-web",
			FounderPuddlePubkey: founder,
			Members:             []string{founder},
			DrScope:             "swarm-wide",
			HasBareRepo:         true,
			BareRepoSizeBytes:   4096,
			HeadRefs:            map[string]string{"refs/heads/main": "abc123"},
			Envelopes:           envelopeCountsTrait{Revokes: 0, Leaves: 0, DigestHex: "00"},
			LeafRole:            "Admin",
			TokenChainDepth:     1,
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

func TestGroupNameClaims(t *testing.T) {
	mk := func(name, pk string, claimedAt int64) TraitNameClaim {
		return TraitNameClaim{
			User: "dan",
			Claim: NameClaim{
				Body:      NameClaimBody{PuddlePubkey: pk, Name: name, ClaimedAt: claimedAt},
				Signature: "deadbeef",
			},
		}
	}
	pkA := "aa" + "00112233445566778899aabbccddeeff00112233445566778899aabbccdd"
	pkB := "bb" + "00112233445566778899aabbccddeeff00112233445566778899aabbccdd"

	envs := []*traitEnvelope{
		// host1: dan→pkA (old), team→pkB
		{NameClaims: []TraitNameClaim{mk("dan", pkA, 100), mk("team", pkB, 50)}},
		// host2: dan→pkA (newer revision — should win), dan→pkB (collision!)
		{NameClaims: []TraitNameClaim{mk("dan", pkA, 200), mk("dan", pkB, 70)}},
		// host3: junk entries dropped
		{NameClaims: []TraitNameClaim{mk("", pkA, 1), mk("x", "", 1)}},
	}
	got := groupNameClaims(envs)

	// "dan" has two distinct claimants → collision, both present, sorted by pubkey.
	dan := got["dan"]
	if len(dan) != 2 {
		t.Fatalf("expected 2 claimants for 'dan', got %d: %+v", len(dan), dan)
	}
	if dan[0].Body.PuddlePubkey != pkA || dan[1].Body.PuddlePubkey != pkB {
		t.Errorf("'dan' claimants not sorted by pubkey: %s, %s", dan[0].Body.PuddlePubkey, dan[1].Body.PuddlePubkey)
	}
	// The pkA claim should be the freshest revision (claimed_at 200).
	if dan[0].Body.ClaimedAt != 200 {
		t.Errorf("'dan'→pkA claimed_at = %d, want 200 (freshest revision wins)", dan[0].Body.ClaimedAt)
	}
	// "team" is unambiguous.
	if len(got["team"]) != 1 || got["team"][0].Body.PuddlePubkey != pkB {
		t.Errorf("'team' = %+v", got["team"])
	}
	// Junk names dropped.
	if _, ok := got[""]; ok {
		t.Error("empty name should be dropped")
	}
	if _, ok := got["x"]; ok {
		t.Error("name with empty pubkey should be dropped")
	}
}

func TestUnambiguousNameByPubkey(t *testing.T) {
	pkA := "aa" + "00112233445566778899aabbccddeeff00112233445566778899aabbccdd"
	pkB := "bb" + "00112233445566778899aabbccddeeff00112233445566778899aabbccdd"
	pkC := "cc" + "00112233445566778899aabbccddeeff00112233445566778899aabbccdd"
	claim := func(name, pk string, at int64) NameClaim {
		return NameClaim{Body: NameClaimBody{PuddlePubkey: pk, Name: name, ClaimedAt: at}}
	}
	grouped := map[string][]NameClaim{
		// unambiguous: one claimant
		"team": {claim("team", pkB, 50)},
		// contested: two claimants → dropped
		"dan": {claim("dan", pkA, 200), claim("dan", pkC, 70)},
		// pkA also (unambiguously) claims "danny" later → wins for pkA
		"danny": {claim("danny", pkA, 300)},
		// empty pubkey → dropped
		"junk": {claim("junk", "", 1)},
	}
	got := unambiguousNameByPubkey(grouped)
	if got[pkB] != "team" {
		t.Errorf("pkB → %q, want 'team'", got[pkB])
	}
	if got[pkA] != "danny" {
		t.Errorf("pkA → %q, want 'danny' (its only uncontested name; the contested 'dan' is ignored)", got[pkA])
	}
	if _, ok := got[pkC]; ok {
		t.Errorf("pkC should not resolve — its only name 'dan' is contested")
	}
	if len(got) != 2 {
		t.Errorf("got %d entries, want 2: %+v", len(got), got)
	}
}

func sliceContainsAll(have, want []string) bool {
	a := append([]string(nil), have...)
	b := append([]string(nil), want...)
	sort.Strings(a)
	sort.Strings(b)
	return reflect.DeepEqual(a, b)
}
