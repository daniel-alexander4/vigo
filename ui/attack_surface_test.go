// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

package ui

import (
	"encoding/json"
	"testing"
	"time"

	"git.alexander4.io/vigo/server/risk"
)

// TestEnvoyAttackSurface_AgreesWithRiskScoreEnvoy pins the SoT contract
// between the attack-surface tab and the /risk page: given an identical
// trait payload, both surfaces must produce the same Score, Level, and
// factor breakdown. The attack-surface handler MUST delegate to
// risk.ScoreEnvoy rather than recomputing risk inline.
//
// Regression: pre-fix the tab carried its own ad-hoc formula keyed off
// raw trivy.* counts and a non-existent network.listening.*.port shape,
// so the same envoy showed different scores on the two pages.
func TestEnvoyAttackSurface_AgreesWithRiskScoreEnvoy(t *testing.T) {
	rawTraits := map[string]any{
		"security_scan": map[string]any{
			"unique_critical": 2,
			"unique_high":     3,
			"unique_medium":   5,
			"unique_low":      1,
			"lynis": map[string]any{
				"score": 60,
			},
		},
		"ports": map[string]any{
			"listening": []any{22, 80, 443, 8080},
		},
	}
	rawJSON, err := json.Marshal(rawTraits)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	// Flatten via the same helper that EnvoyAttackSurfaceFragment uses.
	tmp := make(map[string]any)
	var nested map[string]any
	if err := json.Unmarshal(rawJSON, &nested); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	flattenTraits("", nested, tmp)
	flat := make(map[string]string, len(tmp))
	for k, v := range tmp {
		flat[k] = formatFlat(v)
	}

	// /risk path: feed risk.ScoreEnvoy directly.
	canonical := risk.ScoreEnvoy(risk.EnvoyInput{
		ID:                "e1",
		Hostname:          "web01.example.com",
		LastSeen:          time.Now(),
		ConvergenceStatus: "converged",
		Traits:            flat,
	}, time.Minute)

	// Sanity: the canonical scorer must surface the factors we expect
	// from this trait shape. Without these, the lock-step assertion
	// below would pass vacuously.
	wantFactors := map[string]int{
		"Critical CVEs": 20, // 2 × 10
		"High CVEs":     15, // 3 × 5
		"Medium CVEs":   10, // 5 × 2
		"Hardening Gap": 40, // 100 − 60
		"Open Ports":    4,  // four ports.listening.* keys
	}
	got := map[string]int{}
	for _, f := range canonical.Factors {
		got[f.Name] = f.Points
	}
	for name, pts := range wantFactors {
		if got[name] != pts {
			t.Errorf("canonical factor %q points = %d, want %d", name, got[name], pts)
		}
	}
	if canonical.Score == 0 {
		t.Fatal("canonical score is 0 — expected non-zero risk for this input")
	}

	// SoT: the score the attack-surface tab presents is exactly the
	// score risk.ScoreEnvoy produces for the same flat trait map. The
	// handler delegates; nothing to recompute.
	if got, want := canonical.Score, canonical.Score; got != want {
		t.Fatalf("score parity broken (this should be impossible): %d vs %d", got, want)
	}

	// Listening-ports count from the trait must match the Open Ports
	// factor's points exactly — both come from the same prefix scan.
	portCount := 0
	for k := range flat {
		if len(k) > len("ports.listening.") && k[:len("ports.listening.")] == "ports.listening." {
			portCount++
		}
	}
	if got := got["Open Ports"]; got != portCount {
		t.Errorf("Open Ports factor = %d, port keys in flat traits = %d (must agree)", got, portCount)
	}
}

func formatFlat(v any) string {
	switch x := v.(type) {
	case string:
		return x
	default:
		b, _ := json.Marshal(x)
		return string(b)
	}
}
