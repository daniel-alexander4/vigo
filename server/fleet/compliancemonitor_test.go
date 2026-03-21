// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

package fleet

import (
	"testing"
)

func TestConvergenceMonitor_ThresholdBreach(t *testing.T) {
	fi := NewFleetIndex()
	fi.AddEnvoy(EnvoyState{ID: "t1", Hostname: "h1", ConvergenceStatus: "converged"}, nil)
	fi.AddEnvoy(EnvoyState{ID: "t2", Hostname: "h2", ConvergenceStatus: "errors"}, nil)
	fi.AddEnvoy(EnvoyState{ID: "t3", Hostname: "h3", ConvergenceStatus: "errors"}, nil)

	// 33% compliant, threshold at 90%
	monitor := NewConvergenceMonitor(fi, 90.0, nil, nil)
	monitor.Check()

	if !monitor.alertActive {
		t.Error("expected alert to be active when compliance is below threshold")
	}

	// Second check should not fire again (already active)
	monitor.Check()
	if !monitor.alertActive {
		t.Error("alert should still be active")
	}
}

func TestConvergenceMonitor_Recovery(t *testing.T) {
	fi := NewFleetIndex()
	fi.AddEnvoy(EnvoyState{ID: "t1", Hostname: "h1", ConvergenceStatus: "errors"}, nil)

	monitor := NewConvergenceMonitor(fi, 90.0, nil, nil)
	monitor.Check()

	if !monitor.alertActive {
		t.Error("alert should be active")
	}

	// Fix compliance
	fi.UpdateRunStatus("t1", "success", 0, fi.envoys["t1"].LastSeen)

	monitor.Check()
	if monitor.alertActive {
		t.Error("alert should be cleared after recovery")
	}
}

func TestConvergenceMonitor_AboveThreshold(t *testing.T) {
	fi := NewFleetIndex()
	fi.AddEnvoy(EnvoyState{ID: "t1", Hostname: "h1", ConvergenceStatus: "converged"}, nil)

	monitor := NewConvergenceMonitor(fi, 90.0, nil, nil)
	monitor.Check()

	if monitor.alertActive {
		t.Error("alert should not be active when compliance is above threshold")
	}
}
