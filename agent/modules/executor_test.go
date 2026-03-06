package modules

import (
	"context"
	"testing"
)

func TestActionTakenConstants(t *testing.T) {
	tests := []struct {
		name     string
		action   ActionTaken
		expected string
	}{
		{"none", ActionNone, "none"},
		{"created", ActionCreated, "created"},
		{"updated", ActionUpdated, "updated"},
		{"deleted", ActionDeleted, "deleted"},
		{"skipped", ActionSkipped, "skipped"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if string(tt.action) != tt.expected {
				t.Errorf("ActionTaken %s = %q, want %q", tt.name, tt.action, tt.expected)
			}
		})
	}
}

func TestResourceResultFields(t *testing.T) {
	r := ResourceResult{
		ModuleName:   "nginx-config",
		ResourceType: "file",
		ResourceName: "/etc/nginx/nginx.conf",
		Action:       ActionCreated,
		Changed:      true,
		Error:        nil,
		DurationMs:   42,
	}

	if r.ModuleName != "nginx-config" {
		t.Errorf("ModuleName = %q, want %q", r.ModuleName, "nginx-config")
	}
	if r.ResourceType != "file" {
		t.Errorf("ResourceType = %q, want %q", r.ResourceType, "file")
	}
	if r.ResourceName != "/etc/nginx/nginx.conf" {
		t.Errorf("ResourceName = %q, want %q", r.ResourceName, "/etc/nginx/nginx.conf")
	}
	if r.Action != ActionCreated {
		t.Errorf("Action = %q, want %q", r.Action, ActionCreated)
	}
	if !r.Changed {
		t.Error("Changed = false, want true")
	}
	if r.Error != nil {
		t.Errorf("Error = %v, want nil", r.Error)
	}
	if r.DurationMs != 42 {
		t.Errorf("DurationMs = %d, want 42", r.DurationMs)
	}
}

// Compile-time check that FileExecutor implements Executor.
var _ Executor = (*FileExecutor)(nil)

func TestFileExecutorIdempotent_NoChange(t *testing.T) {
	dir := t.TempDir()
	path := dir + "/test.txt"
	content := "hello world\n"

	// Create the file first
	writeFile(t, path, content, 0644)

	fe := &FileExecutor{}
	result := fe.Execute(context.Background(), map[string]string{
		"path":    path,
		"content": content,
		"mode":    "0644",
		"state":   "present",
	}, false)

	if result.Action != ActionNone {
		t.Errorf("Action = %q, want %q (file content matches)", result.Action, ActionNone)
	}
	if result.Changed {
		t.Error("Changed = true, want false (no change needed)")
	}
	if result.Error != nil {
		t.Errorf("Error = %v, want nil", result.Error)
	}
}

