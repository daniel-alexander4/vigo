// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: Apache-2.0

// Package validate provides static idempotency analysis for Vigo resource
// definitions. It checks that every resource in a module definition is provably
// idempotent before the config is allowed to go live.
//
// Resource types that are inherently idempotent (check-before-act by design):
//   - file, package, service, user, repository, source_package, nonrepo_package, cron
//
// Resource types that require guards for idempotency:
//   - exec: must declare at least one of creates, onlyif, unless
//
// Service resources with state "restarted" or "reloaded" are non-idempotent
// unless gated by subscribes (triggered only on dependency change).
package validate

import (
	"fmt"
	"path/filepath"
	"strconv"
	"strings"
)

// Severity indicates how serious a validation finding is.
type Severity int

const (
	SeverityError   Severity = iota // blocks save / load
	SeverityWarning                 // informational, does not block
)

// Finding describes a single validation issue.
type Finding struct {
	Severity     Severity
	Configcrate  string
	ResourceName string
	ResourceType string
	Message      string
}

func (f Finding) String() string {
	sev := "ERROR"
	if f.Severity == SeverityWarning {
		sev = "WARN"
	}
	loc := f.Configcrate
	if f.ResourceName != "" {
		loc += "/" + f.ResourceName
	}
	return fmt.Sprintf("[%s] %s: %s", sev, loc, f.Message)
}

// Resource mirrors the fields of config.ResourceConfig that we need for
// validation, avoiding a circular import. The caller maps from their types.
type Resource struct {
	Name        string
	Type        string
	When        string
	Attributes  map[string]any
	DependsOn   []string
	Before      []string
	Notify      []string
	Subscribes  []string
	WatchSecret []string
	// Case/CaseMatch carry a per-OS conditional block (`case:` selector +
	// `match:` branches). The branch attributes live here, NOT in
	// Attributes — so without them the required-attribute check could only
	// see the base attrs and a branch omitting a required attr (e.g. the
	// mandatory `state`) would slip through publish. CaseMatch maps each
	// branch selector to that branch's attribute overlay.
	Case      string
	CaseMatch map[string]map[string]any
}

// Configcrate mirrors config.ConfigcrateDefinition for validation purposes.
type Configcrate struct {
	Name      string
	Resources []Resource
}

// KnownTypes is the canonical set of valid resource types. Single source of
// truth: it aliases `generatedKnownTypes` (auto-derived by
// `scripts/gen-executor-schemas.sh` from `executors.insert(...)` calls in the
// agent runner). To add a new type: register a Rust executor + insert it in
// `agent/src/runner/mod.rs`, then `make` regenerates `schemas_gen.go`. No
// hand-edit here.
//
// Exported so modlint can read it — there must be exactly one source of
// truth, and this is it.
var KnownTypes = generatedKnownTypes

// universalAttrs are attributes allowed on any resource type. These are not
// flagged as unknown regardless of type-specific schema.
//
// foreach + key are config-load-time expansion structurals — the parser
// pulls them off into ResourceConfig.ForEach (server/config/parse.go:182)
// but modlint's adapter sees the raw YAML and lands them in Attributes,
// so the unknown-attribute pass needs to know they're legal anywhere.
var universalAttrs = map[string]bool{
	"state": true, "onlyif": true, "unless": true, "creates": true,
	"skip_validation": true, "when": true, "timeout_secs": true,
	"case": true, "match": true, "watch_secret": true,
	"foreach": true, "key": true,
}

// ReversibleTypes are the non-state resource types that support the `revert:`
// lever in this build. Group B value-setters (hostname, timezone) revert by
// restoring a snapshotted prior value; the Group A command types
// (CommandReversibleTypes) run an operator-declared `on_revert:` inverse —
// locally via `sh -c` (exec, backup, db_backup, replace) or in the executor's
// own context (ssh_exec over remote SSH, powershell via powershell.exe). The
// agent's reversal layer (agent/src/runner/reversal.rs) is the matching source
// of truth. The stanza/policy setters (cisco_interface, junos_interface,
// local_security_policy, service_recovery_windows) are a tracked follow-on —
// they revert in a structured (multi-field) or remote-device context this
// build doesn't cover.
var ReversibleTypes = map[string]bool{
	"hostname": true, "timezone": true,
	"exec": true, "backup": true, "db_backup": true, "replace": true,
	"ssh_exec": true, "powershell": true,
}

// CommandReversibleTypes (Group A) revert by running `on_revert:`, so
// `revert: true` on one of these requires a non-empty `on_revert:` — enforced
// at publish by validateReversal. A subset of ReversibleTypes. (How the
// inverse runs — local `sh -c` vs the executor's native transport — is the
// agent's concern; publish validation only cares that `on_revert:` is present.)
var CommandReversibleTypes = map[string]bool{
	"exec": true, "backup": true, "db_backup": true, "replace": true,
	"ssh_exec": true, "powershell": true,
}

// OnRevertNone is the reserved `on_revert:` value declaring that a command
// resource has *no inverse* — an enforce/apply action (tighten perms, reload a
// daemon, enable a service) whose removable state, if any, is owned by sibling
// resources that retract on their own. Under configcrate retraction
// (`state: absent`) such a resource is omitted from the retracted set rather
// than reverted; it is never run as a literal command. Distinct from an empty
// `on_revert:` ("forgot to decide" — still a publish error under retraction).
const OnRevertNone = "none"

