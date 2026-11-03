// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: Apache-2.0

package fleet

import (
	"fmt"
	"sync"
	"testing"
	"time"
)

func TestConcurrentAddGetRemove(t *testing.T) {
	fi := NewFleetIndex()
	var wg sync.WaitGroup

	// 50 writers.
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			id := fmt.Sprintf("w-%d", n)
			fi.AddEnvoy(EnvoyState{
				ID:               id,
				Hostname:         fmt.Sprintf("host-%d.example.com", n),
				ConvergenceStatus: "no data",
			}, nil)
		}(i)
	}

	// 50 readers.
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			for j := 0; j < 100; j++ {
				fi.GetEnvoy(fmt.Sprintf("w-%d", n%50))
			}
		}(i)
	}

	// 10 removers.
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			fi.RemoveEnvoy(fmt.Sprintf("w-%d", n))
		}(i)
	}

	wg.Wait()
}

func TestConcurrentUpdateRunStatus(t *testing.T) {
	fi := NewFleetIndex()
	const n = 100

	for i := 0; i < n; i++ {
		fi.AddEnvoy(EnvoyState{
			ID:               fmt.Sprintf("t-%d", i),
			Hostname:         fmt.Sprintf("h-%d", i),
			ConvergenceStatus: "no data",
		}, nil)
	}

	var wg sync.WaitGroup

	// Concurrent status updates.
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			statuses := []string{"success", "failure", "partial"}
			fi.UpdateRunStatus(
				fmt.Sprintf("t-%d", idx),
				fmt.Sprintf("run-%d", idx),
				statuses[idx%3],
				int64(idx%5),
				int64(idx%2),
				int64(1+idx%4),
				time.Now(),
			)
		}(i)
	}

	// Concurrent summary reads.
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 20; j++ {
				c := fi.ConvergenceSummary()
				_ = c.Total
			}
		}()
	}

	wg.Wait()

	// Verify counters are internally consistent. Offline is an
	// orthogonal reachability dimension and is NOT part of the
	// convergence-status sum — every envoy is in exactly one
	// convergence bucket regardless of whether it's offline.
	c := fi.ConvergenceSummary()
	// Failure-axis buckets sum to Total. Drift counters (Changed,
	// Diverged) are orthogonal and overlap; they do not contribute to
	// this invariant.
	sum := c.Converged + c.Degraded + c.Failed + c.NoData
	if sum != c.Total {
		t.Errorf("counter inconsistency: %d+%d+%d+%d = %d, Total = %d",
			c.Converged, c.Degraded, c.Failed, c.NoData, sum, c.Total)
	}
}

func TestConcurrentMarkSeenDrainDirty(t *testing.T) {
	fi := NewFleetIndex()
	const n = 100

	for i := 0; i < n; i++ {
		fi.AddEnvoy(EnvoyState{
			ID:               fmt.Sprintf("t-%d", i),
			Hostname:         fmt.Sprintf("h-%d", i),
			ConvergenceStatus: "no data",
		}, nil)
	}

	var wg sync.WaitGroup

	// Concurrent MarkSeen.
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			fi.MarkSeen(fmt.Sprintf("t-%d", idx), time.Now())
		}(i)
	}

	// Concurrent DrainDirty.
	totalDrained := make(map[string]bool)
	var mu sync.Mutex
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			dirty := fi.DrainDirty()
			mu.Lock()
			for id := range dirty {
				totalDrained[id] = true
			}
			mu.Unlock()
		}()
	}

	wg.Wait()

	// Drain any remaining.
	remaining := fi.DrainDirty()
	for id := range remaining {
		totalDrained[id] = true
	}

	// Every envoy should have been drained at least once.
	if len(totalDrained) != n {
		t.Errorf("expected %d drained envoys, got %d", n, len(totalDrained))
	}
}

func TestConcurrentSetForcePushAll(t *testing.T) {
	fi := NewFleetIndex()
	const n = 50

	for i := 0; i < n; i++ {
		fi.AddEnvoy(EnvoyState{
			ID:               fmt.Sprintf("t-%d", i),
			Hostname:         fmt.Sprintf("h-%d", i),
			ConvergenceStatus: "no data",
		}, nil)
	}

	var wg sync.WaitGroup

	// Concurrent SetForcePushAll.
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			fi.SetForcePushAll()
		}()
	}

	// Concurrent reads.
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			fi.GetEnvoy(fmt.Sprintf("t-%d", idx))
		}(i)
	}

	wg.Wait()

	// After all SetForcePushAll calls, every active envoy should have ForcePush.
	for i := 0; i < n; i++ {
		ts := fi.GetEnvoy(fmt.Sprintf("t-%d", i))
		if ts != nil && !ts.ForcePush {
			t.Errorf("t-%d: ForcePush should be true after SetForcePushAll", i)
		}
	}
}

