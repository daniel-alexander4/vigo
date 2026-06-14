// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

// Package modlint validates, repairs, normalizes, and auto-comments Vigo
// module definitions (.vgo files). Depends only on server/validate for the
// shared KnownTypes list. Suitable for use in vigosrv, vigocli, and the
// community website (Google Cloud Functions).
//
// The pipeline:
//  1. YAML repair — strip BOM and leading non-ASCII, normalize line
//     endings, tabs→spaces, trim trailing whitespace, fix unquoted
//     YAML 1.1 booleans/nulls in scalar positions, ensure final newline,
//     unindent role-shaped paste-ins.
//  2. Parse — into the internal `module` representation, preserving
//     source-level key order and per-attribute `*yaml.Node` so the
//     renderer can keep operator formatting (flow vs block, key order).
//  3. Rule registry — every check is a Rule (`rule_*.go`) and self-
//     registers via init(). Rules emit `Finding{Severity, Line, Column,
//     RuleID, Message, FixApplied}`; some mutate the module to apply
//     auto-fixes (e.g. injecting `when: "changed"` on a service with
//     subscribes).
//  4. Render — clean normalized YAML with auto-regenerated `#~` block
//     comments. The renderer rebuilds from the parsed AST: hand-written
//     `#` comments are NOT preserved across a lint pass — this is
//     by design (community-upload normalization wants a single canonical
//     form), and is covered by the `TestLint_HandWrittenCommentsStripped`
//     regression test.
//
// Result.Findings is the canonical structured output. Errors and
// Warnings are derived string-slice views over Findings, kept for
// backward compatibility with callers that pre-date the structured
// form. New code should consume Findings directly so it can filter by
// severity or rule ID without parsing message strings.
package modlint

import (
	"fmt"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	vigovalidate "git.alexander4.io/vigo/server/validate"
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
	// IsUsercrate signals that the file is a usercrate (lives under a
	// usercrates/ dir). Usercrates may omit `name:` — the loader defaults
	// it to the filename stem. Callers set this based on the file path.
	IsUsercrate bool
	// StripComments suppresses modlint's generated `#~` annotations in the
	// rendered output, producing the clean machine-facing form. The publish
	// path uses this for the `.live/` tree: the server reads `.live/` and
	// ignores comments, so it carries no `#~` labels (and parse already drops
	// the operator's hand `#` comments). `content:` scalar data is unaffected.
	// Operator source keeps its labels + hand comments via FormatSource.
	StripComments bool
}

// Result holds the output of a lint pass.
//
// Findings is the canonical structured output, carrying severity, source
// line/column, and rule ID for every issue. Errors and Warnings are
// derived string-slice views over Findings, kept for backward
// compatibility with callers that pre-date the structured form. New
// code should consume Findings directly so it can filter by severity
// or rule ID without parsing message strings.
//
// JSON-stable: tags are explicit so external consumers (CI, the
// community-upload Cloud Function) can rely on the schema. The legacy
// Errors/Warnings slices are excluded from JSON to keep the schema
// canonical — JSON consumers should iterate Findings.
type Result struct {
	// Output is the cleaned, normalized, commented YAML.
	Output string `json:"output,omitempty"`
	// Findings is the structured list of every lint result, in
	// rule-registration order. Emitted by repair, parse, and the
	// rule registry. Severity=Error blocks publish; everything else
	// is advisory.
	Findings []Finding `json:"findings"`
	// Warnings is a string-slice view over Findings with severity in
	// {Warning, Info, Hint}. Preserved for callers that print
	// warnings inline without consulting structured fields.
	Warnings []string `json:"-"`
	// Errors is a string-slice view over Findings with severity Error.
	// Preserved for callers that gate on len(Errors) > 0 — equivalent
	// to !OK().
	Errors []string `json:"-"`
}

// OK returns true if there are no error-severity findings.
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
		r.Findings = append(r.Findings, Finding{
			Severity: Error,
			RuleID:   "yaml-parse",
			Message:  fmt.Sprintf("YAML parse error after repair: %s", err),
		})
		finalizeFindings(r)
		return r
	}

	// Step 3: Schema validation via the rule registry. Each rule emits
	// Findings and may mutate `mod` to apply auto-fixes (e.g. injecting
	// `when: "changed"` on a service with subscribes). Rules are
	// registered in their own files (`rule_*.go`) and self-register
	// via init() — adding a check is a matter of dropping in a new
	// file, not editing this dispatcher.
	r.Findings = append(r.Findings, applyRules(mod, opts)...)

	// Step 4: Normalize + auto-comment + serialize.
	r.Output = render(mod, opts, r)

	finalizeFindings(r)
	return r
}

