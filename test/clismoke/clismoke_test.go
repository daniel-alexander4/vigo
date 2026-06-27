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
// Prerequisites: bin/vigosrv + bin/vigocli. `make test` builds them so this
// suite always runs in the canonical gate; absent either (a bare `go test
// ./...`), the suite skips cleanly so it stays green.
package clismoke

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
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
			fmt.Fprintf(os.Stderr, "skip: %s not found — run 'make test' (which builds it) or 'make build'\n", b)
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

	// Enroll a small synthetic fleet. --server-root anchors fakefleet's whole
	// flow on tmp: it writes the config tree under tmp/stacks and publishes it
	// through the real operator path (`vigocli config publish --server-root tmp`
	// → tmp/.live → reload) before enrolling, so the envoys converge against a
	// real published tree — no --skip-publish, no hand-seeded .live. create also
	// runs a first report tick, so envoys land with traits + a convergence run.
	statePath := filepath.Join(tmp, "fleet", "state.json")
	ff := exec.Command(ffBin, "create",
		"--api", srvURL,
		"--server", fmt.Sprintf("127.0.0.1:%d", grpcPort),
		"--admin-token", strings.TrimSpace(string(tok)),
		"--server-root", tmp,
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
	if _, err := runCLI(t, "envoys", "maintenance", sampleEnvoyID, "--until", "2099-01-01T00:00:00Z"); err != nil {
		t.Errorf("envoys maintenance set: %v", err)
	}
	if _, err := runCLI(t, "envoys", "maintenance", sampleEnvoyID, "--clear"); err != nil {
		t.Errorf("envoys maintenance clear: %v", err)
	}
}

// TestCLIMutationRoundTrip proves a write is reflected by the matching read —
// not merely that the write RPC returned 2xx (what TestCLIMutationsSafe
// checks). It creates a web user, confirms it surfaces in `webusers list`,
// flips its role, and confirms the new role surfaces. The instance is torn
// down afterward, so the created user leaves no lasting state.
func TestCLIMutationRoundTrip(t *testing.T) {
	const user = "rt-smoke"

	if _, err := runCLI(t, "webusers", "create", "--username", user, "--role", "viewer", "--no-password"); err != nil {
		t.Fatalf("webusers create: %v", err)
	}
	out, err := runCLI(t, "webusers", "list")
	if err != nil {
		t.Fatalf("webusers list: %v\n%s", err, truncate(out, 1500))
	}
	if !lineContains(out, user, "viewer") {
		t.Fatalf("created user %q (viewer) not reflected in `webusers list`:\n%s", user, truncate(out, 1500))
	}

	// A mutation to that same row must change what the read returns.
	if _, err := runCLI(t, "webusers", "set-role", "--username", user, "--role", "compliance"); err != nil {
		t.Fatalf("webusers set-role: %v", err)
	}
	out, err = runCLI(t, "webusers", "list")
	if err != nil {
		t.Fatalf("webusers list (after set-role): %v\n%s", err, truncate(out, 1500))
	}
	if !lineContains(out, user, "compliance") {
		t.Errorf("set-role to compliance not reflected for %q in `webusers list`:\n%s", user, truncate(out, 1500))
	}
}

// TestCLIJSONOutputContract asserts the machine-readable (`--format json`)
// output of a scriptable read command is valid JSON carrying the documented
// top-level keys — the contract a consumer's `jq` pipeline depends on. A
// renamed or dropped field turns this red. The compliance report is a
// server-built document available on any fleet, so it needs no agent.
func TestCLIJSONOutputContract(t *testing.T) {
	args := []string{"compliance", "report", "compliance", "--format", "json"}
	wantKeys := []string{"generated_at", "summary", "controls"}

	stdout, err := runCLIStdout(t, args...)
	if err != nil {
		t.Fatalf("`vigocli %s` failed: %v", strings.Join(args, " "), err)
	}
	var v map[string]any
	if err := json.Unmarshal([]byte(stdout), &v); err != nil {
		t.Fatalf("`vigocli %s` stdout is not valid JSON: %v\n%s", strings.Join(args, " "), err, truncate(stdout, 1500))
	}
	for _, k := range wantKeys {
		if _, ok := v[k]; !ok {
			t.Errorf("`vigocli %s` JSON missing top-level key %q\n%s", strings.Join(args, " "), k, truncate(stdout, 1500))
		}
	}
}

