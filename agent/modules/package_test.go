package modules

import (
	"context"
	"fmt"
	"testing"

	"github.com/hobnob/hobnob/agent/modules/pkgmgr"
)

// Compile-time interface check
var _ Executor = (*PackageExecutor)(nil)

// mockPackageManager simulates a package manager for testing.
type mockPackageManager struct {
	installed   bool
	version     string
	queryErr    error
	installErr  error
	removeErr   error
	upgradeErr  error
	installCall string
	removeCall  string
	upgradeCall string
}

func (m *mockPackageManager) IsInstalled(_ context.Context, name string) (bool, string, error) {
	return m.installed, m.version, m.queryErr
}

func (m *mockPackageManager) Install(_ context.Context, name, version string) error {
	m.installCall = name
	if version != "" {
		m.installCall = name + "=" + version
	}
	return m.installErr
}

func (m *mockPackageManager) Remove(_ context.Context, name string) error {
	m.removeCall = name
	return m.removeErr
}

func (m *mockPackageManager) Upgrade(_ context.Context, name, version string) error {
	m.upgradeCall = name
	if version != "" {
		m.upgradeCall = name + "=" + version
	}
	return m.upgradeErr
}

// Ensure mockPackageManager implements pkgmgr.PackageManager
var _ pkgmgr.PackageManager = (*mockPackageManager)(nil)

