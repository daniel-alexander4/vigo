// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

// Package clismoke is an end-to-end smoke test for vigocli against a real,
// ephemeral vigosrv. It boots the shipped server binary into a throwaway
// serverRoot (temp dir), enrolls a small synthetic fleet via the fakefleet
// harness, and drives vigocli as a subprocess across its server-mediated
// command surface — asserting each command exits cleanly and returns sane
// output. The instance is torn down at the end, so even mutating commands are
// safe; nothing touches /srv/vigo.
//
// Scope: the REST-backed read/admin surface that round-trips through the
// server (envoys, traits, runs, doctor, security, compliance, license,
// spanner, approved, tokens). Out of scope — needs a real Rust agent or the
// not-yet-relocatable CLI publish path: scrier, swarm content subsystems,
// live task/workflow execution, peer/spanner topology, and config
// publish/rollback (the local-filesystem config commands read /srv/vigo/stacks
// directly). Those stay with the manual home-lab pass.
//
// Prerequisites: bin/vigosrv + bin/vigocli (run `make build`). Absent either,
// the suite skips cleanly so a fresh `go test ./...` stays green.
package clismoke

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Shared fixture, set up once in TestMain and read by the test functions.
var (
	srvURL         string   // https://127.0.0.1:<apiPort>
	cliBin         string   // bin/vigocli
	cliEnv         []string // os.Environ() + VIGO_CONFIG_DIR (points vigocli at the temp admin token)
	sampleEnvoyID  string   // a real enrolled envoy UUID, from fakefleet's state manifest
	sampleHostname string   // its hostname
)

func TestMain(m *testing.M) { os.Exit(run(m)) }

// run does setup/teardown around m.Run() so deferred cleanup actually fires
// (os.Exit in TestMain would skip defers otherwise).
func run(m *testing.M) (code int) {
	root := findRepoRoot()
	vigosrv := filepath.Join(root, "bin", "vigosrv")
	cliBin = filepath.Join(root, "bin", "vigocli")
	for _, b := range []string{vigosrv, cliBin} {
		if _, err := os.Stat(b); err != nil {
			fmt.Fprintf(os.Stderr, "skip: %s not found — run 'make build' first\n", b)
			return 0
		}
	}

	tmp, err := os.MkdirTemp("", "vigo-clismoke-*")
	if err != nil {
		fmt.Fprintln(os.Stderr, "mktemp:", err)
		return 1
	}
	defer os.RemoveAll(tmp)

	// Build the fakefleet harness once into the temp dir.
	ffBin := filepath.Join(tmp, "fakefleet")
	if out, err := buildCmd(root, ffBin, "./test/fakefleet"); err != nil {
		fmt.Fprintf(os.Stderr, "build fakefleet: %v\n%s", err, out)
		return 1
	}

	apiPort, grpcPort := freePort(), freePort()
	if err := writeFixture(tmp, apiPort, grpcPort); err != nil {
		fmt.Fprintln(os.Stderr, "write fixture:", err)
		return 1
	}

	// Boot vigosrv into the temp serverRoot. configFile lives under tmp, so the
	// 0.75.8 serverRoot anchoring keeps the whole tree (.live, puddle, certs,
	// db, license) inside tmp — /srv/vigo is never read or written.
	logPath := filepath.Join(tmp, "vigosrv.log")
	logFile, _ := os.Create(logPath)
	defer logFile.Close()
	srv := exec.Command(vigosrv)
	srv.Dir = tmp
	srv.Env = append(os.Environ(),
		"VIGO_CONFIG_FILE="+filepath.Join(tmp, "server.yaml"),
		"VIGO_DOCS_DIR="+filepath.Join(tmp, "docs"),
	)
	srv.Stdout, srv.Stderr = logFile, logFile
	if err := srv.Start(); err != nil {
		fmt.Fprintln(os.Stderr, "start vigosrv:", err)
		return 1
	}
	defer func() {
		_ = srv.Process.Kill()
		_, _ = srv.Process.Wait()
	}()

	srvURL = fmt.Sprintf("https://127.0.0.1:%d", apiPort)
	if !waitREST(srvURL, 20*time.Second) {
		fmt.Fprintf(os.Stderr, "vigosrv did not become ready; log:\n%s\n", tail(logPath, 40))
		return 1
	}

	// Point vigocli at the temp instance's auto-minted admin token. vigocli
	// reads $VIGO_CONFIG_DIR/token before falling back to /srv/vigo, so this
	// authenticates against the temp server without touching the prod token.
	tok, err := os.ReadFile(filepath.Join(tmp, "cli-admin-token"))
	if err != nil {
		fmt.Fprintln(os.Stderr, "read admin token:", err)
		return 1
	}
	cfgDir := filepath.Join(tmp, "cliconfig")
	if err := os.MkdirAll(cfgDir, 0o700); err != nil {
		fmt.Fprintln(os.Stderr, "mkdir cliconfig:", err)
		return 1
	}
	if err := os.WriteFile(filepath.Join(cfgDir, "token"), tok, 0o600); err != nil {
		fmt.Fprintln(os.Stderr, "write token:", err)
		return 1
	}
	cliEnv = append(os.Environ(), "VIGO_CONFIG_DIR="+cfgDir)

	// Enroll a small synthetic fleet — no publish (the temp instance already
	// serves the seeded .live, and vigocli config publish is hardwired to
	// /srv/vigo). create runs a first report tick, so envoys land with traits
	// and a convergence run for the read surface to show.
	statePath := filepath.Join(tmp, "fleet", "state.json")
	ff := exec.Command(ffBin, "create",
		"--skip-publish",
		"--api", srvURL,
		"--server", fmt.Sprintf("127.0.0.1:%d", grpcPort),
		"--admin-token", strings.TrimSpace(string(tok)),
		"--stacks-dir", filepath.Join(tmp, "stacks"),
		"--state", statePath,
		"--vigocli", cliBin,
		"--count", "5", "--named", "5", "--broken", "1", "--changed", "1",
		"--concurrency", "4",
	)
	if out, err := ff.CombinedOutput(); err != nil {
		fmt.Fprintf(os.Stderr, "fakefleet create: %v\n%s", err, out)
		return 1
	}
	if sampleEnvoyID, sampleHostname, err = readSampleEnvoy(statePath); err != nil {
		fmt.Fprintln(os.Stderr, "read fleet state:", err)
		return 1
	}

	return m.Run()
}

