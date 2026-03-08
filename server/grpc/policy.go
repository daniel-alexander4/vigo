package grpc

import (
	"encoding/json"
	"fmt"

	pb "github.com/hobnob/hobnob/gen/proto/hobnob"
	"github.com/hobnob/hobnob/server/config"
)

// buildPolicyBundle converts a resolved NodeConfig into a PolicyBundle proto.
// Secrets are already resolved in the config (at load time). Vars are merged.
// Template strings and when expressions are left raw — the agent evaluates
// them locally with its own nails.
func buildPolicyBundle(cfg *config.NodeConfig, version uint64) (*pb.PolicyBundle, error) {
	bundle := &pb.PolicyBundle{
		PolicyVersion: version,
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
			Trait:       cv.Trait,
			Operator:    cv.Operator,
			OperandJson: string(operandJSON),
		})
	}

	// Convert modules with raw templates.
	for _, mod := range cfg.Modules {
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

		bundle.Modules = append(bundle.Modules, pbMod)
	}

	return bundle, nil
}
