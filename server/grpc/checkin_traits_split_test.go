// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: AGPL-3.0-only

package grpc

import (
	"encoding/json"
	"testing"

	"git.alexander4.io/vigo/server/fleet"
)

// TestMergeTraitsObjects covers the byte-level join of the disjoint inventory
// and vitals objects (ADR-033) — empties, one-sided, and the concat case — and
// asserts the result is always valid JSON carrying both key sets.
func TestMergeTraitsObjects(t *testing.T) {
	cases := []struct{ inv, vit, want string }{
		{`{}`, `{}`, `{}`},
		{``, ``, `{}`},
		{`{"a":1}`, `{}`, `{"a":1}`},
		{`{}`, `{"b":2}`, `{"b":2}`},
		{``, `{"b":2}`, `{"b":2}`},
		{`{"a":1}`, `{"b":2}`, `{"a":1,"b":2}`},
		{`{"a":{"x":1}}`, `{"b":[1,2]}`, `{"a":{"x":1},"b":[1,2]}`},
	}
	for _, c := range cases {
		got := string(mergeTraitsObjects([]byte(c.inv), []byte(c.vit)))
		if got != c.want {
			t.Errorf("merge(%q,%q) = %q, want %q", c.inv, c.vit, got, c.want)
		}
		var m map[string]any
		if err := json.Unmarshal([]byte(got), &m); err != nil {
			t.Errorf("merge(%q,%q) produced invalid JSON %q: %v", c.inv, c.vit, got, err)
		}
	}
}

// TestEnsureAndMergeTraits exercises the split cache path (ADR-033 Option C): the
// cheap ensureInventory readiness gate (validate+cache on change, hash-compare on
// vitals-only) and the lazy mergeCachedTraits that rebuilds inventory ∪ vitals
// only when the persist key moved. A hash mismatch / unknown envoy is "not ready"
// (the caller then requests a resend); merging with no cache yields nil so Mark
// drops it.
func TestEnsureAndMergeTraits(t *testing.T) {
	s := &AgentService{}
	inv := []byte(`{"packages":["a"]}`)
	vit := []byte(`{"loadavg":1}`)

	// Inventory present → ready + cached; merge yields valid JSON carrying both.
	if !s.ensureInventory("e1", inv, "h1") {
		t.Fatal("inventory-present check-in should be ready (no resend)")
	}
	if full := s.mergeCachedTraits("e1", vit); !json.Valid(full) {
		t.Fatalf("merged traits not valid JSON: %s", full)
	}

	// Vitals-only, matching hash → ready; merge uses cached inventory + fresh vitals.
	if !s.ensureInventory("e1", nil, "h1") {
		t.Fatal("cache hit should be ready")
	}
	var m map[string]any
	if err := json.Unmarshal(s.mergeCachedTraits("e1", []byte(`{"loadavg":2}`)), &m); err != nil {
		t.Fatalf("merge: %v", err)
	}
	if _, ok := m["packages"]; !ok {
		t.Error("cached inventory missing from merge")
	}
	if v, _ := m["loadavg"].(float64); v != 2 {
		t.Errorf("vitals not fresh in merge: loadavg=%v", m["loadavg"])
	}

	// Hash mismatch → not ready (resend). Unknown envoy → not ready.
	if s.ensureInventory("e1", nil, "h2") {
		t.Error("hash mismatch should not be ready")
	}
	if s.ensureInventory("e2", nil, "h1") {
		t.Error("cache miss should not be ready")
	}
	// Merge with no cached inventory → nil, so Mark drops the empty blob.
	if s.mergeCachedTraits("e2", vit) != nil {
		t.Error("merge with no cached inventory should be nil")
	}
}

