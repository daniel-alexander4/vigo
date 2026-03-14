// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: BUSL-1.1

package main

import (
	"fmt"
	"os/user"
	"time"

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
		dangerous, _ := cmd.Flags().GetBool("dangerous")
		skipURLCheck, _ := cmd.Flags().GetBool("skip-url-check")
		urlTimeout, _ := cmd.Flags().GetDuration("url-timeout")
		operator, _ := cmd.Flags().GetString("operator")
		showDiff, _ := cmd.Flags().GetBool("diff")

		if operator == "" {
			if u, err := user.Current(); err == nil {
				operator = u.Username
			}
		}

		opts := publish.Options{
			Dangerous:  dangerous,
			CheckURLs:  !skipURLCheck,
			URLTimeout: urlTimeout,
			Operator:   operator,
			Diff:       showDiff,
		}

		if dryRun {
			res, err := publish.ValidateSource(opts)
			if err != nil {
				return fmt.Errorf("validation failed:\n  %v", err)
			}
			fmt.Printf("Validation passed: %d modules, %d roles, %d thrall entries\n",
				res.Modules, res.Roles, res.Entries)
			printDiff(res)
			return nil
		}

		res, err := publish.Publish(opts)
		if err != nil {
			return err
		}
		if res.Files == 0 && len(res.Changed) == 0 && len(res.Removed) == 0 {
			fmt.Println("Nothing to publish — proposed matches live.")
			return nil
		}
		fmt.Printf("Published: %d modules, %d roles, %d thrall entries (%d files synced)\n",
			res.Modules, res.Roles, res.Entries, res.Files)
		printDiff(res)

		if showDiff && res.DiffSummary != "" {
			fmt.Println("\nDiff:")
			fmt.Println(res.DiffSummary)
		}

		// Record publish in server for audit trail (best-effort).
		client := newAPIClient()
		_ = client.post("/api/v1/config/publishes", map[string]any{
			"operator":      operator,
			"changed_files": res.Changed,
			"removed_files": res.Removed,
			"diff_summary":  res.DiffSummary,
		}, nil)
		return nil
	},
}

func printDiff(res *publish.Result) {
	for _, p := range res.Changed {
		fmt.Printf("  + %s\n", p)
	}
	for _, p := range res.Removed {
		fmt.Printf("  - %s\n", p)
	}
}

func init() {
	configPublishCmd.Flags().Bool("dry-run", false, "validate only, do not publish")
	configPublishCmd.Flags().Bool("dangerous", false, "allow non-idempotent resources")
	configPublishCmd.Flags().Bool("skip-url-check", false, "skip HTTP aliveness checks for URLs")
	configPublishCmd.Flags().Duration("url-timeout", 10*time.Second, "timeout per URL check")
	configPublishCmd.Flags().String("operator", "", "operator name for audit trail (default: $USER)")
	configPublishCmd.Flags().Bool("diff", false, "show line-level diff for each changed file")

	configCmd.AddCommand(configPublishCmd)
}
