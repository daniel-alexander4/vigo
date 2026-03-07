package config

// ShouldRunResource evaluates the four conditional fields on a ResourceConfig
// against the resolved vars map. Conditional var results are stored as "true"
// or "false" strings in vars. The evaluation order is:
//
//  1. GoIfAllPass: ALL listed vars must be true, else return false.
//  2. GoIfAnyPass: at least ONE listed var must be true, else return false.
//  3. SkipIfAllPass: if ALL listed vars are true, return false (skip).
//  4. SkipIfAnyPass: if ANY listed var is true, return false (skip).
//
// If all checks pass (or no fields are set), returns true.
func ShouldRunResource(res ResourceConfig, vars map[string]any) bool {
	// GoIfAllPass: gate — all must be true.
	if len(res.GoIfAllPass) > 0 {
		for _, name := range res.GoIfAllPass {
			if !varIsTrue(name, vars) {
				return false
			}
		}
	}

	// GoIfAnyPass: gate — at least one must be true.
	if len(res.GoIfAnyPass) > 0 {
		anyTrue := false
		for _, name := range res.GoIfAnyPass {
			if varIsTrue(name, vars) {
				anyTrue = true
				break
			}
		}
		if !anyTrue {
			return false
		}
	}

	// SkipIfAllPass: veto — if all are true, skip.
	if len(res.SkipIfAllPass) > 0 {
		allTrue := true
		for _, name := range res.SkipIfAllPass {
			if !varIsTrue(name, vars) {
				allTrue = false
				break
			}
		}
		if allTrue {
			return false
		}
	}

	// SkipIfAnyPass: veto — if any is true, skip.
	if len(res.SkipIfAnyPass) > 0 {
		for _, name := range res.SkipIfAnyPass {
			if varIsTrue(name, vars) {
				return false
			}
		}
	}

	return true
}

// varIsTrue returns true only if vars[name] == "true" (string).
// Unknown var names or non-"true" values return false.
func varIsTrue(name string, vars map[string]any) bool {
	v, ok := vars[name]
	if !ok {
		return false
	}
	s, ok := v.(string)
	if !ok {
		return false
	}
	return s == "true"
}
