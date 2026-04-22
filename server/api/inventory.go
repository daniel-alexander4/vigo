// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"time"

	"git.alexander4.io/vigo/server/db"
)

// InventoryHandler serves the REST endpoint for querying envoys by traits.
type InventoryHandler struct {
	queries *db.Queries
}

// NewInventoryHandler creates an InventoryHandler using the given database queries.
func NewInventoryHandler(queries *db.Queries) *InventoryHandler {
	return &InventoryHandler{queries: queries}
}

// RegisterRoutes registers the inventory endpoint on the given mux.
func (h *InventoryHandler) RegisterRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/v1/inventory", h.HandleInventory)
	mux.HandleFunc("GET /api/v1/inventory/keys", h.HandleInventoryKeys)
}

// inventoryEntry is a single envoy in the inventory response.
type inventoryEntry struct {
	Hostname    string         `json:"hostname"`
	IPAddress   string         `json:"ip_address"`
	CollectedAt string         `json:"collected_at"`
	Traits      map[string]any `json:"traits,omitempty"`
}

// HandleInventory returns envoys filtered by trait values.
//
// Query parameters:
//   - where: repeatable, each is "dot.path=value" (equality match)
//   - show:  comma-separated dot-paths to include in output (default: all traits)
func (h *InventoryHandler) HandleInventory(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	// Parse filters from ?where= params.
	filters, err := parseFilters(r.URL.Query()["where"])
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, err.Error())
		return
	}

	// Parse show fields from ?show= param.
	var showFields []string
	if s := r.URL.Query().Get("show"); s != "" {
		showFields = strings.Split(s, ",")
	}

	all, err := h.queries.GetAllLatestTraits(ctx)
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "internal error")
		return
	}

	var results []inventoryEntry
	for _, t := range all {
		var traits map[string]any
		if err := json.Unmarshal([]byte(t.Traits), &traits); err != nil {
			continue // skip envoys with unparseable traits
		}

		if !matchesFilters(traits, filters) {
			continue
		}

		entry := inventoryEntry{
			Hostname:    t.Hostname,
			IPAddress:   t.IPAddress,
			CollectedAt: t.CollectedAt.Format(time.RFC3339),
		}

		if len(showFields) > 0 {
			picked := make(map[string]any, len(showFields))
			for _, field := range showFields {
				if val, ok := traverseDotPath(traits, field); ok {
					picked[field] = val
				}
			}
			entry.Traits = picked
		} else {
			entry.Traits = traits
		}

		results = append(results, entry)
	}

	// Detect show fields that didn't resolve for any envoy.
	var unknownFields []string
	var availableKeys []string
	if len(showFields) > 0 && len(all) > 0 {
		// Check which show fields had zero hits across all results.
		found := make(map[string]bool, len(showFields))
		for _, entry := range results {
			for _, field := range showFields {
				if _, ok := entry.Traits[field]; ok {
					found[field] = true
				}
			}
		}
		for _, field := range showFields {
			if !found[field] {
				unknownFields = append(unknownFields, field)
			}
		}

		// Collect available top-level and second-level dot-paths from
		// the first matching envoy to power "did you mean?" suggestions.
		if len(unknownFields) > 0 && len(results) > 0 {
			// Re-parse the first matching envoy's full traits.
			for _, t := range all {
				var traits map[string]any
				if json.Unmarshal([]byte(t.Traits), &traits) != nil {
					continue
				}
				if !matchesFilters(traits, filters) {
					continue
				}
				availableKeys = flatDotKeys("", traits, 2)
				break
			}
		}
	}

	resp := map[string]any{
		"count":  len(results),
		"envoys": results,
	}
	if len(unknownFields) > 0 {
		resp["unknown_fields"] = unknownFields
		resp["available_keys"] = availableKeys
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(resp)
}

// filter represents a single where clause: dot.path = value.
type filter struct {
	path  string
	value string
}

