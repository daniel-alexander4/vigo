// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

package main

import (
	"fmt"

	"github.com/spf13/cobra"
)

var envCmd = &cobra.Command{
	Use:   "env",
	Short: "Promote configuration variables between environment tiers (staging, production)",
}

var envPromoteCmd = &cobra.Command{
	Use:   "promote <module>",
	Short: "Copy a module's environment_overrides from one tier to another",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		from, _ := cmd.Flags().GetString("from")
		to, _ := cmd.Flags().GetString("to")
		if from == "" || to == "" {
			return fmt.Errorf("both --from and --to are required")
		}

		client := newAPIClient()
		var resp struct {
			PromotionID string            `json:"promotionId"`
			Diff        map[string]string `json:"diff"`
		}
		err := client.post("/api/v1/env/promote", map[string]string{
			"module_name": args[0],
			"from_env":    from,
			"to_env":      to,
		}, &resp)
		if err != nil {
			return err
		}

		fmt.Printf("Promotion %s: %s -> %s\n", resp.PromotionID, from, to)
		if len(resp.Diff) > 0 {
			fmt.Println("\nVariable changes:")
			for k, v := range resp.Diff {
				fmt.Printf("  %s: %s\n", k, v)
			}
		} else {
			fmt.Println("No variable differences found")
		}
		fmt.Println("\nConfig files not modified. Edit YAML files manually to apply.")
		return nil
	},
}

func init() {
	envPromoteCmd.Flags().String("from", "", "Source environment tier")
	envPromoteCmd.Flags().String("to", "", "Target environment tier")
	configCmd.AddCommand(envPromoteCmd)
}
