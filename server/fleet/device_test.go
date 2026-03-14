// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: BUSL-1.1

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

	// ListByGateway should return only devices for that gateway.
	devices := fi.ListByGateway("netmgmt01.example.com")
	if len(devices) != 2 {
		t.Fatalf("ListByGateway returned %d, want 2", len(devices))
	}

	// Verify device details.
	found := map[string]bool{}
	for _, d := range devices {
		found[d.Hostname] = true
	}
	if !found["core-sw01.example.com"] {
		t.Error("missing core-sw01.example.com in ListByGateway results")
	}
	if !found["edge-rtr01.example.com"] {
		t.Error("missing edge-rtr01.example.com in ListByGateway results")
	}

	// ListByGateway for non-existent gateway should return empty.
	empty := fi.ListByGateway("nonexistent.example.com")
	if len(empty) != 0 {
		t.Errorf("ListByGateway(nonexistent) returned %d, want 0", len(empty))
	}

	// GetGatewayForDevice should return the gateway hostname.
	gw := fi.GetGatewayForDevice("dev-1")
	if gw != "netmgmt01.example.com" {
		t.Errorf("GetGatewayForDevice(dev-1) = %q, want netmgmt01.example.com", gw)
	}

	// GetGatewayForDevice for normal thrall should return empty.
	gw = fi.GetGatewayForDevice("gw-1")
	if gw != "" {
		t.Errorf("GetGatewayForDevice(gw-1) = %q, want empty", gw)
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

func TestFleetIndexDeviceRevoked(t *testing.T) {
	fi := NewFleetIndex()

	fi.AddThrall(ThrallState{
		ID:               "dev-1",
		Hostname:         "sw01.example.com",
		ComplianceStatus: "unknown",
		Gateway:          "gw.example.com",
		DeviceType:       "cisco_ios",
	}, nil)

	// Revoke the device.
	fi.SetRevoked("dev-1", true)

	// Revoked devices should not appear in ListByGateway.
	devices := fi.ListByGateway("gw.example.com")
	if len(devices) != 0 {
		t.Errorf("ListByGateway returned %d revoked devices, want 0", len(devices))
	}
}
