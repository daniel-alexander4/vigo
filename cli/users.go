// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

package main

import (
	"fmt"
	"os"
	"text/tabwriter"

	"github.com/spf13/cobra"
)

var usersCmd = &cobra.Command{
	Use:   "users",
	Short: "Manage users",
}

var usersListCmd = &cobra.Command{
	Use:   "list",
	Short: "List all users",
	RunE: func(cmd *cobra.Command, args []string) error {
		client := newAPIClient()
		var users []struct {
			ID          string `json:"id"`
			Username    string `json:"username"`
			DisplayName string `json:"display_name"`
			Email       string `json:"email"`
			Role        string `json:"role"`
		}
		if err := client.get("/api/v1/users", &users); err != nil {
			return err
		}

		w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
		fmt.Fprintln(w, "ID\tUSERNAME\tDISPLAY NAME\tEMAIL\tROLE")
		for _, u := range users {
			fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\n", u.ID, u.Username, u.DisplayName, u.Email, u.Role)
		}
		return w.Flush()
	},
}

var (
	userUsername    string
	userDisplayName string
	userEmail       string
	userRole        string
	userSSHKeyFile  string
)

var usersCreateCmd = &cobra.Command{
	Use:   "create",
	Short: "Create a new user",
	RunE: func(cmd *cobra.Command, args []string) error {
		if userUsername == "" {
			return fmt.Errorf("--username is required")
		}
		if userRole == "" {
			userRole = "viewer"
		}

		body := map[string]string{
			"username":     userUsername,
			"display_name": userDisplayName,
			"email":        userEmail,
			"role":         userRole,
		}

		if userSSHKeyFile != "" {
			data, err := os.ReadFile(userSSHKeyFile)
			if err != nil {
				return fmt.Errorf("reading SSH key file: %w", err)
			}
			body["ssh_public_key"] = string(data)
		}

		client := newAPIClient()
		var result struct {
			ID       string `json:"id"`
			Username string `json:"username"`
			Role     string `json:"role"`
		}
		if err := client.post("/api/v1/users", body, &result); err != nil {
			return err
		}

		fmt.Printf("Created user %s (id=%s, role=%s)\n", result.Username, result.ID, result.Role)
		return nil
	},
}

var usersDeleteCmd = &cobra.Command{
	Use:   "delete",
	Short: "Delete a user",
	RunE: func(cmd *cobra.Command, args []string) error {
		if userUsername == "" {
			return fmt.Errorf("--username is required")
		}

		// Look up user by username to get the ID.
		client := newAPIClient()
		var users []struct {
			ID       string `json:"id"`
			Username string `json:"username"`
		}
		if err := client.get("/api/v1/users", &users); err != nil {
			return err
		}

		var userID string
		for _, u := range users {
			if u.Username == userUsername {
				userID = u.ID
				break
			}
		}
		if userID == "" {
			return fmt.Errorf("user %q not found", userUsername)
		}

		if err := client.delete("/api/v1/users/"+userID, nil); err != nil {
			return err
		}

		fmt.Printf("Deleted user %s\n", userUsername)
		return nil
	},
}

func init() {
	usersCreateCmd.Flags().StringVar(&userUsername, "username", "", "Username (required)")
	usersCreateCmd.Flags().StringVar(&userDisplayName, "display-name", "", "Display name")
	usersCreateCmd.Flags().StringVar(&userEmail, "email", "", "Email address")
	usersCreateCmd.Flags().StringVar(&userRole, "role", "viewer", "Role (admin or viewer)")
	usersCreateCmd.Flags().StringVar(&userSSHKeyFile, "ssh-key-file", "", "Path to SSH public key file")

	usersDeleteCmd.Flags().StringVar(&userUsername, "username", "", "Username (required)")

	usersCmd.AddCommand(usersListCmd)
	usersCmd.AddCommand(usersCreateCmd)
	usersCmd.AddCommand(usersDeleteCmd)
}
