// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

package fleet

import (
	"testing"
)

func TestFleetIndexDeviceType(t *testing.T) {
	fi := NewFleetIndex()

	// Add a normal gateway thrall.
	fi.AddThrall(ThrallState{
		ID:               "gw-1",
		Hostname:         "netmgmt01.example.com",
		ComplianceStatus: "compliant",
	}, genKey(t))

	// Add a device thrall with Gateway and DeviceType.
	fi.AddThrall(ThrallState{
		ID:               "dev-1",
		Hostname:         "core-sw01.example.com",
		ComplianceStatus: "unknown",
		Gateway:          "netmgmt01.example.com",
		DeviceType:       "cisco_ios",
	}, nil)

	fi.AddThrall(ThrallState{
		ID:               "dev-2",
		Hostname:         "edge-rtr01.example.com",
		ComplianceStatus: "unknown",
		Gateway:          "netmgmt01.example.com",
		DeviceType:       "juniper_junos",
	}, nil)

	// GetThrall should return device fields.
	got := fi.GetThrall("dev-1")
	if got == nil {
		t.Fatal("expected device thrall, got nil")
	}
	if got.DeviceType != "cisco_ios" {
		t.Errorf("DeviceType = %q, want cisco_ios", got.DeviceType)
	}
	if got.Gateway != "netmgmt01.example.com" {
		t.Errorf("Gateway = %q, want netmgmt01.example.com", got.Gateway)
	}

	// Device should be included in compliance counters.
	summary := fi.ComplianceSummary()
	if summary.Total != 3 {
		t.Errorf("ComplianceSummary.Total = %d, want 3 (1 gateway + 2 devices)", summary.Total)
	}

	// Device should appear in AllThralls.
	all := fi.AllThralls()
	if len(all) != 3 {
		t.Errorf("AllThralls returned %d, want 3", len(all))
	}
}

