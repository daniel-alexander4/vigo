// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

package main

import (
	"fmt"

	"github.com/spf13/cobra"
)

var tokensCmd = &cobra.Command{
	Use:   "tokens",
	Short: "Generate one-time tokens used by agents during bootstrap enrollment",
}

var tokensGenerateCmd = &cobra.Command{
	Use:   "generate",
	Short: "Generate a cryptographic token that authorizes a single agent bootstrap",
	RunE: func(cmd *cobra.Command, args []string) error {
		client := newAPIClient()
		var resp struct {
			Token string `json:"token"`
		}
		if err := client.post("/api/v1/tokens", map[string]any{}, &resp); err != nil {
			return err
		}

		fmt.Printf("%s %s\n", bold("Token:"), resp.Token)
		fmt.Printf("\n%s\n  curl -fsSL %s/bootstrap | HOBNOB_TOKEN=%s bash\n", bold("Bootstrap command:"), serverURL, resp.Token)
		return nil
	},
}

func init() {
	tokensCmd.AddCommand(tokensGenerateCmd)
}
