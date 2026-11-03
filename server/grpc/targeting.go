// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: Apache-2.0

package grpc

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"

	"git.alexander4.io/vigo/server/db"
	"git.alexander4.io/vigo/server/fleet"
	"github.com/google/uuid"
)

// targetInfo holds a resolved target envoy.
type targetInfo struct {
	EnvoyID  string
	Hostname string
}

// resolveTargets resolves a target spec to a list of envoys.
// When a FleetIndex is provided, glob and trait resolution use in-memory lookups
// instead of DB queries, eliminating O(N) database round-trips.
//
// The spec is disambiguated as follows:
//   - UUID format → single envoy by ID (TGT-03)
//   - Starts with "tag:" → tag filter, e.g. "tag:webserver" (TGT-04)
//   - Contains "=" → trait filter, e.g. "os.distro=ubuntu" (TGT-02)
//   - Contains "*" or "?" → hostname glob, e.g. "*.web.prod" (TGT-01)
//   - Otherwise → single envoy by hostname (TGT-03)
func resolveTargets(ctx context.Context, queries *db.Queries, spec string, fi *fleet.FleetIndex) ([]targetInfo, error) {
	spec = strings.TrimSpace(spec)
	if spec == "" {
		return nil, fmt.Errorf("target spec is empty")
	}

	// UUID → single envoy by ID.
	if _, err := uuid.Parse(spec); err == nil {
		// FleetIndex path.
		if fi != nil {
			ts, exists, revoked := fi.GetEnvoyLite(spec)
			if !exists {
				return nil, fmt.Errorf("envoy %s not found", spec)
			}
			if revoked {
				return nil, fmt.Errorf("envoy %s is revoked", spec)
			}
			return []targetInfo{{EnvoyID: ts.ID, Hostname: ts.Hostname}}, nil
		}
		envoy, err := queries.GetEnvoyByID(ctx, spec)
		if err != nil {
			return nil, fmt.Errorf("envoy %s not found: %w", spec, err)
		}
		if envoy.Revoked != 0 {
			return nil, fmt.Errorf("envoy %s is revoked", spec)
		}
		return []targetInfo{{EnvoyID: envoy.ID, Hostname: envoy.Hostname}}, nil
	}

	// Tag filter → tag:<tagname>.
	if strings.HasPrefix(spec, "tag:") {
		return resolveByTag(spec[4:], fi)
	}

	// Trait filter → key.path=value.
	if strings.Contains(spec, "=") {
		return resolveByTrait(ctx, queries, spec, fi)
	}

	// Hostname glob → match against all envoys.
	if strings.ContainsAny(spec, "*?") {
		return resolveByGlob(ctx, queries, spec, fi)
	}

	// Exact hostname.
	if fi != nil {
		id := fi.GetByHostname(spec)
		if id == "" {
			return nil, fmt.Errorf("envoy with hostname %q not found", spec)
		}
		ts, exists, revoked := fi.GetEnvoyLite(id)
		if !exists {
			return nil, fmt.Errorf("envoy with hostname %q not found", spec)
		}
		if revoked {
			return nil, fmt.Errorf("envoy %q is revoked", spec)
		}
		return []targetInfo{{EnvoyID: ts.ID, Hostname: ts.Hostname}}, nil
	}
	envoy, err := queries.GetEnvoyByHostname(ctx, spec)
	if err != nil {
		return nil, fmt.Errorf("envoy with hostname %q not found: %w", spec, err)
	}
	if envoy.Revoked != 0 {
		return nil, fmt.Errorf("envoy %q is revoked", spec)
	}
	return []targetInfo{{EnvoyID: envoy.ID, Hostname: envoy.Hostname}}, nil
}

// resolveByGlob matches a hostname glob pattern against all non-revoked envoys.
// Uses FleetIndex when available (O(N) in-memory, no DB).
func resolveByGlob(ctx context.Context, queries *db.Queries, pattern string, fi *fleet.FleetIndex) ([]targetInfo, error) {
	// FleetIndex fast path: in-memory glob match.
	if fi != nil {
		fleetTargets := fi.ListByGlob(pattern)
		if len(fleetTargets) == 0 {
			return nil, fmt.Errorf("no envoys match pattern %q", pattern)
		}
		targets := make([]targetInfo, len(fleetTargets))
		for i, ft := range fleetTargets {
			targets[i] = targetInfo{EnvoyID: ft.EnvoyID, Hostname: ft.Hostname}
		}
		return targets, nil
	}

	// DB fallback.
	envoys, err := queries.ListEnvoys(ctx)
	if err != nil {
		return nil, fmt.Errorf("listing envoys: %w", err)
	}

	var targets []targetInfo
	for _, t := range envoys {
		if t.Revoked != 0 {
			continue
		}
		matched, err := filepath.Match(pattern, t.Hostname)
		if err != nil {
			return nil, fmt.Errorf("invalid glob pattern %q: %w", pattern, err)
		}
		if matched {
			targets = append(targets, targetInfo{EnvoyID: t.ID, Hostname: t.Hostname})
		}
	}

	if len(targets) == 0 {
		return nil, fmt.Errorf("no envoys match pattern %q", pattern)
	}
	return targets, nil
}

