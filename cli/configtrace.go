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

		// Section 1: Inheritance tree (structure only)
		if len(trace.Inheritance) > 0 {
			fmt.Println(bold("Inheritance:"))
			fmt.Println()
			renderInheritanceTree(trace.Inheritance)
			fmt.Println()
		}

		// Section 2: Detail cards (boxes with modules, roles, vars)
		if len(trace.Inheritance) > 0 {
			renderDetailCards(trace.Inheritance, trace.RoleExpansion)
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
	Hostname        string                 `json:"hostname"`
	Matched         bool                   `json:"matched"`
	MatchPattern    string                 `json:"match_pattern"`
	PatternType     string                 `json:"pattern_type"`
	Environment     string                 `json:"environment"`
	Roles           []string               `json:"roles"`
	RoleExpansion   []roleExpansionEntry   `json:"role_expansion"`
	Inheritance     []inheritanceLevel     `json:"inheritance"`
	Modules         []string               `json:"modules"`
	ModuleSources   map[string]string      `json:"module_sources"`
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

type inheritanceLevel = struct {
	Dir       string         `json:"dir"`
	File      string         `json:"file"`
	Modules   []string       `json:"modules"`
	Roles     []string       `json:"roles"`
	Vars      map[string]any `json:"vars"`
	Overrides []string       `json:"overrides"`
}

type roleExpansionEntry = struct {
	Role     string   `json:"role"`
	Includes []string `json:"includes"`
	Modules  []string `json:"modules"`
}

// renderInheritanceTree draws Section 1: the directory tree structure without detail.
func renderInheritanceTree(levels []inheritanceLevel) {
	total := len(levels)

	// Split each level's path into directory segments + file.
	type pathEntry struct {
		dirs []string
		file string
	}
	paths := make([]pathEntry, total)
	for i, lvl := range levels {
		filePath := lvl.File
		if lvl.Dir != "" {
			filePath = lvl.Dir + "/" + lvl.File
		}
		parts := strings.Split(filePath, "/")
		paths[i] = pathEntry{
			dirs: parts[:len(parts)-1],
			file: parts[len(parts)-1],
		}
	}

	// Print tree with indentation based on directory depth.
	// Track which directory segments have already been printed.
	printed := make(map[string]bool)

	for i, p := range paths {
		color := greenRamp(i, total)
		colorBold := greenRampBold(i, total)
		rst := cReset()

		// Print directory segments that haven't been printed yet.
		for d := 0; d < len(p.dirs); d++ {
			dirPath := strings.Join(p.dirs[:d+1], "/")
			if printed[dirPath] {
				continue
			}
			printed[dirPath] = true
			indent := strings.Repeat("    ", d)
			connector := "└── "
			fmt.Printf("  %s%s%s%s%s\n", indent, color, connector, p.dirs[d], rst)
		}

		// Print the file.
		indent := strings.Repeat("    ", len(p.dirs))
		connector := "├── "
		if i == total-1 {
			connector = "└── "
		}
		if i == 0 && len(p.dirs) == 0 {
			connector = "● "
		}
		fmt.Printf("  %s%s%s%s%s\n", indent, colorBold, connector, p.file, rst)

		// Vertical connector between levels.
		if i < total-1 {
			nextDepth := len(paths[i+1].dirs)
			curDepth := len(p.dirs)
			pipeIndent := strings.Repeat("    ", curDepth)
			if nextDepth > curDepth {
				fmt.Printf("  %s%s│%s\n", pipeIndent, color, rst)
			}
		}
	}
}

// renderDetailCards draws Section 2: box cards with modules, inline role expansion, and vars.
func renderDetailCards(levels []inheritanceLevel, roleExpansion []roleExpansionEntry) {
	total := len(levels)

	// Build role → modules lookup.
	roleModules := make(map[string][]string)
	roleIncludes := make(map[string][]string)
	for _, re := range roleExpansion {
		roleModules[re.Role] = re.Modules
		roleIncludes[re.Role] = re.Includes
	}

	for i, lvl := range levels {
		color := greenRamp(i, total)
		colorBold := greenRampBold(i, total)
		rst := cReset()

		filePath := lvl.File
		if lvl.Dir != "" {
			filePath = lvl.Dir + "/" + lvl.File
		}

		// Determine box width.
		boxWidth := len(filePath) + 6
		if boxWidth < 40 {
			boxWidth = 40
		}

		// Top border + title.
		topLine := "┌─ " + filePath + " " + strings.Repeat("─", boxWidth-len(filePath)-4) + "┐"
		botLine := "└" + strings.Repeat("─", boxWidth-1) + "┘"

		fmt.Printf("  %s%s%s\n", colorBold, topLine, rst)

		// Modules.
		if len(lvl.Modules) > 0 {
			fmt.Printf("  %s│%s %smodules:%s %s\n", color, rst, dim(""), rst, strings.Join(lvl.Modules, ", "))
		}

		// Roles with inline expansion.
		for _, roleName := range lvl.Roles {
			mods := roleModules[roleName]
			incl := roleIncludes[roleName]
			label := cyan(roleName)
			if len(incl) > 0 {
				label += " " + dim("(includes: "+strings.Join(incl, ", ")+")")
			}
			fmt.Printf("  %s│%s %sroles:%s   %s\n", color, rst, dim(""), rst, label)
			for j, mod := range mods {
				branch := "├─"
				if j == len(mods)-1 {
					branch = "└─"
				}
				fmt.Printf("  %s│%s            %s %s\n", color, rst, dim(branch), mod)
			}
		}

		// Vars.
		if len(lvl.Vars) > 0 {
			keys := make([]string, 0, len(lvl.Vars))
			for k := range lvl.Vars {
				keys = append(keys, k)
			}
			sort.Strings(keys)

			levelOverrides := make(map[string]bool)
			for _, k := range lvl.Overrides {
				levelOverrides[k] = true
			}

			for j, k := range keys {
				val := fmt.Sprintf("%v", lvl.Vars[k])
				if strings.HasPrefix(val, "secret:") {
					val = red("secret:****")
				}
				override := ""
				if levelOverrides[k] {
					override = " " + yellow("⚡ overrides")
				}
				label := ""
				if j == 0 {
					label = dim("vars:") + "    "
				} else {
					label = "         "
				}
				fmt.Printf("  %s│%s %s%s = %s%s\n", color, rst, label, k, val, override)
			}
		}

		// Bottom border.
		fmt.Printf("  %s%s%s\n", color, botLine, rst)

		// Arrow between cards.
		if i < total-1 {
			fmt.Printf("  %s              │%s\n", color, rst)
			fmt.Printf("  %s              ▼%s\n", color, rst)
		}
	}
	fmt.Println()
}

func init() {
	configTraceCmd.Flags().StringVar(&traceModuleFlag, "module", "", "Filter trace to show only the branch for this module")
	configCmd.AddCommand(configTraceCmd)
}
