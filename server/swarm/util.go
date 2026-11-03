// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: Apache-2.0

// Package swarm provides peer tracking and seed management for
// peer-to-peer content distribution. Agents serve and download chunks
// from each other over mTLS HTTPS on port 1531.
package swarm

// ChunkSize is the standard chunk size for swarm blob distribution (1 MiB).
const ChunkSize = 1 << 20 // 1048576

// IsValidHash checks that a string looks like a hex-encoded SHA256 hash.
func IsValidHash(s string) bool {
	if len(s) != 64 {
		return false
	}
	return isHexString(s)
}

// IsValidHashPrefix checks that a string is a valid hex prefix (at least 6 chars).
func IsValidHashPrefix(s string) bool {
	return len(s) >= 6 && isHexString(s)
}

func isHexString(s string) bool {
	for _, c := range s {
		if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f')) {
			return false
		}
	}
	return true
}
