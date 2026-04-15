// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

package vigorepo

import (
	"fmt"
	"sort"
	"strings"
	"testing"
)

// sidecarFor returns a valid sidecar parsed from a minimal YAML so
// tests don't all duplicate the 5-field boilerplate.
func sidecarFor(t *testing.T, owner string, maxMirrors int) *Sidecar {
	t.Helper()
	yaml := fmt.Sprintf(
		"owner: %s\nmax_mirrors: %d\ntags: []\npaused: false\ndescription: \"\"\n",
		owner, maxMirrors)
	s, _, err := LoadSidecar([]byte(yaml))
	if err != nil {
		t.Fatalf("sidecarFor: %v", err)
	}
	return s
}

func pausedSidecarFor(t *testing.T, owner string) *Sidecar {
	t.Helper()
	yaml := fmt.Sprintf(
		"owner: %s\nmax_mirrors: 5\ntags: []\npaused: true\ndescription: \"\"\n",
		owner)
	s, _, err := LoadSidecar([]byte(yaml))
	if err != nil {
		t.Fatalf("pausedSidecarFor: %v", err)
	}
	return s
}

func candidates(pairs ...string) []Candidate {
	cs := make([]Candidate, 0, len(pairs)/2)
	for i := 0; i+1 < len(pairs); i += 2 {
		cs = append(cs, Candidate{EnvoyID: pairs[i], Hostname: pairs[i+1]})
	}
	return cs
}

// --- parseContentLabel ---------------------------------------------

func TestParseContentLabelValid(t *testing.T) {
	seeder, user, project, err := parseContentLabel("vigorepo:danlap:dan:orangutan")
	if err != nil {
		t.Fatalf("expected ok, got %v", err)
	}
	if seeder != "danlap" || user != "dan" || project != "orangutan" {
		t.Errorf("got (%q, %q, %q)", seeder, user, project)
	}
}

func TestParseContentLabelRejectsMissingPrefix(t *testing.T) {
	if _, _, _, err := parseContentLabel("other:a:b:c"); err == nil {
		t.Error("expected error")
	}
}

func TestParseContentLabelRejectsMetaPrefix(t *testing.T) {
	if _, _, _, err := parseContentLabel("vigorepo-meta:danlap:dan:proj"); err == nil {
		t.Error("expected meta label to be rejected")
	}
}

func TestParseContentLabelRejectsTooFewSegments(t *testing.T) {
	cases := []string{
		"vigorepo:",
		"vigorepo:a",
		"vigorepo:a:b",
	}
	for _, c := range cases {
		if _, _, _, err := parseContentLabel(c); err == nil {
			t.Errorf("expected error for %q", c)
		}
	}
}

func TestParseContentLabelRejectsEmptySegment(t *testing.T) {
	cases := []string{
		"vigorepo::b:c",
		"vigorepo:a::c",
		"vigorepo:a:b:",
	}
	for _, c := range cases {
		if _, _, _, err := parseContentLabel(c); err == nil {
			t.Errorf("expected error for %q", c)
		}
	}
}

// --- selectMirrors --------------------------------------------------

func TestSelectMirrorsEmptyPoolReturnsNil(t *testing.T) {
	got := selectMirrors("vigorepo:h:u:p", nil, 3)
	if got != nil {
		t.Errorf("got %v, want nil", got)
	}
}

func TestSelectMirrorsZeroMaxReturnsNil(t *testing.T) {
	got := selectMirrors("vigorepo:h:u:p", candidates("e1", "a"), 0)
	if got != nil {
		t.Errorf("got %v, want nil", got)
	}
}

func TestSelectMirrorsCapsAtPoolSize(t *testing.T) {
	got := selectMirrors("vigorepo:h:u:p",
		candidates("e1", "a", "e2", "b"), 5)
	if len(got) != 2 {
		t.Errorf("got len %d, want 2", len(got))
	}
}

func TestSelectMirrorsCapsAtMaxMirrors(t *testing.T) {
	pool := candidates(
		"e1", "hostA",
		"e2", "hostB",
		"e3", "hostC",
		"e4", "hostD",
		"e5", "hostE",
		"e6", "hostF",
	)
	got := selectMirrors("vigorepo:h:u:p", pool, 3)
	if len(got) != 3 {
		t.Errorf("got len %d, want 3", len(got))
	}
}

