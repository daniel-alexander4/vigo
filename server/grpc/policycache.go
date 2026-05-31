// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

package grpc

import (
	"sync"

	pb "git.alexander4.io/vigo/generated.code/proto/vigo"
	"git.alexander4.io/vigo/server/config"
)

// policyCache caches pre-built PolicyBundle protos per match pattern.
// When the config version changes, the entire cache is invalidated.
// This eliminates per-check-in JSON marshaling, module hashing, and
// proto conversion — the hot path becomes a map lookup + stub customization.
type policyCache struct {
	mu      sync.RWMutex
	version uint64
	bundles map[string]*pb.PolicyBundle // match pattern → base bundle (no stubs)
}

func newPolicyCache() *policyCache {
	return &policyCache{
		bundles: make(map[string]*pb.PolicyBundle),
	}
}

// GetOrBuild returns a cached base PolicyBundle for the given pattern,
// building and caching it on first access. Returns nil if cfg is nil.
func (pc *policyCache) GetOrBuild(pattern string, cfg *config.EnvoyConfig, version uint64, streamEdit ...config.StreamEditSection) (*pb.PolicyBundle, error) {
	// Fast path: check cache with read lock.
	pc.mu.RLock()
	if pc.version == version {
		if b, ok := pc.bundles[pattern]; ok {
			pc.mu.RUnlock()
			return b, nil
		}
	}
	pc.mu.RUnlock()

	// Slow path: build bundle and cache it.
	pc.mu.Lock()
	defer pc.mu.Unlock()

	// Invalidate if version changed.
	if pc.version != version {
		pc.bundles = make(map[string]*pb.PolicyBundle)
		pc.version = version
	}

	// Double-check after acquiring write lock.
	if b, ok := pc.bundles[pattern]; ok {
		return b, nil
	}

	// Build the base bundle (all modules full, no stubs).
	bundle, err := buildBasePolicyBundle(cfg, version, streamEdit...)
	if err != nil {
		return nil, err
	}

	pc.bundles[pattern] = bundle
	return bundle, nil
}

// buildBasePolicyBundle creates a PolicyBundle with all modules fully
// populated (no stubs). Module content hashes are taken from the
// pre-computed ContentHash on each Configcrate.
func buildBasePolicyBundle(cfg *config.EnvoyConfig, version uint64, streamEdit ...config.StreamEditSection) (*pb.PolicyBundle, error) {
	bundle, err := buildPolicyBundle(cfg, version, nil, nil, streamEdit...)
	if err != nil {
		return nil, err
	}
	return bundle, nil
}

// customizeStubs creates a lightweight copy of a base PolicyBundle with
// stub modules for any that match the agent's cached hashes. Configcrates
// that don't match are shared (read-only) from the base bundle.
// Always returns a new bundle safe for caller mutation (e.g., appending
// custom traits).
func customizeStubs(base *pb.PolicyBundle, cachedHashes map[string]string) *pb.PolicyBundle {
	bundle := &pb.PolicyBundle{
		PolicyVersion:   base.PolicyVersion,
		MatchedPattern:  base.MatchedPattern,
		Environment:     base.Environment,
		Roles:           base.Roles,
		VarsJson:        base.VarsJson,
		VarOrder:        base.VarOrder,
	}

	if len(cachedHashes) == 0 {
		// No stubs needed — share all modules from base.
		bundle.Configcrates = base.Configcrates
		return bundle
	}

	bundle.Configcrates = make([]*pb.PolicyConfigcrate, len(base.Configcrates))
	for i, mod := range base.Configcrates {
		if agentHash, ok := cachedHashes[mod.Name]; ok && agentHash == mod.ContentHash {
			// Agent has this module cached — send stub.
			bundle.Configcrates[i] = &pb.PolicyConfigcrate{
				Name:        mod.Name,
				DependsOn:   mod.DependsOn,
				When:        mod.When,
				ContentHash: mod.ContentHash,
				Stub:        true,
			}
		} else {
			// Share the full module from the base bundle (read-only).
			bundle.Configcrates[i] = mod
		}
	}

	return bundle
}