// LintFile dispatches a single .vgo file to the correct linter by its
// basename. Configcrates and usercrates share the configcrate pipeline
// (Lint), with IsUsercrate — derived from the path — gating the one rule
// that exempts usercrates from `name:`; each scaffolding file type has its
// own parser + rule set. This is the single source of truth for "lint a
// file by its kind"; both `vigocli config validate` and the publish path
// route through it so the two never disagree.
func LintFile(relPath string, data []byte, opts Options) *Result {
	opts.IsUsercrate = opts.IsUsercrate || IsUsercratePath(relPath)
	switch filepath.Base(relPath) {
	case "compliance.vgo":
		// Directory-level compliance-inheritance file (framework→control
		// shape). Validated by the config loader's compliance index at
		// publish time, not by modlint — the publish path skips it here too,
		// so matching that keeps `config validate` in agreement with publish.
		return &Result{}
	case "envoys.vgo":
		return LintHostcrate(data, opts)
	case "roles.vgo":
		return LintRolesFile(data, opts)
	case "waivers.vgo":
		return LintWaiversFile(data, opts)
	case "environments.vgo":
		return LintEnvironmentsFile(data, opts)
	default:
		return Lint(data, opts)
	}
}

// IsUsercratePath reports whether path (slash- or OS-separated) points at a
// file living directly inside a usercrates/ directory. Mirrors the detection
// in server/config/loader.go loadUsercrateDefs: usercrates/ cannot be the
// leaf, and the file must sit directly in it (not a nested subdirectory).
func IsUsercratePath(path string) bool {
	parts := strings.Split(filepath.ToSlash(path), "/")
	for i, p := range parts {
		if p == "usercrates" {
			return i == len(parts)-2
		}
	}
	return false
}

// finalizeFindings populates the legacy Errors/Warnings string-slice
// views from r.Findings. New code should read r.Findings directly;
// this exists so callers that pre-date Phase 1 keep working.
//
// Errors gets every Severity=Error message. Warnings collapses
// {Warning, Info, Hint} together — there's no caller today that
// distinguishes those tiers, and the existing Warnings field has
// always been a catch-all for "things modlint noticed but didn't
// reject." When a caller starts caring about the distinction, it
// should switch to r.Findings rather than expand the string slice.
func finalizeFindings(r *Result) {
	for _, f := range r.Findings {
		if f.Severity == Error {
			r.Errors = append(r.Errors, f.Message)
		} else {
			r.Warnings = append(r.Warnings, f.Message)
		}
	}
}

// warnf appends a Warning-severity finding. See errorf for parameter
// semantics. Used for repair-pass auto-fixes ("converted tabs to
// spaces") that the operator should be aware of but didn't have to
// act on.
func (r *Result) warnf(line int, ruleID, format string, args ...any) {
	r.Findings = append(r.Findings, Finding{
		Severity:   Warning,
		Line:       line,
		RuleID:     ruleID,
		Message:    fmt.Sprintf(format, args...),
		FixApplied: true,
	})
}

// --- Module representation ---

// module is the internal representation of a parsed .vgo module.
//
// Line and Column reference the YAML root mapping's source position
// (1-indexed). Both are zero before parse() runs. Used by rules that
// emit module-level findings so the operator gets a "module foo.vgo:1:1"
// style pointer rather than an unanchored message.
type module struct {
	Name      string
	DependsOn []string
	Before    []string
	Defaults  map[string]any
	// Provides and Bundle are decoded from the nested
	// `compliance: { provides: [...], bundle: [...] }` shape (0.66.57+).
	// Populated only when those sub-keys appear under `compliance:`.
	Provides []string
	Bundle   []string
	// ComplianceUnknownSubKeys captures any direct-children keys of
	// `compliance:` that are NOT `provides` or `bundle` — the pre-0.66.48
	// framework→control list shape. rule_configcrate_compliance_key_retired
	// uses this to fire the retire-error on the old shape.
	ComplianceUnknownSubKeys []string
	// TopLevelProvidesLine / TopLevelBundleLine record the YAML line of
	// a top-level `provides:` or `bundle:` key (the retired 0.66.48-
	// through-0.66.56 authoring shape). 0 = absent.
	// rule_module_top_level_provides_bundle uses these to fire.
	TopLevelProvidesLine int
	TopLevelBundleLine   int
	Vars                 map[string]any
	VarOrder             []string // preserve original key order
	Resources            []resource

	Line   int
	Column int
}

