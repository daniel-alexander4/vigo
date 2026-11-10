// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: AGPL-3.0-only

package api

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"git.alexander4.io/vigo/server/config"
	"git.alexander4.io/vigo/server/db"
	"git.alexander4.io/vigo/server/fleet"
	"git.alexander4.io/vigo/server/secrets"
)

// perControlFixture builds a two-envoy fleet scored for PCI DSS: a.host
// carries a configcrate claiming 10.6.1, b.host does not, and 8.2.1 is
// waived fleet-wide.
func perControlFixture(t *testing.T) *ReportHandler {
	t.Helper()
	dir := t.TempDir()
	write := func(rel, body string) {
		p := filepath.Join(dir, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("roles/.keep", "")
	write("configcrates/timesync/chrony.vgo", "name: chrony\nresources:\n  - name: noop\n    type: exec\n    command: \"true\"\n")
	write("configcrates/timesync/compliance.vgo", "compliance:\n  pci-dss: [\"10.6.1\"]\n")
	write("configcrates/base/base.vgo", "name: base\nresources:\n  - name: noop\n    type: exec\n    command: \"true\"\n")
	write("envoys/envoys.vgo", "envoys:\n  - match: \"a.host\"\n    configcrates: [chrony]\n  - match: \"b.host\"\n    configcrates: [base]\n")
	write("waivers.vgo", "waivers:\n  pci-dss:\n    - control: \"8.2.1\"\n      reason: \"test\"\n      approved_by: \"dan\"\n")

	provider, err := secrets.NewLocalProvider("", t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { provider.Close() })
	store, err := config.NewConfigStore(dir, provider, true)
	if err != nil {
		t.Fatalf("NewConfigStore: %v", err)
	}

	fi := fleet.NewFleetIndex()
	fi.AddEnvoy(fleet.EnvoyState{ID: "a", Hostname: "a.host", ConvergenceStatus: "converged", LastSeen: time.Now()}, nil)
	fi.AddEnvoy(fleet.EnvoyState{ID: "b", Hostname: "b.host", ConvergenceStatus: "converged", LastSeen: time.Now()}, nil)
	fi.AddEnvoy(fleet.EnvoyState{ID: "r", Hostname: "a.host", Revoked: true}, nil)
	return NewReportHandler(db.New(openMigratedTestDB(t)), fi, store, "test")
}

// TestFrameworkScore_PerControlMatchesPartition pins PerControl to the
// per-envoy partition rule: each control's buckets sum to the active fleet,
// a control covered on one envoy shows Covered=1/Missing=1, a waived control
// shows Waived=2, and the per-control tallies sum to the per-envoy tallies.
func TestFrameworkScore_PerControlMatchesPartition(t *testing.T) {
	h := perControlFixture(t)
	score, ok := h.ComputeFrameworkScoreCtx(context.Background(), "pci")
	if !ok {
		t.Fatal("pci not scored")
	}
	if score.Fleet.ActiveEnvoys != 2 {
		t.Fatalf("ActiveEnvoys = %d, want 2", score.Fleet.ActiveEnvoys)
	}
	if got := score.PerControl["10.6.1"]; got != (ControlFleetPartition{Covered: 1, Missing: 1}) {
		t.Errorf("10.6.1 = %+v, want covered 1 / missing 1", got)
	}
	if got := score.PerControl["8.2.1"]; got != (ControlFleetPartition{Waived: 2}) {
		t.Errorf("8.2.1 = %+v, want waived 2", got)
	}
	var ctl, env ControlFleetPartition
	for _, f := range score.Controls {
		pc := score.PerControl[f.ControlID]
		if n := pc.Covered + pc.Waived + pc.Partial + pc.Missing; n != 2 {
			t.Errorf("%s buckets sum to %d, want 2", f.ControlID, n)
		}
		ctl.Covered += pc.Covered
		ctl.Waived += pc.Waived
		ctl.Partial += pc.Partial
		ctl.Missing += pc.Missing
	}
	for _, st := range score.PerEnvoy {
		env.Covered += st.Covered
		env.Waived += st.Waived
		env.Partial += st.Partial
		env.Missing += st.Missing
	}
	if ctl != env {
		t.Errorf("per-control tallies %+v disagree with per-envoy tallies %+v", ctl, env)
	}
}
