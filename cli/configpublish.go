// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

package main

import (
	"bufio"
	"bytes"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"strings"
	"time"

	"git.alexander4.io/vigo/server/config"
	"git.alexander4.io/vigo/server/publish"
	"git.alexander4.io/vigo/server/puddle"
	"github.com/spf13/cobra"
)

var configPublishCmd = &cobra.Command{
	Use:   "publish",
	Short: "Validate .vgo files in stacks/, sync to .live/, and trigger a server config reload",
	Long: fmt.Sprintf(`Validates all .vgo config files in %s using the same
logic as the server's config loader. If valid, atomically syncs to %s.

By default, all http/https URLs in changed configcrates are checked for aliveness.
Use --skip-url-check to disable (e.g., in air-gapped environments).

Use --dry-run to validate without publishing.`, publish.DefaultStacksDir, publish.DefaultLiveDir),
	RunE: func(cmd *cobra.Command, args []string) error {
		dryRun, _ := cmd.Flags().GetBool("dry-run")
		skipURLCheck, _ := cmd.Flags().GetBool("skip-url-check")
		urlTimeout, _ := cmd.Flags().GetDuration("url-timeout")
		operator, _ := cmd.Flags().GetString("operator")
		showDiff, _ := cmd.Flags().GetBool("diff")
		askAI, _ := cmd.Flags().GetBool("ask-ai")
		filePath, _ := cmd.Flags().GetString("file")
		noPrune, _ := cmd.Flags().GetBool("no-prune")
		serverRoot, _ := cmd.Flags().GetString("server-root")

		if operator == "" {
			if u, err := user.Current(); err == nil {
				operator = u.Username
			}
		}

		// Anchor the three publish dirs on --server-root (default /srv/vigo),
		// mirroring the server's 0.75.8 serverRoot anchoring. PathsUnder is the
		// single SoT for the layout, so an ephemeral instance is fully
		// self-contained and never reads or writes the prod /srv/vigo tree.
		stacksDir, liveDir, historyDir := publish.PathsUnder(serverRoot)

		opts := publish.Options{
			StacksDir:  stacksDir,
			LiveDir:    liveDir,
			HistoryDir: historyDir,
			CheckURLs:  !skipURLCheck,
			URLTimeout: urlTimeout,
			Operator:   operator,
			Diff:       showDiff,
			NoPrune:    noPrune,
		}

		// Single-file publish mode.
		if filePath != "" {
			if dryRun {
				return fmt.Errorf("--file and --dry-run cannot be combined")
			}
			res, err := publish.PublishFile(filePath, opts, nil)
			if err != nil {
				suggestFixes(err, !askAI, opts)
				return err
			}
			fmt.Printf("Published: %s\n", filePath)
			recordPublish(operator, res)
			reloadServer(stacksDir)
			return nil
		}

		// Note: .live/ health is NOT checked here. ValidateSource() validates
		// the stacks and returns per-file errors in the Result. The old
		// checkLiveHealth() called NewConfigStore on .live/ which produced
		// uncontrollable slog output before the file list.

		if dryRun {
			res, err := publish.ValidateSource(opts)
			if err != nil {
				suggestFixes(err, !askAI, opts)
				return err
			}
			defer res.Cleanup()
			printFileList(res)
			if res.URLWarning != "" {
				fmt.Fprintf(os.Stderr, "\nwarning: %s\n", res.URLWarning)
			}
			fmt.Printf("\n%s Validation passed: %d configcrates, %d roles, %d envoy entries\n",
				green("✓"), res.Configcrates, res.Roles, res.Entries)
			return nil
		}

		res, err := publish.Publish(opts, nil)
		if err != nil {
			suggestFixes(err, !askAI, opts)
			return err
		}
		printFileList(res)
		if res.URLWarning != "" {
			fmt.Fprintf(os.Stderr, "\nwarning: %s\n", res.URLWarning)
		}
		if res.Files == 0 && len(res.Changed) == 0 && len(res.Removed) == 0 {
			fmt.Printf("\n%s Nothing to publish — .live/ matches stacks.", green("✓"))
			printPublishSummary(res)
			return nil
		}
		printPublishSummary(res)

		if showDiff && res.DiffSummary != "" {
			fmt.Println("\nDiff:")
			fmt.Println(res.DiffSummary)
		}

		recordPublish(operator, res)
		reloadServer(stacksDir)

		// Module retraction: generate retract modules for removed modules
		// (show affected envoys) and for new/changed modules (pre-build).
		if len(res.RemovedModuleContent) > 0 {
			handleRetraction(res, stacksDir)
		}
		if len(res.ChangedModuleContent) > 0 {
			handleRetractionPregen(res, stacksDir)
		}

		return nil
	},
}

