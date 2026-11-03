// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"
)

var authCmd = &cobra.Command{
	Use:   "auth",
	Short: "Manage the API token used by this CLI to authenticate with the server",
}

var authSetTokenCmd = &cobra.Command{
	Use:   "set-token <token>",
	Short: "Save an vgot_ API token to ~/.config/vigo/token for subsequent CLI requests",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		token := strings.TrimSpace(args[0])
		if token == "" {
			return fmt.Errorf("token cannot be empty")
		}
		if err := saveAuthToken(token); err != nil {
			return fmt.Errorf("saving token: %w", err)
		}
		fmt.Println("Token saved.")
		return nil
	},
}

var authStatusCmd = &cobra.Command{
	Use:   "status",
	Short: "Show whether a token is configured and which server it targets",
	RunE: func(cmd *cobra.Command, args []string) error {
		token := loadAuthToken()
		if token == "" {
			fmt.Println("Not authenticated. Run 'vigocli auth set-token <token>' to configure.")
		} else {
			// Show a masked version of the token.
			masked := token[:8] + "..." + token[len(token)-4:]
			fmt.Printf("Authenticated with token: %s\n", masked)
		}
		return nil
	},
}

var authClearCmd = &cobra.Command{
	Use:   "clear",
	Short: "Delete the stored API token so CLI requests are unauthenticated",
	RunE: func(cmd *cobra.Command, args []string) error {
		p := tokenFilePath()
		if err := os.Remove(p); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("removing token: %w", err)
		}
		fmt.Println("Token removed.")
		return nil
	},
}

var authEmergencyAccessCmd = &cobra.Command{
	Use:   "emergency-access --reason <reason>",
	Short: "Record an emergency access event in the tamper-evident audit trail (HIPAA 164.312(a)(2)(ii))",
	RunE: func(cmd *cobra.Command, args []string) error {
		reason, _ := cmd.Flags().GetString("reason")
		if reason == "" {
			return fmt.Errorf("--reason is required")
		}
		client := newAPIClient()
		var resp map[string]string
		if err := client.post("/api/v1/emergency-access", map[string]string{"reason": reason}, &resp); err != nil {
			return fmt.Errorf("recording emergency access: %w", err)
		}
		fmt.Println("Emergency access recorded in audit trail.")
		return nil
	},
}

func init() {
	authEmergencyAccessCmd.Flags().String("reason", "", "Reason for emergency access (required)")
	authCmd.AddCommand(authSetTokenCmd)
	authCmd.AddCommand(authStatusCmd)
	authCmd.AddCommand(authClearCmd)
	authCmd.AddCommand(authEmergencyAccessCmd)
}

// tokenFilePath returns the path to the stored API token.
func tokenFilePath() string {
	if dir := os.Getenv("VIGO_CONFIG_DIR"); dir != "" {
		return filepath.Join(dir, "token")
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".config", "vigo", "token")
}

// localAdminTokenPath is the server-host fallback credential: the 0600
// root-only token vigosrv mints in its data dir (server/auth/localadmin.go).
// vigocli reads it when no per-user token is configured, so `sudo vigocli`
// on the server host works with zero setup. A non-root user can't read it
// — which is the point: it replaced source-IP "peer auth".
const localAdminTokenPath = "/srv/vigo/cli-admin-token"

// loadAuthToken reads the API token: the per-user token file first, then
// the server-host local-admin token. Returns empty when neither is present
// (a remote admin host that has not run `vigocli auth set-token`).
func loadAuthToken() string {
	if p := tokenFilePath(); p != "" {
		if data, err := os.ReadFile(p); err == nil {
			if tok := strings.TrimSpace(string(data)); tok != "" {
				return tok
			}
		}
	}
	if data, err := os.ReadFile(localAdminTokenPath); err == nil {
		return strings.TrimSpace(string(data))
	}
	return ""
}

// saveAuthToken writes the API token to the config file (0600 permissions).
func saveAuthToken(token string) error {
	p := tokenFilePath()
	if p == "" {
		return fmt.Errorf("cannot determine config directory")
	}
	dir := filepath.Dir(p)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return fmt.Errorf("creating config dir: %w", err)
	}
	return os.WriteFile(p, []byte(token+"\n"), 0600)
}
