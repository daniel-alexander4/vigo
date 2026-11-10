// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: AGPL-3.0-only

package grpc

import (
	"context"
	"testing"

	"git.alexander4.io/vigo/server/db"
	"git.alexander4.io/vigo/server/testutil"
)

// TestOptionCGatingSkipsPersistOnChurnyTick is the end-to-end proof of ADR-033
// Option C against a REAL DB-backed TraitsBatcher: it replays the check-in
// handler's exact per-tick gating sequence (ensureInventory → traitsChangeKey
// with the agent's vitals_state_hash → Mark with the lazy merge closure) and
// asserts that a churny-only tick — same vitals_state_hash, different vitals
// bytes — writes NO new envoy_traits row, while a meaningful change (a moved
// vitals_state_hash) does. This is the persist-skip the whole lever depends on.
func TestOptionCGatingSkipsPersistOnChurnyTick(t *testing.T) {
	sqlDB := testutil.OpenMigratedTestDB(t)
	queries := db.New(sqlDB)
	batch := db.NewTraitsBatcher(queries)
	s := &AgentService{traitsBatch: batch}
	ctx := context.Background()

	if _, err := sqlDB.Exec(
		`INSERT INTO envoys (id, hostname, ip_address, pubkey_pem) VALUES ('e1','h1','10.0.0.1','key')`,
	); err != nil {
		t.Fatalf("seed envoy: %v", err)
	}

	const invHash = "inv1"
	inv := []byte(`{"packages":["a"]}`)

	// tick mirrors the server's buildCheckInResponse trait block for a split agent.
	tick := func(vitalsStateHash string, vitals, inventoryJSON []byte) {
		t.Helper()
		if !s.ensureInventory("e1", inventoryJSON, invHash) {
			t.Fatal("ensureInventory not ready")
		}
		key := traitsChangeKey(vitalsStateHash, vitals, nil, invHash)
		batch.Mark("e1", key, func() string {
			return string(s.mergeCachedTraits("e1", vitals))
		})
		batch.Flush(ctx)
	}

	rowCount := func() int {
		var n int
		if err := sqlDB.QueryRow(`SELECT COUNT(*) FROM envoy_traits WHERE envoy_id='e1'`).Scan(&n); err != nil {
			t.Fatalf("count: %v", err)
		}
		return n
	}

	// Tick 1 — first contact: inventory + vitals → one persist.
	tick("vsh-stable", []byte(`{"loadavg":1,"users":["dan"]}`), inv)
	if got := rowCount(); got != 1 {
		t.Fatalf("after first tick: rows=%d, want 1", got)
	}

	// Ticks 2–4 — churny vitals (loadavg changes every tick) but the SAME
	// vitals_state_hash: the change-key is stable, Mark dedups, NO new rows.
	tick("vsh-stable", []byte(`{"loadavg":50,"users":["dan"]}`), nil)
	tick("vsh-stable", []byte(`{"loadavg":250,"users":["dan"]}`), nil)
	tick("vsh-stable", []byte(`{"loadavg":900,"users":["dan"]}`), nil)
	if got := rowCount(); got != 1 {
		t.Fatalf("churny-only ticks must not persist: rows=%d, want 1", got)
	}

	// Tick 5 — meaningful change moves vitals_state_hash → the key moves → persist.
	tick("vsh-changed", []byte(`{"loadavg":900,"users":["dan","ann"]}`), nil)
	if got := rowCount(); got != 2 {
		t.Fatalf("meaningful change must persist: rows=%d, want 2", got)
	}
}