// TestTraitsChangeKey covers the ADR-033 Option C key: a split agent's key is
// inventory_hash + vitals_state_hash, so churny vitals (same state hash) collapse
// to one key while a changed state or inventory hash moves it; an empty state
// hash (mid rolling-upgrade) falls back to hashing the whole vitals blob; a legacy
// agent hashes traits_json.
func TestTraitsChangeKey(t *testing.T) {
	vitsA := []byte(`{"loadavg":1}`)
	vitsB := []byte(`{"loadavg":99}`) // churny-different vitals, same meaningful content

	k1 := traitsChangeKey("vsh1", vitsA, nil, "inv1")
	if got := traitsChangeKey("vsh1", vitsB, nil, "inv1"); got != k1 {
		t.Errorf("same vitals_state_hash should give same key: %q vs %q", k1, got)
	}
	if traitsChangeKey("vsh2", vitsA, nil, "inv1") == k1 {
		t.Error("changed vitals_state_hash should move the key")
	}
	if traitsChangeKey("vsh1", vitsA, nil, "inv2") == k1 {
		t.Error("changed inventory_hash should move the key")
	}

	// Empty vitals_state_hash → fall back to hashing the vitals blob (churny
	// vitals now DO move the key → persist every tick until the agent upgrades).
	if traitsChangeKey("", vitsA, nil, "inv1") == traitsChangeKey("", vitsB, nil, "inv1") {
		t.Error("empty vitals_state_hash should fall back to hashing the vitals blob")
	}

	// Legacy agent (no vitals): hashes traits_json.
	if traitsChangeKey("", nil, []byte(`{"a":1}`), "") == traitsChangeKey("", nil, []byte(`{"a":2}`), "") {
		t.Error("legacy key should change with traits_json")
	}
}

// TestExtractLiveVitals confirms the per-tick liveVitals overlay flattens only
// the operational-health collectors (agent_self/host_self/mountpoints/battery/
// uptime) and excludes churny non-health vitals like loadavg.
func TestExtractLiveVitals(t *testing.T) {
	volTop := map[string]json.RawMessage{
		"agent_self":  json.RawMessage(`{"open_fds":300,"rss_bytes":12345}`),
		"host_self":   json.RawMessage(`{"oom_recent":1}`),
		"mountpoints": json.RawMessage(`{"mounts":{"/":{"used_pct":81.5}}}`),
		"loadavg":     json.RawMessage(`{"load1":9.9}`), // not a health collector
	}
	lv := extractLiveVitals(volTop)
	for k, want := range map[string]string{
		"agent_self.open_fds":           "300",
		"host_self.oom_recent":          "1",
		"mountpoints.mounts./.used_pct": "81.5",
	} {
		if lv[k] != want {
			t.Errorf("liveVitals[%q] = %q, want %q", k, lv[k], want)
		}
	}
	if _, ok := lv["loadavg.load1"]; ok {
		t.Error("loadavg is not a health collector — must be excluded from liveVitals")
	}
	if extractLiveVitals(map[string]json.RawMessage{"loadavg": json.RawMessage(`{"load1":1}`)}) != nil {
		t.Error("no health collectors present → nil (SetLiveVitals no-op)")
	}
}

// TestDeriveOSArch covers the once-per-inventory-change os/arch derivation +
// validity gate: valid inventory with an os sub-tree yields goos/goarch; valid
// inventory without os (or with a non-object os, like fakefleet's "os":"linux")
// is still ok with empty platform; malformed JSON reports not-ok.
func TestDeriveOSArch(t *testing.T) {
	cases := []struct {
		name               string
		inv                string
		wantGoos, wantArch string
		wantOK             bool
	}{
		{"os_present", `{"os":{"family":"debian","arch":"amd64"},"packages":["a"]}`, "linux", "amd64", true},
		{"os_absent", `{"packages":["a"]}`, "", "", true},
		{"os_scalar", `{"os":"linux","packages":["a"]}`, "", "", true}, // fakefleet shape
		{"malformed", `{"packages":[`, "", "", false},
		{"not_object", `["a","b"]`, "", "", false}, // top-level non-object can't shallow-parse into a map
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			goos, arch, ok := deriveOSArch([]byte(c.inv))
			if goos != c.wantGoos || arch != c.wantArch || ok != c.wantOK {
				t.Errorf("deriveOSArch(%q) = (%q,%q,%v), want (%q,%q,%v)",
					c.inv, goos, arch, ok, c.wantGoos, c.wantArch, c.wantOK)
			}
		})
	}
}