func TestFileExecutorCreatesNewFile(t *testing.T) {
	dir := t.TempDir()
	path := dir + "/new.txt"
	content := "new file content\n"

	fe := &FileExecutor{}
	result := fe.Execute(context.Background(), map[string]string{
		"path":    path,
		"content": content,
		"mode":    "0644",
		"state":   "present",
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

	// Verify file was written
	got := readFile(t, path)
	if got != content {
		t.Errorf("file content = %q, want %q", got, content)
	}
}

func TestFileExecutorUpdatesExistingFile(t *testing.T) {
	dir := t.TempDir()
	path := dir + "/existing.txt"

	writeFile(t, path, "old content\n", 0644)

	newContent := "updated content\n"
	fe := &FileExecutor{}
	result := fe.Execute(context.Background(), map[string]string{
		"path":    path,
		"content": newContent,
		"mode":    "0644",
		"state":   "present",
	}, false)

	if result.Action != ActionUpdated {
		t.Errorf("Action = %q, want %q", result.Action, ActionUpdated)
	}
	if !result.Changed {
		t.Error("Changed = false, want true")
	}

	got := readFile(t, path)
	if got != newContent {
		t.Errorf("file content = %q, want %q", got, newContent)
	}
}

func TestFileExecutorDryRun(t *testing.T) {
	dir := t.TempDir()
	path := dir + "/dryrun.txt"
	content := "should not be written\n"

	fe := &FileExecutor{}
	result := fe.Execute(context.Background(), map[string]string{
		"path":    path,
		"content": content,
		"mode":    "0644",
		"state":   "present",
	}, true)

	if result.Action != ActionCreated {
		t.Errorf("Action = %q, want %q (dry-run reports planned action)", result.Action, ActionCreated)
	}
	if !result.Changed {
		t.Error("Changed = false, want true (dry-run reports planned change)")
	}

	// File should NOT exist after dry-run
	if fileExists(path) {
		t.Error("file was created during dry-run, should not have been")
	}
}

func TestFileExecutorDeletesFile(t *testing.T) {
	dir := t.TempDir()
	path := dir + "/todelete.txt"

	writeFile(t, path, "to be deleted\n", 0644)

	fe := &FileExecutor{}
	result := fe.Execute(context.Background(), map[string]string{
		"path":  path,
		"state": "absent",
	}, false)

	if result.Action != ActionDeleted {
		t.Errorf("Action = %q, want %q", result.Action, ActionDeleted)
	}
	if !result.Changed {
		t.Error("Changed = false, want true")
	}
	if fileExists(path) {
		t.Error("file still exists after state=absent")
	}
}

func TestFileExecutorDeleteNonExistent(t *testing.T) {
	dir := t.TempDir()
	path := dir + "/noexist.txt"

	fe := &FileExecutor{}
	result := fe.Execute(context.Background(), map[string]string{
		"path":  path,
		"state": "absent",
	}, false)

	if result.Action != ActionNone {
		t.Errorf("Action = %q, want %q (already absent)", result.Action, ActionNone)
	}
	if result.Changed {
		t.Error("Changed = true, want false (nothing to delete)")
	}
}

func TestFileExecutorModeChange(t *testing.T) {
	dir := t.TempDir()
	path := dir + "/modefile.txt"
	content := "same content\n"

	writeFile(t, path, content, 0600)

	fe := &FileExecutor{}
	result := fe.Execute(context.Background(), map[string]string{
		"path":    path,
		"content": content,
		"mode":    "0644",
		"state":   "present",
	}, false)

	if result.Action != ActionUpdated {
		t.Errorf("Action = %q, want %q (mode changed)", result.Action, ActionUpdated)
	}
	if !result.Changed {
		t.Error("Changed = false, want true (mode changed)")
	}
}

func TestFileExecutorDefaultMode(t *testing.T) {
	dir := t.TempDir()
	path := dir + "/defaultmode.txt"
	content := "default mode file\n"

	fe := &FileExecutor{}
	result := fe.Execute(context.Background(), map[string]string{
		"path":    path,
		"content": content,
		"state":   "present",
	}, false)

	if result.Error != nil {
		t.Fatalf("Error = %v, want nil", result.Error)
	}

	// Default mode should be 0644
	info := fileInfo(t, path)
	if info.Mode().Perm() != 0644 {
		t.Errorf("file mode = %o, want 0644", info.Mode().Perm())
	}
}

func TestFileExecutorCreatesParentDir(t *testing.T) {
	dir := t.TempDir()
	path := dir + "/sub/dir/test.txt"
	content := "nested file\n"

	fe := &FileExecutor{}
	result := fe.Execute(context.Background(), map[string]string{
		"path":    path,
		"content": content,
		"mode":    "0644",
		"state":   "present",
	}, false)

	if result.Error != nil {
		t.Fatalf("Error = %v, want nil", result.Error)
	}
	if result.Action != ActionCreated {
		t.Errorf("Action = %q, want %q", result.Action, ActionCreated)
	}

	got := readFile(t, path)
	if got != content {
		t.Errorf("file content = %q, want %q", got, content)
	}
}

func TestFileExecutorSnapshotIntegration(t *testing.T) {
	dir := t.TempDir()
	snapshotDir := t.TempDir()
	path := dir + "/snapped.txt"
	oldContent := "original content\n"
	newContent := "new content\n"

	writeFile(t, path, oldContent, 0644)

	mgr := newTestSnapshotManager(snapshotDir)
	fe := &FileExecutor{
		SnapshotMgr: mgr,
		RunID:       "test-run-001",
	}

	result := fe.Execute(context.Background(), map[string]string{
		"path":    path,
		"content": newContent,
		"mode":    "0644",
		"state":   "present",
	}, false)

	if result.Error != nil {
		t.Fatalf("Error = %v, want nil", result.Error)
	}
	if result.Action != ActionUpdated {
		t.Errorf("Action = %q, want %q", result.Action, ActionUpdated)
	}

	// Verify snapshot was saved
	snaps := listSnapshots(t, snapshotDir, path)
	if len(snaps) == 0 {
		t.Error("no snapshot was saved before overwrite")
	}
}

func TestFileExecutorResourceType(t *testing.T) {
	dir := t.TempDir()
	path := dir + "/typed.txt"

	fe := &FileExecutor{}
	result := fe.Execute(context.Background(), map[string]string{
		"path":    path,
		"content": "test\n",
		"state":   "present",
	}, false)

	if result.ResourceType != "file" {
		t.Errorf("ResourceType = %q, want %q", result.ResourceType, "file")
	}
}
