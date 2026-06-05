// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

package grpc

import "bytes"

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
type inventoryEntry struct {
	hash string
	json []byte
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
		// Inventory changed — cache it (copy; the proto buffer is reused) and merge.
		s.inventoryCache.Store(envoyID, &inventoryEntry{
			hash: inventoryHash,
			json: append([]byte(nil), inventoryJSON...),
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
