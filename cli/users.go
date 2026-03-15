// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

package main

import (
	"fmt"
	"os"
	"text/tabwriter"

	"github.com/spf13/cobra"
	"golang.org/x/crypto/bcrypt"
	"golang.org/x/term"
)

var usersCmd = &cobra.Command{
	Use:   "users",
	Short: "Manage web UI and API user accounts (admin/viewer roles)",
}

var usersListCmd = &cobra.Command{
	Use:   "list",
	Short: "List all user accounts with role and creation time",
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
	Short: "Create a new user account with a username, password, and admin/viewer role",
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
	Short: "Permanently remove a user account and revoke their sessions",
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

		if !confirmAction(fmt.Sprintf("Delete user %q? This cannot be undone.", userUsername)) {
			fmt.Println("Cancelled.")
			return nil
		}

		if err := client.delete("/api/v1/users/"+userID, nil); err != nil {
			return err
		}

		fmt.Printf("Deleted user %s\n", userUsername)
		return nil
	},
}

var usersSetPasswordCmd = &cobra.Command{
	Use:   "set-password",
	Short: "Set or reset a user's password (prompts interactively)",
	Long: `Set the password for a web UI user account. The password is bcrypt-hashed
and stored in the local secrets provider at hobnob/web/auth/<username>.

The account must already exist in the database (created via 'users create').
The account is not active for login until a password is set.`,
	RunE: func(cmd *cobra.Command, args []string) error {
		if userUsername == "" {
			return fmt.Errorf("--username is required")
		}

		// Read password interactively.
		fmt.Fprint(os.Stderr, "Enter password: ")
		pw1, err := term.ReadPassword(int(os.Stdin.Fd()))
		fmt.Fprintln(os.Stderr)
		if err != nil {
			return fmt.Errorf("reading password: %w", err)
		}
		if len(pw1) < 8 {
			return fmt.Errorf("password must be at least 8 characters")
		}

		fmt.Fprint(os.Stderr, "Confirm password: ")
		pw2, err := term.ReadPassword(int(os.Stdin.Fd()))
		fmt.Fprintln(os.Stderr)
		if err != nil {
			return fmt.Errorf("reading password: %w", err)
		}
		if string(pw1) != string(pw2) {
			return fmt.Errorf("passwords do not match")
		}

		// Bcrypt hash the password.
		hash, err := bcrypt.GenerateFromPassword(pw1, bcrypt.DefaultCost)
		if err != nil {
			return fmt.Errorf("hashing password: %w", err)
		}

		// Store the hash in the local secrets provider.
		provider, err := newLocalProvider(cmd)
		if err != nil {
			return err
		}
		defer provider.Close()

		secretKey := fmt.Sprintf("hobnob/web/auth/%s", userUsername)
		if err := provider.Set(secretKey, string(hash)); err != nil {
			return fmt.Errorf("storing password: %w", err)
		}

		fmt.Printf("Password set for user %q\n", userUsername)
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

	usersSetPasswordCmd.Flags().StringVar(&userUsername, "username", "", "Username (required)")
	usersSetPasswordCmd.Flags().String("key-file", "", "Path to master encryption key (omit to auto-generate)")
	usersSetPasswordCmd.Flags().String("secrets-dir", "/srv/hobnob/secrets", "Directory for secret files")

	usersCmd.AddCommand(usersListCmd)
	usersCmd.AddCommand(usersCreateCmd)
	usersCmd.AddCommand(usersDeleteCmd)
	usersCmd.AddCommand(usersSetPasswordCmd)
}
