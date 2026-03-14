// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

package grpc

import (
	"testing"
)

func TestDeviceVirtualIDDeterministic(t *testing.T) {
	hostname := "core-sw01.example.com"
	id1 := DeviceVirtualID(hostname)
	id2 := DeviceVirtualID(hostname)

	if id1 != id2 {
		t.Errorf("DeviceVirtualID not deterministic: %q != %q", id1, id2)
	}

	// Different hostnames should produce different IDs.
	id3 := DeviceVirtualID("other-sw.example.com")
	if id1 == id3 {
		t.Errorf("different hostnames produced same ID: %q", id1)
	}

	// Should be a valid UUID format.
	if len(id1) != 36 {
		t.Errorf("DeviceVirtualID length = %d, want 36 (UUID format)", len(id1))
	}
}
