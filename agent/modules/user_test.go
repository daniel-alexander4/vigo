package modules

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Compile-time interface check
var _ Executor = (*UserExecutor)(nil)

func TestUserExecutor_CreateMissing(t *testing.T) {
	dir := t.TempDir()
	passwdFile := filepath.Join(dir, "passwd")
	os.WriteFile(passwdFile, []byte("root:x:0:0:root:/root:/bin/bash\n"), 0644)

	runner := newMockCmdRunnerGeneric()
	ue := &UserExecutor{Runner: runner, PasswdPath: passwdFile}
	result := ue.Execute(context.Background(), map[string]string{
		"name":  "www-data",
		"uid":   "33",
		"shell": "/usr/sbin/nologin",
		"home":  "/var/www",
		"state": "present",
	}, false)

	if result.Action != ActionCreated {
		t.Errorf("Action = %q, want %q", result.Action, ActionCreated)
	}
	if !result.Changed {
		t.Error("Changed = false, want true")
	}
	if result.Error != nil {
		t.Errorf("Error = %v, want nil", result.Error)
	}
	found := false
	for _, c := range runner.calls {
		if strings.Contains(c, "useradd") && strings.Contains(c, "www-data") {
			found = true
		}
	}
	if !found {
		t.Errorf("expected useradd call, got %v", runner.calls)
	}
}

func TestUserExecutor_AlreadyExists(t *testing.T) {
	dir := t.TempDir()
	passwdFile := filepath.Join(dir, "passwd")
	os.WriteFile(passwdFile, []byte("www-data:x:33:33:www-data:/var/www:/usr/sbin/nologin\n"), 0644)

	runner := newMockCmdRunnerGeneric()
	ue := &UserExecutor{Runner: runner, PasswdPath: passwdFile}
	result := ue.Execute(context.Background(), map[string]string{
		"name":  "www-data",
		"uid":   "33",
		"shell": "/usr/sbin/nologin",
		"home":  "/var/www",
		"state": "present",
	}, false)

	if result.Action != ActionNone {
		t.Errorf("Action = %q, want %q", result.Action, ActionNone)
	}
	if result.Changed {
		t.Error("Changed = true, want false")
	}
}

func TestUserExecutor_UpdateAttributes(t *testing.T) {
	dir := t.TempDir()
	passwdFile := filepath.Join(dir, "passwd")
	os.WriteFile(passwdFile, []byte("www-data:x:33:33:www-data:/var/www:/bin/bash\n"), 0644)

	runner := newMockCmdRunnerGeneric()
	ue := &UserExecutor{Runner: runner, PasswdPath: passwdFile}
	result := ue.Execute(context.Background(), map[string]string{
		"name":  "www-data",
		"uid":   "33",
		"shell": "/usr/sbin/nologin",
		"home":  "/var/www",
		"state": "present",
	}, false)

	if result.Action != ActionUpdated {
		t.Errorf("Action = %q, want %q", result.Action, ActionUpdated)
	}
	if !result.Changed {
		t.Error("Changed = false, want true")
	}
	found := false
	for _, c := range runner.calls {
		if strings.Contains(c, "usermod") && strings.Contains(c, "www-data") {
			found = true
		}
	}
	if !found {
		t.Errorf("expected usermod call, got %v", runner.calls)
	}
}

func TestUserExecutor_RemoveExisting(t *testing.T) {
	dir := t.TempDir()
	passwdFile := filepath.Join(dir, "passwd")
	os.WriteFile(passwdFile, []byte("www-data:x:33:33:www-data:/var/www:/usr/sbin/nologin\n"), 0644)

	runner := newMockCmdRunnerGeneric()
	ue := &UserExecutor{Runner: runner, PasswdPath: passwdFile}
	result := ue.Execute(context.Background(), map[string]string{
		"name":  "www-data",
		"state": "absent",
	}, false)

	if result.Action != ActionDeleted {
		t.Errorf("Action = %q, want %q", result.Action, ActionDeleted)
	}
	if !result.Changed {
		t.Error("Changed = false, want true")
	}
	found := false
	for _, c := range runner.calls {
		if strings.Contains(c, "userdel") && strings.Contains(c, "www-data") {
			found = true
		}
	}
	if !found {
		t.Errorf("expected userdel call, got %v", runner.calls)
	}
}

