// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: AGPL-3.0-only

package main

import (
	"bufio"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"git.alexander4.io/vigo/server/puddle"
	"github.com/spf13/cobra"
)

// Stacks commands (ADR-025 Slice 3). Today: founder-side `init`
// only — creates the ops-puddle-owned stacks gitback project on
// this vigosrv, then auto-imports the operator's existing on-disk
// stacks content as the first commit. The joining-vigosrv flow
// + cross-vigosrv distribution land in Slice 4.

var spannerStacksCmd = &cobra.Command{
	Use:   "stacks",
	Short: "Manage the stacks gitback project owned by the ops-puddle",
	Long: `The stacks project is the gitback project that holds the operator's
config tree (stacks/) and distributes it across every vigosrv
in the spanner via the swarm substrate. Founded once on a single vigosrv
(usually the hub), then joined from each additional ops-puddle member.`,
}

var spannerStacksInitCmd = &cobra.Command{
	Use:   "init",
	Short: "Found the stacks gitback project on this vigosrv and auto-import the current stacks",
	Long: `Calls the service-account puddle helper at /var/run/vigo/puddle.sock
to mint a new gitback project owned by the ops-puddle (handle
"stacks", scope members-only), then auto-imports this vigosrv's
existing on-disk stacks content as the first commit.

Preconditions:
  - spanner.role != standalone (standalone uses local-write + SIGHUP)
  - ops-puddle exists at --ops-puddle-dir (run 'spanner ops-puddle init' first)
  - swarm.gitback machinery available on this host

The project state lands under <ops-puddle-dir>/.vigo-gitback/<project-id>/
and the bare repo at /var/lib/vigo/swarm/gitback/projects/<project-id>.git.`,
	RunE: runSpannerStacksInit,
}

var spannerStacksJoinCmd = &cobra.Command{
	Use:   "join",
	Short: "Create local stacks gitback project state on a vigosrv that joined the ops-puddle",
	Long: `For joining vigosrvs (post-ops-puddle-join). Calls the helper's
FOUND_OPS_PROJECT to create local project state for the stacks
project — the project_id is deterministic (sha256(ops-puddle pubkey,
"stacks")) so every member computes the same id and chains all
verify against the shared ops-puddle pubkey.

After state lands, the existing gitback catch-up reactor pulls the
bytes from any reachable peer (LAN multicast discovery or
cross_subnet_members). The stacks watcher then pulls bare→.live
and triggers configStore.Reload().

Preconditions:
  - spanner.role != standalone
  - ops-puddle exists locally (joined via 'spanner ops-puddle join')
  - no local stacks state yet`,
	RunE: runSpannerStacksJoin,
}

func init() {
	spannerStacksInitCmd.Flags().String(
		"source",
		"/srv/vigo/stacks",
		"Source directory whose contents are auto-imported as the first commit",
	)
	spannerStacksInitCmd.Flags().String(
		"helper-socket",
		"/var/run/vigo/puddle.sock",
		"Path to the service-account puddle helper Unix socket",
	)
	spannerStacksInitCmd.Flags().String(
		"gitback-base",
		"/var/lib/vigo/swarm/gitback",
		"Base dir for gitback bare repos (the bare repo lands at <base>/projects/<id>.git)",
	)

	spannerCmd.AddCommand(spannerStacksCmd)
	spannerStacksCmd.AddCommand(spannerStacksInitCmd)
	spannerStacksCmd.AddCommand(spannerStacksJoinCmd)

	// Init + join both write ops-puddle-derived state on disk — same
	// operator-side gate as the other ops-puddle write verbs.
	spannerStacksInitCmd.PreRunE = requireUnlockedAlways
	spannerStacksJoinCmd.PreRunE = requireUnlockedAlways
}

// stacksHandle is the fixed handle for the stacks gitback project.
// Operators don't name it; there's exactly one per spanner.
const stacksHandle = "stacks"

// stacksScope is fixed at members-only per ADR-025 Slice 3 decision.
// Non-ops-puddle envoys don't hold stacks bytes; their policy comes
// through the standard check-in gRPC stream from their bolt.
const stacksScope = "members-only"