// IsOnRevertNone reports whether an `on_revert:` value is the no-inverse
// sentinel. Whitespace-tolerant, exact-match otherwise — the single decision
// point shared by the loader's retraction transform and validateReversal so the
// two can't drift on what counts as `none`.
func IsOnRevertNone(onRevert string) bool {
	return strings.TrimSpace(onRevert) == OnRevertNone
}

// ParamSpec describes a single attribute's validation constraints.
type ParamSpec struct {
	Required bool // must be present and non-empty
	AbsPath  bool // must be an absolute path (if present)
	Mode     bool // must be valid octal mode (if present)
}

// TypeSchema defines the allowed and required attributes for a resource type.
// If AllowExtra is true, unknown attributes are not flagged (for types with
// freeform or platform-varying attributes). Add schemas for every KnownType —
// the test TestAllKnownTypesHaveSchema enforces this.
type TypeSchema struct {
	Attrs      map[string]ParamSpec
	AllowExtra bool // true = don't flag unknown attributes
}

// TypeSchemas is populated at init time from generatedSchemas (auto-extracted
// from Rust executor source code) plus semantic constraints (Required, AbsPath,
// Mode) defined below. Regenerate after executor changes:
//
//	scripts/gen-executor-schemas.sh > server/validate/schemas_gen.go
var TypeSchemas = map[string]TypeSchema{}

// TypeConstraints carries the per-type semantic validation rules — what the
// auto-extractor in `gen-executor-schemas.sh` can't see by greping
// `params.get(...)`. Hand-maintained, but co-located per type so adding a
// new resource type is a single edit (the prior shape spread the same
// type's rules across four separate maps).
//
// Required:     attribute must be present and non-empty
// AbsPath:      attribute (if present) must be an absolute path
// Mode:         attribute (if present) must be a valid octal mode
// Supplemental: attribute names the generator can't extract (variable-key
//
//	lookups, cross-platform unions, runner-injected params)
type TypeConstraints struct {
	Required     []string
	AbsPath      []string
	Mode         []string
	Supplemental []string
}

// typeConstraints lists every type that needs semantic constraints beyond
// the auto-extracted attribute set. Types whose constraint block is `{}`
// or absent get a plain TypeSchema with the generated attrs and nothing
// else. Keep alphabetical for legibility.
//
// Note: `container` requires `image` OR `artifact`, and `vm` requires
// `disk_path` OR `artifact` — those one-of constraints can't be expressed
// in `Required` (which is AND-semantics) and live in dedicated modlint
// rules.
var typeConstraints = map[string]TypeConstraints{
	"alternatives":  {Required: []string{"path"}, AbsPath: []string{"path"}},
	"backup":        {Required: []string{"source_dir", "target_path"}, AbsPath: []string{"source_dir", "target_path"}},
	"blockinfile":   {Required: []string{"target_path", "block_content"}, AbsPath: []string{"target_path"}, Mode: []string{"mode"}},
	"cron":          {Required: []string{"command", "user"}, Supplemental: []string{"minute", "hour", "day", "month", "weekday"}},
	"db_backup":     {Required: []string{"db_type", "source_db", "target_path"}, AbsPath: []string{"target_path"}},
	"directory":     {Required: []string{"target_path"}, AbsPath: []string{"target_path"}, Mode: []string{"mode"}},
	"exec":          {Required: []string{"command"}, AbsPath: []string{"cwd"}},
	"field_edit":    {Required: []string{"path", "line_match", "select_field"}, AbsPath: []string{"path"}},
	"file":          {Required: []string{"target_path", "owner", "group", "mode"}, AbsPath: []string{"target_path"}, Mode: []string{"mode"}},
	"file_line":     {Required: []string{"target_path"}, AbsPath: []string{"target_path"}},
	"firewall":      {Required: []string{"port"}},
	"gem":           {Required: []string{"package"}},
	"git":           {Required: []string{"repo", "path"}, AbsPath: []string{"path"}},
	"group":         {Required: []string{"group"}},
	"host":          {Required: []string{"hostname"}},
	"hostname":      {Required: []string{"hostname"}},
	"ini":           {Required: []string{"target_path", "key"}, AbsPath: []string{"target_path"}},
	"json_file":     {Required: []string{"target_path", "key"}, AbsPath: []string{"target_path"}},
	"kernel_module": {Required: []string{"module"}},
	"logrotate":     {Required: []string{"path"}, AbsPath: []string{"path"}},
	"mount":         {Required: []string{"path"}, AbsPath: []string{"path"}},
	"npm":           {Required: []string{"package"}},
	// `package`: Required guards against missing scrape; Supplemental
	// covers variable-key reads + runner-injected values the gen can't see.
	"package":        {Required: []string{"package"}, Supplemental: []string{"package", "name", "version", "allow_downgrade", "lock_version", "purge", "post_cleanup", "update_cache", "flags", "timeout", "retries", "retry_delay"}},
	"pip":            {Required: []string{"package"}, AbsPath: []string{"virtualenv"}},
	"process":        {Required: []string{"process"}},
	"replace":        {Required: []string{"target_path", "match_pattern", "replacement"}, AbsPath: []string{"target_path"}},
	"selinux":        {AbsPath: []string{"path"}},
	"service":        {Required: []string{"service"}},
	"source_package": {Required: []string{"url"}, AbsPath: []string{"target_path"}, Mode: []string{"mode"}},
	"sshkey":         {Required: []string{"hostname", "sshkey"}},
	"swap":           {Required: []string{"path"}, AbsPath: []string{"path"}},
	"symlink":        {Required: []string{"target_path"}, AbsPath: []string{"target_path"}},
	"sysctl":         {Required: []string{"key", "value"}},
	"systemd_dropin": {Required: []string{"unit", "content"}},
	"timezone":       {Required: []string{"timezone"}},
	"tmpfile":        {Required: []string{"path"}, AbsPath: []string{"path"}, Mode: []string{"mode"}},
	"user":           {Required: []string{"username"}},
	// `vm`: the `disk_path` OR `artifact` one-of is enforced at runtime by
	// the vm executor (see `agent/src/executors/vm.rs`) and in a dedicated
	// modlint rule. The unconditional scalars are listed here.
	"vm": {Required: []string{"vm_name", "memory_mib", "vcpu"}, AbsPath: []string{"disk_path"}},
}

