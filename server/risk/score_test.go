// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

package risk

import (
	"testing"
	"time"

	"git.alexander4.io/vigo/server/convergence"
)

func TestScoreEnvoy_Clean(t *testing.T) {
	es := EnvoyInput{
		ID:                "test-1",
		Hostname:          "clean.example.com",
		LastSeen:          time.Now(),
		ConvergenceStatus: "converged",
		Traits:            map[string]string{},
	}
	r := ScoreEnvoy(es, 30*time.Second, 0)
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
	r := ScoreEnvoy(es, 30*time.Second, 0)
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
	r := ScoreEnvoy(es, 30*time.Second, 0) // 2×30s = 60s threshold
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
	r := ScoreEnvoy(es, 30*time.Second, 0)
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
	r := ScoreEnvoy(es, 30*time.Second, 0)
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
	r := ScoreEnvoy(es, 30*time.Second, 0)
	// 100 - 60 = 40 points
	if r.Score != 40 {
		t.Errorf("hardening envoy score = %d, want 40", r.Score)
	}
}

func TestScoreEnvoy_Diverged(t *testing.T) {
	es := EnvoyInput{
		ID:                "test-7",
		Hostname:          "drift.example.com",
		LastSeen:          time.Now(),
		ConvergenceStatus: "converged",
		Drift:             convergence.DriftDiverged,
		Traits:            map[string]string{},
	}
	r := ScoreEnvoy(es, 30*time.Second, 0)
	if r.Score != 15 {
		t.Errorf("diverged envoy score = %d, want 15", r.Score)
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
	r := ScoreEnvoy(es, 30*time.Second, 0)
	if r.Score != 5 {
		t.Errorf("ports envoy score = %d, want 5", r.Score)
	}
}

func TestScoreEnvoy_ExpiringCerts(t *testing.T) {
	es := EnvoyInput{
		ID:       "test-certs",
		Hostname: "certs.example.com",
		LastSeen: time.Now(),
		Traits: map[string]string{
			// 2 expired (+10 each = 20)
			"cert_lifecycle.certificates.0.days_remaining": "-5",
			"cert_lifecycle.certificates.1.days_remaining": "-1",
			// 1 expiring within 7 days (+5)
			"cert_lifecycle.certificates.2.days_remaining": "3",
			// 1 expiring within 30 days (+1)
			"cert_lifecycle.certificates.3.days_remaining": "20",
			// 1 with plenty of time (ignored)
			"cert_lifecycle.certificates.4.days_remaining": "180",
			// Unparseable value (skipped)
			"cert_lifecycle.certificates.5.days_remaining": "unknown",
		},
	}
	r := ScoreEnvoy(es, 30*time.Second, 0)
	// 20 + 5 + 1 = 26
	if r.Score != 26 {
		t.Errorf("expiring-cert envoy score = %d, want 26", r.Score)
	}
	var foundFactor *Factor
	for i := range r.Factors {
		if r.Factors[i].Name == "Expiring Certificates" {
			foundFactor = &r.Factors[i]
			break
		}
	}
	if foundFactor == nil {
		t.Fatalf("expected 'Expiring Certificates' factor, got %v", r.Factors)
	}
	if foundFactor.Points != 26 {
		t.Errorf("factor points = %d, want 26", foundFactor.Points)
	}
	want := "2 expired certs, 1 <7d, 1 <30d"
	if foundFactor.Detail != want {
		t.Errorf("factor detail = %q, want %q", foundFactor.Detail, want)
	}
}

func TestScoreEnvoy_NoExpiringCerts(t *testing.T) {
	// Envoy with healthy certs should not add an Expiring Certificates factor.
	es := EnvoyInput{
		ID:       "test-certs-ok",
		Hostname: "certs-ok.example.com",
		LastSeen: time.Now(),
		Traits: map[string]string{
			"cert_lifecycle.certificates.0.days_remaining": "90",
			"cert_lifecycle.certificates.1.days_remaining": "365",
		},
	}
	r := ScoreEnvoy(es, 30*time.Second, 0)
	for _, f := range r.Factors {
		if f.Name == "Expiring Certificates" {
			t.Errorf("did not expect Expiring Certificates factor, got %+v", f)
		}
	}
}

func TestScoreEnvoy_RunningCVEs(t *testing.T) {
	// Host with 2 running critical (+10 bonus), 3 running high (+6 bonus),
	// 1 running medium (+1 bonus). Also 2 installed critical (+20) and
	// 1 installed high (+5) for base CVE factors. Total: 20+5+10+6+1 = 42.
	es := EnvoyInput{
		ID:       "test-running-cves",
		Hostname: "running.example.com",
		LastSeen: time.Now(),
		Traits: map[string]string{
			// Installed CVE counts (existing factor).
			"security_scan.unique_critical": "2",
			"security_scan.unique_high":     "1",
			// Running-CVE summary counts (new factor).
			"cve_running_correlation.summary.critical": "2",
			"cve_running_correlation.summary.high":     "3",
			"cve_running_correlation.summary.medium":   "1",
			"cve_running_correlation.summary.low":      "4", // low is ignored
		},
	}
	r := ScoreEnvoy(es, 30*time.Second, 0)
	if r.Score != 42 {
		t.Errorf("running-cves envoy score = %d, want 42", r.Score)
	}

	var running *Factor
	for i := range r.Factors {
		if r.Factors[i].Name == "Running CVEs" {
			running = &r.Factors[i]
			break
		}
	}
	if running == nil {
		t.Fatalf("expected 'Running CVEs' factor, got %v", r.Factors)
	}
	if running.Points != 17 {
		t.Errorf("Running CVEs points = %d, want 17 (2*5 + 3*2 + 1*1)", running.Points)
	}
	want := "2 critical, 3 high, 1 medium running"
	if running.Detail != want {
		t.Errorf("Running CVEs detail = %q, want %q", running.Detail, want)
	}
}

func TestScoreEnvoy_NoRunningCVEs(t *testing.T) {
	// Host with installed CVEs but no correlation trait emitted —
	// the Running CVEs factor must not appear.
	es := EnvoyInput{
		ID:       "test-no-running",
		Hostname: "no-running.example.com",
		LastSeen: time.Now(),
		Traits: map[string]string{
			"security_scan.unique_critical": "1",
		},
	}
	r := ScoreEnvoy(es, 30*time.Second, 0)
	for _, f := range r.Factors {
		if f.Name == "Running CVEs" {
			t.Errorf("did not expect Running CVEs factor without correlation trait, got %+v", f)
		}
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
		got := Classify(tc.score)
		if got != tc.want {
			t.Errorf("Classify(%d) = %s, want %s", tc.score, got, tc.want)
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
	fr := ScoreFleet(envoys, 30*time.Second, 0, 10)

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
