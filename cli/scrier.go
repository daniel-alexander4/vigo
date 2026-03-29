// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

package main

import (
	"fmt"
	"io"
	"os"
	"strings"
	"text/tabwriter"

	"github.com/spf13/cobra"
)

var scrierCmd = &cobra.Command{
	Use:   "scrier",
	Short: "Manage Scrier remote access",
}

var scrierUsersCmd = &cobra.Command{
	Use:   "users",
	Short: "Manage Scrier SSH pubkey access for web UI users",
}

var scrierUsersListCmd = &cobra.Command{
	Use:   "list",
	Short: "List web UI users with SSH pubkeys configured for Scrier access",
	RunE: func(cmd *cobra.Command, args []string) error {
		client := newAPIClient()
		var resp struct {
			Users []struct {
				Username string `json:"username"`
				Pubkey   string `json:"pubkey"`
			} `json:"users"`
		}
		if err := client.get("/api/v1/scrier/users", &resp); err != nil {
			return err
		}

		if len(resp.Users) == 0 {
			fmt.Println("No Scrier SSH pubkeys configured.")
			return nil
		}

		w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
		fmt.Fprintf(w, "%s\t%s\n", header("USERNAME"), header("PUBKEY"))
		for _, u := range resp.Users {
			// Show key type + first 20 chars of the key for readability.
			short := u.Pubkey
			parts := strings.Fields(short)
			if len(parts) >= 2 {
				key := parts[1]
				if len(key) > 20 {
					key = key[:20] + "..."
				}
				short = parts[0] + " " + key
				if len(parts) >= 3 {
					short += " " + parts[2]
				}
			}
			fmt.Fprintf(w, "%s\t%s\n", u.Username, short)
		}
		return w.Flush()
	},
}

var scrierUsersSetPubkeyCmd = &cobra.Command{
	Use:   "setpubkey --user <username>",
	Short: "Set a user's SSH public key for Scrier access (reads from stdin or --key)",
	RunE: func(cmd *cobra.Command, args []string) error {
		username, _ := cmd.Flags().GetString("user")
		if username == "" {
			return fmt.Errorf("--user is required")
		}

		keyFile, _ := cmd.Flags().GetString("key")
		var pubkey string
		if keyFile != "" {
			data, err := os.ReadFile(keyFile)
			if err != nil {
				return fmt.Errorf("reading key file: %w", err)
			}
			pubkey = strings.TrimSpace(string(data))
		} else {
			data, err := io.ReadAll(os.Stdin)
			if err != nil {
				return fmt.Errorf("reading stdin: %w", err)
			}
			pubkey = strings.TrimSpace(string(data))
		}

		if pubkey == "" {
			return fmt.Errorf("no public key provided (pipe via stdin or use --key)")
		}

		client := newAPIClient()
		if err := client.put("/api/v1/scrier/users/"+username+"/pubkey",
			map[string]string{"pubkey": pubkey}, nil); err != nil {
			return err
		}

		fmt.Printf("SSH pubkey set for Scrier user %s\n", username)
		return nil
	},
}

var scrierUsersRemoveCmd = &cobra.Command{
	Use:   "remove --user <username>",
	Short: "Remove a user's SSH public key, revoking their Scrier access",
	RunE: func(cmd *cobra.Command, args []string) error {
		username, _ := cmd.Flags().GetString("user")
		if username == "" {
			return fmt.Errorf("--user is required")
		}

		client := newAPIClient()
		if err := client.delete("/api/v1/scrier/users/"+username+"/pubkey", nil); err != nil {
			return err
		}

		fmt.Printf("SSH pubkey removed for Scrier user %s\n", username)
		return nil
	},
}

func init() {
	scrierUsersSetPubkeyCmd.Flags().String("user", "", "Web UI username")
	scrierUsersSetPubkeyCmd.Flags().String("key", "", "Path to SSH public key file (alternative to stdin)")
	scrierUsersRemoveCmd.Flags().String("user", "", "Web UI username")

	scrierUsersCmd.AddCommand(scrierUsersListCmd, scrierUsersSetPubkeyCmd, scrierUsersRemoveCmd)
	scrierCmd.AddCommand(scrierUsersCmd)
}
