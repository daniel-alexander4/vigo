// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

package main

import (
	"fmt"
	"net/url"
	"sort"
	"strings"

	"github.com/spf13/cobra"
)

var configCmd = &cobra.Command{
	Use:   "config",
	Short: "Publish, inspect, freeze, approve, and rollback .vgo configuration",
}

var traceModuleFlag string

var configTraceCmd = &cobra.Command{
	Use:   "trace <hostname>",
	Short: "Show the directory inheritance chain, module sources, and var resolution for a hostname",
	Long: `Traces config resolution for a hostname, showing the full directory
inheritance chain with vars and overrides at each level, which modules
came from which files, and where each var's final value originated.

Use --module to filter the trace to a single module's branch.`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		hostname := args[0]
		client := newAPIClient()

		var trace traceResult

		apiURL := fmt.Sprintf("/api/v1/config/trace?hostname=%s", url.QueryEscape(hostname))
		if traceModuleFlag != "" {
			apiURL += "&module=" + url.QueryEscape(traceModuleFlag)
		}

		if err := client.get(apiURL, &trace); err != nil {
			return err
		}

		if !trace.Matched {
			fmt.Printf("%s No config entry matches hostname %q\n", yellow("!"), hostname)
			return nil
		}

		// Header
		fmt.Printf("%s %s\n", bold("Hostname:"), trace.Hostname)
		fmt.Printf("%s %s %s\n", bold("Match:"), yellow(trace.MatchPattern), dim("("+trace.PatternType+")"))
		if trace.Environment != "" {
			fmt.Printf("%s %s\n", bold("Environment:"), trace.Environment)
		}
		fmt.Printf("%s %d total\n", bold("Resources:"), trace.ResourceCount)
		fmt.Println()

		// Inheritance chain with colored boxes
		if len(trace.Inheritance) > 0 {
			fmt.Println(bold("Inheritance:"))
			fmt.Println()
			renderInheritanceChain(trace.Inheritance)
			fmt.Println()
		}

		// Modules with source annotations
		fmt.Printf("%s\n", bold(fmt.Sprintf("Modules (%d):", len(trace.Modules))))
		if len(trace.Modules) > 0 {
			maxModLen := 0
			for _, mod := range trace.Modules {
				if len(mod) > maxModLen {
					maxModLen = len(mod)
				}
			}
			for _, mod := range trace.Modules {
				src := trace.ModuleSources[mod]
				if src != "" {
					fmt.Printf("  %-*s  %s\n", maxModLen, mod, dim("← "+src))
				} else {
					fmt.Printf("  %s\n", mod)
				}
			}
		}
		fmt.Println()

		// Roles
		if len(trace.Roles) > 0 {
			fmt.Println(bold("Roles:"))
			for _, re := range trace.RoleExpansion {
				if len(re.Includes) > 0 {
					fmt.Printf("  %s %s\n", cyan(re.Role), dim("(includes: "+strings.Join(re.Includes, ", ")+")"))
				} else {
					fmt.Printf("  %s\n", cyan(re.Role))
				}
				for _, mod := range re.Modules {
					fmt.Printf("    %s %s\n", dim("→"), mod)
				}
			}
			fmt.Println()
		}

		// Vars
		if len(trace.VarSources) > 0 {
			fmt.Println(bold("Vars:"))
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
				if strings.HasPrefix(val, "secret:") {
					val = red("secret:****")
				}
				fmt.Printf("  %s = %-30s  %s\n", bold(fmt.Sprintf("%-*s", maxKeyLen, v.Key)), val, dim("← "+v.Source))
			}
			fmt.Println()
		}

		// Conditional vars
		if len(trace.ConditionalVars) > 0 {
			fmt.Printf("%s %s\n", bold("Conditional vars:"), cyan(strings.Join(trace.ConditionalVars, ", ")))
		}

		return nil
	},
}

// traceResult is the CLI-side representation of the config trace API response.
type traceResult struct {
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
	Inheritance []struct {
		Dir       string         `json:"dir"`
		File      string         `json:"file"`
		Modules   []string       `json:"modules"`
		Roles     []string       `json:"roles"`
		Vars      map[string]any `json:"vars"`
		Overrides []string       `json:"overrides"`
	} `json:"inheritance"`
	Modules         []string          `json:"modules"`
	ModuleSources   map[string]string `json:"module_sources"`
	VarSources      []struct {
		Key    string `json:"key"`
		Value  any    `json:"value"`
		Source string `json:"source"`
	} `json:"var_sources"`
	ConditionalVars []string `json:"conditional_vars"`
	ResourceCount   int      `json:"resource_count"`
}