func TestUserExecutor_RemoveNonexistent(t *testing.T) {
	dir := t.TempDir()
	passwdFile := filepath.Join(dir, "passwd")
	os.WriteFile(passwdFile, []byte("root:x:0:0:root:/root:/bin/bash\n"), 0644)

	runner := newMockCmdRunnerGeneric()
	ue := &UserExecutor{Runner: runner, PasswdPath: passwdFile}
	result := ue.Execute(context.Background(), map[string]string{
		"name":  "www-data",
		"state": "absent",
	}, false)

	if result.Action != ActionNone {
		t.Errorf("Action = %q, want %q", result.Action, ActionNone)
	}
	if result.Changed {
		t.Error("Changed = true, want false")
	}
}

func TestUserExecutor_DryRun(t *testing.T) {
	dir := t.TempDir()
	passwdFile := filepath.Join(dir, "passwd")
	os.WriteFile(passwdFile, []byte("root:x:0:0:root:/root:/bin/bash\n"), 0644)

	runner := newMockCmdRunnerGeneric()
	ue := &UserExecutor{Runner: runner, PasswdPath: passwdFile}
	result := ue.Execute(context.Background(), map[string]string{
		"name":  "www-data",
		"uid":   "33",
		"shell": "/usr/sbin/nologin",
		"home":  "/var/www",
		"state": "present",
	}, true)

	if result.Action != ActionCreated {
		t.Errorf("Action = %q, want %q", result.Action, ActionCreated)
	}
	if !result.Changed {
		t.Error("Changed = false, want true")
	}
	for _, c := range runner.calls {
		if strings.Contains(c, "useradd") {
			t.Errorf("dry-run should not run useradd, got: %s", c)
		}
	}
}

func TestUserExecutor_CreateWithGroup(t *testing.T) {
	dir := t.TempDir()
	passwdFile := filepath.Join(dir, "passwd")
	os.WriteFile(passwdFile, []byte("root:x:0:0:root:/root:/bin/bash\n"), 0644)

	runner := newMockCmdRunnerGeneric()
	ue := &UserExecutor{Runner: runner, PasswdPath: passwdFile}
	result := ue.Execute(context.Background(), map[string]string{
		"name":  "deploy",
		"group": "www-data",
		"state": "present",
	}, false)

	if result.Action != ActionCreated {
		t.Errorf("Action = %q, want %q", result.Action, ActionCreated)
	}
	foundGroup := false
	for _, c := range runner.calls {
		if strings.Contains(c, "-g") && strings.Contains(c, "www-data") {
			foundGroup = true
		}
	}
	if !foundGroup {
		t.Errorf("expected -g www-data in useradd call, got %v", runner.calls)
	}
}

func TestUserExecutor_ResourceType(t *testing.T) {
	dir := t.TempDir()
	passwdFile := filepath.Join(dir, "passwd")
	os.WriteFile(passwdFile, []byte("root:x:0:0:root:/root:/bin/bash\n"), 0644)

	runner := newMockCmdRunnerGeneric()
	ue := &UserExecutor{Runner: runner, PasswdPath: passwdFile}
	result := ue.Execute(context.Background(), map[string]string{
		"name":  "testuser",
		"state": "present",
	}, true)

	if result.ResourceType != "user" {
		t.Errorf("ResourceType = %q, want %q", result.ResourceType, "user")
	}
}

