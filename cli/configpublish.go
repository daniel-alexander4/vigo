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
	"os/user"
	"path/filepath"
	"strings"
	"time"

	"git.alexander4.io/vigo/server/config"
	"git.alexander4.io/vigo/server/publish"
	"github.com/spf13/cobra"
)

var configPublishCmd = &cobra.Command{
	Use:   "publish",
	Short: "Validate .vgo files in stockpile/, sync to .live/, and trigger a server config reload",
	Long: fmt.Sprintf(`Validates all .vgo config files in %s using the same
logic as the server's config loader. If valid, atomically syncs to %s.

By default, all http/https URLs in changed modules are checked for aliveness.
Use --skip-url-check to disable (e.g., in air-gapped environments).

Use --dry-run to validate without publishing.`, publish.StockpileDir, publish.StructureDir),
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
			reloadServer(res.DiffSummary)
			return nil
		}

		// Note: .live/ health is NOT checked here. ValidateSource() validates
		// the stockpile and returns per-file errors in the Result. The old
		// checkLiveHealth() called NewConfigStore on .live/ which produced
		// uncontrollable slog output before the file list.

		if dryRun {
			res, err := publish.ValidateSource(opts)
			if err != nil {
				suggestFixes(err, !askAI)
				return err
			}
			defer res.Cleanup()
			printFileList(res)
			if res.URLWarning != "" {
				fmt.Fprintf(os.Stderr, "\nwarning: %s\n", res.URLWarning)
			}
			fmt.Printf("\n%s Validation passed: %d modules, %d roles, %d envoy entries\n",
				green("✓"), res.Modules, res.Roles, res.Entries)
			return nil
		}

		res, err := publish.Publish(opts)
		if err != nil {
			suggestFixes(err, !askAI)
			return err
		}
		printFileList(res)
		if res.URLWarning != "" {
			fmt.Fprintf(os.Stderr, "\nwarning: %s\n", res.URLWarning)
		}
		if res.Files == 0 && len(res.Changed) == 0 && len(res.Removed) == 0 {
			fmt.Printf("\n%s Nothing to publish — .live/ matches stockpile.", green("✓"))
			printPublishSummary(res)
			return nil
		}
		printPublishSummary(res)

		if showDiff && res.DiffSummary != "" {
			fmt.Println("\nDiff:")
			fmt.Println(res.DiffSummary)
		}

		recordPublish(operator, res)
		reloadServer(res.DiffSummary)

		// Module retraction: generate retract modules for removed modules
		// (show affected envoys) and for new/changed modules (pre-build).
		if len(res.RemovedModuleContent) > 0 {
			handleRetraction(res)
		}
		if len(res.ChangedModuleContent) > 0 {
			handleRetractionPregen(res)
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
		// Convert absolute path to relative by stripping the stockpile prefix.
		rel := fe.Path
		if i := strings.Index(rel, "stockpile/"); i >= 0 {
			rel = rel[i+len("stockpile/"):]
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
		fmt.Printf("  %s  %d module(s) with validation errors\n", red("✗"), errCount)
	}
	if res.Modules > 0 || res.Roles > 0 || res.Entries > 0 {
		fmt.Printf("  %s  %d modules, %d roles, %d envoy entries\n",
			bold("Summary"), res.Modules, res.Roles, res.Entries)
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
		b.WriteString("You are reviewing Vigo .vgo config files that failed validation.\n")
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

	// Try to find a file path in the error (e.g., "parsing /srv/vigo/stockpile/modules/foo.vgo: ...")
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
		// Only read files within the config tree to prevent information disclosure.
		cleaned := filepath.Clean(filePath)
		if strings.HasPrefix(cleaned, publish.StockpileDir) || strings.HasPrefix(cleaned, publish.StructureDir) {
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

// reloadServer tells the server to re-read config from .live/ (best-effort).
// When the server returns 403 (approval required), it submits a pending change
// request and prints the change ID with approval instructions.
func reloadServer(diffSummary string) {
	client := newAPIClient()
	err := client.post("/api/v1/config/reload", nil, nil)
	if err == nil {
		return
	}

	// Check if the error indicates approval is required (403).
	if strings.Contains(err.Error(), "403") {
		submitPendingChange(client, diffSummary)
		return
	}
	fmt.Fprintf(os.Stderr, "warning: server reload failed (%v) — restart the server to pick up changes\n", err)
}

// submitPendingChange creates a pending config change request when approval
// is required. Prints the change ID and instructions for approval.
func submitPendingChange(client *apiClient, diffSummary string) {
	var resp struct {
		ID          int64  `json:"id"`
		Status      string `json:"status"`
		RequestedBy string `json:"requested_by"`
		ExpiresAt   string `json:"expires_at"`
	}
	body := map[string]string{
		"diff_summary": diffSummary,
	}
	if err := client.post("/api/v1/config/changes", body, &resp); err != nil {
		fmt.Fprintf(os.Stderr, "warning: failed to submit change for approval (%v)\n", err)
		return
	}
	fmt.Printf("\nApproval required — change %d submitted (expires %s).\n", resp.ID, formatTimestamp(resp.ExpiresAt))
	fmt.Printf("A different user must approve it:\n")
	fmt.Printf("  vigocli config approve %d\n", resp.ID)
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
	Enabled bool              `json:"enabled"`
	Reason  string            `json:"reason,omitempty"`
	Results []retractAPIResult `json:"results,omitempty"`
}

type retractAPIResult struct {
	Name          string `json:"name"`
	RetractYAML string `json:"retract_yaml"`
	Warnings      []struct {
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
// writes .retract files, and prints a summary for the operator.
func handleRetraction(res *publish.Result) {
	// Build request with removed module names and content.
	type modInput struct {
		Name    string `json:"name"`
		Content string `json:"content"`
	}
	var modules []modInput
	for rel, content := range res.RemovedModuleContent {
		// Extract module name from path: "modules/nginx.vgo" → "nginx"
		base := filepath.Base(rel)
		name := strings.TrimSuffix(base, filepath.Ext(base))
		modules = append(modules, modInput{Name: name, Content: string(content)})
	}

	client := newAPIClient()
	var resp retractAPIResponse
	err := client.post("/api/v1/config/retract", map[string]any{"modules": modules}, &resp)
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
		printRetractionResult(result)
	}
}

// handleRetractionPregen calls the server's retract endpoint for new/changed
// modules to pre-generate .retract files. These sit alongside the modules,
// ready for use if the module is later removed from a node's config.
func handleRetractionPregen(res *publish.Result) {
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
	if err := client.post("/api/v1/config/retract", map[string]any{"modules": modules}, &resp); err != nil {
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
		retractPath := filepath.Join(publish.StockpileDir, "stones", result.Name+".retract")
		if err := os.WriteFile(retractPath, []byte(result.RetractYAML), 0644); err != nil {
			fmt.Fprintf(os.Stderr, "warning: failed to write %s: %v\n", retractPath, err)
			continue
		}
		generated++
	}
	if generated > 0 {
		fmt.Printf("\n%s Generated %d retract module(s) in stockpile/modules/\n",
			green("✓"), generated)
	}
}

func printRetractionResult(result retractAPIResult) {
	// Write the .retract file to stockpile/modules/.
	retractPath := filepath.Join(publish.StockpileDir, "stones", result.Name+".retract")
	if result.RetractYAML != "" {
		if err := os.WriteFile(retractPath, []byte(result.RetractYAML), 0644); err != nil {
			fmt.Fprintf(os.Stderr, "warning: failed to write %s: %v\n", retractPath, err)
			return
		}
	}

	fmt.Printf("%s Module removed: %s%s\n", yellow("⚠"), bold(result.Name), cReset())

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
		fmt.Printf("  %s\n", dim("No envoys found with this module in run history."))
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
	fmt.Printf("    3. Add module %s to the relevant match blocks\n", bold("\""+result.Name+"-retract\""))
	fmt.Println("    4. Run: vigocli config publish")
	fmt.Println("    5. After all envoys converge, remove the module and entry")
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
	configPublishCmd.Flags().String("file", "", "publish a single .vgo file (path relative to stockpile/)")

	configCmd.AddCommand(configPublishCmd)
}