func init() {
	// Build TypeSchemas from generatedSchemas (auto-extracted from Rust
	// source) merged with typeConstraints (hand-maintained semantic rules).
	// Every known type goes through this loop — KnownTypes is the
	// generated source of truth.
	for typeName := range KnownTypes {
		schema := TypeSchema{Attrs: make(map[string]ParamSpec)}

		if params, ok := generatedSchemas[typeName]; ok {
			for _, p := range params {
				schema.Attrs[p] = ParamSpec{}
			}
		} else {
			// No generated schema — allow extra attrs.
			schema.AllowExtra = true
		}

		constraints := typeConstraints[typeName]

		// Supplemental attrs the generator can't detect (variable-key
		// reads, runner-injected values, cross-platform unions).
		for _, p := range constraints.Supplemental {
			if _, exists := schema.Attrs[p]; !exists {
				schema.Attrs[p] = ParamSpec{}
			}
		}

		for _, attr := range constraints.Required {
			spec := schema.Attrs[attr]
			spec.Required = true
			schema.Attrs[attr] = spec
		}
		for _, attr := range constraints.AbsPath {
			spec := schema.Attrs[attr]
			spec.AbsPath = true
			schema.Attrs[attr] = spec
		}
		for _, attr := range constraints.Mode {
			spec := schema.Attrs[attr]
			spec.Mode = true
			schema.Attrs[attr] = spec
		}

		// Mandatory state: every type whose executor actually consumes a
		// `state` param (generatedStateTypes, scraped from the Rust source)
		// must declare it explicitly. State is the resource's lifecycle
		// lever — flipping `present`→`absent` is the cheapest retraction —
		// so it stays visible in every resource that supports it rather
		// than hiding behind a per-type implicit default. Types that ignore
		// state (exec, timezone, the *_interface device executors, …) are
		// not in the set and keep no such requirement.
		if generatedStateTypes[typeName] {
			spec := schema.Attrs["state"]
			spec.Required = true
			schema.Attrs["state"] = spec
		}

		// Reversal: `revert:` is allowed on every reversible type and
		// `on_revert:` on the command-reverted (Group A) types. Both are
		// optional — their presence in source is nudged by modlint's
		// generated annotation, not required by the schema. Read at the
		// agent's dispatch layer, not in executor files, so the scraper
		// can't see them — they're added here.
		if ReversibleTypes[typeName] {
			if _, exists := schema.Attrs["revert"]; !exists {
				schema.Attrs["revert"] = ParamSpec{}
			}
		}
		if CommandReversibleTypes[typeName] {
			if _, exists := schema.Attrs["on_revert"]; !exists {
				schema.Attrs["on_revert"] = ParamSpec{}
			}
		}

		TypeSchemas[typeName] = schema
	}
}

// ValidateConfigcrate checks all resources in a module for idempotency.
// Returns a slice of findings (errors and warnings). A module with any
// SeverityError findings must not be loaded unless dangerous mode is on.
func ValidateConfigcrate(mod Configcrate) []Finding {
	var findings []Finding
	for _, res := range mod.Resources {
		findings = append(findings, validateResource(mod.Name, res)...)
	}
	findings = append(findings, validateCrossReferences(mod)...)
	return findings
}

