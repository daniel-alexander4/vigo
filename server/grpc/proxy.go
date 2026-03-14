// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: BUSL-1.1

package grpc

import (
	"github.com/google/uuid"
	"github.com/hobnob/hobnob/server/config"
)

// deviceNamespace is a fixed UUID v5 namespace for generating deterministic
// virtual thrall IDs from device hostnames. This ensures the same device
// always gets the same UUID across server restarts and config reloads.
var deviceNamespace = uuid.MustParse("a3e4c8f1-7b2d-4e5a-9c6d-1f8e2b3a4d5c")

// DeviceVirtualID generates a stable UUID for a network device based on its
// hostname. Uses UUID v5 (SHA-1) so the ID is deterministic and reproducible.
func DeviceVirtualID(hostname string) string {
	return uuid.NewSHA1(deviceNamespace, []byte(hostname)).String()
}

// isAuthorizedGateway checks whether gatewayHostname is the configured gateway
// for the given device thrall_id. Uses ConfigStore to resolve the device entry
// and verify the gateway relationship.
func isAuthorizedGateway(cs *config.ConfigStore, deviceThrallID, gatewayHostname string) bool {
	// Iterate all device entries and check if the virtual ID matches.
	// This is only called when DeviceType != "" in FleetIndex, so it's
	// not on the hot path.
	for _, entry := range cs.AllDeviceEntries() {
		if DeviceVirtualID(entry.Hostname) == deviceThrallID {
			return entry.Gateway == gatewayHostname
		}
	}
	return false
}
