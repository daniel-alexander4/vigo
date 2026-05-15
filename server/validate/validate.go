// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

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
	Module       string
	ResourceName string
	ResourceType string
	Message      string
}

func (f Finding) String() string {
	sev := "ERROR"
	if f.Severity == SeverityWarning {
		sev = "WARN"
	}
	loc := f.Module
	if f.ResourceName != "" {
		loc += "/" + f.ResourceName
	}
	return fmt.Sprintf("[%s] %s: %s", sev, loc, f.Message)
}

// Resource mirrors the fields of config.ResourceConfig that we need for
// validation, avoiding a circular import. The caller maps from their types.
type Resource struct {
	Name             string
	Type             string
	When             string
	Attributes       map[string]any
	DependsOn        []string
	Before           []string
	Notify           []string
	Subscribes       []string
	WatchSecret []string
}

// Module mirrors config.ModuleDefinition for validation purposes.
type Module struct {
	Name      string
	Resources []Resource
}

// KnownTypes is the canonical set of valid resource types. Exported so
// modlint can use the same list — there must be exactly one source of truth.
var KnownTypes = map[string]bool{
	"file":             true,
	"directory":        true,
	"symlink":          true,
	"package":          true,
	"service":          true,
	"user":             true,
	"exec":             true,
	"repository":       true,
	"source_package":   true,
	"nonrepo_package":  true,
	"cron":             true,
	"process":          true,
	"file_line":        true,
	"group":            true,
	"host":             true,
	"sysctl":           true,
	"mount":            true,
	"hostname":         true,
	"timezone":         true,
	"sshkey":           true,
	"firewall":         true,
	"kernel_module":    true,
	"swap":             true,
	"tmpfile":          true,
	"ini":              true,
	"json_file":        true,
	"logrotate":        true,
	"git":              true,
	"reboot":           true,
	"alternatives":     true,
	"pip":              true,
	"gem":              true,
	"npm":              true,
	"blockinfile":      true,
	"container":      true,
	"docker_compose": true,
	"replace":          true,
	"field_edit":       true,
	"systemd_dropin":   true,
	"selinux":          true,
	// Network device types.
	"cisco_vlan": true, "cisco_interface": true, "network_banner": true,
	"network_ntp": true, "network_syslog": true, "network_snmp": true,
	"junos_interface": true, "ssh_exec": true, "network_acl": true,
	"network_static_route": true, "network_user": true,
	"network_config": true, "cisco_acl": true,
	"network_interface": true, "network_vlan": true, "network_dns": true,
	// macOS/BSD.
	"service_macos": true, "user_macos": true, "group_macos": true,
	"hostname_macos": true, "timezone_macos": true,
	"service_freebsd": true, "user_freebsd": true, "group_freebsd": true,
	"kernel_module_freebsd": true, "service_openbsd": true,
	"firewall_pf": true, "hostname_bsd": true, "mount_bsd": true,
	"swap_bsd": true, "sysctl_bsd": true, "timezone_bsd": true,
	// illumos.
	"service_illumos": true, "timezone_illumos": true,
	// Windows.
	"service_windows": true, "package_windows": true, "user_windows": true,
	"firewall_windows": true, "cron_windows": true, "hostname_windows": true,
	"timezone_windows": true, "registry": true, "reboot_windows": true,
	"local_security_policy": true,
	"windows_update": true,
	"windows_defender": true, "windows_env": true, "domain_membership": true,
	"certificate_windows": true, "service_recovery_windows": true,
	"powershell": true,
	// Backup.
	"backup": true, "db_backup": true,
}