// TestCLIReadSurface drives the server-mediated read/admin commands. Each must
// exit 0; where a stable token is expected in the output, assert it.
func TestCLIReadSurface(t *testing.T) {
	cases := []struct {
		name string
		args []string
		want string // substring required in output (empty = exit-0 is enough)
	}{
		{"approved list", []string{"approved", "list"}, ""},
		{"tokens list", []string{"tokens", "list"}, ""},
		{"envoys list", []string{"envoys", "list"}, sampleHostname},
		{"envoys show", []string{"envoys", "show", sampleEnvoyID}, sampleHostname},
		{"envoys preview", []string{"envoys", "preview", sampleEnvoyID}, ""},
		{"runs list", []string{"runs", "list"}, ""},
		// `envoys traits <hostname>` is intentionally omitted: inline CheckIn
		// traits land in envoy_traits via a batched insert, so a query right
		// after enroll 404s with "not reported yet" until the batch flushes.
		// `traits inventory` reads the immediate in-memory FleetIndex instead.
		{"traits inventory", []string{"traits", "inventory"}, ""},
		{"doctor", []string{"doctor"}, ""},
		{"security audit", []string{"security", "audit"}, ""},
		{"security risk", []string{"security", "risk"}, ""},
		{"compliance report", []string{"compliance", "report"}, ""},
		{"compliance provisions", []string{"compliance", "provisions"}, ""},
		{"compliance bundles", []string{"compliance", "bundles"}, ""},
		{"license", []string{"license"}, ""},
		{"spanner status", []string{"spanner", "status"}, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			out, err := runCLI(t, c.args...)
			if err != nil {
				t.Fatalf("`vigocli %s` failed: %v\n%s", strings.Join(c.args, " "), err, truncate(out, 1500))
			}
			if c.want != "" && !strings.Contains(out, c.want) {
				t.Errorf("`vigocli %s` output missing %q\n%s", strings.Join(c.args, " "), c.want, truncate(out, 1500))
			}
		})
	}
}

// TestCLIMutationsSafe exercises a few write commands. The instance is torn
// down after the run, so these leave no lasting state — they prove the write
// RPCs reach the server and return cleanly.
func TestCLIMutationsSafe(t *testing.T) {
	if _, err := runCLI(t, "tokens", "generate"); err != nil {
		t.Errorf("tokens generate: %v", err)
	}
	if _, err := runCLI(t, "envoys", "tag", sampleEnvoyID, "smoke-tag"); err != nil {
		t.Errorf("envoys tag: %v", err)
	}
	if _, err := runCLI(t, "envoys", "untag", sampleEnvoyID, "smoke-tag"); err != nil {
		t.Errorf("envoys untag: %v", err)
	}
	if _, err := runCLI(t, "envoys", "maintenance", sampleEnvoyID, "--until", "2099-01-01T00:00:00Z"); err != nil {
		t.Errorf("envoys maintenance set: %v", err)
	}
	if _, err := runCLI(t, "envoys", "maintenance", sampleEnvoyID, "--clear"); err != nil {
		t.Errorf("envoys maintenance clear: %v", err)
	}
}

