// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: BUSL-1.1

package update

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

func TestCheckForUpdate_ReturnsVersionInfo_WhenNewer(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]string{
			"version":      "2.0.0",
			"sha256":       "abc123",
			"download_url": "/api/v1/agent/download/linux/amd64",
		})
	}))
	defer srv.Close()

	info, err := CheckForUpdate(context.Background(), srv.URL, "1.0.0", "linux", "amd64")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if info == nil {
		t.Fatal("expected VersionInfo, got nil")
	}
	if info.Version != "2.0.0" {
		t.Errorf("expected version 2.0.0, got %s", info.Version)
	}
	if info.SHA256 != "abc123" {
		t.Errorf("expected sha256 abc123, got %s", info.SHA256)
	}
}

func TestCheckForUpdate_ReturnsNil_WhenVersionMatches(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]string{
			"version":      "1.0.0",
			"sha256":       "abc123",
			"download_url": "/api/v1/agent/download/linux/amd64",
		})
	}))
	defer srv.Close()

	info, err := CheckForUpdate(context.Background(), srv.URL, "1.0.0", "linux", "amd64")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if info != nil {
		t.Fatalf("expected nil (no update), got version=%s", info.Version)
	}
}

func TestCheckForUpdate_ReturnsNil_WhenEmptyVersion(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]string{
			"version":      "",
			"sha256":       "",
			"download_url": "",
		})
	}))
	defer srv.Close()

	info, err := CheckForUpdate(context.Background(), srv.URL, "1.0.0", "linux", "amd64")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if info != nil {
		t.Fatalf("expected nil (no binaries available), got version=%s", info.Version)
	}
}

func TestApply_DownloadsVerifiesAndReplaces(t *testing.T) {
	binaryContent := []byte("new-agent-binary-v2")
	checksum := fmt.Sprintf("%x", sha256.Sum256(binaryContent))

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write(binaryContent)
	}))
	defer srv.Close()

	dir := t.TempDir()
	currentBinary := filepath.Join(dir, "hobnob")
	if err := os.WriteFile(currentBinary, []byte("old-agent-binary-v1"), 0755); err != nil {
		t.Fatal(err)
	}

	execCalled := false
	fakeExec := func(binary string, args []string, env []string) error {
		execCalled = true
		return nil
	}

	info := &VersionInfo{
		Version:     "2.0.0",
		SHA256:      checksum,
		DownloadURL: srv.URL + "/download",
	}

	err := Apply(info, currentBinary, http.DefaultClient, fakeExec)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Verify binary was replaced.
	content, err := os.ReadFile(currentBinary)
	if err != nil {
		t.Fatal(err)
	}
	if string(content) != "new-agent-binary-v2" {
		t.Errorf("binary content not updated: %s", string(content))
	}

	// Verify exec was called.
	if !execCalled {
		t.Error("exec function should have been called")
	}
}

func TestApply_ReturnsError_WhenChecksumMismatches(t *testing.T) {
	binaryContent := []byte("new-agent-binary-v2")

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write(binaryContent)
	}))
	defer srv.Close()

	dir := t.TempDir()
	currentBinary := filepath.Join(dir, "hobnob")
	if err := os.WriteFile(currentBinary, []byte("old"), 0755); err != nil {
		t.Fatal(err)
	}

	info := &VersionInfo{
		Version:     "2.0.0",
		SHA256:      "wrong-checksum-value",
		DownloadURL: srv.URL + "/download",
	}

	err := Apply(info, currentBinary, http.DefaultClient, nil)
	if err == nil {
		t.Fatal("expected error for checksum mismatch")
	}

	// Verify original binary was NOT replaced.
	content, err := os.ReadFile(currentBinary)
	if err != nil {
		t.Fatal(err)
	}
	if string(content) != "old" {
		t.Errorf("original binary should be unchanged, got: %s", string(content))
	}
}

func TestApply_ReturnsError_WhenDownloadFails(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	dir := t.TempDir()
	currentBinary := filepath.Join(dir, "hobnob")
	if err := os.WriteFile(currentBinary, []byte("old"), 0755); err != nil {
		t.Fatal(err)
	}

	info := &VersionInfo{
		Version:     "2.0.0",
		SHA256:      "abc",
		DownloadURL: srv.URL + "/download",
	}

	err := Apply(info, currentBinary, http.DefaultClient, nil)
	if err == nil {
		t.Fatal("expected error for failed download")
	}
}

func TestApply_CleansUpTempFileOnError(t *testing.T) {
	binaryContent := []byte("new-agent-binary-v2")

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write(binaryContent)
	}))
	defer srv.Close()

	dir := t.TempDir()
	currentBinary := filepath.Join(dir, "hobnob")
	if err := os.WriteFile(currentBinary, []byte("old"), 0755); err != nil {
		t.Fatal(err)
	}

	info := &VersionInfo{
		Version:     "2.0.0",
		SHA256:      "wrong-checksum",
		DownloadURL: srv.URL + "/download",
	}

	_ = Apply(info, currentBinary, http.DefaultClient, nil)

	// Verify no temp files remain in the directory.
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if e.Name() != "hobnob" {
			t.Errorf("leftover temp file found: %s", e.Name())
		}
	}
}
