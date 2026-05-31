// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

package main

import (
	"fmt"

	"github.com/spf13/cobra"
)

var traitsCmd = &cobra.Command{
	Use:   "traits",
	Short: "Search and explore trait dot-paths available for use in templates and when: expressions",
}

var traitsSearchCmd = &cobra.Command{
	Use:   "search <keyword>",
	Short: "Search trait keys by keyword",
	Long: `Search all known trait dot-paths for a keyword match.
Matches against both key names and sample values. Use this to find
the correct trait path for .Traits references in templates or
when: expressions in configcrates.

Examples:
  vigocli traits search version
  vigocli traits search cpu
  vigocli traits search cloud
  vigocli traits search disk`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		client := newAPIClient()
		keyword := args[0]

		var resp struct {
			Count int `json:"count"`
			Keys  []struct {
				Key    string `json:"key"`
				Sample string `json:"sample"`
			} `json:"keys"`
		}
		if err := client.get("/api/v1/inventory/keys?q="+keyword, &resp); err != nil {
			return err
		}

		if resp.Count == 0 {
			fmt.Printf("No traits matching %q found.\n", keyword)
			return nil
		}

		w := newAlignedTable(cmd.OutOrStdout())
		fmt.Fprintln(w, "TRAIT\tSAMPLE VALUE")
		for _, k := range resp.Keys {
			fmt.Fprintf(w, "%s\t%s\n", k.Key, k.Sample)
		}
		fmt.Fprintf(w, "\n%d traits matching %q\n", resp.Count, keyword)
		return w.Flush()
	},
}

func init() {
	traitsCmd.AddCommand(traitsSearchCmd)
}
