package modules

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"strings"
	"time"
)

// HostExecutor manages /etc/hosts entries idempotently.
// Params:
//   - hostname (or name): the hostname to manage
//   - ip: IP address (required for state=present)
//   - aliases: optional space-separated additional hostnames
//   - state: present (default) or absent
type HostExecutor struct {
	HostsFile string // override for testing (default /etc/hosts)
}

func (e *HostExecutor) hostsFile() string {
	if e.HostsFile != "" {
		return e.HostsFile
	}
	return "/etc/hosts"
}

func (e *HostExecutor) Execute(ctx context.Context, params map[string]string, dryRun bool) ResourceResult {
	start := time.Now()
	hostname := params["hostname"]
	if hostname == "" {
		hostname = params["name"]
	}
	ip := params["ip"]
	aliases := params["aliases"]
	state := params["state"]
	if state == "" {
		state = "present"
	}

	result := ResourceResult{
		ResourceType: "host",
		ResourceName: hostname,
	}
	defer func() { result.DurationMs = int32(time.Since(start).Milliseconds()) }()

	if hostname == "" {
		result.Error = fmt.Errorf("host executor: hostname or name param is required")
		return result
	}

	switch state {
	case "present":
		if ip == "" {
			result.Error = fmt.Errorf("host executor: ip param is required for state=present")
			return result
		}
		return e.ensurePresent(hostname, ip, aliases, dryRun, result)
	case "absent":
		return e.ensureAbsent(hostname, dryRun, result)
	default:
		result.Error = fmt.Errorf("host executor: unknown state %q", state)
		return result
	}
}

func (e *HostExecutor) ensurePresent(hostname, ip, aliases string, dryRun bool, result ResourceResult) ResourceResult {
	desiredLine := buildHostLine(ip, hostname, aliases)

	lines, err := readLines(e.hostsFile())
	if err != nil && !os.IsNotExist(err) {
		result.Error = fmt.Errorf("reading %s: %w", e.hostsFile(), err)
		return result
	}

	// Check if an exact match already exists.
	for _, line := range lines {
		if normalizeHostLine(line) == normalizeHostLine(desiredLine) {
			result.Action = ActionNone
			return result
		}
	}

	// Remove any existing entries for this hostname, then append the new one.
	var newLines []string
	for _, line := range lines {
		if !lineMatchesHost(line, hostname) {
			newLines = append(newLines, line)
		}
	}
	newLines = append(newLines, desiredLine)

	result.Changed = true
	if lineExistsForHost(lines, hostname) {
		result.Action = ActionUpdated
	} else {
		result.Action = ActionCreated
	}

	if !dryRun {
		if err := writeLines(e.hostsFile(), newLines); err != nil {
			result.Error = fmt.Errorf("writing %s: %w", e.hostsFile(), err)
		}
	}
	return result
}

func (e *HostExecutor) ensureAbsent(hostname string, dryRun bool, result ResourceResult) ResourceResult {
	lines, err := readLines(e.hostsFile())
	if err != nil {
		if os.IsNotExist(err) {
			result.Action = ActionNone
			return result
		}
		result.Error = fmt.Errorf("reading %s: %w", e.hostsFile(), err)
		return result
	}

	if !lineExistsForHost(lines, hostname) {
		result.Action = ActionNone
		return result
	}

	var newLines []string
	for _, line := range lines {
		if !lineMatchesHost(line, hostname) {
			newLines = append(newLines, line)
		}
	}

	result.Action = ActionDeleted
	result.Changed = true
	if !dryRun {
		if err := writeLines(e.hostsFile(), newLines); err != nil {
			result.Error = fmt.Errorf("writing %s: %w", e.hostsFile(), err)
		}
	}
	return result
}

func buildHostLine(ip, hostname, aliases string) string {
	line := ip + "\t" + hostname
	if aliases != "" {
		line += " " + aliases
	}
	return line
}

// lineMatchesHost returns true if the /etc/hosts line contains the given hostname.
func lineMatchesHost(line, hostname string) bool {
	trimmed := strings.TrimSpace(line)
	if trimmed == "" || strings.HasPrefix(trimmed, "#") {
		return false
	}
	fields := strings.Fields(trimmed)
	for _, f := range fields[1:] { // skip IP
		if f == hostname {
			return true
		}
	}
	return false
}

func lineExistsForHost(lines []string, hostname string) bool {
	for _, line := range lines {
		if lineMatchesHost(line, hostname) {
			return true
		}
	}
	return false
}

func normalizeHostLine(line string) string {
	return strings.Join(strings.Fields(strings.TrimSpace(line)), " ")
}

func readLines(path string) ([]string, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	var lines []string
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		lines = append(lines, scanner.Text())
	}
	return lines, scanner.Err()
}

func writeLines(path string, lines []string) error {
	content := strings.Join(lines, "\n") + "\n"
	return os.WriteFile(path, []byte(content), 0644)
}