// TestCLIRBACMatrix mints API tokens for the viewer and compliance roles and
// asserts the per-role authorization boundary the server enforces. The key
// invariant is the documented carve-out: the compliance role is denied
// audit.read (the raw hash-chained audit log stays admin/viewer-only). Denied
// calls must come back 403 (authenticated, unauthorized) — not 401 — which
// also proves the minted token authenticated correctly.
func TestCLIRBACMatrix(t *testing.T) {
	viewerTok := mintRoleToken(t, "viewer", "rbac-viewer")
	compTok := mintRoleToken(t, "compliance", "rbac-compliance")

	cases := []struct {
		name    string
		token   string
		args    []string
		wantErr bool
		want    string // substring required in output
	}{
		// viewer holds fleet.read + audit.read, but not fleet.write. The
		// successful read also proves the minted token authenticates.
		{"viewer reads envoys", viewerTok, []string{"envoys", "list"}, false, sampleHostname},
		{"viewer reads audit (audit.read)", viewerTok, []string{"security", "audit", "list"}, false, ""},
		{"viewer denied maintenance (fleet.write)", viewerTok, []string{"envoys", "maintenance", sampleEnvoyID, "--until", "2099-01-01T00:00:00Z"}, true, "403"},
		// compliance is deliberately denied audit.read — the documented
		// carve-out keeping the raw hash-chained audit log admin/viewer-only.
		// A 403 (not 401) here proves the compliance token authenticated.
		{"compliance denied audit (audit.read carve-out)", compTok, []string{"security", "audit", "list"}, true, "403"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			out, err := runCLIWithToken(t, c.token, c.args...)
			if c.wantErr && err == nil {
				t.Fatalf("`vigocli %s` unexpectedly succeeded; want denial\n%s", strings.Join(c.args, " "), truncate(out, 1500))
			}
			if !c.wantErr && err != nil {
				t.Fatalf("`vigocli %s` unexpectedly failed: %v\n%s", strings.Join(c.args, " "), err, truncate(out, 1500))
			}
			if c.want != "" && !strings.Contains(out, c.want) {
				t.Errorf("`vigocli %s` output missing %q\n%s", strings.Join(c.args, " "), c.want, truncate(out, 1500))
			}
		})
	}
}

// TestCLIErrorSurface checks the unhappy paths surface a clean operator-facing
// error — an HTTP status the caller can act on, never a panic or a raw Go
// stack trace leaking out of the CLI.
func TestCLIErrorSurface(t *testing.T) {
	// A well-formed but nonexistent envoy ID → 404 not found.
	out, err := runCLI(t, "envoys", "show", "00000000-0000-0000-0000-000000000000")
	if err == nil {
		t.Errorf("envoys show <nonexistent> should fail, got success:\n%s", truncate(out, 1500))
	} else if !strings.Contains(out, "404") && !strings.Contains(out, "not found") {
		t.Errorf("envoys show <nonexistent>: want a 404/not-found error, got:\n%s", truncate(out, 1500))
	}

	// A syntactically valid but unknown token → 401 unauthorized (not a crash).
	const junk = "vgot_0000000000000000000000000000000000000000000000000000000000000000"
	out, err = runCLIWithToken(t, junk, "envoys", "list")
	if err == nil {
		t.Errorf("envoys list with junk token should fail, got success:\n%s", truncate(out, 1500))
	} else if !strings.Contains(out, "401") && !strings.Contains(strings.ToLower(out), "auth") {
		t.Errorf("envoys list with junk token: want a 401/auth error, got:\n%s", truncate(out, 1500))
	}

	// Creating a webuser that already exists → 409 conflict with an actionable
	// hint, not an opaque 500. The headline fix: a UNIQUE-constraint violation
	// used to collapse to `server returned 500: failed to create user`, leaving
	// the operator unable to tell it was their own duplicate.
	const dup = "clismoke-dupe-viewer"
	if _, err := runCLI(t, "webusers", "create", "--username", dup, "--role", "viewer", "--no-password"); err != nil {
		t.Fatalf("first webusers create (%s): %v", dup, err)
	}
	out, err = runCLI(t, "webusers", "create", "--username", dup, "--role", "viewer", "--no-password")
	if err == nil {
		t.Errorf("duplicate webusers create should fail, got success:\n%s", truncate(out, 1500))
	} else if !strings.Contains(out, "409") && !strings.Contains(strings.ToLower(out), "already exists") {
		t.Errorf("duplicate webusers create: want a 409/already-exists error, got:\n%s", truncate(out, 1500))
	}
}