// resolveByTrait matches envoys whose latest traits contain key=value.
// Uses FleetIndex inverted trait index when available (O(1) lookup).
// The key is a dot-separated JSON path (e.g. "os.distro").
func resolveByTrait(ctx context.Context, queries *db.Queries, filter string, fi *fleet.FleetIndex) ([]targetInfo, error) {
	parts := strings.SplitN(filter, "=", 2)
	if len(parts) != 2 {
		return nil, fmt.Errorf("invalid trait filter %q (expected key=value)", filter)
	}
	traitPath := strings.TrimSpace(parts[0])
	wantValue := strings.TrimSpace(parts[1])

	if traitPath == "" {
		return nil, fmt.Errorf("trait filter key is empty")
	}

	// FleetIndex fast path: inverted index lookup, zero DB queries.
	if fi != nil {
		fleetTargets := fi.ListByTrait(traitPath, wantValue)
		if len(fleetTargets) > 0 {
			targets := make([]targetInfo, len(fleetTargets))
			for i, ft := range fleetTargets {
				targets[i] = targetInfo{EnvoyID: ft.EnvoyID, Hostname: ft.Hostname}
			}
			return targets, nil
		}

		// Reverse-index miss. byTrait intentionally excludes array-element
		// leaves (hasNumericSegment) — e.g. "cves.0.id" — so a selector on
		// such a path never hits above. Fall back to an on-demand scan of the
		// in-memory flattened traits, which still carry every leaf, so
		// selectors like "cves.0.id=CVE-..." keep working. Off the hot path:
		// resolveByTrait is reached only by operator live-query / task-push,
		// never check-in or convergence. Exact-match to mirror byTrait
		// semantics (the values it indexes are these same flattened strings).
		var targets []targetInfo
		for _, es := range fi.SnapshotEnvoys() {
			if es.Traits[traitPath] == wantValue {
				targets = append(targets, targetInfo{EnvoyID: es.ID, Hostname: es.Hostname})
			}
		}
		if len(targets) == 0 {
			return nil, fmt.Errorf("no envoys match trait filter %q", filter)
		}
		return targets, nil
	}

	// DB fallback: O(N) queries per envoy.
	envoys, err := queries.ListEnvoys(ctx)
	if err != nil {
		return nil, fmt.Errorf("listing envoys: %w", err)
	}

	// Build JSON pointer from dot path: "os.distro" → "/os/distro"
	pointer := "/" + strings.ReplaceAll(traitPath, ".", "/")

	var targets []targetInfo
	for _, t := range envoys {
		if t.Revoked != 0 {
			continue
		}

		traits, err := queries.GetLatestEnvoyTraits(ctx, t.ID)
		if err != nil {
			continue // No traits recorded — skip.
		}

		var parsed map[string]any
		if err := json.Unmarshal([]byte(traits.Traits), &parsed); err != nil {
			continue
		}

		val := jsonPointerLookup(parsed, pointer)
		if val != nil && fmt.Sprintf("%v", val) == wantValue {
			targets = append(targets, targetInfo{EnvoyID: t.ID, Hostname: t.Hostname})
		}
	}

	if len(targets) == 0 {
		return nil, fmt.Errorf("no envoys match trait filter %q", filter)
	}
	return targets, nil
}

// resolveByTag matches envoys with the given tag via FleetIndex.
func resolveByTag(tag string, fi *fleet.FleetIndex) ([]targetInfo, error) {
	tag = strings.TrimSpace(tag)
	if tag == "" {
		return nil, fmt.Errorf("tag filter is empty")
	}
	if fi == nil {
		return nil, fmt.Errorf("tag-based targeting requires FleetIndex")
	}
	fleetTargets := fi.ListByTag(tag)
	if len(fleetTargets) == 0 {
		return nil, fmt.Errorf("no envoys have tag %q", tag)
	}
	targets := make([]targetInfo, len(fleetTargets))
	for i, ft := range fleetTargets {
		targets[i] = targetInfo{EnvoyID: ft.EnvoyID, Hostname: ft.Hostname}
	}
	return targets, nil
}

// jsonPointerLookup traverses a JSON object using a simple pointer path.
// Supports paths like "/os/distro" for nested lookups.
func jsonPointerLookup(data map[string]any, pointer string) any {
	parts := strings.Split(strings.TrimPrefix(pointer, "/"), "/")
	var current any = data
	for _, part := range parts {
		m, ok := current.(map[string]any)
		if !ok {
			return nil
		}
		current, ok = m[part]
		if !ok {
			return nil
		}
	}
	return current
}
