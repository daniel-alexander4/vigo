// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: AGPL-3.0-only

package fleet

import (
	"fmt"
	"math/rand"
	"testing"
	"time"

	"git.alexander4.io/vigo/server/convergence"
)

// TestDriftCounterCoherence_RandomSequence pins the invariant that
// `ConvergenceCounters.{Changed, Diverged}` always match what you'd
// compute by walking the non-revoked envoys' `EnvoyState.Drift` field
// directly. The invariant is maintained today by routing every Drift
// mutation through `bumpDriftCounters` — called from UpdateRunStatus
// (failure transitions + drift transitions both touch the counter
// path), AddEnvoy, RemoveEnvoy, and SetRevoked. A refactor that adds
// a new mutation site without the bump — or splits a current site's
// state-vs-counter update so they fall out of phase — would silently
// corrupt the counters. This test catches that loudly by driving a
// random sequence across all four mutation actions and asserting
// coherence after every step.
//
// Counter semantics (per `ConvergenceCounters` doc): Diverged ⊆ Changed
// — an envoy in DriftDiverged contributes to BOTH Changed and Diverged
// counts. Revoked envoys are excluded from both. Ground truth therefore:
//
//	actualChanged  = |non-revoked envoys where Drift ∈ {Changed, Diverged}|
//	actualDiverged = |non-revoked envoys where Drift == Diverged|
//
// Notes on the original pending entry:
//   - It suggested `sum(driftCounters) == |envoys|` — that formula is
//     wrong because of the Diverged ⊆ Changed nesting. The
//     walk-and-compare assertion below is the correct invariant.
//   - It pointed at the `if newDrift != oldDrift` guard at the
//     UpdateRunStatus drift-update site, claiming a future refactor
//     that drops the guard would corrupt counters. Verified during
//     test development: dropping the guard does NOT corrupt counters,
//     because `bumpDriftCounters(X, X)` is idempotent (decrement and
//     increment of the same level net to zero). The guard is a perf
//     optimization, not a correctness gate. The real risk this test
//     catches is "a new Drift mutation site that skips the bump
//     entirely" — broader and more useful than the original framing.
func TestDriftCounterCoherence_RandomSequence(t *testing.T) {
	fi := NewFleetIndex()
	// Fixed seed so failures are deterministic; the test pins an
	// invariant, not a probabilistic claim.
	rng := rand.New(rand.NewSource(0xC0FFEE)) //nolint:gosec // test-only

	activeIDs := []string{}
	nextID := 0
	addEnvoy := func() string {
		nextID++
		id := fmt.Sprintf("env-%d", nextID)
		fi.AddEnvoy(EnvoyState{
			ID:                id,
			Hostname:          id,
			ConvergenceStatus: "no data",
		}, nil)
		activeIDs = append(activeIDs, id)
		return id
	}

	assertCoherent := func(step int, action string) {
		t.Helper()
		// Revoked envoys are excluded from ConvergenceCounters — both
		// failure (Total/Converged/Degraded/Failed/NoData) and drift
		// (Changed/Diverged) axes. SetRevoked moves the drift counter
		// to DriftNone while preserving EnvoyState.Drift so un-revoke
		// can restore it. Ground-truth walk must mirror that filter.
		var actualChanged, actualDiverged int
		for _, e := range fi.AllEnvoys() {
			if e.Revoked {
				continue
			}
			switch e.Drift {
			case convergence.DriftChanged:
				actualChanged++
			case convergence.DriftDiverged:
				actualChanged++
				actualDiverged++
			}
		}
		got := fi.ConvergenceSummary()
		if got.Changed != actualChanged || got.Diverged != actualDiverged {
			t.Fatalf(
				"drift-counter coherence broken after step %d (%s):\n"+
					"  cached: Changed=%d Diverged=%d\n"+
					"  walked: Changed=%d Diverged=%d (non-revoked only)\n"+
					"  total:  %d",
				step, action,
				got.Changed, got.Diverged,
				actualChanged, actualDiverged,
				got.Total,
			)
		}
	}

	// Seed with 5 envoys so the random loop has something to mutate
	// before the first add.
	for i := 0; i < 5; i++ {
		addEnvoy()
	}
	assertCoherent(0, "init")

	const (
		steps        = 200
		actionUpdate = 0
		actionAdd    = 1
		actionRemove = 2
		actionRevoke = 3
	)
	actionNames := [...]string{"update", "add", "remove", "revoke"}
	// Track which actions actually fired so the acceptance criterion
	// "all four bump call sites exercised" is verifiable from the test.
	fired := [4]int{}

	for step := 1; step <= steps; step++ {
		action := rng.Intn(4)
		switch action {
		case actionUpdate:
			if len(activeIDs) == 0 {
				continue
			}
			id := activeIDs[rng.Intn(len(activeIDs))]
			// ChangedCount in {0, 1, 2} drives ComputeDrift through
			// None/Changed/Diverged depending on the consecutive run
			// of non-zero ChangedCount entries in RecentRuns. Mix all
			// three over the run sequence.
			changedCount := int64(rng.Intn(3))
			fi.UpdateRunStatus(
				id,
				fmt.Sprintf("run-%d", step),
				"ok",
				changedCount,
				0, // failedCount
				5, // moduleCount
				time.Now(),
			)
		case actionAdd:
			addEnvoy()
		case actionRemove:
			if len(activeIDs) == 0 {
				continue
			}
			idx := rng.Intn(len(activeIDs))
			fi.RemoveEnvoy(activeIDs[idx])
			activeIDs = append(activeIDs[:idx], activeIDs[idx+1:]...)
		case actionRevoke:
			if len(activeIDs) == 0 {
				continue
			}
			id := activeIDs[rng.Intn(len(activeIDs))]
			fi.SetRevoked(id, rng.Intn(2) == 0)
		}
		fired[action]++
		assertCoherent(step, actionNames[action])
	}

	// All four mutation paths must have fired at least once for the
	// test to be the coverage pin it claims to be. If RNG rolls
	// extreme and one bucket stays zero, the seed needs tuning.
	for i, n := range fired {
		if n == 0 {
			t.Errorf("action %s never fired across %d steps — RNG seed needs adjustment",
				actionNames[i], steps)
		}
	}
}
