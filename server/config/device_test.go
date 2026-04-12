// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

package config

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestConfigLoadDeviceEntry(t *testing.T) {
	dir := setupDir(t)

	writeFile(t, filepath.Join(dir, "modules", "switch-base.vgo"), `
name: switch-base
resources:
  - name: set-hostname
    type: ssh_exec
    command: "hostname core-sw01"
`)

	writeFile(t, filepath.Join(dir, "envoys", "envoys.vgo"), `
envoys:
  - match: "netmgmt01.example.com"
    modules: []

  - match: "core-sw01.example.com"
    device_type: cisco_ios
    gateway: "netmgmt01.example.com"
    credential: "secret:network/core-sw01/ssh"
    username: admin
    ip_address: "10.0.1.1"
    modules: [switch-base]
`)

	prov := &mockProvider{secrets: map[string]string{
		"network/core-sw01/ssh": "s3cret",
	}}
	store, err := NewConfigStore(dir, prov, true)
	if err != nil {
		t.Fatalf("NewConfigStore: %v", err)
	}

	cfg := store.Get("core-sw01.example.com")
	if cfg.DeviceType != "cisco_ios" {
		t.Errorf("DeviceType = %q, want cisco_ios", cfg.DeviceType)
	}
	if cfg.Gateway != "netmgmt01.example.com" {
		t.Errorf("Gateway = %q, want netmgmt01.example.com", cfg.Gateway)
	}
	// Credential should be resolved (not the secret: reference).
	if cfg.Credential != "s3cret" {
		t.Errorf("Credential = %q, want s3cret", cfg.Credential)
	}
	if cfg.Username != "admin" {
		t.Errorf("Username = %q, want admin", cfg.Username)
	}
	if cfg.IPAddress != "10.0.1.1" {
		t.Errorf("IPAddress = %q, want 10.0.1.1", cfg.IPAddress)
	}
	if len(cfg.Modules) != 1 || cfg.Modules[0].Name != "switch-base" {
		t.Errorf("Modules = %v, want [switch-base]", cfg.Modules)
	}
}

func TestConfigLoadDeviceEntryDefaultPort(t *testing.T) {
	dir := setupDir(t)

	writeFile(t, filepath.Join(dir, "modules", "base.vgo"), `
name: base
resources:
  - name: noop
    type: ssh_exec
    command: "true"
`)

	writeFile(t, filepath.Join(dir, "envoys", "envoys.vgo"), `
envoys:
  - match: "gw.example.com"
    modules: []
  - match: "sw01.example.com"
    device_type: cisco_ios
    gateway: "gw.example.com"
    credential: "secret:net/sw01"
    ip_address: "10.0.1.1"
    modules: [base]
`)

	prov := &mockProvider{secrets: map[string]string{"net/sw01": "pass"}}
	store, err := NewConfigStore(dir, prov, true)
	if err != nil {
		t.Fatalf("NewConfigStore: %v", err)
	}

	devices := store.DevicesForGateway("gw.example.com")
	if len(devices) != 1 {
		t.Fatalf("DevicesForGateway returned %d, want 1", len(devices))
	}
	if devices[0].Port != 22 {
		t.Errorf("Port = %d, want 22 (default)", devices[0].Port)
	}
	if devices[0].Username != "admin" {
		t.Errorf("Username = %q, want admin (default)", devices[0].Username)
	}
}

func TestConfigValidateGatewayExists(t *testing.T) {
	dir := setupDir(t)

	writeFile(t, filepath.Join(dir, "modules", "base.vgo"), `
name: base
resources:
  - name: noop
    type: ssh_exec
    command: "true"
`)

	writeFile(t, filepath.Join(dir, "envoys", "envoys.vgo"), `
envoys:
  - match: "sw01.example.com"
    device_type: cisco_ios
    gateway: "nonexistent.example.com"
    credential: "secret:net/sw01"
    ip_address: "10.0.1.1"
    modules: [base]
`)

	prov := &mockProvider{secrets: map[string]string{"net/sw01": "pass"}}
	_, err := NewConfigStore(dir, prov, true)
	if err == nil {
		t.Fatal("expected error for nonexistent gateway, got nil")
	}
	if !strings.Contains(err.Error(), "does not exist") {
		t.Errorf("error = %v, want 'does not exist'", err)
	}
}