func runSpannerStacksInit(cmd *cobra.Command, _ []string) error {
	opsDir, _ := cmd.Flags().GetString("ops-puddle-dir")
	source, _ := cmd.Flags().GetString("source")
	socketPath, _ := cmd.Flags().GetString("helper-socket")
	gitbackBase, _ := cmd.Flags().GetString("gitback-base")

	// Precondition: ops-puddle exists locally.
	opsPubkeyPath := puddle.PubkeyPath(opsDir)
	if _, err := os.Stat(opsPubkeyPath); err != nil {
		return fmt.Errorf(
			"no ops-puddle at %s: %w (run `vigocli spanner ops-puddle init <name>` first, or `join` to claim one)",
			opsDir, err,
		)
	}
	friendlyNameBytes, err := os.ReadFile(filepath.Join(opsDir, puddle.FriendlyNameFile))
	if err != nil {
		return fmt.Errorf("reading ops-puddle friendly name: %w", err)
	}
	friendlyName := strings.TrimRight(string(friendlyNameBytes), "\n")

	// Precondition: spanner role isn't standalone (check via REST against
	// the local vigosrv). Failure to reach the server isn't fatal —
	// standalone deployments may not even have the REST API up the way
	// vigocli expects; surface a clear error rather than silently doing
	// the wrong thing.
	client := newAPIClient()
	var status struct {
		Mode string `json:"mode"`
	}
	if err := client.get("/api/v1/spanner/status", &status); err != nil {
		return fmt.Errorf(
			"cannot reach local vigosrv to check spanner mode: %w (the stacks project is for spanner-mode deployments only — standalone keeps local-write + SIGHUP)",
			err,
		)
	}
	if status.Mode == "" || status.Mode == "standalone" {
		return fmt.Errorf(
			"this vigosrv is %q — the stacks gitback project is only for spanner-mode deployments (mode: spanner)",
			status.Mode,
		)
	}

	// Precondition: source dir exists + has content worth committing.
	srcInfo, err := os.Stat(source)
	if err != nil {
		return fmt.Errorf("source dir %s: %w", source, err)
	}
	if !srcInfo.IsDir() {
		return fmt.Errorf("source %s is not a directory", source)
	}

	// Found the project via the helper. The helper unwraps the ops-puddle
	// on first OPS_* call and runs the existing gitback founder-bootstrap
	// path under an in-process signer closure (ADR-025 Slice 1b + 3).
	projectIDHex, err := callHelper(
		socketPath,
		fmt.Sprintf("FOUND_OPS_PROJECT %s %s", stacksHandle, stacksScope),
	)
	if err != nil {
		return fmt.Errorf("founding ops-puddle gitback project: %w", err)
	}
	projectIDHex = strings.TrimSpace(projectIDHex)
	bareRepoPath := filepath.Join(gitbackBase, "projects", projectIDHex+".git")

	// Auto-import: copy source → fresh work tree → git init/add/commit → push
	// directly to the bare repo via file://. The push triggers gitback's
	// post-receive hook (which fans out to peers — no-op at init time
	// since no other ops-puddle member has the project state yet) and
	// the new watcher in server/stackswatch/ which calls
	// configStore.Reload(). Local file:// push avoids needing the
	// per-user git-remote-gitback path; gitback auth applies to network
	// pushes, not in-process ones into a bare repo this vigosrv owns.
	commitCount, byteCount, err := autoImport(source, bareRepoPath, friendlyName)
	if err != nil {
		return fmt.Errorf("auto-import from %s: %w", source, err)
	}

	gitbackURL := fmt.Sprintf("gitback://%s/%s", friendlyName, stacksHandle)
	fmt.Printf("Stacks project founded\n")
	fmt.Printf("  URL:           %s\n", gitbackURL)
	fmt.Printf("  Project ID:    %s\n", projectIDHex)
	fmt.Printf("  Source:        %s\n", source)
	fmt.Printf("  Bare repo:     %s\n", bareRepoPath)
	fmt.Printf("  Auto-imported: %d commit, %d bytes\n", commitCount, byteCount)
	fmt.Println()
	fmt.Printf("Other ops-puddle members will receive the stacks after the next push\n")
	fmt.Printf("(landing in Slice 4 — today's init seeds the founder side only).\n")
	return nil
}

