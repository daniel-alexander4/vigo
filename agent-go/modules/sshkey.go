// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: BUSL-1.1

package modules

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"
)

// SSHKeyExecutor manages SSH known_hosts entries.
// Params:
//   - hostname (or name): remote hostname
//   - type: key type (ssh-rsa, ssh-ed25519, ecdsa-sha2-nistp256, etc.)
//   - key: the public key data
//   - state: present (default) or absent
//   - aliases: optional comma-separated additional hostnames/IPs
//   - file: known_hosts file path (default /etc/ssh/ssh_known_hosts)
type SSHKeyExecutor struct{}

func (e *SSHKeyExecutor) Execute(ctx context.Context, params map[string]string, dryRun bool) ResourceResult {
	start := time.Now()
	hostname := params["hostname"]
	if hostname == "" {
		hostname = params["name"]
	}
	keyType := params["type"]
	keyData := params["key"]
	aliases := params["aliases"]
	state := params["state"]
	if state == "" {
		state = "present"
	}
	file := params["file"]
	if file == "" {
		file = "/etc/ssh/ssh_known_hosts"
	}

	result := ResourceResult{
		ResourceType: "sshkey",
		ResourceName: hostname,
	}
	defer func() { result.DurationMs = int32(time.Since(start).Milliseconds()) }()

	if hostname == "" {
		result.Error = fmt.Errorf("sshkey executor: hostname or name param is required")
		return result
	}

	switch state {
	case "present":
		if keyType == "" || keyData == "" {
			result.Error = fmt.Errorf("sshkey executor: type and key params are required for state=present")
			return result
		}
		return e.ensurePresent(hostname, keyType, keyData, aliases, file, dryRun, result)
	case "absent":
		return e.ensureAbsent(hostname, file, dryRun, result)
	default:
		result.Error = fmt.Errorf("sshkey executor: unknown state %q", state)
		return result
	}
}

func (e *SSHKeyExecutor) ensurePresent(hostname, keyType, keyData, aliases, file string, dryRun bool, result ResourceResult) ResourceResult {
	hostField := buildSSHHostField(hostname, aliases)
	desiredLine := hostField + " " + keyType + " " + keyData

	lines, err := readLines(file)
	if err != nil && !os.IsNotExist(err) {
		result.Error = fmt.Errorf("reading %s: %w", file, err)
		return result
	}

	// Check for exact match.
	for _, line := range lines {
		if strings.TrimSpace(line) == desiredLine {
			result.Action = ActionNone
			return result
		}
	}

	// Remove any existing entry for this hostname+type, then add new one.
	var newLines []string
	existed := false
	for _, line := range lines {
		if sshLineMatchesHostAndType(line, hostname, keyType) {
			existed = true
			continue
		}
		newLines = append(newLines, line)
	}
	newLines = append(newLines, desiredLine)

	result.Changed = true
	if existed {
		result.Action = ActionUpdated
	} else {
		result.Action = ActionCreated
	}

	if !dryRun {
		if err := writeLines(file, newLines); err != nil {
			result.Error = fmt.Errorf("writing %s: %w", file, err)
		}
	}
	return result
}

func (e *SSHKeyExecutor) ensureAbsent(hostname, file string, dryRun bool, result ResourceResult) ResourceResult {
	lines, err := readLines(file)
	if err != nil {
		if os.IsNotExist(err) {
			result.Action = ActionNone
			return result
		}
		result.Error = fmt.Errorf("reading %s: %w", file, err)
		return result
	}

	var newLines []string
	found := false
	for _, line := range lines {
		if sshLineMatchesHost(line, hostname) {
			found = true
			continue
		}
		newLines = append(newLines, line)
	}

	if !found {
		result.Action = ActionNone
		return result
	}

	result.Action = ActionDeleted
	result.Changed = true
	if !dryRun {
		if err := writeLines(file, newLines); err != nil {
			result.Error = fmt.Errorf("writing %s: %w", file, err)
		}
	}
	return result
}

func buildSSHHostField(hostname, aliases string) string {
	if aliases == "" {
		return hostname
	}
	parts := []string{hostname}
	for _, a := range strings.Split(aliases, ",") {
		a = strings.TrimSpace(a)
		if a != "" {
			parts = append(parts, a)
		}
	}
	return strings.Join(parts, ",")
}

func sshLineMatchesHost(line, hostname string) bool {
	trimmed := strings.TrimSpace(line)
	if trimmed == "" || strings.HasPrefix(trimmed, "#") {
		return false
	}
	fields := strings.Fields(trimmed)
	if len(fields) < 3 {
		return false
	}
	hosts := strings.Split(fields[0], ",")
	for _, h := range hosts {
		if h == hostname {
			return true
		}
	}
	return false
}

func sshLineMatchesHostAndType(line, hostname, keyType string) bool {
	trimmed := strings.TrimSpace(line)
	if trimmed == "" || strings.HasPrefix(trimmed, "#") {
		return false
	}
	fields := strings.Fields(trimmed)
	if len(fields) < 3 {
		return false
	}
	if fields[1] != keyType {
		return false
	}
	hosts := strings.Split(fields[0], ",")
	for _, h := range hosts {
		if h == hostname {
			return true
		}
	}
	return false
}
