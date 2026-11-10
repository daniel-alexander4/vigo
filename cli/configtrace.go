// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: AGPL-3.0-only

package main

import (
	"fmt"
	"net/url"
	"os"
	"sort"
	"strings"

	"github.com/spf13/cobra"
)

var configCmd = &cobra.Command{
	Use:   "config",
	Short: "Publish, inspect, freeze, approve, and rollback .vgo configuration",
}

var (
	traceConfigcrateFlag string
	traceHostFlag        string
	traceUsercrateFlag   string
	traceIncludeTreeFlag bool
)

var configTraceCmd = &cobra.Command{
	Use:               "trace [hostname]",
	Short:             "Show config resolution for a hostname, or where a configcrate/usercrate is applied across the fleet",
	ValidArgsFunction: completeHostnames,
	Long: `Forward (hostname-rooted):
  Traces config resolution for one hostname — directory inheritance, configcrate
  sources, var resolution, secret references, dependencies, compliance, and
  conditional evaluation. Use --configcrate to filter to a single configcrate's branch.

Inverse (subject-rooted):
  Use --configcrate or --usercrate (without a hostname) to find every enrolled
  envoy whose resolved config includes that configcrate or usercrate, plus the
  carrier (file/role/env) that pulled it in. Add --include-tree to also
  surface config-tree patterns that reference the subject but match no
  currently-enrolled envoy.

Examples:
  vigocli config trace danlap
  vigocli config trace --host danlap --configcrate nginx
  vigocli config trace --configcrate nginx
  vigocli config trace --usercrate alexander4 --include-tree`,
	Args: cobra.MaximumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		// Resolve the host subject: positional and --host are aliases.
		host := traceHostFlag
		if len(args) > 0 {
			if host != "" && host != args[0] {
				return fmt.Errorf("ambiguous host: positional %q and --host %q disagree", args[0], host)
			}
			host = args[0]
		}

		// Exactly one subject required: host, --module (inverse), or --usercrate (inverse).
		// --module *with* a host is the existing per-host filter, not a separate subject.
		inverseConfigcrate := host == "" && traceConfigcrateFlag != ""
		inverseUsercrate := host == "" && traceUsercrateFlag != ""

		switch {
		case host == "" && traceConfigcrateFlag == "" && traceUsercrateFlag == "":
			return fmt.Errorf("specify one of: <hostname>, --host, --configcrate, --usercrate")
		case host != "" && traceUsercrateFlag != "":
			return fmt.Errorf("--usercrate is an inverse subject; remove the hostname to query it")
		case inverseConfigcrate && inverseUsercrate:
			return fmt.Errorf("--configcrate and --usercrate are mutually exclusive in inverse mode")
		case traceIncludeTreeFlag && host != "":
			return fmt.Errorf("--include-tree only applies to inverse mode (no hostname)")
		}

		if inverseConfigcrate || inverseUsercrate {
			return runInverseTrace(inverseConfigcrate, inverseUsercrate)
		}

		// Forward (hostname-rooted) trace.
		client := newAPIClient()

		var trace traceResult

		apiURL := fmt.Sprintf("/api/v1/config/trace?hostname=%s", url.QueryEscape(host))
		if traceConfigcrateFlag != "" {
			apiURL += "&configcrate=" + url.QueryEscape(traceConfigcrateFlag)
		}

		if err := client.get(apiURL, &trace); err != nil {
			return err
		}

		if !trace.Matched {
			fmt.Printf("%s No config entry matches hostname %q\n", yellow("!"), host)
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
			renderInheritanceTree(trace.Inheritance, trace.EnvoyWaiverFiles)
			fmt.Println()
		}

		// Section 1a: Usercrates (auto-applied per-user config modules)
		if hasUsercrates(trace.Inheritance) {
			fmt.Println("  " + medGray("─────────────────────────────────────────"))
			fmt.Println()
			fmt.Println(bold("Usercrates:"))
			fmt.Println()
			renderUsercrates(trace.Inheritance)
		}

		// Separator between tree and module cards.
		if len(trace.Inheritance) > 0 {
			fmt.Println("  " + medGray("─────────────────────────────────────────"))
			fmt.Println()
		}

		// Section 2: Configcrate Inheritance (boxes with modules and roles, no vars).
		// Usercrates already have their own section above — skip them here so
		// they don't render twice as both a "Usercrates" row and a module box.
		configcrateLevels := levelsExcludingUsercrates(trace.Inheritance)
		if len(configcrateLevels) > 0 {
			fmt.Println(bold("Configcrate Inheritance:"))
			fmt.Println()
			renderConfigcrateCards(configcrateLevels, trace.RoleExpansion, trace.MatchPattern)
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

		// Section 6a: Configcrates-tree compliance.vgo provenance
		if len(trace.ConfigcrateComplianceFiles) > 0 {
			fmt.Println("  " + medGray("─────────────────────────────────────────"))
			fmt.Println()
			fmt.Println(bold("Compliance Sources:"))
			fmt.Println()
			renderConfigcrateComplianceSources(trace.ConfigcrateComplianceFiles)
		}

		// Section 7: Waivers Inheritance
		if len(trace.EnvoyWaivers) > 0 {
			fmt.Println("  " + medGray("─────────────────────────────────────────"))
			fmt.Println()
			fmt.Println(bold("Waivers Inheritance:"))
			fmt.Println()
			renderWaiverInheritance(trace.EnvoyWaivers)
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
	Hostname           string               `json:"hostname"`
	Matched            bool                 `json:"matched"`
	MatchPattern       string               `json:"match_pattern"`
	PatternType        string               `json:"pattern_type"`
	Environment        string               `json:"environment"`
	Roles              []string             `json:"roles"`
	RoleExpansion      []roleExpansionEntry `json:"role_expansion"`
	Inheritance        []inheritanceLevel   `json:"inheritance"`
	Configcrates       []string             `json:"configcrates"`
	ConfigcrateSources map[string]string    `json:"configcrate_sources"`
	VarSources         []struct {
		Key    string `json:"key"`
		Value  any    `json:"value"`
		Source string `json:"source"`
	} `json:"var_sources"`
	ConditionalVars []string `json:"conditional_vars"`
	ResourceCount   int      `json:"resource_count"`

	SecretRefs                 []secretTraceEntry               `json:"secret_refs"`
	SecretsProvider            string                           `json:"secrets_provider"`
	Dependencies               []dependencyTraceEntry           `json:"dependencies"`
	ComplianceTags             []complianceTraceEntry           `json:"compliance_tags"`
	ConfigcrateComplianceFiles []configcrateComplianceFileEntry `json:"configcrate_compliance_files"`
	EnvoyWaiverFiles           []string                         `json:"envoy_waiver_files"`
	EnvoyWaivers               []envoyWaiverTraceEntry          `json:"envoy_waivers"`
	Conditionals               []conditionalTraceEntry          `json:"conditionals"`
}

type configcrateComplianceFileEntry = struct {
	SourceFile          string              `json:"source_file"`
	Compliance          map[string][]string `json:"compliance"`
	CoveredConfigcrates []string            `json:"covered_configcrates"`
}

type secretTraceEntry = struct {
	Key         string `json:"key"`
	Path        string `json:"path"`
	Source      string `json:"source"`
	Configcrate string `json:"configcrate"`
}

type dependencyTraceEntry = struct {
	From  string `json:"from"`
	To    string `json:"to"`
	Type  string `json:"type"`
	Level string `json:"level"`
}

type complianceTraceEntry = struct {
	Configcrate    string                       `json:"configcrate"`
	Provides       []string                     `json:"provides,omitempty"`
	Bundle         []string                     `json:"bundle,omitempty"`
	Frameworks     map[string][]string          `json:"frameworks"`
	ControlSources map[string]map[string]string `json:"control_sources,omitempty"`
}

type envoyWaiverTraceEntry = struct {
	SourceFile string `json:"source_file"`
	Framework  string `json:"framework"`
	Control    string `json:"control"`
	Reason     string `json:"reason"`
	ApprovedBy string `json:"approved_by"`
	Expires    string `json:"expires"`
}

type conditionalTraceEntry = struct {
	Configcrate  string `json:"configcrate"`
	Resource     string `json:"resource"`
	When         string `json:"when"`
	ResolvedWhen string `json:"resolved_when"`
	EvalSide     string `json:"eval_side"`
}

// greenRamp returns a 24-bit ANSI color code interpolating from Catppuccin
// Green (#a6e3a1) to Catppuccin Teal (#94e2d5) based on depth within the
// chain. A minimum of 5 levels is used for the gradient so shallow traces
// don't jump straight to the end color.
func greenRamp(depth, total int) string {
	if !useColor || total <= 1 {
		return ""
	}
	effective := total
	if effective < 5 {
		effective = 5
	}
	t := float64(depth) / float64(effective-1)
	// Catppuccin Green (#a6e3a1 = 166,227,161) → Teal (#94e2d5 = 148,226,213)
	r := int(166 - t*18)
	g := int(227 - t*1)
	b := int(161 + t*52)
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
	Dir          string         `json:"dir"`
	File         string         `json:"file"`
	Configcrates []string       `json:"configcrates"`
	Roles        []string       `json:"roles"`
	Vars         map[string]any `json:"vars"`
	Overrides    []string       `json:"overrides"`
}

type roleExpansionEntry = struct {
	Role         string   `json:"role"`
	Includes     []string `json:"includes"`
	Configcrates []string `json:"configcrates"`
}

// renderInheritanceTree draws Section 1: the directory tree structure.
// Each directory level shows its files (common.vgo, compliance.vgo, leaf entry)
// as siblings, with subdirectories branching below.
func renderInheritanceTree(levels []inheritanceLevel, waiverFiles []string) {
	// Build a list of files at each directory level.
	type dirLevel struct {
		dir      string   // "" for root
		files    []string // files at this level (common.vgo, compliance.vgo, leaf.vgo)
		hasChild bool     // has a subdirectory after it
	}

	// Collect files per directory from inheritance levels.
	dirFiles := make(map[string][]string) // dir → files
	var dirOrder []string
	dirSeen := make(map[string]bool)
	for _, lvl := range levels {
		if !dirSeen[lvl.Dir] {
			dirSeen[lvl.Dir] = true
			dirOrder = append(dirOrder, lvl.Dir)
		}
		dirFiles[lvl.Dir] = append(dirFiles[lvl.Dir], lvl.File)
	}

	// Add waiver files at their directory level.
	for _, wf := range waiverFiles {
		parts := strings.Split(wf, "/")
		dir := ""
		if len(parts) > 1 {
			dir = strings.Join(parts[:len(parts)-1], "/")
		}
		file := parts[len(parts)-1]
		// Insert after common.vgo if present, otherwise append.
		existing := dirFiles[dir]
		inserted := false
		for i, f := range existing {
			if f == "common.vgo" {
				// Insert compliance.vgo right after common.vgo.
				tail := make([]string, len(existing[i+1:]))
				copy(tail, existing[i+1:])
				dirFiles[dir] = append(existing[:i+1], append([]string{file}, tail...)...)
				inserted = true
				break
			}
		}
		if !inserted {
			dirFiles[dir] = append(existing, file)
		}
		if !dirSeen[dir] {
			dirSeen[dir] = true
			dirOrder = append(dirOrder, dir)
		}
	}

	// Mark which levels have a child directory.
	var dirLevels []dirLevel
	for i, dir := range dirOrder {
		dl := dirLevel{dir: dir, files: dirFiles[dir]}
		if i < len(dirOrder)-1 {
			dl.hasChild = true
		}
		dirLevels = append(dirLevels, dl)
	}

	total := len(dirLevels)
	for i, dl := range dirLevels {
		color := greenRamp(i, total)
		colorBold := greenRampBold(i, total)
		rst := cReset()

		depth := 0
		if dl.dir != "" {
			depth = len(strings.Split(dl.dir, "/"))
		}
		indent := strings.Repeat("    ", depth)

		// Print directory name (except root which gets ● on its first file).
		if dl.dir != "" {
			parts := strings.Split(dl.dir, "/")
			dirName := parts[len(parts)-1]
			parentIndent := strings.Repeat("    ", depth-1)
			fmt.Printf("  %s%s└── %s%s\n", parentIndent, color, dirName, rst)
		}

		// Print directory header for root level.
		if i == 0 && dl.dir == "" {
			fmt.Printf("  %s%s● .live%s\n", indent, colorBold, rst)
		}

		// Print files at this level.
		for j, file := range dl.files {
			isLast := j == len(dl.files)-1 && !dl.hasChild
			connector := "├── "
			if isLast {
				connector = "└── "
			}
			fmt.Printf("  %s%s%s%s%s\n", indent, colorBold, connector, file, rst)
		}

		// Vertical connector to next level.
		if dl.hasChild {
			fmt.Printf("  %s%s│%s\n", indent, color, rst)
		}
	}
}

// renderConfigcrateCards draws Section 2: box cards with modules and roles (no vars).
func renderConfigcrateCards(levels []inheritanceLevel, roleExpansion []roleExpansionEntry, matchPattern string) {
	total := len(levels)

	// Build role → modules lookup.
	roleConfigcrates := make(map[string][]string)
	roleIncludes := make(map[string][]string)
	for _, re := range roleExpansion {
		roleConfigcrates[re.Role] = re.Configcrates
		roleIncludes[re.Role] = re.Includes
	}

	for i, lvl := range levels {
		// Skip levels that contribute no modules or roles.
		if len(lvl.Configcrates) == 0 && len(lvl.Roles) == 0 && !(i == total-1 && matchPattern != "") {
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

		// Configcrates.
		if len(lvl.Configcrates) > 0 {
			fmt.Printf("  %s│%s %s %s\n", color, rst, amber("configcrates:"), strings.Join(lvl.Configcrates, ", "))
		}

		// Roles with inline expansion.
		for _, roleName := range lvl.Roles {
			mods := roleConfigcrates[roleName]
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

	// Final resolution table: show where each var's value actually came from.
	if len(varSources) > 0 {
		fmt.Println()
		fmt.Printf("  %s\n\n", bold("Variable Final Resolution:"))
		w := newAlignedTable(os.Stdout)
		fmt.Fprintf(w, "    VARIABLE\tVALUE\tSOURCE\n")
		for _, vs := range varSources {
			val := fmt.Sprintf("%v", vs.Value)
			if strings.HasPrefix(val, "secret:") {
				val = "secret:****"
			}
			fmt.Fprintf(w, "    %s\t%s\t%s\n", vs.Key, val, vs.Source)
		}
		w.Flush()
	}
	fmt.Println()
}

// renderPasswordInheritance draws Section 4: secret references with masked values.
func renderPasswordInheritance(refs []secretTraceEntry, provider string) {
	if provider != "" {
		fmt.Printf("  %s %s\n\n", bold("Provider:"), provider)
	}

	// Group by module.
	order := []string{}
	byConfigcrate := map[string][]secretTraceEntry{}
	for _, ref := range refs {
		mod := ref.Configcrate
		if mod == "" {
			mod = "(envoy-level)"
		}
		if _, ok := byConfigcrate[mod]; !ok {
			order = append(order, mod)
		}
		byConfigcrate[mod] = append(byConfigcrate[mod], ref)
	}

	for _, mod := range order {
		modRefs := byConfigcrate[mod]
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

// renderDependencyInheritance draws Section 5: dependency DAG as a table.
func renderDependencyInheritance(deps []dependencyTraceEntry) {
	w := newAlignedTable(os.Stdout)
	fmt.Fprintf(w, "    TYPE\tFROM\t \tTO\n")
	for _, d := range deps {
		level := ""
		if d.Level == "resource" {
			level = " (resource)"
		}
		fmt.Fprintf(w, "    %s\t%s\t▶\t%s%s\n", d.Type, d.From, d.To, level)
	}
	w.Flush()
	fmt.Println()
}

// renderComplianceInheritance draws Section 6: compliance framework
// tags per module. When the module declares `provides:` or `bundle:`,
// each control is annotated with its source — `(via <provision>)` /
// `(via bundle:<id>)` for catalog-derived controls, no suffix for
// explicit `compliance:` claims. The module header lists the
// provides/bundle tags so the operator sees "this module declared
// time-sync" / "this module implements cis-ubuntu-access" before
// reading the expanded controls.
func renderComplianceInheritance(tags []complianceTraceEntry) {
	for _, ct := range tags {
		header := amber(ct.Configcrate)
		if len(ct.Provides) > 0 {
			header = header + dim("  provides: ["+strings.Join(ct.Provides, ", ")+"]")
		}
		if len(ct.Bundle) > 0 {
			header = header + dim("  bundle: ["+strings.Join(ct.Bundle, ", ")+"]")
		}
		fmt.Printf("  %s\n", header)

		// Sort framework names for consistent output.
		fwNames := make([]string, 0, len(ct.Frameworks))
		for fw := range ct.Frameworks {
			fwNames = append(fwNames, fw)
		}
		sort.Strings(fwNames)

		for _, fw := range fwNames {
			controls := ct.Frameworks[fw]
			if len(ct.ControlSources) == 0 {
				// No provides/bundle on this module — render the
				// legacy compact form (all controls one line).
				fmt.Printf("    %s  %s\n", bold(fw+":"), strings.Join(controls, ", "))
				continue
			}
			// Annotate each control with its source. Sort within a
			// framework so the output is stable.
			sortedControls := append([]string(nil), controls...)
			sort.Strings(sortedControls)
			for _, cid := range sortedControls {
				src := ct.ControlSources[fw][cid]
				suffix := ""
				switch {
				case strings.HasPrefix(src, "provides:"):
					suffix = "  " + dim("(via "+strings.TrimPrefix(src, "provides:")+")")
				case strings.HasPrefix(src, "bundle:"):
					suffix = "  " + dim("(via bundle:"+strings.TrimPrefix(src, "bundle:")+")")
				}
				fmt.Printf("    %s  %s%s\n", bold(fw+":"), cid, suffix)
			}
		}
	}
	fmt.Println()
}

// renderConfigcrateComplianceSources draws the Compliance Sources section: one
// box card per modules-tree compliance.vgo file that attached claims to any
// of the envoy's effective modules. Each card lists the frameworks + controls
// the file contributed and the modules it covered, so an operator can see
// exactly why a given module carries a given compliance tag.
func renderConfigcrateComplianceSources(files []configcrateComplianceFileEntry) {
	total := len(files)
	for i, f := range files {
		color := greenRamp(i, total)
		colorBold := greenRampBold(i, total)
		rst := cReset()

		boxWidth := len(f.SourceFile) + 6
		if boxWidth < 40 {
			boxWidth = 40
		}
		topLine := "┌─ " + f.SourceFile + " " + strings.Repeat("─", boxWidth-len(f.SourceFile)-4) + "┐"
		botLine := "└" + strings.Repeat("─", boxWidth-1) + "┘"

		fmt.Printf("  %s%s%s\n", colorBold, topLine, rst)

		fwNames := make([]string, 0, len(f.Compliance))
		for fw := range f.Compliance {
			fwNames = append(fwNames, fw)
		}
		sort.Strings(fwNames)
		for _, fw := range fwNames {
			controls := f.Compliance[fw]
			fmt.Printf("  %s│%s %s %s\n", color, rst, teal(fw+":"), strings.Join(controls, ", "))
		}

		if len(f.CoveredConfigcrates) > 0 {
			fmt.Printf("  %s│%s %s %s\n", color, rst, medGray("configcrates:"), strings.Join(f.CoveredConfigcrates, ", "))
		}

		fmt.Printf("  %s%s%s\n", color, botLine, rst)

		if i < total-1 {
			fmt.Printf("  %s              │%s\n", color, rst)
			fmt.Printf("  %s              ▼%s\n", color, rst)
		}
	}
	fmt.Println()
}

// renderWaiverInheritance draws waivers grouped by source file with box cards.
func renderWaiverInheritance(waivers []envoyWaiverTraceEntry) {
	// Group by source file, preserving order.
	type fileGroup struct {
		file    string
		entries []envoyWaiverTraceEntry
	}
	var groups []fileGroup
	fileIdx := make(map[string]int)
	for _, w := range waivers {
		src := w.SourceFile
		if src == "" {
			src = "compliance.vgo"
		}
		if idx, ok := fileIdx[src]; ok {
			groups[idx].entries = append(groups[idx].entries, w)
		} else {
			fileIdx[src] = len(groups)
			groups = append(groups, fileGroup{file: src, entries: []envoyWaiverTraceEntry{w}})
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

// renderConditionalInheritance draws Section 7: when: expressions as a table
// grouped by evaluation side (server first, then agent).
func renderConditionalInheritance(conds []conditionalTraceEntry) {
	// Split into server and agent groups.
	var serverConds, agentConds []conditionalTraceEntry
	for _, c := range conds {
		if c.EvalSide == "server" {
			serverConds = append(serverConds, c)
		} else {
			agentConds = append(agentConds, c)
		}
	}

	printCondGroup := func(label string, group []conditionalTraceEntry) {
		if len(group) == 0 {
			return
		}
		fmt.Printf("  %s %s\n\n", bold(label), medGray(fmt.Sprintf("(%d)", len(group))))
		w := newAlignedTable(os.Stdout)
		fmt.Fprintf(w, "    RESOURCE\tWHEN\n")
		for _, c := range group {
			scope := c.Configcrate
			if c.Resource != "" {
				scope += "/" + c.Resource
			}
			fmt.Fprintf(w, "    %s\t%s\n", scope, c.When)
			if c.ResolvedWhen != "" && c.ResolvedWhen != c.When {
				fmt.Fprintf(w, "    \t↳ %s\n", c.ResolvedWhen)
			}
		}
		w.Flush()
		fmt.Println()
	}

	printCondGroup("Server-evaluated:", serverConds)
	printCondGroup("Agent-evaluated:", agentConds)
}

// hasUsercrates reports whether any inheritance level carries auto-applied
// usercrate entries. Used to gate the "Usercrates:" section header so the
// separator + heading don't render for stackss without usercrates.
func hasUsercrates(levels []inheritanceLevel) bool {
	for _, lvl := range levels {
		if lvl.File == "usercrates/" && len(lvl.Configcrates) > 0 {
			return true
		}
	}
	return false
}

// levelsExcludingUsercrates returns the inheritance levels with the
// auto-applied usercrate entries filtered out. The Usercrates section
// renders those separately; letting them fall through to the module
// cards would duplicate the usercrate listing.
func levelsExcludingUsercrates(levels []inheritanceLevel) []inheritanceLevel {
	out := make([]inheritanceLevel, 0, len(levels))
	for _, lvl := range levels {
		if lvl.File == "usercrates/" {
			continue
		}
		out = append(out, lvl)
	}
	return out
}

// renderUsercrates draws the Usercrates section: one line per
// auto-applied usercrate with the scope directory where it lives, so
// operators can quickly see which per-user configs this envoy picks
// up and from which level of the stacks tree. Sources the data
// from inheritance levels tagged File == "usercrates/".
func renderUsercrates(levels []inheritanceLevel) {
	// Collect (scope, usercrate-name) pairs preserving parent→child order.
	type usercrateRow struct {
		scope string
		name  string
	}
	var rows []usercrateRow
	for _, lvl := range levels {
		if lvl.File != "usercrates/" {
			continue
		}
		scope := lvl.Dir
		if scope == "" {
			scope = "(fleet-wide)"
		}
		for _, name := range lvl.Configcrates {
			rows = append(rows, usercrateRow{scope: scope, name: name})
		}
	}
	if len(rows) == 0 {
		fmt.Printf("  %s\n\n", medGray("(none auto-applied)"))
		return
	}
	rst := cReset()
	for _, row := range rows {
		fmt.Printf("  %s%s%s  %s\n", colorBold(""), amber(row.name), rst, medGray("← "+row.scope+"/usercrates/"))
	}
	fmt.Println()
}

// colorBold is a no-arg helper so renderUsercrates can keep colorBold()
// consistent with other sections without threading a ramp depth —
// usercrates aren't an inheritance chain, they're a bag.
func colorBold(_ string) string {
	if !useColor {
		return ""
	}
	return "\x1b[1m"
}

// inverseTraceResult mirrors server/config.InverseTraceResult on the wire.
type inverseTraceResult struct {
	Subject     string              `json:"subject"`
	Value       string              `json:"value"`
	IncludeTree bool                `json:"include_tree"`
	Envoys      []inverseEnvoyEntry `json:"envoys"`
	TreeRefs    []inverseTreeRef    `json:"tree_refs,omitempty"`
}

type inverseEnvoyEntry struct {
	EnvoyID     string `json:"envoy_id"`
	Hostname    string `json:"hostname"`
	Match       string `json:"match"`
	Environment string `json:"environment,omitempty"`
	Carrier     string `json:"carrier"`
}

type inverseTreeRef struct {
	Match       string `json:"match"`
	Environment string `json:"environment,omitempty"`
	Carrier     string `json:"carrier"`
}

// runInverseTrace fetches and renders an inverse trace — answers
// "where is this module/usercrate applied?" across the fleet.
func runInverseTrace(asConfigcrate, asUsercrate bool) error {
	subject := "configcrate"
	value := traceConfigcrateFlag
	if asUsercrate {
		subject = "usercrate"
		value = traceUsercrateFlag
	}
	_ = asConfigcrate

	params := url.Values{}
	params.Set(subject, value)
	if traceIncludeTreeFlag {
		params.Set("include_tree", "true")
	}

	client := newAPIClient()
	var result inverseTraceResult
	if err := client.get("/api/v1/config/trace?"+params.Encode(), &result); err != nil {
		return err
	}

	heading := fmt.Sprintf("%s %s", bold(subject+":"), amber(value))
	fmt.Println(heading)
	fmt.Println()

	if len(result.Envoys) == 0 && len(result.TreeRefs) == 0 {
		if traceIncludeTreeFlag {
			fmt.Printf("  %s\n", medGray("Not applied on any enrolled envoy, and no config-tree pattern references it."))
		} else {
			fmt.Printf("  %s\n", medGray("Not applied on any enrolled envoy."))
			fmt.Printf("  %s\n", medGray("(use --include-tree to also surface unmatched config-tree patterns.)"))
		}
		return nil
	}

	if len(result.Envoys) > 0 {
		fmt.Println(bold("Enrolled envoys:"))
		fmt.Println()
		w := newAlignedTable(os.Stdout)
		fmt.Fprintf(w, "    HOSTNAME\tMATCH\tENV\tCARRIER\n")
		for _, e := range result.Envoys {
			env := e.Environment
			if env == "" {
				env = medGray("—")
			}
			fmt.Fprintf(w, "    %s\t%s\t%s\t%s\n", e.Hostname, amber(e.Match), env, medGray(e.Carrier))
		}
		w.Flush()
		fmt.Println()
	}

	if len(result.TreeRefs) > 0 {
		fmt.Println(bold("Config-tree patterns (no enrolled envoy matches):"))
		fmt.Println()
		w := newAlignedTable(os.Stdout)
		fmt.Fprintf(w, "    MATCH\tENV\tCARRIER\n")
		for _, tr := range result.TreeRefs {
			env := tr.Environment
			if env == "" {
				env = medGray("—")
			}
			fmt.Fprintf(w, "    %s\t%s\t%s\n", amber(tr.Match), env, medGray(tr.Carrier))
		}
		w.Flush()
		fmt.Println()
	}

	summary := fmt.Sprintf("%d envoy", len(result.Envoys))
	if len(result.Envoys) != 1 {
		summary += "s"
	}
	if traceIncludeTreeFlag {
		summary += fmt.Sprintf(", %d config-tree pattern", len(result.TreeRefs))
		if len(result.TreeRefs) != 1 {
			summary += "s"
		}
	}
	fmt.Printf("  %s\n", medGray(summary+"."))
	return nil
}

func init() {
	configTraceCmd.Flags().StringVar(&traceConfigcrateFlag, "configcrate", "", "With <hostname>: filter trace to this configcrate's branch. Without <hostname>: find where this configcrate is applied.")
	configTraceCmd.Flags().StringVar(&traceHostFlag, "host", "", "Hostname to trace (alias for the positional argument)")
	configTraceCmd.Flags().StringVar(&traceUsercrateFlag, "usercrate", "", "Find where this usercrate is applied across the fleet")
	configTraceCmd.Flags().BoolVar(&traceIncludeTreeFlag, "include-tree", false, "Inverse mode only: also include config-tree patterns no enrolled envoy matches")
	configCmd.AddCommand(configTraceCmd)
}
