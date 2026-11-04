// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: Apache-2.0

package validate

import (
	"fmt"
	"path"
	"strings"
)

// The lists below are a publish-time preview of the agent's guardrails; the
// agent (agent/src/guardrails.rs) is the authority and enforces them on the
// envoy. They are a hand copy of the agent's unix lists plus its Windows
// dangerous patterns, and TestGuardrailListsMatchAgent fails when the two
// drift.

// Exempt paths — children of protected paths that resources may write
// (scan output). Mirrors the agent's EXEMPT_PATHS (unix).
var exemptPaths = []string{
	"/var/lib/vigo/security/",
}

// Protected paths — resources targeting these are flagged at config load time.
// Mirrors the agent's PROTECTED_PATHS (unix).
var protectedPaths = []string{
	// Agent (Linux)
	"/usr/local/sbin/vigo",
	"/etc/vigo-envoy/",
	"/etc/vigo/",
	"/var/lib/vigo/",
	"/etc/systemd/system/vigo-envoy.service",
	"/etc/systemd/system/vigo-envoy.service.d/",
	"/etc/systemd/system/vigosrv.service.d/",
	// Agent (macOS)
	"/usr/local/etc/vigo-envoy/",
	"/Library/Vigo/",
	"/Library/LaunchDaemons/com.vigo.envoy.plist",
	// Agent (FreeBSD)
	"/usr/local/etc/rc.d/vigo_envoy",
	// Agent (illumos)
	"/var/svc/manifest/system/vigo-envoy.xml",
	// Server
	"/usr/local/sbin/vigosrv",
	"/usr/local/bin/vigocli",
	"/srv/vigo/",
	"/etc/systemd/system/vigosrv.service",
}

// Protected services — cannot be stopped/disabled/masked.
var protectedServices = []string{"vigo-envoy", "vigosrv"}

// Protected packages — cannot be removed.
var protectedPackages = []string{"vigo", "vigosrv", "vigocli"}

// Dangerous command patterns for exec resources. Mirrors the agent's
// DANGEROUS_PATTERNS (unix, then Windows).
var dangerousPatterns = []string{
	"systemctl stop vigo",
	"systemctl disable vigo",
	"systemctl mask vigo",
	"systemctl stop vigosrv",
	"systemctl disable vigosrv",
	"systemctl mask vigosrv",
	"service vigo-envoy stop",
	"service vigo_envoy stop",
	"service vigosrv stop",
	"rcctl stop vigo",
	"rcctl disable vigo",
	"launchctl unload com.vigo",
	"launchctl remove com.vigo",
	"launchctl bootout com.vigo",
	"svcadm disable vigo",
	"svccfg delete vigo",
	"pkill vigo",
	"pkill vigosrv",
	"killall vigo",
	"killall vigosrv",
	"kill -9 vigo",
	"kill -9 vigosrv",
	"rm /usr/local/sbin/vigo",
	"rm /usr/local/sbin/vigosrv",
	"rm /usr/local/bin/vigocli",
	"rm -rf /etc/vigo",
	"rm -rf /var/lib/vigo",
	"rm -rf /srv/vigo",
	"rm -rf /etc/vigo-envoy",
	"rm -rf /library/vigo",
	// Windows
	"sc stop vigo",
	"sc delete vigo",
	"taskkill /im vigo",
	"del /f vigo.exe",
	"rmdir /s vigo",
}

// normalizeCommand lowercases a command, collapses whitespace runs to one
// space and collapses `//` and `/./` in paths, so `systemctl  stop vigo` or
// `rm /etc//vigo/x` cannot slip past a substring pattern. This is a
// heuristic preview; the structural checks are the real boundary.
func normalizeCommand(command string) string {
	s := strings.Join(strings.Fields(strings.ToLower(command)), " ")
	for {
		next := strings.ReplaceAll(strings.ReplaceAll(s, "/./", "/"), "//", "/")
		if next == s {
			return s
		}
		s = next
	}
}

// Violation is the typed result of a guardrail check on a command. It
// implements error so ValidatePushCommand can keep returning a plain
// error; callers that want the structured fields (REST handlers
// building a 400 body, audit rows attributing the matched pattern) use
// errors.As to pull it out. Pattern is the exact dangerous-command
// substring or protected path that triggered the block — sufficient for
// audit attribution without re-running the matcher.
type Violation struct {
	Reason  string // human-readable: "command matches dangerous pattern" or "command targets protected path"
	Pattern string // the matched dangerous-command substring or protected path
}

// Error implements error: "<reason>: <pattern>".
func (v *Violation) Error() string {
	if v.Pattern == "" {
		return v.Reason
	}
	return v.Reason + ": " + v.Pattern
}

// ValidateSelfProtection checks a module's resources for guardrail violations.
// Returns findings with SeverityError for any resource that would be blocked
// by the agent's hardcoded guardrails.
func ValidateSelfProtection(mod Configcrate) []Finding {
	var findings []Finding
	for _, res := range mod.Resources {
		findings = append(findings, checkSelfProtection(mod.Name, res)...)
	}
	return findings
}

// ValidatePushCommand checks an ad-hoc push command for guardrail
// violations. Returns nil on pass, or a *Violation (as error) carrying
// the reason + matched pattern on hit.
func ValidatePushCommand(command string) error {
	lower := normalizeCommand(command)
	for _, pattern := range dangerousPatterns {
		if strings.Contains(lower, pattern) {
			return &Violation{Reason: "command matches dangerous pattern", Pattern: pattern}
		}
	}
	for _, p := range protectedPaths {
		trimmed := strings.ToLower(strings.TrimRight(p, "/"))
		if (strings.Contains(lower, "rm ") || strings.Contains(lower, "unlink ")) &&
			strings.Contains(lower, trimmed) {
			return &Violation{Reason: "command targets protected path", Pattern: p}
		}
	}
	return nil
}

func checkSelfProtection(moduleName string, res Resource) []Finding {
	base := Finding{
		Configcrate:  moduleName,
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
	_, target := IdentityAttr(res.Type, res.Attributes)
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
	_, service := IdentityAttr(res.Type, res.Attributes)
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
	_, pkg := IdentityAttr(res.Type, res.Attributes)
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
	_, command := IdentityAttr(res.Type, res.Attributes)
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

// matchProtectedPath cleans target (so `/etc//vigo/x` and `/etc/./vigo`
// match) and returns the protected path it falls under, or "". Exempt paths
// win, as on the agent.
func matchProtectedPath(target string) string {
	normalized := target
	if strings.HasPrefix(target, "/") {
		normalized = path.Clean(target)
	}
	normalized = strings.TrimRight(normalized, "/")
	for _, exempt := range exemptPaths {
		enorm := strings.TrimRight(exempt, "/")
		if normalized == enorm || strings.HasPrefix(normalized, enorm+"/") {
			return ""
		}
	}
	for _, protected := range protectedPaths {
		pnorm := strings.TrimRight(protected, "/")
		if normalized == pnorm || strings.HasPrefix(normalized, pnorm+"/") {
			return protected
		}
	}
	return ""
}
