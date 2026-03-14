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
	Short: "Validate and publish config from proposed to live",
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
		skipSuggest, _ := cmd.Flags().GetBool("skip-suggest")
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
				suggestFixes(err, skipSuggest)
				return err
			}
			fmt.Printf("Published: %s\n", filePath)
			recordPublish(operator, res)
			return nil
		}

		if dryRun {
			res, err := publish.ValidateSource(opts)
			if err != nil {
				suggestFixes(err, skipSuggest)
				return fmt.Errorf("validation failed:\n  %v", err)
			}
			printFileList(res)
			fmt.Printf("\nValidation passed: %d modules, %d roles, %d thrall entries\n",
				res.Modules, res.Roles, res.Entries)
			return nil
		}

		res, err := publish.Publish(opts)
		if err != nil {
			suggestFixes(err, skipSuggest)
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

// suggestFixes checks if the error contains validation findings and, if so,
// asks the AI assistant to suggest fixes. Requires the server to be running.
func suggestFixes(err error, disabled bool) {
	if disabled {
		return
	}

	var ie *config.IdempotencyErrors
	if !errors.As(err, &ie) {
		return
	}

	// Build a question with the validation errors and the failing module YAML.
	var b strings.Builder
	b.WriteString("The following Hobnob config validation errors must be fixed before publishing.\n")
	b.WriteString("For each error, explain what is wrong and show the corrected YAML.\n\n")
	b.WriteString("Validation errors:\n")
	b.WriteString(ie.Error())
	b.WriteString("\n")

	for _, m := range ie.Modules {
		data, readErr := os.ReadFile(m.Path)
		if readErr != nil {
			continue
		}
		fmt.Fprintf(&b, "--- %s ---\n%s\n\n", m.Path, string(data))
	}

	question := b.String()

	// Call the AI endpoint (best-effort — skip silently if server is not running).
	body, jsonErr := json.Marshal(map[string]any{"question": question})
	if jsonErr != nil {
		return
	}

	req, reqErr := http.NewRequest("POST", serverURL+"/api/v1/ask", bytes.NewReader(body))
	if reqErr != nil {
		return
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "text/event-stream")

	client := &http.Client{Timeout: 2 * time.Minute}
	resp, doErr := client.Do(req)
	if doErr != nil {
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return
	}

	fmt.Fprintf(os.Stderr, "\n--- AI suggestions ---\n\n")

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

	fmt.Fprintf(os.Stderr, "\n--- end suggestions ---\n")
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
	configPublishCmd.Flags().Bool("skip-suggest", false, "skip AI-assisted fix suggestions on validation failure")
	configPublishCmd.Flags().String("file", "", "publish a single .hnb file (path relative to proposed/)")

	configCmd.AddCommand(configPublishCmd)
}
