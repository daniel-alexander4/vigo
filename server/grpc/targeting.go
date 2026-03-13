// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: BUSL-1.1

package grpc

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/google/uuid"
	"github.com/hobnob/hobnob/server/db"
)

// targetInfo holds a resolved target thrall.
type targetInfo struct {
	ThrallID string
	Hostname string
}

// resolveTargets resolves a target spec to a list of thralls.
//
// The spec is disambiguated as follows:
//   - UUID format → single thrall by ID (TGT-03)
//   - Contains "=" → trait filter, e.g. "os.distro=ubuntu" (TGT-02)
//   - Contains "*" or "?" → hostname glob, e.g. "*.web.prod" (TGT-01)
//   - Otherwise → single thrall by hostname (TGT-03)
func resolveTargets(ctx context.Context, queries *db.Queries, spec string) ([]targetInfo, error) {
	spec = strings.TrimSpace(spec)
	if spec == "" {
		return nil, fmt.Errorf("target spec is empty")
	}

	// UUID → single thrall by ID.
	if _, err := uuid.Parse(spec); err == nil {
		thrall, err := queries.GetThrallByID(ctx, spec)
		if err != nil {
			return nil, fmt.Errorf("thrall %s not found: %w", spec, err)
		}
		if thrall.Revoked != 0 {
			return nil, fmt.Errorf("thrall %s is revoked", spec)
		}
		return []targetInfo{{ThrallID: thrall.ID, Hostname: thrall.Hostname}}, nil
	}

	// Trait filter → key.path=value.
	if strings.Contains(spec, "=") {
		return resolveByTrait(ctx, queries, spec)
	}

	// Hostname glob → match against all thralls.
	if strings.ContainsAny(spec, "*?") {
		return resolveByGlob(ctx, queries, spec)
	}

	// Exact hostname.
	thrall, err := queries.GetThrallByHostname(ctx, spec)
	if err != nil {
		return nil, fmt.Errorf("thrall with hostname %q not found: %w", spec, err)
	}
	if thrall.Revoked != 0 {
		return nil, fmt.Errorf("thrall %q is revoked", spec)
	}
	return []targetInfo{{ThrallID: thrall.ID, Hostname: thrall.Hostname}}, nil
}

// resolveByGlob matches a hostname glob pattern against all non-revoked thralls.
func resolveByGlob(ctx context.Context, queries *db.Queries, pattern string) ([]targetInfo, error) {
	thralls, err := queries.ListThralls(ctx)
	if err != nil {
		return nil, fmt.Errorf("listing thralls: %w", err)
	}

	var targets []targetInfo
	for _, t := range thralls {
		if t.Revoked != 0 {
			continue
		}
		matched, err := filepath.Match(pattern, t.Hostname)
		if err != nil {
			return nil, fmt.Errorf("invalid glob pattern %q: %w", pattern, err)
		}
		if matched {
			targets = append(targets, targetInfo{ThrallID: t.ID, Hostname: t.Hostname})
		}
	}

	if len(targets) == 0 {
		return nil, fmt.Errorf("no thralls match pattern %q", pattern)
	}
	return targets, nil
}

// resolveByTrait matches thralls whose latest traits contain key=value.
// The key is a dot-separated JSON path (e.g. "os.distro").
func resolveByTrait(ctx context.Context, queries *db.Queries, filter string) ([]targetInfo, error) {
	parts := strings.SplitN(filter, "=", 2)
	if len(parts) != 2 {
		return nil, fmt.Errorf("invalid trait filter %q (expected key=value)", filter)
	}
	traitPath := strings.TrimSpace(parts[0])
	wantValue := strings.TrimSpace(parts[1])

	if traitPath == "" {
		return nil, fmt.Errorf("trait filter key is empty")
	}

	thralls, err := queries.ListThralls(ctx)
	if err != nil {
		return nil, fmt.Errorf("listing thralls: %w", err)
	}

	// Build JSON pointer from dot path: "os.distro" → "/os/distro"
	pointer := "/" + strings.ReplaceAll(traitPath, ".", "/")

	var targets []targetInfo
	for _, t := range thralls {
		if t.Revoked != 0 {
			continue
		}

		traits, err := queries.GetLatestThrallTraits(ctx, t.ID)
		if err != nil {
			continue // No traits recorded — skip.
		}

		var parsed map[string]any
		if err := json.Unmarshal([]byte(traits.Traits), &parsed); err != nil {
			continue
		}

		val := jsonPointerLookup(parsed, pointer)
		if val != nil && fmt.Sprintf("%v", val) == wantValue {
			targets = append(targets, targetInfo{ThrallID: t.ID, Hostname: t.Hostname})
		}
	}

	if len(targets) == 0 {
		return nil, fmt.Errorf("no thralls match trait filter %q", filter)
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