// printFileList shows all examined files with color-coded status indicators.
// Validation errors are shown inline after the affected file.
func printFileList(res *publish.Result) {
	changedSet := make(map[string]bool, len(res.Changed))
	for _, p := range res.Changed {
		changedSet[p] = true
	}

	// Build error lookup: relative path → messages.
	// ValidationErrors use absolute paths; AllFiles use relative paths.
	errorMap := make(map[string][]string)
	for _, fe := range res.ValidationErrors {
		// Convert absolute path to relative by stripping the stacks prefix.
		rel := fe.Path
		if i := strings.Index(rel, "stacks/"); i >= 0 {
			rel = rel[i+len("stacks/"):]
		} else if i := strings.Index(rel, ".live/"); i >= 0 {
			rel = rel[i+len(".live/"):]
		}
		errorMap[rel] = fe.Messages
	}

	for _, p := range res.AllFiles {
		if msgs, hasErr := errorMap[p]; hasErr {
			fmt.Printf("  %s %s\n", red("(error)   "), p)
			for _, m := range msgs {
				fmt.Printf("             %s\n", red(m))
			}
		} else if changedSet[p] {
			fmt.Printf("  %s %s\n", yellow("(changed) "), p)
		} else {
			fmt.Printf("  %s %s\n", green("(unchanged)"), p)
		}
	}
	for _, p := range res.Removed {
		fmt.Printf("  %s %s\n", red("(removed) "), p)
	}
}

// printPublishSummary shows a color-coded summary of the publish result.
func printPublishSummary(res *publish.Result) {
	fmt.Println()

	errCount := len(res.ValidationErrors)
	ok := len(res.AllFiles) - len(res.Changed) - errCount

	if errCount > 0 {
		fmt.Printf("  %s  %d configcrate(s) with validation errors\n", red("✗"), errCount)
	}
	if res.Configcrates > 0 || res.Roles > 0 || res.Entries > 0 {
		fmt.Printf("  %s  %d configcrates, %d roles, %d envoy entries\n",
			bold("Summary"), res.Configcrates, res.Roles, res.Entries)
	}
	if ok > 0 {
		fmt.Printf("  %s  %d unchanged\n", green("✓"), ok)
	}
	if len(res.Changed) > 0 {
		fmt.Printf("  %s  %d changed\n", yellow("~"), len(res.Changed))
	}
	if len(res.Removed) > 0 {
		fmt.Printf("  %s  %d removed\n", red("✗"), len(res.Removed))
	}
	if len(res.Changed) > 0 || len(res.Removed) > 0 {
		fmt.Printf("  %s  published to .live/\n", green("→"))
	}
	fmt.Println()
}