// callHelper sends a single line to the helper Unix socket and reads
// the single-line response. Returns the OK payload (sans "OK " prefix)
// or an error if the helper returned ERR or the connection failed.
func callHelper(socketPath, line string) (string, error) {
	conn, err := net.DialTimeout("unix", socketPath, 5*time.Second)
	if err != nil {
		return "", fmt.Errorf("connecting to helper at %s: %w (is the agent running with /srv/vigo/puddle/identity.wrapped on disk?)", socketPath, err)
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(30 * time.Second))

	if _, err := io.WriteString(conn, line+"\n"); err != nil {
		return "", fmt.Errorf("sending request: %w", err)
	}
	resp, err := bufio.NewReader(conn).ReadString('\n')
	if err != nil && err != io.EOF {
		return "", fmt.Errorf("reading response: %w", err)
	}
	resp = strings.TrimRight(resp, "\n")
	if strings.HasPrefix(resp, "OK ") {
		return strings.TrimPrefix(resp, "OK "), nil
	}
	if resp == "OK" {
		return "", nil
	}
	if strings.HasPrefix(resp, "ERR ") {
		return "", fmt.Errorf("helper: %s", strings.TrimPrefix(resp, "ERR "))
	}
	return "", fmt.Errorf("unexpected helper response: %q", resp)
}

// autoImport stages source/ into a temp work tree, runs the standard
// git init/add/commit + file:// push to bareRepoPath, and returns
// (commit_count, bytes_in_source).
func autoImport(source, bareRepoPath, opsFriendlyName string) (int, int64, error) {
	// Verify bare repo exists. The helper's FOUND_OPS_PROJECT creates
	// project state but the bare repo is materialized by gitback's
	// helper-daemon on first push — so we ensure it here for the
	// initial seed. `git init --bare` is idempotent.
	if err := os.MkdirAll(filepath.Dir(bareRepoPath), 0o700); err != nil {
		return 0, 0, fmt.Errorf("creating projects parent dir: %w", err)
	}
	if err := runGit("", "init", "--bare", bareRepoPath); err != nil {
		return 0, 0, fmt.Errorf("git init --bare %s: %w", bareRepoPath, err)
	}

	// Stage the source into a temp work tree. Using a tempdir keeps us
	// from contaminating the source with .git/ — the operator's
	// stacks/ tree stays a plain directory, untouched.
	workTree, err := os.MkdirTemp("", "vigo-stacks-init-*")
	if err != nil {
		return 0, 0, fmt.Errorf("creating work tree: %w", err)
	}
	defer os.RemoveAll(workTree)

	bytes, err := copyTree(source, workTree)
	if err != nil {
		return 0, 0, fmt.Errorf("copying %s → work tree: %w", source, err)
	}

	if err := runGit(workTree, "init", "--initial-branch=main"); err != nil {
		return 0, 0, fmt.Errorf("git init: %w", err)
	}
	// Author/committer identity: bind to the ops-puddle's friendly name
	// so the commit log makes the provenance obvious. Email is a
	// non-routable marker — gitback doesn't use it for identity (puddle
	// pubkeys do that).
	for _, kv := range [][]string{
		{"user.name", fmt.Sprintf("vigo ops-puddle: %s", opsFriendlyName)},
		{"user.email", fmt.Sprintf("ops-puddle@%s.spanner.local", opsFriendlyName)},
	} {
		if err := runGit(workTree, "config", kv[0], kv[1]); err != nil {
			return 0, 0, fmt.Errorf("git config %s: %w", kv[0], err)
		}
	}
	if err := runGit(workTree, "add", "."); err != nil {
		return 0, 0, fmt.Errorf("git add: %w", err)
	}
	if err := runGit(workTree, "commit", "-m", "stacks init"); err != nil {
		return 0, 0, fmt.Errorf("git commit: %w", err)
	}
	// Push to the bare repo via file://. Bypasses the gitback network
	// auth (we own the bare repo on disk; no cross-envoy push happens
	// here). The post-receive hook in the bare repo fires regardless,
	// which is what triggers stacks-watch + downstream fan-out once
	// other vigosrvs have joined.
	if err := runGit(workTree, "push", "file://"+bareRepoPath, "main"); err != nil {
		return 0, 0, fmt.Errorf("git push to bare repo: %w", err)
	}
	return 1, bytes, nil
}

