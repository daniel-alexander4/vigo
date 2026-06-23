// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

package grpc

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"log/slog"
)

// ADR-033 conditional traits transmission (server side).
//
// A split agent leaves CheckInRequest.traits_json empty and instead sends:
//   - vitals_json     — the volatile collectors, every check-in (~5 KB)
//   - inventory_json  — the stable+periodic collectors, ONLY when they change
//   - inventory_hash  — the hash of the inventory the agent currently holds
//
// The server caches the last full inventory per envoy and reconstructs the full
// traits = inventory ∪ vitals, then runs the unchanged CheckIn processing path.
// Older agents that still send the full payload in traits_json are untouched.

// inventoryEntry is the per-envoy cached inventory backing reconstruction.
// goos/goarch are derived once from the inventory's os sub-tree when it's cached
// (on change), so the steady-state check-in path resolves the agent's platform
// for auto-update without re-parsing the ~213 KB inventory every tick.
type inventoryEntry struct {
	hash   string
	json   []byte
	goos   string
	goarch string
}

// reconstructTraits rebuilds the full traits JSON for a split check-in from the
// agent's fresh vitals plus the server-cached inventory. inventoryJSON is
// non-empty only when the inventory changed (the agent then ships it); otherwise
// the cached copy is used, gated on inventoryHash matching what was cached.
//
// Returns (full, resend). resend is true when the server holds no inventory
// matching inventoryHash (server restart, cache eviction, or desync) — full is
// then nil, this check-in skips trait processing, and the CheckInResponse carries
// resend_inventory=true so the agent ships the full inventory next cycle.
func (s *AgentService) reconstructTraits(envoyID string, vitalsJSON, inventoryJSON []byte, inventoryHash string) (full []byte, resend bool) {
	if len(inventoryJSON) > 0 {
		// Inventory changed — validate + derive os/arch ONCE here (not every
		// tick), then cache (copy; the proto buffer is reused) and merge. A
		// malformed inventory is not cached; ask the agent for a clean resend so
		// the steady-state path never serves/stores invalid traits.
		goos, goarch, ok := deriveOSArch(inventoryJSON)
		if !ok {
			slog.Warn("malformed inventory JSON on check-in; requesting resend", "envoy_id", envoyID)
			return nil, true
		}
		s.inventoryCache.Store(envoyID, &inventoryEntry{
			hash:   inventoryHash,
			json:   append([]byte(nil), inventoryJSON...),
			goos:   goos,
			goarch: goarch,
		})
		return mergeTraitsObjects(inventoryJSON, vitalsJSON), false
	}
	// Vitals-only — use the cached inventory iff its hash matches.
	if v, ok := s.inventoryCache.Load(envoyID); ok {
		if e := v.(*inventoryEntry); e.hash == inventoryHash {
			return mergeTraitsObjects(e.json, vitalsJSON), false
		}
	}
	// Cache miss or hash mismatch — ask the agent to resend; skip traits this cycle.
	return nil, true
}

// mergeTraitsObjects concatenates two compact JSON objects into one. The agent
// serializes traits with serde_json (compact, no trailing whitespace) and the
// inventory and vitals key sets are disjoint (distinct collectors), so a
// byte-level join is correct and avoids re-parsing the ~213 KB inventory:
//
//	{inv...} + {vit...}  ->  {inv...,vit...}
func mergeTraitsObjects(inventory, vitals []byte) []byte {
	inv := bytes.TrimSpace(inventory)
	vit := bytes.TrimSpace(vitals)
	invEmpty := len(inv) <= 2 // "" or "{}"
	vitEmpty := len(vit) <= 2
	switch {
	case invEmpty && vitEmpty:
		return []byte("{}")
	case invEmpty:
		return vit
	case vitEmpty:
		return inv
	default:
		out := make([]byte, 0, len(inv)+len(vit))
		out = append(out, inv[:len(inv)-1]...) // drop trailing '}'
		out = append(out, ',')
		out = append(out, vit[1:]...) // drop leading '{'
		return out
	}
}

// deriveOSArch shallow-parses an inventory blob — which validates it — and
// resolves the agent's goos/goarch from its os sub-tree, the only
// inventory-derived value the steady-state check-in path needs. Called once when
// the inventory is cached (on change), so vitals-only ticks never re-parse it.
// Returns ok=false when the inventory is not valid JSON.
func deriveOSArch(inventoryJSON []byte) (goos, goarch string, ok bool) {
	var top map[string]json.RawMessage
	if json.Unmarshal(inventoryJSON, &top) != nil {
		return "", "", false
	}
	goos, goarch = goosGoarchFromTraits(top["os"])
	return goos, goarch, true
}

// traitsChangeKey is a cheap fingerprint of an envoy's full merged traits that
// changes iff the traits change — fed to traitsBatch.Mark so the steady-state
// check-in path never hashes the full ~155 KB merged blob (ADR-033). For a split
// agent it is inventory_hash (identifies the stable inventory the server holds)
// + a hash of the small vitals blob (identifies the volatile part); for a legacy
// agent (no vitals) it falls back to hashing the full traits_json. Used on a tick
// where the cached inventory matches the agent's inventory_hash, so the hash
// faithfully identifies the merged inventory portion.
func traitsChangeKey(vitalsJSON, traitsJSON []byte, inventoryHash string) string {
	if len(vitalsJSON) > 0 {
		h := sha256.Sum256(vitalsJSON)
		return inventoryHash + ":" + hex.EncodeToString(h[:])
	}
	h := sha256.Sum256(traitsJSON)
	return hex.EncodeToString(h[:])
}

// cachedOSArch returns the goos/goarch derived from the envoy's cached inventory,
// or empty strings when no inventory is cached (a legacy traits_json agent, or a
// pre-resend cache miss). Lets the auto-update check skip re-parsing the
// inventory on every check-in.
func (s *AgentService) cachedOSArch(envoyID string) (goos, goarch string) {
	if v, ok := s.inventoryCache.Load(envoyID); ok {
		e := v.(*inventoryEntry)
		return e.goos, e.goarch
	}
	return "", ""
}
