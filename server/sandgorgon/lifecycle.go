// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: AGPL-3.0-only

package sandgorgon

import "fmt"

// Lifecycle states for bare metal assets.
const (
	StateAvailable       = "available"
	StateCommissioned    = "commissioned"
	StateProvisioned     = "provisioned"
	StateActive          = "active"
	StateDecommissioning = "decommissioning"
	StateRetired         = "retired"
)

// validTransitions defines allowed state transitions.
var validTransitions = map[string][]string{
	StateAvailable:       {StateCommissioned},
	StateCommissioned:    {StateProvisioned, StateActive, StateDecommissioning},
	StateProvisioned:     {StateActive, StateDecommissioning},
	StateActive:          {StateDecommissioning},
	StateDecommissioning: {StateRetired},
	StateRetired:         {StateAvailable}, // return to pool after full sanitization
}

// ValidateTransition checks whether moving from one state to another is allowed.
func ValidateTransition(from, to string) error {
	targets, ok := validTransitions[from]
	if !ok {
		return fmt.Errorf("unknown lifecycle state: %q", from)
	}
	for _, t := range targets {
		if t == to {
			return nil
		}
	}
	return fmt.Errorf("invalid transition: %s → %s (allowed: %v)", from, to, targets)
}

// AllStates returns all valid lifecycle states.
func AllStates() []string {
	return []string{
		StateAvailable,
		StateCommissioned,
		StateProvisioned,
		StateActive,
		StateDecommissioning,
		StateRetired,
	}
}
