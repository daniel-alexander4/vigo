// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: Apache-2.0

package main

import "testing"

// TestVitalsStateHashStableAcrossChurnyTicks proves the premise the Option C
// load test rests on: freshVitals() stamps a new timestamp every call (so the
// bytes differ tick to tick), but the operational content (users/ports) is
// stable, so the vitals_state_hash the harness ships must be IDENTICAL across
// ticks. That stable hash is what lets the server's change-key dedup and skip
// the per-tick persist; if it drifted, the load test would persist every tick
// and measure nothing.
func TestVitalsStateHashStableAcrossChurnyTicks(t *testing.T) {
	a := freshVitals()
	b := freshVitals()
	if string(a) == string(b) {
		t.Skip("vitals bytes identical (timestamp didn't advance) — premise not exercised")
	}
	ha, hb := vitalsStateHash(a), vitalsStateHash(b)
	if ha == "" {
		t.Fatal("vitals_state_hash empty — no operational collectors were hashed")
	}
	if ha != hb {
		t.Errorf("vitals_state_hash must be stable across churny ticks: %s vs %s", ha, hb)
	}
}

// TestVitalsStateHashMovesOnOperationalChange confirms the hash ignores churny
// telemetry but moves on a real operational change — matching the agent's
// semantics so the server gates correctly.
func TestVitalsStateHashMovesOnOperationalChange(t *testing.T) {
	base := []byte(`{"loadavg":1,"users":[{"name":"a"}],"ports":[{"port":"22"}]}`)
	churn := []byte(`{"loadavg":999,"users":[{"name":"a"}],"ports":[{"port":"22"}]}`)
	if vitalsStateHash(base) != vitalsStateHash(churn) {
		t.Error("a churny loadavg change must NOT move vitals_state_hash")
	}
	moved := []byte(`{"loadavg":1,"users":[{"name":"a"}],"ports":[{"port":"22"},{"port":"443"}]}`)
	if vitalsStateHash(base) == vitalsStateHash(moved) {
		t.Error("an operational change (new listening port) must move vitals_state_hash")
	}
	if vitalsStateHash([]byte(`{"loadavg":1}`)) != "" {
		t.Error("no operational collectors present → empty hash")
	}
}
