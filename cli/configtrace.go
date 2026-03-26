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
		fmt.Printf("%s %s %s\n", bold("Match:"), amber(trace.MatchPattern), medGray("("+trace.PatternType+")"))
		if trace.Environment != "" {
			fmt.Printf("%s %s\n", bold("Environment:"), trace.Environment)
		}
		fmt.Printf("%s %d total\n", bold("Resources:"), trace.ResourceCount)
		fmt.Println()

		// Section 1: Path Inheritance (directory tree structure)
		if len(trace.Inheritance) > 0 {
			fmt.Println(bold("Path Inheritance:"))
			fmt.Println()
			renderInheritanceTree(trace.Inheritance, trace.WaiverFiles)
			fmt.Println()
		}

		// Separator between tree and module cards.
		if len(trace.Inheritance) > 0 {
			fmt.Println("  " + medGray("─────────────────────────────────────────"))
			fmt.Println()
		}

		// Section 2: Module Inheritance (boxes with modules and roles, no vars)
		if len(trace.Inheritance) > 0 {
			fmt.Println(bold("Module Inheritance:"))
			fmt.Println()
			renderModuleCards(trace.Inheritance, trace.RoleExpansion, trace.MatchPattern)
		}

		// Separator between modules and vars.
		if len(trace.Inheritance) > 0 {
			fmt.Println("  " + medGray("─────────────────────────────────────────"))
			fmt.Println()
		}

		// Section 3: Variable Inheritance (var resolution through chain + final sources)
		if len(trace.Inheritance) > 0 || len(trace.VarSources) > 0 {
			fmt.Println(bold("Variable Inheritance:"))
			fmt.Println()
			renderVarInheritance(trace.Inheritance, trace.VarSources)
		}

		// Section 4: Password Inheritance
		if len(trace.SecretRefs) > 0 {
			fmt.Println("  " + medGray("─────────────────────────────────────────"))
			fmt.Println()
			fmt.Println(bold("Password Inheritance:"))
			fmt.Println()
			renderPasswordInheritance(trace.SecretRefs, trace.SecretsProvider)
		}

		// Section 5: Dependency Inheritance
		if len(trace.Dependencies) > 0 {
			fmt.Println("  " + medGray("─────────────────────────────────────────"))
			fmt.Println()
			fmt.Println(bold("Dependency Inheritance:"))
			fmt.Println()
			renderDependencyInheritance(trace.Dependencies)
		}

		// Section 6: Compliance Inheritance
		if len(trace.ComplianceTags) > 0 {
			fmt.Println("  " + medGray("─────────────────────────────────────────"))
			fmt.Println()
			fmt.Println(bold("Compliance Inheritance:"))
			fmt.Println()
			renderComplianceInheritance(trace.ComplianceTags)
		}

		// Section 7: Waivers Inheritance
		if len(trace.Waivers) > 0 {
			fmt.Println("  " + medGray("─────────────────────────────────────────"))
			fmt.Println()
			fmt.Println(bold("Waivers Inheritance:"))
			fmt.Println()
			renderWaiverInheritance(trace.Waivers)
		}

		// Section 8: Conditional Inheritance
		if len(trace.Conditionals) > 0 {
			fmt.Println("  " + medGray("─────────────────────────────────────────"))
			fmt.Println()
			fmt.Println(bold("Conditional Inheritance:"))
			fmt.Println()
			renderConditionalInheritance(trace.Conditionals)
		}

		// Conditional vars
		if len(trace.ConditionalVars) > 0 {
			fmt.Printf("%s %s\n", bold("Conditional vars:"), teal(strings.Join(trace.ConditionalVars, ", ")))
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

	SecretRefs      []secretTraceEntry      `json:"secret_refs"`
	SecretsProvider string                  `json:"secrets_provider"`
	Dependencies    []dependencyTraceEntry  `json:"dependencies"`
	ComplianceTags  []complianceTraceEntry  `json:"compliance_tags"`
	WaiverFiles     []string                `json:"waiver_files"`
	Waivers         []waiverTraceEntry      `json:"waivers"`
	Conditionals    []conditionalTraceEntry `json:"conditionals"`
}

type secretTraceEntry = struct {
	Key    string `json:"key"`
	Path   string `json:"path"`
	Source string `json:"source"`
	Module string `json:"module"`
}

