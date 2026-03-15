// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

package main

import (
	"fmt"
	"time"

	"github.com/spf13/cobra"
)

var configFreezeCmd = &cobra.Command{
	Use:   "freeze",
	Short: "Block all config publishes until a specified time (maintenance window)",
	RunE: func(cmd *cobra.Command, args []string) error {
		until, _ := cmd.Flags().GetString("until")
		if until == "" {
			return fmt.Errorf("--until is required (RFC3339 or duration like 24h)")
		}

		// Try parsing as RFC3339 first, then as duration.
		var freezeUntil time.Time
		if t, err := time.Parse(time.RFC3339, until); err == nil {
			freezeUntil = t
		} else if d, err := time.ParseDuration(until); err == nil {
			freezeUntil = time.Now().Add(d)
		} else {
			return fmt.Errorf("invalid --until value: must be RFC3339 timestamp or duration (e.g., 24h)")
		}

		client := newAPIClient()
		req := map[string]string{"freeze_until": freezeUntil.Format(time.RFC3339)}
		if err := client.post("/api/v1/config/freeze", req, nil); err != nil {
			return err
		}
		fmt.Printf("Config publishes frozen until %s\n", freezeUntil.Format(time.RFC3339))
		return nil
	},
}

var configUnfreezeCmd = &cobra.Command{
	Use:   "unfreeze",
	Short: "Lift the config publish freeze and allow publishes to proceed",
	RunE: func(cmd *cobra.Command, args []string) error {
		client := newAPIClient()
		if err := client.post("/api/v1/config/unfreeze", nil, nil); err != nil {
			return err
		}
		fmt.Println("Config publish freeze removed.")
		return nil
	},
}

func init() {
	configFreezeCmd.Flags().String("until", "", "freeze until (RFC3339 or duration like 24h)")
	configCmd.AddCommand(configFreezeCmd, configUnfreezeCmd, webhooksCmd)
}
