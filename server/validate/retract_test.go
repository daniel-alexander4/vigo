// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: AGPL-3.0-only

package validate

import "testing"

func TestRetractedStateAttrs(t *testing.T) {
	t.Run("service stops and disables", func(t *testing.T) {
		attrs, ok := RetractedStateAttrs("service")
		if !ok {
			t.Fatal("service should retract via state")
		}
		if attrs["state"] != "stopped" || attrs["enabled"] != false {
			t.Errorf("service attrs = %v, want stopped+disabled", attrs)
		}
	})

	t.Run("stateful type flips to absent", func(t *testing.T) {
		attrs, ok := RetractedStateAttrs("package")
		if !ok || attrs["state"] != "absent" {
			t.Errorf("package attrs = %v ok=%v, want state:absent", attrs, ok)
		}
	})

	t.Run("command type does not retract via state", func(t *testing.T) {
		if _, ok := RetractedStateAttrs("exec"); ok {
			t.Error("exec is command-reversible, must not retract via state")
		}
	})

	t.Run("non-reversible type", func(t *testing.T) {
		if _, ok := RetractedStateAttrs("cisco_interface"); ok {
			t.Error("cisco_interface is not state-reversible")
		}
	})
}
