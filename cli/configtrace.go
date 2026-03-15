// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

package main

import (
	"fmt"
	"strings"

	"github.com/spf13/cobra"
)

var configCmd = &cobra.Command{
	Use:   "config",
	Short: "Publish, inspect, freeze, and rollback .hnb configuration",
}

var configTraceCmd = &cobra.Command{
	Use:   "trace <hostname>",
	Short: "Show which node entry matched, role expansion, var sources, and final modules for a hostname",
	Long:  "Shows which pattern matched, how roles expanded, where each var came from, and the final module list.",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		hostname := args[0]
		client := newAPIClient()

		var trace struct {
			Hostname      string   `json:"hostname"`
			Matched       bool     `json:"matched"`
			MatchPattern  string   `json:"match_pattern"`
			PatternType   string   `json:"pattern_type"`
			Environment   string   `json:"environment"`
			Roles         []string `json:"roles"`
			RoleExpansion []struct {
				Role     string   `json:"role"`
				Includes []string `json:"includes"`
				Modules  []string `json:"modules"`
			} `json:"role_expansion"`
			Modules         []string `json:"modules"`
			VarSources      []struct {
				Key    string `json:"key"`
				Value  any    `json:"value"`
				Source string `json:"source"`
			} `json:"var_sources"`
			ConditionalVars []string `json:"conditional_vars"`
			ResourceCount   int      `json:"resource_count"`
		}

		if err := client.get(fmt.Sprintf("/api/v1/config/trace?hostname=%s", hostname), &trace); err != nil {
			return err
		}

		if !trace.Matched {
			fmt.Printf("No config entry matches hostname %q\n", hostname)
			return nil
		}

		fmt.Printf("Hostname:      %s\n", trace.Hostname)
		fmt.Printf("Match pattern: %s (%s)\n", trace.MatchPattern, trace.PatternType)
		if trace.Environment != "" {
			fmt.Printf("Environment:   %s\n", trace.Environment)
		}
		fmt.Printf("Resources:     %d total\n", trace.ResourceCount)
		fmt.Println()

		// Roles
		if len(trace.Roles) > 0 {
			fmt.Println("Roles:")
			for _, re := range trace.RoleExpansion {
				if len(re.Includes) > 0 {
					fmt.Printf("  %s (includes: %s)\n", re.Role, strings.Join(re.Includes, ", "))
				} else {
					fmt.Printf("  %s\n", re.Role)
				}
				for _, mod := range re.Modules {
					fmt.Printf("    → %s\n", mod)
				}
			}
			fmt.Println()
		}

		// Modules
		fmt.Printf("Modules (%d):\n", len(trace.Modules))
		for _, mod := range trace.Modules {
			fmt.Printf("  %s\n", mod)
		}
		fmt.Println()

		// Vars
		if len(trace.VarSources) > 0 {
			fmt.Println("Vars:")
			maxKeyLen := 0
			for _, v := range trace.VarSources {
				if len(v.Key) > maxKeyLen {
					maxKeyLen = len(v.Key)
				}
			}
			for _, v := range trace.VarSources {
				val := fmt.Sprintf("%v", v.Value)
				if len(val) > 60 {
					val = val[:57] + "..."
				}
				// Mask secret values
				if strings.HasPrefix(val, "secret:") {
					val = "secret:****"
				}
				fmt.Printf("  %-*s = %-30s  ← %s\n", maxKeyLen, v.Key, val, v.Source)
			}
			fmt.Println()
		}

		// Conditional vars
		if len(trace.ConditionalVars) > 0 {
			fmt.Printf("Conditional vars: %s\n", strings.Join(trace.ConditionalVars, ", "))
		}

		return nil
	},
}

func init() {
	configCmd.AddCommand(configTraceCmd)
}
