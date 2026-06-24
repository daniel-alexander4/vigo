// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

package grpc

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"log/slog"

	"git.alexander4.io/vigo/server/fleet"
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

// ensureInventory makes the server's cached inventory current for a split
// check-in and reports whether it now holds inventory matching inventoryHash.
// inventoryJSON is non-empty only when the inventory changed (the agent then
// ships it): it is validated + the os/arch derived ONCE and cached. On a
// vitals-only tick the cached copy is checked against inventoryHash.
//
// Returns ready=false when the server holds no valid inventory matching the
// hash (server restart, cache eviction, desync, or a malformed inventory) — the
// caller then skips the trait persist and sets resend_inventory so the agent
// ships the full inventory next cycle. This is the cheap half of the old
// reconstructTraits: it never performs the ~155 KB merge, so it can run every
// tick to drive the resend decision (ADR-033 Option C). The merge itself is
// deferred to mergeCachedTraits, called lazily only when the persist key moves.
func (s *AgentService) ensureInventory(envoyID string, inventoryJSON []byte, inventoryHash string) (ready bool) {
	if len(inventoryJSON) > 0 {
		// Inventory changed — validate + derive os/arch ONCE here (not every
		// tick), then cache (copy; the proto buffer is reused). A malformed
		// inventory is not cached; ask the agent for a clean resend so the
		// steady-state path never serves/stores invalid traits.
		goos, goarch, ok := deriveOSArch(inventoryJSON)
		if !ok {
			slog.Warn("malformed inventory JSON on check-in; requesting resend", "envoy_id", envoyID)
			return false
		}
		s.inventoryCache.Store(envoyID, &inventoryEntry{
			hash:   inventoryHash,
			json:   append([]byte(nil), inventoryJSON...),
			goos:   goos,
			goarch: goarch,
		})
		return true
	}
	// Vitals-only — the cached inventory is usable iff its hash matches.
	if v, ok := s.inventoryCache.Load(envoyID); ok {
		return v.(*inventoryEntry).hash == inventoryHash
	}
	return false
}

// mergeCachedTraits rebuilds the full traits JSON = cached inventory ∪ fresh
// vitals. Called lazily inside the traitsBatch.Mark build closure — only when
// the persist change-key actually moved — so the ~155 KB byte-concat never runs
// on a churny-only tick (ADR-033 Option C). Returns nil if the inventory cache
// was lost between ensureInventory and here (Mark then drops the empty blob and
// retries next tick); the caller must have confirmed readiness via
// ensureInventory first.
func (s *AgentService) mergeCachedTraits(envoyID string, vitalsJSON []byte) []byte {
	if v, ok := s.inventoryCache.Load(envoyID); ok {
		return mergeTraitsObjects(v.(*inventoryEntry).json, vitalsJSON)
	}
	return nil
}

// fullCheckinTraits returns the complete per-envoy traits for one check-in:
// cached inventory ∪ vitals for a split agent (nil when the server doesn't hold
// the inventory — a resend tick), or the legacy full payload as-is. Shared by
// the trait persist (lazily, inside Mark's build closure, only when the change-
// key moved) and the per-envoy bundle expansion (case/lookups, only when a
// bundle is rebuilt). For a split agent each caller re-merges, but they fire
// together only on a tick that both rebuilds the bundle AND changed the persist
// key (rare²), so the steady-state hot path merges at most once.
func (s *AgentService) fullCheckinTraits(envoyID string, vitals, legacyTraits []byte, splitVitals bool) []byte {
	if splitVitals {
		return s.mergeCachedTraits(envoyID, vitals)
	}
	return legacyTraits
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

// traitsChangeKey is a cheap fingerprint of an envoy's full merged traits, fed to
// traitsBatch.Mark so the steady-state check-in path never hashes the full
// ~155 KB merged blob (ADR-033) and persists/re-flattens only when MEANINGFUL
// content changes (ADR-033 Option C). For a split agent it is inventory_hash
// (identifies the stable inventory the server holds) + the agent's
// vitals_state_hash — a hash over only the operational vitals' meaningful
// content, so churny telemetry (loadavg, rss, download progress, updated_at)
// no longer flips the key and the merge/persist/re-flatten is skipped on those
// ticks. A split agent that predates vitals_state_hash (mid rolling-upgrade)
// sends it empty: fall back to hashing the whole vitals blob — correct, just
// persists every tick (no savings) until it upgrades. A legacy agent (no
// vitals) hashes the full traits_json. Used on a tick where the cached inventory
// matches the agent's inventory_hash, so the key faithfully identifies the
// merged inventory portion.
func traitsChangeKey(vitalsStateHash string, vitalsJSON, traitsJSON []byte, inventoryHash string) string {
	if len(vitalsJSON) > 0 {
		if vitalsStateHash != "" {
			return inventoryHash + ":" + vitalsStateHash
		}
		h := sha256.Sum256(vitalsJSON)
		return inventoryHash + ":" + hex.EncodeToString(h[:])
	}
	h := sha256.Sum256(traitsJSON)
	return hex.EncodeToString(h[:])
}

// liveVitalsCollectors are the volatile collectors whose flattened fields the
// fleetpressure agent-health surface reads — agent_self/host_self (FD counts,
// OOM-recent), mountpoints (rootfs/inode %), battery (wear health), and uptime.
// Under ADR-033 Option C the full traits map is re-persisted only on meaningful
// change, so these churny health fields are extracted from every check-in's
// vitals into the FleetIndex liveVitals overlay (see fleet.EnvoyState.liveVitals)
// to keep the health card current without a full re-flatten.
var liveVitalsCollectors = []string{"agent_self", "host_self", "mountpoints", "battery", "uptime"}

// extractLiveVitals flattens the operational-health sub-trees out of the parsed
// volatile blob into the flat key→value shape fleetpressure consumes. Returns
// nil when none are present (legacy/minimal agents), which SetLiveVitals treats
// as a no-op.
func extractLiveVitals(volTop map[string]json.RawMessage) map[string]string {
	sub := make(map[string]any, len(liveVitalsCollectors))
	for _, k := range liveVitalsCollectors {
		raw, ok := volTop[k]
		if !ok {
			continue
		}
		var v any
		if json.Unmarshal(raw, &v) == nil {
			sub[k] = v
		}
	}
	if len(sub) == 0 {
		return nil
	}
	flat := make(map[string]string)
	fleet.FlattenJSON("", sub, flat)
	return flat
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