// universalAttrs are attributes allowed on any resource type. These are not
// flagged as unknown regardless of type-specific schema.
var universalAttrs = map[string]bool{
	"state": true, "onlyif": true, "unless": true, "creates": true,
	"skip_validation": true, "when": true, "timeout_secs": true,
	"case": true, "match": true, "watch_secret": true,
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
//   scripts/gen-executor-schemas.sh > server/validate/schemas_gen.go
var TypeSchemas = map[string]TypeSchema{}
// requiredAttrs defines which attributes are required per type.
var requiredAttrs = map[string][]string{
	"file":            {"target_path", "owner", "group", "mode"},
	"directory":       {"target_path"},
	"symlink":         {"target_path"},
	"package":         {"package"},
	"service":         {"service"},
	"user":            {"username"},
	"group":           {"group"},
	"exec":            {"command"},
	"source_package":  {"url"},
	"cron":            {"command", "user"},
	"process":         {"process"},
	"file_line":       {"target_path"},
	"host":            {"hostname"},
	"sysctl":          {"key", "value"},
	"mount":           {"path"},
	"hostname":        {"hostname"},
	"timezone":        {"timezone"},
	"sshkey":          {"hostname", "sshkey"},
	"firewall":        {"port"},
	"kernel_module":   {"module"},
	"swap":            {"path"},
	"tmpfile":         {"path"},
	"ini":             {"target_path", "key"},
	"json_file":       {"target_path", "key"},
	"logrotate":       {"path"},
	"git":             {"repo", "path"},
	"alternatives":    {"name", "path"},
	"pip":             {"package"},
	"gem":             {"package"},
	"npm":             {"package"},
	"blockinfile":     {"target_path", "block_content"},
	"replace":         {"target_path", "match_pattern", "replacement"},
	"field_edit":      {"path", "line_match", "select_field"},
	"systemd_dropin":  {"unit", "content"},
	// `container` requires `image` OR `artifact`; the mutual-exclusion +
	// "at least one for state=running" enforcement lives in the
	// container-image-source modlint rule, since requiredAttrs can't
	// express the OR.
	"docker_compose": {"path"},
	"backup":          {"source_dir", "target_path"},
	"db_backup":       {"db_type", "source_db", "target_path"},
}

// absPathAttrs defines which attributes must be absolute paths.
var absPathAttrs = map[string][]string{
	"file": {"target_path"}, "directory": {"target_path"}, "symlink": {"target_path"},
	"source_package": {"target_path", "binary_path"}, "file_line": {"target_path"},
	"mount": {"path"}, "swap": {"path"}, "tmpfile": {"path"},
	"ini": {"target_path"}, "json_file": {"target_path"}, "logrotate": {"path"},
	"git": {"path"}, "alternatives": {"path"}, "pip": {"virtualenv"},
	"blockinfile": {"target_path"}, "replace": {"target_path"}, "field_edit": {"path"},
	"selinux": {"path"}, "docker_compose": {"path"},
	"backup": {"source_dir", "target_path"}, "db_backup": {"target_path"},
	"exec": {"cwd"},
}

// modeAttrs defines which attributes must be valid octal modes.
var modeAttrs = map[string][]string{
	"file": {"mode"}, "directory": {"mode"}, "tmpfile": {"mode"},
	"blockinfile": {"mode"}, "source_package": {"mode"},
}

// supplementalAttrs adds params that the generator can't auto-detect
// (e.g., variable-key access like params.get(*k) in a loop).
// supplementalAttrs adds params that the generator can't auto-detect
// (variable-key access, runner-level params, cross-platform unions).
var supplementalAttrs = map[string][]string{
	"cron":    {"minute", "hour", "day", "month", "weekday"},
	"package": {"package", "name", "version", "allow_downgrade", "lock_version", "purge", "post_cleanup", "update_cache"},
}

func init() {
	// Build TypeSchemas from generatedSchemas (auto-extracted from Rust source)
	// plus semantic constraints (Required, AbsPath, Mode) and supplemental attrs.
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

		// Add supplemental attrs the generator can't detect.
		for _, p := range supplementalAttrs[typeName] {
			if _, exists := schema.Attrs[p]; !exists {
				schema.Attrs[p] = ParamSpec{}
			}
		}

		for _, attr := range requiredAttrs[typeName] {
			spec := schema.Attrs[attr]
			spec.Required = true
			schema.Attrs[attr] = spec
		}
		for _, attr := range absPathAttrs[typeName] {
			spec := schema.Attrs[attr]
			spec.AbsPath = true
			schema.Attrs[attr] = spec
		}
		for _, attr := range modeAttrs[typeName] {
			spec := schema.Attrs[attr]
			spec.Mode = true
			schema.Attrs[attr] = spec
		}

		TypeSchemas[typeName] = schema
	}
}

