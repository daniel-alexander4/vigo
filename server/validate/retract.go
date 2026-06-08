// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

package validate

// RetractedStateAttrs returns the lifecycle-state attribute overrides that
// retract a state-managed resource of resType: services stop and disable,
// every other state-consuming type (generatedStateTypes) flips to absent. ok is
// false for types that do not retract via state — the command-reversible types
// (which revert via revert:/on_revert:) and types that aren't reversible at all.
//
// This is the single source of truth for state-based retraction. The inline
// per-configcrate `state: absent` transform (server/config) calls it directly;
// the publish .retract generator (server/publish) is pinned to it by a
// conformance test (TestRetractGeneratorMatchesStateSoT).
func RetractedStateAttrs(resType string) (map[string]any, bool) {
	switch resType {
	case "service", "service_macos", "service_freebsd", "service_openbsd",
		"service_netbsd", "service_illumos", "service_windows":
		return map[string]any{"state": "stopped", "enabled": false}, true
	}
	if generatedStateTypes[resType] {
		return map[string]any{"state": "absent"}, true
	}
	return nil, false
}
