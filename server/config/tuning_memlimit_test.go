// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: Apache-2.0

package config

import (
	"strings"
	"testing"
)

// TestDetectRetiredTuningMemoryLimit verifies that the retired tuning.memory_limit
// key hard-errors at load with a pointer to the GOMEMLIMIT env var, while a
// memory_limit-free tuning block (or no tuning block) loads cleanly.
func TestDetectRetiredTuningMemoryLimit(t *testing.T) {
	tests := []struct {
		name    string
		yaml    string
		wantErr bool
	}{
		{"absent", "tuning:\n  gogc: 100\n", false},
		{"no tuning block", "database:\n  driver: sqlite\n", false},
		{"retired memory_limit", "tuning:\n  memory_limit: \"4GiB\"\n", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := detectRetiredTuningMemoryLimit([]byte(tt.yaml))
			if (err != nil) != tt.wantErr {
				t.Fatalf("detectRetiredTuningMemoryLimit err = %v, wantErr %v", err, tt.wantErr)
			}
			if tt.wantErr && !strings.Contains(err.Error(), "GOMEMLIMIT") {
				t.Errorf("error %q should point at the GOMEMLIMIT env var", err.Error())
			}
		})
	}
}
