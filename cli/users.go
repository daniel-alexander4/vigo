// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

package main

import (
	"fmt"
	"os"
	"os/user"

	"github.com/spf13/cobra"
)

// validRoles enumerates every role string the server accepts. Kept in
// sync with server/auth.ValidRoles and the CHECK constraint in
// migrations/000001_initial_schema.up.sql; the CLI validates client-
// side so a typo at the prompt fails before the round-trip.
var validRoles = []string{"admin", "viewer", "compliance"}

func isValidRole(role string) bool {
	for _, r := range validRoles {
		if r == role {
			return true
		}
	}
	return false
}

var webusersCmd = &cobra.Command{
	Use:   "webusers",
	Short: "Manage web UI and API user accounts (admin/viewer/compliance roles)",
}

var webusersListCmd = &cobra.Command{
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

		w := newAlignedTable(os.Stdout)
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\n", header("ID"), header("USERNAME"), header("DISPLAY NAME"), header("EMAIL"), header("ROLE"))
		for _, u := range users {
			fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\n", dim(u.ID), u.Username, u.DisplayName, u.Email, u.Role)
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

var webusersCreateCmd = &cobra.Command{
	Use:   "create",
	Short: "Create a new user account with a username, password, and admin/viewer role",
	RunE: func(cmd *cobra.Command, args []string) error {
		if userUsername == "" {
			return fmt.Errorf("--username is required")
		}
		if userRole == "" {
			userRole = "viewer"
		}
		if !isValidRole(userRole) {
			return fmt.Errorf("invalid --role %q (valid: %v)", userRole, validRoles)
		}

		body := map[string]string{
			"username":     userUsername,
			"display_name": userDisplayName,
			"email":        userEmail,
			"role":         userRole,
		}

		// Admin webusers must map 1:1 with a human OS user on the server
		// host. The pubkey is auto-read from that OS user's standard SSH
		// key location to enforce the mapping (and so audit attribution
		// has a real OS identity backing each admin action). Viewer /
		// compliance users have no such constraint — they're one-offs.
		if userRole == "admin" {
			if userSSHKeyFile != "" {
				return fmt.Errorf("--ssh-key-file is not allowed for --role admin; the pubkey is auto-read from the matching OS user's ~/.ssh/id_ed25519.pub (fallback id_rsa.pub). Drop the flag and run again from the server host as root or via sudo")
			}
			osUser, err := user.Lookup(userUsername)
			if err != nil {
				return fmt.Errorf("admin role requires a matching OS user on the server host — %q not found locally (%w). Run this command on the server host as root after the OS user has been created", userUsername, err)
			}
			pubkey, err := readDefaultSSHPubkey(osUser.HomeDir)
			if err != nil {
				return fmt.Errorf("admin pubkey auto-read: %w (generate a key for OS user %q first: `sudo -u %s ssh-keygen -t ed25519`)", err, userUsername, userUsername)
			}
			body["ssh_public_key"] = pubkey
		} else if userSSHKeyFile != "" {
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

var webusersDeleteCmd = &cobra.Command{
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


func init() {
	webusersCreateCmd.Flags().StringVar(&userUsername, "username", "", "Username (required)")
	webusersCreateCmd.Flags().StringVar(&userDisplayName, "display-name", "", "Display name")
	webusersCreateCmd.Flags().StringVar(&userEmail, "email", "", "Email address")
	webusersCreateCmd.Flags().StringVar(&userRole, "role", "viewer", "Role (admin, viewer, or compliance)")
	webusersCreateCmd.Flags().StringVar(&userSSHKeyFile, "ssh-key-file", "", "Path to SSH public key file")

	webusersDeleteCmd.Flags().StringVar(&userUsername, "username", "", "Username (required)")

	webusersCmd.AddCommand(webusersListCmd)
	webusersCmd.AddCommand(webusersCreateCmd)
	webusersCmd.AddCommand(webusersDeleteCmd)
}