func TestUserExecutor_MissingName(t *testing.T) {
	runner := newMockCmdRunnerGeneric()
	ue := &UserExecutor{Runner: runner, PasswdPath: "/dev/null"}
	result := ue.Execute(context.Background(), map[string]string{
		"state": "present",
	}, false)

	if result.Error == nil {
		t.Error("expected error for missing name, got nil")
	}
}

func TestUserExecutor_UseradError(t *testing.T) {
	dir := t.TempDir()
	passwdFile := filepath.Join(dir, "passwd")
	os.WriteFile(passwdFile, []byte("root:x:0:0:root:/root:/bin/bash\n"), 0644)

	runner := newMockCmdRunnerGeneric()
	runner.results["useradd"] = mockCmdResult{err: fmt.Errorf("exit status 1")}
	ue := &UserExecutor{Runner: runner, PasswdPath: passwdFile}
	result := ue.Execute(context.Background(), map[string]string{
		"name":  "testuser",
		"state": "present",
	}, false)

	if result.Error == nil {
		t.Error("expected error from useradd failure, got nil")
	}
}

// --- Supplementary groups tests ---

func TestUserExecutor_CreateWithGroups(t *testing.T) {
	dir := t.TempDir()
	passwdFile := filepath.Join(dir, "passwd")
	os.WriteFile(passwdFile, []byte("root:x:0:0:root:/root:/bin/bash\n"), 0644)

	runner := newMockCmdRunnerGeneric()
	ue := &UserExecutor{Runner: runner, PasswdPath: passwdFile}
	result := ue.Execute(context.Background(), map[string]string{
		"name":   "dan",
		"groups": "sudo,docker",
		"state":  "present",
	}, false)

	if result.Action != ActionCreated {
		t.Errorf("Action = %q, want %q", result.Action, ActionCreated)
	}
	if result.Error != nil {
		t.Errorf("Error = %v, want nil", result.Error)
	}

	// Should call useradd then usermod -aG
	foundUseradd := false
	foundGroups := false
	for _, c := range runner.calls {
		if strings.Contains(c, "useradd") && strings.Contains(c, "dan") {
			foundUseradd = true
		}
		if strings.Contains(c, "usermod") && strings.Contains(c, "-aG") && strings.Contains(c, "docker") {
			foundGroups = true
		}
	}
	if !foundUseradd {
		t.Errorf("expected useradd call, got %v", runner.calls)
	}
	if !foundGroups {
		t.Errorf("expected usermod -aG call, got %v", runner.calls)
	}
}

func TestUserExecutor_UpdateAddsGroups(t *testing.T) {
	dir := t.TempDir()
	passwdFile := filepath.Join(dir, "passwd")
	os.WriteFile(passwdFile, []byte("dan:x:1000:1000:dan:/home/dan:/bin/bash\n"), 0644)

	runner := newMockCmdRunnerGeneric()
	// id -nG returns current groups (only "dan")
	runner.results["id -nG dan"] = mockCmdResult{output: []byte("dan\n")}
	ue := &UserExecutor{Runner: runner, PasswdPath: passwdFile}
	result := ue.Execute(context.Background(), map[string]string{
		"name":   "dan",
		"shell":  "/bin/bash",
		"groups": "sudo,docker",
		"state":  "present",
	}, false)

	if result.Action != ActionUpdated {
		t.Errorf("Action = %q, want %q", result.Action, ActionUpdated)
	}
	if !result.Changed {
		t.Error("Changed = false, want true")
	}

	foundGroups := false
	for _, c := range runner.calls {
		if strings.Contains(c, "usermod") && strings.Contains(c, "-aG") {
			foundGroups = true
		}
	}
	if !foundGroups {
		t.Errorf("expected usermod -aG call, got %v", runner.calls)
	}
}

