// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: BUSL-1.1

// Package policy provides agent-side evaluation of server policy bundles.
// When the server sends a PolicyBundle (raw templates, unevaluated conditionals),
// this package evaluates everything locally using the agent's own traits,
// producing the same compiled proto modules the runner expects.
package policy

import (
	"encoding/json"
	"fmt"
	"regexp"
	"sync"

	pb "github.com/hobnob/hobnob/gen/proto/hobnob"
	"github.com/hobnob/hobnob/server/conditionals"
	"github.com/hobnob/hobnob/server/config"
)

// regexCache caches compiled regexes by pattern string, avoiding recompilation
// on every policy evaluation cycle.
var regexCache sync.Map // map[string]*regexp.Regexp

// EvalBundle evaluates a policy bundle using the agent's traits. It performs
// the same computation the server would do in legacy mode:
//  1. Parse conditional var definitions and evaluate against traits
//  2. Render vars templates with traits
//  3. Filter modules by when expressions
//  4. Filter resources by when expressions (split server/agent-side)
//  5. Render resource attribute templates
//
// Returns compiled proto modules in the same format the runner expects.
func EvalBundle(bundle *pb.PolicyBundle, traits map[string]any) ([]*pb.Module, error) {
	if bundle == nil || len(bundle.Modules) == 0 {
		return nil, nil
	}

	// 1. Reconstruct vars from JSON.
	vars := make(map[string]any)
	if len(bundle.VarsJson) > 0 {
		if err := json.Unmarshal(bundle.VarsJson, &vars); err != nil {
			return nil, fmt.Errorf("unmarshaling vars: %w", err)
		}
	}

	// 2. Evaluate conditional vars against traits.
	varOrder := bundle.VarOrder
	vars, varOrder = evaluateConditionalVars(bundle.ConditionalVars, traits, vars, varOrder)

	// 3. Render vars templates.
	var err error
	vars, err = config.RenderVars(vars, traits, varOrder)
	if err != nil {
		return nil, fmt.Errorf("rendering vars: %w", err)
	}

	// 4. Convert policy modules to compiled proto modules.
	modules := make([]*pb.Module, 0, len(bundle.Modules))
	for _, pmod := range bundle.Modules {
		// Check module-level when.
		if pmod.When != "" {
			include, err := evalModuleWhen(pmod.When, vars)
			if err != nil {
				return nil, fmt.Errorf("evaluating when for module %q: %w", pmod.Name, err)
			}
			if !include {
				continue
			}
		}

		pbMod := &pb.Module{
			Name:      pmod.Name,
			DependsOn: pmod.DependsOn,
		}

		for _, pres := range pmod.Resources {
			// Evaluate resource when expression.
			shouldInclude, agentWhen, err := config.EvaluateResourceWhen(pres.When, vars)
			if err != nil {
				return nil, fmt.Errorf("evaluating when for resource %q: %w", pres.Name, err)
			}
			if !shouldInclude {
				continue
			}

			// Render attribute templates.
			params := make(map[string]string, len(pres.Attributes))
			for k, v := range pres.Attributes {
				rendered, err := config.RenderTemplate(v, traits, vars)
				if err != nil {
					return nil, fmt.Errorf("rendering template for resource %q attr %q: %w",
						pres.Name, k, err)
				}
				params[k] = rendered
			}

			// Mark resources with subscribes_secret so the runner can skip them
			// in the main pass unless the server injected _secret_triggered=true.
			if len(pres.SubscribesSecret) > 0 && params["_secret_triggered"] != "true" {
				params["_subscribes_secret"] = "true"
			}

			pbRes := &pb.Resource{
				Name:   pres.Name,
				Type:   pres.Type,
				Params: params,
			}
			if agentWhen != "" {
				pbRes.When = agentWhen
			}
			pbMod.Resources = append(pbMod.Resources, pbRes)

			if pbMod.ResourceType == "" {
				pbMod.ResourceType = pres.Type
			}

			pbMod.Notify = append(pbMod.Notify, pres.Notify...)
			pbMod.Subscribes = append(pbMod.Subscribes, pres.Subscribes...)
		}

		if len(pbMod.Resources) == 0 {
			continue
		}

		modules = append(modules, pbMod)
	}

	return modules, nil
}

// evaluateConditionalVars parses ConditionalVarDef protos and evaluates each
// against the traits map, setting "true"/"false" string values in vars.
func evaluateConditionalVars(defs []*pb.ConditionalVarDef, traits, vars map[string]any, varOrder []string) (map[string]any, []string) {
	if len(defs) == 0 {
		return vars, varOrder
	}

	var condNames []string
	for _, def := range defs {
		cv, err := parseConditionalVarDef(def)
		if err != nil {
			// Unparseable conditional → false.
			vars[def.Name] = "false"
			condNames = append(condNames, def.Name)
			continue
		}

		if conditionals.Evaluate(cv, traits) {
			vars[cv.Name] = "true"
		} else {
			vars[cv.Name] = "false"
		}
		condNames = append(condNames, cv.Name)
	}

	newOrder := make([]string, 0, len(condNames)+len(varOrder))
	newOrder = append(newOrder, condNames...)
	newOrder = append(newOrder, varOrder...)

	return vars, newOrder
}

// parseConditionalVarDef reconstructs a ConditionalVar from a proto definition.
func parseConditionalVarDef(def *pb.ConditionalVarDef) (conditionals.ConditionalVar, error) {
	var operand any
	if err := json.Unmarshal([]byte(def.OperandJson), &operand); err != nil {
		return conditionals.ConditionalVar{}, fmt.Errorf("parsing operand for %q: %w", def.Name, err)
	}

	// For regex operator, compile the pattern (cached across cycles).
	if def.Operator == "regex" {
		pattern, ok := operand.(string)
		if !ok {
			return conditionals.ConditionalVar{}, fmt.Errorf("regex operand for %q must be a string", def.Name)
		}
		if cached, ok := regexCache.Load(pattern); ok {
			operand = cached.(*regexp.Regexp)
		} else {
			re, err := regexp.Compile(pattern)
			if err != nil {
				return conditionals.ConditionalVar{}, fmt.Errorf("invalid regex for %q: %w", def.Name, err)
			}
			regexCache.Store(pattern, re)
			operand = re
		}
	}

	return conditionals.ConditionalVar{
		Name:     def.Name,
		Trait:    def.Trait,
		Operator: def.Operator,
		Operand:  operand,
	}, nil
}

// evalModuleWhen evaluates a module-level when expression against vars.
func evalModuleWhen(when string, vars map[string]any) (bool, error) {
	expr, err := conditionals.ParseWhenExpr(when)
	if err != nil {
		return false, err
	}
	return conditionals.EvalWhenExpr(expr, func(name string) bool {
		v, ok := vars[name]
		if !ok {
			return false
		}
		s, ok := v.(string)
		return ok && s == "true"
	})
}