// resource is the internal representation of a parsed resource.
//
// Line and Column reference the resource mapping's first key in the
// source YAML (typically the `- name:` line). Populated during parse;
// rules use it to anchor findings to a specific resource without
// having to re-walk the YAML tree.
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

	// AttrNodes captures the original `*yaml.Node` for each attribute so the
	// writer can preserve the operator's source ordering and flow vs block
	// style choices for nested maps and sequences. Without this, a nested
	// map like `groups: {danlap: [docker], default: [], girlslaptop: ...}`
	// gets alphabetized + every list expanded to multi-line block style on
	// every publish, which mangles hand-written formatting. The node is
	// dropped for an attribute whose value modlint mutates (e.g. mode
	// normalization to octal string), in which case the writer falls back
	// to the decoded `Attrs[key]` and the existing yamlScalar path.
	AttrNodes map[string]*yaml.Node

	Line   int
	Column int
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
				r.warnf(1, "repair-non-ascii", "stripped leading non-ASCII bytes")
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
				r.warnf(i+1, "repair-tabs", "line %d: converted tabs to spaces", i+1)
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
//
// Gating rule: the first non-empty line must be a `key: value` shape with a
// non-empty value on the same line. If it's a `key:` block-introducer (no
// value after the colon), subsequent indented content is the legitimate
// nested block and stripping would corrupt the file. This distinction
// keeps non-module YAML safe — `envoys:`, `roles:`, `environments:`,
// `waivers:` all use the block-introducer shape.
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

	// First line must be a `key: value` form (not a `key:` block-introducer).
	// A trailing `:` with nothing after means the next indented content IS
	// the value — stripping its indent would re-parent every nested key as
	// a top-level peer and corrupt the file.
	if isBlockIntroducer(firstLine) {
		return lines
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
	r.warnf(0, "repair-over-indent",
		"stripped %d leading spaces from all lines after 'name:' — this looks like a role definition pasted as a module file",
		minIndent)
	return fixed
}

// isBlockIntroducer reports whether line `l` is a YAML key with no value
// on the same line — the form `<key>:` (optionally followed by a comment).
// Such lines introduce a nested block whose content must be indented;
// the over-indent fixer must NOT strip that indent.
func isBlockIntroducer(l string) bool {
	// Strip a trailing comment, if any. YAML comments start at `#` only
	// when preceded by a space (or the start of the line) — we use a
	// permissive split since either misclassification is fine for the
	// gating purpose here (false-positive: rule skips when it could
	// have applied; false-negative: rule applies anyway, same as today).
	if idx := strings.Index(l, " #"); idx >= 0 {
		l = l[:idx]
	}
	l = strings.TrimRight(l, " \t")
	if l == "" {
		return false
	}
	colon := strings.LastIndexByte(l, ':')
	if colon < 0 {
		return false // not a mapping at all
	}
	// Everything after the last `:` must be empty (modulo trailing
	// whitespace already trimmed). `key: value` has content; `key:`
	// alone is a block-introducer.
	return strings.TrimSpace(l[colon+1:]) == ""
}

// blockScalarOpenRe matches a `key: |` or `key: >` block-scalar
// introducer (with optional chomping/indentation indicators and an
// optional trailing comment). The captured indent of the key line
// bounds the block: every following line indented deeper than the key
// belongs to the scalar and must be left untouched by the bool rewrite.
var blockScalarOpenRe = regexp.MustCompile(`^(\s*)\S.*:\s*[|>][+-]?\d*\s*(?:#.*)?$`)

