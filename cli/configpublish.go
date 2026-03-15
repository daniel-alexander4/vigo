// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/user"
	"strings"
	"time"

	"github.com/hobnob/hobnob/server/config"
	"github.com/hobnob/hobnob/server/publish"
	"github.com/spf13/cobra"
)

var configPublishCmd = &cobra.Command{
	Use:   "publish",
	Short: "Validate .hnb files in proposed/, sync to .live/, and trigger a server config reload",
	Long: fmt.Sprintf(`Validates all .hnb config files in %s using the same
logic as the server's config loader. If valid, atomically syncs to %s.

By default, all http/https URLs in changed modules are checked for aliveness.
Use --skip-url-check to disable (e.g., in air-gapped environments).

Use --dry-run to validate without publishing.`, publish.ProposedDir, publish.LiveDir),
	RunE: func(cmd *cobra.Command, args []string) error {
		dryRun, _ := cmd.Flags().GetBool("dry-run")
		skipURLCheck, _ := cmd.Flags().GetBool("skip-url-check")
		urlTimeout, _ := cmd.Flags().GetDuration("url-timeout")
		operator, _ := cmd.Flags().GetString("operator")
		showDiff, _ := cmd.Flags().GetBool("diff")
		askAI, _ := cmd.Flags().GetBool("ask-ai")
		filePath, _ := cmd.Flags().GetString("file")

		if operator == "" {
			if u, err := user.Current(); err == nil {
				operator = u.Username
			}
		}

		opts := publish.Options{
			CheckURLs:  !skipURLCheck,
			URLTimeout: urlTimeout,
			Operator:   operator,
			Diff:       showDiff,
		}

		// Single-file publish mode.
		if filePath != "" {
			if dryRun {
				return fmt.Errorf("--file and --dry-run cannot be combined")
			}
			res, err := publish.PublishFile(filePath, opts)
			if err != nil {
				suggestFixes(err, !askAI)
				return err
			}
			fmt.Printf("Published: %s\n", filePath)
			recordPublish(operator, res)
			reloadServer()
			return nil
		}

		if dryRun {
			res, err := publish.ValidateSource(opts)
			if err != nil {
				suggestFixes(err, !askAI)
				return err
			}
			printFileList(res)
			fmt.Printf("\nValidation passed: %d modules, %d roles, %d thrall entries\n",
				res.Modules, res.Roles, res.Entries)
			return nil
		}

		res, err := publish.Publish(opts)
		if err != nil {
			suggestFixes(err, !askAI)
			return err
		}
		printFileList(res)
		if res.Files == 0 && len(res.Changed) == 0 && len(res.Removed) == 0 {
			fmt.Println("\nNothing to publish — proposed matches live.")
			return nil
		}
		fmt.Printf("\nPublished: %d modules, %d roles, %d thrall entries (%d files synced)\n",
			res.Modules, res.Roles, res.Entries, res.Files)

		if showDiff && res.DiffSummary != "" {
			fmt.Println("\nDiff:")
			fmt.Println(res.DiffSummary)
		}

		recordPublish(operator, res)
		reloadServer()
		return nil
	},
}

// printFileList shows all examined files with status indicators:
//   (unchanged)  modules/nginx.hnb
//   (new)        modules/plex.hnb
//   (changed)    thralls/web.hnb
//   (removed)    roles/old.hnb
func printFileList(res *publish.Result) {
	changedSet := make(map[string]bool, len(res.Changed))
	for _, p := range res.Changed {
		changedSet[p] = true
	}

	// Detect new vs changed: new files don't exist in .live.
	// The Changed list includes both new and modified — we distinguish by
	// checking if it was also in AllFiles from a previous live snapshot.
	// Since we don't track live files separately, treat all Changed as "changed"
	// and show "(new)" only when the file wasn't in the previous live.

	for _, p := range res.AllFiles {
		if changedSet[p] {
			fmt.Printf("  %-12s %s\n", "(changed)", p)
		} else {
			fmt.Printf("  %-12s %s\n", "(ok)", p)
		}
	}
	for _, p := range res.Removed {
		fmt.Printf("  %-12s %s\n", "(removed)", p)
	}
}

// suggestFixes asks the AI assistant to suggest fixes for config errors.
// Handles both idempotency errors (with per-module YAML) and generic parse
// errors (extracts file path from error message).
func suggestFixes(err error, disabled bool) {
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
		b.WriteString("You are reviewing Hobnob .hnb config files that failed validation.\n")
		b.WriteString("For each error below, show the corrected YAML for just the failing resource.\n")
		b.WriteString("Use ```yaml code blocks. Be concise — fix only, no explanations.\n\n")

		for _, m := range ie.Modules {
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
		question = buildGenericFixPrompt(err)
	}

	if question == "" {
		return
	}

	askAI(question)
}

// buildGenericFixPrompt extracts file paths from error messages and builds
// an AI prompt with the error and file content.
func buildGenericFixPrompt(err error) string {
	errMsg := err.Error()

	// Try to find a file path in the error (e.g., "parsing /srv/hobnob/hnb/proposed/modules/foo.hnb: ...")
	var filePath string
	for _, part := range strings.Fields(errMsg) {
		if strings.HasSuffix(part, ".hnb") || strings.HasSuffix(part, ".hnb:") || strings.HasSuffix(part, ".yaml") || strings.HasSuffix(part, ".yaml:") {
			filePath = strings.TrimSuffix(part, ":")
			break
		}
	}

	var b strings.Builder
	b.WriteString("A Hobnob .hnb config file failed to load with this error:\n\n")
	fmt.Fprintf(&b, "  %s\n\n", errMsg)
	b.WriteString("Show the corrected YAML. Use ```yaml code blocks. Be concise — fix only, no explanations.\n\n")

	if filePath != "" {
		data, readErr := os.ReadFile(filePath)
		if readErr == nil {
			fmt.Fprintf(&b, "File: %s\n```yaml\n%s```\n", filePath, string(data))
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

// reloadServer tells the server to re-read config from .live/ (best-effort).
func reloadServer() {
	client := newAPIClient()
	if err := client.post("/api/v1/config/reload", nil, nil); err != nil {
		fmt.Fprintf(os.Stderr, "warning: server reload failed (%v) — restart the server to pick up changes\n", err)
	}
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

func init() {
	configPublishCmd.Flags().Bool("dry-run", false, "validate only, do not publish")
	configPublishCmd.Flags().Bool("skip-url-check", false, "skip HTTP aliveness checks for URLs")
	configPublishCmd.Flags().Duration("url-timeout", 10*time.Second, "timeout per URL check")
	configPublishCmd.Flags().String("operator", "", "operator name for audit trail (default: $USER)")
	configPublishCmd.Flags().Bool("diff", false, "show line-level diff for each changed file")
	configPublishCmd.Flags().Bool("ask-ai", false, "ask AI to suggest fixes for validation errors")
	configPublishCmd.Flags().String("file", "", "publish a single .hnb file (path relative to proposed/)")

	configCmd.AddCommand(configPublishCmd)
}
