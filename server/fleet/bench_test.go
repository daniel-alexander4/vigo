// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: BUSL-1.1

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
		fi.AddThrall(ThrallState{
			ID:               fmt.Sprintf("t-%d", i),
			Hostname:         fmt.Sprintf("web%04d.prod.example.com", i),
			IP:               fmt.Sprintf("10.0.%d.%d", i/256, i%256),
			ComplianceStatus: "compliant",
			Traits: map[string]string{
				"os.distro":  distros[i%5],
				"os.version": "22.04",
				"arch":       "amd64",
			},
		}, benchKey())
	}
	return fi
}

func BenchmarkFleetIndexGetThrall(b *testing.B) {
	fi := populateIndex(10000)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		fi.GetThrall(fmt.Sprintf("t-%d", i%10000))
	}
}

func BenchmarkFleetIndexGetThrallLite(b *testing.B) {
	fi := populateIndex(10000)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		fi.GetThrallLite(fmt.Sprintf("t-%d", i%10000))
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

func BenchmarkFleetIndexComplianceSummary(b *testing.B) {
	fi := populateIndex(10000)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		fi.ComplianceSummary()
	}
}