// fixUnquotedSpecials rewrites unquoted YAML 1.1 booleans (yes/no/on/off)
// to true/false in `key: value` scalar positions. Block-scalar regions
// (`content: |`, etc.) are skipped — their bytes are literal payload
// shipped to the fleet, and rewriting a colon-form line like
// `PasswordAuthentication: no` inside an sshd_config block would corrupt
// the delivered file.
func fixUnquotedSpecials(text string, r *Result) string {
	lines := strings.Split(text, "\n")
	// blockIndent is the indent of the current block scalar's introducer
	// key, or -1 when not inside a block scalar. A line strictly more
	// indented than blockIndent (or blank) stays inside the block.
	blockIndent := -1
	for i, line := range lines {
		if blockIndent >= 0 {
			trimmed := strings.TrimSpace(line)
			indent := len(line) - len(strings.TrimLeft(line, " "))
			if trimmed == "" || indent > blockIndent {
				// Still inside the block scalar — leave the line verbatim.
				continue
			}
			// Dedent back to the key level (or shallower) ends the block.
			blockIndent = -1
		}
		if m := blockScalarOpenRe.FindStringSubmatch(line); m != nil {
			blockIndent = len(m[1])
			continue
		}
		lines[i] = yamlBoolPattern.ReplaceAllStringFunc(line, func(match string) string {
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
			r.warnf(0, "repair-unquoted-bool", "converted unquoted %q to %s", val, replacement)
			return prefix + replacement + suffix
		})
	}
	return strings.Join(lines, "\n")
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
		Vars:   make(map[string]any),
		Line:   root.Line,
		Column: root.Column,
	}

	seen := make(map[string]bool)
	for i := 0; i < len(root.Content)-1; i += 2 {
		keyNode := root.Content[i]
		key := keyNode.Value
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
			// 0.66.57+ shape: { provides: [...], bundle: [...] }.
			// Walk the mapping manually so we can distinguish the new
			// sub-keys from pre-0.66.48 framework→control entries;
			// rule_configcrate_compliance_key_retired fires on the latter.
			if val.Kind != yaml.MappingNode {
				return nil, fmt.Errorf("compliance: expected mapping, got %v", val.Kind)
			}
			for j := 0; j < len(val.Content)-1; j += 2 {
				subKey := val.Content[j].Value
				subVal := val.Content[j+1]
				switch subKey {
				case "provides":
					if err := subVal.Decode(&mod.Provides); err != nil {
						return nil, fmt.Errorf("decoding compliance.provides: %w", err)
					}
				case "bundle":
					if err := subVal.Decode(&mod.Bundle); err != nil {
						return nil, fmt.Errorf("decoding compliance.bundle: %w", err)
					}
				default:
					mod.ComplianceUnknownSubKeys = append(mod.ComplianceUnknownSubKeys, subKey)
				}
			}
		case "provides":
			// Top-level `provides:` was the 0.66.48-through-0.66.56
			// authoring shape; retired in 0.66.57. Record the line so
			// rule_module_top_level_provides_bundle can point at it.
			mod.TopLevelProvidesLine = keyNode.Line
		case "bundle":
			// Top-level `bundle:` — same retirement as `provides:` above.
			mod.TopLevelBundleLine = keyNode.Line
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
	res.AttrNodes = make(map[string]*yaml.Node)
	res.Line = node.Line
	res.Column = node.Column

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
				// `mode` arrives as an integer when the operator typed
				// `mode: 644` rather than `mode: '0644'`. We deliberately
				// preserve the integer here so the modeOctalRule can
				// detect the unquoted-octal trap and emit a proper
				// auto-fix finding. The rule mutates Attrs["mode"] to
				// a quoted string before render runs.
				res.Attrs[key] = v
				res.AttrOrder = append(res.AttrOrder, key)
				res.AttrNodes[key] = val
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
//
// All validation runs through the rule registry now (see rule.go and
// the per-check rule_*.go files). The pipeline applies each registered
// rule in order and aggregates findings; adding a new check is a
// matter of dropping in a new rule_*.go that calls register() in init().

// toValidateModule converts a modlint module to a validate.Configcrate.
func toValidateModule(mod *module) vigovalidate.Configcrate {
	vm := vigovalidate.Configcrate{Name: mod.Name}
	for _, res := range mod.Resources {
		vm.Resources = append(vm.Resources, vigovalidate.Resource{
			Name:       res.Name,
			Type:       res.Type,
			When:       res.When,
			Attributes: res.Attrs,
			DependsOn:  res.DependsOn,
			Before:     res.Before,
			Notify:     res.Notify,
			Subscribes: res.Subscribes,
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

	// Module-level block comment (suppressed for the stripped .live form).
	if !opts.StripComments {
		b.WriteString(fmt.Sprintf("#~ %s\n", generateModuleComment(mod)))
	}
	if mod.Name != "" {
		b.WriteString(fmt.Sprintf("name: %s\n", mod.Name))
	}

	// Module-level depends_on.
	if len(mod.DependsOn) > 0 {
		b.WriteString(fmt.Sprintf("depends_on: [%s]\n", strings.Join(mod.DependsOn, ", ")))
	}
	if len(mod.Before) > 0 {
		b.WriteString(fmt.Sprintf("before: [%s]\n", strings.Join(mod.Before, ", ")))
	}

	// Compliance — 0.66.57+ shape: `compliance: { provides: [...], bundle: [...] }`.
	if len(mod.Provides) > 0 || len(mod.Bundle) > 0 {
		b.WriteString("\ncompliance:\n")
		if len(mod.Provides) > 0 {
			quoted := make([]string, len(mod.Provides))
			for i, p := range mod.Provides {
				quoted[i] = yamlScalar(p)
			}
			b.WriteString(fmt.Sprintf("  provides: [%s]\n", strings.Join(quoted, ", ")))
		}
		if len(mod.Bundle) > 0 {
			quoted := make([]string, len(mod.Bundle))
			for i, x := range mod.Bundle {
				quoted[i] = yamlScalar(x)
			}
			b.WriteString(fmt.Sprintf("  bundle: [%s]\n", strings.Join(quoted, ", ")))
		}
	}

	// Defaults.
	if len(mod.Defaults) > 0 {
		b.WriteString("\ndefaults:\n")
		for _, k := range sortedKeys(mod.Defaults) {
			emitVarOrDefault(&b, "  ", k, mod.Defaults[k])
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
			emitVarOrDefault(&b, "  ", k, v)
		}
	}

	// Resources.
	if len(mod.Resources) > 0 {
		b.WriteString("\nresources:\n")
		for i, res := range mod.Resources {
			if i > 0 {
				b.WriteString("\n")
			}
			renderResource(&b, res, opts.StripComments)
		}
	}

	return b.String()
}

// renderResource writes a single resource, with its `#~` label unless
// stripComments is set (the clean machine-facing .live form).
func renderResource(b *strings.Builder, res resource, stripComments bool) {
	if !stripComments {
		comment := generateResourceComment(res) + reversalNote(res.Type)
		b.WriteString(fmt.Sprintf("  #~ %s\n", comment))
	}
	b.WriteString(fmt.Sprintf("  - name: %s\n", res.Name))
	b.WriteString(fmt.Sprintf("    type: %s\n", res.Type))

	if res.When != "" {
		b.WriteString(fmt.Sprintf("    when: %s\n", yamlScalar(res.When)))
	}

	// Type-specific attributes in a sensible order.
	attrOrder := canonicalAttrOrder(res)
	for _, key := range attrOrder {
		// Prefer the original yaml.Node when available — it preserves
		// per-key source order and flow-vs-block style for nested
		// maps/sequences. Falls back to the decoded value path for
		// attributes whose value modlint mutated (currently only
		// `mode` — see parseOneResource for why we drop the node).
		if node, ok := res.AttrNodes[key]; ok && node != nil {
			writeAttrNode(b, key, node, 4)
			continue
		}
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
		// Empty list must round-trip as `key: []`, not as a bare `key:`
		// with no children — the latter parses as YAML null and silently
		// corrupts list-valued attributes (notably the `default:` arm of
		// lookup tables).
		if len(val) == 0 {
			b.WriteString(fmt.Sprintf("%s%s: []\n", prefix, key))
			return
		}
		b.WriteString(fmt.Sprintf("%s%s:\n", prefix, key))
		for _, item := range val {
			b.WriteString(fmt.Sprintf("%s  - %s\n", prefix, yamlScalar(item)))
		}
	case map[string]any:
		// Same empty-collection hazard as []any — an empty map rendered
		// as a bare `key:` round-trips to null.
		if len(val) == 0 {
			b.WriteString(fmt.Sprintf("%s%s: {}\n", prefix, key))
			return
		}
		b.WriteString(fmt.Sprintf("%s%s:\n", prefix, key))
		for _, k := range sortedKeys(val) {
			writeAttr(b, k, val[k], indent+2)
		}
	default:
		b.WriteString(fmt.Sprintf("%s%s: %s\n", prefix, key, yamlScalar(v)))
	}
}

// flowLineWidthBudget caps the total line length when modlint is allowed to
// emit a sequence in flow style (`key: [a, b, c]`). Anything that would
// overflow falls back to multi-line block style. 80 is the conventional
// terminal width and matches what most operators write by hand.
const flowLineWidthBudget = 80

// writeAttrNode emits a key-value pair where the value is taken from a
// `*yaml.Node` instead of a decoded `any`. Preserves source-level key
// order for mappings and chooses flow vs block style for sequences based
// on item count, scalar-only-ness, and total line length. This is the
// path that keeps hand-written `groups: { danlap: [docker], ... }` from
// being alphabetized + expanded into block style on every publish.
//
// The walker is intentionally limited to the shapes that show up inside
// resource attributes:
//
//   - Scalar         → emit via yamlScalar (handles strings, ints, bools,
//     newline-multilines, quoting). Falls back to the
//     raw `node.Value` only when the node has a tag the
//     scalar coercion doesn't recognize (rare edge case).
//   - Sequence       → flow if all items are scalars and the resulting
//     line fits within `flowLineWidthBudget`; block
//     (`- item` per line) otherwise. Empty sequences
//     always use flow form `[]` to round-trip safely
//     (see TestLint_EmptyListAttr_RoundTrips).
//   - Mapping        → preserve key order from `node.Content`. Empty
//     mappings emit as `{}`. Recurses into each value,
//     so a per-host lookup table whose values are
//     sequences gets nested flow handling automatically.
//
// Multi-line literals (`|` block scalars) are emitted via the existing
// yamlScalar string path so the existing authorized-keys / templated
// content rendering continues to work unchanged.
func writeAttrNode(b *strings.Builder, key string, node *yaml.Node, indent int) {
	prefix := strings.Repeat(" ", indent)

	switch node.Kind {
	case yaml.ScalarNode:
		// Multi-line strings must use the existing block-scalar emitter
		// (writeAttr's string branch). Decode and re-dispatch.
		if strings.Contains(node.Value, "\n") {
			writeAttr(b, key, node.Value, indent)
			return
		}
		// For typed scalars (int, bool, float, null), let yaml.Node's
		// own decode tell us the parsed value so yamlScalar quotes
		// correctly — bare "true" must stay bare, "yes" must be quoted,
		// integers must not be over-quoted, etc.
		var decoded any
		if err := node.Decode(&decoded); err != nil {
			// Defensive: fall through to the raw scalar form. Should
			// not happen for any well-formed YAML modlint accepts.
			b.WriteString(fmt.Sprintf("%s%s: %s\n", prefix, key, node.Value))
			return
		}
		b.WriteString(fmt.Sprintf("%s%s: %s\n", prefix, key, yamlScalar(decoded)))

	case yaml.SequenceNode:
		if len(node.Content) == 0 {
			b.WriteString(fmt.Sprintf("%s%s: []\n", prefix, key))
			return
		}
		// Decide flow vs block: flow only when every child is a scalar
		// AND the rendered line fits the width budget.
		allScalar := true
		var rendered []string
		for _, child := range node.Content {
			if child.Kind != yaml.ScalarNode || strings.Contains(child.Value, "\n") {
				allScalar = false
				break
			}
			var decoded any
			if err := child.Decode(&decoded); err != nil {
				allScalar = false
				break
			}
			rendered = append(rendered, yamlScalar(decoded))
		}
		if allScalar {
			// `<indent><key>: [a, b, c]`
			line := fmt.Sprintf("%s%s: [%s]", prefix, key, strings.Join(rendered, ", "))
			if len(line) <= flowLineWidthBudget {
				b.WriteString(line)
				b.WriteString("\n")
				return
			}
		}
		// Block form: one item per line. Items themselves may be
		// nested maps/sequences, in which case we recurse via a
		// synthesized child key (the dash itself).
		b.WriteString(fmt.Sprintf("%s%s:\n", prefix, key))
		for _, child := range node.Content {
			switch child.Kind {
			case yaml.ScalarNode:
				var decoded any
				if err := child.Decode(&decoded); err != nil {
					b.WriteString(fmt.Sprintf("%s  - %s\n", prefix, child.Value))
				} else {
					b.WriteString(fmt.Sprintf("%s  - %s\n", prefix, yamlScalar(decoded)))
				}
			default:
				// Nested map/seq item — emit `-\n` then recurse with
				// extra indent. Rare in resource attributes; the
				// existing decoded-value path didn't handle this
				// case at all, so this is a strict improvement.
				b.WriteString(fmt.Sprintf("%s  -\n", prefix))
				if child.Kind == yaml.MappingNode {
					for i := 0; i < len(child.Content)-1; i += 2 {
						k := child.Content[i].Value
						writeAttrNode(b, k, child.Content[i+1], indent+4)
					}
				}
			}
		}

	case yaml.MappingNode:
		if len(node.Content) == 0 {
			b.WriteString(fmt.Sprintf("%s%s: {}\n", prefix, key))
			return
		}
		b.WriteString(fmt.Sprintf("%s%s:\n", prefix, key))
		// Walk Content in source order — KEY ORDER PRESERVED.
		for i := 0; i < len(node.Content)-1; i += 2 {
			k := node.Content[i].Value
			writeAttrNode(b, k, node.Content[i+1], indent+2)
		}

	default:
		// Document / Alias / etc. — defensive fallback to the decoded path.
		var decoded any
		if err := node.Decode(&decoded); err == nil {
			writeAttr(b, key, decoded, indent)
		} else {
			b.WriteString(fmt.Sprintf("%s%s: %s\n", prefix, key, node.Value))
		}
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
	case "source_package":
		return fmt.Sprintf("Extract %s", clean)
	case "nonrepo_package":
		return fmt.Sprintf("Install %s", clean)
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

// reversalNote returns the trailing reversibility annotation for a resource's
// generated #~ comment. The non-state types have no `state: absent` retraction;
// for the reversible ones this surfaces the `revert:` lever right where the
// operator reads the resource — the visibility the mandatory-`state` rule gives
// the state types, without an active attribute on every stanza. Empty for any
// non-reversible type. SoT: validate.ReversibleTypes / CommandReversibleTypes.
func reversalNote(typ string) string {
	if vigovalidate.CommandReversibleTypes[typ] {
		return " — reversible: set revert: true with on_revert: to undo"
	}
	if vigovalidate.ReversibleTypes[typ] {
		return " — reversible: set revert: true to restore the prior value"
	}
	return ""
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

// emitVarOrDefault writes a `key: value` pair for a vars/defaults entry,
// handling both scalar and structured values.
//
// Scalars use the single-line `key: value` form via yamlScalar. Maps and
// slices fall back to a block-style emission rooted at `key:` followed by
// indented yaml.Marshal output, so structured vars (e.g. lists of maps for
// dnsmasq dhcp-host reservations) survive the canonical writer instead of
// being stringified through fmt.Sprintf("%v", val).
//
// Map keys are sorted alphabetically inside the structured branch so the
// canonical output is deterministic across runs (yaml.v3 doesn't sort
// map[string]any keys on its own).
func emitVarOrDefault(b *strings.Builder, indent, key string, val any) {
	switch val.(type) {
	case map[string]any, []any:
		node, err := sortedYAMLNode(val)
		if err != nil {
			// Fall through to the legacy single-line path so we at least
			// produce *some* output rather than dropping the var. The
			// resulting bytes may still fail downstream parse, but that's
			// no worse than the prior behavior.
			fmt.Fprintf(b, "%s%s: %s\n", indent, key, yamlScalar(val))
			return
		}
		raw, err := yaml.Marshal(node)
		if err != nil {
			fmt.Fprintf(b, "%s%s: %s\n", indent, key, yamlScalar(val))
			return
		}
		body := strings.TrimRight(string(raw), "\n")
		if body == "" || body == "{}" || body == "[]" {
			fmt.Fprintf(b, "%s%s: %s\n", indent, key, body)
			return
		}
		fmt.Fprintf(b, "%s%s:\n", indent, key)
		for _, line := range strings.Split(body, "\n") {
			fmt.Fprintf(b, "%s  %s\n", indent, line)
		}
	default:
		fmt.Fprintf(b, "%s%s: %s\n", indent, key, yamlScalar(val))
	}
}

// sortedYAMLNode builds a *yaml.Node for val with map keys sorted
// alphabetically at every level. Used by emitVarOrDefault to keep canonical
// output deterministic.
func sortedYAMLNode(val any) (*yaml.Node, error) {
	n := &yaml.Node{}
	switch v := val.(type) {
	case map[string]any:
		n.Kind = yaml.MappingNode
		keys := make([]string, 0, len(v))
		for k := range v {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			keyNode := &yaml.Node{Kind: yaml.ScalarNode, Value: k}
			valNode, err := sortedYAMLNode(v[k])
			if err != nil {
				return nil, err
			}
			n.Content = append(n.Content, keyNode, valNode)
		}
	case []any:
		n.Kind = yaml.SequenceNode
		for _, item := range v {
			itemNode, err := sortedYAMLNode(item)
			if err != nil {
				return nil, err
			}
			n.Content = append(n.Content, itemNode)
		}
	default:
		if err := n.Encode(val); err != nil {
			return nil, err
		}
	}
	return n, nil
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
	// Quote strings containing flow-indicator characters anywhere — yaml.v3
	// treats unquoted commas/brackets/braces as flow-context separators even
	// in block-mapping values, so an unquoted "192.168.1.100,192.168.1.200,12h"
	// fails to round-trip with "did not find expected ',' or ']'".
	if strings.ContainsAny(s, ",[]{}") {
		return true
	}
	// Quote strings containing : followed by space.
	if strings.Contains(s, ": ") || strings.Contains(s, " #") {
		return true
	}
	// Quote all-digit strings so they round-trip as strings instead of
	// being reparsed as integers. The classic case is `mode: '0644'`:
	// without quoting the renderer would emit `mode: 0644`, which YAML
	// 1.2 parses as int 644 — silently shipping the wrong permissions.
	// Same risk for any operator-typed numeric string (port numbers
	// stored as strings, version components, etc.).
	if isAllDigits(s) {
		return true
	}
	return false
}

// isAllDigits reports whether s consists only of ASCII decimal digits
// 0-9. A leading sign is NOT permitted; sign chars trip needsQuoting's
// special-character check above so we don't double-handle them here.
func isAllDigits(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return s != ""
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
	"file":            {"target_path", "content", "source", "owner", "group", "mode", "state", "stream_edit"},
	"directory":       {"target_path", "owner", "group", "mode", "state", "recurse"},
	"symlink":         {"target_path", "to", "state"},
	"package":         {"package", "version", "state", "allow_downgrade", "purge", "update_cache", "lock_version", "post_cleanup", "flags", "timeout", "retries", "retry_delay"},
	"service":         {"service", "state", "enabled"},
	"user":            {"username", "uid", "group", "groups", "home", "shell", "state"},
	"group":           {"group", "gid", "state"},
	"exec":            {"command", "onlyif", "unless", "user", "cwd", "timeout_secs"},
	"cron":            {"command", "schedule", "minute", "hour", "day", "month", "weekday", "user", "state"},
	"file_line":       {"target_path", "line", "match_pattern", "state"},
	"repository":      {"repository", "key_url", "state"},
	"source_package":  {"url", "target_path", "checksum", "mode"},
	"nonrepo_package": {"url", "source", "package", "version", "state", "sha256", "lock_version", "allow_downgrade", "purge", "update_cache", "post_cleanup", "flags", "pre_install", "post_install", "headers", "timeout", "retries", "retry_delay"},
	"git":             {"repo", "path", "branch", "depth"},
	"sysctl":          {"key", "value", "state"},
	"host":            {"hostname", "ip", "state"},
	"mount":           {"path", "device", "fstype", "options", "state"},
	"hostname":        {"hostname"},
	"timezone":        {"timezone"},
	"sshkey":          {"username", "sshkey_type", "sshkey"},
	"firewall":        {"port", "protocol", "action", "state"},
	"kernel_module":   {"module", "state"},
	"swap":            {"path", "size", "state"},
	"tmpfile":         {"path", "type", "mode", "age"},
	"ini":             {"target_path", "section", "key", "value"},
	"json_file":       {"target_path", "key", "value"},
	"logrotate":       {"path", "frequency", "rotate_count"},
	"reboot":          {"message", "reboot_required_file"},
	"container":       {"container", "image", "artifact", "ports", "volumes", "state", "env", "network", "restart", "command", "pull", "tmpfs", "cap_drop", "cap_add", "labels", "log_driver", "log_options", "user", "read_only", "healthcheck_command", "healthcheck_interval", "healthcheck_timeout", "healthcheck_retries", "healthcheck_start_period"},
	"replace":         {"target_path", "match_pattern", "replacement"},
	"field_edit":      {"path", "line_match", "select_field", "field_value", "state"},
	"systemd_dropin":  {"service", "section", "key", "value"},
	"blockinfile":     {"target_path", "block", "marker_begin", "marker_end", "state"},
}
