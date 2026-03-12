// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: BUSL-1.1

package pkgmgr

import (
	"context"
	"fmt"
	"strings"
	"testing"
)

// mockRunner records command invocations and returns canned output.
type mockRunner struct {
	calls  []string
	output []byte
	err    error
}

func (m *mockRunner) Run(ctx context.Context, name string, args ...string) ([]byte, error) {
	m.calls = append(m.calls, name+" "+strings.Join(args, " "))
	return m.output, m.err
}

// --- Detect tests ---

func TestDetectUbuntu(t *testing.T) {
	mgr, err := Detect("linux", "ubuntu")
	if err != nil {
		t.Fatalf("Detect(linux, ubuntu): %v", err)
	}
	if _, ok := mgr.(*AptManager); !ok {
		t.Errorf("expected *AptManager, got %T", mgr)
	}
}

func TestDetectDebian(t *testing.T) {
	mgr, err := Detect("linux", "debian")
	if err != nil {
		t.Fatalf("Detect(linux, debian): %v", err)
	}
	if _, ok := mgr.(*AptManager); !ok {
		t.Errorf("expected *AptManager, got %T", mgr)
	}
}

func TestDetectMint(t *testing.T) {
	mgr, err := Detect("linux", "mint")
	if err != nil {
		t.Fatalf("Detect(linux, mint): %v", err)
	}
	if _, ok := mgr.(*AptManager); !ok {
		t.Errorf("expected *AptManager, got %T", mgr)
	}
}

func TestDetectFedora(t *testing.T) {
	mgr, err := Detect("linux", "fedora")
	if err != nil {
		t.Fatalf("Detect(linux, fedora): %v", err)
	}
	if _, ok := mgr.(*DnfManager); !ok {
		t.Errorf("expected *DnfManager, got %T", mgr)
	}
}

func TestDetectRHEL(t *testing.T) {
	mgr, err := Detect("linux", "rhel")
	if err != nil {
		t.Fatalf("Detect(linux, rhel): %v", err)
	}
	if _, ok := mgr.(*DnfManager); !ok {
		t.Errorf("expected *DnfManager, got %T", mgr)
	}
}

func TestDetectRocky(t *testing.T) {
	mgr, err := Detect("linux", "rocky")
	if err != nil {
		t.Fatalf("Detect(linux, rocky): %v", err)
	}
	if _, ok := mgr.(*DnfManager); !ok {
		t.Errorf("expected *DnfManager, got %T", mgr)
	}
}

func TestDetectAlma(t *testing.T) {
	mgr, err := Detect("linux", "alma")
	if err != nil {
		t.Fatalf("Detect(linux, alma): %v", err)
	}
	if _, ok := mgr.(*DnfManager); !ok {
		t.Errorf("expected *DnfManager, got %T", mgr)
	}
}

func TestDetectCentOS(t *testing.T) {
	mgr, err := Detect("linux", "centos")
	if err != nil {
		t.Fatalf("Detect(linux, centos): %v", err)
	}
	if _, ok := mgr.(*YumManager); !ok {
		t.Errorf("expected *YumManager, got %T", mgr)
	}
}

func TestDetectAlpine(t *testing.T) {
	mgr, err := Detect("linux", "alpine")
	if err != nil {
		t.Fatalf("Detect(linux, alpine): %v", err)
	}
	if _, ok := mgr.(*ApkManager); !ok {
		t.Errorf("expected *ApkManager, got %T", mgr)
	}
}

func TestDetectArch(t *testing.T) {
	mgr, err := Detect("linux", "arch")
	if err != nil {
		t.Fatalf("Detect(linux, arch): %v", err)
	}
	if _, ok := mgr.(*PacmanManager); !ok {
		t.Errorf("expected *PacmanManager, got %T", mgr)
	}
}

func TestDetectManjaro(t *testing.T) {
	mgr, err := Detect("linux", "manjaro")
	if err != nil {
		t.Fatalf("Detect(linux, manjaro): %v", err)
	}
	if _, ok := mgr.(*PacmanManager); !ok {
		t.Errorf("expected *PacmanManager, got %T", mgr)
	}
}

func TestDetectDarwin(t *testing.T) {
	mgr, err := Detect("darwin", "")
	if err != nil {
		t.Fatalf("Detect(darwin, ''): %v", err)
	}
	if _, ok := mgr.(*BrewManager); !ok {
		t.Errorf("expected *BrewManager, got %T", mgr)
	}
}