// TestCLIConfigPublish proves `config publish --server-root` is relocatable:
// the CLI validates a stacks tree under an arbitrary root and writes .live +
// a config-history snapshot under that same root — never /srv/vigo. It runs
// against an isolated throwaway root (not the enrolled instance's), so it
// can't disturb the other tests; the only thing it borrows from the running
// server is the post-publish reload trigger (a harmless no-op, since the
// server reads its own root). This is the surface clismoke could not reach
// while publish was hardwired to /srv/vigo.
func TestCLIConfigPublish(t *testing.T) {
	root := t.TempDir()
	// Publish locks .live read-only (lockdownLiveDir). Registered after
	// t.TempDir(), this cleanup runs first (LIFO) and loosens the tree so
	// TempDir's RemoveAll can delete it.
	t.Cleanup(func() {
		_ = filepath.Walk(filepath.Join(root, ".live"), func(p string, _ os.FileInfo, _ error) error {
			_ = os.Chmod(p, 0o755)
			return nil
		})
	})
	stacks := filepath.Join(root, "stacks")
	for _, sub := range []string{"configcrates", "templates", "usercrates", "envoys", "roles"} {
		if err := os.MkdirAll(filepath.Join(stacks, sub), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	// A minimal valid tree: one configcrate referenced by one envoy match.
	writeStacksFile(t, stacks, "configcrates/nginx.vgo", "name: nginx\nresources:\n  - name: install-nginx\n    type: package\n    package: nginx\n    state: present\n")
	writeStacksFile(t, stacks, "envoys/web.vgo", "envoys:\n  - match: \"web.example.com\"\n    configcrates: [nginx]\n")

	// --skip-url-check: the fixture references no URLs, and offline is faster.
	out, err := runCLI(t, "config", "publish", "--server-root", root, "--skip-url-check")
	if err != nil {
		t.Fatalf("config publish --server-root %s failed: %v\n%s", root, err, truncate(out, 1500))
	}

	// LiveDir honored: the validated tree landed under <root>/.live.
	if _, err := os.Stat(filepath.Join(root, ".live", "configcrates", "nginx.vgo")); err != nil {
		t.Errorf("published .live not found under --server-root: %v\n%s", err, truncate(out, 1500))
	}
	// HistoryDir honored: a rollback snapshot was written under <root>/config-history.
	snaps, _ := filepath.Glob(filepath.Join(root, "config-history", "*.tar.gz"))
	if len(snaps) == 0 {
		t.Errorf("no config-history snapshot under --server-root after publish\n%s", truncate(out, 1500))
	}
}

// TestCLIPublishedConfigServed proves the tree fakefleet published via
// `config publish --server-root <tmp>` actually reached and is served by the
// running server — not merely written to disk and dropped by a warn-only reload
// failure. `config trace` resolves a host against the server's loaded config: a
// real published hostcrate yields a match with resolved resources, whereas an
// empty or unreloaded config prints "No config entry matches". This is the
// end-to-end signal that the relocated publish path round-trips.
func TestCLIPublishedConfigServed(t *testing.T) {
	out, err := runCLI(t, "config", "trace", "--host", sampleHostname)
	if err != nil {
		t.Fatalf("config trace --host %s failed: %v\n%s", sampleHostname, err, truncate(out, 1500))
	}
	if strings.Contains(out, "No config entry matches") {
		t.Fatalf("server is not serving the published config — host %q resolved to nothing:\n%s", sampleHostname, truncate(out, 1500))
	}
	if !strings.Contains(out, "Resources:") {
		t.Errorf("config trace for %q showed no resolved config (publish may not have reached the server):\n%s", sampleHostname, truncate(out, 1500))
	}
}

// ---------- helpers ----------

// writeStacksFile writes content to <stacks>/<rel>, creating parents.
func writeStacksFile(t *testing.T, stacks, rel, content string) {
	t.Helper()
	path := filepath.Join(stacks, rel)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

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

// runCLIStdout runs vigocli (as admin) capturing stdout separately from
// stderr, so machine-readable `--json` output parses without any log noise.
func runCLIStdout(t *testing.T, args ...string) (string, error) {
	t.Helper()
	full := append(append([]string{}, args...), "--server", srvURL)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, cliBin, full...)
	cmd.Env = cliEnv
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	err := cmd.Run()
	if err != nil && stderr.Len() > 0 {
		err = fmt.Errorf("%w\nstderr: %s", err, truncate(stderr.String(), 500))
	}
	return stdout.String(), err
}

// runCLIWithToken runs vigocli authenticated as the given API token rather than
// the admin principal — by pointing VIGO_CONFIG_DIR at a throwaway config dir
// holding it. Returns combined output so denial messages (printed to stderr)
// are visible to substring assertions.
func runCLIWithToken(t *testing.T, token string, args ...string) (string, error) {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "token"), []byte(token), 0o600); err != nil {
		t.Fatalf("write token: %v", err)
	}
	full := append(append([]string{}, args...), "--server", srvURL)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, cliBin, full...)
	cmd.Env = append(os.Environ(), "VIGO_CONFIG_DIR="+dir)
	out, err := cmd.CombinedOutput()
	return string(out), err
}