// validateCrossReferences checks that depends_on, notify, subscribes, and
// before all reference resource names that exist within the same module.
// Also detects redundant notify+subscribes pairs.
func validateCrossReferences(mod Configcrate) []Finding {
	var findings []Finding

	// Build set of known resource names.
	names := make(map[string]bool, len(mod.Resources))
	for _, res := range mod.Resources {
		if res.Name != "" {
			names[res.Name] = true
		}
	}

	// Check all reference lists.
	for _, res := range mod.Resources {
		base := Finding{
			Configcrate:  mod.Name,
			ResourceName: res.Name,
			ResourceType: res.Type,
		}

		// Unknown-target findings are Warning, not Error: single-file
		// lint cannot see resources defined in sibling modules, and
		// cross-module notify/subscribes is the dominant pattern (e.g.
		// a vhost module notifies the `reload-nginx` resource owned by
		// the nginx module). Whole-stacks resolution is the publish
		// path's job. Self-references stay as Error — a resource that
		// references itself is a real bug regardless of module scope.
		for _, ref := range res.DependsOn {
			if !names[ref] {
				f := base
				f.Severity = SeverityWarning
				f.Message = fmt.Sprintf("depends_on references unknown resource %q (may resolve cross-configcrate at publish)", ref)
				findings = append(findings, f)
			}
			if ref == res.Name {
				f := base
				f.Severity = SeverityError
				f.Message = "depends_on references itself"
				findings = append(findings, f)
			}
		}

		for _, ref := range res.Before {
			if !names[ref] {
				f := base
				f.Severity = SeverityWarning
				f.Message = fmt.Sprintf("before references unknown resource %q (may resolve cross-configcrate at publish)", ref)
				findings = append(findings, f)
			}
			if ref == res.Name {
				f := base
				f.Severity = SeverityError
				f.Message = "before references itself"
				findings = append(findings, f)
			}
		}

		for _, ref := range res.Notify {
			if !names[ref] {
				f := base
				f.Severity = SeverityWarning
				f.Message = fmt.Sprintf("notify references unknown resource %q (may resolve cross-configcrate at publish)", ref)
				findings = append(findings, f)
			}
			if ref == res.Name {
				f := base
				f.Severity = SeverityError
				f.Message = "notify references itself"
				findings = append(findings, f)
			}
		}

		for _, ref := range res.Subscribes {
			if !names[ref] {
				f := base
				f.Severity = SeverityWarning
				f.Message = fmt.Sprintf("subscribes references unknown resource %q (may resolve cross-configcrate at publish)", ref)
				findings = append(findings, f)
			}
			if ref == res.Name {
				f := base
				f.Severity = SeverityError
				f.Message = "subscribes references itself"
				findings = append(findings, f)
			}
		}

		// watch_secret references secret paths, not resource names — skip.
	}

	// Detect redundant notify+subscribes pairs.
	// If resource A has notify: [B] and B has subscribes: [A], the trigger is redundant.
	notifyMap := make(map[string]map[string]bool) // notifier → set of targets
	for _, res := range mod.Resources {
		if len(res.Notify) > 0 {
			s := make(map[string]bool, len(res.Notify))
			for _, n := range res.Notify {
				s[n] = true
			}
			notifyMap[res.Name] = s
		}
	}
	for _, res := range mod.Resources {
		for _, sub := range res.Subscribes {
			if targets, ok := notifyMap[sub]; ok && targets[res.Name] {
				f := Finding{
					Configcrate:  mod.Name,
					ResourceName: res.Name,
					ResourceType: res.Type,
					Severity:     SeverityError,
					Message:      fmt.Sprintf("redundant trigger: %q already has notify: [%s] — remove subscribes or notify, not both", sub, res.Name),
				}
				findings = append(findings, f)
			}
		}
	}

	// DAG cycle detection on depends_on + before edges.
	if err := detectCycles(mod.Resources); err != nil {
		findings = append(findings, Finding{
			Configcrate: mod.Name,
			Severity:    SeverityError,
			Message:     err.Error(),
		})
	}

	return findings
}

// detectCycles checks the depends_on + before graph for cycles using DFS.
func detectCycles(resources []Resource) error {
	adj := make(map[string][]string)
	for _, res := range resources {
		adj[res.Name] = append(adj[res.Name], res.DependsOn...)
		for _, b := range res.Before {
			adj[b] = append(adj[b], res.Name)
		}
	}

	const (
		white = 0
		gray  = 1
		black = 2
	)
	color := make(map[string]int)
	var cycle []string

	var dfs func(string) bool
	dfs = func(node string) bool {
		color[node] = gray
		for _, next := range adj[node] {
			switch color[next] {
			case gray:
				cycle = append(cycle, node, next)
				return true
			case white:
				if dfs(next) {
					return true
				}
			}
		}
		color[node] = black
		return false
	}

	for _, res := range resources {
		if color[res.Name] == white {
			if dfs(res.Name) {
				return fmt.Errorf("dependency cycle detected: %s", strings.Join(cycle, " → "))
			}
		}
	}
	return nil
}

// HasErrors returns true if any finding is SeverityError.
func HasErrors(findings []Finding) bool {
	for _, f := range findings {
		if f.Severity == SeverityError {
			return true
		}
	}
	return false
}

// FormatFindings returns a human-readable multi-line string of all findings.
func FormatFindings(findings []Finding) string {
	if len(findings) == 0 {
		return ""
	}
	var b strings.Builder
	for _, f := range findings {
		b.WriteString(f.String())
		b.WriteByte('\n')
	}
	return b.String()
}

func validateResource(moduleName string, res Resource) []Finding {
	var findings []Finding

	base := Finding{
		Configcrate:  moduleName,
		ResourceName: res.Name,
		ResourceType: res.Type,
	}

	if res.Type == "" {
		f := base
		f.Severity = SeverityError
		f.Message = "missing resource type"
		return []Finding{f}
	}

	if res.Name == "" {
		f := base
		f.Severity = SeverityError
		f.Message = "missing resource name"
		findings = append(findings, f)
	}

	if !KnownTypes[res.Type] {
		if attrBool(res.Attributes, "skip_validation") {
			return findings
		}
		f := base
		f.Severity = SeverityError
		f.Message = fmt.Sprintf("unknown resource type %q — add skip_validation: true if this is a custom executor", res.Type)
		findings = append(findings, f)
		return findings
	}

	// Schema-driven validation: required fields, unknown attributes, path/mode checks.
	findings = append(findings, validateSchema(base, res)...)

	// State-value validation: reject a state the type's executor doesn't
	// accept. Cross-type, so a single check rather than a per-type case.
	findings = append(findings, validateStateValue(base, res)...)

	// Type-specific semantic validation (idempotency guards, state enums, etc.).
	switch res.Type {
	case "file":
		findings = append(findings, validateFile(base, res)...)
	case "symlink":
		findings = append(findings, validateSymlink(base, res)...)
	case "exec":
		findings = append(findings, validateExec(base, res)...)
	case "service":
		findings = append(findings, validateService(base, res)...)
	case "cron":
		findings = append(findings, validateCron(base, res)...)
	case "reboot":
		findings = append(findings, validateReboot(base, res)...)
	case "field_edit":
		findings = append(findings, validateFieldEdit(base, res)...)
	case "nonrepo_package":
		findings = append(findings, validateNonRepoPackage(base, res)...)
	case "package":
		findings = append(findings, validatePackage(base, res)...)
	}

	// Reversal: applies across the Group A command-reversible set, so it's a
	// single cross-type check rather than a per-type switch case.
	findings = append(findings, validateReversal(base, res)...)

	return findings
}