// suggestFixes asks the AI assistant to suggest fixes for config errors.
// Handles both idempotency errors (with per-module YAML) and generic parse
// errors (extracts file path from error message). opts carries the resolved
// publish dirs so the generic path confines file reads to the active config
// tree (honoring --server-root), not the hardcoded /srv/vigo.
func suggestFixes(err error, disabled bool, opts publish.Options) {
	if disabled {
		return
	}

	var question string

	// Secret errors already include fix commands — no AI needed.
	var se *config.SecretErrors
	if errors.As(err, &se) {
		return
	}

	var ie *config.IdempotencyErrors
	if errors.As(err, &ie) {
		// Idempotency/validation errors — include each module's YAML.
		var b strings.Builder
		b.WriteString("You are reviewing Vigo .vgo config files that failed validation.\n")
		b.WriteString("For each error below, show the corrected YAML for just the failing resource.\n")
		b.WriteString("Use ```yaml code blocks. Be concise — fix only, no explanations.\n\n")

		for _, m := range ie.Configcrates {
			fmt.Fprintf(&b, "File: %s\n", m.Path)
			for _, f := range m.Findings {
				fmt.Fprintf(&b, "  Error: %s (%s): %s\n", f.ResourceName, f.ResourceType, f.Message)
			}
			data, readErr := os.ReadFile(m.Path)
			if readErr != nil {
				continue
			}
			fmt.Fprintf(&b, "\n```yaml\n%s```\n\n", string(data))
		}
		question = b.String()
	} else {
		// Generic error (YAML parse, missing fields, renamed keys, etc.)
		// Try to extract a file path from the error and include its content.
		question = buildGenericFixPrompt(err, opts)
	}

	if question == "" {
		return
	}

	askAI(question)
}

// buildGenericFixPrompt extracts file paths from error messages and builds
// an AI prompt with the error and file content.
func buildGenericFixPrompt(err error, opts publish.Options) string {
	errMsg := err.Error()

	// Try to find a file path in the error (e.g., "parsing /srv/vigo/stacks/configcrates/foo.vgo: ...")
	var filePath string
	for _, part := range strings.Fields(errMsg) {
		if strings.HasSuffix(part, ".vgo") || strings.HasSuffix(part, ".vgo:") || strings.HasSuffix(part, ".yaml") || strings.HasSuffix(part, ".yaml:") {
			filePath = strings.TrimSuffix(part, ":")
			break
		}
	}

	var b strings.Builder
	b.WriteString("A Vigo .vgo config file failed to load with this error:\n\n")
	fmt.Fprintf(&b, "  %s\n\n", errMsg)
	b.WriteString("Show the corrected YAML. Use ```yaml code blocks. Be concise — fix only, no explanations.\n\n")

	if filePath != "" {
		// Only read files within the active config tree to prevent information
		// disclosure. The roots come from opts (--server-root-resolved), not
		// the hardcoded defaults, so this stays correct off /srv/vigo.
		cleaned := filepath.Clean(filePath)
		if strings.HasPrefix(cleaned, opts.StacksDir) || strings.HasPrefix(cleaned, opts.LiveDir) {
			data, readErr := os.ReadFile(cleaned)
			if readErr == nil {
				fmt.Fprintf(&b, "File: %s\n```yaml\n%s```\n", cleaned, string(data))
			}
		}
	}

	return b.String()
}

// askAI sends a question to the server's AI endpoint and streams the response.
func askAI(question string) {
	body, jsonErr := json.Marshal(map[string]any{"question": question})
	if jsonErr != nil {
		return
	}

	fmt.Fprintf(os.Stderr, "\nAsking AI for fix suggestions...\n")

	req, reqErr := http.NewRequest("POST", serverURL+"/api/v1/ask", bytes.NewReader(body))
	if reqErr != nil {
		return
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "text/event-stream")

	client := &http.Client{Timeout: 2 * time.Minute}
	if isLocalhost(serverURL) {
		client.Transport = &http.Transport{
			TLSClientConfig: &tls.Config{InsecureSkipVerify: true},
		}
	}
	resp, doErr := client.Do(req)
	if doErr != nil {
		fmt.Fprintf(os.Stderr, "(AI unavailable — server not running or AI not enabled)\n")
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		fmt.Fprintf(os.Stderr, "(AI unavailable — server returned %d)\n", resp.StatusCode)
		return
	}

	fmt.Fprintln(os.Stderr)

	if strings.HasPrefix(resp.Header.Get("Content-Type"), "text/event-stream") {
		_ = streamPublishSSE(resp.Body)
	} else {
		var result struct {
			Answer string `json:"answer"`
		}
		if decErr := json.NewDecoder(resp.Body).Decode(&result); decErr == nil {
			fmt.Fprintln(os.Stderr, result.Answer)
		}
	}

	fmt.Fprintln(os.Stderr)
}

