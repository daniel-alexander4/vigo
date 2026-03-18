// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

package main

import (
	"fmt"
	"net/url"
	"strings"

	"github.com/spf13/cobra"
)

var (
	searchMatch  string
	searchModule string
	searchVar    string
)

var configSearchCmd = &cobra.Command{
	Use:   "search",
	Short: "Search thrall entries by match pattern, module, or var",
	Long: `Find thrall entries matching the given criteria and show their
inheritance chain, modules, and vars.

Examples:
  hobnobcli config search --match "*web*"
  hobnobcli config search --module nginx
  hobnobcli config search --var log_level`,
	RunE: func(cmd *cobra.Command, args []string) error {
		if searchMatch == "" && searchModule == "" && searchVar == "" {
			return fmt.Errorf("at least one of --match, --module, or --var is required")
		}

		client := newAPIClient()

		params := url.Values{}
		if searchMatch != "" {
			params.Set("match", searchMatch)
		}
		if searchModule != "" {
			params.Set("module", searchModule)
		}
		if searchVar != "" {
			params.Set("var", searchVar)
		}

		var result struct {
			Entries []searchEntry `json:"entries"`
		}

		if err := client.get("/api/v1/config/search?"+params.Encode(), &result); err != nil {
			return err
		}

		if len(result.Entries) == 0 {
			fmt.Println(dim("No matching entries found."))
			return nil
		}

		for i, entry := range result.Entries {
			if i > 0 {
				fmt.Println()
			}
			printSearchEntry(entry)
		}

		return nil
	},
}

type searchEntry struct {
	Match       string `json:"match"`
	Dir         string `json:"dir"`
	Inheritance []struct {
		Dir     string   `json:"dir"`
		File    string   `json:"file"`
		Modules []string `json:"modules"`
		Roles   []string `json:"roles"`
	} `json:"inheritance"`
	OwnModules []string       `json:"own_modules"`
	OwnRoles   []string       `json:"own_roles"`
	Total      []string       `json:"total"`
	Excluded   []string       `json:"excluded"`
	Vars       map[string]any `json:"vars"`
}

func printSearchEntry(e searchEntry) {
	// Header: match pattern and location.
	dirDisplay := e.Dir
	if dirDisplay == "" {
		dirDisplay = "(root)"
	}
	fmt.Printf("  %s  %s\n", bold(yellow(e.Match)), dim(dirDisplay))

	// Inheritance chain.
	if len(e.Inheritance) > 0 {
		fmt.Printf("  %s\n", dim("Chain:"))
		for _, level := range e.Inheritance {
			dir := level.Dir
			if dir == "" {
				dir = "(root)"
			}
			modules := strings.Join(level.Modules, ", ")
			if modules == "" {
				modules = "(none)"
			}
			roles := ""
			if len(level.Roles) > 0 {
				roles = " roles:[" + strings.Join(level.Roles, ", ") + "]"
			}
			fmt.Printf("    %s/%s → %s%s\n",
				blue(dir), dim(level.File),
				dim(modules), dim(roles))
		}
	}

	// Own modules.
	if len(e.OwnModules) > 0 {
		fmt.Printf("  %s %s\n", dim("Own:"), bold(strings.Join(e.OwnModules, ", ")))
	}
	if len(e.OwnRoles) > 0 {
		fmt.Printf("  %s %s\n", dim("Roles:"), strings.Join(e.OwnRoles, ", "))
	}

	// Total.
	if len(e.Total) > 0 {
		fmt.Printf("  %s %s\n", dim("Total:"), green(strings.Join(e.Total, ", ")))
	}

	// Excluded.
	if len(e.Excluded) > 0 {
		excluded := make([]string, len(e.Excluded))
		for i, ex := range e.Excluded {
			excluded[i] = strike(ex)
		}
		fmt.Printf("  %s %s\n", dim("Excluded:"), strings.Join(excluded, ", "))
	}

	// Vars (only when searching by var).
	if searchVar != "" {
		if val, ok := e.Vars[searchVar]; ok {
			fmt.Printf("  %s %s = %v\n", dim("Var:"), bold(searchVar), val)
		}
	}
}

func init() {
	configSearchCmd.Flags().StringVar(&searchMatch, "match", "", "Glob pattern to match against thrall match: patterns")
	configSearchCmd.Flags().StringVar(&searchModule, "module", "", "Find entries using this module (inherited or direct)")
	configSearchCmd.Flags().StringVar(&searchVar, "var", "", "Find entries that define this var")

	configCmd.AddCommand(configSearchCmd)
}
