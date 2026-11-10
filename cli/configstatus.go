// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: AGPL-3.0-only

package main

import (
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"
)

var configStatusCmd = &cobra.Command{
	Use:   "status",
	Short: "Show config-reload health (exit 0 healthy, 1 unhealthy)",
	Long: `Reports whether the most recent config reload succeeded. Suitable for
monitoring hooks and shell scripts: exit code is 0 when healthy, 1 when
the last reload returned an error (e.g., IdempotencyErrors that left the
store empty).`,
	RunE: func(cmd *cobra.Command, args []string) error {
		client := newAPIClient()
		var resp struct {
			Healthy      bool   `json:"healthy"`
			Version      uint64 `json:"version"`
			LastError    string `json:"last_error"`
			LastReloadAt string `json:"last_reload_at"`
		}
		if err := client.get("/api/v1/config/status", &resp); err != nil {
			return err
		}
		status := "healthy"
		if !resp.Healthy {
			status = "UNHEALTHY"
		}
		fmt.Printf("Config: %s\n", status)
		fmt.Printf("Version: %d\n", resp.Version)
		if resp.LastReloadAt != "" {
			fmt.Printf("Last reload: %s\n", resp.LastReloadAt)
		}
		if resp.LastError != "" {
			// IdempotencyErrors.Format() emits a multi-line block; print the
			// label on its own line and indent the body so the report stays
			// scannable instead of collapsing under "Last error: <newline>".
			fmt.Println("Last error:")
			for _, line := range strings.Split(strings.TrimSpace(resp.LastError), "\n") {
				if line == "" {
					fmt.Println()
				} else {
					fmt.Println("  " + line)
				}
			}
		}
		// Exit 1 on unhealthy without returning an error — a returned error
		// would prepend cobra's "Error:" line + usage to the report. The
		// unhealthy case isn't a CLI execution failure; it's a successful
		// query that reports bad state.
		if !resp.Healthy {
			os.Exit(1)
		}
		return nil
	},
}

func init() {
	configCmd.AddCommand(configStatusCmd)
}