func TestUserExecutor_GroupsAlreadyPresent(t *testing.T) {
	dir := t.TempDir()
	passwdFile := filepath.Join(dir, "passwd")
	os.WriteFile(passwdFile, []byte("dan:x:1000:1000:dan:/home/dan:/bin/bash\n"), 0644)

	runner := newMockCmdRunnerGeneric()
	runner.results["id -nG dan"] = mockCmdResult{output: []byte("dan sudo docker\n")}
	ue := &UserExecutor{Runner: runner, PasswdPath: passwdFile}
	result := ue.Execute(context.Background(), map[string]string{
		"name":   "dan",
		"shell":  "/bin/bash",
		"groups": "sudo,docker",
		"state":  "present",
	}, false)

	if result.Action != ActionNone {
		t.Errorf("Action = %q, want %q (groups already present)", result.Action, ActionNone)
	}
	if result.Changed {
		t.Error("Changed = true, want false")
	}
}

// --- Authorized keys tests ---

func TestUserExecutor_CreateWithAuthorizedKeys(t *testing.T) {
	dir := t.TempDir()
	passwdFile := filepath.Join(dir, "passwd")
	homeDir := filepath.Join(dir, "home", "dan")
	os.WriteFile(passwdFile, []byte("root:x:0:0:root:/root:/bin/bash\n"), 0644)

	runner := newMockCmdRunnerGeneric()
	// After useradd, lookupUser will find the user in passwd.
	// We simulate this by writing the entry before the test since
	// useradd is mocked and won't actually modify passwd.
	// Instead, write it upfront and test createUser path directly
	// by using a passwd file without the user (create path).
	ue := &UserExecutor{Runner: runner, PasswdPath: passwdFile}

	keys := "ssh-ed25519 AAAA... dan@workstation\n"
	result := ue.Execute(context.Background(), map[string]string{
		"name":            "dan",
		"home":            homeDir,
		"authorized_keys": keys,
		"state":           "present",
	}, false)

	if result.Action != ActionCreated {
		t.Errorf("Action = %q, want %q", result.Action, ActionCreated)
	}
	if result.Error != nil {
		t.Errorf("Error = %v, want nil", result.Error)
	}

	// Verify authorized_keys was written
	akPath := filepath.Join(homeDir, ".ssh", "authorized_keys")
	got, err := os.ReadFile(akPath)
	if err != nil {
		t.Fatalf("authorized_keys not written: %v", err)
	}
	if string(got) != keys {
		t.Errorf("authorized_keys = %q, want %q", string(got), keys)
	}

	// Verify .ssh dir permissions
	info, err := os.Stat(filepath.Join(homeDir, ".ssh"))
	if err != nil {
		t.Fatalf("stat .ssh: %v", err)
	}
	if info.Mode().Perm() != 0700 {
		t.Errorf(".ssh mode = %o, want 0700", info.Mode().Perm())
	}

	// Verify chown was called
	foundChown := false
	for _, c := range runner.calls {
		if strings.Contains(c, "chown") && strings.Contains(c, "dan:") {
			foundChown = true
		}
	}
	if !foundChown {
		t.Errorf("expected chown call, got %v", runner.calls)
	}
}

func TestUserExecutor_UpdateAuthorizedKeys(t *testing.T) {
	dir := t.TempDir()
	passwdFile := filepath.Join(dir, "passwd")
	homeDir := filepath.Join(dir, "home", "dan")
	os.WriteFile(passwdFile, []byte("dan:x:1000:1000:dan:"+homeDir+":/bin/bash\n"), 0644)

	// Write existing authorized_keys with old content.
	sshDir := filepath.Join(homeDir, ".ssh")
	os.MkdirAll(sshDir, 0700)
	os.WriteFile(filepath.Join(sshDir, "authorized_keys"), []byte("ssh-rsa OLD...\n"), 0600)

	runner := newMockCmdRunnerGeneric()
	ue := &UserExecutor{Runner: runner, PasswdPath: passwdFile}

	newKeys := "ssh-ed25519 AAAA... dan@workstation\n"
	result := ue.Execute(context.Background(), map[string]string{
		"name":            "dan",
		"shell":           "/bin/bash",
		"authorized_keys": newKeys,
		"state":           "present",
	}, false)

	if result.Action != ActionUpdated {
		t.Errorf("Action = %q, want %q", result.Action, ActionUpdated)
	}
	if !result.Changed {
		t.Error("Changed = false, want true")
	}

	got, err := os.ReadFile(filepath.Join(sshDir, "authorized_keys"))
	if err != nil {
		t.Fatalf("read authorized_keys: %v", err)
	}
	if string(got) != newKeys {
		t.Errorf("authorized_keys = %q, want %q", string(got), newKeys)
	}
}

