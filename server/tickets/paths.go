// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: Apache-2.0

// Package tickets persists TLS 1.3 session-ticket encryption keys across
// vigosrv restarts. Go's crypto/tls auto-generates ticket keys per
// process and rotates them periodically, so without persistence every
// vigosrv restart invalidates the entire fleet's cached PSK tickets and
// forces a full handshake on the next CheckIn from every envoy — a
// measurable CPU spike at deploy time on top of the steady-state cost.
//
// The on-disk layout mirrors the sibling identity packages
// (server/puddle, server/bolt): a wrapped envelope at
// <dir>/keys.wrapped, a per-host HKDF salt at <dir>/.salt. The wrap key
// is derived from the secrets master key via HKDF purpose
// `vigo-tls-tickets-v1`, distinct from the puddle/bolt purposes so a
// theoretical compromise of one derivation cannot cross the role
// boundary.
package tickets

import "path/filepath"

// DefaultDir is the canonical filesystem location for the ticket-key
// store. Mirrors /srv/vigo/puddle, /srv/vigo/bolt, etc.
const DefaultDir = "/srv/vigo/tls-tickets"

const (
	WrappedFile = "keys.wrapped"
	SaltFile    = ".salt"
)

// WrappedPath returns the absolute path to the wrapped keys file.
func WrappedPath(dir string) string { return filepath.Join(dir, WrappedFile) }

// SaltPath returns the absolute path to the per-host HKDF salt file.
func SaltPath(dir string) string { return filepath.Join(dir, SaltFile) }

// HKDFPurpose binds the wrap-key derivation to the ticket store; a
// theoretical master-key leak that only exposes one HKDF derivation
// cannot cross over to puddle/bolt material.
const HKDFPurpose = "vigo-tls-tickets-v1"
