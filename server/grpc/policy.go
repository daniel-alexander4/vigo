// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

package grpc

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"path/filepath"
	"strings"

	"google.golang.org/protobuf/proto"

	pb "git.alexander4.io/vigo/generated.code/proto/vigo"
	"git.alexander4.io/vigo/server/config"
)

// buildPolicyBundle converts a resolved EnvoyConfig into a PolicyBundle proto.
// Secrets are already resolved in the config (at load time). Vars are merged.
// Template strings and when expressions are left raw — the agent evaluates
// them locally with its own traits.
//
// If cachedHashes is non-nil, modules whose content hash matches the agent's
// cached version are sent as stubs (name + hash only, no resources), saving
// bandwidth when only a subset of modules changed.
func buildPolicyBundle(cfg *config.EnvoyConfig, version uint64, cachedHashes map[string]string, traits map[string]any, streamEdit ...config.StreamEditSection) (*pb.PolicyBundle, error) {
	bundle := &pb.PolicyBundle{
		PolicyVersion:  version,
		MatchedPattern: cfg.MatchPattern,
		Environment:    cfg.Environment,
		Roles:          cfg.Roles,
	}

	// Populate stream_edit guardrail fields for agent-side enforcement.
	var se config.StreamEditSection
	if len(streamEdit) > 0 {
		se = streamEdit[0]
	}
	bundle.StreamEditAllowedPaths = se.StreamEditAllowedPaths()
	bundle.StreamEditTimeoutSecs = se.StreamEditTimeoutSeconds()

	// Serialize vars as JSON (preserves types: int, string, bool, etc.).
	if len(cfg.Vars) > 0 {
		varsJSON, err := json.Marshal(cfg.Vars)
		if err != nil {
			return nil, fmt.Errorf("marshaling vars: %w", err)
		}
		bundle.VarsJson = varsJSON
	}

	bundle.VarOrder = cfg.VarOrder

	// Convert modules to proto. Content hashes are pre-computed at config
	// load time (Configcrate.ContentHash) to avoid per-check-in SHA256.
	seEnabled := se.StreamEditEnabled()
	seAllowed := se.StreamEditAllowedPaths()
	for _, mod := range cfg.Configcrates {
		// Evaluate module-level when: server-side using traits.
		// Builtin functions (os_family, distro, arch, etc.) are resolved
		// here so the agent doesn't need to support them.
		if mod.When != "" && traits != nil {
			include, _ := config.EvalConfigcrateWhenServerSide(mod.When, traits)
			if !include {
				continue // skip module entirely
			}
		}

		pbMod := buildPolicyConfigcrate(mod, seEnabled, seAllowed, traits)
		pbMod.ContentHash = mod.ContentHash

		// If agent has this module cached with the same hash, send a stub.
		if cachedHashes != nil {
			if agentHash, ok := cachedHashes[mod.Name]; ok && agentHash == mod.ContentHash {
				pbMod.Stub = true
				pbMod.Resources = nil
			}
		}

		bundle.Configcrates = append(bundle.Configcrates, pbMod)
	}

	// Compute Merkle root from the finalized bundle content after module
	// filtering.
	bundle.MerkleRoot = config.MerkleRoot(cfg.Configcrates, cfg.Vars, cfg.VarOrder)

	return bundle, nil
}