func TestDetectOpenSUSE(t *testing.T) {
	mgr, err := Detect("linux", "opensuse")
	if err != nil {
		t.Fatalf("Detect(linux, opensuse): %v", err)
	}
	if _, ok := mgr.(*ZypperManager); !ok {
		t.Errorf("expected *ZypperManager, got %T", mgr)
	}
}

func TestDetectSLES(t *testing.T) {
	mgr, err := Detect("linux", "sles")
	if err != nil {
		t.Fatalf("Detect(linux, sles): %v", err)
	}
	if _, ok := mgr.(*ZypperManager); !ok {
		t.Errorf("expected *ZypperManager, got %T", mgr)
	}
}

func TestDetectUnknown(t *testing.T) {
	_, err := Detect("linux", "unknown")
	if err == nil {
		t.Fatal("expected error for unknown distro, got nil")
	}
}

// --- Backend operation tests ---
// Each backend is tested for IsInstalled, Install, Remove, Upgrade
// using mock CommandRunner to verify correct CLI invocations.

func TestAptIsInstalled(t *testing.T) {
	m := &mockRunner{output: []byte("Status: install ok installed\nVersion: 1.18.0-6ubuntu14.6\n")}
	mgr := &AptManager{Runner: m}
	installed, version, err := mgr.IsInstalled(context.Background(), "nginx")
	if err != nil {
		t.Fatal(err)
	}
	if !installed {
		t.Error("expected installed=true")
	}
	if version != "1.18.0-6ubuntu14.6" {
		t.Errorf("expected version=1.18.0-6ubuntu14.6, got %s", version)
	}
	if len(m.calls) != 1 || !strings.Contains(m.calls[0], "dpkg -s nginx") {
		t.Errorf("expected dpkg -s nginx, got %v", m.calls)
	}
}

func TestAptIsInstalledBrokenStates(t *testing.T) {
	broken := []struct {
		name   string
		status string
	}{
		{"deinstall config-files", "Status: deinstall ok config-files\nVersion: 12.1.1\n"},
		{"half-installed", "Status: install reinstreq half-installed\nVersion: 12.1.1\n"},
		{"half-configured", "Status: install ok half-configured\nVersion: 12.1.1\n"},
		{"unpacked", "Status: install ok unpacked\nVersion: 12.1.1\n"},
		{"triggers-awaited", "Status: install ok triggers-awaited\nVersion: 12.1.1\n"},
		{"triggers-pending", "Status: install ok triggers-pending\nVersion: 12.1.1\n"},
	}
	for _, tc := range broken {
		t.Run(tc.name, func(t *testing.T) {
			m := &mockRunner{output: []byte(tc.status)}
			mgr := &AptManager{Runner: m}
			installed, _, err := mgr.IsInstalled(context.Background(), "pkg")
			if err != nil {
				t.Fatal(err)
			}
			if installed {
				t.Errorf("expected installed=false for %s state", tc.name)
			}
		})
	}
}

func TestAptIsInstalledNotFound(t *testing.T) {
	m := &mockRunner{output: []byte("dpkg-query: package 'nginx' is not installed\n"), err: fmt.Errorf("exit status 1")}
	mgr := &AptManager{Runner: m}
	installed, _, err := mgr.IsInstalled(context.Background(), "nginx")
	if err != nil {
		t.Fatal(err)
	}
	if installed {
		t.Error("expected installed=false")
	}
}

func TestAptInstall(t *testing.T) {
	m := &mockRunner{}
	mgr := &AptManager{Runner: m}
	err := mgr.Install(context.Background(), "nginx", "")
	if err != nil {
		t.Fatal(err)
	}
	// Install now calls dpkg -s first to check for broken state, then apt-get install.
	foundInstall := false
	for _, c := range m.calls {
		if strings.Contains(c, "apt-get install -y nginx") {
			foundInstall = true
		}
	}
	if !foundInstall {
		t.Errorf("expected apt-get install -y nginx, got %v", m.calls)
	}
}

func TestAptInstallWithVersion(t *testing.T) {
	m := &mockRunner{}
	mgr := &AptManager{Runner: m}
	err := mgr.Install(context.Background(), "nginx", "1.18.0")
	if err != nil {
		t.Fatal(err)
	}
	foundInstall := false
	for _, c := range m.calls {
		if strings.Contains(c, "nginx=1.18.0") {
			foundInstall = true
		}
	}
	if !foundInstall {
		t.Errorf("expected nginx=1.18.0, got %v", m.calls)
	}
}

