package grpc

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"

	pb "github.com/hobnob/hobnob/gen/proto/hobnob"
	"github.com/hobnob/hobnob/server/config"
)

// buildPolicyBundle converts a resolved NodeConfig into a PolicyBundle proto.
// Secrets are already resolved in the config (at load time). Vars are merged.
// Template strings and when expressions are left raw — the agent evaluates
// them locally with its own traits.
//
// If cachedHashes is non-nil, modules whose content hash matches the agent's
// cached version are sent as stubs (name + hash only, no resources), saving
// bandwidth when only a subset of modules changed.
func buildPolicyBundle(cfg *config.NodeConfig, version uint64, cachedHashes map[string]string) (*pb.PolicyBundle, error) {
	bundle := &pb.PolicyBundle{
		PolicyVersion:  version,
		MatchedPattern: cfg.MatchPattern,
		Environment:    cfg.Environment,
		Roles:          cfg.Roles,
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

	// Convert conditional vars to proto.
	for _, cv := range cfg.ConditionalVars {
		operandJSON, err := json.Marshal(cv.Operand)
		if err != nil {
			return nil, fmt.Errorf("marshaling operand for conditional var %q: %w", cv.Name, err)
		}
		bundle.ConditionalVars = append(bundle.ConditionalVars, &pb.ConditionalVarDef{
			Name:        cv.Name,
			Trait:        cv.Trait,
			Operator:    cv.Operator,
			OperandJson: string(operandJSON),
		})
	}

	// Convert modules with raw templates and compute content hashes.
	for _, mod := range cfg.Modules {
		pbMod := buildPolicyModule(mod)

		// Compute content hash for delta transfer.
		hash := hashPolicyModule(pbMod)
		pbMod.ContentHash = hash

		// If agent has this module cached with the same hash, send a stub.
		if cachedHashes != nil {
			if agentHash, ok := cachedHashes[mod.Name]; ok && agentHash == hash {
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
			attrs[k] = fmt.Sprintf("%v", v)
		}

		pbRes := &pb.PolicyResource{
			Name:       res.Name,
			Type:       res.Type,
			Attributes: attrs,
			When:       res.When,
			Notify:     res.Notify,
			Subscribes: res.Subscribes,
		}
		pbMod.Resources = append(pbMod.Resources, pbRes)
	}

	return pbMod
}

// hashPolicyModule computes a SHA256 hash of a PolicyModule's content.
// The hash covers resources (name, type, attributes, when, notify, subscribes),
// depends_on, and the module-level when expression — everything that affects
// the module's behavior. The module name is NOT included (it's the cache key).
func hashPolicyModule(mod *pb.PolicyModule) string {
	h := sha256.New()

	// Module-level fields.
	h.Write([]byte("when:"))
	h.Write([]byte(mod.When))
	h.Write([]byte("\ndeps:"))
	for _, dep := range mod.DependsOn {
		h.Write([]byte(dep))
		h.Write([]byte(","))
	}

	// Resources in order.
	for _, res := range mod.Resources {
		h.Write([]byte("\nres:"))
		h.Write([]byte(res.Name))
		h.Write([]byte("|"))
		h.Write([]byte(res.Type))
		h.Write([]byte("|when:"))
		h.Write([]byte(res.When))

		// Sort attribute keys for deterministic hashing.
		keys := make([]string, 0, len(res.Attributes))
		for k := range res.Attributes {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			h.Write([]byte("|"))
			h.Write([]byte(k))
			h.Write([]byte("="))
			h.Write([]byte(res.Attributes[k]))
		}

		// Notify and subscribes.
		h.Write([]byte("|notify:"))
		for _, n := range res.Notify {
			h.Write([]byte(n))
			h.Write([]byte(","))
		}
		h.Write([]byte("|subscribes:"))
		for _, s := range res.Subscribes {
			h.Write([]byte(s))
			h.Write([]byte(","))
		}
	}

	return hex.EncodeToString(h.Sum(nil))
}
