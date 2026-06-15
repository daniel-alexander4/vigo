// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

package config

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"sort"
)

// MerkleRoot computes a Merkle tree root hash over an envoy's resolved config.
// The tree structure is:
//
//	    root
//	   /    \
//	vars   modules
//	       / | \
//	    mod1 mod2 mod3
//
// Leaves are the per-module content hashes (already computed by HashModuleContent)
// plus a vars leaf (SHA256 of the deterministic vars JSON). The root is a single
// hash representing the entire envoy config — if it matches the agent's cached
// root, the config hasn't changed and the server can skip bundle construction.
func MerkleRoot(configcrates []Configcrate, vars map[string]any, varOrder []string) string {
	varsHash := hashVars(vars, varOrder)
	configcratesHash := hashConfigcrates(configcrates)
	return combinePair(varsHash, configcratesHash)
}

// hashVars produces a deterministic SHA256 of the vars map using the
// canonical key order. If vars is nil/empty, returns a fixed empty hash.
func hashVars(vars map[string]any, varOrder []string) string {
	if len(vars) == 0 {
		return emptyHash()
	}

	// Build ordered map for deterministic JSON output.
	ordered := make([]kv, 0, len(vars))

	// Use varOrder for keys that appear in it, then append any remaining
	// keys in sorted order (belt and suspenders for determinism).
	seen := make(map[string]bool, len(varOrder))
	for _, k := range varOrder {
		if v, ok := vars[k]; ok {
			ordered = append(ordered, kv{k, v})
			seen[k] = true
		}
	}
	remaining := make([]string, 0)
	for k := range vars {
		if !seen[k] {
			remaining = append(remaining, k)
		}
	}
	sort.Strings(remaining)
	for _, k := range remaining {
		ordered = append(ordered, kv{k, vars[k]})
	}

	data, err := json.Marshal(ordered)
	if err != nil {
		// Vars are map[string]any from YAML — always serializable.
		return emptyHash()
	}
	h := sha256.Sum256(data)
	return hex.EncodeToString(h[:])
}

type kv struct {
	K string `json:"k"`
	V any    `json:"v"`
}

// hashModules builds a binary Merkle tree from module content hashes.
// Configcrates are processed in their DAG order (as stored in the slice).
func hashConfigcrates(configcrates []Configcrate) string {
	if len(configcrates) == 0 {
		return emptyHash()
	}

	leaves := make([]string, len(configcrates))
	for i, mod := range configcrates {
		if mod.ContentHash != "" {
			leaves[i] = mod.ContentHash
		} else {
			leaves[i] = emptyHash()
		}
	}

	// Build tree bottom-up: pair adjacent hashes until one remains.
	level := leaves
	for len(level) > 1 {
		next := make([]string, 0, (len(level)+1)/2)
		for i := 0; i < len(level); i += 2 {
			if i+1 < len(level) {
				next = append(next, combinePair(level[i], level[i+1]))
			} else {
				// Odd node: promote without pairing.
				next = append(next, level[i])
			}
		}
		level = next
	}
	return level[0]
}

// combinePair computes SHA256(left || right) for two hex-encoded hashes.
func combinePair(left, right string) string {
	h := sha256.New()
	h.Write([]byte(left))
	h.Write([]byte(right))
	return hex.EncodeToString(h.Sum(nil))
}

// emptyHash returns SHA256 of the empty string.
func emptyHash() string {
	h := sha256.Sum256(nil)
	return hex.EncodeToString(h[:])
}