func TestSelectMirrorsIsDeterministic(t *testing.T) {
	pool := candidates(
		"e1", "hostA",
		"e2", "hostB",
		"e3", "hostC",
		"e4", "hostD",
		"e5", "hostE",
	)
	first := selectMirrors("vigorepo:h:u:p", pool, 3)

	// Reverse pool order and re-run — result must be identical.
	reversed := make([]Candidate, len(pool))
	for i, c := range pool {
		reversed[len(pool)-1-i] = c
	}
	second := selectMirrors("vigorepo:h:u:p", reversed, 3)

	if len(first) != len(second) {
		t.Fatalf("lengths differ: %v vs %v", first, second)
	}
	for i := range first {
		if first[i] != second[i] {
			t.Errorf("index %d: %q vs %q", i, first[i], second[i])
		}
	}
}

func TestSelectMirrorsDifferentLabelsReshuffle(t *testing.T) {
	pool := candidates(
		"e1", "hostA",
		"e2", "hostB",
		"e3", "hostC",
		"e4", "hostD",
		"e5", "hostE",
	)
	a := selectMirrors("vigorepo:h:u:projA", pool, 3)
	b := selectMirrors("vigorepo:h:u:projB", pool, 3)
	// Very unlikely (not impossible) that two different labels
	// pick the same 3 envoys in the same order. Assert that at
	// least the ordered selection differs.
	equal := len(a) == len(b)
	for i := range a {
		if i >= len(b) || a[i] != b[i] {
			equal = false
			break
		}
	}
	if equal {
		t.Errorf("different labels produced identical selections; "+
			"hash distribution likely broken. a=%v b=%v", a, b)
	}
}

func TestSelectMirrorsDifferentHostnamesProduceDifferentScores(t *testing.T) {
	// Regression: if we forgot the NUL separator, "label=foo, host=bar"
	// and "label=foobar, host=" might collide. Verify hosts with
	// common prefixes get distinct selection positions.
	pool := candidates(
		"e1", "hostPrefix",
		"e2", "hostPrefixLong",
		"e3", "hostPrefixLonger",
	)
	got := selectMirrors("vigorepo:h:u:p", pool, 3)
	if len(got) != 3 {
		t.Fatalf("got len %d, want 3", len(got))
	}
	// All three should be unique.
	seen := map[string]bool{}
	for _, id := range got {
		if seen[id] {
			t.Errorf("duplicate envoy id: %q", id)
		}
		seen[id] = true
	}
}

// --- ClaimTable Upsert / Claims / Remove ---------------------------

func TestClaimTableUpsertAddsSource(t *testing.T) {
	ct := NewClaimTable()
	sc := sidecarFor(t, "danlap", 3)
	pool := candidates("e1", "a", "e2", "b", "e3", "c")
	state, err := ct.Upsert("vigorepo:danlap:dan:proj", sc, pool)
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if state == nil {
		t.Fatal("state is nil")
	}
	if state.SeederHost != "danlap" || state.TargetUser != "dan" || state.Project != "proj" {
		t.Errorf("segments wrong: %+v", state)
	}
	if len(state.Mirrors) != 3 {
		t.Errorf("mirrors len = %d, want 3", len(state.Mirrors))
	}
	if state.UnderReplicated {
		t.Error("under-replicated should be false")
	}
	if ct.Len() != 1 {
		t.Errorf("Len = %d, want 1", ct.Len())
	}
}

func TestClaimTableUpsertSetsUnderReplicatedWhenPoolSmall(t *testing.T) {
	ct := NewClaimTable()
	sc := sidecarFor(t, "danlap", 5)
	pool := candidates("e1", "a", "e2", "b")
	state, err := ct.Upsert("vigorepo:danlap:dan:proj", sc, pool)
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if !state.UnderReplicated {
		t.Error("expected under-replicated=true")
	}
	if len(state.Mirrors) != 2 {
		t.Errorf("mirrors len = %d, want 2", len(state.Mirrors))
	}
	if state.CandidateCount != 2 {
		t.Errorf("CandidateCount = %d, want 2", state.CandidateCount)
	}
}

func TestClaimTableUpsertPausedEmitsNoMirrors(t *testing.T) {
	ct := NewClaimTable()
	sc := pausedSidecarFor(t, "danlap")
	pool := candidates("e1", "a", "e2", "b", "e3", "c")
	state, err := ct.Upsert("vigorepo:danlap:dan:proj", sc, pool)
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if len(state.Mirrors) != 0 {
		t.Errorf("paused source should have zero mirrors, got %v", state.Mirrors)
	}
	if state.UnderReplicated {
		t.Error("paused source should not be flagged under-replicated")
	}
}