// copyTree recursively copies src → dst, skipping .git/ if it exists
// at src's root (operators sometimes have a working .git/ from a prior
// VCS, which would conflict with our fresh init). Returns total bytes
// copied (for the import summary line).
func copyTree(src, dst string) (int64, error) {
	var total int64
	err := filepath.Walk(src, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		if rel == "." {
			return nil
		}
		// Skip an existing .git/ at the source root.
		if rel == ".git" && info.IsDir() {
			return filepath.SkipDir
		}
		target := filepath.Join(dst, rel)
		if info.IsDir() {
			return os.MkdirAll(target, info.Mode().Perm())
		}
		if info.Mode().IsRegular() {
			in, err := os.Open(path)
			if err != nil {
				return err
			}
			defer in.Close()
			out, err := os.OpenFile(target, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, info.Mode().Perm())
			if err != nil {
				return err
			}
			defer out.Close()
			n, err := io.Copy(out, in)
			total += n
			return err
		}
		// Symlinks, devices, etc.: skip silently — stacks is plain files.
		return nil
	})
	return total, err
}

func runGit(dir string, args ...string) error {
	cmd := exec.Command("git", args...)
	if dir != "" {
		cmd.Dir = dir
	}
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("git %s: %w (output: %s)", strings.Join(args, " "), err, strings.TrimSpace(string(out)))
	}
	return nil
}

func runSpannerStacksJoin(cmd *cobra.Command, _ []string) error {
	opsDir, _ := cmd.Flags().GetString("ops-puddle-dir")
	socketPath, _ := cmd.Flags().GetString("helper-socket")

	// Precondition: ops-puddle exists locally.
	opsPubkeyPath := puddle.PubkeyPath(opsDir)
	if _, err := os.Stat(opsPubkeyPath); err != nil {
		return fmt.Errorf(
			"no ops-puddle at %s: %w (run `vigocli spanner ops-puddle join --from <existing-member> --code <code>` first)",
			opsDir, err,
		)
	}

	// Precondition: spanner mode (not standalone).
	client := newAPIClient()
	var status struct {
		Mode string `json:"mode"`
	}
	if err := client.get("/api/v1/spanner/status", &status); err != nil {
		return fmt.Errorf("cannot reach local vigosrv to check spanner mode: %w", err)
	}
	if status.Mode == "" || status.Mode == "standalone" {
		return fmt.Errorf(
			"this vigosrv is %q — stacks join only applies in spanner mode",
			status.Mode,
		)
	}

	// Call FOUND_OPS_PROJECT. project_id is deterministic from
	// (ops-puddle pubkey, "stacks") so the joiner ends up with
	// the same id the founder used; both chains verify against the
	// shared ops-puddle pubkey.
	projectIDHex, err := callHelper(
		socketPath,
		fmt.Sprintf("FOUND_OPS_PROJECT %s %s", stacksHandle, stacksScope),
	)
	if err != nil {
		// If the project state already exists locally the helper
		// surfaces an "already exists" error from mint_founder_project;
		// surface that distinctly so operators don't re-run pointlessly.
		if strings.Contains(err.Error(), "already exists") {
			return fmt.Errorf("stacks project already locally tracked; nothing to join")
		}
		return fmt.Errorf("creating local stacks project state: %w", err)
	}
	projectIDHex = strings.TrimSpace(projectIDHex)

	fmt.Printf("Stacks project state created\n")
	fmt.Printf("  Project ID:    %s\n", projectIDHex)
	fmt.Printf("  State dir:     %s/.vigo-gitback/%s/\n", opsDir, projectIDHex)
	fmt.Println()
	fmt.Printf("The gitback catch-up reactor will pull stacks bytes from a\n")
	fmt.Printf("reachable ops-puddle member shortly (LAN multicast or cross-\n")
	fmt.Printf("subnet members). The stacks watcher syncs bare→.live and\n")
	fmt.Printf("reloads on arrival.\n")
	return nil
}
