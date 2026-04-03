// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

package main

import (
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"sort"
	"strings"
	"text/tabwriter"

	"github.com/spf13/cobra"
)

var inventoryWhere []string
var inventoryShow []string

var inventoryCmd = &cobra.Command{
	Use:   "inventory",
	Short: "Search the fleet by trait values (e.g., OS, memory, packages) and display matching envoys",
	Long: `Query enrolled envoys by their collected traits.

Examples:
  vigocli inventory --where os.distro=ubuntu
  vigocli inventory --where os.distro=ubuntu --show network.ip
  vigocli inventory --where os.family=debian --where virtual.system=kvm --show os.distro,os.version`,
	RunE: func(cmd *cobra.Command, args []string) error {
		client := newAPIClient()

		// Build query string.
		params := url.Values{}
		for _, w := range inventoryWhere {
			params.Add("where", w)
		}
		if len(inventoryShow) > 0 {
			params.Set("show", strings.Join(inventoryShow, ","))
		}

		path := "/api/v1/inventory"
		if encoded := params.Encode(); encoded != "" {
			path += "?" + encoded
		}

		var resp struct {
			Count         int      `json:"count"`
			UnknownFields []string `json:"unknown_fields"`
			AvailableKeys []string `json:"available_keys"`
			Envoys        []struct {
				Hostname    string         `json:"hostname"`
				IPAddress   string         `json:"ip_address"`
				CollectedAt string         `json:"collected_at"`
				Traits      map[string]any `json:"traits"`
			} `json:"envoys"`
		}
		if err := client.get(path, &resp); err != nil {
			return err
		}

		if resp.Count == 0 {
			fmt.Println("No matching envoys found.")
			return nil
		}

		// If --show was provided, render a table with columns.
		if len(inventoryShow) > 0 {
			if err := renderTable(resp.Envoys, inventoryShow); err != nil {
				return err
			}
			// Warn about unknown fields after the table so output isn't interrupted.
			if len(resp.UnknownFields) > 0 {
				for _, field := range resp.UnknownFields {
					fmt.Fprintf(os.Stderr, "warning: trait %q not found on any envoy", field)
					if similar := findSimilar(field, resp.AvailableKeys, 3); len(similar) > 0 {
						fmt.Fprintf(os.Stderr, " — did you mean: %s", strings.Join(similar, ", "))
					}
					fmt.Fprintln(os.Stderr, "")
				}
			}
			return nil
		}

		// Otherwise print JSON.
		data, err := json.MarshalIndent(resp, "", "  ")
		if err != nil {
			return fmt.Errorf("failed to format response: %w", err)
		}
		fmt.Fprintln(os.Stdout, string(data))
		return nil
	},
}

func renderTable(envoys []struct {
	Hostname    string         `json:"hostname"`
	IPAddress   string         `json:"ip_address"`
	CollectedAt string         `json:"collected_at"`
	Traits      map[string]any `json:"traits"`
}, showFields []string) error {
	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)

	// Header: HOSTNAME + each show field.
	header := "HOSTNAME"
	for _, f := range showFields {
		header += "\t" + strings.ToUpper(f)
	}
	fmt.Fprintln(w, header)

	// Rows.
	for _, t := range envoys {
		row := t.Hostname
		for _, f := range showFields {
			row += "\t" + traitLookup(t.Traits, f)
		}
		fmt.Fprintln(w, row)
	}

	fmt.Fprintf(w, "\n%d envoys\n", len(envoys))
	return w.Flush()
}

// traitLookup looks up a trait value by dot-path. Tries a flat key first
// (the API returns picked show fields as flat keys like "os.distro"), then
// falls back to nested map traversal for full trait objects.
func traitLookup(data map[string]any, path string) string {
	// Flat key (API show-picked traits).
	if v, ok := data[path]; ok {
		return formatTraitValue(v)
	}
	// Nested traversal (full trait objects).
	if v, ok := traverseDotPath(data, path); ok {
		return formatTraitValue(v)
	}
	return ""
}

