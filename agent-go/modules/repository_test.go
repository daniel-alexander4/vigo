// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: BUSL-1.1

package modules

import (
	"context"
	"fmt"
	"testing"
)

// Compile-time check that RepositoryExecutor implements Executor.
var _ Executor = (*RepositoryExecutor)(nil)

// mockRepoManager is a test double for pkgmgr.RepoManager.
type mockRepoManager struct {
	repos          map[string]bool
	addCalls       []repoAddCall
	removeCalls    []string
	refreshCalled  int
	addErr         error
	removeErr      error
	refreshErr     error
	isRepoErr      error
}

type repoAddCall struct {
	name, keyURL, repoLine string
}

func newMockRepoManager() *mockRepoManager {
	return &mockRepoManager{repos: make(map[string]bool)}
}

func (m *mockRepoManager) IsRepoPresent(_ context.Context, name string) (bool, error) {
	if m.isRepoErr != nil {
		return false, m.isRepoErr
	}
	return m.repos[name], nil
}

func (m *mockRepoManager) AddRepo(_ context.Context, name, keyURL, repoLine string) error {
	m.addCalls = append(m.addCalls, repoAddCall{name, keyURL, repoLine})
	if m.addErr != nil {
		return m.addErr
	}
	m.repos[name] = true
	return nil
}

func (m *mockRepoManager) RemoveRepo(_ context.Context, name string) error {
	m.removeCalls = append(m.removeCalls, name)
	if m.removeErr != nil {
		return m.removeErr
	}
	delete(m.repos, name)
	return nil
}

func (m *mockRepoManager) RefreshIndex(_ context.Context) error {
	m.refreshCalled++
	return m.refreshErr
}

func TestRepositoryExecutor_AddNew(t *testing.T) {
	rm := newMockRepoManager()
	exe := &RepositoryExecutor{RepoMgr: rm}

	result := exe.Execute(context.Background(), map[string]string{
		"name":    "grafana",
		"key_url": "https://apt.grafana.com/gpg.key",
		"repo":    "deb [signed-by=/etc/apt/keyrings/grafana.gpg] https://apt.grafana.com stable main",
		"state":   "present",
	}, false)

	if result.Error != nil {
		t.Fatalf("Error = %v, want nil", result.Error)
	}
	if result.Action != ActionCreated {
		t.Errorf("Action = %q, want %q", result.Action, ActionCreated)
	}
	if !result.Changed {
		t.Error("Changed = false, want true")
	}
	if result.ResourceType != "repository" {
		t.Errorf("ResourceType = %q, want %q", result.ResourceType, "repository")
	}
	if len(rm.addCalls) != 1 {
		t.Fatalf("AddRepo called %d times, want 1", len(rm.addCalls))
	}
	if rm.addCalls[0].keyURL != "https://apt.grafana.com/gpg.key" {
		t.Errorf("keyURL = %q, want grafana key URL", rm.addCalls[0].keyURL)
	}
	if rm.refreshCalled != 1 {
		t.Errorf("RefreshIndex called %d times, want 1", rm.refreshCalled)
	}
}

func TestRepositoryExecutor_AlreadyPresent(t *testing.T) {
	rm := newMockRepoManager()
	rm.repos["grafana"] = true
	exe := &RepositoryExecutor{RepoMgr: rm}

	result := exe.Execute(context.Background(), map[string]string{
		"name":  "grafana",
		"repo":  "deb https://apt.grafana.com stable main",
		"state": "present",
	}, false)

	if result.Error != nil {
		t.Fatalf("Error = %v, want nil", result.Error)
	}
	if result.Action != ActionNone {
		t.Errorf("Action = %q, want %q", result.Action, ActionNone)
	}
	if result.Changed {
		t.Error("Changed = true, want false")
	}
	if len(rm.addCalls) != 0 {
		t.Errorf("AddRepo called %d times, want 0", len(rm.addCalls))
	}
	if rm.refreshCalled != 0 {
		t.Errorf("RefreshIndex called %d times, want 0", rm.refreshCalled)
	}
}

func TestRepositoryExecutor_Remove(t *testing.T) {
	rm := newMockRepoManager()
	rm.repos["grafana"] = true
	exe := &RepositoryExecutor{RepoMgr: rm}

	result := exe.Execute(context.Background(), map[string]string{
		"name":  "grafana",
		"state": "absent",
	}, false)

	if result.Error != nil {
		t.Fatalf("Error = %v, want nil", result.Error)
	}
	if result.Action != ActionDeleted {
		t.Errorf("Action = %q, want %q", result.Action, ActionDeleted)
	}
	if !result.Changed {
		t.Error("Changed = false, want true")
	}
	if len(rm.removeCalls) != 1 {
		t.Fatalf("RemoveRepo called %d times, want 1", len(rm.removeCalls))
	}
	if rm.refreshCalled != 1 {
		t.Errorf("RefreshIndex called %d times, want 1", rm.refreshCalled)
	}
}