func TestUserExecutor_AuthorizedKeysAlreadyCorrect(t *testing.T) {
	dir := t.TempDir()
	passwdFile := filepath.Join(dir, "passwd")
	homeDir := filepath.Join(dir, "home", "dan")
	os.WriteFile(passwdFile, []byte("dan:x:1000:1000:dan:"+homeDir+":/bin/bash\n"), 0644)

	keys := "ssh-ed25519 AAAA... dan@workstation\n"
	sshDir := filepath.Join(homeDir, ".ssh")
	os.MkdirAll(sshDir, 0700)
	os.WriteFile(filepath.Join(sshDir, "authorized_keys"), []byte(keys), 0600)

	runner := newMockCmdRunnerGeneric()
	ue := &UserExecutor{Runner: runner, PasswdPath: passwdFile}
	result := ue.Execute(context.Background(), map[string]string{
		"name":            "dan",
		"shell":           "/bin/bash",
		"authorized_keys": keys,
		"state":           "present",
	}, false)

	if result.Action != ActionNone {
		t.Errorf("Action = %q, want %q (keys already match)", result.Action, ActionNone)
	}
	if result.Changed {
		t.Error("Changed = true, want false")
	}
}

func TestUserExecutor_DryRunSkipsGroupsAndKeys(t *testing.T) {
	dir := t.TempDir()
	passwdFile := filepath.Join(dir, "passwd")
	os.WriteFile(passwdFile, []byte("root:x:0:0:root:/root:/bin/bash\n"), 0644)

	runner := newMockCmdRunnerGeneric()
	ue := &UserExecutor{Runner: runner, PasswdPath: passwdFile}
	result := ue.Execute(context.Background(), map[string]string{
		"name":            "dan",
		"groups":          "sudo,docker",
		"authorized_keys": "ssh-ed25519 AAAA...\n",
		"state":           "present",
	}, true)

	if result.Action != ActionCreated {
		t.Errorf("Action = %q, want %q", result.Action, ActionCreated)
	}
	// No commands should have been run in dry-run.
	if len(runner.calls) > 0 {
		t.Errorf("dry-run should not run commands, got %v", runner.calls)
	}
}

func TestUserExecutor_UseradWithHomeFlag(t *testing.T) {
	dir := t.TempDir()
	passwdFile := filepath.Join(dir, "passwd")
	os.WriteFile(passwdFile, []byte("root:x:0:0:root:/root:/bin/bash\n"), 0644)

	runner := newMockCmdRunnerGeneric()
	ue := &UserExecutor{Runner: runner, PasswdPath: passwdFile}
	ue.Execute(context.Background(), map[string]string{
		"name":  "deploy",
		"state": "present",
	}, false)

	// useradd should include -m flag to create home dir
	found := false
	for _, c := range runner.calls {
		if strings.Contains(c, "useradd") && strings.Contains(c, "-m") {
			found = true
		}
	}
	if !found {
		t.Errorf("expected useradd with -m flag, got %v", runner.calls)
	}
}

// --- Comment/GECOS tests ---

