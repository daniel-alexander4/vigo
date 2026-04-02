// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

// Package modlint validates, repairs, normalizes, and auto-comments Vigo
// module definitions (.vgo files). Depends only on server/validate for the
// shared KnownTypes list. Suitable for use in vigosrv, vigocli, and the
// community website (Google Cloud Functions).
//
// The pipeline:
//  1. YAML repair — fix indentation, tabs→spaces, duplicate keys, unquoted
//     special values, trailing whitespace, missing final newline.
//  2. Structure normalization — canonical key ordering, consistent 2-space
//     indentation, normalized quoting.
//  3. Schema validation — required fields per resource type, unknown types,
//     cross-references (depends_on, notify, subscribes), DAG cycles,
//     idempotency checks.
//  4. Auto-commenting — #~ prefixed comments regenerated on every run.
//     Hand-written # comments are preserved.
package modlint

import (
	"fmt"
	"regexp"
	"sort"
	"strings"

	vigovalidate "github.com/daniel-alexander4/vigo/server/validate"
	"gopkg.in/yaml.v3"
)

// Options controls linter behavior.
type Options struct {
	// CommunityUpload adds GPL-3.0 header and rejects secret: references.
	CommunityUpload bool
	// Author is injected into the module header for community uploads.
	Author string
	// Version is injected into the module header for community uploads.
	Version string
}

// Result holds the output of a lint pass.
type Result struct {
	// Output is the cleaned, normalized, commented YAML.
	Output string
	// Warnings are non-fatal issues that were auto-fixed.
	Warnings []string
	// Errors are fatal issues that could not be auto-fixed.
	Errors []string
}

// OK returns true if there are no errors.
func (r *Result) OK() bool {
	return len(r.Errors) == 0
}

// Lint runs the full validation/repair/comment pipeline on raw module YAML.
func Lint(input []byte, opts Options) *Result {
	r := &Result{}

	// Step 1: YAML repair.
	repaired := repair(input, r)

	// Step 2: Parse into structured form.
	mod, err := parse(repaired)
	if err != nil {
		r.Errors = append(r.Errors, fmt.Sprintf("YAML parse error after repair: %s", err))
		return r
	}

	// Step 3: Schema validation.
	validate(mod, opts, r)

	// Step 4: Normalize + auto-comment + serialize.
	r.Output = render(mod, opts, r)

	return r
}

// --- Module representation ---

// module is the internal representation of a parsed .vgo module.
type module struct {
	Name            string
	DependsOn       []string
	Before          []string
	Defaults        map[string]any
	Compliance      map[string][]string
	ComplianceOrder []string // preserve original framework key order
	Vars            map[string]any
	VarOrder        []string // preserve original key order
	Resources       []resource
}

// resource is the internal representation of a parsed resource.
type resource struct {
	Name       string
	Type       string
	When       string
	Notify     []string
	Subscribes []string
	DependsOn  []string
	Before     []string
	Attrs      map[string]any
	AttrOrder  []string // preserve original key order
}

// --- Step 1: YAML Repair ---

// repair fixes common YAML syntax issues at the text level before parsing.
func repair(input []byte, r *Result) []byte {
	text := string(input)

	// Strip UTF-8 BOM if present.
	text = strings.TrimPrefix(text, "\xef\xbb\xbf")

	// Strip leading non-ASCII bytes before the first YAML content.
	// Copy-paste from terminals can inject characters like ● (U+25CF)
	// from systemd status output.
	if len(text) > 0 && text[0] > 127 {
		for i := 0; i < len(text); i++ {
			if text[i] <= 127 {
				text = text[i:]
				r.Warnings = append(r.Warnings, "stripped leading non-ASCII bytes")
				break
			}
		}
	}

	// Normalize line endings.
	text = strings.ReplaceAll(text, "\r\n", "\n")
	text = strings.ReplaceAll(text, "\r", "\n")

	lines := strings.Split(text, "\n")
	var out []string

	for i, line := range lines {
		original := line

		// Tabs → 2 spaces.
		if strings.Contains(line, "\t") {
			line = strings.ReplaceAll(line, "\t", "  ")
			if line != original {
				r.Warnings = append(r.Warnings, fmt.Sprintf("line %d: converted tabs to spaces", i+1))
			}
		}

		// Trailing whitespace.
		trimmed := strings.TrimRight(line, " \t")
		if trimmed != line {
			line = trimmed
		}

		out = append(out, line)
	}

	// Detect over-indentation: if the first non-empty line starts at column 0
	// (e.g., "name: foo") but all subsequent non-empty, non-comment lines are
	// indented, the user likely pasted a role definition as a module file.
	// Auto-fix by stripping the common leading whitespace.
	out = fixOverIndentation(out, r)

	// Ensure final newline.
	result := strings.Join(out, "\n")
	if !strings.HasSuffix(result, "\n") {
		result += "\n"
	}

	// Fix unquoted YAML boolean/null values in scalar positions.
	// This is applied after joining so we can use line-based regex.
	result = fixUnquotedSpecials(result, r)

	return []byte(result)
}

