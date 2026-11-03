// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: Apache-2.0

package fleet

import (
	"fmt"
	"testing"
)

// Each agent emits both unique_critical/high/medium/low (counted from the
// FULL deduped scanner set) and the cves.N.* list (truncated to MAX_CVES).
// SecurityScanData has to handle two consequences: (a) the truncated list
// can lead to fleet undercount, surfaced via AnyTruncated; (b) the same
// CVE reported by multiple hosts with different severities used to flip
// non-deterministically — now max-severity wins. These tests pin the
// behavior so a future regression doesn't silently degrade the stat card.

func TestSecurityScanData_MaxSeverityWinsAcrossHosts(t *testing.T) {
	fi := NewFleetIndex()
	pubKey := genKey(t)

	// Host A reports CVE-A as high (e.g., agent saw it from both trivy and
	// debsecan, merged to higher rank). Host B reports same CVE as medium
	// (only one scanner saw it). Fleet bucket should land in HIGH.
	fi.AddEnvoy(EnvoyState{
		ID: "h1", Hostname: "host1",
		Traits: map[string]string{
			"security_scan.cves.0.id":       "CVE-A",
			"security_scan.cves.0.severity": "high",
			"security_scan.unique_high":     "1",
		},
	}, pubKey)
	fi.AddEnvoy(EnvoyState{
		ID: "h2", Hostname: "host2",
		Traits: map[string]string{
			"security_scan.cves.0.id":       "CVE-A",
			"security_scan.cves.0.severity": "medium",
			"security_scan.unique_medium":   "1",
		},
	}, pubKey)

	ds := NewDataSource(fi, nil, nil, nil)
	data := ds.SecurityScanData()

	if data.UniqueCVEs != 1 {
		t.Errorf("UniqueCVEs = %d, want 1", data.UniqueCVEs)
	}
	if data.UniqueHigh != 1 {
		t.Errorf("UniqueHigh = %d, want 1 (max severity across hosts)", data.UniqueHigh)
	}
	if data.UniqueMedium != 0 {
		t.Errorf("UniqueMedium = %d, want 0 (medium reporter loses to high)", data.UniqueMedium)
	}
}

func TestSecurityScanData_DedupDeterministic(t *testing.T) {
	// Same CVE on five hosts with mixed severities. Map iteration order
	// is non-deterministic; max-severity dedup must produce the same
	// fleet aggregate every time.
	severities := []string{"critical", "high", "medium", "low", "high"}
	uniqueKeys := []string{
		"security_scan.unique_critical",
		"security_scan.unique_high",
		"security_scan.unique_medium",
		"security_scan.unique_low",
		"security_scan.unique_high",
	}

	for trial := 0; trial < 5; trial++ {
		fi := NewFleetIndex()
		pubKey := genKey(t)
		for i := 0; i < 5; i++ {
			fi.AddEnvoy(EnvoyState{
				ID: fmt.Sprintf("h%d", i), Hostname: fmt.Sprintf("host%d", i),
				Traits: map[string]string{
					"security_scan.cves.0.id":       "CVE-X",
					"security_scan.cves.0.severity": severities[i],
					uniqueKeys[i]:                   "1",
				},
			}, pubKey)
		}
		data := NewDataSource(fi, nil, nil, nil).SecurityScanData()

		if data.UniqueCVEs != 1 {
			t.Errorf("trial %d: UniqueCVEs = %d, want 1", trial, data.UniqueCVEs)
		}
		if data.UniqueCritical != 1 {
			t.Errorf("trial %d: UniqueCritical = %d, want 1 (max wins)", trial, data.UniqueCritical)
		}
		if data.UniqueHigh != 0 || data.UniqueMedium != 0 || data.UniqueLow != 0 {
			t.Errorf("trial %d: expected only critical bucket populated, got C=%d H=%d M=%d L=%d",
				trial, data.UniqueCritical, data.UniqueHigh, data.UniqueMedium, data.UniqueLow)
		}
	}
}

func TestSecurityScanData_TruncationFlag(t *testing.T) {
	fi := NewFleetIndex()
	pubKey := genKey(t)

	// Agent reports unique_high=5 (computed from full deduped set on the
	// host) but only 3 cves entries rode the wire (the rest were
	// truncated past MAX_CVES). Server should set AnyTruncated.
	fi.AddEnvoy(EnvoyState{
		ID: "h1", Hostname: "truncated-host",
		Traits: map[string]string{
			"security_scan.unique_high":     "5",
			"security_scan.cves.0.id":       "CVE-1",
			"security_scan.cves.0.severity": "high",
			"security_scan.cves.1.id":       "CVE-2",
			"security_scan.cves.1.severity": "high",
			"security_scan.cves.2.id":       "CVE-3",
			"security_scan.cves.2.severity": "high",
		},
	}, pubKey)

	data := NewDataSource(fi, nil, nil, nil).SecurityScanData()

	if !data.AnyTruncated {
		t.Errorf("expected AnyTruncated=true (5 unique > 3 wire)")
	}
	if len(data.PerHost) != 1 {
		t.Fatalf("expected 1 PerHost row, got %d", len(data.PerHost))
	}
	if !data.PerHost[0].Truncated {
		t.Errorf("expected PerHost[0].Truncated=true")
	}
}

func TestSecurityScanData_NoTruncationWhenComplete(t *testing.T) {
	fi := NewFleetIndex()
	pubKey := genKey(t)

	// unique_high=2 matches len(cves)=2 → not truncated.
	fi.AddEnvoy(EnvoyState{
		ID: "h1", Hostname: "complete-host",
		Traits: map[string]string{
			"security_scan.unique_high":     "2",
			"security_scan.cves.0.id":       "CVE-1",
			"security_scan.cves.0.severity": "high",
			"security_scan.cves.1.id":       "CVE-2",
			"security_scan.cves.1.severity": "high",
		},
	}, pubKey)

	data := NewDataSource(fi, nil, nil, nil).SecurityScanData()

	if data.AnyTruncated {
		t.Errorf("expected AnyTruncated=false (2 unique == 2 wire)")
	}
	if len(data.PerHost) != 1 || data.PerHost[0].Truncated {
		t.Errorf("expected PerHost[0].Truncated=false")
	}
}
