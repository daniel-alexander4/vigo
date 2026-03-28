// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

package main

import (
	"encoding/json"
	"fmt"
	"net/url"
	"os"
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
  vigogo inventory --where os.distro=ubuntu
  vigogo inventory --where os.distro=ubuntu --show network.ip
  vigogo inventory --where os.family=debian --where virtual.type=kvm --show os.distro,os.version`,
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
			Count   int `json:"count"`
			Envoys []struct {
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
			return renderTable(resp.Envoys, inventoryShow)
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

func init() {
	inventoryCmd.Flags().StringArrayVar(&inventoryWhere, "where", nil, "Filter by trait (key=value, repeatable)")
	inventoryCmd.Flags().StringSliceVar(&inventoryShow, "show", nil, "Trait fields to display as columns (comma-separated)")
}
