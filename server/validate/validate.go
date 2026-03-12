// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: BUSL-1.1

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
	Notify           []string
	Subscribes       []string
	SubscribesSecret []string
}

// Module mirrors config.ModuleDefinition for validation purposes.
type Module struct {
	Name      string
	Resources []Resource
}

// knownTypes are the resource types with built-in idempotent executors.
var knownTypes = map[string]bool{
	"file":             true,
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
}

// ValidateModule checks all resources in a module for idempotency.
// Returns a slice of findings (errors and warnings). A module with any
// SeverityError findings must not be loaded unless dangerous mode is on.
func ValidateModule(mod Module) []Finding {
	var findings []Finding
	for _, res := range mod.Resources {
		findings = append(findings, validateResource(mod.Name, res)...)
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
		f := base
		f.Severity = SeverityWarning
		f.Message = fmt.Sprintf("unknown resource type %q (custom executor required on agent)", res.Type)
		findings = append(findings, f)
		return findings
	}

	switch res.Type {
	case "file":
		findings = append(findings, validateFile(base, res)...)
	case "exec":
		findings = append(findings, validateExec(base, res)...)
	case "service":
		findings = append(findings, validateService(base, res)...)
	case "reboot":
		findings = append(findings, validateReboot(base, res)...)
	}

	return findings
}

// validateFile checks that file resources declare required attributes:
// targetpath, owner, group, mode, and one of content/source (unless state=absent).
func validateFile(base Finding, res Resource) []Finding {
	var findings []Finding

	for _, field := range []string{"targetpath", "owner", "group", "mode"} {
		if attrString(res.Attributes, field) == "" {
			f := base
			f.Severity = SeverityError
			f.Message = fmt.Sprintf("file resource missing required attribute %q", field)
			findings = append(findings, f)
		}
	}

	state := attrString(res.Attributes, "state")
	if state != "absent" {
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

// validateExec checks that exec resources have at least one idempotency guard.
func validateExec(base Finding, res Resource) []Finding {
	hasCreates := attrString(res.Attributes, "creates") != ""
	hasOnlyIf := attrString(res.Attributes, "onlyif") != ""
	hasUnless := attrString(res.Attributes, "unless") != ""
	hasSubscribes := len(res.Subscribes) > 0
	hasSubscribesSecret := len(res.SubscribesSecret) > 0

	if !hasCreates && !hasOnlyIf && !hasUnless && !hasSubscribes && !hasSubscribesSecret {
		f := base
		f.Severity = SeverityError
		f.Message = "exec resource has no idempotency guard — add creates, onlyif, or unless"
		return []Finding{f}
	}
	return nil
}

// validateService checks that restarted/reloaded services are gated by subscribes.
func validateService(base Finding, res Resource) []Finding {
	state := attrString(res.Attributes, "state")
	if state != "restarted" && state != "reloaded" {
		return nil
	}

	if len(res.Subscribes) > 0 {
		return nil
	}

	f := base
	f.Severity = SeverityWarning
	f.Message = fmt.Sprintf("service with state %q runs every convergence — consider adding subscribes", state)
	return []Finding{f}
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
	f.Severity = SeverityWarning
	f.Message = "reboot resource has no subscribes guard and no reboot_required_file — will reboot every convergence"
	return []Finding{f}
}

func attrString(attrs map[string]any, key string) string {
	if attrs == nil {
		return ""
	}
	v, ok := attrs[key]
	if !ok {
		return ""
	}
	s, _ := v.(string)
	return s
}
