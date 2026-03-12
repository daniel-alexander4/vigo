// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: BUSL-1.1

// Package conditionals provides parsing and evaluation of conditional var
// definitions. A conditional var is a structured YAML map with a "trait:"
// field and exactly one operator field. It evaluates to a boolean based on
// the value of the referenced trait.
package conditionals

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// validOperators defines the set of allowed operator names.
var validOperators = map[string]bool{
	"in":     true,
	"not_in": true,
	"gt":     true,
	"lt":     true,
	"gte":    true,
	"lte":    true,
	"regex":  true,
}

// ConditionalVar represents a parsed conditional variable definition.
type ConditionalVar struct {
	Name        string
	Trait       string // dot-path into traits map (e.g. "os.distro")
	Operator    string // one of: in, not_in, gt, lt, gte, lte, regex
	Operand     any    // type depends on operator
	OperandJSON string // pre-serialized JSON of the raw operand (before regex compilation)
}

// IsConditionalVar returns true if v is a map with a "trait" key,
// distinguishing conditional vars from regular string vars.
func IsConditionalVar(v any) bool {
	m, ok := v.(map[string]any)
	if !ok {
		return false
	}
	_, hasTrait := m["trait"]
	return hasTrait
}

// HasOperatorKey returns true if v is a map containing any known operator key.
// This catches malformed conditional vars (e.g., missing "trait" field) so they
// can be reported as validation errors rather than silently treated as regular vars.
func HasOperatorKey(v any) bool {
	m, ok := v.(map[string]any)
	if !ok {
		return false
	}
	for key := range m {
		if validOperators[key] {
			return true
		}
	}
	return false
}

// ParseConditionalVar validates and parses a raw map[string]any (from YAML
// unmarshaling) into a ConditionalVar. It returns an error if the trait field
// is missing, no operator is present, multiple operators are present, the
// operator name is invalid, or the operand is invalid (e.g. bad regex).
func ParseConditionalVar(name string, raw map[string]any) (ConditionalVar, error) {
	traitVal, ok := raw["trait"]
	if !ok {
		return ConditionalVar{}, fmt.Errorf("conditional var %q: missing required 'trait' field", name)
	}
	trait, ok := traitVal.(string)
	if !ok {
		return ConditionalVar{}, fmt.Errorf("conditional var %q: 'trait' must be a string", name)
	}

	// Find exactly one operator field.
	var operator string
	var operand any
	for key, val := range raw {
		if key == "trait" {
			continue
		}
		if !validOperators[key] {
			return ConditionalVar{}, fmt.Errorf("conditional var %q: unknown operator %q (valid: in, not_in, gt, lt, gte, lte, regex)", name, key)
		}
		if operator != "" {
			return ConditionalVar{}, fmt.Errorf("conditional var %q: multiple operators found (%q and %q); exactly one required", name, operator, key)
		}
		operator = key
		operand = val
	}
	if operator == "" {
		return ConditionalVar{}, fmt.Errorf("conditional var %q: no operator field found (need one of: in, not_in, gt, lt, gte, lte, regex)", name)
	}

	// Pre-serialize the raw operand before any transformation (e.g. regex
	// compilation). This avoids json.Marshal on the hot path during check-in.
	operandJSON, err := json.Marshal(operand)
	if err != nil {
		return ConditionalVar{}, fmt.Errorf("conditional var %q: marshaling operand: %w", name, err)
	}

	// Validate and transform operand based on operator.
	switch operator {
	case "regex":
		pattern, ok := operand.(string)
		if !ok {
			return ConditionalVar{}, fmt.Errorf("conditional var %q: 'regex' operand must be a string", name)
		}
		re, err := regexp.Compile(pattern)
		if err != nil {
			return ConditionalVar{}, fmt.Errorf("conditional var %q: invalid regex in 'regex': %w", name, err)
		}
		operand = re
	}

	return ConditionalVar{
		Name:        name,
		Trait:       trait,
		Operator:    operator,
		Operand:     operand,
		OperandJSON: string(operandJSON),
	}, nil
}

// Evaluate resolves the trait dot-path against the traits map and applies the
// operator. Returns false if the trait is missing or cannot be coerced to the
// required type.
func Evaluate(cv ConditionalVar, traits map[string]any) bool {
	traitVal := traverseDotPath(cv.Trait, traits)
	if traitVal == nil {
		return false
	}

	switch cv.Operator {
	case "in":
		return evalIn(traitVal, cv.Operand)
	case "not_in":
		return evalNotIn(traitVal, cv.Operand)
	case "gt":
		return evalNumeric(traitVal, cv.Operand, func(a, b float64) bool { return a > b })
	case "lt":
		return evalNumeric(traitVal, cv.Operand, func(a, b float64) bool { return a < b })
	case "gte":
		return evalNumeric(traitVal, cv.Operand, func(a, b float64) bool { return a >= b })
	case "lte":
		return evalNumeric(traitVal, cv.Operand, func(a, b float64) bool { return a <= b })
	case "regex":
		return evalRegex(traitVal, cv.Operand)
	default:
		return false
	}
}

// traverseDotPath splits the dot-path and walks into nested map[string]any.
// Returns nil if any segment is missing or the intermediate value is not a map.
func traverseDotPath(path string, m map[string]any) any {
	parts := strings.Split(path, ".")
	var current any = m
	for _, part := range parts {
		cm, ok := current.(map[string]any)
		if !ok {
			return nil
		}
		current, ok = cm[part]
		if !ok {
			return nil
		}
	}
	return current
}

// evalIn checks if the trait value (as string) is in the operand list.
func evalIn(traitVal, operand any) bool {
	list, ok := operand.([]any)
	if !ok {
		return false
	}
	traitStr := fmt.Sprintf("%v", traitVal)
	for _, item := range list {
		if fmt.Sprintf("%v", item) == traitStr {
			return true
		}
	}
	return false
}

// evalNotIn checks if the trait value (as string) is NOT in the operand list.
func evalNotIn(traitVal, operand any) bool {
	list, ok := operand.([]any)
	if !ok {
		return false
	}
	traitStr := fmt.Sprintf("%v", traitVal)
	for _, item := range list {
		if fmt.Sprintf("%v", item) == traitStr {
			return false
		}
	}
	return true
}

// evalNumeric coerces both trait value and operand to float64, then applies cmp.
// Returns false if either side cannot be coerced.
func evalNumeric(traitVal, operand any, cmp func(a, b float64) bool) bool {
	a, ok := toFloat64(traitVal)
	if !ok {
		return false
	}
	b, ok := toFloat64(operand)
	if !ok {
		return false
	}
	return cmp(a, b)
}

// toFloat64 attempts to coerce any value to float64.
// Handles int, float64, and string representations.
func toFloat64(v any) (float64, bool) {
	switch val := v.(type) {
	case float64:
		return val, true
	case int:
		return float64(val), true
	case int64:
		return float64(val), true
	case string:
		f, err := strconv.ParseFloat(val, 64)
		if err != nil {
			return 0, false
		}
		return f, true
	default:
		// Try fmt.Sprintf + ParseFloat as universal fallback.
		f, err := strconv.ParseFloat(fmt.Sprintf("%v", v), 64)
		if err != nil {
			return 0, false
		}
		return f, true
	}
}

// evalRegex runs the pre-compiled regex against the string form of the trait value.
func evalRegex(traitVal, operand any) bool {
	re, ok := operand.(*regexp.Regexp)
	if !ok {
		return false
	}
	traitStr := fmt.Sprintf("%v", traitVal)
	return re.MatchString(traitStr)
}