// yamlBooleans are values that YAML 1.1 treats as booleans but users often
// intend as strings. We only fix values in key: value positions (not in
// content blocks or comments).
var yamlBoolPattern = regexp.MustCompile(`(?m)^(\s*\w[\w_]*:\s+)(yes|no|on|off|Yes|No|YES|NO|On|Off|ON|OFF)(\s*(?:#.*)?)$`)

// fixOverIndentation detects when the first line is unindented (e.g., "name: foo")
// but all subsequent non-empty lines share a common indent prefix. This happens
// when a role definition is pasted into a module file. Strips the common indent
// and warns.
func fixOverIndentation(lines []string, r *Result) []string {
	// Find the first non-empty line.
	firstIdx := -1
	for i, line := range lines {
		if strings.TrimSpace(line) != "" {
			firstIdx = i
			break
		}
	}
	if firstIdx < 0 {
		return lines
	}

	// First non-empty line must start at column 0.
	firstLine := lines[firstIdx]
	if len(firstLine) > 0 && (firstLine[0] == ' ' || firstLine[0] == '\t') {
		return lines // First line is also indented — not this pattern.
	}

	// Find minimum indent of all subsequent non-empty, non-comment lines.
	minIndent := -1
	hasSubsequent := false
	for i := firstIdx + 1; i < len(lines); i++ {
		trimmed := strings.TrimSpace(lines[i])
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		hasSubsequent = true
		indent := len(lines[i]) - len(strings.TrimLeft(lines[i], " "))
		if indent == 0 {
			return lines // At least one subsequent line is at column 0 — not over-indented.
		}
		if minIndent < 0 || indent < minIndent {
			minIndent = indent
		}
	}

	if !hasSubsequent || minIndent <= 0 {
		return lines
	}

	// All subsequent lines are indented by at least minIndent. Strip it.
	prefix := strings.Repeat(" ", minIndent)
	fixed := make([]string, len(lines))
	for i, line := range lines {
		if i <= firstIdx {
			fixed[i] = line
		} else {
			fixed[i] = strings.TrimPrefix(line, prefix)
		}
	}
	r.Warnings = append(r.Warnings, fmt.Sprintf(
		"stripped %d leading spaces from all lines after 'name:' — this looks like a role definition pasted as a module file",
		minIndent,
	))
	return fixed
}

func fixUnquotedSpecials(text string, r *Result) string {
	return yamlBoolPattern.ReplaceAllStringFunc(text, func(match string) string {
		parts := yamlBoolPattern.FindStringSubmatch(match)
		if len(parts) < 3 {
			return match
		}
		prefix, val, suffix := parts[1], parts[2], parts[3]
		replacement := "true"
		lower := strings.ToLower(val)
		if lower == "no" || lower == "off" {
			replacement = "false"
		}
		r.Warnings = append(r.Warnings, fmt.Sprintf("converted unquoted %q to %s", val, replacement))
		return prefix + replacement + suffix
	})
}

// --- Step 2: Parse ---