// mintRoleToken creates a no-password web user with the given role and an API
// token owned by it, returning the vgot_ plaintext. The token resolves to the
// owning user's role on the server, so it carries exactly that role's
// permissions — the lever the RBAC matrix exercises.
func mintRoleToken(t *testing.T, role, name string) string {
	t.Helper()
	user := name + "-user"
	if _, err := runCLI(t, "webusers", "create", "--username", user, "--role", role, "--no-password"); err != nil {
		t.Fatalf("create %s user: %v", role, err)
	}
	out, err := runCLI(t, "auth", "create-token", "--user", user, "--name", name)
	if err != nil {
		t.Fatalf("create-token for %s: %v\n%s", user, err, truncate(out, 1500))
	}
	for _, f := range strings.Fields(out) {
		if strings.HasPrefix(f, "vgot_") {
			return f
		}
	}
	t.Fatalf("no vgot_ token in create-token output:\n%s", out)
	return ""
}

// lineContains reports whether any single line of s contains all of subs —
// used to assert a table row carries an expected (username, value) pair
// without matching the substrings against different rows.
func lineContains(s string, subs ...string) bool {
	for _, line := range strings.Split(s, "\n") {
		all := true
		for _, sub := range subs {
			if !strings.Contains(line, sub) {
				all = false
				break
			}
		}
		if all {
			return true
		}
	}
	return false
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
			// Read the whole body: vigo_build_info sits well past the first 4 KB
			// of the (now much larger) /metrics output, so a single bounded Read
			// would never see it.
			body, _ := io.ReadAll(resp.Body)
			resp.Body.Close()
			if strings.Contains(string(body), "vigo_build_info") {
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
// ports with every writable path pointed inside tmp, plus an empty .live dir.
// The .live is not seeded — the server boots cleanly on an empty config tree
// (it tolerates an empty .live but errors on a missing one at startup), and
// fakefleet then publishes the real tree via `config publish --server-root tmp`
// before enrolling.
func writeFixture(tmp string, apiPort, grpcPort int) error {
	for _, d := range []string{
		filepath.Join(tmp, ".live"),
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
	return os.WriteFile(filepath.Join(tmp, "server.yaml"), []byte(yaml), 0o600)
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