// HandleInventoryKeys returns all known trait dot-paths with sample values.
// Aggregates keys from all envoys to cover platform differences.
// Optional ?q= parameter filters keys by substring match.
func (h *InventoryHandler) HandleInventoryKeys(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	query := strings.ToLower(r.URL.Query().Get("q"))

	all, err := h.queries.GetAllLatestTraits(ctx)
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "internal error")
		return
	}

	// Aggregate keys and sample values from all envoys.
	keyValues := map[string]string{} // key → first non-empty sample value
	for _, t := range all {
		var traits map[string]any
		if json.Unmarshal([]byte(t.Traits), &traits) != nil {
			continue
		}
		collectKeyValues("", traits, keyValues)
	}

	type keyEntry struct {
		Key   string `json:"key"`
		Sample string `json:"sample"`
	}
	var keys []keyEntry
	for k, v := range keyValues {
		if query != "" && !strings.Contains(strings.ToLower(k), query) &&
			!strings.Contains(strings.ToLower(v), query) {
			continue
		}
		keys = append(keys, keyEntry{Key: k, Sample: v})
	}
	sort.Slice(keys, func(i, j int) bool { return keys[i].Key < keys[j].Key })

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{
		"count": len(keys),
		"keys":  keys,
	})
}

// collectKeyValues recursively flattens a trait map into dot-separated keys
// with sample values, keeping the first non-empty value seen for each key.
func collectKeyValues(prefix string, data map[string]any, out map[string]string) {
	for k, v := range data {
		full := k
		if prefix != "" {
			full = prefix + "." + k
		}
		switch val := v.(type) {
		case map[string]any:
			collectKeyValues(full, val, out)
		case []any:
			// Record the array itself as a key with element count.
			if _, exists := out[full]; !exists {
				out[full] = fmt.Sprintf("[%d items]", len(val))
			}
			// Also flatten first element if it's a map.
			if len(val) > 0 {
				if m, ok := val[0].(map[string]any); ok {
					collectKeyValues(full+".0", m, out)
				}
			}
		default:
			s := fmt.Sprint(v)
			if _, exists := out[full]; !exists && s != "" && s != "<nil>" {
				// Truncate long values.
				if len(s) > 60 {
					s = s[:60] + "..."
				}
				out[full] = s
			}
		}
	}
}

// parseFilters parses "key=value" strings into filter structs.
func parseFilters(raw []string) ([]filter, error) {
	var filters []filter
	for _, s := range raw {
		idx := strings.IndexByte(s, '=')
		if idx < 1 {
			return nil, fmt.Errorf("invalid where clause %q: expected key=value", s)
		}
		filters = append(filters, filter{
			path:  s[:idx],
			value: s[idx+1:],
		})
	}
	return filters, nil
}

// matchesFilters returns true if traits match all filters (AND logic).
func matchesFilters(traits map[string]any, filters []filter) bool {
	for _, f := range filters {
		val, ok := traverseDotPath(traits, f.path)
		if !ok {
			return false
		}
		if fmt.Sprint(val) != f.value {
			return false
		}
	}
	return true
}

// flatDotKeys collects dot-separated keys from a nested map up to maxDepth levels.
func flatDotKeys(prefix string, data map[string]any, maxDepth int) []string {
	var keys []string
	for k, v := range data {
		full := k
		if prefix != "" {
			full = prefix + "." + k
		}
		keys = append(keys, full)
		if maxDepth > 1 {
			if sub, ok := v.(map[string]any); ok {
				keys = append(keys, flatDotKeys(full, sub, maxDepth-1)...)
			}
		}
	}
	sort.Strings(keys)
	return keys
}

// traverseDotPath walks a nested map using a dot-separated path.
func traverseDotPath(data map[string]any, path string) (any, bool) {
	parts := strings.Split(path, ".")
	var current any = data
	for _, part := range parts {
		m, ok := current.(map[string]any)
		if !ok {
			return nil, false
		}
		current, ok = m[part]
		if !ok {
			return nil, false
		}
	}
	return current, true
}
