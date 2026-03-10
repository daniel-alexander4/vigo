package modules

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// TmpfileExecutor manages systemd-tmpfiles configuration entries.
// Creates config files in /etc/tmpfiles.d/ and applies them.
// Params:
//   - name: config name (used as filename, e.g. "myapp" → /etc/tmpfiles.d/myapp.conf)
//   - entry_type: tmpfiles type (default "d" for directory; see tmpfiles.d(5))
//   - path: target path (required)
//   - mode: permissions (default "0755")
//   - user: owner (default "-")
//   - group: group owner (default "-")
//   - age: cleanup age (default "-", e.g. "10d")
//   - state: present (default) or absent
type TmpfileExecutor struct {
	TmpfilesDir string // override for testing (default /etc/tmpfiles.d)
	Runner      CommandRunner
}

func (e *TmpfileExecutor) tmpfilesDir() string {
	if e.TmpfilesDir != "" {
		return e.TmpfilesDir
	}
	return "/etc/tmpfiles.d"
}

func (e *TmpfileExecutor) runner() CommandRunner {
	if e.Runner != nil {
		return e.Runner
	}
	return &DefaultRunner{}
}

func (e *TmpfileExecutor) Execute(ctx context.Context, params map[string]string, dryRun bool) ResourceResult {
	start := time.Now()
	name := params["name"]
	state := params["state"]
	if state == "" {
		state = "present"
	}

	result := ResourceResult{
		ResourceType: "tmpfile",
		ResourceName: name,
	}
	defer func() { result.DurationMs = int32(time.Since(start).Milliseconds()) }()

	if name == "" {
		result.Error = fmt.Errorf("tmpfile executor: name param is required")
		return result
	}

	switch state {
	case "present":
		return e.ensurePresent(ctx, name, params, dryRun, result)
	case "absent":
		return e.ensureAbsent(ctx, name, dryRun, result)
	default:
		result.Error = fmt.Errorf("tmpfile executor: unknown state %q", state)
		return result
	}
}

func (e *TmpfileExecutor) ensurePresent(ctx context.Context, name string, params map[string]string, dryRun bool, result ResourceResult) ResourceResult {
	path := params["path"]
	if path == "" {
		result.Error = fmt.Errorf("tmpfile executor: path param is required for state=present")
		return result
	}

	entryType := params["entry_type"]
	if entryType == "" {
		entryType = "d"
	}
	mode := params["mode"]
	if mode == "" {
		mode = "0755"
	}
	user := params["user"]
	if user == "" {
		user = "-"
	}
	group := params["group"]
	if group == "" {
		group = "-"
	}
	age := params["age"]
	if age == "" {
		age = "-"
	}

	desiredLine := fmt.Sprintf("%s %s %s %s %s %s", entryType, path, mode, user, group, age)
	confPath := e.confPath(name)

	// Check current state.
	existing, err := os.ReadFile(confPath)
	if err == nil && strings.TrimSpace(string(existing)) == desiredLine {
		result.Action = ActionNone
		return result
	}

	result.Changed = true
	if os.IsNotExist(err) || err != nil {
		result.Action = ActionCreated
	} else {
		result.Action = ActionUpdated
	}

	if dryRun {
		return result
	}

	if err := os.MkdirAll(e.tmpfilesDir(), 0755); err != nil {
		result.Error = fmt.Errorf("creating %s: %w", e.tmpfilesDir(), err)
		return result
	}

	if err := os.WriteFile(confPath, []byte(desiredLine+"\n"), 0644); err != nil {
		result.Error = fmt.Errorf("writing %s: %w", confPath, err)
		return result
	}

	// Apply the config.
	out, err := e.runner().Run(ctx, "systemd-tmpfiles", "--create", confPath)
	if err != nil {
		result.Error = fmt.Errorf("systemd-tmpfiles --create %s: %w: %s", confPath, err, string(out))
	}

	return result
}

func (e *TmpfileExecutor) ensureAbsent(_ context.Context, name string, dryRun bool, result ResourceResult) ResourceResult {
	confPath := e.confPath(name)
	if _, err := os.Stat(confPath); os.IsNotExist(err) {
		result.Action = ActionNone
		return result
	}

	result.Action = ActionDeleted
	result.Changed = true

	if !dryRun {
		if err := os.Remove(confPath); err != nil && !os.IsNotExist(err) {
			result.Error = fmt.Errorf("removing %s: %w", confPath, err)
		}
	}

	return result
}

func (e *TmpfileExecutor) confPath(name string) string {
	return filepath.Join(e.tmpfilesDir(), name+".conf")
}
