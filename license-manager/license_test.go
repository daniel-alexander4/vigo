// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/hobnob/hobnob/server/license"
)

func TestSignAndVerifyRoundTrip(t *testing.T) {
	dir := t.TempDir()
	cleanupImmutableKeys(t, dir)
	if err := generateAndSaveKeys(dir); err != nil {
		t.Fatalf("generateAndSaveKeys: %v", err)
	}

	origKeyDir := keyDir
	keyDir = dir
	defer func() { keyDir = origKeyDir }()

	payload := license.Payload{
		CustomerName: "Acme Corp",
		CustomerID:   "acme",
		LicenseID:    "lic-test-acme",
		Tier:         "standard",
		MaxThralls:   100,
		IssuedAt:     "2026-01-01",
		ExpiresAt:    "2027-01-01",
		Version:      1,
	}

	lic, err := signPayload(payload)
	if err != nil {
		t.Fatalf("signPayload: %v", err)
	}

	pubHex, err := os.ReadFile(filepath.Join(dir, publicKeyFile))
	if err != nil {
		t.Fatalf("reading public key: %v", err)
	}

	if err := lic.VerifyWithKey(string(pubHex)); err != nil {
		t.Fatalf("VerifyWithKey: %v", err)
	}
}

func TestWriteAndLoadLicenseFile(t *testing.T) {
	dir := t.TempDir()
	cleanupImmutableKeys(t, dir)
	if err := generateAndSaveKeys(dir); err != nil {
		t.Fatalf("generateAndSaveKeys: %v", err)
	}

	origKeyDir := keyDir
	keyDir = dir
	defer func() { keyDir = origKeyDir }()

	payload := license.Payload{
		CustomerName: "Test Co",
		CustomerID:   "testco",
		LicenseID:    "lic-test-testco",
		Tier:         "standard",
		MaxThralls:   50,
		IssuedAt:     "2026-03-01",
		ExpiresAt:    "2027-03-01",
		Version:      1,
	}

	lic, err := signPayload(payload)
	if err != nil {
		t.Fatalf("signPayload: %v", err)
	}

	outPath := filepath.Join(dir, "test.lic")
	if err := writeLicenseFile(outPath, lic); err != nil {
		t.Fatalf("writeLicenseFile: %v", err)
	}

	loaded, err := license.LoadFile(outPath)
	if err != nil {
		t.Fatalf("LoadFile: %v", err)
	}

	if loaded.Payload.CustomerName != "Test Co" {
		t.Errorf("CustomerName = %q, want Test Co", loaded.Payload.CustomerName)
	}
	if loaded.Payload.MaxThralls != 50 {
		t.Errorf("MaxThralls = %d, want 50", loaded.Payload.MaxThralls)
	}
	if loaded.Payload.LicenseID != "lic-test-testco" {
		t.Errorf("LicenseID = %q, want lic-test-testco", loaded.Payload.LicenseID)
	}
}

func TestVerifyCorruptSignature(t *testing.T) {
	dir := t.TempDir()
	cleanupImmutableKeys(t, dir)
	if err := generateAndSaveKeys(dir); err != nil {
		t.Fatalf("generateAndSaveKeys: %v", err)
	}

	origKeyDir := keyDir
	keyDir = dir
	defer func() { keyDir = origKeyDir }()

	payload := license.Payload{
		CustomerName: "Corrupt Test",
		CustomerID:   "corrupt",
		LicenseID:    "lic-test-corrupt",
		Tier:         "standard",
		MaxThralls:   10,
		IssuedAt:     "2026-01-01",
		ExpiresAt:    "2027-01-01",
		Version:      1,
	}

	lic, err := signPayload(payload)
	if err != nil {
		t.Fatalf("signPayload: %v", err)
	}

	// Corrupt the signature.
	lic.Signature = "AAAA" + lic.Signature[4:]

	pubHex, _ := os.ReadFile(filepath.Join(dir, publicKeyFile))
	if err := lic.VerifyWithKey(string(pubHex)); err == nil {
		t.Fatal("expected verification to fail with corrupt signature")
	}
}

func TestLicenseInspectFile(t *testing.T) {
	dir := t.TempDir()
	cleanupImmutableKeys(t, dir)
	if err := generateAndSaveKeys(dir); err != nil {
		t.Fatalf("generateAndSaveKeys: %v", err)
	}

	origKeyDir := keyDir
	keyDir = dir
	defer func() { keyDir = origKeyDir }()

	payload := license.Payload{
		CustomerName: "Inspect Test",
		CustomerID:   "inspect",
		LicenseID:    "lic-test-inspect",
		Tier:         "standard",
		MaxThralls:   25,
		IssuedAt:     "2026-01-01",
		ExpiresAt:    "2027-01-01",
		Version:      1,
	}

	lic, err := signPayload(payload)
	if err != nil {
		t.Fatalf("signPayload: %v", err)
	}

	outPath := filepath.Join(dir, "inspect.lic")
	if err := writeLicenseFile(outPath, lic); err != nil {
		t.Fatalf("writeLicenseFile: %v", err)
	}

	// runLicenseInspect should not return an error.
	if err := runLicenseInspect(nil, []string{outPath}); err != nil {
		t.Fatalf("runLicenseInspect: %v", err)
	}
}
