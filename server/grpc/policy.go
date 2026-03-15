// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

package grpc

import (
	"encoding/json"
	"fmt"

	pb "github.com/hobnob/hobnob/gen/proto/hobnob"
	"github.com/hobnob/hobnob/server/config"
)

// buildPolicyBundle converts a resolved ThrallConfig into a PolicyBundle proto.
// Secrets are already resolved in the config (at load time). Vars are merged.
// Template strings and when expressions are left raw — the agent evaluates
// them locally with its own traits.
//
// If cachedHashes is non-nil, modules whose content hash matches the agent's
// cached version are sent as stubs (name + hash only, no resources), saving
// bandwidth when only a subset of modules changed.
func buildPolicyBundle(cfg *config.ThrallConfig, version uint64, cachedHashes map[string]string, traits map[string]any) (*pb.PolicyBundle, error) {
	bundle := &pb.PolicyBundle{
		PolicyVersion:  version,
		MatchedPattern: cfg.MatchPattern,
		Environment:    cfg.Environment,
		Roles:          cfg.Roles,
	}

	// Resolve match vars (trait-conditional string vars) into the vars map
	// before serialization. These use when expressions evaluated server-side
	// against the thrall's traits.
	if len(cfg.MatchVars) > 0 && traits != nil {
		config.ResolveMatchVars(cfg.Vars, &cfg.VarOrder, cfg.MatchVars, cfg.MatchVarDefaults, traits)
	}

	// Serialize vars as JSON (preserves types: int, string, bool, etc.).
	if len(cfg.Vars) > 0 {
		varsJSON, err := json.Marshal(cfg.Vars)
		if err != nil {
			return nil, fmt.Errorf("marshaling vars: %w", err)
		}
		bundle.VarsJson = varsJSON
	}

	bundle.VarOrder = cfg.VarOrder

	// Convert conditional vars to proto (OperandJSON pre-serialized at config load).
	for _, cv := range cfg.ConditionalVars {
		bundle.ConditionalVars = append(bundle.ConditionalVars, &pb.ConditionalVarDef{
			Name:        cv.Name,
			Trait:        cv.Trait,
			Operator:    cv.Operator,
			OperandJson: cv.OperandJSON,
		})
	}

	// Convert modules to proto. Content hashes are pre-computed at config
	// load time (ModuleConfig.ContentHash) to avoid per-check-in SHA256.
	for _, mod := range cfg.Modules {
		pbMod := buildPolicyModule(mod)
		pbMod.ContentHash = mod.ContentHash

		// If agent has this module cached with the same hash, send a stub.
		if cachedHashes != nil {
			if agentHash, ok := cachedHashes[mod.Name]; ok && agentHash == mod.ContentHash {
				pbMod.Stub = true
				pbMod.Resources = nil
			}
		}

		bundle.Modules = append(bundle.Modules, pbMod)
	}

	return bundle, nil
}

// buildPolicyModule converts a config ModuleConfig to a PolicyModule proto.
func buildPolicyModule(mod config.ModuleConfig) *pb.PolicyModule {
	pbMod := &pb.PolicyModule{
		Name:      mod.Name,
		DependsOn: mod.DependsOn,
		When:      mod.When,
	}

	for _, res := range mod.Resources {
		attrs := make(map[string]string, len(res.Attributes))
		for k, v := range res.Attributes {
			switch val := v.(type) {
			case []interface{}:
				jsonBytes, err := json.Marshal(val)
				if err != nil {
					attrs[k] = fmt.Sprintf("%v", v)
				} else {
					attrs[k] = string(jsonBytes)
				}
			default:
				attrs[k] = fmt.Sprintf("%v", v)
			}
		}

		pbRes := &pb.PolicyResource{
			Name:             res.Name,
			Type:             res.Type,
			Attributes:       attrs,
			When:             res.When,
			Notify:           res.Notify,
			Subscribes:       res.Subscribes,
			WatchSecret: res.WatchSecret,
		}
		pbMod.Resources = append(pbMod.Resources, pbRes)
	}

	return pbMod
}

// injectSecretTriggers sets _secret_triggered=true on resources whose
// watch_secret list intersects the rotated paths. Since modules may be
// shared from the policy cache, affected resources are cloned before mutation.
func injectSecretTriggers(bundle *pb.PolicyBundle, rotatedPaths []string) {
	rotatedSet := make(map[string]bool, len(rotatedPaths))
	for _, p := range rotatedPaths {
		rotatedSet[p] = true
	}
	// Copy the modules slice so writes don't corrupt the shared policy cache.
	modules := make([]*pb.PolicyModule, len(bundle.Modules))
	copy(modules, bundle.Modules)
	bundle.Modules = modules

	for mi, mod := range bundle.Modules {
		if mod.Stub {
			continue
		}
		for ri, res := range mod.Resources {
			for _, sp := range res.WatchSecret {
				if rotatedSet[sp] {
					// Clone module + resource so the cache entry stays clean.
					clonedMod := *mod
					clonedMod.Resources = make([]*pb.PolicyResource, len(mod.Resources))
					copy(clonedMod.Resources, mod.Resources)

					clonedRes := *res
					clonedRes.Attributes = make(map[string]string, len(res.Attributes)+1)
					for k, v := range res.Attributes {
						clonedRes.Attributes[k] = v
					}
					clonedRes.Attributes["_secret_triggered"] = "true"

					clonedMod.Resources[ri] = &clonedRes
					bundle.Modules[mi] = &clonedMod
					break
				}
			}
		}
	}
}

// hashPolicyModule computes a content hash from a PolicyModule proto.
// Delegates to config.HashModuleContent after converting to ModuleConfig.
// Kept for backward compatibility with tests; production code uses
// pre-computed ModuleConfig.ContentHash instead.
func hashPolicyModule(mod *pb.PolicyModule) string {
	cfg := config.ModuleConfig{
		When:      mod.When,
		DependsOn: mod.DependsOn,
	}
	for _, res := range mod.Resources {
		attrs := make(map[string]any, len(res.Attributes))
		for k, v := range res.Attributes {
			attrs[k] = v
		}
		cfg.Resources = append(cfg.Resources, config.ResourceConfig{
			Name:             res.Name,
			Type:             res.Type,
			When:             res.When,
			Attributes:       attrs,
			Notify:           res.Notify,
			Subscribes:       res.Subscribes,
			WatchSecret: res.WatchSecret,
		})
	}
	return config.HashModuleContent(cfg)
}
