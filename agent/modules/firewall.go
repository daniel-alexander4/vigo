package modules

import (
	"context"
	"fmt"
	"strings"
	"time"
)

// FirewallExecutor manages ufw firewall rules.
// Params:
//   - port: port number, range (e.g., "8080", "3000:3100"), or service name
//   - proto: tcp, udp, or any (default "any")
//   - action: allow, deny, or reject (required)
//   - from: source address (default "any")
//   - to: destination address (default "any")
//   - direction: in or out (default "in")
//   - state: present (default) or absent
//   - comment: optional rule comment
type FirewallExecutor struct {
	Runner CommandRunner
}

func (e *FirewallExecutor) runner() CommandRunner {
	if e.Runner != nil {
		return e.Runner
	}
	return &DefaultRunner{}
}

func (e *FirewallExecutor) Execute(ctx context.Context, params map[string]string, dryRun bool) ResourceResult {
	start := time.Now()
	port := params["port"]
	if port == "" {
		port = params["name"]
	}
	proto := params["proto"]
	if proto == "" || proto == "any" {
		proto = ""
	}
	action := params["action"]
	from := params["from"]
	if from == "" {
		from = "any"
	}
	direction := params["direction"]
	if direction == "" {
		direction = "in"
	}
	state := params["state"]
	if state == "" {
		state = "present"
	}
	comment := params["comment"]

	result := ResourceResult{
		ResourceType: "firewall",
		ResourceName: port,
	}
	defer func() { result.DurationMs = int32(time.Since(start).Milliseconds()) }()

	if port == "" {
		result.Error = fmt.Errorf("firewall executor: port or name param is required")
		return result
	}
	if action == "" && state == "present" {
		result.Error = fmt.Errorf("firewall executor: action param is required (allow, deny, or reject)")
		return result
	}

	// Build the rule spec for checking and applying.
	ruleSpec := e.buildRuleSpec(port, proto, action, from, direction)

	switch state {
	case "present":
		return e.ensurePresent(ctx, ruleSpec, comment, dryRun, result)
	case "absent":
		return e.ensureAbsent(ctx, port, proto, from, direction, dryRun, result)
	default:
		result.Error = fmt.Errorf("firewall executor: unknown state %q", state)
		return result
	}
}

func (e *FirewallExecutor) ensurePresent(ctx context.Context, ruleSpec []string, comment string, dryRun bool, result ResourceResult) ResourceResult {
	// Check if rule exists.
	if e.ruleExists(ctx, ruleSpec) {
		result.Action = ActionNone
		return result
	}

	result.Action = ActionCreated
	result.Changed = true

	if dryRun {
		return result
	}

	args := append([]string{"--force"}, ruleSpec...)
	if comment != "" {
		args = append(args, "comment", comment)
	}
	if _, err := e.runner().Run(ctx, "ufw", args...); err != nil {
		result.Error = fmt.Errorf("ufw %s: %w", strings.Join(ruleSpec, " "), err)
	}
	return result
}

func (e *FirewallExecutor) ensureAbsent(ctx context.Context, port, proto, from, direction string, dryRun bool, result ResourceResult) ResourceResult {
	// Build a delete spec.
	deleteSpec := e.buildDeleteSpec(port, proto, from, direction)

	// Check if any matching rule exists.
	if !e.ruleExists(ctx, deleteSpec[1:]) { // skip "delete"
		result.Action = ActionNone
		return result
	}

	result.Action = ActionDeleted
	result.Changed = true

	if dryRun {
		return result
	}

	args := append([]string{"--force"}, deleteSpec...)
	if _, err := e.runner().Run(ctx, "ufw", args...); err != nil {
		result.Error = fmt.Errorf("ufw delete: %w", err)
	}
	return result
}

// buildRuleSpec builds ufw arguments like: allow in from any to any port 80 proto tcp
func (e *FirewallExecutor) buildRuleSpec(port, proto, action, from, direction string) []string {
	args := []string{action, direction}

	if from != "any" {
		args = append(args, "from", from)
	}

	args = append(args, "to", "any", "port", port)

	if proto != "" {
		args = append(args, "proto", proto)
	}

	return args
}

// buildDeleteSpec builds ufw delete arguments.
func (e *FirewallExecutor) buildDeleteSpec(port, proto, from, direction string) []string {
	args := []string{"delete", "allow", direction}
	if from != "any" {
		args = append(args, "from", from)
	}
	args = append(args, "to", "any", "port", port)
	if proto != "" {
		args = append(args, "proto", proto)
	}
	return args
}

// ruleExists checks ufw status for a matching rule.
func (e *FirewallExecutor) ruleExists(ctx context.Context, ruleSpec []string) bool {
	out, err := e.runner().Run(ctx, "ufw", "status", "verbose")
	if err != nil {
		return false
	}

	// Parse the port and proto from ruleSpec to search in status output.
	var port, proto, direction string
	for i, arg := range ruleSpec {
		switch arg {
		case "port":
			if i+1 < len(ruleSpec) {
				port = ruleSpec[i+1]
			}
		case "proto":
			if i+1 < len(ruleSpec) {
				proto = ruleSpec[i+1]
			}
		case "in", "out":
			direction = arg
		}
	}

	if port == "" {
		return false
	}

	// Search for the port in the ufw status output.
	// UFW status format: "80/tcp    ALLOW IN    Anywhere"
	needle := port
	if proto != "" {
		needle = port + "/" + proto
	}

	for _, line := range strings.Split(string(out), "\n") {
		line = strings.TrimSpace(line)
		if strings.Contains(line, needle) {
			if direction == "" || strings.Contains(strings.ToUpper(line), strings.ToUpper(direction)) {
				return true
			}
		}
	}

	return false
}