// stateValues maps a resource type to the set of `state:` values its executor
// accepts, for types whose vocabulary differs from the universal present|absent
// default. Each set is the UNION across OS-variant executors (e.g. service on
// illumos adds started/disabled/maintenance; swap on BSD adds on/off;
// kernel_module accepts loaded/unloaded aliases) — kept generous so a valid
// platform-specific value is never rejected at publish. Types absent from this
// map accept exactly present|absent.
//
// SoT note: hand-derived by reading agent/src/executors/. The agent is the
// authoritative validator (its executors fail closed on an unknown state); this
// is the early publish-time gate. TestStateValuesMatchExecutors guards these
// value sets against executor drift via an annotated checklist with per-value
// source pointers (the executor vocab is too heterogeneous across platform
// variants to scrape reliably); TestStateValuesKeysAreStateTypes guards the keys.
var stateValues = map[string][]string{
	"service":           {"running", "started", "stopped", "restarted", "reloaded", "enabled", "disabled", "maintenance"},
	"process":           {"running", "stopped"},
	"container":         {"running", "stopped", "absent"},
	"vm":                {"running", "stopped", "defined", "absent"},
	"mount":             {"mounted", "present", "unmounted", "absent"},
	"swap":              {"present", "absent", "on", "off"},
	"kernel_module":     {"present", "absent", "loaded", "unloaded"},
	"network_interface": {"present", "absent", "up", "down"},
}

// retiredStateLatestTypes names the package-family types whose `state: latest`
// was retired in favor of `version: latest` (0.76.91) — state is the lifecycle
// lever (present|absent); which version to hold is the version param's axis.
// validateStateValue points migrations at the new spelling instead of emitting
// the generic invalid-state message.
var retiredStateLatestTypes = map[string]bool{
	"package":         true,
	"npm":             true,
	"pip":             true,
	"gem":             true,
	"nonrepo_package": true,
}

// defaultStateValues is the universal vocabulary for state-consuming types not
// listed in stateValues.
var defaultStateValues = []string{"present", "absent"}

// allowedStates returns the accepted state values for a type.
func allowedStates(typeName string) []string {
	if v, ok := stateValues[typeName]; ok {
		return v
	}
	return defaultStateValues
}

// validateStateValue rejects a `state:` value the type's executor doesn't
// accept. Cross-type (like validateReversal): runs for every state-consuming
// type. Catches typos (state: absnt) and wrong-vocab flips (state: stopped on a
// file) at publish, instead of as a red resource — or a silent no-op — at apply
// time on the agent. Mandatory-state (0.72.9) guarantees presence; this guards
// the value.
func validateStateValue(base Finding, res Resource) []Finding {
	if !generatedStateTypes[res.Type] {
		return nil
	}
	state := attrString(res.Attributes, "state")
	if state == "" {
		return nil // presence is enforced separately by the mandatory-state rule
	}
	if strings.Contains(state, "{{") {
		return nil // templated — resolves at check-in time; the agent validates the rendered value
	}
	allowed := allowedStates(res.Type)
	for _, a := range allowed {
		if state == a {
			return nil
		}
	}
	f := base
	f.Severity = SeverityError
	if state == "latest" && retiredStateLatestTypes[res.Type] {
		f.Message = fmt.Sprintf("type %q: state: latest was retired — use version: latest (see docs/reference/vigo/resources/package.md)", res.Type)
		return []Finding{f}
	}
	f.Message = fmt.Sprintf("invalid state %q for type %q — must be one of: %s", state, res.Type, strings.Join(allowed, ", "))
	return []Finding{f}
}

// validateReversal enforces the publish-time half of the `revert:` lever: a
// Group A (command-reverted) resource that sets `revert: true` must declare a
// non-empty `on_revert:` — its inverse can't be inferred (unlike the Group B
// value-setters, which restore a snapshot). The server can prove this
// statically, so it fails at publish rather than as a red resource at apply.
func validateReversal(base Finding, res Resource) []Finding {
	if !attrIsTrue(res.Attributes, "revert") {
		return nil
	}
	if CommandReversibleTypes[res.Type] {
		onRevert := attrString(res.Attributes, "on_revert")
		if onRevert == "" {
			f := base
			f.Severity = SeverityError
			f.Message = fmt.Sprintf(
				"%s sets revert: true but declares no on_revert: — actions have no inferable inverse; "+
					"declare the command to run on revert, or remove revert:", res.Type)
			return []Finding{f}
		}
		if IsOnRevertNone(onRevert) {
			f := base
			f.Severity = SeverityError
			f.Message = fmt.Sprintf(
				"%s sets revert: true with on_revert: none — these contradict: on_revert: none declares the "+
					"action has no inverse, so it cannot be reverted. Remove revert:, or give a real on_revert: command.", res.Type)
			return []Finding{f}
		}
	}
	return nil
}