func TestRepositoryExecutor_RemoveNotPresent(t *testing.T) {
	rm := newMockRepoManager()
	exe := &RepositoryExecutor{RepoMgr: rm}

	result := exe.Execute(context.Background(), map[string]string{
		"name":  "grafana",
		"state": "absent",
	}, false)

	if result.Error != nil {
		t.Fatalf("Error = %v, want nil", result.Error)
	}
	if result.Action != ActionNone {
		t.Errorf("Action = %q, want %q", result.Action, ActionNone)
	}
	if result.Changed {
		t.Error("Changed = true, want false")
	}
}

func TestRepositoryExecutor_DryRun(t *testing.T) {
	rm := newMockRepoManager()
	exe := &RepositoryExecutor{RepoMgr: rm}

	result := exe.Execute(context.Background(), map[string]string{
		"name":  "grafana",
		"repo":  "deb https://apt.grafana.com stable main",
		"state": "present",
	}, true)

	if result.Error != nil {
		t.Fatalf("Error = %v, want nil", result.Error)
	}
	if result.Action != ActionCreated {
		t.Errorf("Action = %q, want %q", result.Action, ActionCreated)
	}
	if !result.Changed {
		t.Error("Changed = false, want true")
	}
	// Should NOT have actually added the repo.
	if len(rm.addCalls) != 0 {
		t.Errorf("AddRepo called %d times during dry-run, want 0", len(rm.addCalls))
	}
	if rm.refreshCalled != 0 {
		t.Errorf("RefreshIndex called %d times during dry-run, want 0", rm.refreshCalled)
	}
}

func TestRepositoryExecutor_MissingName(t *testing.T) {
	rm := newMockRepoManager()
	exe := &RepositoryExecutor{RepoMgr: rm}

	result := exe.Execute(context.Background(), map[string]string{
		"repo":  "deb https://example.com stable main",
		"state": "present",
	}, false)

	if result.Error == nil {
		t.Fatal("Error = nil, want error for missing name")
	}
}

func TestRepositoryExecutor_MissingRepo(t *testing.T) {
	rm := newMockRepoManager()
	exe := &RepositoryExecutor{RepoMgr: rm}

	result := exe.Execute(context.Background(), map[string]string{
		"name":  "test",
		"state": "present",
	}, false)

	if result.Error == nil {
		t.Fatal("Error = nil, want error for missing repo")
	}
}

func TestRepositoryExecutor_NilRepoMgr(t *testing.T) {
	exe := &RepositoryExecutor{}

	result := exe.Execute(context.Background(), map[string]string{
		"name":  "test",
		"repo":  "deb https://example.com stable main",
		"state": "present",
	}, false)

	if result.Error == nil {
		t.Fatal("Error = nil, want error for nil repo manager")
	}
}

func TestRepositoryExecutor_AddError(t *testing.T) {
	rm := newMockRepoManager()
	rm.addErr = fmt.Errorf("network error")
	exe := &RepositoryExecutor{RepoMgr: rm}

	result := exe.Execute(context.Background(), map[string]string{
		"name":  "test",
		"repo":  "deb https://example.com stable main",
		"state": "present",
	}, false)

	if result.Error == nil {
		t.Fatal("Error = nil, want error")
	}
}

func TestRepositoryExecutor_RefreshErrorOnAdd(t *testing.T) {
	rm := newMockRepoManager()
	rm.refreshErr = fmt.Errorf("refresh failed")
	exe := &RepositoryExecutor{RepoMgr: rm}

	result := exe.Execute(context.Background(), map[string]string{
		"name":  "test",
		"repo":  "deb https://example.com stable main",
		"state": "present",
	}, false)

	if result.Error == nil {
		t.Fatal("Error = nil, want error for refresh failure")
	}
}

func TestRepositoryExecutor_DefaultState(t *testing.T) {
	rm := newMockRepoManager()
	exe := &RepositoryExecutor{RepoMgr: rm}

	result := exe.Execute(context.Background(), map[string]string{
		"name": "test",
		"repo": "deb https://example.com stable main",
	}, false)

	if result.Error != nil {
		t.Fatalf("Error = %v, want nil", result.Error)
	}
	if result.Action != ActionCreated {
		t.Errorf("Action = %q, want %q (default state=present)", result.Action, ActionCreated)
	}
}

func TestRepositoryExecutor_NoKeyURL(t *testing.T) {
	rm := newMockRepoManager()
	exe := &RepositoryExecutor{RepoMgr: rm}

	result := exe.Execute(context.Background(), map[string]string{
		"name": "test",
		"repo": "deb https://example.com stable main",
	}, false)

	if result.Error != nil {
		t.Fatalf("Error = %v, want nil", result.Error)
	}
	if len(rm.addCalls) != 1 {
		t.Fatalf("AddRepo called %d times, want 1", len(rm.addCalls))
	}
	if rm.addCalls[0].keyURL != "" {
		t.Errorf("keyURL = %q, want empty", rm.addCalls[0].keyURL)
	}
}