// streamPublishSSE writes AI delta text to stderr for publish suggestions.
func streamPublishSSE(body io.Reader) error {
	scanner := bufio.NewScanner(body)
	for scanner.Scan() {
		line := scanner.Text()
		if strings.HasPrefix(line, "data: ") {
			data := strings.TrimPrefix(line, "data: ")
			var event struct {
				Type string `json:"type"`
				Text string `json:"text,omitempty"`
			}
			if jsonErr := json.Unmarshal([]byte(data), &event); jsonErr != nil {
				continue
			}
			switch event.Type {
			case "delta":
				fmt.Fprint(os.Stderr, event.Text)
			case "error":
				return fmt.Errorf("AI error: %s", event.Text)
			case "done":
				fmt.Fprintln(os.Stderr)
				return nil
			}
		}
	}
	return scanner.Err()
}

// reloadServer dispatches the post-publish reload either through the
// legacy stacks→.live local-write + REST-reload path (standalone)
// or through the spanner-mode gitback push (which is what triggers
// the stacks watcher's bare→.live pull + reload). Detection is
// via /api/v1/spanner/status; failure to detect falls back to the
// legacy path so misconfigured deployments still get something. stacksDir is
// the --server-root-resolved source the spanner-mode mirror pushes from.
func reloadServer(stacksDir string) {
	client := newAPIClient()
	var status struct {
		Role string `json:"role"`
	}
	if err := client.get("/api/v1/spanner/status", &status); err != nil {
		// Can't even talk to the server — keep the legacy fallback so
		// operators see the same error they'd see today.
		legacyReloadServer()
		return
	}
	if status.Role == "" || status.Role == "standalone" {
		legacyReloadServer()
		return
	}
	if err := publishViaGitback(stacksDir); err != nil {
		fmt.Fprintf(os.Stderr, "warning: spanner gitback publish failed: %v\n", err)
	}
}

func legacyReloadServer() {
	client := newAPIClient()
	if err := client.post("/api/v1/config/reload", nil, nil); err != nil {
		fmt.Fprintf(os.Stderr, "warning: server reload failed (%v) — restart the server to pick up changes\n", err)
	}
}

// publishViaGitback is the spanner-mode publish path (ADR-025 Slice 4):
// clone the stacks bare repo into a temp work tree, mirror this
// vigosrv's on-disk stacks/ into it (delete-aware), git add+commit+
// push via file://, then poll the server's config version until the
// stacks watcher's bare→.live pull + Reload increments it. The
// version-poll is the operator-visible signal that the publish landed.
func publishViaGitback(stacksDir string) error {
	opsDir := puddle.OpsDefaultDir
	projectIDHex, err := publishLookupStacksProjectID(opsDir)
	if err != nil {
		return err
	}
	bareRepoPath := filepath.Join("/var/lib/vigo/swarm/gitback/projects", projectIDHex+".git")
	if _, err := os.Stat(bareRepoPath); err != nil {
		return fmt.Errorf("stacks bare repo missing at %s: %w (run `vigocli spanner stacks init` on the founding vigosrv first)", bareRepoPath, err)
	}

	preVersion, err := publishGetConfigVersion()
	if err != nil {
		return fmt.Errorf("reading pre-publish config version: %w", err)
	}

	workTree, err := os.MkdirTemp("", "vigo-publish-*")
	if err != nil {
		return fmt.Errorf("creating temp work tree: %w", err)
	}
	defer os.RemoveAll(workTree)

	if err := publishRunGit("", "clone", bareRepoPath, workTree); err != nil {
		return fmt.Errorf("cloning bare repo: %w", err)
	}

	// Mirror the (--server-root-resolved) stacks tree → workTree (delete-aware).
	if err := publishMirrorIntoWorkTree(stacksDir, workTree); err != nil {
		return fmt.Errorf("mirroring stacks into work tree: %w", err)
	}

	// Configure committer identity (gitback verifies on signature, not
	// these fields, but git refuses to commit without something here).
	for _, kv := range [][]string{
		{"user.name", "vigo config publish"},
		{"user.email", "publish@vigo.local"},
	} {
		if err := publishRunGit(workTree, "config", kv[0], kv[1]); err != nil {
			return fmt.Errorf("git config %s: %w", kv[0], err)
		}
	}
	if err := publishRunGit(workTree, "add", "-A", "."); err != nil {
		return fmt.Errorf("git add -A: %w", err)
	}
	// Anything staged? `diff --cached --quiet` exits 0 when there is
	// nothing to commit, non-zero otherwise. Exit 0 = nothing to push.
	if err := publishRunGit(workTree, "diff", "--cached", "--quiet"); err == nil {
		fmt.Println("Nothing to publish — bare repo already matches stacks.")
		return nil
	}
	if err := publishRunGit(workTree, "commit", "-m", "publish"); err != nil {
		return fmt.Errorf("git commit: %w", err)
	}
	if err := publishRunGit(workTree, "push", "origin", "main"); err != nil {
		return fmt.Errorf("git push to bare repo: %w", err)
	}

	// Poll for the watcher to land the bare→.live pull + reload. The
	// debounce is 250ms; reload itself is sub-100ms typical. 30s is
	// generous for any reasonable stacks size.
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		v, err := publishGetConfigVersion()
		if err == nil && v > preVersion {
			fmt.Printf("Published; watcher confirmed reload (config version %d → %d).\n", preVersion, v)
			return nil
		}
		time.Sleep(250 * time.Millisecond)
	}
	return fmt.Errorf("watcher did not confirm reload within 30s (pre-version %d); check `journalctl -u vigosrv` for stackswatch errors", preVersion)
}