// TestEnsureInventoryCachesOSArch verifies the steady-state optimization: the
// agent's goos/goarch is derived ONCE when the inventory is cached and then
// served from cachedOSArch on vitals-only ticks, so the ~150 KB inventory is
// never re-parsed per check-in for the auto-update lookup.
func TestEnsureInventoryCachesOSArch(t *testing.T) {
	s := &AgentService{}
	inv := []byte(`{"os":{"family":"debian","arch":"amd64"},"packages":["a"]}`)

	if !s.ensureInventory("e1", inv, "h1") {
		t.Fatal("inventory-present check-in should be ready")
	}
	if goos, arch := s.cachedOSArch("e1"); goos != "linux" || arch != "amd64" {
		t.Fatalf("cachedOSArch after store = (%q,%q), want (linux,amd64)", goos, arch)
	}
	// A vitals-only tick (cache hit) must keep the derived platform available
	// without re-parsing the inventory.
	if !s.ensureInventory("e1", nil, "h1") {
		t.Fatal("cache hit should be ready")
	}
	if goos, arch := s.cachedOSArch("e1"); goos != "linux" || arch != "amd64" {
		t.Errorf("cachedOSArch after vitals-only tick = (%q,%q), want (linux,amd64)", goos, arch)
	}
	// Unknown envoy → empty platform (legacy/no-cache fallback territory).
	if goos, arch := s.cachedOSArch("nope"); goos != "" || arch != "" {
		t.Errorf("cachedOSArch(unknown) = (%q,%q), want empty", goos, arch)
	}
}

// TestEnsureInventoryMalformedResends verifies that a malformed inventory is not
// cached and reports not-ready (the caller then requests a resend), so the
// steady-state path never serves or stores invalid traits — validity is gated
// here, once, on inventory change.
func TestEnsureInventoryMalformedResends(t *testing.T) {
	s := &AgentService{}
	if s.ensureInventory("e1", []byte(`{"packages":[`), "h1") {
		t.Error("malformed inventory should not be ready (resend)")
	}
	if _, ok := s.inventoryCache.Load("e1"); ok {
		t.Error("malformed inventory must not be cached")
	}
}

// TestInventoryEvictedOnEnvoyRemoval verifies the lifecycle wiring (ADR-033 #1):
// the inventory cache is tied to fleet membership via FleetIndex.SetOnRemove, so
// removing an envoy drops its cached inventory and the next vitals-only check-in
// is no longer ready (the caller requests a resend).
func TestInventoryEvictedOnEnvoyRemoval(t *testing.T) {
	s := &AgentService{}
	fi := fleet.NewFleetIndex()
	// Mirror the production wiring in grpc/server.go.
	fi.SetOnRemove(func(id string) { s.inventoryCache.Delete(id) })

	fi.AddEnvoy(fleet.EnvoyState{ID: "e1", Hostname: "h1", ConvergenceStatus: "converged"}, nil)

	// First contact caches the inventory; a vitals-only check-in then hits.
	s.ensureInventory("e1", []byte(`{"packages":["a"]}`), "h1")
	if !s.ensureInventory("e1", nil, "h1") {
		t.Fatal("cache hit expected before removal")
	}

	// Removing the envoy evicts its inventory; the next vitals-only check-in misses.
	fi.RemoveEnvoy("e1")
	if s.ensureInventory("e1", nil, "h1") {
		t.Fatal("inventory should be evicted after envoy removal → not ready")
	}
}
