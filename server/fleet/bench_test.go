// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: Apache-2.0

package fleet

import (
	"crypto/ed25519"
	"crypto/rand"
	"fmt"
	"testing"
	"time"
)

func benchKey() ed25519.PublicKey {
	pub, _, _ := ed25519.GenerateKey(rand.Reader)
	return pub
}

func populateIndex(n int) *FleetIndex {
	fi := NewFleetIndex()
	distros := []string{"ubuntu", "debian", "centos", "rhel", "suse"}
	for i := 0; i < n; i++ {
		fi.AddEnvoy(EnvoyState{
			ID:               fmt.Sprintf("t-%d", i),
			Hostname:         fmt.Sprintf("web%04d.prod.example.com", i),
			IP:               fmt.Sprintf("10.0.%d.%d", i/256, i%256),
			ConvergenceStatus: "converged",
			Traits: map[string]string{
				"os.distro":  distros[i%5],
				"os.version": "22.04",
				"arch":       "amd64",
			},
		}, benchKey())
	}
	return fi
}

func BenchmarkFleetIndexGetEnvoy(b *testing.B) {
	fi := populateIndex(10000)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		fi.GetEnvoy(fmt.Sprintf("t-%d", i%10000))
	}
}

func BenchmarkFleetIndexGetEnvoyLite(b *testing.B) {
	fi := populateIndex(10000)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		fi.GetEnvoyLite(fmt.Sprintf("t-%d", i%10000))
	}
}

func BenchmarkFleetIndexGetPubKey(b *testing.B) {
	fi := populateIndex(10000)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		fi.GetPubKey(fmt.Sprintf("t-%d", i%10000))
	}
}

func BenchmarkFleetIndexListByGlob(b *testing.B) {
	fi := populateIndex(10000)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		fi.ListByGlob("web*.prod.*")
	}
}

func BenchmarkFleetIndexListByTrait(b *testing.B) {
	fi := populateIndex(10000)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		fi.ListByTrait("os.distro", "ubuntu")
	}
}

func BenchmarkFleetIndexMarkSeen(b *testing.B) {
	fi := populateIndex(10000)
	now := time.Now()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		fi.MarkSeen(fmt.Sprintf("t-%d", i%10000), now)
	}
}

func BenchmarkFleetIndexMarkSeenInMemory(b *testing.B) {
	fi := populateIndex(10000)
	now := time.Now()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		fi.MarkSeenInMemory(fmt.Sprintf("t-%d", i%10000), now)
	}
}

// BenchmarkFleetIndexMarkSeenParallel measures MarkSeen under writer contention,
// the load shape that pinned p99 at 14.6s in the 2026-05-25 25K plateau (see
// test/loadtest/results/2026-05-25-girlslaptop/FINDINGS.md). 25K-envoy index
// keeps stripe occupancy at the same ≈100-envoys-per-stripe ratio the production
// fleet sees; ids are pre-built so the harness measures lock-acquire cost, not
// fmt.Sprintf churn.
func BenchmarkFleetIndexMarkSeenParallel(b *testing.B) {
	const n = 25000
	fi := populateIndex(n)
	ids := make([]string, n)
	for i := 0; i < n; i++ {
		ids[i] = fmt.Sprintf("t-%d", i)
	}
	now := time.Now()
	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		i := 0
		for pb.Next() {
			fi.MarkSeen(ids[i%n], now)
			i++
		}
	})
}

func BenchmarkFleetIndexConvergenceSummary(b *testing.B) {
	fi := populateIndex(10000)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		fi.ConvergenceSummary()
	}
}

func BenchmarkFleetIndexUpdateRunStatus(b *testing.B) {
	const n = 10000
	fi := populateIndex(n)
	now := time.Now()
	// Pre-build IDs so the benchmark measures UpdateRunStatus's own allocations,
	// not fmt.Sprintf churn from the harness.
	ids := make([]string, n)
	runIDs := make([]string, n)
	for i := 0; i < n; i++ {
		ids[i] = fmt.Sprintf("t-%d", i)
		runIDs[i] = fmt.Sprintf("run-%d", i)
	}
	b.ResetTimer()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		fi.UpdateRunStatus(ids[i%n], runIDs[i%n], "ok", 0, 0, 12, now)
	}
}
