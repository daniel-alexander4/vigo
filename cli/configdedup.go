// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: AGPL-3.0-only

package main

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"
)

var configDedupCmd = &cobra.Command{
	Use:   "dedup",
	Short: "Find redundant configcrates and variables in the config tree",
	Long: `Scans the config tree for redundant configcrate references and variable
definitions. Reports configcrates that are already inherited from a parent
common.vgo and variables that repeat the same value as an ancestor.

Examples:
  vigocli config dedup
  vigocli config dedup --configcrate nginx
  vigocli config dedup --var ssh_port`,
	RunE: func(cmd *cobra.Command, args []string) error {
		client := newAPIClient()

		var result struct {
			Findings []struct {
				Type          string `json:"type"`
				Name          string `json:"name"`
				File          string `json:"file"`
				Reason        string `json:"reason"`
				InheritedFrom string `json:"inherited_from"`
			} `json:"findings"`
		}

		if err := client.get("/api/v1/config/dedup", &result); err != nil {
			return err
		}

		// Apply filters.
		configcrateFilter, _ := cmd.Flags().GetString("configcrate")
		varFilter, _ := cmd.Flags().GetString("var")

		var filtered []struct {
			Type          string `json:"type"`
			Name          string `json:"name"`
			File          string `json:"file"`
			Reason        string `json:"reason"`
			InheritedFrom string `json:"inherited_from"`
		}

		for _, f := range result.Findings {
			if configcrateFilter != "" && (f.Type != "configcrate" || f.Name != configcrateFilter) {
				continue
			}
			if varFilter != "" && (f.Type != "var" || f.Name != varFilter) {
				continue
			}
			filtered = append(filtered, f)
		}

		if len(filtered) == 0 {
			fmt.Println(green("No redundancies found."))
			return nil
		}

		fmt.Printf("%s %d redundancies found\n\n", amber("!"), len(filtered))

		w := newAlignedTable(os.Stdout)
		fmt.Fprintf(w, "  TYPE\tNAME\tFILE\tREASON\n")
		for _, f := range filtered {
			var typeLabel string
			if f.Type == "configcrate" {
				typeLabel = amber("configcrate")
			} else {
				typeLabel = slateBlue("var")
			}
			fmt.Fprintf(w, "  %s\t%s\t%s\t%s\n", typeLabel, f.Name, medGray(f.File), f.Reason)
		}
		w.Flush()

		return nil
	},
}

func init() {
	configDedupCmd.Flags().String("configcrate", "", "filter to a specific configcrate name")
	configDedupCmd.Flags().String("var", "", "filter to a specific variable name")
	configCmd.AddCommand(configDedupCmd)
}
