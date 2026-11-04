// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"fmt"
	"os"
	"time"

	"github.com/spf13/cobra"
)

var authCreateTokenCmd = &cobra.Command{
	Use:   "create-token",
	Short: "Create a new API token on the server",
	RunE: func(cmd *cobra.Command, args []string) error {
		name, _ := cmd.Flags().GetString("name")
		expiresIn, _ := cmd.Flags().GetString("expires-in")
		user, _ := cmd.Flags().GetString("user")

		body := map[string]string{}
		if name != "" {
			body["name"] = name
		}
		if expiresIn != "" {
			body["expires_in"] = expiresIn
		}
		if user != "" {
			body["user"] = user
		}

		var resp struct {
			Token string `json:"token"`
			ID    string `json:"id"`
			Name  string `json:"name"`
		}
		c := newAPIClient()
		if err := c.post("/api/v1/auth/tokens", body, &resp); err != nil {
			return err
		}

		fmt.Printf("Token created.\n")
		fmt.Printf("  ID:    %s\n", resp.ID)
		if resp.Name != "" {
			fmt.Printf("  Name:  %s\n", resp.Name)
		}
		fmt.Printf("  Token: %s\n", resp.Token)
		fmt.Println()
		fmt.Println("Save this token — it will not be shown again.")
		fmt.Println("To use it: vigocli auth set-token   (paste the token at the prompt)")
		return nil
	},
}

var authListTokensCmd = &cobra.Command{
	Use:   "list-tokens",
	Short: "List your API tokens on the server",
	RunE: func(cmd *cobra.Command, args []string) error {
		var tokens []struct {
			ID        string     `json:"id"`
			Name      string     `json:"name"`
			Prefix    string     `json:"prefix"`
			CreatedAt time.Time  `json:"created_at"`
			ExpiresAt *time.Time `json:"expires_at"`
		}
		c := newAPIClient()
		if err := c.get("/api/v1/auth/tokens", &tokens); err != nil {
			return err
		}

		if len(tokens) == 0 {
			fmt.Println("No API tokens.")
			return nil
		}

		w := newAlignedTable(os.Stdout)
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\n", header("ID"), header("NAME"), header("PREFIX"), header("CREATED"), header("EXPIRES"))
		for _, t := range tokens {
			expires := "never"
			if t.ExpiresAt != nil {
				expires = t.ExpiresAt.Format("2006-01-02")
			}
			fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\n",
				t.ID,
				t.Name,
				t.Prefix,
				t.CreatedAt.Format("2006-01-02"),
				expires,
			)
		}
		w.Flush()
		return nil
	},
}

var authRevokeTokenCmd = &cobra.Command{
	Use:   "revoke-token <id>",
	Short: "Revoke an API token on the server",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		c := newAPIClient()
		if err := c.delete("/api/v1/auth/tokens/"+args[0], nil); err != nil {
			return err
		}
		fmt.Println("Token revoked.")
		return nil
	},
}

func init() {
	authCreateTokenCmd.Flags().String("name", "", "Descriptive name for the token")
	authCreateTokenCmd.Flags().String("expires-in", "", "Expiry duration (e.g., 720h for 30 days). Omit for no expiry.")
	authCreateTokenCmd.Flags().String("user", "", "Owner web-user (e.g. 'dan'). Required when running on the server host, where the CLI authenticates as the local-admin principal rather than a web user.")

	authCmd.AddCommand(authCreateTokenCmd)
	authCmd.AddCommand(authListTokensCmd)
	authCmd.AddCommand(authRevokeTokenCmd)
}