func parse(data []byte) (*module, error) {
	var node yaml.Node
	if err := yaml.Unmarshal(data, &node); err != nil {
		return nil, err
	}

	if node.Kind != yaml.DocumentNode || len(node.Content) == 0 {
		return nil, fmt.Errorf("empty document")
	}
	root := node.Content[0]
	if root.Kind != yaml.MappingNode {
		return nil, fmt.Errorf("expected mapping at root, got %v", root.Kind)
	}

	mod := &module{
		Vars: make(map[string]any),
	}

	seen := make(map[string]bool)
	for i := 0; i < len(root.Content)-1; i += 2 {
		key := root.Content[i].Value
		val := root.Content[i+1]

		if seen[key] {
			// Duplicate key — keep last (YAML spec behavior).
			continue
		}
		seen[key] = true

		switch key {
		case "name":
			mod.Name = val.Value
		case "depends_on":
			if err := val.Decode(&mod.DependsOn); err != nil {
				return nil, fmt.Errorf("decoding depends_on: %w", err)
			}
		case "before":
			if err := val.Decode(&mod.Before); err != nil {
				return nil, fmt.Errorf("decoding before: %w", err)
			}
		case "defaults":
			if err := val.Decode(&mod.Defaults); err != nil {
				return nil, fmt.Errorf("decoding defaults: %w", err)
			}
		case "compliance":
			if val.Kind == yaml.MappingNode {
				mod.ComplianceOrder = extractKeyOrder(val)
			}
			if err := val.Decode(&mod.Compliance); err != nil {
				return nil, fmt.Errorf("decoding compliance: %w", err)
			}
		case "vars":
			if val.Kind == yaml.MappingNode {
				mod.VarOrder = extractKeyOrder(val)
			}
			if err := val.Decode(&mod.Vars); err != nil {
				return nil, fmt.Errorf("decoding vars: %w", err)
			}
		case "resources":
			if val.Kind != yaml.SequenceNode {
				return nil, fmt.Errorf("resources: expected sequence")
			}
			resources, err := parseResources(val.Content)
			if err != nil {
				return nil, fmt.Errorf("parsing resources: %w", err)
			}
			mod.Resources = resources
		}
	}

	return mod, nil
}

func parseResources(nodes []*yaml.Node) ([]resource, error) {
	var result []resource
	for _, node := range nodes {
		if node.Kind != yaml.MappingNode {
			return nil, fmt.Errorf("expected mapping for resource")
		}
		res, err := parseOneResource(node)
		if err != nil {
			return nil, err
		}
		result = append(result, res)
	}
	return result, nil
}

func parseOneResource(node *yaml.Node) (resource, error) {
	var res resource
	res.Attrs = make(map[string]any)

	// Structural fields we pull out explicitly.
	structural := map[string]bool{
		"name": true, "type": true, "when": true,
		"notify": true, "subscribes": true,
		"depends_on": true, "before": true,
	}

	seen := make(map[string]bool)
	for i := 0; i < len(node.Content)-1; i += 2 {
		key := node.Content[i].Value
		val := node.Content[i+1]

		if seen[key] {
			// Duplicate — skip, keep first.
			continue
		}
		seen[key] = true

		switch key {
		case "name":
			res.Name = val.Value
		case "type":
			res.Type = val.Value
		case "when":
			res.When = val.Value
		case "notify":
			if err := val.Decode(&res.Notify); err != nil {
				return res, fmt.Errorf("decoding notify: %w", err)
			}
		case "subscribes":
			if err := val.Decode(&res.Subscribes); err != nil {
				return res, fmt.Errorf("decoding subscribes: %w", err)
			}
		case "depends_on":
			if err := val.Decode(&res.DependsOn); err != nil {
				return res, fmt.Errorf("decoding depends_on: %w", err)
			}
		case "before":
			if err := val.Decode(&res.Before); err != nil {
				return res, fmt.Errorf("decoding before: %w", err)
			}
		default:
			if !structural[key] {
				var v any
				if err := val.Decode(&v); err != nil {
					return res, fmt.Errorf("decoding %q: %w", key, err)
				}
				// YAML parses bare octal like 0644 as int 420. Coerce
				// mode values back to an octal string so "0644" round-trips.
				if key == "mode" {
					if n, ok := v.(int); ok {
						v = fmt.Sprintf("%04o", n)
					}
				}
				res.Attrs[key] = v
				res.AttrOrder = append(res.AttrOrder, key)
			}
		}
	}

	return res, nil
}

// extractKeyOrder returns the key names from a YAML mapping node in order.
func extractKeyOrder(node *yaml.Node) []string {
	if node.Kind != yaml.MappingNode {
		return nil
	}
	var keys []string
	for i := 0; i < len(node.Content)-1; i += 2 {
		keys = append(keys, node.Content[i].Value)
	}
	return keys
}

// --- Step 3: Validate ---