// ---------- helpers ----------

// runCLI runs vigocli against the temp server with the temp admin token.
// --server goes last, matching the persistent-flag usage operators copy.
func runCLI(t *testing.T, args ...string) (string, error) {
	t.Helper()
	full := append(append([]string{}, args...), "--server", srvURL)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, cliBin, full...)
	cmd.Env = cliEnv
	out, err := cmd.CombinedOutput()
	return string(out), err
}

func buildCmd(dir, out, pkg string) (string, error) {
	cmd := exec.Command("go", "build", "-o", out, pkg)
	cmd.Dir = dir
	b, err := cmd.CombinedOutput()
	return string(b), err
}

// freePort grabs an unused localhost TCP port. A small bind race is acceptable
// for a single-shot test fixture.
func freePort() int {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port
}

// waitREST polls the unauthenticated /metrics endpoint until the server's
// build_info line appears or the deadline passes.
func waitREST(base string, timeout time.Duration) bool {
	client := &http.Client{
		Timeout:   2 * time.Second,
		Transport: &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}},
	}
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		resp, err := client.Get(base + "/metrics")
		if err == nil {
			body := make([]byte, 4096)
			n, _ := resp.Body.Read(body)
			resp.Body.Close()
			if strings.Contains(string(body[:n]), "vigo_build_info") {
				return true
			}
		}
		time.Sleep(250 * time.Millisecond)
	}
	return false
}

func readSampleEnvoy(statePath string) (id, hostname string, err error) {
	data, err := os.ReadFile(statePath)
	if err != nil {
		return "", "", err
	}
	var st struct {
		Hosts []struct {
			EnvoyID  string `json:"envoy_id"`
			Hostname string `json:"hostname"`
		} `json:"hosts"`
	}
	if err := json.Unmarshal(data, &st); err != nil {
		return "", "", err
	}
	for _, h := range st.Hosts {
		if h.EnvoyID != "" {
			return h.EnvoyID, h.Hostname, nil
		}
	}
	return "", "", fmt.Errorf("no enrolled envoy in %s", statePath)
}

// writeFixture lays down the minimal temp serverRoot: a server.yaml on random
// ports with every writable path pointed inside tmp, plus a seeded .live so
// the config store has a valid published tree to load at boot.
func writeFixture(tmp string, apiPort, grpcPort int) error {
	for _, d := range []string{
		filepath.Join(tmp, ".live", "stacks"),
		filepath.Join(tmp, ".live", "configcrates"),
		filepath.Join(tmp, "secrets"),
		filepath.Join(tmp, "fleet"),
		filepath.Join(tmp, "docs"),
		filepath.Join(tmp, "compliance-docs"),
	} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			return err
		}
	}

	yaml := fmt.Sprintf(`server:
  grpc_listen: "127.0.0.1:%d"
  api_listen: "127.0.0.1:%d"
  tls:
    cert_file: "tls/cert"
    key_file: "tls/key"
    ca_file: "tls/ca"
    ca_key_file: "tls/ca_key"
database:
  dsn: %q
secrets:
  backend: local
  secrets_dir: %q
checkin:
  interval: "5m"
  jitter_percent: 20
bootstrap:
  cert_validity: "8760h"
  trusted_enrollment:
    - pattern: "*"
      cidrs: ["127.0.0.0/8"]
watcher:
  enabled: false
compliance:
  docs:
    docs_dir: %q
`,
		grpcPort, apiPort,
		filepath.Join(tmp, "vigo.db"),
		filepath.Join(tmp, "secrets"),
		filepath.Join(tmp, "compliance-docs"),
	)
	if err := os.WriteFile(filepath.Join(tmp, "server.yaml"), []byte(yaml), 0o600); err != nil {
		return err
	}

	// One hostcrate matching every hostname, so enrolled envoys resolve to a
	// (empty) config and the read surface has something to trace.
	envoys := "envoys:\n  - match: \"*\"\n    roles: []\n"
	return os.WriteFile(filepath.Join(tmp, ".live", "stacks", "envoys.vgo"), []byte(envoys), 0o600)
}

func findRepoRoot() string {
	dir, _ := os.Getwd()
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return dir
		}
		dir = parent
	}
}

func tail(path string, lines int) string {
	data, err := os.ReadFile(path)
	if err != nil {
		return "(no log: " + err.Error() + ")"
	}
	ls := strings.Split(strings.TrimRight(string(data), "\n"), "\n")
	if len(ls) > lines {
		ls = ls[len(ls)-lines:]
	}
	return strings.Join(ls, "\n")
}

func truncate(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max] + "\n... (truncated)"
}
