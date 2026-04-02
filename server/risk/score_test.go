// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

package risk

import (
	"testing"
	"time"
)

func TestScoreEnvoy_Clean(t *testing.T) {
	es := EnvoyInput{
		ID:                "test-1",
		Hostname:          "clean.example.com",
		LastSeen:          time.Now(),
		ConvergenceStatus: "converged",
		Traits:            map[string]string{},
	}
	r := ScoreEnvoy(es, 30*time.Second)
	if r.Score != 0 {
		t.Errorf("clean envoy score = %d, want 0", r.Score)
	}
	if r.Level != LevelLow {
		t.Errorf("clean envoy level = %s, want low", r.Level)
	}
}

func TestScoreEnvoy_CriticalCVEs(t *testing.T) {
	es := EnvoyInput{
		ID:       "test-2",
		Hostname: "vuln.example.com",
		LastSeen: time.Now(),
		Traits: map[string]string{
			"security_scan.unique_critical": "3",
			"security_scan.unique_high":     "2",
		},
	}
	r := ScoreEnvoy(es, 30*time.Second)
	// 3 critical × 10 = 30, 2 high × 5 = 10 → 40
	if r.Score != 40 {
		t.Errorf("CVE envoy score = %d, want 40", r.Score)
	}
	if r.Level != LevelHigh {
		t.Errorf("CVE envoy level = %s, want high", r.Level)
	}
}

func TestScoreEnvoy_Offline(t *testing.T) {
	es := EnvoyInput{
		ID:       "test-3",
		Hostname: "stale.example.com",
		LastSeen: time.Now().Add(-5 * time.Minute),
		Traits:   map[string]string{},
	}
	r := ScoreEnvoy(es, 30*time.Second) // 2×30s = 60s threshold
	if r.Score != 20 {
		t.Errorf("stale envoy score = %d, want 20", r.Score)
	}
}

func TestScoreEnvoy_Capped(t *testing.T) {
	es := EnvoyInput{
		ID:                "test-4",
		Hostname:          "bad.example.com",
		LastSeen:          time.Now().Add(-5 * time.Minute),
		ConvergenceStatus: "failed",
		Traits: map[string]string{
			"security_scan.unique_critical":      "10",
			"security_scan.unique_high":          "10",
			"security_scan.lynis.score":         "20",
			"security_scan.chkrootkit.infected": "2",
			"security_scan.aide.changed":        "5",
		},
	}
	r := ScoreEnvoy(es, 30*time.Second)
	if r.Score != 100 {
		t.Errorf("bad envoy score = %d, want 100 (capped)", r.Score)
	}
	if r.Level != LevelCritical {
		t.Errorf("bad envoy level = %s, want critical", r.Level)
	}
}

func TestScoreEnvoy_Revoked(t *testing.T) {
	es := EnvoyInput{
		ID:      "test-5",
		Revoked: true,
		Traits: map[string]string{
			"security_scan.unique_critical": "10",
		},
	}
	r := ScoreEnvoy(es, 30*time.Second)
	if r.Score != 0 {
		t.Errorf("revoked envoy score = %d, want 0", r.Score)
	}
}

func TestScoreEnvoy_Hardening(t *testing.T) {
	es := EnvoyInput{
		ID:       "test-6",
		Hostname: "weak.example.com",
		LastSeen: time.Now(),
		Traits: map[string]string{
			"security_scan.lynis.score": "60",
		},
	}
	r := ScoreEnvoy(es, 30*time.Second)
	// 100 - 60 = 40 points
	if r.Score != 40 {
		t.Errorf("hardening envoy score = %d, want 40", r.Score)
	}
}

func TestScoreEnvoy_Relapsed(t *testing.T) {
	es := EnvoyInput{
		ID:                "test-7",
		Hostname:          "drift.example.com",
		LastSeen:          time.Now(),
		ConvergenceStatus: "relapsed",
		Traits:            map[string]string{},
	}
	r := ScoreEnvoy(es, 30*time.Second)
	if r.Score != 10 {
		t.Errorf("relapsed envoy score = %d, want 10", r.Score)
	}
}

func TestScoreEnvoy_OpenPorts(t *testing.T) {
	es := EnvoyInput{
		ID:       "test-8",
		Hostname: "ports.example.com",
		LastSeen: time.Now(),
		Traits: map[string]string{
			"ports.listening.0": "22",
			"ports.listening.1": "80",
			"ports.listening.2": "443",
			"ports.listening.3": "8080",
			"ports.listening.4": "3306",
		},
	}
	r := ScoreEnvoy(es, 30*time.Second)
	if r.Score != 5 {
		t.Errorf("ports envoy score = %d, want 5", r.Score)
	}
}

func TestClassify(t *testing.T) {
	cases := []struct {
		score int
		want  Level
	}{
		{0, LevelLow},
		{19, LevelLow},
		{20, LevelMedium},
		{39, LevelMedium},
		{40, LevelHigh},
		{69, LevelHigh},
		{70, LevelCritical},
		{100, LevelCritical},
	}
	for _, tc := range cases {
		got := classify(tc.score)
		if got != tc.want {
			t.Errorf("classify(%d) = %s, want %s", tc.score, got, tc.want)
		}
	}
}

func TestScoreFleet(t *testing.T) {
	envoys := []EnvoyInput{
		{ID: "a", Hostname: "clean.example.com", LastSeen: time.Now(), Traits: map[string]string{}},
		{ID: "b", Hostname: "vuln.example.com", LastSeen: time.Now(), Traits: map[string]string{
			"security_scan.unique_critical": "5",
		}},
		{ID: "c", Hostname: "revoked.example.com", Revoked: true, Traits: map[string]string{}},
	}
	fr := ScoreFleet(envoys, 30*time.Second, 10)

	if fr.TotalEnvoys != 3 {
		t.Errorf("total = %d, want 3", fr.TotalEnvoys)
	}
	if fr.ScoredEnvoys != 2 {
		t.Errorf("scored = %d, want 2 (revoked excluded)", fr.ScoredEnvoys)
	}
	if fr.MaxScore != 50 {
		t.Errorf("max = %d, want 50", fr.MaxScore)
	}
	if len(fr.TopRisks) != 2 {
		t.Errorf("top risks = %d, want 2", len(fr.TopRisks))
	}
	if fr.TopRisks[0].Hostname != "vuln.example.com" {
		t.Errorf("top risk = %s, want vuln.example.com", fr.TopRisks[0].Hostname)
	}
}