func validate(mod *module, opts Options, r *Result) {
	// Structural checks that aren't in validate.ValidateModule.
	if mod.Name == "" {
		r.Errors = append(r.Errors, "missing required 'name' field")
	}
	if len(mod.Resources) == 0 {
		r.Errors = append(r.Errors, "module has no resources")
	}

	// Check for duplicate resource names.
	names := make(map[string]bool, len(mod.Resources))
	for _, res := range mod.Resources {
		if res.Name != "" {
			if names[res.Name] {
				r.Errors = append(r.Errors, fmt.Sprintf("duplicate resource name %q", res.Name))
			}
			names[res.Name] = true
		}
	}

	// Delegate type-specific and cross-reference validation to the
	// canonical validator. This ensures modlint and the server use
	// identical validation rules.
	vm := toValidateModule(mod)
	findings := vigovalidate.ValidateModule(vm)
	for _, f := range findings {
		if f.Severity == vigovalidate.SeverityError {
			r.Errors = append(r.Errors, f.Message)
		} else {
			r.Warnings = append(r.Warnings, f.Message)
		}
	}

	// Auto-fix: service with state restarted/reloaded + subscribes but no
	// when: "changed" will fire every cycle. Add the guard automatically.
	for i := range mod.Resources {
		res := &mod.Resources[i]
		if res.Type != "service" {
			continue
		}
		state := attrStr(res.Attrs, "state")
		if state != "restarted" && state != "reloaded" {
			continue
		}
		if len(res.Subscribes) > 0 && !strings.Contains(res.When, "changed") {
			res.When = "changed"
			r.Warnings = append(r.Warnings, fmt.Sprintf("resource %q: added when: \"changed\" — service with state %q and subscribes must be gated to prevent restart every cycle", res.Name, state))
		}
	}

	// Community: reject secret: references.
	if opts.CommunityUpload {
		for _, res := range mod.Resources {
			for _, key := range res.AttrOrder {
				if s, ok := res.Attrs[key].(string); ok && strings.HasPrefix(s, "secret:") {
					r.Errors = append(r.Errors, fmt.Sprintf("resource %q: community modules cannot use secret: references (attribute %q)", res.Name, key))
				}
			}
		}
	}
}

// toValidateModule converts a modlint module to a validate.Module.
func toValidateModule(mod *module) vigovalidate.Module {
	vm := vigovalidate.Module{Name: mod.Name}
	for _, res := range mod.Resources {
		vm.Resources = append(vm.Resources, vigovalidate.Resource{
			Name:        res.Name,
			Type:        res.Type,
			When:        res.When,
			Attributes:  res.Attrs,
			DependsOn:   res.DependsOn,
			Before:      res.Before,
			Notify:      res.Notify,
			Subscribes:  res.Subscribes,
		})
	}
	return vm
}


// --- Step 4: Render ---

// render produces clean, normalized, auto-commented YAML.
func render(mod *module, opts Options, r *Result) string {
	var b strings.Builder

	// GPL-3.0 header for community uploads.
	if opts.CommunityUpload {
		b.WriteString("# SPDX-License-Identifier: GPL-3.0-or-later\n")
		if opts.Author != "" {
			b.WriteString(fmt.Sprintf("# Author: %s\n", opts.Author))
		}
		if opts.Version != "" {
			b.WriteString(fmt.Sprintf("# Version: %s\n", opts.Version))
		}
		b.WriteString("\n")
	}

	// Module-level block comment.
	b.WriteString(fmt.Sprintf("#~ %s\n", generateModuleComment(mod)))
	b.WriteString(fmt.Sprintf("name: %s\n", mod.Name))

	// Module-level depends_on.
	if len(mod.DependsOn) > 0 {
		b.WriteString(fmt.Sprintf("depends_on: [%s]\n", strings.Join(mod.DependsOn, ", ")))
	}
	if len(mod.Before) > 0 {
		b.WriteString(fmt.Sprintf("before: [%s]\n", strings.Join(mod.Before, ", ")))
	}

	// Compliance.
	if len(mod.Compliance) > 0 {
		b.WriteString("\ncompliance:\n")
		order := mod.ComplianceOrder
		if len(order) == 0 {
			order = sortedKeys(mod.Compliance)
		}
		for _, fw := range order {
			controls, ok := mod.Compliance[fw]
			if !ok {
				continue
			}
			quoted := make([]string, len(controls))
			for i, c := range controls {
				quoted[i] = yamlScalar(c)
			}
			b.WriteString(fmt.Sprintf("  %s: [%s]\n", fw, strings.Join(quoted, ", ")))
		}
	}

	// Defaults.
	if len(mod.Defaults) > 0 {
		b.WriteString("\ndefaults:\n")
		for _, k := range sortedKeys(mod.Defaults) {
			b.WriteString(fmt.Sprintf("  %s: %s\n", k, yamlScalar(mod.Defaults[k])))
		}
	}

	// Vars.
	if len(mod.Vars) > 0 {
		b.WriteString("\nvars:\n")
		order := mod.VarOrder
		if len(order) == 0 {
			order = sortedKeys(mod.Vars)
		}
		for _, k := range order {
			v, ok := mod.Vars[k]
			if !ok {
				continue
			}
			b.WriteString(fmt.Sprintf("  %s: %s\n", k, yamlScalar(v)))
		}
	}

	// Resources.
	if len(mod.Resources) > 0 {
		b.WriteString("\nresources:\n")
		for i, res := range mod.Resources {
			if i > 0 {
				b.WriteString("\n")
			}
			renderResource(&b, res)
		}
	}

	return b.String()
}

