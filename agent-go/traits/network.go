// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: BUSL-1.1

package traits

import (
	"context"
	"net"
	"os"

	psnet "github.com/shirou/gopsutil/v4/net"
)

// NetworkCollector gathers network traits: hostname, FQDN, IP addresses,
// and MAC addresses.
type NetworkCollector struct{}

// Name returns "network".
func (c *NetworkCollector) Name() string { return "network" }

// Collect returns network traits using os.Hostname and gopsutil net.Interfaces.
func (c *NetworkCollector) Collect(ctx context.Context) (map[string]any, error) {
	hostname, err := os.Hostname()
	if err != nil {
		return nil, err
	}

	// Attempt FQDN lookup with context; fall back to hostname on timeout.
	fqdn := hostname
	resolver := &net.Resolver{}
	addrs, err := resolver.LookupAddr(ctx, hostname)
	if err == nil && len(addrs) > 0 {
		// LookupAddr returns names with trailing dot — trim it.
		name := addrs[0]
		if len(name) > 0 && name[len(name)-1] == '.' {
			name = name[:len(name)-1]
		}
		if name != "" {
			fqdn = name
		}
	}

	ifaces, err := psnet.Interfaces()
	if err != nil {
		return nil, err
	}

	var ipAddresses []string
	var macAddresses []string

	for _, iface := range ifaces {
		// Skip loopback interfaces.
		for _, flag := range iface.Flags {
			if flag == "loopback" {
				goto nextIface
			}
		}

		if iface.HardwareAddr != "" {
			macAddresses = append(macAddresses, iface.HardwareAddr)
		}
		for _, addr := range iface.Addrs {
			// addr.Addr is in CIDR notation (e.g., "10.0.0.1/24"); extract the IP.
			ip, _, err := net.ParseCIDR(addr.Addr)
			if err != nil {
				// Try parsing as plain IP.
				ip = net.ParseIP(addr.Addr)
			}
			if ip != nil && !ip.IsLoopback() {
				ipAddresses = append(ipAddresses, ip.String())
			}
		}

	nextIface:
	}

	// Ensure we return non-nil slices for consistent JSON serialization.
	if ipAddresses == nil {
		ipAddresses = []string{}
	}
	if macAddresses == nil {
		macAddresses = []string{}
	}

	return map[string]any{
		"hostname":      hostname,
		"fqdn":          fqdn,
		"ip_addresses":  ipAddresses,
		"mac_addresses": macAddresses,
	}, nil
}