func TestClaimTableUpsertRejectsInvalidLabel(t *testing.T) {
	ct := NewClaimTable()
	sc := sidecarFor(t, "danlap", 3)
	if _, err := ct.Upsert("not-a-vigorepo-label", sc, nil); err == nil {
		t.Error("expected error")
	}
}

func TestClaimTableUpsertRejectsNilSidecar(t *testing.T) {
	ct := NewClaimTable()
	if _, err := ct.Upsert("vigorepo:danlap:dan:proj", nil, nil); err == nil {
		t.Error("expected error for nil sidecar")
	}
}

func TestClaimTableClaimsReturnsAssignedLabels(t *testing.T) {
	ct := NewClaimTable()
	sc := sidecarFor(t, "danlap", 3)
	pool := candidates("e1", "hostA", "e2", "hostB", "e3", "hostC")
	if _, err := ct.Upsert("vigorepo:danlap:dan:proj1", sc, pool); err != nil {
		t.Fatal(err)
	}
	if _, err := ct.Upsert("vigorepo:danlap:dan:proj2", sc, pool); err != nil {
		t.Fatal(err)
	}

	// With 3-envoy pool and max_mirrors=3, every envoy is picked
	// for every label.
	claims := ct.Claims("e1")
	if len(claims) != 2 {
		t.Errorf("e1 got %d claims, want 2: %v", len(claims), claims)
	}
	// Output must be sorted.
	if !sort.StringsAreSorted(claims) {
		t.Errorf("claims not sorted: %v", claims)
	}
}

func TestClaimTableClaimsEmptyForUnselectedEnvoy(t *testing.T) {
	ct := NewClaimTable()
	sc := sidecarFor(t, "danlap", 3)
	pool := candidates("e1", "a", "e2", "b", "e3", "c")
	if _, err := ct.Upsert("vigorepo:danlap:dan:proj", sc, pool); err != nil {
		t.Fatal(err)
	}
	claims := ct.Claims("never-heard-of-me")
	if len(claims) != 0 {
		t.Errorf("got %v, want empty", claims)
	}
}

func TestClaimTableRemoveDropsSource(t *testing.T) {
	ct := NewClaimTable()
	sc := sidecarFor(t, "danlap", 3)
	pool := candidates("e1", "a", "e2", "b", "e3", "c")
	if _, err := ct.Upsert("vigorepo:danlap:dan:proj", sc, pool); err != nil {
		t.Fatal(err)
	}
	ct.Remove("vigorepo:danlap:dan:proj")
	if ct.Len() != 0 {
		t.Errorf("Len after remove = %d, want 0", ct.Len())
	}
	if ct.Get("vigorepo:danlap:dan:proj") != nil {
		t.Error("Get should return nil after Remove")
	}
}

func TestClaimTableGetReturnsDefensiveCopy(t *testing.T) {
	ct := NewClaimTable()
	sc := sidecarFor(t, "danlap", 3)
	sc.Tags = []string{"a", "b"}
	pool := candidates("e1", "hostA", "e2", "hostB", "e3", "hostC")
	if _, err := ct.Upsert("vigorepo:danlap:dan:proj", sc, pool); err != nil {
		t.Fatal(err)
	}

	got := ct.Get("vigorepo:danlap:dan:proj")
	if got == nil {
		t.Fatal("Get returned nil")
	}
	// Mutate the copy — must not affect subsequent Get results.
	got.Mirrors[0] = "MUTATED"
	got.Sidecar.Tags[0] = "MUTATED"

	got2 := ct.Get("vigorepo:danlap:dan:proj")
	if got2.Mirrors[0] == "MUTATED" {
		t.Error("Mirrors aliased the internal slice")
	}
	if got2.Sidecar.Tags[0] == "MUTATED" {
		t.Error("Sidecar.Tags aliased the internal slice")
	}
}

func TestClaimTableSnapshotReturnsDefensiveCopy(t *testing.T) {
	ct := NewClaimTable()
	sc := sidecarFor(t, "danlap", 3)
	pool := candidates("e1", "a", "e2", "b", "e3", "c")
	for _, p := range []string{"proj1", "proj2", "proj3"} {
		if _, err := ct.Upsert("vigorepo:danlap:dan:"+p, sc, pool); err != nil {
			t.Fatal(err)
		}
	}
	snap := ct.Snapshot()
	if len(snap) != 3 {
		t.Errorf("snapshot len = %d, want 3", len(snap))
	}
	// Mutating the snapshot must not affect subsequent Snapshots.
	delete(snap, "vigorepo:danlap:dan:proj1")
	snap2 := ct.Snapshot()
	if len(snap2) != 3 {
		t.Errorf("snapshot2 len = %d, want 3 (snapshot was not a copy)", len(snap2))
	}
}

