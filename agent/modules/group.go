package modules

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"
)

// GroupExecutor manages system groups via groupadd/groupmod/groupdel.
// Params:
//   - group (or name): group name
//   - state: present (default) or absent
//   - gid: optional numeric GID
type GroupExecutor struct {
	GroupFile string // override for testing (default /etc/group)
	Runner   CommandRunner
}

func (e *GroupExecutor) groupFile() string {
	if e.GroupFile != "" {
		return e.GroupFile
	}
	return "/etc/group"
}

func (e *GroupExecutor) runner() CommandRunner {
	if e.Runner != nil {
		return e.Runner
	}
	return &DefaultRunner{}
}

func (e *GroupExecutor) Execute(ctx context.Context, params map[string]string, dryRun bool) ResourceResult {
	start := time.Now()
	name := params["group"]
	if name == "" {
		name = params["name"]
	}
	state := params["state"]
	if state == "" {
		state = "present"
	}
	gid := params["gid"]

	result := ResourceResult{
		ResourceType: "group",
		ResourceName: name,
	}
	defer func() { result.DurationMs = int32(time.Since(start).Milliseconds()) }()

	if name == "" {
		result.Error = fmt.Errorf("group executor: group or name param is required")
		return result
	}

	exists, currentGID := e.groupExists(name)

	switch state {
	case "present":
		if exists {
			// Check if GID needs updating.
			if gid != "" && gid != currentGID {
				result.Action = ActionUpdated
				result.Changed = true
				if !dryRun {
					if _, err := e.runner().Run(ctx, "groupmod", "-g", gid, name); err != nil {
						result.Error = fmt.Errorf("groupmod: %w", err)
					}
				}
				return result
			}
			result.Action = ActionNone
			return result
		}
		result.Action = ActionCreated
		result.Changed = true
		if !dryRun {
			args := []string{name}
			if gid != "" {
				args = []string{"-g", gid, name}
			}
			if _, err := e.runner().Run(ctx, "groupadd", args...); err != nil {
				result.Error = fmt.Errorf("groupadd: %w", err)
			}
		}

	case "absent":
		if !exists {
			result.Action = ActionNone
			return result
		}
		result.Action = ActionDeleted
		result.Changed = true
		if !dryRun {
			if _, err := e.runner().Run(ctx, "groupdel", name); err != nil {
				result.Error = fmt.Errorf("groupdel: %w", err)
			}
		}

	default:
		result.Error = fmt.Errorf("group executor: unknown state %q (want present or absent)", state)
	}

	return result
}

// groupExists checks /etc/group for the named group. Returns (exists, gid).
func (e *GroupExecutor) groupExists(name string) (bool, string) {
	f, err := os.Open(e.groupFile())
	if err != nil {
		return false, ""
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		parts := strings.SplitN(scanner.Text(), ":", 4)
		if len(parts) >= 3 && parts[0] == name {
			return true, parts[2]
		}
	}
	return false, ""
}

// groupExistsCmd is a fallback using getent (for systems without readable /etc/group).
func (e *GroupExecutor) groupExistsCmd(ctx context.Context, name string) bool {
	cmd := exec.CommandContext(ctx, "getent", "group", name)
	return cmd.Run() == nil
}
