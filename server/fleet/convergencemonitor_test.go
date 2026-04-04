// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

package fleet

import (
	"testing"
)

func TestConvergenceMonitor_ThresholdBreach(t *testing.T) {
	fi := NewFleetIndex()
	fi.AddEnvoy(EnvoyState{ID: "t1", Hostname: "h1", ConvergenceStatus: "converged"}, nil)
	fi.AddEnvoy(EnvoyState{ID: "t2", Hostname: "h2", ConvergenceStatus: "failed"}, nil)
	fi.AddEnvoy(EnvoyState{ID: "t3", Hostname: "h3", ConvergenceStatus: "failed"}, nil)

	// 33% compliant, threshold at 90%
	monitor := NewConvergenceMonitor(fi, 90.0, nil)
	monitor.Check()

	if !monitor.alertActive {
		t.Error("expected alert to be active when convergence is below threshold")
	}

	// Second check should not fire again (already active)
	monitor.Check()
	if !monitor.alertActive {
		t.Error("alert should still be active")
	}
}

func TestConvergenceMonitor_Recovery(t *testing.T) {
	fi := NewFleetIndex()
	fi.AddEnvoy(EnvoyState{ID: "t1", Hostname: "h1", ConvergenceStatus: "failed"}, nil)

	monitor := NewConvergenceMonitor(fi, 90.0, nil)
	monitor.Check()

	if !monitor.alertActive {
		t.Error("alert should be active")
	}

	// Fix convergence
	fi.UpdateRunStatus("t1", "run-fix", "success", 0, fi.envoys["t1"].LastSeen)

	monitor.Check()
	if monitor.alertActive {
		t.Error("alert should be cleared after recovery")
	}
}

func TestConvergenceMonitor_AboveThreshold(t *testing.T) {
	fi := NewFleetIndex()
	fi.AddEnvoy(EnvoyState{ID: "t1", Hostname: "h1", ConvergenceStatus: "converged"}, nil)

	monitor := NewConvergenceMonitor(fi, 90.0, nil)
	monitor.Check()

	if monitor.alertActive {
		t.Error("alert should not be active when convergence is above threshold")
	}
}