func publishGetConfigVersion() (uint64, error) {
	client := newAPIClient()
	var resp struct {
		Version uint64 `json:"version"`
	}
	if err := client.get("/api/v1/config/version", &resp); err != nil {
		return 0, err
	}
	return resp.Version, nil
}

// publishLookupStacksProjectID walks <opsDir>/.vigo-gitback/<id>/
// project.json files for the entry whose handle is "stacks".
// Mirrors server/main.go's lookupStacksProjectID — duplicated
// rather than factored to keep cli/ free of imports from server/main.
func publishLookupStacksProjectID(opsDir string) (string, error) {
	gitbackDir := filepath.Join(opsDir, ".vigo-gitback")
	entries, err := os.ReadDir(gitbackDir)
	if err != nil {
		return "", fmt.Errorf("reading %s: %w", gitbackDir, err)
	}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		configPath := filepath.Join(gitbackDir, e.Name(), "project.json")
		data, err := os.ReadFile(configPath)
		if err != nil {
			continue
		}
		var cfg struct {
			Handle string `json:"handle"`
		}
		if err := json.Unmarshal(data, &cfg); err != nil {
			continue
		}
		if cfg.Handle == "stacks" {
			return e.Name(), nil
		}
	}
	return "", fmt.Errorf("no stacks project state under %s — run `vigocli spanner stacks init` (founder) or `spanner stacks join` (joiner)", gitbackDir)
}

// publishMirrorIntoWorkTree clears src out of workTree (preserving
// .git/) then copies fresh src/ contents in. After this call the work
// tree's HEAD vs disk diff reflects exactly the stacks→bare delta.
func publishMirrorIntoWorkTree(src, workTree string) error {
	// Step 1: remove every top-level entry in workTree except .git.
	entries, err := os.ReadDir(workTree)
	if err != nil {
		return err
	}
	for _, e := range entries {
		if e.Name() == ".git" {
			continue
		}
		if err := os.RemoveAll(filepath.Join(workTree, e.Name())); err != nil {
			return err
		}
	}
	// Step 2: copy src/* into workTree.
	return filepath.Walk(src, func(path string, info os.FileInfo, err error) error {
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
		if rel == ".git" && info.IsDir() {
			return filepath.SkipDir
		}
		target := filepath.Join(workTree, rel)
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
			_, err = io.Copy(out, in)
			return err
		}
		return nil
	})
}