func TestUserExecutor_CreateWithComment(t *testing.T) {
	dir := t.TempDir()
	passwdFile := filepath.Join(dir, "passwd")
	os.WriteFile(passwdFile, []byte("root:x:0:0:root:/root:/bin/bash\n"), 0644)

	runner := newMockCmdRunnerGeneric()
	ue := &UserExecutor{Runner: runner, PasswdPath: passwdFile}
	result := ue.Execute(context.Background(), map[string]string{
		"name":    "deploy",
		"comment": "Deploy User",
		"state":   "present",
	}, false)

	if result.Action != ActionCreated {
		t.Errorf("Action = %q, want %q", result.Action, ActionCreated)
	}
	if result.Error != nil {
		t.Errorf("Error = %v, want nil", result.Error)
	}
	found := false
	for _, c := range runner.calls {
		if strings.Contains(c, "useradd") && strings.Contains(c, "-c") && strings.Contains(c, "Deploy User") {
			found = true
		}
	}
	if !found {
		t.Errorf("expected useradd with -c flag, got %v", runner.calls)
	}
}

func TestUserExecutor_UpdateComment(t *testing.T) {
	dir := t.TempDir()
	passwdFile := filepath.Join(dir, "passwd")
	os.WriteFile(passwdFile, []byte("deploy:x:1001:1001:Old Name:/home/deploy:/bin/bash\n"), 0644)

	runner := newMockCmdRunnerGeneric()
	ue := &UserExecutor{Runner: runner, PasswdPath: passwdFile}
	result := ue.Execute(context.Background(), map[string]string{
		"name":    "deploy",
		"shell":   "/bin/bash",
		"comment": "New Name",
		"state":   "present",
	}, false)

	if result.Action != ActionUpdated {
		t.Errorf("Action = %q, want %q", result.Action, ActionUpdated)
	}
	if !result.Changed {
		t.Error("Changed = false, want true")
	}
	found := false
	for _, c := range runner.calls {
		if strings.Contains(c, "usermod") && strings.Contains(c, "-c") && strings.Contains(c, "New Name") {
			found = true
		}
	}
	if !found {
		t.Errorf("expected usermod -c call, got %v", runner.calls)
	}
}

func TestUserExecutor_CommentAlreadyCorrect(t *testing.T) {
	dir := t.TempDir()
	passwdFile := filepath.Join(dir, "passwd")
	os.WriteFile(passwdFile, []byte("deploy:x:1001:1001:Deploy User:/home/deploy:/bin/bash\n"), 0644)

	runner := newMockCmdRunnerGeneric()
	ue := &UserExecutor{Runner: runner, PasswdPath: passwdFile}
	result := ue.Execute(context.Background(), map[string]string{
		"name":    "deploy",
		"shell":   "/bin/bash",
		"comment": "Deploy User",
		"state":   "present",
	}, false)

	if result.Action != ActionNone {
		t.Errorf("Action = %q, want %q (comment matches)", result.Action, ActionNone)
	}
	if result.Changed {
		t.Error("Changed = true, want false")
	}
}

// --- Password tests ---

func TestUserExecutor_CreateWithPassword(t *testing.T) {
	dir := t.TempDir()
	passwdFile := filepath.Join(dir, "passwd")
	os.WriteFile(passwdFile, []byte("root:x:0:0:root:/root:/bin/bash\n"), 0644)

	runner := newMockCmdRunnerGeneric()
	ue := &UserExecutor{Runner: runner, PasswdPath: passwdFile}
	hash := "$6$rounds=5000$salt$hashedvalue"
	result := ue.Execute(context.Background(), map[string]string{
		"name":     "deploy",
		"password": hash,
		"state":    "present",
	}, false)

	if result.Action != ActionCreated {
		t.Errorf("Action = %q, want %q", result.Action, ActionCreated)
	}
	if result.Error != nil {
		t.Errorf("Error = %v, want nil", result.Error)
	}
	found := false
	for _, c := range runner.calls {
		if strings.Contains(c, "useradd") && strings.Contains(c, "-p") && strings.Contains(c, hash) {
			found = true
		}
	}
	if !found {
		t.Errorf("expected useradd with -p flag, got %v", runner.calls)
	}
}