// attrIsTrue treats both a YAML bool `true` and the string "true" as true —
// `revert:` may be written either way and both serialize to "true" on the wire.
func attrIsTrue(attrs map[string]any, key string) bool {
	return attrBool(attrs, key) || attrString(attrs, key) == "true"
}

// validateSchema runs data-driven validation from TypeSchemas: checks required
// fields, flags unknown attributes, and validates absolute paths and modes.
//
// Resources with `skip_validation: true` bypass the schema entirely. The flag
// originally only suppressed the unknown-type error (custom executors), but
// some shipped executors — notably registry, windows_update, windows_defender
// — have attribute namespaces that collide with structural YAML keys
// (`name`, `type`) and force operators to write aspirational attribute names
// (`value_name`, `value_type`, etc.) until the executor design is fixed. For
// those, `skip_validation: true` is the operator's explicit acknowledgement
// that the resource is non-canonical and shouldn't be schema-checked.
func validateSchema(base Finding, res Resource) []Finding {
	if attrBool(res.Attributes, "skip_validation") {
		return nil
	}
	schema, ok := TypeSchemas[res.Type]
	if !ok {
		return nil // no schema defined — skip
	}

	var findings []Finding

	// Check required attributes. A resource with a case/match block carries
	// its required attrs inside the branches, so validate each branch's
	// merged attributes (base ∪ branch) instead of the bare base — a branch
	// omitting a required attr (e.g. the mandatory `state`) would otherwise
	// slip through publish. (res.Attributes["case"] is the modlint adapter
	// path, where case/match land untyped in Attributes.)
	hasCase := len(res.CaseMatch) > 0 || res.Attributes["case"] != nil
	isAbsent := attrString(res.Attributes, "state") == "absent"
	if !hasCase && !isAbsent {
		for attr, spec := range schema.Attrs {
			if !spec.Required {
				continue
			}
			if attrString(res.Attributes, attr) == "" {
				f := base
				f.Severity = SeverityError
				f.Message = fmt.Sprintf("%s resource missing required attribute %q", res.Type, attr)
				findings = append(findings, f)
			}
		}
	}
	if len(res.CaseMatch) > 0 {
		findings = append(findings, validateCaseBranches(base, res, schema)...)
	}

	// Check absolute paths and modes.
	for attr, spec := range schema.Attrs {
		if spec.AbsPath {
			findings = append(findings, requireAbsolutePath(base, res, attr)...)
		}
		if spec.Mode {
			findings = append(findings, requireValidMode(base, res, attr)...)
		}
	}

	// Flag unknown attributes (unless AllowExtra is set).
	if !schema.AllowExtra && schema.Attrs != nil {
		for attr := range res.Attributes {
			if universalAttrs[attr] {
				continue
			}
			if _, known := schema.Attrs[attr]; !known {
				f := base
				f.Severity = SeverityError
				f.Message = fmt.Sprintf("unknown attribute %q for %s resource", attr, res.Type)
				findings = append(findings, f)
			}
		}
	}

	return findings
}

// validateCaseBranches checks required attributes against each case/match
// branch's merged attributes (base ∪ branch), the same merge ExpandCase does
// at check-in. Without this, branch-only required attrs (incl. the mandatory
// `state`) are never validated at publish. An absent-state branch is skipped
// (the resource is being deleted there, so its attrs are meaningless).
func validateCaseBranches(base Finding, res Resource, schema TypeSchema) []Finding {
	var findings []Finding
	for branch, branchAttrs := range res.CaseMatch {
		merged := make(map[string]any, len(res.Attributes)+len(branchAttrs))
		for k, v := range res.Attributes {
			// case/match are structural, not real attrs (they only appear in
			// Attributes on the modlint adapter path) — never merge them.
			if k == "case" || k == "match" {
				continue
			}
			merged[k] = v
		}
		for k, v := range branchAttrs {
			merged[k] = v
		}
		if attrString(merged, "state") == "absent" {
			continue
		}
		for attr, spec := range schema.Attrs {
			if !spec.Required {
				continue
			}
			if attrString(merged, attr) == "" {
				f := base
				f.Severity = SeverityError
				f.Message = fmt.Sprintf("%s resource missing required attribute %q in case branch %q", res.Type, attr, branch)
				findings = append(findings, f)
			}
		}
	}
	return findings
}

// validateFile checks file-specific semantic rules (content/source requirement).
// Required fields, path, and mode are handled by validateSchema.
func validateFile(base Finding, res Resource) []Finding {
	var findings []Finding

	state := attrString(res.Attributes, "state")
	if state == "directory" {
		f := base
		f.Severity = SeverityError
		f.Message = "file resource with state: directory — use type: directory instead"
		findings = append(findings, f)
	} else if state != "absent" {
		hasContent := attrString(res.Attributes, "content") != ""
		hasSource := attrString(res.Attributes, "source") != ""
		if !hasContent && !hasSource {
			f := base
			f.Severity = SeverityError
			f.Message = "file resource missing content — add content or source"
			findings = append(findings, f)
		}
	}

	return findings
}