// buildPolicyConfigcrate converts a config Configcrate to a PolicyConfigcrate proto.
// If stream_edit is disabled, strips the attribute from all file resources.
// If allowed paths are set, strips stream_edit scripts outside those directories.
func buildPolicyConfigcrate(mod config.Configcrate, seEnabled bool, seAllowed []string, traits map[string]any) *pb.PolicyConfigcrate {
	pbMod := &pb.PolicyConfigcrate{
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
			case int:
				// YAML parses bare octal (mode: 0644) as int.
				// Coerce mode values to octal string for the agent.
				if k == "mode" {
					attrs[k] = fmt.Sprintf("%04o", val)
				} else {
					attrs[k] = fmt.Sprintf("%d", val)
				}
			default:
				attrs[k] = fmt.Sprintf("%v", v)
			}
		}

		// Enforce stream_edit guardrails on file resources.
		if res.Type == "file" {
			if seVal, ok := attrs["stream_edit"]; ok {
				if !seEnabled {
					slog.Warn("stream_edit disabled, stripping attribute",
						"configcrate", mod.Name, "resource", res.Name)
					delete(attrs, "stream_edit")
				} else if !streamEditPathsAllowed(seVal, seAllowed) {
					slog.Warn("stream_edit script outside allowed paths, stripping attribute",
						"configcrate", mod.Name, "resource", res.Name,
						"allowed_paths", seAllowed)
					delete(attrs, "stream_edit")
				}
			}
		}

		// Evaluate resource-level when: server-side for builtin functions.
		// If the expression is fully server-resolvable (os_family, distro, etc.),
		// evaluate and filter. If it contains agent-only functions (has_command,
		// file_exists), pass through to the agent.
		resWhen := res.When
		if resWhen != "" && traits != nil {
			include, remaining := config.EvalConfigcrateWhenServerSide(resWhen, traits)
			if !include {
				continue // skip this resource
			}
			resWhen = remaining // empty if fully resolved, original if agent-only
		}

		pbRes := &pb.PolicyResource{
			Name:        res.Name,
			Type:        res.Type,
			Attributes:  attrs,
			When:        resWhen,
			Notify:      res.Notify,
			Subscribes:  res.Subscribes,
			WatchSecret: res.WatchSecret,
		}
		pbMod.Resources = append(pbMod.Resources, pbRes)
	}

	return pbMod
}

// filterBundleByTraits removes modules and resources from a pre-built policy
// bundle based on the envoy's traits. Used after the policy cache returns a
// shared base bundle — the cache can't do per-envoy filtering.
// injectServerVars merges server-derived per-envoy gates into bundle.VarsJson,
// preserving any operator-set vars from cfg.Vars. Called by request handlers
// that have envoy hostname access — the cache layer doesn't know per-envoy
// state, so injection happens after customizeStubs returns. The bundle's
// VarsJson is reassigned to a fresh slice; the cached base bundle is not
// mutated.
func injectServerVars(bundle *pb.PolicyBundle, server map[string]any) error {
	if len(server) == 0 {
		return nil
	}
	merged := make(map[string]any, len(server)+8)
	if len(bundle.VarsJson) > 0 {
		var existing map[string]any
		if err := json.Unmarshal(bundle.VarsJson, &existing); err != nil {
			return fmt.Errorf("re-parsing vars_json: %w", err)
		}
		for k, v := range existing {
			merged[k] = v
		}
	}
	for k, v := range server {
		merged[k] = v
	}
	out, err := json.Marshal(merged)
	if err != nil {
		return fmt.Errorf("re-marshaling vars_json: %w", err)
	}
	bundle.VarsJson = out
	return nil
}

func filterBundleByTraits(bundle *pb.PolicyBundle, traits map[string]any) *pb.PolicyBundle {
	if traits == nil || bundle == nil {
		return bundle
	}

	// Short-circuit: if no configcrate (or any of its resources) carries a
	// when:, there is nothing to filter — return the input bundle unchanged
	// and avoid the clone. (A resource-level when: still requires the filter
	// pass even when the module has none.)
	needsFilter := false
	for _, mod := range bundle.Configcrates {
		if mod.When != "" {
			needsFilter = true
			break
		}
		for _, res := range mod.Resources {
			if res.When != "" {
				needsFilter = true
				break
			}
		}
		if needsFilter {
			break
		}
	}
	if !needsFilter {
		return bundle
	}

	// Clone the whole bundle so every signed/scalar field (MerkleRoot,
	// VarOrder, CustomTraits, MatchedPattern, Environment, Roles,
	// StreamEdit*, SecretsEpoch, …) is preserved; only the Configcrates
	// slice is rebuilt from the trait-filtered set below.
	filtered := proto.Clone(bundle).(*pb.PolicyBundle)
	filtered.Configcrates = nil

	for _, mod := range bundle.Configcrates {
		// Module-level when: filtering.
		if mod.When != "" {
			include, remaining := config.EvalConfigcrateWhenServerSide(mod.When, traits)
			if !include {
				continue
			}
			mod = shallowCopyConfigcrate(mod)
			mod.When = remaining
		}

		// Resource-level when: filtering (only for non-stub modules).
		if !mod.Stub && len(mod.Resources) > 0 {
			var kept []*pb.PolicyResource
			for _, res := range mod.Resources {
				if res.When != "" {
					include, remaining := config.EvalConfigcrateWhenServerSide(res.When, traits)
					if !include {
						continue
					}
					res = shallowCopyResource(res)
					res.When = remaining
				}
				kept = append(kept, res)
			}
			if mod.When != "" || len(kept) != len(mod.Resources) {
				mod = shallowCopyConfigcrate(mod)
			}
			mod.Resources = kept
		}

		filtered.Configcrates = append(filtered.Configcrates, mod)
	}

	return filtered
}