func TestAptRemove(t *testing.T) {
	m := &mockRunner{}
	mgr := &AptManager{Runner: m}
	err := mgr.Remove(context.Background(), "nginx")
	if err != nil {
		t.Fatal(err)
	}
	if len(m.calls) != 1 || !strings.Contains(m.calls[0], "apt-get remove -y nginx") {
		t.Errorf("expected apt-get remove -y nginx, got %v", m.calls)
	}
}

func TestDnfInstall(t *testing.T) {
	m := &mockRunner{}
	mgr := &DnfManager{Runner: m}
	err := mgr.Install(context.Background(), "nginx", "")
	if err != nil {
		t.Fatal(err)
	}
	if len(m.calls) != 1 || !strings.Contains(m.calls[0], "dnf install -y nginx") {
		t.Errorf("expected dnf install -y nginx, got %v", m.calls)
	}
}

func TestDnfIsInstalled(t *testing.T) {
	m := &mockRunner{output: []byte("nginx-1.20.1-1.el8.x86_64\n")}
	mgr := &DnfManager{Runner: m}
	installed, version, err := mgr.IsInstalled(context.Background(), "nginx")
	if err != nil {
		t.Fatal(err)
	}
	if !installed {
		t.Error("expected installed=true")
	}
	if version == "" {
		t.Error("expected non-empty version")
	}
	if len(m.calls) != 1 || !strings.Contains(m.calls[0], "rpm -q nginx") {
		t.Errorf("expected rpm -q nginx, got %v", m.calls)
	}
}

func TestYumInstall(t *testing.T) {
	m := &mockRunner{}
	mgr := &YumManager{Runner: m}
	err := mgr.Install(context.Background(), "nginx", "")
	if err != nil {
		t.Fatal(err)
	}
	if len(m.calls) != 1 || !strings.Contains(m.calls[0], "yum install -y nginx") {
		t.Errorf("expected yum install -y nginx, got %v", m.calls)
	}
}

func TestYumIsInstalled(t *testing.T) {
	m := &mockRunner{output: []byte("nginx-1.20.1-1.el7.x86_64\n")}
	mgr := &YumManager{Runner: m}
	installed, _, err := mgr.IsInstalled(context.Background(), "nginx")
	if err != nil {
		t.Fatal(err)
	}
	if !installed {
		t.Error("expected installed=true")
	}
	if len(m.calls) != 1 || !strings.Contains(m.calls[0], "rpm -q nginx") {
		t.Errorf("expected rpm -q nginx, got %v", m.calls)
	}
}

func TestApkInstall(t *testing.T) {
	m := &mockRunner{}
	mgr := &ApkManager{Runner: m}
	err := mgr.Install(context.Background(), "nginx", "")
	if err != nil {
		t.Fatal(err)
	}
	if len(m.calls) != 1 || !strings.Contains(m.calls[0], "apk add nginx") {
		t.Errorf("expected apk add nginx, got %v", m.calls)
	}
}

func TestApkIsInstalled(t *testing.T) {
	m := &mockRunner{output: []byte("nginx-1.24.0-r6\n")}
	mgr := &ApkManager{Runner: m}
	installed, _, err := mgr.IsInstalled(context.Background(), "nginx")
	if err != nil {
		t.Fatal(err)
	}
	if !installed {
		t.Error("expected installed=true")
	}
	if len(m.calls) != 1 || !strings.Contains(m.calls[0], "apk info -e nginx") {
		t.Errorf("expected apk info -e nginx, got %v", m.calls)
	}
}

func TestPacmanInstall(t *testing.T) {
	m := &mockRunner{}
	mgr := &PacmanManager{Runner: m}
	err := mgr.Install(context.Background(), "nginx", "")
	if err != nil {
		t.Fatal(err)
	}
	if len(m.calls) != 1 || !strings.Contains(m.calls[0], "pacman -S --noconfirm nginx") {
		t.Errorf("expected pacman -S --noconfirm nginx, got %v", m.calls)
	}
}