// validateSymlink checks symlink-specific semantic rules (to required unless absent).
// Required fields and path checks are handled by validateSchema.
func validateSymlink(base Finding, res Resource) []Finding {
	state := attrString(res.Attributes, "state")
	if state != "absent" && attrString(res.Attributes, "to") == "" {
		f := base
		f.Severity = SeverityError
		f.Message = "symlink resource missing required attribute \"to\""
		return []Finding{f}
	}
	return nil
}

// validateExec checks that exec resources have at least one idempotency guard.
func validateExec(base Finding, res Resource) []Finding {
	hasOnlyIf := attrString(res.Attributes, "onlyif") != ""
	hasUnless := attrString(res.Attributes, "unless") != ""
	hasSubscribes := len(res.Subscribes) > 0
	hasWatchSecret := len(res.WatchSecret) > 0

	if !hasOnlyIf && !hasUnless && !hasSubscribes && !hasWatchSecret {
		f := base
		f.Severity = SeverityError
		f.Message = "exec resource has no idempotency guard — add onlyif or unless"
		return []Finding{f}
	}
	return nil
}

// validateService checks subscribes gating for restart/reload states.
// The state-value enum itself is validated generically by validateStateValue.
func validateService(base Finding, res Resource) []Finding {
	var findings []Finding

	state := attrString(res.Attributes, "state")
	if (state == "restarted" || state == "reloaded") && len(res.Subscribes) == 0 && !strings.Contains(res.When, "changed") {
		f := base
		f.Severity = SeverityError
		f.Message = fmt.Sprintf("service with state %q runs every convergence — add subscribes or when: \"changed\" to gate on dependency change", state)
		findings = append(findings, f)
	}

	return findings
}

// validateReboot warns if a reboot resource has no subscribes guard.
func validateReboot(base Finding, res Resource) []Finding {
	if len(res.Subscribes) > 0 {
		return nil
	}
	if attrString(res.Attributes, "reboot_required_file") != "" {
		return nil
	}

	f := base
	f.Severity = SeverityError
	f.Message = "reboot resource has no subscribes guard and no reboot_required_file — will reboot every convergence"
	return []Finding{f}
}

// validateCron checks cron-specific semantic rules (schedule completeness).
// Required fields are handled by validateSchema.
func validateCron(base Finding, res Resource) []Finding {
	var findings []Finding

	state := attrString(res.Attributes, "state")
	if state == "absent" {
		return nil
	}

	// Require an explicit schedule. Either the "schedule" field (full cron expression)
	// or all five individual fields (minute, hour, day, month, weekday).
	hasSchedule := attrString(res.Attributes, "schedule") != "" || attrString(res.Attributes, "special") != ""
	if !hasSchedule {
		hasAll := true
		for _, field := range []string{"minute", "hour", "day", "month", "weekday"} {
			if attrString(res.Attributes, field) == "" {
				hasAll = false
				break
			}
		}
		if !hasAll {
			f := base
			f.Severity = SeverityError
			f.Message = "cron resource missing schedule — add schedule or all of minute/hour/day/month/weekday (default is every minute)"
			findings = append(findings, f)
		}
	}

	// Require an explicit user.
	if attrString(res.Attributes, "user") == "" {
		f := base
		f.Severity = SeverityError
		f.Message = "cron resource missing user — add user (default is root)"
		findings = append(findings, f)
	}

	return findings
}

// validateFieldEdit checks field_edit-specific semantic rules (field_value required unless absent).
// Required fields and path checks are handled by validateSchema.
func validateFieldEdit(base Finding, res Resource) []Finding {
	state := attrString(res.Attributes, "state")
	if state != "absent" && attrString(res.Attributes, "field_value") == "" {
		f := base
		f.Severity = SeverityError
		f.Message = "field_edit resource missing field_value (required when state is not absent)"
		return []Finding{f}
	}
	return nil
}

// validateNonRepoPackage checks nonrepo_package-specific semantic rules
// (url/source either-or, URL extension, state enum).
func validateNonRepoPackage(base Finding, res Resource) []Finding {
	var findings []Finding

	url := attrString(res.Attributes, "url")
	source := attrString(res.Attributes, "source")

	if url == "" && source == "" {
		f := base
		f.Severity = SeverityError
		f.Message = "nonrepo_package resource missing required attribute \"url\" or \"source\""
		findings = append(findings, f)
	}

	if url != "" {
		findings = append(findings, requireHTTPURL(base, res, "url")...)
		lower := strings.ToLower(url)
		if qIdx := strings.IndexByte(lower, '?'); qIdx > 0 {
			lower = lower[:qIdx]
		}
		validExt := strings.HasSuffix(lower, ".deb") ||
			strings.HasSuffix(lower, ".rpm") ||
			strings.HasSuffix(lower, ".pkg") ||
			strings.HasSuffix(lower, ".msi")
		if !validExt {
			f := base
			f.Severity = SeverityError
			f.Message = fmt.Sprintf("nonrepo_package url must end in .deb, .rpm, .pkg, or .msi (got %q)", filepath.Base(url))
			findings = append(findings, f)
		}
	}

	return findings
}