func TestUserExecutor_UpdatePassword(t *testing.T) {
	dir := t.TempDir()
	passwdFile := filepath.Join(dir, "passwd")
	shadowFile := filepath.Join(dir, "shadow")
	os.WriteFile(passwdFile, []byte("deploy:x:1001:1001:deploy:/home/deploy:/bin/bash\n"), 0644)
	os.WriteFile(shadowFile, []byte("deploy:$6$old$oldhash:19000:0:99999:7:::\n"), 0600)

	runner := newMockCmdRunnerGeneric()
	ue := &UserExecutor{Runner: runner, PasswdPath: passwdFile, ShadowPath: shadowFile}
	newHash := "$6$new$newhash"
	result := ue.Execute(context.Background(), map[string]string{
		"name":     "deploy",
		"shell":    "/bin/bash",
		"password": newHash,
		"state":    "present",
	}, false)

	if result.Action != ActionUpdated {
		t.Errorf("Action = %q, want %q", result.Action, ActionUpdated)
	}
	if !result.Changed {
		t.Error("Changed = false, want true")
	}
	found := false
	for _, c := range runner.calls {
		if strings.Contains(c, "usermod") && strings.Contains(c, "-p") && strings.Contains(c, newHash) {
			found = true
		}
	}
	if !found {
		t.Errorf("expected usermod -p call, got %v", runner.calls)
	}
}

func TestUserExecutor_PasswordAlreadyCorrect(t *testing.T) {
	dir := t.TempDir()
	passwdFile := filepath.Join(dir, "passwd")
	shadowFile := filepath.Join(dir, "shadow")
	hash := "$6$rounds=5000$salt$hashedvalue"
	os.WriteFile(passwdFile, []byte("deploy:x:1001:1001:deploy:/home/deploy:/bin/bash\n"), 0644)
	os.WriteFile(shadowFile, []byte("deploy:"+hash+":19000:0:99999:7:::\n"), 0600)

	runner := newMockCmdRunnerGeneric()
	ue := &UserExecutor{Runner: runner, PasswdPath: passwdFile, ShadowPath: shadowFile}
	result := ue.Execute(context.Background(), map[string]string{
		"name":     "deploy",
		"shell":    "/bin/bash",
		"password": hash,
		"state":    "present",
	}, false)

	if result.Action != ActionNone {
		t.Errorf("Action = %q, want %q (password matches)", result.Action, ActionNone)
	}
	if result.Changed {
		t.Error("Changed = true, want false")
	}
}

// --- Helper tests ---

func TestParseGroups(t *testing.T) {
	tests := []struct {
		input    string
		expected []string
	}{
		{"sudo,docker", []string{"docker", "sudo"}},
		{"sudo, docker , lxd", []string{"docker", "lxd", "sudo"}},
		{"single", []string{"single"}},
		{"", nil},
	}
	for _, tt := range tests {
		got := parseGroups(tt.input)
		if len(got) != len(tt.expected) {
			t.Errorf("parseGroups(%q) = %v, want %v", tt.input, got, tt.expected)
			continue
		}
		for i := range got {
			if got[i] != tt.expected[i] {
				t.Errorf("parseGroups(%q)[%d] = %q, want %q", tt.input, i, got[i], tt.expected[i])
			}
		}
	}
}

func TestMissingGroups(t *testing.T) {
	tests := []struct {
		current  []string
		desired  []string
		expected []string
	}{
		{[]string{"dan"}, []string{"sudo", "docker"}, []string{"sudo", "docker"}},
		{[]string{"dan", "sudo", "docker"}, []string{"sudo", "docker"}, nil},
		{[]string{"dan", "sudo"}, []string{"sudo", "docker"}, []string{"docker"}},
	}
	for _, tt := range tests {
		got := missingGroups(tt.current, tt.desired)
		if len(got) != len(tt.expected) {
			t.Errorf("missingGroups(%v, %v) = %v, want %v", tt.current, tt.desired, got, tt.expected)
		}
	}
}