// ValidateModule checks all resources in a module for idempotency.
// Returns a slice of findings (errors and warnings). A module with any
// SeverityError findings must not be loaded unless dangerous mode is on.
func ValidateModule(mod Module) []Finding {
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
func validateCrossReferences(mod Module) []Finding {
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
			Module:       mod.Name,
			ResourceName: res.Name,
			ResourceType: res.Type,
		}

		for _, ref := range res.DependsOn {
			if !names[ref] {
				f := base
				f.Severity = SeverityError
				f.Message = fmt.Sprintf("depends_on references unknown resource %q", ref)
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
				f.Severity = SeverityError
				f.Message = fmt.Sprintf("before references unknown resource %q", ref)
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
				f.Severity = SeverityError
				f.Message = fmt.Sprintf("notify references unknown resource %q", ref)
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
				f.Severity = SeverityError
				f.Message = fmt.Sprintf("subscribes references unknown resource %q", ref)
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
					Module:       mod.Name,
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
			Module:   mod.Name,
			Severity: SeverityError,
			Message:  err.Error(),
		})
	}

	return findings
}

// detectCycles checks the depends_on + before graph for cycles using DFS.
func detectCycles(resources []Resource) error {
	adj := make(map[string][]string)
	for _, res := range resources {
		for _, dep := range res.DependsOn {
			adj[res.Name] = append(adj[res.Name], dep)
		}
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
		Module:       moduleName,
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
	}

	return findings
}

// validateSchema runs data-driven validation from TypeSchemas: checks required
// fields, flags unknown attributes, and validates absolute paths and modes.
func validateSchema(base Finding, res Resource) []Finding {
	schema, ok := TypeSchemas[res.Type]
	if !ok {
		return nil // no schema defined — skip
	}

	var findings []Finding

	// Check required attributes. Skip if:
	// - resource uses case/match (required attrs are inside conditional blocks)
	// - state is "absent" (resource is being deleted, attributes are meaningless)
	hasCase := res.Attributes["case"] != nil
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

// validServiceStates are the allowed values for service.state.
var validServiceStates = map[string]bool{
	"running":   true,
	"stopped":   true,
	"restarted": true,
	"reloaded":  true,
}

// validateService checks service state enum and subscribes gating.
func validateService(base Finding, res Resource) []Finding {
	var findings []Finding

	state := attrString(res.Attributes, "state")
	if state != "" && !validServiceStates[state] {
		f := base
		f.Severity = SeverityError
		f.Message = fmt.Sprintf("invalid service state %q — must be one of: running, stopped, restarted, reloaded", state)
		findings = append(findings, f)
	}

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

	state := attrString(res.Attributes, "state")
	if state != "" && state != "present" && state != "absent" && state != "latest" {
		f := base
		f.Severity = SeverityError
		f.Message = fmt.Sprintf("nonrepo_package state must be present, absent, or latest (got %q)", state)
		findings = append(findings, f)
	}

	return findings
}

// Removed: validateGitResource, validateMount, validateLogrotate, validateTmpfile,
// validateReplace, validateSourcePackage — all fully handled by schema-driven validation.

// requireAbsolutePath emits an error if the given attribute is present but not
// an absolute path. Empty/missing values are skipped (caught by required-field checks).
func requireAbsolutePath(base Finding, res Resource, field string) []Finding {
	v := attrString(res.Attributes, field)
	if v == "" {
		return nil
	}
	// Skip validation on templated values — they resolve at check-in time.
	if strings.Contains(v, "{{") {
		return nil
	}
	if !filepath.IsAbs(v) {
		f := base
		f.Severity = SeverityError
		f.Message = fmt.Sprintf("%s %q must be an absolute path", field, v)
		return []Finding{f}
	}
	return nil
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