// publishRunGit is the publish-flow's git invocation helper —
// parallel to spanner_stacks.go's runGit; duplicated to keep this
// file self-contained.
func publishRunGit(dir string, args ...string) error {
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

// recordPublish sends publish metadata to the server for audit (best-effort).
func recordPublish(operator string, res *publish.Result) {
	client := newAPIClient()
	_ = client.post("/api/v1/config/publishes", map[string]any{
		"operator":      operator,
		"changed_files": res.Changed,
		"removed_files": res.Removed,
		"diff_summary":  res.DiffSummary,
	}, nil)
}

// checkLiveHealth was removed — it called NewConfigStore on .live/ which
// produced uncontrollable slog output. Validation errors are now captured
// in the Result from ValidateSource() and displayed inline in the file list.

// --- Module Retraction ---

// retractAPIResponse matches the server's retract endpoint response.
type retractAPIResponse struct {
	Enabled bool               `json:"enabled"`
	Reason  string             `json:"reason,omitempty"`
	Results []retractAPIResult `json:"results,omitempty"`
}

type retractAPIResult struct {
	Name        string `json:"name"`
	RetractYAML string `json:"retract_yaml"`
	Warnings    []struct {
		Resource   string `json:"resource"`
		Type       string `json:"type"`
		Confidence string `json:"confidence"`
		Message    string `json:"message"`
	} `json:"warnings"`
	Envoys []struct {
		Hostname string `json:"hostname"`
		LastRun  string `json:"last_run"`
	} `json:"envoys"`
}

// handleRetraction calls the server's retract endpoint for removed modules,
// writes .retract files, and prints a summary for the operator. stacksDir is
// the --server-root-resolved tree the .retract files are written into.
func handleRetraction(res *publish.Result, stacksDir string) {
	// Build request with removed module names and content.
	type modInput struct {
		Name    string `json:"name"`
		Content string `json:"content"`
	}
	var modules []modInput
	for rel, content := range res.RemovedModuleContent {
		// Extract configcrate name from path: "configcrates/nginx.vgo" → "nginx"
		base := filepath.Base(rel)
		name := strings.TrimSuffix(base, filepath.Ext(base))
		modules = append(modules, modInput{Name: name, Content: string(content)})
	}

	client := newAPIClient()
	var resp retractAPIResponse
	err := client.post("/api/v1/config/retract", map[string]any{"configcrates": modules}, &resp)
	if err != nil {
		// Server unreachable or error — not fatal, just skip retraction.
		return
	}

	if !resp.Enabled {
		return
	}

	if len(resp.Results) == 0 {
		return
	}

	fmt.Println()
	for _, result := range resp.Results {
		printRetractionResult(result, stacksDir)
	}
}

// handleRetractionPregen calls the server's retract endpoint for new/changed
// modules to pre-generate .retract files. These sit alongside the modules,
// ready for use if the module is later removed from a node's config.
func handleRetractionPregen(res *publish.Result, stacksDir string) {
	type modInput struct {
		Name    string `json:"name"`
		Content string `json:"content"`
	}
	var modules []modInput
	for rel, content := range res.ChangedModuleContent {
		base := filepath.Base(rel)
		name := strings.TrimSuffix(base, filepath.Ext(base))
		modules = append(modules, modInput{Name: name, Content: string(content)})
	}

	client := newAPIClient()
	var resp retractAPIResponse
	if err := client.post("/api/v1/config/retract", map[string]any{"configcrates": modules}, &resp); err != nil {
		return
	}
	if !resp.Enabled || len(resp.Results) == 0 {
		return
	}

	var generated int
	for _, result := range resp.Results {
		if result.RetractYAML == "" {
			continue
		}
		retractPath := filepath.Join(stacksDir, "configcrates", result.Name+".retract")
		if err := os.WriteFile(retractPath, []byte(result.RetractYAML), 0644); err != nil {
			fmt.Fprintf(os.Stderr, "warning: failed to write %s: %v\n", retractPath, err)
			continue
		}
		generated++
	}
	if generated > 0 {
		fmt.Printf("\n%s Generated %d retract configcrate(s) in stacks/configcrates/\n",
			green("✓"), generated)
	}
}

func printRetractionResult(result retractAPIResult, stacksDir string) {
	// Write the .retract file to the active stacks tree's configcrates/.
	retractPath := filepath.Join(stacksDir, "configcrates", result.Name+".retract")
	if result.RetractYAML != "" {
		if err := os.WriteFile(retractPath, []byte(result.RetractYAML), 0644); err != nil {
			fmt.Fprintf(os.Stderr, "warning: failed to write %s: %v\n", retractPath, err)
			return
		}
	}

	fmt.Printf("%s Configcrate removed: %s%s\n", yellow("⚠"), bold(result.Name), cReset())

	// Show affected envoys.
	if len(result.Envoys) > 0 {
		fmt.Printf("  Previously applied to %d envoy(s):\n", len(result.Envoys))
		for _, e := range result.Envoys {
			lastRun := e.LastRun
			if t, err := time.Parse(time.RFC3339, e.LastRun); err == nil {
				lastRun = formatDuration(time.Since(t)) + " ago"
			} else if t, err := time.Parse("2006-01-02T15:04:05Z", e.LastRun); err == nil {
				lastRun = formatDuration(time.Since(t)) + " ago"
			}
			fmt.Printf("    %s %s %s\n", green("•"), e.Hostname, dim("(last converged "+lastRun+")"))
		}
	} else {
		fmt.Printf("  %s\n", dim("No envoys found with this configcrate in run history."))
	}

	// Show generated file.
	if result.RetractYAML != "" {
		fmt.Printf("\n  Generated: %s\n", green(retractPath))
	}

	// Show per-resource warnings.
	fmt.Println()
	fmt.Printf("    %s\n", yellow("⚠ REVIEW BEFORE ACTIVATING — rename to .vgo when ready"))
	fmt.Println()
	for _, w := range result.Warnings {
		var icon, label string
		switch w.Confidence {
		case "high":
			icon = green("✓")
			label = w.Message
		case "medium":
			icon = yellow("~")
			label = w.Message
		case "low":
			icon = red("⚠")
			label = w.Message + " — VERIFY"
		case "skip":
			icon = dim("–")
			label = w.Message
		default:
			icon = dim("?")
			label = w.Message
		}
		if w.Resource != "" {
			fmt.Printf("      %s %s: %s %s\n", icon, bold(w.Type), w.Resource, dim("("+label+")"))
		} else {
			fmt.Printf("      %s %s\n", icon, label)
		}
	}

	// Show instructions.
	fmt.Println()
	fmt.Println("  To retract:")
	fmt.Printf("    1. Review %s\n", retractPath)
	fmt.Printf("    2. Rename to %s\n", green(result.Name+"-retract.vgo"))
	fmt.Printf("    3. Add configcrate %s to the relevant match blocks\n", bold("\""+result.Name+"-retract\""))
	fmt.Println("    4. Run: vigocli config publish")
	fmt.Println("    5. After all envoys converge, remove the configcrate and entry")
	fmt.Println()
}

// formatDuration returns a human-readable short duration like "2h", "3d", "45m".
func formatDuration(d time.Duration) string {
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh", int(d.Hours()))
	default:
		return fmt.Sprintf("%dd", int(d.Hours()/24))
	}
}

func init() {
	configPublishCmd.Flags().Bool("dry-run", false, "validate only, do not publish")
	configPublishCmd.Flags().Bool("skip-url-check", false, "skip HTTP aliveness checks for URLs")
	configPublishCmd.Flags().Duration("url-timeout", 10*time.Second, "timeout per URL check")
	configPublishCmd.Flags().String("operator", "", "operator name for audit trail (default: $USER)")
	configPublishCmd.Flags().Bool("diff", false, "show line-level diff for each changed file")
	configPublishCmd.Flags().Bool("ask-ai", false, "ask AI to suggest fixes for validation errors")
	configPublishCmd.Flags().String("file", "", "publish a single .vgo file (path relative to stacks/)")
	configPublishCmd.Flags().Bool("no-prune", false, "publish every file in stacks/ verbatim — skip the reachability prune that drops configcrates, usercrates, and templates no carrier references")
	configPublishCmd.Flags().String("server-root", publish.DefaultServerRoot, "server root holding stacks/, .live/, config-history/ — override to publish a non-default instance")

	configCmd.AddCommand(configPublishCmd)
}