func TestConfigValidateGatewayCycle(t *testing.T) {
	dir := setupDir(t)

	writeFile(t, filepath.Join(dir, "modules", "base.vgo"), `
name: base
resources:
  - name: noop
    type: ssh_exec
    command: "true"
`)

	writeFile(t, filepath.Join(dir, "envoys", "envoys.vgo"), `
envoys:
  - match: "sw01.example.com"
    device_type: cisco_ios
    gateway: "sw02.example.com"
    credential: "secret:net/sw01"
    ip_address: "10.0.1.1"
    modules: [base]
  - match: "sw02.example.com"
    device_type: cisco_nxos
    gateway: "sw01.example.com"
    credential: "secret:net/sw02"
    ip_address: "10.0.1.2"
    modules: [base]
`)

	prov := &mockProvider{secrets: map[string]string{"net/sw01": "pass", "net/sw02": "pass"}}
	_, err := NewConfigStore(dir, prov, true)
	if err == nil {
		t.Fatal("expected error for gateway that is itself a device, got nil")
	}
	if !strings.Contains(err.Error(), "itself a network device") {
		t.Errorf("error = %v, want 'itself a network device'", err)
	}
}

func TestConfigValidateDeviceTypeMissing(t *testing.T) {
	dir := setupDir(t)

	writeFile(t, filepath.Join(dir, "envoys", "envoys.vgo"), `
envoys:
  - match: "gw.example.com"
    modules: []
  - match: "sw01.example.com"
    gateway: "gw.example.com"
    credential: "secret:net/sw01"
    ip_address: "10.0.1.1"
`)

	prov := &mockProvider{secrets: map[string]string{"net/sw01": "pass"}}
	_, err := NewConfigStore(dir, prov, true)
	if err == nil {
		t.Fatal("expected error for gateway without device_type, got nil")
	}
	if !strings.Contains(err.Error(), "no device_type") {
		t.Errorf("error = %v, want 'no device_type'", err)
	}
}

func TestConfigValidateUnsupportedDeviceType(t *testing.T) {
	dir := setupDir(t)

	writeFile(t, filepath.Join(dir, "envoys", "envoys.vgo"), `
envoys:
  - match: "gw.example.com"
    modules: []
  - match: "sw01.example.com"
    device_type: huawei_vrp
    gateway: "gw.example.com"
    credential: "secret:net/sw01"
    ip_address: "10.0.1.1"
`)

	prov := &mockProvider{secrets: map[string]string{"net/sw01": "pass"}}
	_, err := NewConfigStore(dir, prov, true)
	if err == nil {
		t.Fatal("expected error for unsupported device_type, got nil")
	}
	if !strings.Contains(err.Error(), "unsupported device_type") {
		t.Errorf("error = %v, want 'unsupported device_type'", err)
	}
}

func TestConfigValidateCredentialRequired(t *testing.T) {
	dir := setupDir(t)

	writeFile(t, filepath.Join(dir, "envoys", "envoys.vgo"), `
envoys:
  - match: "gw.example.com"
    modules: []
  - match: "sw01.example.com"
    device_type: cisco_ios
    gateway: "gw.example.com"
    ip_address: "10.0.1.1"
`)

	prov := &mockProvider{secrets: map[string]string{}}
	_, err := NewConfigStore(dir, prov, true)
	if err == nil {
		t.Fatal("expected error for missing credential, got nil")
	}
	if !strings.Contains(err.Error(), "requires a credential") {
		t.Errorf("error = %v, want 'requires a credential'", err)
	}
}

func TestConfigValidateCredentialSecretPrefix(t *testing.T) {
	dir := setupDir(t)

	writeFile(t, filepath.Join(dir, "envoys", "envoys.vgo"), `
envoys:
  - match: "gw.example.com"
    modules: []
  - match: "sw01.example.com"
    device_type: cisco_ios
    gateway: "gw.example.com"
    credential: "plaintext-password"
    ip_address: "10.0.1.1"
`)

	prov := &mockProvider{secrets: map[string]string{}}
	_, err := NewConfigStore(dir, prov, true)
	if err == nil {
		t.Fatal("expected error for plaintext credential, got nil")
	}
	if !strings.Contains(err.Error(), `"secret:"`) {
		t.Errorf("error = %v, want secret: prefix error", err)
	}
}