// renderResource writes a single resource with #~ comment.
func renderResource(b *strings.Builder, res resource) {
	comment := generateResourceComment(res)
	b.WriteString(fmt.Sprintf("  #~ %s\n", comment))
	b.WriteString(fmt.Sprintf("  - name: %s\n", res.Name))
	b.WriteString(fmt.Sprintf("    type: %s\n", res.Type))

	if res.When != "" {
		b.WriteString(fmt.Sprintf("    when: %s\n", yamlScalar(res.When)))
	}

	// Type-specific attributes in a sensible order.
	attrOrder := canonicalAttrOrder(res)
	for _, key := range attrOrder {
		v := res.Attrs[key]
		writeAttr(b, key, v, 4)
	}

	// Relationship fields last.
	if len(res.DependsOn) > 0 {
		b.WriteString(fmt.Sprintf("    depends_on: [%s]\n", strings.Join(res.DependsOn, ", ")))
	}
	if len(res.Before) > 0 {
		b.WriteString(fmt.Sprintf("    before: [%s]\n", strings.Join(res.Before, ", ")))
	}
	if len(res.Notify) > 0 {
		b.WriteString(fmt.Sprintf("    notify: [%s]\n", strings.Join(res.Notify, ", ")))
	}
	if len(res.Subscribes) > 0 {
		b.WriteString(fmt.Sprintf("    subscribes: [%s]\n", strings.Join(res.Subscribes, ", ")))
	}
}

// writeAttr writes a key-value pair with proper indentation and multiline handling.
func writeAttr(b *strings.Builder, key string, v any, indent int) {
	prefix := strings.Repeat(" ", indent)

	switch val := v.(type) {
	case string:
		if strings.Contains(val, "\n") {
			b.WriteString(fmt.Sprintf("%s%s: |\n", prefix, key))
			for _, line := range strings.Split(val, "\n") {
				if line == "" {
					b.WriteString("\n")
				} else {
					b.WriteString(fmt.Sprintf("%s  %s\n", prefix, line))
				}
			}
		} else {
			b.WriteString(fmt.Sprintf("%s%s: %s\n", prefix, key, yamlScalar(val)))
		}
	case []any:
		b.WriteString(fmt.Sprintf("%s%s:\n", prefix, key))
		for _, item := range val {
			b.WriteString(fmt.Sprintf("%s  - %s\n", prefix, yamlScalar(item)))
		}
	case map[string]any:
		b.WriteString(fmt.Sprintf("%s%s:\n", prefix, key))
		for _, k := range sortedKeys(val) {
			writeAttr(b, k, val[k], indent+2)
		}
	default:
		b.WriteString(fmt.Sprintf("%s%s: %s\n", prefix, key, yamlScalar(v)))
	}
}

// --- Comment generation ---