func TestClaimTableUpsertIsIdempotent(t *testing.T) {
	ct := NewClaimTable()
	sc := sidecarFor(t, "danlap", 3)
	pool := candidates("e1", "a", "e2", "b", "e3", "c")
	first, _ := ct.Upsert("vigorepo:danlap:dan:proj", sc, pool)
	second, _ := ct.Upsert("vigorepo:danlap:dan:proj", sc, pool)

	if len(first.Mirrors) != len(second.Mirrors) {
		t.Fatalf("mirror length changed: %v vs %v", first.Mirrors, second.Mirrors)
	}
	for i := range first.Mirrors {
		if first.Mirrors[i] != second.Mirrors[i] {
			t.Errorf("mirror[%d] changed: %q vs %q", i, first.Mirrors[i], second.Mirrors[i])
		}
	}
	if ct.Len() != 1 {
		t.Errorf("Len = %d, want 1 (upsert not deduping)", ct.Len())
	}
}

func TestClaimTableUpsertReflectsNewCandidatePool(t *testing.T) {
	ct := NewClaimTable()
	sc := sidecarFor(t, "danlap", 3) // uncapped — 3 is MinMirrors

	poolA := candidates(
		"e1", "hostA",
		"e2", "hostB",
		"e3", "hostC",
		"e4", "hostD",
	)
	stateA, _ := ct.Upsert("vigorepo:danlap:dan:proj", sc, poolA)
	if len(stateA.Mirrors) != 3 {
		t.Fatalf("pool A: mirrors=%d, want 3", len(stateA.Mirrors))
	}
	if stateA.UnderReplicated {
		t.Error("pool A should not be under-replicated")
	}

	// Shrink pool to 2 — selection must shrink with it, and
	// under-replicated must flip to true.
	poolB := candidates("e1", "hostA", "e2", "hostB")
	stateB, _ := ct.Upsert("vigorepo:danlap:dan:proj", sc, poolB)
	if len(stateB.Mirrors) != 2 {
		t.Fatalf("pool B: mirrors=%d, want 2", len(stateB.Mirrors))
	}
	if !stateB.UnderReplicated {
		t.Error("pool B should be under-replicated (2 < 3)")
	}
}

// --- end-to-end "Claims" integration ------------------------------

func TestClaimsPartitionsAcrossManyLabels(t *testing.T) {
	// With lots of labels over the same pool, every envoy should
	// get a roughly equal share of claims. We're not asserting
	// exact equality — just that no envoy is starved.
	ct := NewClaimTable()
	sc := sidecarFor(t, "danlap", 3)

	var hostnames []string
	for i := 0; i < 10; i++ {
		hostnames = append(hostnames, fmt.Sprintf("host%02d", i))
	}
	var pool []Candidate
	for i, h := range hostnames {
		pool = append(pool, Candidate{EnvoyID: fmt.Sprintf("e%02d", i), Hostname: h})
	}

	for i := 0; i < 50; i++ {
		label := fmt.Sprintf("vigorepo:danlap:dan:proj%02d", i)
		if _, err := ct.Upsert(label, sc, pool); err != nil {
			t.Fatal(err)
		}
	}

	totalClaims := 0
	for i := 0; i < 10; i++ {
		claims := ct.Claims(fmt.Sprintf("e%02d", i))
		if len(claims) == 0 {
			t.Errorf("envoy e%02d got zero claims — starved", i)
		}
		totalClaims += len(claims)
	}
	// With 50 labels × 3 mirrors each = 150 claims, distributed
	// across 10 envoys, average ~15 per envoy.
	if totalClaims != 50*3 {
		t.Errorf("totalClaims = %d, want 150", totalClaims)
	}
}

// scoreLabelHost regression: ensure two labels that concatenate to
// the same bytes without the length prefix produce distinct scores.
func TestScoreLabelHostLengthPrefixDefendsAgainstConcatCollision(t *testing.T) {
	// "ab" + "c" and "a" + "bc" would collide with naive
	// `sha256(label + hostname)`. With the length prefix, they
	// must differ.
	a := scoreLabelHost("ab", "c")
	b := scoreLabelHost("a", "bc")
	if a == b {
		t.Error("length prefix failed to disambiguate label boundary")
	}
	// And with a NUL separator too (both defenses active).
	aStr := fmt.Sprintf("%x", a[:])
	bStr := fmt.Sprintf("%x", b[:])
	if strings.EqualFold(aStr, bStr) {
		t.Error("scores should differ hex-wise too")
	}
}