func TestPackageExecutor_InstallMissing(t *testing.T) {
	mock := &mockPackageManager{installed: false}
	pe := &PackageExecutor{PkgMgr: mock}
	result := pe.Execute(context.Background(), map[string]string{
		"name":  "nginx",
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
	if mock.installCall != "nginx" {
		t.Errorf("Install not called or wrong package: %q", mock.installCall)
	}
}

func TestPackageExecutor_AlreadyInstalled(t *testing.T) {
	mock := &mockPackageManager{installed: true, version: "1.18.0"}
	pe := &PackageExecutor{PkgMgr: mock}
	result := pe.Execute(context.Background(), map[string]string{
		"name":  "nginx",
		"state": "present",
	}, false)

	if result.Action != ActionNone {
		t.Errorf("Action = %q, want %q", result.Action, ActionNone)
	}
	if result.Changed {
		t.Error("Changed = true, want false")
	}
}

func TestPackageExecutor_RemoveInstalled(t *testing.T) {
	mock := &mockPackageManager{installed: true, version: "1.18.0"}
	pe := &PackageExecutor{PkgMgr: mock}
	result := pe.Execute(context.Background(), map[string]string{
		"name":  "nginx",
		"state": "absent",
	}, false)

	if result.Action != ActionDeleted {
		t.Errorf("Action = %q, want %q", result.Action, ActionDeleted)
	}
	if !result.Changed {
		t.Error("Changed = false, want true")
	}
	if mock.removeCall != "nginx" {
		t.Errorf("Remove not called or wrong package: %q", mock.removeCall)
	}
}

func TestPackageExecutor_RemoveNotInstalled(t *testing.T) {
	mock := &mockPackageManager{installed: false}
	pe := &PackageExecutor{PkgMgr: mock}
	result := pe.Execute(context.Background(), map[string]string{
		"name":  "nginx",
		"state": "absent",
	}, false)

	if result.Action != ActionNone {
		t.Errorf("Action = %q, want %q", result.Action, ActionNone)
	}
	if result.Changed {
		t.Error("Changed = true, want false")
	}
}

func TestPackageExecutor_VersionPin(t *testing.T) {
	mock := &mockPackageManager{installed: false}
	pe := &PackageExecutor{PkgMgr: mock}
	result := pe.Execute(context.Background(), map[string]string{
		"name":    "nginx",
		"state":   "present",
		"version": "1.18.0",
	}, false)

	if result.Action != ActionCreated {
		t.Errorf("Action = %q, want %q", result.Action, ActionCreated)
	}
	if mock.installCall != "nginx=1.18.0" {
		t.Errorf("Install called with %q, want %q", mock.installCall, "nginx=1.18.0")
	}
}

func TestPackageExecutor_VersionUpgrade(t *testing.T) {
	mock := &mockPackageManager{installed: true, version: "1.16.0"}
	pe := &PackageExecutor{PkgMgr: mock}
	result := pe.Execute(context.Background(), map[string]string{
		"name":    "nginx",
		"state":   "present",
		"version": "1.18.0",
	}, false)

	if result.Action != ActionUpdated {
		t.Errorf("Action = %q, want %q", result.Action, ActionUpdated)
	}
	if !result.Changed {
		t.Error("Changed = false, want true")
	}
	if mock.upgradeCall != "nginx=1.18.0" {
		t.Errorf("Upgrade called with %q, want %q", mock.upgradeCall, "nginx=1.18.0")
	}
}

func TestPackageExecutor_DryRun(t *testing.T) {
	mock := &mockPackageManager{installed: false}
	pe := &PackageExecutor{PkgMgr: mock}
	result := pe.Execute(context.Background(), map[string]string{
		"name":  "nginx",
		"state": "present",
	}, true)

	if result.Action != ActionCreated {
		t.Errorf("Action = %q, want %q", result.Action, ActionCreated)
	}
	if !result.Changed {
		t.Error("Changed = false, want true (dry-run reports planned change)")
	}
	// Should NOT have called Install
	if mock.installCall != "" {
		t.Errorf("Install was called during dry-run: %q", mock.installCall)
	}
}

func TestPackageExecutor_InstallError(t *testing.T) {
	mock := &mockPackageManager{installed: false, installErr: fmt.Errorf("apt-get failed")}
	pe := &PackageExecutor{PkgMgr: mock}
	result := pe.Execute(context.Background(), map[string]string{
		"name":  "nginx",
		"state": "present",
	}, false)

	if result.Error == nil {
		t.Error("expected error, got nil")
	}
}

func TestPackageExecutor_ResourceType(t *testing.T) {
	mock := &mockPackageManager{installed: true, version: "1.0"}
	pe := &PackageExecutor{PkgMgr: mock}
	result := pe.Execute(context.Background(), map[string]string{
		"name":  "nginx",
		"state": "present",
	}, false)

	if result.ResourceType != "package" {
		t.Errorf("ResourceType = %q, want %q", result.ResourceType, "package")
	}
	if result.ResourceName != "nginx" {
		t.Errorf("ResourceName = %q, want %q", result.ResourceName, "nginx")
	}
}

func TestPackageExecutor_MissingName(t *testing.T) {
	mock := &mockPackageManager{}
	pe := &PackageExecutor{PkgMgr: mock}
	result := pe.Execute(context.Background(), map[string]string{
		"state": "present",
	}, false)

	if result.Error == nil {
		t.Error("expected error for missing name, got nil")
	}
}

// multiMockPkgMgr supports per-package installed state for multi-package tests.
type multiMockPkgMgr struct {
	packages     map[string]string // name -> version ("" = not installed)
	installCalls []string
	removeCalls  []string
	installErr   error
}

func (m *multiMockPkgMgr) IsInstalled(_ context.Context, name string) (bool, string, error) {
	v, ok := m.packages[name]
	if !ok || v == "" {
		return false, "", nil
	}
	return true, v, nil
}

func (m *multiMockPkgMgr) Install(_ context.Context, name, version string) error {
	m.installCalls = append(m.installCalls, name)
	return m.installErr
}

func (m *multiMockPkgMgr) Remove(_ context.Context, name string) error {
	m.removeCalls = append(m.removeCalls, name)
	return nil
}

func (m *multiMockPkgMgr) Upgrade(_ context.Context, name, version string) error {
	return nil
}

var _ pkgmgr.PackageManager = (*multiMockPkgMgr)(nil)

func TestPackageExecutor_MultiplePackages(t *testing.T) {
	mock := &multiMockPkgMgr{
		packages: map[string]string{
			"curl": "7.68.0", // already installed
		},
	}
	pe := &PackageExecutor{PkgMgr: mock}
	result := pe.Execute(context.Background(), map[string]string{
		"name":  "nginx, curl, htop",
		"state": "present",
	}, false)

	if result.Error != nil {
		t.Fatalf("unexpected error: %v", result.Error)
	}
	if !result.Changed {
		t.Error("Changed = false, want true (nginx and htop need installing)")
	}
	if result.Action != ActionCreated {
		t.Errorf("Action = %q, want %q", result.Action, ActionCreated)
	}
	// curl is already installed, so only nginx and htop should be installed
	if len(mock.installCalls) != 2 {
		t.Fatalf("expected 2 install calls, got %d: %v", len(mock.installCalls), mock.installCalls)
	}
}

func TestPackageExecutor_MultipleAllInstalled(t *testing.T) {
	mock := &multiMockPkgMgr{
		packages: map[string]string{
			"nginx": "1.18.0",
			"curl":  "7.68.0",
		},
	}
	pe := &PackageExecutor{PkgMgr: mock}
	result := pe.Execute(context.Background(), map[string]string{
		"name":  "nginx, curl",
		"state": "present",
	}, false)

	if result.Error != nil {
		t.Fatalf("unexpected error: %v", result.Error)
	}
	if result.Changed {
		t.Error("Changed = true, want false (all already installed)")
	}
	if result.Action != ActionNone {
		t.Errorf("Action = %q, want %q", result.Action, ActionNone)
	}
}

func TestPackageExecutor_MultipleStopsOnError(t *testing.T) {
	mock := &multiMockPkgMgr{
		packages:   map[string]string{},
		installErr: fmt.Errorf("apt-get failed"),
	}
	pe := &PackageExecutor{PkgMgr: mock}
	result := pe.Execute(context.Background(), map[string]string{
		"name":  "nginx, curl, htop",
		"state": "present",
	}, false)

	if result.Error == nil {
		t.Fatal("expected error, got nil")
	}
	// Should stop after first failure
	if len(mock.installCalls) != 1 {
		t.Errorf("expected 1 install call before failure, got %d", len(mock.installCalls))
	}
}

func TestPackageExecutor_MultipleRemove(t *testing.T) {
	mock := &multiMockPkgMgr{
		packages: map[string]string{
			"nginx": "1.18.0",
			"curl":  "7.68.0",
		},
	}
	pe := &PackageExecutor{PkgMgr: mock}
	result := pe.Execute(context.Background(), map[string]string{
		"name":  "nginx, curl",
		"state": "absent",
	}, false)

	if result.Error != nil {
		t.Fatalf("unexpected error: %v", result.Error)
	}
	if !result.Changed {
		t.Error("Changed = false, want true")
	}
	if len(mock.removeCalls) != 2 {
		t.Fatalf("expected 2 remove calls, got %d", len(mock.removeCalls))
	}
}