func TestPacmanIsInstalled(t *testing.T) {
	m := &mockRunner{output: []byte("nginx 1.24.0-2\n")}
	mgr := &PacmanManager{Runner: m}
	installed, _, err := mgr.IsInstalled(context.Background(), "nginx")
	if err != nil {
		t.Fatal(err)
	}
	if !installed {
		t.Error("expected installed=true")
	}
	if len(m.calls) != 1 || !strings.Contains(m.calls[0], "pacman -Q nginx") {
		t.Errorf("expected pacman -Q nginx, got %v", m.calls)
	}
}

func TestBrewInstall(t *testing.T) {
	m := &mockRunner{}
	mgr := &BrewManager{Runner: m}
	err := mgr.Install(context.Background(), "nginx", "")
	if err != nil {
		t.Fatal(err)
	}
	if len(m.calls) != 1 || !strings.Contains(m.calls[0], "brew install nginx") {
		t.Errorf("expected brew install nginx, got %v", m.calls)
	}
}

func TestBrewIsInstalled(t *testing.T) {
	m := &mockRunner{output: []byte("nginx\n")}
	mgr := &BrewManager{Runner: m}
	installed, _, err := mgr.IsInstalled(context.Background(), "nginx")
	if err != nil {
		t.Fatal(err)
	}
	if !installed {
		t.Error("expected installed=true")
	}
	if len(m.calls) != 1 || !strings.Contains(m.calls[0], "brew list nginx") {
		t.Errorf("expected brew list nginx, got %v", m.calls)
	}
}

func TestZypperInstall(t *testing.T) {
	m := &mockRunner{}
	mgr := &ZypperManager{Runner: m}
	err := mgr.Install(context.Background(), "nginx", "")
	if err != nil {
		t.Fatal(err)
	}
	if len(m.calls) != 1 || !strings.Contains(m.calls[0], "zypper install -y nginx") {
		t.Errorf("expected zypper install -y nginx, got %v", m.calls)
	}
}

func TestZypperIsInstalled(t *testing.T) {
	m := &mockRunner{output: []byte("i | nginx | package | 1.21.5\n")}
	mgr := &ZypperManager{Runner: m}
	installed, _, err := mgr.IsInstalled(context.Background(), "nginx")
	if err != nil {
		t.Fatal(err)
	}
	if !installed {
		t.Error("expected installed=true")
	}
	if len(m.calls) != 1 || !strings.Contains(m.calls[0], "zypper se -i nginx") {
		t.Errorf("expected zypper se -i nginx, got %v", m.calls)
	}
}

// --- Remove tests for remaining backends ---

func TestDnfRemove(t *testing.T) {
	m := &mockRunner{}
	mgr := &DnfManager{Runner: m}
	err := mgr.Remove(context.Background(), "nginx")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(m.calls[0], "dnf remove -y nginx") {
		t.Errorf("expected dnf remove -y nginx, got %v", m.calls)
	}
}

func TestYumRemove(t *testing.T) {
	m := &mockRunner{}
	mgr := &YumManager{Runner: m}
	err := mgr.Remove(context.Background(), "nginx")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(m.calls[0], "yum remove -y nginx") {
		t.Errorf("expected yum remove -y nginx, got %v", m.calls)
	}
}

func TestApkRemove(t *testing.T) {
	m := &mockRunner{}
	mgr := &ApkManager{Runner: m}
	err := mgr.Remove(context.Background(), "nginx")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(m.calls[0], "apk del nginx") {
		t.Errorf("expected apk del nginx, got %v", m.calls)
	}
}

func TestPacmanRemove(t *testing.T) {
	m := &mockRunner{}
	mgr := &PacmanManager{Runner: m}
	err := mgr.Remove(context.Background(), "nginx")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(m.calls[0], "pacman -R --noconfirm nginx") {
		t.Errorf("expected pacman -R --noconfirm nginx, got %v", m.calls)
	}
}

func TestBrewRemove(t *testing.T) {
	m := &mockRunner{}
	mgr := &BrewManager{Runner: m}
	err := mgr.Remove(context.Background(), "nginx")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(m.calls[0], "brew uninstall nginx") {
		t.Errorf("expected brew uninstall nginx, got %v", m.calls)
	}
}

func TestZypperRemove(t *testing.T) {
	m := &mockRunner{}
	mgr := &ZypperManager{Runner: m}
	err := mgr.Remove(context.Background(), "nginx")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(m.calls[0], "zypper remove -y nginx") {
		t.Errorf("expected zypper remove -y nginx, got %v", m.calls)
	}
}

// --- Apt broken package repair tests ---

