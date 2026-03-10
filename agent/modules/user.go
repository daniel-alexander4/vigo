package modules

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// UserExecutor manages user resources idempotently. It parses /etc/passwd
// for existence and attribute comparison before creating, modifying, or
// removing users.
type UserExecutor struct {
	Runner     CommandRunner
	PasswdPath string // defaults to /etc/passwd if empty
	ShadowPath string // defaults to /etc/shadow if empty
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

func (u *UserExecutor) shadowPath() string {
	if u.ShadowPath != "" {
		return u.ShadowPath
	}
	return "/etc/shadow"
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

// lookupShadowHash reads the password hash for a user from the shadow file.
func (u *UserExecutor) lookupShadowHash(name string) (string, error) {
	f, err := os.Open(u.shadowPath())
	if err != nil {
		return "", fmt.Errorf("open shadow file: %w", err)
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		fields := strings.SplitN(scanner.Text(), ":", 3)
		if len(fields) >= 2 && fields[0] == name {
			return fields[1], nil
		}
	}
	if err := scanner.Err(); err != nil {
		return "", fmt.Errorf("read shadow file: %w", err)
	}
	return "", nil
}

// currentGroups returns the supplementary groups for a user via `id -nG`.
func (u *UserExecutor) currentGroups(ctx context.Context, name string) ([]string, error) {
	out, err := u.runner().Run(ctx, "id", "-nG", name)
	if err != nil {
		return nil, fmt.Errorf("id -nG %s: %w", name, err)
	}
	var groups []string
	for _, g := range strings.Fields(strings.TrimSpace(string(out))) {
		groups = append(groups, g)
	}
	return groups, nil
}

// missingGroups returns desired groups the user is not already a member of.
func missingGroups(current, desired []string) []string {
	have := make(map[string]bool, len(current))
	for _, g := range current {
		have[g] = true
	}
	var missing []string
	for _, g := range desired {
		if !have[g] {
			missing = append(missing, g)
		}
	}
	return missing
}

// parseGroups splits a comma-separated groups string into a sorted slice.
func parseGroups(s string) []string {
	var groups []string
	for _, g := range strings.Split(s, ",") {
		g = strings.TrimSpace(g)
		if g != "" {
			groups = append(groups, g)
		}
	}
	sort.Strings(groups)
	return groups
}

// resolveHome returns the user's home directory: explicit param, passwd entry, or /home/<name>.
func resolveHome(name string, params map[string]string, entry *passwdEntry) string {
	if h, ok := params["home"]; ok && h != "" {
		return h
	}
	if entry != nil && entry.home != "" {
		return entry.home
	}
	return "/home/" + name
}

// Execute applies the desired user state based on params.
// Supported params:
//   - username: login name (required)
//   - state: "present" (default) or "absent"
//   - uid: desired user ID (optional)
//   - shell: desired login shell (optional)
//   - home: desired home directory (optional)
//   - group: desired primary group (optional)
//   - comment: GECOS field, e.g. "First Last" (optional)
//   - password: pre-hashed password, e.g. $6$... (optional, checked against /etc/shadow)
//   - groups: supplementary groups, comma-separated (optional, additive)
//   - authorized_keys: SSH public keys, one per line (optional, declarative)
func (u *UserExecutor) Execute(ctx context.Context, params map[string]string, dryRun bool) ResourceResult {
	start := time.Now()
	name := params["username"]
	if name == "" {
		name = params["name"]
	}
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

// createUser runs useradd with the specified attributes, then applies
// supplementary groups and authorized_keys.
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
		return result
	}

	// Supplementary groups (additive via usermod -aG).
	if groups, ok := params["groups"]; ok && groups != "" {
		desired := parseGroups(groups)
		if len(desired) > 0 {
			modArgs := []string{"-aG", strings.Join(desired, ","), name}
			out, err := u.runner().Run(ctx, "usermod", modArgs...)
			if err != nil {
				result.Error = fmt.Errorf("usermod -aG %s: %w: %s", name, err, string(out))
				return result
			}
		}
	}

	// Authorized keys.
	if keys, ok := params["authorized_keys"]; ok && keys != "" {
		// Re-read passwd to get the home dir after useradd.
		entry, err := u.lookupUser(name)
		if err != nil {
			result.Error = fmt.Errorf("reading home dir after create: %w", err)
			return result
		}
		home := resolveHome(name, params, entry)
		if err := u.writeAuthorizedKeys(ctx, name, home, keys); err != nil {
			result.Error = err
			return result
		}
	}

	return result
}

// updateUser checks if attributes differ and runs usermod if needed,
// then applies supplementary groups and authorized_keys.
func (u *UserExecutor) updateUser(ctx context.Context, name string, params map[string]string, entry *passwdEntry, dryRun bool, result ResourceResult) ResourceResult {
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

	if comment, ok := params["comment"]; ok && comment != "" && comment != entry.gecos {
		needsUpdate = true
		modArgs = append(modArgs, "-c", comment)
	}

	if group, ok := params["group"]; ok && group != "" {
		modArgs = append(modArgs, "-g", group)
		needsUpdate = true
	}

	// Check supplementary groups (additive).
	var groupsToAdd []string
	if groups, ok := params["groups"]; ok && groups != "" {
		desired := parseGroups(groups)
		current, err := u.currentGroups(ctx, name)
		if err != nil {
			result.Error = err
			return result
		}
		groupsToAdd = missingGroups(current, desired)
		if len(groupsToAdd) > 0 {
			needsUpdate = true
		}
	}

	// Check password hash against /etc/shadow.
	passwordChanged := false
	var desiredPassword string
	if pw, ok := params["password"]; ok && pw != "" {
		desiredPassword = pw
		currentHash, err := u.lookupShadowHash(name)
		if err != nil {
			result.Error = err
			return result
		}
		if currentHash != pw {
			passwordChanged = true
			needsUpdate = true
		}
	}

	// Check authorized_keys.
	keysChanged := false
	var desiredKeys string
	if keys, ok := params["authorized_keys"]; ok && keys != "" {
		desiredKeys = keys
		home := resolveHome(name, params, entry)
		akPath := filepath.Join(home, ".ssh", "authorized_keys")
		existing, err := os.ReadFile(akPath)
		if err != nil || string(existing) != desiredKeys {
			keysChanged = true
			needsUpdate = true
		}
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

	// Apply core attribute changes.
	if len(modArgs) > 0 {
		modArgs = append(modArgs, name)
		out, err := u.runner().Run(ctx, "usermod", modArgs...)
		if err != nil {
			result.Error = fmt.Errorf("usermod %s: %w: %s", name, err, string(out))
			return result
		}
	}

	// Apply supplementary groups.
	if len(groupsToAdd) > 0 {
		gArgs := []string{"-aG", strings.Join(groupsToAdd, ","), name}
		out, err := u.runner().Run(ctx, "usermod", gArgs...)
		if err != nil {
			result.Error = fmt.Errorf("usermod -aG %s: %w: %s", name, err, string(out))
			return result
		}
	}

	// Apply password.
	if passwordChanged {
		out, err := u.runner().Run(ctx, "usermod", "-p", desiredPassword, name)
		if err != nil {
			result.Error = fmt.Errorf("usermod -p %s: %w: %s", name, err, string(out))
			return result
		}
	}

	// Apply authorized_keys.
	if keysChanged {
		home := resolveHome(name, params, entry)
		if err := u.writeAuthorizedKeys(ctx, name, home, desiredKeys); err != nil {
			result.Error = err
			return result
		}
	}

	return result
}

// writeAuthorizedKeys ensures ~/.ssh exists and writes the authorized_keys file
// with correct ownership and permissions.
func (u *UserExecutor) writeAuthorizedKeys(ctx context.Context, name, home, keys string) error {
	sshDir := filepath.Join(home, ".ssh")

	if err := os.MkdirAll(sshDir, 0700); err != nil {
		return fmt.Errorf("mkdir %s: %w", sshDir, err)
	}

	akPath := filepath.Join(sshDir, "authorized_keys")
	if err := os.WriteFile(akPath, []byte(keys), 0600); err != nil {
		return fmt.Errorf("write %s: %w", akPath, err)
	}

	// chown the .ssh dir and authorized_keys to the user.
	out, err := u.runner().Run(ctx, "chown", "-R", name+":", sshDir)
	if err != nil {
		return fmt.Errorf("chown %s: %w: %s", sshDir, err, string(out))
	}

	return nil
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
	if comment, ok := params["comment"]; ok && comment != "" {
		args = append(args, "-c", comment)
	}
	if group, ok := params["group"]; ok && group != "" {
		args = append(args, "-g", group)
	}
	if password, ok := params["password"]; ok && password != "" {
		args = append(args, "-p", password)
	}

	args = append(args, "-m", name) // -m creates home dir
	return args
}