// greenRamp returns a 24-bit ANSI color code interpolating from light green
// to dark green based on depth within the chain.
func greenRamp(depth, total int) string {
	if !useColor || total <= 1 {
		return ""
	}
	t := float64(depth) / float64(total-1)
	// Light green (#c6f6d5 = 198,246,213) → dark green (#14532d = 20,83,45)
	r := int(198 - t*178)
	g := int(246 - t*163)
	b := int(213 - t*168)
	return fmt.Sprintf("\033[38;2;%d;%d;%dm", r, g, b)
}

// greenRampBold returns a bold 24-bit ANSI color for the box border/header.
func greenRampBold(depth, total int) string {
	if !useColor || total <= 1 {
		return cBold()
	}
	return cBold() + greenRamp(depth, total)
}

// renderInheritanceChain draws the colored box-based inheritance visualization.
func renderInheritanceChain(levels []struct {
	Dir       string         `json:"dir"`
	File      string         `json:"file"`
	Modules   []string       `json:"modules"`
	Roles     []string       `json:"roles"`
	Vars      map[string]any `json:"vars"`
	Overrides []string       `json:"overrides"`
}) {
	total := len(levels)
	overrideSet := make(map[string]bool)

	for i, lvl := range levels {
		color := greenRamp(i, total)
		colorBold := greenRampBold(i, total)
		rst := cReset()

		// Build override lookup for this level.
		levelOverrides := make(map[string]bool)
		for _, k := range lvl.Overrides {
			levelOverrides[k] = true
			overrideSet[k] = true
		}

		// File path label
		filePath := lvl.File
		if lvl.Dir != "" {
			filePath = lvl.Dir + "/" + lvl.File
		}

		// Connector
		connector := "├─"
		pipe := "│"
		if i == total-1 {
			connector = "└─"
			pipe = " "
		}
		if i == 0 && total == 1 {
			connector = "──"
			pipe = " "
		}

		fmt.Printf("  %s%s %s%s%s\n", colorBold, connector, filePath, rst, "")

		// Modules
		if len(lvl.Modules) > 0 {
			fmt.Printf("  %s%s%s  %smodules:%s %s\n", color, pipe, rst, color, rst, strings.Join(lvl.Modules, ", "))
		}

		// Roles
		if len(lvl.Roles) > 0 {
			fmt.Printf("  %s%s%s  %sroles:%s   %s\n", color, pipe, rst, color, rst, strings.Join(lvl.Roles, ", "))
		}

		// Vars
		if len(lvl.Vars) > 0 {
			keys := make([]string, 0, len(lvl.Vars))
			for k := range lvl.Vars {
				keys = append(keys, k)
			}
			sort.Strings(keys)
			for j, k := range keys {
				val := fmt.Sprintf("%v", lvl.Vars[k])
				if strings.HasPrefix(val, "secret:") {
					val = "secret:****"
				}
				override := ""
				if levelOverrides[k] {
					override = " " + yellow("⚡ overrides parent")
				}
				prefix := fmt.Sprintf("  %s%s%s  ", color, pipe, rst)
				if j == 0 && len(lvl.Modules) == 0 && len(lvl.Roles) == 0 {
					// First var line, no modules/roles above
				}
				label := "vars:"
				pad := "   "
				if j > 0 {
					label = ""
					pad = "         "
				}
				if label != "" {
					fmt.Printf("%s%s%s%s %s = %s%s\n", prefix, color, label, rst, k, val, override)
				} else {
					fmt.Printf("%s%s%s = %s%s\n", prefix, pad, k, val, override)
				}
			}
		}

		// Blank line between levels (except after last)
		if i < total-1 {
			fmt.Printf("  %s%s%s\n", color, pipe, rst)
		}
	}
}

func init() {
	configTraceCmd.Flags().StringVar(&traceModuleFlag, "module", "", "Filter trace to show only the branch for this module")
	configCmd.AddCommand(configTraceCmd)
}
