// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: BUSL-1.1

package fleet

import (
	"testing"
)

func TestComplianceMonitor_ThresholdBreach(t *testing.T) {
	fi := NewFleetIndex()
	fi.AddThrall(ThrallState{ID: "t1", Hostname: "h1", ComplianceStatus: "compliant"}, nil)
	fi.AddThrall(ThrallState{ID: "t2", Hostname: "h2", ComplianceStatus: "failing"}, nil)
	fi.AddThrall(ThrallState{ID: "t3", Hostname: "h3", ComplianceStatus: "failing"}, nil)

	// 33% compliant, threshold at 90%
	monitor := NewComplianceMonitor(fi, 90.0, nil, nil)
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

func TestComplianceMonitor_Recovery(t *testing.T) {
	fi := NewFleetIndex()
	fi.AddThrall(ThrallState{ID: "t1", Hostname: "h1", ComplianceStatus: "failing"}, nil)

	monitor := NewComplianceMonitor(fi, 90.0, nil, nil)
	monitor.Check()

	if !monitor.alertActive {
		t.Error("alert should be active")
	}

	// Fix compliance
	fi.UpdateRunStatus("t1", "success", 0, fi.thralls["t1"].LastSeen)

	monitor.Check()
	if monitor.alertActive {
		t.Error("alert should be cleared after recovery")
	}
}

func TestComplianceMonitor_AboveThreshold(t *testing.T) {
	fi := NewFleetIndex()
	fi.AddThrall(ThrallState{ID: "t1", Hostname: "h1", ComplianceStatus: "compliant"}, nil)

	monitor := NewComplianceMonitor(fi, 90.0, nil, nil)
	monitor.Check()

	if monitor.alertActive {
		t.Error("alert should not be active when compliance is above threshold")
	}
}
