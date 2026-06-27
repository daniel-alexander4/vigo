// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

package api

import (
	"net"
	"strings"
)

// Config-driven hands-off spanner peering — source-address allowlist helpers
// (ADR-026). The handler that uses them (handlePeerAdmit) lives in
// spanner_admission.go alongside its route registration; these are the
// utility funcs it leans on. See server/spanner/peering.go for the joiner
// side and the security rationale (founder-routed admission + off-wire CA
// bound a rogue that slips the IP-only gate).

// sourceAddrAllowed reports whether remoteAddr's IP resolves to one of
// the configured peer addresses. The match rule is pinned to
// resolve-and-compare: each peer entry's host is resolved to its IP set
// (literal IPs pass through unchanged; hostnames go through the
// resolver) and the request's source IP must equal one of them. This
// handles both literal-IP and DNS-name peer entries; it is the
// deliberately-weak admission gate whose acceptability rests on the
// off-wire fleet CA (see server/spanner/peering.go).
func sourceAddrAllowed(remoteAddr string, peers []string) bool {
	host, _, err := net.SplitHostPort(remoteAddr)
	if err != nil {
		host = remoteAddr
	}
	srcIP := net.ParseIP(host)
	if srcIP == nil {
		return false
	}
	for _, peer := range peers {
		for _, ip := range resolvePeerIPs(peer) {
			if ip.Equal(srcIP) {
				return true
			}
		}
	}
	return false
}

// resolvePeerIPs returns the IP set a configured peer entry resolves to.
// A literal IP returns itself; a hostname goes through net.LookupHost.
// Resolution failures yield no IPs (the peer simply does not match).
func resolvePeerIPs(peer string) []net.IP {
	h := peerHostOnly(peer)
	if ip := net.ParseIP(h); ip != nil {
		return []net.IP{ip}
	}
	addrs, err := net.LookupHost(h)
	if err != nil {
		return nil
	}
	out := make([]net.IP, 0, len(addrs))
	for _, a := range addrs {
		if ip := net.ParseIP(a); ip != nil {
			out = append(out, ip)
		}
	}
	return out
}

// peerHostOnly strips any scheme and port from a configured peer entry,
// returning just the host (IP or DNS name).
func peerHostOnly(peer string) string {
	p := strings.TrimSpace(peer)
	p = strings.TrimPrefix(p, "https://")
	p = strings.TrimPrefix(p, "http://")
	p = strings.TrimRight(p, "/")
	if host, _, err := net.SplitHostPort(p); err == nil {
		return host
	}
	return p
}