func shallowCopyConfigcrate(m *pb.PolicyConfigcrate) *pb.PolicyConfigcrate {
	return proto.Clone(m).(*pb.PolicyConfigcrate)
}

func shallowCopyResource(r *pb.PolicyResource) *pb.PolicyResource {
	return proto.Clone(r).(*pb.PolicyResource)
}

// streamEditPathsAllowed checks if all script paths in a stream_edit value
// are under one of the allowed directories. The value may be a single path
// or a JSON array of paths.
func streamEditPathsAllowed(value string, allowedDirs []string) bool {
	if len(allowedDirs) == 0 {
		return true // no restrictions configured
	}
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return true
	}

	var paths []string
	if strings.HasPrefix(trimmed, "[") {
		if err := json.Unmarshal([]byte(trimmed), &paths); err != nil {
			paths = []string{trimmed}
		}
	} else {
		paths = []string{trimmed}
	}

	for _, p := range paths {
		if !isStreamEditPathAllowed(p, allowedDirs) {
			return false
		}
	}
	return true
}

// isStreamEditPathAllowed checks if a single script path is under one of the
// allowed directories. Rejects relative paths and path traversal.
func isStreamEditPathAllowed(scriptPath string, allowedDirs []string) bool {
	cleaned := filepath.Clean(scriptPath)
	if !filepath.IsAbs(cleaned) {
		return false
	}
	for _, dir := range allowedDirs {
		prefix := filepath.Clean(dir) + string(filepath.Separator)
		if strings.HasPrefix(cleaned, prefix) {
			return true
		}
	}
	return false
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
	configcrates := make([]*pb.PolicyConfigcrate, len(bundle.Configcrates))
	copy(configcrates, bundle.Configcrates)
	bundle.Configcrates = configcrates

	for mi, mod := range bundle.Configcrates {
		if mod.Stub {
			continue
		}
		for ri, res := range mod.Resources {
			for _, sp := range res.WatchSecret {
				if rotatedSet[sp] {
					// Clone module + resource so the cache entry stays clean.
					clonedMod := proto.Clone(mod).(*pb.PolicyConfigcrate)
					clonedRes := clonedMod.Resources[ri]
					if clonedRes.Attributes == nil {
						clonedRes.Attributes = make(map[string]string, 1)
					}
					clonedRes.Attributes["_secret_triggered"] = "true"
					bundle.Configcrates[mi] = clonedMod
					break
				}
			}
		}
	}
}

// hashPolicyConfigcrate computes a content hash from a PolicyConfigcrate proto.
// Delegates to config.HashConfigcrateContent after converting to Configcrate.
// Kept for backward compatibility with tests; production code uses
// pre-computed Configcrate.ContentHash instead.
func hashPolicyConfigcrate(mod *pb.PolicyConfigcrate) string {
	cfg := config.Configcrate{
		When:      mod.When,
		DependsOn: mod.DependsOn,
	}
	for _, res := range mod.Resources {
		attrs := make(map[string]any, len(res.Attributes))
		for k, v := range res.Attributes {
			attrs[k] = v
		}
		cfg.Resources = append(cfg.Resources, config.ResourceConfig{
			Name:        res.Name,
			Type:        res.Type,
			When:        res.When,
			Attributes:  attrs,
			Notify:      res.Notify,
			Subscribes:  res.Subscribes,
			WatchSecret: res.WatchSecret,
		})
	}
	return config.HashConfigcrateContent(cfg)
}
