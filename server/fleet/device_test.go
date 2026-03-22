// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

package fleet

import (
	"testing"
)

func TestFleetIndexDeviceType(t *testing.T) {
	fi := NewFleetIndex()

	// Add a normal gateway envoy.
	fi.AddEnvoy(EnvoyState{
		ID:               "gw-1",
		Hostname:         "netmgmt01.example.com",
		ConvergenceStatus: "converged",
	}, genKey(t))

	// Add a device envoy with Gateway and DeviceType.
	fi.AddEnvoy(EnvoyState{
		ID:               "dev-1",
		Hostname:         "core-sw01.example.com",
		ConvergenceStatus: "unknown",
		Gateway:          "netmgmt01.example.com",
		DeviceType:       "cisco_ios",
	}, nil)

	fi.AddEnvoy(EnvoyState{
		ID:               "dev-2",
		Hostname:         "edge-rtr01.example.com",
		ConvergenceStatus: "unknown",
		Gateway:          "netmgmt01.example.com",
		DeviceType:       "juniper_junos",
	}, nil)

	// GetEnvoy should return device fields.
	got := fi.GetEnvoy("dev-1")
	if got == nil {
		t.Fatal("expected device envoy, got nil")
	}
	if got.DeviceType != "cisco_ios" {
		t.Errorf("DeviceType = %q, want cisco_ios", got.DeviceType)
	}
	if got.Gateway != "netmgmt01.example.com" {
		t.Errorf("Gateway = %q, want netmgmt01.example.com", got.Gateway)
	}

	// Device should be included in convergence counters.
	summary := fi.ConvergenceSummary()
	if summary.Total != 3 {
		t.Errorf("ConvergenceSummary.Total = %d, want 3 (1 gateway + 2 devices)", summary.Total)
	}

	// Device should appear in AllEnvoys.
	all := fi.AllEnvoys()
	if len(all) != 3 {
		t.Errorf("AllEnvoys returned %d, want 3", len(all))
	}
}

