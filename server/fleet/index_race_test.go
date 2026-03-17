// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

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
			fi.AddThrall(ThrallState{
				ID:               id,
				Hostname:         fmt.Sprintf("host-%d.example.com", n),
				ComplianceStatus: "unknown",
			}, nil)
		}(i)
	}

	// 50 readers.
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			for j := 0; j < 100; j++ {
				fi.GetThrall(fmt.Sprintf("w-%d", n%50))
			}
		}(i)
	}

	// 10 removers.
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			fi.RemoveThrall(fmt.Sprintf("w-%d", n))
		}(i)
	}

	wg.Wait()
}

func TestConcurrentUpdateRunStatus(t *testing.T) {
	fi := NewFleetIndex()
	const n = 100

	for i := 0; i < n; i++ {
		fi.AddThrall(ThrallState{
			ID:               fmt.Sprintf("t-%d", i),
			Hostname:         fmt.Sprintf("h-%d", i),
			ComplianceStatus: "unknown",
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
				statuses[idx%3],
				int64(idx%5),
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
				c := fi.ComplianceSummary()
				_ = c.Total
			}
		}()
	}

	wg.Wait()

	// Verify counters are internally consistent.
	c := fi.ComplianceSummary()
	sum := c.Compliant + c.Drifted + c.Failing + c.Stale + c.Unknown
	if sum != c.Total {
		t.Errorf("counter inconsistency: %d+%d+%d+%d+%d = %d, Total = %d",
			c.Compliant, c.Drifted, c.Failing, c.Stale, c.Unknown, sum, c.Total)
	}
}

func TestConcurrentMarkSeenDrainDirty(t *testing.T) {
	fi := NewFleetIndex()
	const n = 100

	for i := 0; i < n; i++ {
		fi.AddThrall(ThrallState{
			ID:               fmt.Sprintf("t-%d", i),
			Hostname:         fmt.Sprintf("h-%d", i),
			ComplianceStatus: "unknown",
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

	// Every thrall should have been drained at least once.
	if len(totalDrained) != n {
		t.Errorf("expected %d drained thralls, got %d", n, len(totalDrained))
	}
}

func TestConcurrentSetForcePushAll(t *testing.T) {
	fi := NewFleetIndex()
	const n = 50

	for i := 0; i < n; i++ {
		fi.AddThrall(ThrallState{
			ID:               fmt.Sprintf("t-%d", i),
			Hostname:         fmt.Sprintf("h-%d", i),
			ComplianceStatus: "unknown",
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
			fi.GetThrall(fmt.Sprintf("t-%d", idx))
		}(i)
	}

	wg.Wait()

	// After all SetForcePushAll calls, every active thrall should have ForcePush.
	for i := 0; i < n; i++ {
		ts := fi.GetThrall(fmt.Sprintf("t-%d", i))
		if ts != nil && !ts.ForcePush {
			t.Errorf("t-%d: ForcePush should be true after SetForcePushAll", i)
		}
	}
}