// mockSeqRunner returns different results for sequential calls.
type mockSeqRunner struct {
	calls   []string
	results []mockSeqResult
	idx     int
}

type mockSeqResult struct {
	output []byte
	err    error
}

func (m *mockSeqRunner) Run(_ context.Context, name string, args ...string) ([]byte, error) {
	m.calls = append(m.calls, name+" "+strings.Join(args, " "))
	if m.idx < len(m.results) {
		r := m.results[m.idx]
		m.idx++
		return r.output, r.err
	}
	return nil, nil
}

func TestAptInstallRepairsBrokenPackage(t *testing.T) {
	// Sequence: dpkg -s (broken) → dpkg --configure (succeeds) → dpkg -s (now installed)
	m := &mockSeqRunner{results: []mockSeqResult{
		{output: []byte("Status: install ok half-configured\nVersion: 2.31.2\n")},  // dpkg -s: broken
		{},                                                                           // dpkg --configure: succeeds
		{output: []byte("Status: install ok installed\nVersion: 2.31.2\n")},         // dpkg -s: repaired
	}}
	mgr := &AptManager{Runner: m}
	err := mgr.Install(context.Background(), "prometheus", "")
	if err != nil {
		t.Fatalf("Install() = %v, want nil", err)
	}
	// Should have called dpkg -s, dpkg --configure, dpkg -s — no apt-get needed
	if len(m.calls) != 3 {
		t.Fatalf("expected 3 calls, got %d: %v", len(m.calls), m.calls)
	}
	if !strings.Contains(m.calls[0], "dpkg -s prometheus") {
		t.Errorf("call[0] = %q, want dpkg -s", m.calls[0])
	}
	if !strings.Contains(m.calls[1], "dpkg --configure prometheus") {
		t.Errorf("call[1] = %q, want dpkg --configure", m.calls[1])
	}
	if !strings.Contains(m.calls[2], "dpkg -s prometheus") {
		t.Errorf("call[2] = %q, want dpkg -s", m.calls[2])
	}
}

func TestAptInstallRepairFailsFallsBackToInstall(t *testing.T) {
	// Sequence: dpkg -s (broken) → dpkg --configure (fails) → dpkg -s (still broken) → apt-get install
	m := &mockSeqRunner{results: []mockSeqResult{
		{output: []byte("Status: install ok half-configured\nVersion: 2.31.2\n")},  // dpkg -s: broken
		{err: fmt.Errorf("exit status 1")},                                          // dpkg --configure: fails
		{output: []byte("Status: install ok half-configured\nVersion: 2.31.2\n")},  // dpkg -s: still broken
		{},                                                                           // apt-get install: succeeds
	}}
	mgr := &AptManager{Runner: m}
	err := mgr.Install(context.Background(), "prometheus", "")
	if err != nil {
		t.Fatalf("Install() = %v, want nil", err)
	}
	if len(m.calls) != 4 {
		t.Fatalf("expected 4 calls, got %d: %v", len(m.calls), m.calls)
	}
	if !strings.Contains(m.calls[3], "apt-get install") {
		t.Errorf("call[3] = %q, want apt-get install", m.calls[3])
	}
}

func TestAptInstallSkipsRepairForCleanInstall(t *testing.T) {
	// Package not known to dpkg at all — straight to apt-get install
	m := &mockSeqRunner{results: []mockSeqResult{
		{err: fmt.Errorf("exit status 1")}, // dpkg -s: not found
		{},                                  // apt-get install
	}}
	mgr := &AptManager{Runner: m}
	err := mgr.Install(context.Background(), "nginx", "")
	if err != nil {
		t.Fatalf("Install() = %v, want nil", err)
	}
	if len(m.calls) != 2 {
		t.Fatalf("expected 2 calls, got %d: %v", len(m.calls), m.calls)
	}
	if !strings.Contains(m.calls[1], "apt-get install -y nginx") {
		t.Errorf("call[1] = %q, want apt-get install", m.calls[1])
	}
}

// --- Interface compliance checks ---

var (
	_ PackageManager = (*AptManager)(nil)
	_ PackageManager = (*DnfManager)(nil)
	_ PackageManager = (*YumManager)(nil)
	_ PackageManager = (*ApkManager)(nil)
	_ PackageManager = (*PacmanManager)(nil)
	_ PackageManager = (*BrewManager)(nil)
	_ PackageManager = (*ZypperManager)(nil)
)
