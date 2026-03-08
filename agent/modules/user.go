package modules

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"strings"
	"time"
)

// UserExecutor manages user resources idempotently. It parses /etc/passwd
// for existence and attribute comparison before creating, modifying, or
// removing users.
type UserExecutor struct {
	Runner     CommandRunner
	PasswdPath string // defaults to /etc/passwd if empty
}

func (u *UserExecutor) runner() CommandRunner {
	if u.Runner != nil {
		return u.Runner
	}
	return &DefaultRunner{}
}

func (u *UserExecutor) passwdPath() string {
	if u.PasswdPath != "" {
		return u.PasswdPath
	}
	return "/etc/passwd"
}

// passwdEntry holds parsed fields from a /etc/passwd line.
type passwdEntry struct {
	name  string
	uid   string
	gid   string
	gecos string
	home  string
	shell string
}

// lookupUser parses the passwd file for a user entry.
func (u *UserExecutor) lookupUser(name string) (*passwdEntry, error) {
	f, err := os.Open(u.passwdPath())
	if err != nil {
		return nil, fmt.Errorf("open passwd file: %w", err)
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := scanner.Text()
		fields := strings.Split(line, ":")
		if len(fields) >= 7 && fields[0] == name {
			return &passwdEntry{
				name:  fields[0],
				uid:   fields[2],
				gid:   fields[3],
				gecos: fields[4],
				home:  fields[5],
				shell: fields[6],
			}, nil
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("read passwd file: %w", err)
	}
	return nil, nil // user not found
}

// Execute applies the desired user state based on params.
// Supported params:
//   - name: username (required)
//   - state: "present" (default) or "absent"
//   - uid: desired user ID (optional)
//   - shell: desired login shell (optional)
//   - home: desired home directory (optional)
//   - group: desired primary group (optional)
func (u *UserExecutor) Execute(ctx context.Context, params map[string]string, dryRun bool) ResourceResult {
	start := time.Now()
	name := params["name"]
	result := ResourceResult{
		ResourceType: "user",
		ResourceName: name,
	}

	defer func() {
		result.DurationMs = int32(time.Since(start).Milliseconds())
	}()

	if name == "" {
		result.Error = fmt.Errorf("user executor: name param is required")
		return result
	}

	state := params["state"]
	if state == "" {
		state = "present"
	}

	entry, err := u.lookupUser(name)
	if err != nil {
		result.Error = err
		return result
	}
	exists := entry != nil

	switch state {
	case "present":
		if !exists {
			return u.createUser(ctx, name, params, dryRun, result)
		}
		return u.updateUser(ctx, name, params, entry, dryRun, result)

	case "absent":
		if !exists {
			result.Action = ActionNone
			result.Changed = false
			return result
		}
		result.Action = ActionDeleted
		result.Changed = true
		if dryRun {
			return result
		}
		out, err := u.runner().Run(ctx, "userdel", name)
		if err != nil {
			result.Error = fmt.Errorf("userdel %s: %w: %s", name, err, string(out))
		}
		return result

	default:
		result.Error = fmt.Errorf("user executor: unknown state %q", state)
		return result
	}
}

// createUser runs useradd with the specified attributes.
func (u *UserExecutor) createUser(ctx context.Context, name string, params map[string]string, dryRun bool, result ResourceResult) ResourceResult {
	result.Action = ActionCreated
	result.Changed = true

	if dryRun {
		return result
	}

	args := u.buildUseraddArgs(name, params)
	out, err := u.runner().Run(ctx, "useradd", args...)
	if err != nil {
		result.Error = fmt.Errorf("useradd %s: %w: %s", name, err, string(out))
	}
	return result
}

// updateUser checks if attributes differ and runs usermod if needed.
func (u *UserExecutor) updateUser(ctx context.Context, name string, params map[string]string, entry *passwdEntry, dryRun bool, result ResourceResult) ResourceResult {
	// Check if any attributes need to change
	needsUpdate := false
	var modArgs []string

	if uid, ok := params["uid"]; ok && uid != "" && uid != entry.uid {
		needsUpdate = true
		modArgs = append(modArgs, "-u", uid)
	}

	if shell, ok := params["shell"]; ok && shell != "" && shell != entry.shell {
		needsUpdate = true
		modArgs = append(modArgs, "-s", shell)
	}

	if home, ok := params["home"]; ok && home != "" && home != entry.home {
		needsUpdate = true
		modArgs = append(modArgs, "-d", home)
	}

	if group, ok := params["group"]; ok && group != "" {
		// Can't compare GID to group name easily, but if specified we include it
		modArgs = append(modArgs, "-g", group)
		needsUpdate = true
	}

	if !needsUpdate {
		result.Action = ActionNone
		result.Changed = false
		return result
	}

	result.Action = ActionUpdated
	result.Changed = true

	if dryRun {
		return result
	}

	modArgs = append(modArgs, name)
	out, err := u.runner().Run(ctx, "usermod", modArgs...)
	if err != nil {
		result.Error = fmt.Errorf("usermod %s: %w: %s", name, err, string(out))
	}
	return result
}

// buildUseraddArgs constructs the arguments for the useradd command.
func (u *UserExecutor) buildUseraddArgs(name string, params map[string]string) []string {
	var args []string

	if uid, ok := params["uid"]; ok && uid != "" {
		args = append(args, "-u", uid)
	}
	if shell, ok := params["shell"]; ok && shell != "" {
		args = append(args, "-s", shell)
	}
	if home, ok := params["home"]; ok && home != "" {
		args = append(args, "-d", home)
	}
	if group, ok := params["group"]; ok && group != "" {
		args = append(args, "-g", group)
	}

	args = append(args, name)
	return args
}
