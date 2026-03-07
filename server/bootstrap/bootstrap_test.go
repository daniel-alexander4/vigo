package bootstrap

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestBootstrapScript_ReturnsShellScript(t *testing.T) {
	dir := t.TempDir()
	caFile := filepath.Join(dir, "ca.crt")
	if err := os.WriteFile(caFile, []byte("-----BEGIN CERTIFICATE-----\nFAKECA\n-----END CERTIFICATE-----\n"), 0644); err != nil {
		t.Fatal(err)
	}

	h := NewHandler("https://server.example.com:8080", caFile, filepath.Join(dir, "agents"), "1.0.0")

	req := httptest.NewRequest(http.MethodGet, "/bootstrap?token=mytoken123", nil)
	w := httptest.NewRecorder()
	h.BootstrapScript(w, req)

	resp := w.Result()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); ct != "text/plain" {
		t.Fatalf("expected Content-Type text/plain, got %s", ct)
	}

	body, _ := io.ReadAll(resp.Body)
	script := string(body)

	if !strings.HasPrefix(script, "#!/bin/sh") && !strings.HasPrefix(script, "#!/usr/bin/env sh") {
		t.Error("bootstrap script should start with a shebang line")
	}
	if !strings.Contains(script, "server.example.com:8080") {
		t.Error("bootstrap script should contain server address")
	}
}

func TestBootstrapScript_ContainsTokenAndServer(t *testing.T) {
	dir := t.TempDir()
	caFile := filepath.Join(dir, "ca.crt")
	if err := os.WriteFile(caFile, []byte("-----BEGIN CERTIFICATE-----\nFAKECA\n-----END CERTIFICATE-----\n"), 0644); err != nil {
		t.Fatal(err)
	}

	h := NewHandler("https://server.example.com:8080", caFile, filepath.Join(dir, "agents"), "1.0.0")

	req := httptest.NewRequest(http.MethodGet, "/bootstrap?token=secret-token-42", nil)
	w := httptest.NewRecorder()
	h.BootstrapScript(w, req)

	body, _ := io.ReadAll(w.Result().Body)
	script := string(body)

	if !strings.Contains(script, "secret-token-42") {
		t.Error("bootstrap script should contain the provided token")
	}
	if !strings.Contains(script, "server.example.com:8080") {
		t.Error("bootstrap script should contain the server address")
	}
}

func TestBootstrapScript_SingleQuotesInterpolatedValues(t *testing.T) {
	dir := t.TempDir()
	caFile := filepath.Join(dir, "ca.crt")
	if err := os.WriteFile(caFile, []byte("-----BEGIN CERTIFICATE-----\nFAKECA\n-----END CERTIFICATE-----\n"), 0644); err != nil {
		t.Fatal(err)
	}

	h := NewHandler("https://server.example.com:8080", caFile, filepath.Join(dir, "agents"), "1.0.0")

	req := httptest.NewRequest(http.MethodGet, "/bootstrap?token=test-token", nil)
	w := httptest.NewRecorder()
	h.BootstrapScript(w, req)

	body, _ := io.ReadAll(w.Result().Body)
	script := string(body)

	// The token and server address should be single-quoted for injection safety.
	if !strings.Contains(script, "'test-token'") {
		t.Error("token should be single-quoted in bootstrap script")
	}
	if !strings.Contains(script, "'https://server.example.com:8080'") {
		t.Error("server address should be single-quoted in bootstrap script")
	}
}

func TestBootstrapScript_Returns400_WithoutToken(t *testing.T) {
	dir := t.TempDir()
	caFile := filepath.Join(dir, "ca.crt")
	if err := os.WriteFile(caFile, []byte("FAKE"), 0644); err != nil {
		t.Fatal(err)
	}

	h := NewHandler("https://server:8080", caFile, filepath.Join(dir, "agents"), "1.0.0")

	req := httptest.NewRequest(http.MethodGet, "/bootstrap", nil)
	w := httptest.NewRecorder()
	h.BootstrapScript(w, req)

	if w.Result().StatusCode != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", w.Result().StatusCode)
	}
}

func TestAgentLatest_ReturnsVersionJSON(t *testing.T) {
	dir := t.TempDir()
	// Create a fake agent binary.
	agentDir := filepath.Join(dir, "agents")
	osDir := filepath.Join(agentDir, "linux", "amd64")
	if err := os.MkdirAll(osDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(osDir, "isogov-agent"), []byte("binary-content"), 0755); err != nil {
		t.Fatal(err)
	}

	h := NewHandler("https://server:8080", "", agentDir, "1.2.3")

	req := httptest.NewRequest(http.MethodGet, "/api/v1/agent/latest?os=linux&arch=amd64", nil)
	w := httptest.NewRecorder()
	h.AgentLatest(w, req)

	resp := w.Result()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}

	var body struct {
		Version     string `json:"version"`
		SHA256      string `json:"sha256"`
		DownloadURL string `json:"download_url"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode JSON: %v", err)
	}

	if body.Version != "1.2.3" {
		t.Errorf("expected version 1.2.3, got %s", body.Version)
	}
	if body.SHA256 == "" {
		t.Error("sha256 should not be empty when binary exists")
	}
	if body.DownloadURL != "/api/v1/agent/download/linux/amd64" {
		t.Errorf("unexpected download_url: %s", body.DownloadURL)
	}
}

func TestAgentDownload_ServesFile(t *testing.T) {
	dir := t.TempDir()
	agentDir := filepath.Join(dir, "agents")
	osDir := filepath.Join(agentDir, "linux", "amd64")
	if err := os.MkdirAll(osDir, 0755); err != nil {
		t.Fatal(err)
	}
	content := []byte("fake-agent-binary-v1")
	if err := os.WriteFile(filepath.Join(osDir, "isogov-agent"), content, 0755); err != nil {
		t.Fatal(err)
	}

	h := NewHandler("https://server:8080", "", agentDir, "1.0.0")

	mux := http.NewServeMux()
	h.RegisterRoutes(mux)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/agent/download/linux/amd64", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	resp := w.Result()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}

	body, _ := io.ReadAll(resp.Body)
	if string(body) != "fake-agent-binary-v1" {
		t.Errorf("unexpected body: %s", string(body))
	}
}

func TestAgentDownload_Returns404_WhenBinaryMissing(t *testing.T) {
	dir := t.TempDir()
	h := NewHandler("https://server:8080", "", filepath.Join(dir, "agents"), "1.0.0")

	mux := http.NewServeMux()
	h.RegisterRoutes(mux)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/agent/download/linux/amd64", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Result().StatusCode != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", w.Result().StatusCode)
	}
}

func TestAgentDownload_RejectsInvalidOsArch(t *testing.T) {
	dir := t.TempDir()
	h := NewHandler("https://server:8080", "", filepath.Join(dir, "agents"), "1.0.0")

	mux := http.NewServeMux()
	h.RegisterRoutes(mux)

	tests := []struct {
		path string
	}{
		{"/api/v1/agent/download/windows/amd64"},
		{"/api/v1/agent/download/linux/386"},
		{"/api/v1/agent/download/freebsd/arm64"},
	}

	for _, tt := range tests {
		req := httptest.NewRequest(http.MethodGet, tt.path, nil)
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, req)

		if w.Result().StatusCode != http.StatusBadRequest {
			t.Errorf("%s: expected 400, got %d", tt.path, w.Result().StatusCode)
		}
	}
}