// validatePackage guards the package resource's cross-param rules at publish,
// mirroring the agent-side checks so a misuse errors at `config publish`
// rather than as a red resource at apply time. Templated values pass through —
// the agent validates the rendered form.
func validatePackage(base Finding, res Resource) []Finding {
	var findings []Finding

	name := attrString(res.Attributes, "package")
	if name == "" {
		name = attrString(res.Attributes, "name")
	}
	version := attrString(res.Attributes, "version")
	state := attrString(res.Attributes, "state")
	if strings.Contains(version, "{{") || strings.Contains(state, "{{") {
		return nil
	}

	if strings.TrimSpace(name) == "*" && (version != "latest" || (state != "" && state != "present")) {
		f := base
		f.Severity = SeverityError
		f.Message = `package "*" (wildcard) requires version: latest with state: present`
		findings = append(findings, f)
	}

	if attrBool(res.Attributes, "lock_version") && version == "latest" {
		f := base
		f.Severity = SeverityError
		f.Message = "lock_version conflicts with version: latest — a hold blocks exactly the upgrades latest exists to perform"
		findings = append(findings, f)
	}

	return findings
}

// Removed: validateGitResource, validateMount, validateLogrotate, validateTmpfile,
// validateReplace, validateSourcePackage — all fully handled by schema-driven validation.

// requireAbsolutePath emits an error if the given attribute is present but not
// an absolute path. Empty/missing values are skipped (caught by required-field checks).
//
// Path shapes accepted:
//   - Unix absolute: filepath.IsAbs on Linux build hosts catches "/foo/bar".
//   - Windows drive-letter (e.g. "C:\\Program Files\\Foo") — only when the
//     resource's `when:` clause restricts to Windows. Linux-build's
//     filepath.IsAbs returns false for drive-letter paths regardless of
//     intent, so the cross-OS check is explicit.
func requireAbsolutePath(base Finding, res Resource, field string) []Finding {
	v := attrString(res.Attributes, field)
	if v == "" {
		return nil
	}
	// Skip validation on templated values — they resolve at check-in time.
	if strings.Contains(v, "{{") {
		return nil
	}
	if filepath.IsAbs(v) {
		return nil
	}
	if isWindowsAbsPath(v) && resourceIsWindowsGated(res) {
		return nil
	}
	f := base
	f.Severity = SeverityError
	f.Message = fmt.Sprintf("%s %q must be an absolute path", field, v)
	return []Finding{f}
}

// isWindowsAbsPath returns true for "C:\..." / "C:/..." style paths
// regardless of build OS. filepath.IsAbs alone is not OS-portable for
// validating cross-platform module configs.
func isWindowsAbsPath(p string) bool {
	if len(p) < 3 {
		return false
	}
	c := p[0]
	if !((c >= 'A' && c <= 'Z') || (c >= 'a' && c <= 'z')) {
		return false
	}
	if p[1] != ':' {
		return false
	}
	return p[2] == '\\' || p[2] == '/'
}

// resourceIsWindowsGated returns true if the resource's `when:` clause
// restricts it to Windows envoys. The conservative match looks for the
// canonical `os_family('windows')` form in either quote style. A
// resource without that gate writing a `C:\...` path is treated as a
// bug — it would fail at runtime on a non-Windows envoy.
func resourceIsWindowsGated(res Resource) bool {
	if res.When == "" {
		return false
	}
	return strings.Contains(res.When, "os_family('windows')") ||
		strings.Contains(res.When, `os_family("windows")`)
}

// requireValidMode emits an error if the mode attribute is present but not a
// valid octal string (3-4 digits, 0-7 each). Handles YAML's octal-to-int
// coercion (mode: 0644 → int 420 → "0644").
func requireValidMode(base Finding, res Resource, field string) []Finding {
	raw, ok := res.Attributes[field]
	if !ok {
		return nil
	}
	// Coerce YAML int (octal parsed as decimal) back to octal string.
	var v string
	switch t := raw.(type) {
	case string:
		v = t
	case int:
		v = fmt.Sprintf("%04o", t)
	case int64:
		v = fmt.Sprintf("%04o", t)
	default:
		v = fmt.Sprintf("%v", raw)
	}
	if v == "" {
		return nil
	}
	if len(v) < 3 || len(v) > 4 {
		f := base
		f.Severity = SeverityError
		f.Message = fmt.Sprintf("mode %q must be a 3- or 4-digit octal string (e.g. \"0644\")", v)
		return []Finding{f}
	}
	if _, err := strconv.ParseUint(v, 8, 32); err != nil {
		f := base
		f.Severity = SeverityError
		f.Message = fmt.Sprintf("mode %q is not valid octal", v)
		return []Finding{f}
	}
	return nil
}

// requireHTTPURL emits an error if the given attribute is present but not a
// valid http:// or https:// URL. Other schemes (git://, ssh://) are allowed
// through without complaint since they can't be aliveness-checked.
func requireHTTPURL(base Finding, res Resource, field string) []Finding {
	v := attrString(res.Attributes, field)
	if v == "" {
		return nil
	}
	if strings.HasPrefix(v, "http://") || strings.HasPrefix(v, "https://") {
		// Basic format check: must have something after the scheme+authority.
		after := strings.TrimPrefix(strings.TrimPrefix(v, "https://"), "http://")
		if after == "" || (!strings.Contains(after, ".") && !strings.HasPrefix(after, "localhost")) {
			f := base
			f.Severity = SeverityError
			f.Message = fmt.Sprintf("%s %q has no valid hostname", field, v)
			return []Finding{f}
		}
	}
	return nil
}

func attrBool(attrs map[string]any, key string) bool {
	if attrs == nil {
		return false
	}
	v, ok := attrs[key]
	if !ok {
		return false
	}
	b, _ := v.(bool)
	return b
}

func attrString(attrs map[string]any, key string) string {
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
