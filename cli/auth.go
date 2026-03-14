// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

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
	Short: "Manage CLI authentication",
}

var authSetTokenCmd = &cobra.Command{
	Use:   "set-token <token>",
	Short: "Store an API token for CLI authentication",
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
	Short: "Show current CLI authentication status",
	RunE: func(cmd *cobra.Command, args []string) error {
		token := loadAuthToken()
		if token == "" {
			fmt.Println("Not authenticated. Run 'hobnobcli auth set-token <token>' to configure.")
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
	Short: "Remove the stored API token",
	RunE: func(cmd *cobra.Command, args []string) error {
		p := tokenFilePath()
		if err := os.Remove(p); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("removing token: %w", err)
		}
		fmt.Println("Token removed.")
		return nil
	},
}

func init() {
	authCmd.AddCommand(authSetTokenCmd)
	authCmd.AddCommand(authStatusCmd)
	authCmd.AddCommand(authClearCmd)
}

// tokenFilePath returns the path to the stored API token.
func tokenFilePath() string {
	if dir := os.Getenv("HOBNOB_CONFIG_DIR"); dir != "" {
		return filepath.Join(dir, "token")
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".config", "hobnob", "token")
}

// loadAuthToken reads the API token from the config file.
func loadAuthToken() string {
	p := tokenFilePath()
	if p == "" {
		return ""
	}
	data, err := os.ReadFile(p)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(data))
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