// generateModuleComment creates a block-level #~ comment describing the module.
func generateModuleComment(mod *module) string {
	if len(mod.Resources) == 0 {
		return mod.Name
	}

	// Collect unique resource types.
	types := make(map[string]bool)
	for _, res := range mod.Resources {
		types[res.Type] = true
	}
	typeList := sortedKeys(types)

	// Build a natural description.
	switch {
	case len(typeList) == 1:
		return fmt.Sprintf("Manages %s resources", typeList[0])
	case len(typeList) <= 3:
		return fmt.Sprintf("Manages %s", strings.Join(typeList, ", "))
	default:
		return fmt.Sprintf("Manages %d resources (%s, ...)", len(mod.Resources), strings.Join(typeList[:3], ", "))
	}
}

// generateResourceComment creates a single-line #~ comment for a resource.
func generateResourceComment(res resource) string {
	name := res.Name
	typ := res.Type

	// Use name directly if it's descriptive enough.
	// Strip the type prefix if present (e.g., "nginx-package" for type "package").
	clean := name
	if strings.HasPrefix(name, typ+"-") {
		clean = strings.TrimPrefix(name, typ+"-")
	}

	// Generate action-oriented comment based on type.
	state := attrStr(res.Attrs, "state")
	switch typ {
	case "package":
		pkg := attrStr(res.Attrs, "package")
		if pkg == "" {
			pkg = clean
		}
		if state == "absent" {
			return fmt.Sprintf("Remove %s package", pkg)
		}
		return fmt.Sprintf("Install %s package", pkg)
	case "service":
		svc := attrStr(res.Attrs, "service")
		if svc == "" {
			svc = clean
		}
		switch state {
		case "stopped":
			return fmt.Sprintf("Stop %s service", svc)
		case "restarted":
			return fmt.Sprintf("Restart %s on dependency change", svc)
		case "reloaded":
			return fmt.Sprintf("Reload %s on dependency change", svc)
		default:
			enabled := res.Attrs["enabled"]
			if enabled == true {
				return fmt.Sprintf("Ensure %s is running and enabled", svc)
			}
			return fmt.Sprintf("Ensure %s is running", svc)
		}
	case "file":
		target := attrStr(res.Attrs, "target_path")
		if target == "" {
			target = clean
		}
		if state == "absent" {
			return fmt.Sprintf("Remove %s", target)
		}
		return fmt.Sprintf("Deploy %s", target)
	case "directory":
		target := attrStr(res.Attrs, "target_path")
		if target == "" {
			target = clean
		}
		if state == "absent" {
			return fmt.Sprintf("Remove directory %s", target)
		}
		return fmt.Sprintf("Create directory %s", target)
	case "user":
		user := attrStr(res.Attrs, "username")
		if user == "" {
			user = clean
		}
		if state == "absent" {
			return fmt.Sprintf("Remove user %s", user)
		}
		return fmt.Sprintf("Manage user %s", user)
	case "group":
		grp := attrStr(res.Attrs, "group")
		if grp == "" {
			grp = clean
		}
		return fmt.Sprintf("Manage group %s", grp)
	case "exec":
		return fmt.Sprintf("Run %s", clean)
	case "cron":
		return fmt.Sprintf("Schedule %s", clean)
	case "sysctl":
		key := attrStr(res.Attrs, "key")
		if key != "" {
			return fmt.Sprintf("Set sysctl %s", key)
		}
		return fmt.Sprintf("Configure %s", clean)
	case "repository":
		return fmt.Sprintf("Add repository %s", clean)
	case "git":
		repo := attrStr(res.Attrs, "repo")
		if repo != "" {
			return fmt.Sprintf("Clone %s", repo)
		}
		return fmt.Sprintf("Clone %s", clean)
	case "archive":
		return fmt.Sprintf("Extract %s", clean)
	case "symlink":
		target := attrStr(res.Attrs, "target_path")
		to := attrStr(res.Attrs, "to")
		if target != "" && to != "" {
			return fmt.Sprintf("Symlink %s → %s", target, to)
		}
		return fmt.Sprintf("Create symlink %s", clean)
	default:
		return fmt.Sprintf("Manage %s (%s)", clean, typ)
	}
}

// --- Helpers ---

// canonicalAttrOrder returns attribute keys in a sensible order:
// type-specific primary fields first, then remaining in original order.
func canonicalAttrOrder(res resource) []string {
	primary := typeKeyOrder[res.Type]
	seen := make(map[string]bool)
	var order []string

	// Primary fields first (if present).
	for _, k := range primary {
		if _, ok := res.Attrs[k]; ok {
			order = append(order, k)
			seen[k] = true
		}
	}

	// Remaining in original order.
	for _, k := range res.AttrOrder {
		if !seen[k] {
			order = append(order, k)
			seen[k] = true
		}
	}

	return order
}