func TestDevicesForGateway(t *testing.T) {
	dir := setupDir(t)

	writeFile(t, filepath.Join(dir, "modules", "switch-base.vgo"), `
name: switch-base
resources:
  - name: set-hostname
    type: ssh_exec
    command: "hostname test"
`)
	writeFile(t, filepath.Join(dir, "modules", "router-base.vgo"), `
name: router-base
resources:
  - name: set-hostname
    type: ssh_exec
    command: "set system host-name test"
`)

	writeFile(t, filepath.Join(dir, "envoys", "envoys.vgo"), `
envoys:
  - match: "netmgmt01.example.com"
    modules: []

  - match: "core-sw01.example.com"
    device_type: cisco_ios
    gateway: "netmgmt01.example.com"
    credential: "secret:net/sw01"
    username: admin
    port: 2222
    ip_address: "10.0.1.1"
    modules: [switch-base]

  - match: "edge-rtr01.example.com"
    device_type: juniper_junos
    gateway: "netmgmt01.example.com"
    credential: "secret:net/rtr01"
    username: netops
    ip_address: "10.0.1.2"
    modules: [router-base]

  - match: "other-sw.example.com"
    device_type: cisco_nxos
    gateway: "other-gw.example.com"
    credential: "secret:net/other"
    ip_address: "10.0.2.1"

  - match: "other-gw.example.com"
    modules: []
`)

	prov := &mockProvider{secrets: map[string]string{
		"net/sw01":  "pass1",
		"net/rtr01": "pass2",
		"net/other": "pass3",
	}}
	store, err := NewConfigStore(dir, prov, true)
	if err != nil {
		t.Fatalf("NewConfigStore: %v", err)
	}

	devices := store.DevicesForGateway("netmgmt01.example.com")
	if len(devices) != 2 {
		t.Fatalf("DevicesForGateway returned %d devices, want 2", len(devices))
	}

	// Check first device.
	sw := devices[0]
	if sw.Hostname != "core-sw01.example.com" {
		t.Errorf("device[0].Hostname = %q, want core-sw01.example.com", sw.Hostname)
	}
	if sw.DeviceType != "cisco_ios" {
		t.Errorf("device[0].DeviceType = %q, want cisco_ios", sw.DeviceType)
	}
	if sw.Port != 2222 {
		t.Errorf("device[0].Port = %d, want 2222", sw.Port)
	}
	if sw.Credential != "pass1" {
		t.Errorf("device[0].Credential = %q, want pass1", sw.Credential)
	}

	// Check second device.
	rtr := devices[1]
	if rtr.Hostname != "edge-rtr01.example.com" {
		t.Errorf("device[1].Hostname = %q, want edge-rtr01.example.com", rtr.Hostname)
	}
	if rtr.DeviceType != "juniper_junos" {
		t.Errorf("device[1].DeviceType = %q, want juniper_junos", rtr.DeviceType)
	}
	if rtr.Username != "netops" {
		t.Errorf("device[1].Username = %q, want netops", rtr.Username)
	}

	// Verify the other gateway's devices are not included.
	otherDevices := store.DevicesForGateway("other-gw.example.com")
	if len(otherDevices) != 1 {
		t.Errorf("DevicesForGateway(other-gw) returned %d, want 1", len(otherDevices))
	}

	// Non-existent gateway returns empty.
	empty := store.DevicesForGateway("nonexistent.example.com")
	if len(empty) != 0 {
		t.Errorf("DevicesForGateway(nonexistent) returned %d, want 0", len(empty))
	}
}

func TestAllDeviceEntries(t *testing.T) {
	dir := setupDir(t)

	writeFile(t, filepath.Join(dir, "modules", "base.vgo"), `
name: base
resources:
  - name: noop
    type: ssh_exec
    command: "true"
`)

	writeFile(t, filepath.Join(dir, "envoys", "envoys.vgo"), `
envoys:
  - match: "gw.example.com"
    modules: []
  - match: "sw01.example.com"
    device_type: cisco_ios
    gateway: "gw.example.com"
    credential: "secret:net/sw01"
    ip_address: "10.0.1.1"
    modules: [base]
  - match: "rtr01.example.com"
    device_type: juniper_junos
    gateway: "gw.example.com"
    credential: "secret:net/rtr01"
    ip_address: "10.0.1.2"
    modules: [base]
  - match: "normal.example.com"
    modules: [base]
`)

	prov := &mockProvider{secrets: map[string]string{"net/sw01": "p1", "net/rtr01": "p2"}}
	store, err := NewConfigStore(dir, prov, true)
	if err != nil {
		t.Fatalf("NewConfigStore: %v", err)
	}

	entries := store.AllDeviceEntries()
	if len(entries) != 2 {
		t.Fatalf("AllDeviceEntries returned %d, want 2", len(entries))
	}

	// Verify only device entries are returned (not normal envoys).
	for _, e := range entries {
		if e.DeviceType == "" {
			t.Errorf("AllDeviceEntries returned non-device entry: %s", e.Hostname)
		}
		if e.Gateway != "gw.example.com" {
			t.Errorf("entry %s Gateway = %q, want gw.example.com", e.Hostname, e.Gateway)
		}
	}
}
