// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

// Package validate provides static idempotency analysis for Hobnob resource
// definitions. It checks that every resource in a module definition is provably
// idempotent before the config is allowed to go live.
//
// Resource types that are inherently idempotent (check-before-act by design):
//   - file, package, service, user, repository, archive, cron
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

// knownTypes are the resource types with built-in idempotent executors.
var knownTypes = map[string]bool{
	"file":             true,
	"directory":        true,
	"symlink":          true,
	"package":          true,
	"service":          true,
	"user":             true,
	"exec":             true,
	"repository":       true,
	"archive":          true,
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
	"docker_container": true,
	"docker_image":     true,
	"docker_compose":   true,
	"replace":          true,
	"field_edit":       true,
	"systemd_dropin":   true,
	"selinux":          true,
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
		}

		for _, ref := range res.Notify {
			if !names[ref] {
				f := base
				f.Severity = SeverityError
				f.Message = fmt.Sprintf("notify references unknown resource %q", ref)
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

	return findings
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

	if !knownTypes[res.Type] {
		if attrBool(res.Attributes, "skip_validation") {
			return findings
		}
		f := base
		f.Severity = SeverityError
		f.Message = fmt.Sprintf("unknown resource type %q — add skip_validation: true if this is a custom executor", res.Type)
		findings = append(findings, f)
		return findings
	}

	switch res.Type {
	case "file":
		findings = append(findings, validateFile(base, res)...)
	case "directory":
		findings = append(findings, validateDirectory(base, res)...)
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
	case "replace":
		findings = append(findings, validateReplace(base, res)...)
	case "field_edit":
		findings = append(findings, validateFieldEdit(base, res)...)
	case "archive":
		findings = append(findings, validateArchive(base, res)...)
	case "git":
		findings = append(findings, validateGitResource(base, res)...)
	case "mount":
		findings = append(findings, validateMount(base, res)...)
	case "logrotate":
		findings = append(findings, validateLogrotate(base, res)...)
	case "tmpfile":
		findings = append(findings, validateTmpfile(base, res)...)
	}

	return findings
}

// validateFile checks that file resources declare required attributes:
// target_path, owner, group, mode, and one of content/source (unless state=absent).
func validateFile(base Finding, res Resource) []Finding {
	var findings []Finding

	for _, field := range []string{"target_path", "owner", "group", "mode"} {
		if attrString(res.Attributes, field) == "" {
			f := base
			f.Severity = SeverityError
			f.Message = fmt.Sprintf("file resource missing required attribute %q", field)
			findings = append(findings, f)
		}
	}

	findings = append(findings, requireAbsolutePath(base, res, "target_path")...)
	findings = append(findings, requireValidMode(base, res, "mode")...)

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

// validateDirectory checks that directory resources have target_path.
// owner, group, mode are optional (defaults: root/root/0755).
func validateDirectory(base Finding, res Resource) []Finding {
	var findings []Finding

	if attrString(res.Attributes, "target_path") == "" {
		f := base
		f.Severity = SeverityError
		f.Message = "directory resource missing required attribute \"target_path\""
		findings = append(findings, f)
	}

	findings = append(findings, requireAbsolutePath(base, res, "target_path")...)
	findings = append(findings, requireValidMode(base, res, "mode")...)

	return findings
}

// validateSymlink checks that symlink resources have target_path and to.
func validateSymlink(base Finding, res Resource) []Finding {
	var findings []Finding

	if attrString(res.Attributes, "target_path") == "" {
		f := base
		f.Severity = SeverityError
		f.Message = "symlink resource missing required attribute \"target_path\""
		findings = append(findings, f)
	}

	findings = append(findings, requireAbsolutePath(base, res, "target_path")...)

	state := attrString(res.Attributes, "state")
	if state != "absent" && attrString(res.Attributes, "to") == "" {
		f := base
		f.Severity = SeverityError
		f.Message = "symlink resource missing required attribute \"to\""
		findings = append(findings, f)
	}

	return findings
}

// validateExec checks that exec resources have at least one idempotency guard.
func validateExec(base Finding, res Resource) []Finding {
	hasCreates := attrString(res.Attributes, "creates") != ""
	hasOnlyIf := attrString(res.Attributes, "onlyif") != ""
	hasUnless := attrString(res.Attributes, "unless") != ""
	hasSubscribes := len(res.Subscribes) > 0
	hasWatchSecret := len(res.WatchSecret) > 0

	if !hasCreates && !hasOnlyIf && !hasUnless && !hasSubscribes && !hasWatchSecret {
		f := base
		f.Severity = SeverityError
		f.Message = "exec resource has no idempotency guard — add creates, onlyif, or unless"
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

	if (state == "restarted" || state == "reloaded") && len(res.Subscribes) == 0 {
		f := base
		f.Severity = SeverityError
		f.Message = fmt.Sprintf("service with state %q runs every convergence — add subscribes to gate on dependency change", state)
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

// validateCron checks that cron resources explicitly declare schedule and user.
// Without these, the defaults are "* * * * *" (every minute) as "root" — dangerous.
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

// validateReplace checks that replace resources declare required attributes.
func validateReplace(base Finding, res Resource) []Finding {
	var findings []Finding
	for _, field := range []string{"target_path", "match_pattern", "replacement"} {
		if attrString(res.Attributes, field) == "" {
			f := base
			f.Severity = SeverityError
			f.Message = fmt.Sprintf("replace resource missing required attribute %q", field)
			findings = append(findings, f)
		}
	}
	findings = append(findings, requireAbsolutePath(base, res, "target_path")...)
	return findings
}

// validateFieldEdit checks that field_edit resources declare required attributes.
func validateFieldEdit(base Finding, res Resource) []Finding {
	var findings []Finding
	for _, field := range []string{"path", "line_match", "select_field"} {
		if attrString(res.Attributes, field) == "" {
			f := base
			f.Severity = SeverityError
			f.Message = fmt.Sprintf("field_edit resource missing required attribute %q", field)
			findings = append(findings, f)
		}
	}
	findings = append(findings, requireAbsolutePath(base, res, "path")...)

	state := attrString(res.Attributes, "state")
	if state != "absent" && attrString(res.Attributes, "field_value") == "" {
		f := base
		f.Severity = SeverityError
		f.Message = "field_edit resource missing field_value (required when state is not absent)"
		findings = append(findings, f)
	}
	return findings
}

// validateArchive checks that archive resources have a url and valid target_path/mode.
func validateArchive(base Finding, res Resource) []Finding {
	var findings []Finding

	if attrString(res.Attributes, "url") == "" {
		f := base
		f.Severity = SeverityError
		f.Message = "archive resource missing required attribute \"url\""
		findings = append(findings, f)
	}
	findings = append(findings, requireHTTPURL(base, res, "url")...)
	findings = append(findings, requireAbsolutePath(base, res, "target_path")...)
	findings = append(findings, requireValidMode(base, res, "mode")...)

	return findings
}

// validateGitResource checks that git resources have repo and an absolute path.
func validateGitResource(base Finding, res Resource) []Finding {
	var findings []Finding

	if attrString(res.Attributes, "repo") == "" {
		f := base
		f.Severity = SeverityError
		f.Message = "git resource missing required attribute \"repo\""
		findings = append(findings, f)
	}

	if attrString(res.Attributes, "path") == "" {
		f := base
		f.Severity = SeverityError
		f.Message = "git resource missing required attribute \"path\""
		findings = append(findings, f)
	}
	findings = append(findings, requireAbsolutePath(base, res, "path")...)

	return findings
}

// validateMount checks that mount resources have path.
func validateMount(base Finding, res Resource) []Finding {
	var findings []Finding

	if attrString(res.Attributes, "path") == "" {
		f := base
		f.Severity = SeverityError
		f.Message = "mount resource missing required attribute \"path\""
		findings = append(findings, f)
	}
	findings = append(findings, requireAbsolutePath(base, res, "path")...)

	return findings
}

// validateLogrotate checks that logrotate resources have an absolute path.
func validateLogrotate(base Finding, res Resource) []Finding {
	return requireAbsolutePath(base, res, "path")
}

// validateTmpfile checks that tmpfile resources have an absolute path and valid mode.
func validateTmpfile(base Finding, res Resource) []Finding {
	var findings []Finding
	findings = append(findings, requireAbsolutePath(base, res, "path")...)
	findings = append(findings, requireValidMode(base, res, "mode")...)
	return findings
}

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
		if after == "" || !strings.Contains(after, ".") && !strings.Contains(after, "localhost") {
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