// yamlScalar formats a Go value as a YAML scalar string.
func yamlScalar(v any) string {
	switch val := v.(type) {
	case string:
		if val == "" {
			return `""`
		}
		if needsQuoting(val) {
			return fmt.Sprintf("%q", val)
		}
		return val
	case bool:
		if val {
			return "true"
		}
		return "false"
	case int:
		return fmt.Sprintf("%d", val)
	case float64:
		if val == float64(int(val)) {
			return fmt.Sprintf("%d", int(val))
		}
		return fmt.Sprintf("%g", val)
	default:
		return fmt.Sprintf("%v", val)
	}
}

// needsQuoting returns true if a string value needs YAML quoting.
func needsQuoting(s string) bool {
	if s == "" {
		return true
	}
	// Quote strings that look like booleans, nulls, or numbers.
	lower := strings.ToLower(s)
	switch lower {
	case "true", "false", "yes", "no", "on", "off", "null", "~":
		return true
	}
	// Quote strings starting with special YAML characters.
	if strings.ContainsAny(s[:1], "{}[]&*?|->!%@`,'\"#") {
		return true
	}
	// Quote strings containing : followed by space.
	if strings.Contains(s, ": ") || strings.Contains(s, " #") {
		return true
	}
	return false
}

func attrStr(attrs map[string]any, key string) string {
	if attrs == nil {
		return ""
	}
	v, ok := attrs[key]
	if !ok {
		return ""
	}
	switch t := v.(type) {
	case string:
		return t
	case int:
		return fmt.Sprintf("%d", t)
	case int64:
		return fmt.Sprintf("%d", t)
	case float64:
		return fmt.Sprintf("%g", t)
	case bool:
		return fmt.Sprintf("%t", t)
	default:
		return fmt.Sprintf("%v", v)
	}
}

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// --- Type registry ---

// typeKeyOrder defines the canonical attribute ordering per resource type.
var typeKeyOrder = map[string][]string{
	"file":             {"target_path", "content", "source", "owner", "group", "mode", "state", "stream_edit"},
	"directory":        {"target_path", "owner", "group", "mode", "state", "recurse"},
	"symlink":          {"target_path", "to", "state"},
	"package":          {"package", "version", "state"},
	"service":          {"service", "state", "enabled"},
	"user":             {"username", "uid", "group", "groups", "home", "shell", "state"},
	"group":            {"group", "gid", "state"},
	"exec":             {"command", "creates", "onlyif", "unless", "user", "cwd", "timeout_secs"},
	"cron":             {"command", "schedule", "minute", "hour", "day", "month", "weekday", "user", "state"},
	"file_line":        {"target_path", "line", "match_pattern", "state"},
	"repository":       {"repository", "key_url", "state"},
	"archive":          {"url", "target_path", "checksum", "mode"},
	"git":              {"repo", "path", "branch", "depth"},
	"sysctl":           {"key", "value", "state"},
	"host":             {"hostname", "ip", "state"},
	"mount":            {"path", "device", "fstype", "options", "state"},
	"hostname":         {"hostname"},
	"timezone":         {"timezone"},
	"sshkey":           {"username", "sshkey_type", "sshkey"},
	"firewall":         {"port", "protocol", "action", "state"},
	"kernel_module":    {"module", "state"},
	"swap":             {"path", "size", "state"},
	"tmpfile":          {"path", "type", "mode", "age"},
	"ini":              {"target_path", "section", "key", "value"},
	"json_file":        {"target_path", "key", "value"},
	"logrotate":        {"path", "frequency", "rotate_count"},
	"reboot":           {"message", "reboot_required_file"},
	"docker_container": {"container", "image", "ports", "volumes", "state"},
	"docker_image":     {"image", "tag", "state"},
	"docker_compose":   {"target_path", "state"},
	"replace":          {"target_path", "match_pattern", "replacement"},
	"field_edit":       {"path", "line_match", "select_field", "field_value", "state"},
	"systemd_dropin":   {"service", "section", "key", "value"},
	"blockinfile":      {"target_path", "block", "marker_begin", "marker_end", "state"},
}