type dependencyTraceEntry = struct {
	From  string `json:"from"`
	To    string `json:"to"`
	Type  string `json:"type"`
	Level string `json:"level"`
}

type complianceTraceEntry = struct {
	Module     string              `json:"module"`
	Frameworks map[string][]string `json:"frameworks"`
}

type waiverTraceEntry = struct {
	SourceFile string `json:"source_file"`
	Framework  string `json:"framework"`
	Control    string `json:"control"`
	Reason     string `json:"reason"`
	ApprovedBy string `json:"approved_by"`
	Expires    string `json:"expires"`
}

type conditionalTraceEntry = struct {
	Module       string `json:"module"`
	Resource     string `json:"resource"`
	When         string `json:"when"`
	ResolvedWhen string `json:"resolved_when"`
	EvalSide     string `json:"eval_side"`
}

// greenRamp returns a 24-bit ANSI color code interpolating from light green
// to dark green based on depth within the chain. A minimum of 5 levels is
// used for the gradient so shallow traces don't jump straight to dark green.
func greenRamp(depth, total int) string {
	if !useColor || total <= 1 {
		return ""
	}
	effective := total
	if effective < 5 {
		effective = 5
	}
	t := float64(depth) / float64(effective-1)
	// Medium green (#5bb370 = 91,179,112) → forest green (#2d7a4a = 45,122,74)
	r := int(91 - t*46)
	g := int(179 - t*57)
	b := int(112 - t*38)
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
// waiverFiles are interleaved alongside common.vgo entries at matching directory levels.
func renderInheritanceTree(levels []inheritanceLevel, waiverFiles []string) {
	// Build combined list: inheritance levels + waiver files as synthetic entries.
	type pathEntry struct {
		dirs []string
		file string
	}

	// Interleave: for each inheritance level, after its entry, insert any
	// waiver file at the same directory level.
	waiverByDir := make(map[string]string) // dir → waiver rel path
	for _, wf := range waiverFiles {
		parts := strings.Split(wf, "/")
		dir := ""
		if len(parts) > 1 {
			dir = strings.Join(parts[:len(parts)-1], "/")
		}
		waiverByDir[dir] = wf
	}

	var paths []pathEntry
	usedWaiver := make(map[string]bool)
	for _, lvl := range levels {
		filePath := lvl.File
		if lvl.Dir != "" {
			filePath = lvl.Dir + "/" + lvl.File
		}
		parts := strings.Split(filePath, "/")
		paths = append(paths, pathEntry{dirs: parts[:len(parts)-1], file: parts[len(parts)-1]})

		// Insert waiver file at the same directory level right after common.vgo.
		if wf, ok := waiverByDir[lvl.Dir]; ok && !usedWaiver[lvl.Dir] {
			usedWaiver[lvl.Dir] = true
			wParts := strings.Split(wf, "/")
			paths = append(paths, pathEntry{dirs: wParts[:len(wParts)-1], file: wParts[len(wParts)-1]})
		}
	}
	total := len(paths)

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

// renderModuleCards draws Section 2: box cards with modules and roles (no vars).
func renderModuleCards(levels []inheritanceLevel, roleExpansion []roleExpansionEntry, matchPattern string) {
	total := len(levels)

	// Build role → modules lookup.
	roleModules := make(map[string][]string)
	roleIncludes := make(map[string][]string)
	for _, re := range roleExpansion {
		roleModules[re.Role] = re.Modules
		roleIncludes[re.Role] = re.Includes
	}

	for i, lvl := range levels {
		// Skip levels that contribute no modules or roles.
		if len(lvl.Modules) == 0 && len(lvl.Roles) == 0 && !(i == total-1 && matchPattern != "") {
			continue
		}

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

		// Match (leaf card only).
		if i == total-1 && matchPattern != "" {
			fmt.Printf("  %s│%s %s %s\n", color, rst, magenta("match:"), matchPattern)
		}

		// Modules.
		if len(lvl.Modules) > 0 {
			fmt.Printf("  %s│%s %s %s\n", color, rst, amber("modules:"), strings.Join(lvl.Modules, ", "))
		}

		// Roles with inline expansion.
		for _, roleName := range lvl.Roles {
			mods := roleModules[roleName]
			incl := roleIncludes[roleName]
			label := teal(roleName)
			if len(incl) > 0 {
				label += " " + medGray("(includes: "+strings.Join(incl, ", ")+")")
			}
			fmt.Printf("  %s│%s %s   %s\n", color, rst, teal("roles:"), label)
			for j, mod := range mods {
				branch := "├─"
				if j == len(mods)-1 {
					branch = "└─"
				}
				fmt.Printf("  %s│%s            %s %s\n", color, rst, medGray(branch), mod)
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

// renderVarInheritance draws Section 3: variable resolution through the inheritance chain.
func renderVarInheritance(levels []inheritanceLevel, varSources []struct {
	Key    string `json:"key"`
	Value  any    `json:"value"`
	Source string `json:"source"`
}) {
	total := len(levels)

	// Show vars at each inheritance level with override markers.
	for i, lvl := range levels {
		if len(lvl.Vars) == 0 {
			continue
		}

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

		topLine := "┌─ " + filePath + " " + strings.Repeat("─", boxWidth-len(filePath)-4) + "┐"
		botLine := "└" + strings.Repeat("─", boxWidth-1) + "┘"

		fmt.Printf("  %s%s%s\n", colorBold, topLine, rst)

		keys := make([]string, 0, len(lvl.Vars))
		for k := range lvl.Vars {
			keys = append(keys, k)
		}
		sort.Strings(keys)

		levelOverrides := make(map[string]bool)
		for _, k := range lvl.Overrides {
			levelOverrides[k] = true
		}

		for _, k := range keys {
			val := fmt.Sprintf("%v", lvl.Vars[k])
			if strings.HasPrefix(val, "secret:") {
				val = red("secret:****")
			}
			override := ""
			if levelOverrides[k] {
				override = " " + amber("overrides")
			}
			fmt.Printf("  %s│%s %s = %s%s\n", color, rst, slateBlue(k), val, override)
		}

		fmt.Printf("  %s%s%s\n", color, botLine, rst)

		if i < total-1 {
			// Check if any later level also has vars.
			hasMoreVars := false
			for _, later := range levels[i+1:] {
				if len(later.Vars) > 0 {
					hasMoreVars = true
					break
				}
			}
			if hasMoreVars {
				fmt.Printf("  %s              │%s\n", color, rst)
				fmt.Printf("  %s              ▼%s\n", color, rst)
			}
		}
	}

	// Final resolution: show where each var's value actually came from.
	if len(varSources) > 0 {
		fmt.Println()
		fmt.Printf("  %s\n", bold("Variable Final Resolution:"))
		for _, vs := range varSources {
			val := fmt.Sprintf("%v", vs.Value)
			if strings.HasPrefix(val, "secret:") {
				val = red("secret:****")
			}
			src := medGray(vs.Source)
			fmt.Printf("    %s = %s  %s\n", slateBlue(vs.Key), val, src)
		}
	}
	fmt.Println()
}

// renderPasswordInheritance draws Section 4: secret references with masked values.
func renderPasswordInheritance(refs []secretTraceEntry, provider string) {
	if provider != "" {
		fmt.Printf("  %s %s\n\n", bold("Provider:"), provider)
	}

	// Group by module.
	type group struct {
		module string
		refs   []secretTraceEntry
	}
	order := []string{}
	byModule := map[string][]secretTraceEntry{}
	for _, ref := range refs {
		mod := ref.Module
		if mod == "" {
			mod = "(envoy-level)"
		}
		if _, ok := byModule[mod]; !ok {
			order = append(order, mod)
		}
		byModule[mod] = append(byModule[mod], ref)
	}

	for _, mod := range order {
		modRefs := byModule[mod]
		fmt.Printf("  %s\n", amber(mod))
		for _, ref := range modRefs {
			fmt.Printf("    %s  %s  %s\n",
				slateBlue(ref.Key),
				red("secret:****"),
				medGray("path: "+ref.Path+"  source: "+ref.Source),
			)
		}
	}
	fmt.Println()
}

// renderDependencyInheritance draws Section 5: dependency DAG.
func renderDependencyInheritance(deps []dependencyTraceEntry) {
	// Group by type.
	types := []string{"depends_on", "before", "notify", "subscribes", "watch_secret"}
	byType := map[string][]dependencyTraceEntry{}
	for _, d := range deps {
		byType[d.Type] = append(byType[d.Type], d)
	}

	for _, typ := range types {
		entries := byType[typ]
		if len(entries) == 0 {
			continue
		}
		fmt.Printf("  %s\n", bold(typ+":"))
		for _, d := range entries {
			arrow := " ──▶ "
			levelTag := ""
			if d.Level == "resource" {
				levelTag = medGray(" (resource)")
			}
			fmt.Printf("    %s%s%s%s\n", teal(d.From), arrow, teal(d.To), levelTag)
		}
		fmt.Println()
	}
}

// renderComplianceInheritance draws Section 6: compliance framework tags per module.
func renderComplianceInheritance(tags []complianceTraceEntry) {
	for _, ct := range tags {
		fmt.Printf("  %s\n", amber(ct.Module))

		// Sort framework names for consistent output.
		fwNames := make([]string, 0, len(ct.Frameworks))
		for fw := range ct.Frameworks {
			fwNames = append(fwNames, fw)
		}
		sort.Strings(fwNames)

		for _, fw := range fwNames {
			controls := ct.Frameworks[fw]
			fmt.Printf("    %s  %s\n", bold(fw+":"), strings.Join(controls, ", "))
		}
	}
	fmt.Println()
}

// renderWaiverInheritance draws waivers grouped by source file with box cards.
func renderWaiverInheritance(waivers []waiverTraceEntry) {
	// Group by source file, preserving order.
	type fileGroup struct {
		file    string
		entries []waiverTraceEntry
	}
	var groups []fileGroup
	fileIdx := make(map[string]int)
	for _, w := range waivers {
		src := w.SourceFile
		if src == "" {
			src = "waivers.vgo"
		}
		if idx, ok := fileIdx[src]; ok {
			groups[idx].entries = append(groups[idx].entries, w)
		} else {
			fileIdx[src] = len(groups)
			groups = append(groups, fileGroup{file: src, entries: []waiverTraceEntry{w}})
		}
	}

	total := len(groups)
	for i, g := range groups {
		color := greenRamp(i, total)
		colorBold := greenRampBold(i, total)
		rst := cReset()

		boxWidth := len(g.file) + 6
		if boxWidth < 40 {
			boxWidth = 40
		}
		topLine := "┌─ " + g.file + " " + strings.Repeat("─", boxWidth-len(g.file)-4) + "┐"
		botLine := "└" + strings.Repeat("─", boxWidth-1) + "┘"

		fmt.Printf("  %s%s%s\n", colorBold, topLine, rst)
		for _, w := range g.entries {
			expiry := ""
			if w.Expires != "" {
				expiry = medGray("  expires " + w.Expires)
			}
			fmt.Printf("  %s│%s %s  %s%s\n", color, rst, teal(w.Framework+"/"+w.Control), w.Reason, expiry)
			fmt.Printf("  %s│%s   %s %s\n", color, rst, medGray("approved by:"), w.ApprovedBy)
		}
		fmt.Printf("  %s%s%s\n", color, botLine, rst)

		if i < total-1 {
			fmt.Printf("  %s              │%s\n", color, rst)
			fmt.Printf("  %s              ▼%s\n", color, rst)
		}
	}
	fmt.Println()
}

// renderConditionalInheritance draws Section 7: when: expressions with eval side.
func renderConditionalInheritance(conds []conditionalTraceEntry) {
	for _, c := range conds {
		scope := amber(c.Module)
		if c.Resource != "" {
			scope += "/" + c.Resource
		}

		side := green("server")
		if c.EvalSide == "agent" {
			side = amber("agent")
		}

		fmt.Printf("  %s  %s  %s\n", scope, teal(c.When), side)
		if c.ResolvedWhen != "" && c.ResolvedWhen != c.When {
			scopeLen := len(c.Module)
			if c.Resource != "" {
				scopeLen += 1 + len(c.Resource)
			}
			fmt.Printf("  %s  %s\n", strings.Repeat(" ", scopeLen), medGray(c.ResolvedWhen))
		}
	}
	fmt.Println()
}

func init() {
	configTraceCmd.Flags().StringVar(&traceModuleFlag, "module", "", "Filter trace to show only the branch for this module")
	configCmd.AddCommand(configTraceCmd)
}
