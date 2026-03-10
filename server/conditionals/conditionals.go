// Package conditionals provides parsing and evaluation of conditional var
// definitions. A conditional var is a structured YAML map with a "nail:"
// field and exactly one operator field. It evaluates to a boolean based on
// the value of the referenced nail.
package conditionals

import (
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
	Name     string
	Nail    string // dot-path into nails map (e.g. "os.distro")
	Operator string // one of: in, not_in, gt, lt, gte, lte, regex
	Operand  any    // type depends on operator
}

// IsConditionalVar returns true if v is a map with a "nail" key,
// distinguishing conditional vars from regular string vars.
func IsConditionalVar(v any) bool {
	m, ok := v.(map[string]any)
	if !ok {
		return false
	}
	_, hasNail := m["nail"]
	return hasNail
}

// HasOperatorKey returns true if v is a map containing any known operator key.
// This catches malformed conditional vars (e.g., missing "nail" field) so they
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
// unmarshaling) into a ConditionalVar. It returns an error if the nail field
// is missing, no operator is present, multiple operators are present, the
// operator name is invalid, or the operand is invalid (e.g. bad regex).
func ParseConditionalVar(name string, raw map[string]any) (ConditionalVar, error) {
	nailVal, ok := raw["nail"]
	if !ok {
		return ConditionalVar{}, fmt.Errorf("conditional var %q: missing required 'nail' field", name)
	}
	nail, ok := nailVal.(string)
	if !ok {
		return ConditionalVar{}, fmt.Errorf("conditional var %q: 'nail' must be a string", name)
	}

	// Find exactly one operator field.
	var operator string
	var operand any
	for key, val := range raw {
		if key == "nail" {
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
		Name:     name,
		Nail:    nail,
		Operator: operator,
		Operand:  operand,
	}, nil
}

// Evaluate resolves the nail dot-path against the nails map and applies the
// operator. Returns false if the nail is missing or cannot be coerced to the
// required type.
func Evaluate(cv ConditionalVar, nails map[string]any) bool {
	nailVal := traverseDotPath(cv.Nail, nails)
	if nailVal == nil {
		return false
	}

	switch cv.Operator {
	case "in":
		return evalIn(nailVal, cv.Operand)
	case "not_in":
		return evalNotIn(nailVal, cv.Operand)
	case "gt":
		return evalNumeric(nailVal, cv.Operand, func(a, b float64) bool { return a > b })
	case "lt":
		return evalNumeric(nailVal, cv.Operand, func(a, b float64) bool { return a < b })
	case "gte":
		return evalNumeric(nailVal, cv.Operand, func(a, b float64) bool { return a >= b })
	case "lte":
		return evalNumeric(nailVal, cv.Operand, func(a, b float64) bool { return a <= b })
	case "regex":
		return evalRegex(nailVal, cv.Operand)
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

// evalIn checks if the nail value (as string) is in the operand list.
func evalIn(nailVal, operand any) bool {
	list, ok := operand.([]any)
	if !ok {
		return false
	}
	nailStr := fmt.Sprintf("%v", nailVal)
	for _, item := range list {
		if fmt.Sprintf("%v", item) == nailStr {
			return true
		}
	}
	return false
}

// evalNotIn checks if the nail value (as string) is NOT in the operand list.
func evalNotIn(nailVal, operand any) bool {
	list, ok := operand.([]any)
	if !ok {
		return false
	}
	nailStr := fmt.Sprintf("%v", nailVal)
	for _, item := range list {
		if fmt.Sprintf("%v", item) == nailStr {
			return false
		}
	}
	return true
}

// evalNumeric coerces both nail value and operand to float64, then applies cmp.
// Returns false if either side cannot be coerced.
func evalNumeric(nailVal, operand any, cmp func(a, b float64) bool) bool {
	a, ok := toFloat64(nailVal)
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

// evalRegex runs the pre-compiled regex against the string form of the nail value.
func evalRegex(nailVal, operand any) bool {
	re, ok := operand.(*regexp.Regexp)
	if !ok {
		return false
	}
	nailStr := fmt.Sprintf("%v", nailVal)
	return re.MatchString(nailStr)
}
