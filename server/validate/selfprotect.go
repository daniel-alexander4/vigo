// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

package validate

import (
	"fmt"
	"strings"
)

// Protected paths — resources targeting these are flagged at config load time.
// Mirrors the agent-side list in agent-rs/src/guardrails.rs.
var protectedPaths = []string{
	// Agent
	"/usr/local/sbin/vigo",
	"/etc/vigo-envoy/",
	"/etc/vigo/",
	"/var/lib/vigo/",
	"/etc/systemd/system/vigo-envoy.service",
	// Server
	"/usr/local/sbin/vigosrv",
	"/usr/local/bin/vigogo",
	"/usr/local/sbin/vigo-license-manager",
	"/srv/vigo/",
	"/etc/systemd/system/vigosrv.service",
}

// Protected services — cannot be stopped/disabled/masked.
var protectedServices = []string{"vigo-envoy", "vigosrv"}

// Protected packages — cannot be removed.
var protectedPackages = []string{"vigo", "vigosrv", "vigogo"}

// Dangerous command patterns for exec resources.
var dangerousPatterns = []string{
	"systemctl stop vigo",
	"systemctl disable vigo",
	"systemctl mask vigo",
	"systemctl stop vigosrv",
	"systemctl disable vigosrv",
	"systemctl mask vigosrv",
	"service vigo-envoy stop",
	"service vigosrv stop",
	"pkill vigo",
	"pkill vigosrv",
	"killall vigo",
	"killall vigosrv",
}

// ValidateSelfProtection checks a module's resources for guardrail violations.
// Returns findings with SeverityError for any resource that would be blocked
// by the agent's hardcoded guardrails.
func ValidateSelfProtection(mod Module) []Finding {
	var findings []Finding
	for _, res := range mod.Resources {
		findings = append(findings, checkSelfProtection(mod.Name, res)...)
	}
	return findings
}

// ValidatePushCommand checks an ad-hoc push command for guardrail violations.
func ValidatePushCommand(command string) error {
	lower := strings.ToLower(command)
	for _, pattern := range dangerousPatterns {
		if strings.Contains(lower, pattern) {
			return fmt.Errorf("command matches dangerous pattern: %q", pattern)
		}
	}
	for _, path := range protectedPaths {
		trimmed := strings.TrimRight(path, "/")
		if (strings.Contains(lower, "rm ") || strings.Contains(lower, "unlink ")) &&
			strings.Contains(lower, trimmed) {
			return fmt.Errorf("command targets protected path: %s", path)
		}
	}
	return nil
}

func checkSelfProtection(moduleName string, res Resource) []Finding {
	base := Finding{
		Module:       moduleName,
		ResourceName: res.Name,
		ResourceType: res.Type,
		Severity:     SeverityError,
	}

	switch res.Type {
	case "file", "directory", "symlink", "file_line", "blockinfile", "ini", "json_file", "replace", "field_edit":
		return checkFileProtection(base, res)
	case "service":
		return checkServiceProtection(base, res)
	case "package":
		return checkPackageProtection(base, res)
	case "exec":
		return checkExecProtection(base, res)
	}
	return nil
}

func checkFileProtection(base Finding, res Resource) []Finding {
	target := attrString(res.Attributes, "target_path")
	if target == "" {
		target = attrString(res.Attributes, "path")
	}
	if target == "" {
		return nil
	}

	if matched := matchProtectedPath(target); matched != "" {
		f := base
		f.Message = fmt.Sprintf("targets protected path %s (agent will block this resource)", matched)
		return []Finding{f}
	}
	return nil
}

func checkServiceProtection(base Finding, res Resource) []Finding {
	service := attrString(res.Attributes, "service")
	if service == "" {
		service = attrString(res.Attributes, "name")
	}
	state := attrString(res.Attributes, "state")

	for _, protected := range protectedServices {
		if service == protected {
			switch state {
			case "stopped", "disabled", "masked", "absent":
				f := base
				f.Message = fmt.Sprintf("cannot set protected service %q to state %q (agent will block this)", service, state)
				return []Finding{f}
			}
		}
	}
	return nil
}

func checkPackageProtection(base Finding, res Resource) []Finding {
	pkg := attrString(res.Attributes, "package")
	if pkg == "" {
		pkg = attrString(res.Attributes, "name")
	}
	state := attrString(res.Attributes, "state")

	if state == "absent" {
		for _, protected := range protectedPackages {
			if pkg == protected {
				f := base
				f.Message = fmt.Sprintf("cannot remove protected package %q (agent will block this)", pkg)
				return []Finding{f}
			}
		}
	}
	return nil
}

func checkExecProtection(base Finding, res Resource) []Finding {
	command := attrString(res.Attributes, "command")
	if command == "" {
		command = attrString(res.Attributes, "cmd")
	}
	if command == "" {
		return nil
	}

	if err := ValidatePushCommand(command); err != nil {
		f := base
		f.Message = fmt.Sprintf("exec command blocked: %s", err)
		return []Finding{f}
	}
	return nil
}

func matchProtectedPath(target string) string {
	normalized := strings.TrimRight(target, "/")
	for _, protected := range protectedPaths {
		pnorm := strings.TrimRight(protected, "/")
		if normalized == pnorm || strings.HasPrefix(normalized, pnorm+"/") {
			return protected
		}
	}
	return ""
}