// formatTraitValue converts a trait value to a readable string.
func formatTraitValue(v any) string {
	switch val := v.(type) {
	case string:
		return val
	case float64:
		if val == float64(int64(val)) {
			return fmt.Sprintf("%d", int64(val))
		}
		return fmt.Sprintf("%.2f", val)
	case bool:
		return fmt.Sprintf("%t", val)
	case map[string]any:
		// Summarize: list keys with their values.
		var parts []string
		for k, v := range val {
			parts = append(parts, fmt.Sprintf("%s=%s", k, formatTraitValue(v)))
		}
		return strings.Join(parts, ", ")
	case []any:
		var parts []string
		for _, elem := range val {
			parts = append(parts, formatTraitValue(elem))
		}
		return strings.Join(parts, "; ")
	case nil:
		return ""
	default:
		return fmt.Sprint(v)
	}
}

// findSimilar returns up to n available keys that are similar to the input.
// Matches by: exact suffix (e.g., "cpus" matches "hardware.cpu_count" via
// shared segment), substring containment, or low edit distance on the last
// dot-segment.
func findSimilar(input string, available []string, n int) []string {
	// Extract the last segment for fuzzy matching (e.g., "cpus" from "hardware.cpus").
	lastSeg := input
	if idx := strings.LastIndexByte(input, '.'); idx >= 0 {
		lastSeg = input[idx+1:]
	}
	inputLower := strings.ToLower(input)
	lastSegLower := strings.ToLower(lastSeg)

	type scored struct {
		key   string
		score int // lower is better
	}
	var candidates []scored

	for _, key := range available {
		keyLower := strings.ToLower(key)
		keyLastSeg := key
		if idx := strings.LastIndexByte(key, '.'); idx >= 0 {
			keyLastSeg = key[idx+1:]
		}
		keyLastSegLower := strings.ToLower(keyLastSeg)

		// Exact match — skip (shouldn't happen but guard).
		if keyLower == inputLower {
			continue
		}

		// Skip top-level namespace keys when input has a dot (user expects a leaf).
		if strings.Contains(input, ".") && !strings.Contains(key, ".") {
			continue
		}

		score := 100
		// Same namespace prefix — strong signal.
		inputNS := strings.SplitN(inputLower, ".", 2)[0]
		if strings.HasPrefix(keyLower, inputNS+".") {
			score = 2
			// Substring match within same namespace — even stronger.
			if strings.Contains(keyLastSegLower, lastSegLower) || strings.Contains(lastSegLower, keyLastSegLower) {
				score = 0
			}
		} else if strings.Contains(keyLower, lastSegLower) && len(lastSegLower) >= 3 {
			// Cross-namespace substring match (only if segment is meaningful).
			score = 3
		}
		// Edit distance on last segment.
		if d := editDistance(lastSegLower, keyLastSegLower); d <= 3 && d < score {
			score = d
		}

		if score < 100 {
			candidates = append(candidates, scored{key, score})
		}
	}

	// Sort by score, pick top n.
	sort.Slice(candidates, func(i, j int) bool {
		if candidates[i].score != candidates[j].score {
			return candidates[i].score < candidates[j].score
		}
		return candidates[i].key < candidates[j].key
	})
	var result []string
	for i := 0; i < len(candidates) && i < n; i++ {
		result = append(result, candidates[i].key)
	}
	return result
}

// editDistance computes the Levenshtein distance between two strings.
func editDistance(a, b string) int {
	la, lb := len(a), len(b)
	if la == 0 {
		return lb
	}
	if lb == 0 {
		return la
	}
	prev := make([]int, lb+1)
	for j := range prev {
		prev[j] = j
	}
	for i := 1; i <= la; i++ {
		curr := make([]int, lb+1)
		curr[0] = i
		for j := 1; j <= lb; j++ {
			cost := 1
			if a[i-1] == b[j-1] {
				cost = 0
			}
			curr[j] = min(curr[j-1]+1, min(prev[j]+1, prev[j-1]+cost))
		}
		prev = curr
	}
	return prev[lb]
}

func init() {
	inventoryCmd.Flags().StringArrayVar(&inventoryWhere, "where", nil, "Filter by trait (key=value, repeatable)")
	inventoryCmd.Flags().StringSliceVar(&inventoryShow, "show", nil, "Trait fields to display as columns (comma-separated)")
	traitsCmd.AddCommand(inventoryCmd)
}
