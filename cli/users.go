// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

package main

import (
	"fmt"
	"io"
	"os"
	"os/user"
	"strings"

	"git.alexander4.io/vigo/server/secrets"
	"github.com/spf13/cobra"
	"golang.org/x/term"
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
	userUsername      string
	userDisplayName   string
	userEmail         string
	userRole          string
	userSSHKeyFile    string
	userPasswordStdin bool
	userNoPassword    bool
)

var webusersCreateCmd = &cobra.Command{
	Use:   "create",
	Short: "Create a new web/API user (admin, viewer, or compliance) and set its password",
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

		// Read the password BEFORE the server-side create. If the vault is
		// locked or input is unusable, we'd rather fail here than leave a
		// password-less user row on the server (the exact bug today).
		// --no-password skips this for OIDC / API-token-only flows.
		var password string
		if !userNoPassword {
			if err := requireVaultUnlockedForWebusers(); err != nil {
				return err
			}
			pw, err := readWebuserPassword()
			if err != nil {
				return err
			}
			password = pw
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

		// Password write must happen after the row exists. If it fails the
		// row is real — tell the operator to retry with set-password
		// instead of leaving them to guess.
		if password != "" {
			if err := writeWebuserPassword(result.Username, password); err != nil {
				return fmt.Errorf("user %s created but writing password failed: %w — run `vigocli webusers set-password --username %s` to retry", result.Username, err, result.Username)
			}
		}

		fmt.Printf("Created user %s (id=%s, role=%s)\n", result.Username, result.ID, result.Role)
		if userNoPassword {
			fmt.Printf("  No password set. Run `vigocli webusers set-password --username %s` if you need UI/basic-auth login.\n", result.Username)
		}
		return nil
	},
}

var webusersSetPasswordCmd = &cobra.Command{
	Use:   "set-password",
	Short: "Set or change a web user's password (writes to the secrets vault)",
	RunE: func(cmd *cobra.Command, args []string) error {
		if userUsername == "" {
			return fmt.Errorf("--username is required")
		}
		if err := requireVaultUnlockedForWebusers(); err != nil {
			return err
		}
		password, err := readWebuserPassword()
		if err != nil {
			return err
		}
		if err := writeWebuserPassword(userUsername, password); err != nil {
			return err
		}
		fmt.Printf("Password set for user %s.\n", userUsername)
		return nil
	},
}

// requireVaultUnlockedForWebusers gates webuser password operations behind
// the same unlock verifier as `vigocli secrets set`. Bare two-check
// primitive — no cobra plumbing — so it can be called from a RunE body
// conditionally (e.g. `webusers create` skips it when --no-password).
func requireVaultUnlockedForWebusers() error {
	if !verifierExists() {
		return vaultNotInitializedError()
	}
	if !isUnlocked() {
		return fmt.Errorf("secrets vault is locked — run `vigocli secrets unlock`")
	}
	bumpSentinel()
	return nil
}

// readWebuserPassword reads the password from --password-stdin if set,
// otherwise prompts interactively. Refuses if stdin is not a TTY and the
// stdin flag wasn't passed — operators get a clear error instead of an
// invisible hang.
func readWebuserPassword() (string, error) {
	if userPasswordStdin {
		data, err := io.ReadAll(os.Stdin)
		if err != nil {
			return "", fmt.Errorf("reading password from stdin: %w", err)
		}
		pw := strings.TrimRight(string(data), "\n")
		if err := validatePasswordComplexity(pw); err != nil {
			return "", err
		}
		return pw, nil
	}
	if !term.IsTerminal(int(os.Stdin.Fd())) {
		return "", fmt.Errorf("stdin is not a terminal — pass --password-stdin to read from stdin, or run interactively")
	}
	pw, err := promptPassword()
	if err != nil {
		return "", err
	}
	return string(pw), nil
}

// webuserPasswordKey is the canonical secrets-vault path basic auth reads
// from (server/auth/basic.go:188). One source of truth; if this drifts,
// passwords get written somewhere basic auth can't find — silent breakage.
func webuserPasswordKey(username string) string {
	return fmt.Sprintf("vigo/web/auth/%s", username)
}

// writeWebuserPassword stores the password at the canonical key the basic
// auth provider reads from. secretsDir is parameterized so tests can use a
// tmpdir; production callers pass defaultSecretsDir.
func writeWebuserPassword(username, password string) error {
	provider, err := secrets.NewLocalProvider("", defaultSecretsDir)
	if err != nil {
		return err
	}
	defer provider.Close()
	return provider.Set(webuserPasswordKey(username), password)
}

// defaultSecretsDir matches the persistent --secrets-dir default on the
// `secrets` command tree and the path vigosrv auto-generates the master
// key under. Constant so tests can refer to the production value.
const defaultSecretsDir = "/srv/vigo/secrets"

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
	webusersCreateCmd.Flags().BoolVar(&userPasswordStdin, "password-stdin", false, "Read password from stdin instead of prompting (for scripted use)")
	webusersCreateCmd.Flags().BoolVar(&userNoPassword, "no-password", false, "Skip password set — for OIDC or API-token-only users")

	webusersSetPasswordCmd.Flags().StringVar(&userUsername, "username", "", "Username (required)")
	webusersSetPasswordCmd.Flags().BoolVar(&userPasswordStdin, "password-stdin", false, "Read password from stdin instead of prompting (for scripted use)")

	webusersDeleteCmd.Flags().StringVar(&userUsername, "username", "", "Username (required)")

	webusersCmd.AddCommand(webusersListCmd)
	webusersCmd.AddCommand(webusersCreateCmd)
	webusersCmd.AddCommand(webusersSetPasswordCmd)
	webusersCmd.AddCommand(webusersDeleteCmd)
}
