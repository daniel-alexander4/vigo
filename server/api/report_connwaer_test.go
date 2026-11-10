// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: AGPL-3.0-only

package api

import (
	"strings"
	"testing"
	"time"

	"git.alexander4.io/vigo/server/fleet"
)

// TestApplyConnwaerEscalations_Satisfied — every envoy reports
// connwaer.encryption_at_rest.status == "pass", so HIPAA
// 164.312(a)(2)(iv) escalates from "not_satisfied" to "satisfied"
// and the evidence string carries the Connwaer attribution.
func TestApplyConnwaerEscalations_Satisfied(t *testing.T) {
	fi := fleet.NewFleetIndex()
	for _, host := range []string{"a", "b", "c"} {
		fi.AddEnvoy(fleet.EnvoyState{
			ID: host, Hostname: host, LastSeen: time.Now(),
			Traits: map[string]string{"connwaer.encryption_at_rest.status": "pass"},
		}, nil)
	}
	h := &ReportHandler{fleetIndex: fi}

	findings := []ControlFinding{
		{ControlID: "164.312(a)(2)(iv)", Title: "Encryption and Decryption", Status: "not_satisfied"},
		{ControlID: "164.312(a)(1)", Title: "Access Control", Status: "not_satisfied"},
	}
	h.applyConnwaerEscalations("compliance", findings)

	if findings[0].Status != "satisfied" {
		t.Errorf("encryption finding status = %q, want satisfied", findings[0].Status)
	}
	if !strings.Contains(findings[0].Evidence, "VERIFIED by Connwaer") {
		t.Errorf("encryption finding evidence missing Connwaer attribution: %q", findings[0].Evidence)
	}
	if findings[1].Status != "not_satisfied" {
		t.Errorf("unmapped control status = %q, want not_satisfied (no escalation)", findings[1].Status)
	}
}

// TestApplyConnwaerEscalations_Partial — half the fleet passes the
// trait, so the helper returns "partial" with fleet-coverage detail.
func TestApplyConnwaerEscalations_Partial(t *testing.T) {
	fi := fleet.NewFleetIndex()
	fi.AddEnvoy(fleet.EnvoyState{
		ID: "a", Hostname: "a", LastSeen: time.Now(),
		Traits: map[string]string{"connwaer.session_timeout.status": "pass"},
	}, nil)
	fi.AddEnvoy(fleet.EnvoyState{
		ID: "b", Hostname: "b", LastSeen: time.Now(),
		Traits: map[string]string{"connwaer.session_timeout.status": "fail"},
	}, nil)
	h := &ReportHandler{fleetIndex: fi}

	findings := []ControlFinding{
		{ControlID: "8.2.8", Title: "Session idle timeout", Status: "not_satisfied"},
	}
	h.applyConnwaerEscalations("pci", findings)

	if findings[0].Status != "partial" {
		t.Errorf("status = %q, want partial", findings[0].Status)
	}
	if !strings.Contains(findings[0].Evidence, "VERIFIED by Connwaer") {
		t.Errorf("evidence missing Connwaer attribution: %q", findings[0].Evidence)
	}
}

// TestApplyConnwaerEscalations_NoTrait — no envoy reports the trait,
// so the helper returns "not_satisfied" + upsell text and the
// escalation pass leaves Status untouched (so the upstream "missing"
// rendering keeps working downstream).
func TestApplyConnwaerEscalations_NoTrait(t *testing.T) {
	fi := fleet.NewFleetIndex()
	fi.AddEnvoy(fleet.EnvoyState{
		ID: "a", Hostname: "a", LastSeen: time.Now(),
	}, nil)
	h := &ReportHandler{fleetIndex: fi}

	original := ControlFinding{ControlID: "AC-4", Title: "Information Flow Enforcement", Status: "not_satisfied", Evidence: ""}
	findings := []ControlFinding{original}
	h.applyConnwaerEscalations("nist", findings)

	if findings[0].Status != "not_satisfied" {
		t.Errorf("status = %q, want not_satisfied (no escalation when no fleet evidence)", findings[0].Status)
	}
	if findings[0].Evidence != "" {
		t.Errorf("evidence = %q, want empty (upsell text must not leak into framework rows)", findings[0].Evidence)
	}
}

// TestApplyConnwaerEscalations_UnmappedFramework — the slug has no
// entry in connwaerCapabilityByControl, so nothing is touched.
func TestApplyConnwaerEscalations_UnmappedFramework(t *testing.T) {
	fi := fleet.NewFleetIndex()
	h := &ReportHandler{fleetIndex: fi}

	findings := []ControlFinding{
		{ControlID: "1.1.1.1", Title: "Disable cramfs", Status: "not_satisfied"},
	}
	h.applyConnwaerEscalations("cisubuntu", findings)

	if findings[0].Status != "not_satisfied" {
		t.Errorf("status = %q, want untouched not_satisfied for unmapped framework", findings[0].Status)
	}
}
