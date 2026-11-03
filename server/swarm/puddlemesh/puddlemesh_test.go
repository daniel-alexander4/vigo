// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: Apache-2.0

package puddlemesh

import "testing"

const (
	pkA = "aa00112233445566778899aabbccddeeff00112233445566778899aabbccdd00"
	pkB = "bb00112233445566778899aabbccddeeff00112233445566778899aabbccdd00"
	pkC = "cc00112233445566778899aabbccddeeff00112233445566778899aabbccdd00"
)

// nameClaim builds a puddleTrait carrying a name claim for the shape
// groupNameClaims consumes.
func named(user, pk, name string, claimedAt int64) puddleTrait {
	return puddleTrait{
		User:         user,
		PuddlePubkey: pk,
		Initialized:  true,
		NameClaim: &NameClaim{
			Body:      NameClaimBody{PuddlePubkey: pk, Name: name, ClaimedAt: claimedAt},
			Signature: "deadbeef",
		},
	}
}

func TestGroupNameClaims(t *testing.T) {
	envs := []*traitEnvelope{
		// host1: dan→pkA (old), team→pkB
		{Puddles: []puddleTrait{named("dan", pkA, "dan", 100), named("o", pkB, "team", 50)}},
		// host2: dan→pkA (newer revision — should win), dan→pkB (collision!)
		{Puddles: []puddleTrait{named("dan", pkA, "dan", 200), named("e", pkB, "dan", 70)}},
		// host3: junk entries dropped (empty name, empty pubkey)
		{Puddles: []puddleTrait{named("x", pkA, "", 1), named("y", "", "x", 1)}},
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
	if _, ok := got[""]; ok {
		t.Error("empty name should be dropped")
	}
	if _, ok := got["x"]; ok {
		t.Error("name with empty pubkey should be dropped")
	}
}

func TestUnambiguousNameByPubkey(t *testing.T) {
	grouped := map[string][]NameClaim{
		"team":  {{Body: NameClaimBody{PuddlePubkey: pkB, Name: "team", ClaimedAt: 50}}},
		"dan":   {{Body: NameClaimBody{PuddlePubkey: pkA, Name: "dan", ClaimedAt: 200}}, {Body: NameClaimBody{PuddlePubkey: pkC, Name: "dan", ClaimedAt: 70}}},
		"danny": {{Body: NameClaimBody{PuddlePubkey: pkA, Name: "danny", ClaimedAt: 300}}},
		"junk":  {{Body: NameClaimBody{PuddlePubkey: "", Name: "junk", ClaimedAt: 1}}},
	}
	got := unambiguousNameByPubkey(grouped)
	if got[pkB] != "team" {
		t.Errorf("pkB → %q, want 'team'", got[pkB])
	}
	if got[pkA] != "danny" {
		t.Errorf("pkA → %q, want 'danny' (its only uncontested name; the contested 'dan' is ignored)", got[pkA])
	}
	if _, ok := got[pkC]; ok {
		t.Error("pkC should not resolve — its only name 'dan' is contested")
	}
	if len(got) != 2 {
		t.Errorf("got %d entries, want 2: %+v", len(got), got)
	}
}

func TestContestedPubkeys(t *testing.T) {
	grouped := map[string][]NameClaim{
		"team": {{Body: NameClaimBody{PuddlePubkey: pkB, Name: "team"}}},
		"dan":  {{Body: NameClaimBody{PuddlePubkey: pkA, Name: "dan"}}, {Body: NameClaimBody{PuddlePubkey: pkC, Name: "dan"}}},
	}
	c := contestedPubkeys(grouped)
	if !c[pkA] || !c[pkC] {
		t.Errorf("pkA + pkC should be contested: %+v", c)
	}
	if c[pkB] {
		t.Error("pkB ('team', single claimant) must not be contested")
	}
}

// Health: a reporting, initialized, uncontested puddle is healthy.
func TestClassifyHealth_Healthy(t *testing.T) {
	entries := []entryFor{
		{hostname: "a", updated: 100, stale: false, p: puddleTrait{User: "dan", Initialized: true}},
	}
	s := summarizePuddle(pkA, entries, "dan", false)
	if s.Health.Band != HealthHealthy {
		t.Fatalf("band = %q (%v), want healthy", s.Health.Band, s.Health.Reasons)
	}
}

// Health: every holder stale → degraded (unreachable).
func TestClassifyHealth_AllStaleDegraded(t *testing.T) {
	entries := []entryFor{
		{hostname: "a", updated: 1, stale: true, p: puddleTrait{User: "dan", Initialized: true}},
		{hostname: "b", updated: 1, stale: true, p: puddleTrait{User: "dan", Initialized: true}},
	}
	s := summarizePuddle(pkA, entries, "dan", false)
	if s.Health.Band != HealthDegraded {
		t.Fatalf("band = %q, want degraded", s.Health.Band)
	}
}

// Health: contested name + a rekey in progress + a partial stale → warning,
// with all reasons surfaced.
func TestClassifyHealth_Warning(t *testing.T) {
	entries := []entryFor{
		{hostname: "a", updated: 100, stale: false, p: puddleTrait{User: "dan", Initialized: true, RekeyPending: true}},
		{hostname: "b", updated: 1, stale: true, p: puddleTrait{User: "dan", Initialized: true}},
	}
	s := summarizePuddle(pkA, entries, "", true) // contested → owner empty
	if s.Health.Band != HealthWarning {
		t.Fatalf("band = %q, want warning", s.Health.Band)
	}
	if len(s.Health.Reasons) != 3 {
		t.Errorf("want 3 reasons (contested + rekey + stale), got %v", s.Health.Reasons)
	}
	if !s.RekeyPending || !s.OwnerContested {
		t.Error("summary should carry RekeyPending + OwnerContested")
	}
}

// summarizePuddle collapses multiple holders + users deterministically.
func TestSummarizePuddle_MultiHolder(t *testing.T) {
	entries := []entryFor{
		{hostname: "plex", updated: 200, stale: false, p: puddleTrait{User: "dan", Initialized: true, SessionUnlocked: true, RetiredCount: 1}},
		{hostname: "danlap", updated: 150, stale: false, p: puddleTrait{User: "dan", Initialized: true}},
	}
	s := summarizePuddle(pkA, entries, "dan", false)
	if s.EnvoyCount != 2 {
		t.Fatalf("envoy count = %d, want 2", s.EnvoyCount)
	}
	if s.Envoys[0].Hostname != "danlap" {
		t.Errorf("envoys not sorted by hostname: %+v", s.Envoys)
	}
	if !s.SessionUnlocked {
		t.Error("session should be unlocked (one holder reports it)")
	}
	if s.RetiredCount != 1 {
		t.Errorf("retired count = %d, want 1 (max across holders)", s.RetiredCount)
	}
	if s.LastUpdated != 200 {
		t.Errorf("last updated = %d, want 200", s.LastUpdated)
	}
	if len(s.Users) != 1 || s.Users[0] != "dan" {
		t.Errorf("users = %v, want [dan]", s.Users)
	}
}
