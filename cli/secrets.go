// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: BUSL-1.1

package main

import (
	"crypto/rand"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"text/tabwriter"

	"github.com/hobnob/hobnob/server/secrets"
	"github.com/spf13/cobra"
	"golang.org/x/term"
)

var secretsCmd = &cobra.Command{
	Use:   "secrets",
	Short: "Manage local encrypted secrets",
	Long:  "Manage secrets stored as AES-256-GCM encrypted files.",
}

var secretsSetCmd = &cobra.Command{
	Use:   "set <key> [value]",
	Short: "Encrypt and store a secret",
	Long: `Encrypt and store a secret value.

The value can be provided in three ways (in priority order):
  1. --from-file <path>   Read value from a file
  2. Positional argument   hobnobcli secrets set mykey myvalue
  3. Interactive prompt    Reads from terminal with echo disabled (default)

When stdin is a pipe, reads from stdin automatically:
  echo "myvalue" | hobnobcli secrets set mykey

WARNING: Passing the value as a CLI argument exposes it in shell
history and process listings. Prefer --from-file or the interactive
prompt for sensitive values.`,
	Args: cobra.RangeArgs(1, 2),
	RunE: runSecretsSet,
}

var secretsGetCmd = &cobra.Command{
	Use:   "get <key>",
	Short: "Decrypt and display a secret",
	Args:  cobra.ExactArgs(1),
	RunE:  runSecretsGet,
}

var secretsDeleteCmd = &cobra.Command{
	Use:   "delete <key>",
	Short: "Remove an encrypted secret",
	Args:  cobra.ExactArgs(1),
	RunE:  runSecretsDelete,
}

var secretsListCmd = &cobra.Command{
	Use:   "list",
	Short: "List all secret keys",
	RunE:  runSecretsList,
}

var secretsInitCmd = &cobra.Command{
	Use:   "init",
	Short: "Generate a new master key file",
	RunE:  runSecretsInit,
}

func init() {
	secretsCmd.PersistentFlags().String("key-file", "", "Path to master encryption key (omit to auto-generate)")
	secretsCmd.PersistentFlags().String("secrets-dir", "/etc/hobnob-server/secrets", "Directory for secret files")

	secretsSetCmd.Flags().String("from-file", "", "Read secret value from a file")

	secretsInitCmd.Flags().String("key-file", "", "Path to write the new master key")
	secretsInitCmd.MarkFlagRequired("key-file")

	secretsCmd.AddCommand(secretsSetCmd)
	secretsCmd.AddCommand(secretsGetCmd)
	secretsCmd.AddCommand(secretsDeleteCmd)
	secretsCmd.AddCommand(secretsListCmd)
	secretsCmd.AddCommand(secretsInitCmd)
}

func newLocalProvider(cmd *cobra.Command) (*secrets.LocalProvider, error) {
	keyFile, _ := cmd.Flags().GetString("key-file")
	secretsDir, _ := cmd.Flags().GetString("secrets-dir")
	return secrets.NewLocalProvider(keyFile, secretsDir)
}

// readSecretValue determines the secret value from flags, args, stdin, or interactive prompt.
func readSecretValue(cmd *cobra.Command, args []string) (string, error) {
	// Priority 1: --from-file flag.
	fromFile, _ := cmd.Flags().GetString("from-file")
	if fromFile != "" {
		data, err := os.ReadFile(fromFile)
		if err != nil {
			return "", fmt.Errorf("reading secret from file: %w", err)
		}
		return strings.TrimRight(string(data), "\n"), nil
	}

	// Priority 2: Positional argument (value provided on command line).
	if len(args) >= 2 {
		value := args[1]
		// Legacy stdin mode: explicit "-" reads from stdin pipe.
		if value == "-" {
			data, err := io.ReadAll(os.Stdin)
			if err != nil {
				return "", fmt.Errorf("reading stdin: %w", err)
			}
			return strings.TrimRight(string(data), "\n"), nil
		}
		return value, nil
	}

	// Priority 3: Stdin is a pipe (not a terminal).
	if !term.IsTerminal(int(os.Stdin.Fd())) {
		data, err := io.ReadAll(os.Stdin)
		if err != nil {
			return "", fmt.Errorf("reading stdin: %w", err)
		}
		return strings.TrimRight(string(data), "\n"), nil
	}

	// Priority 4: Interactive prompt with echo disabled.
	fmt.Fprint(os.Stderr, "Enter secret value: ")
	value, err := term.ReadPassword(int(os.Stdin.Fd()))
	fmt.Fprintln(os.Stderr) // newline after hidden input
	if err != nil {
		return "", fmt.Errorf("reading secret: %w", err)
	}
	if len(value) == 0 {
		return "", fmt.Errorf("empty secret value")
	}
	return string(value), nil
}

func runSecretsSet(cmd *cobra.Command, args []string) error {
	provider, err := newLocalProvider(cmd)
	if err != nil {
		return err
	}
	defer provider.Close()

	key := args[0]
	value, err := readSecretValue(cmd, args)
	if err != nil {
		return err
	}

	if err := provider.Set(key, value); err != nil {
		return err
	}
	fmt.Printf("  Secret %q stored.\n", key)
	return nil
}

func runSecretsGet(cmd *cobra.Command, args []string) error {
	provider, err := newLocalProvider(cmd)
	if err != nil {
		return err
	}
	defer provider.Close()

	val, err := provider.Get(cmd.Context(), args[0])
	if err != nil {
		return err
	}
	fmt.Println(val)
	return nil
}

func runSecretsDelete(cmd *cobra.Command, args []string) error {
	provider, err := newLocalProvider(cmd)
	if err != nil {
		return err
	}
	defer provider.Close()

	if err := provider.Delete(args[0]); err != nil {
		return err
	}
	fmt.Printf("  Secret %q deleted.\n", args[0])
	return nil
}

func runSecretsList(cmd *cobra.Command, _ []string) error {
	provider, err := newLocalProvider(cmd)
	if err != nil {
		return err
	}
	defer provider.Close()

	keys, err := provider.List()
	if err != nil {
		return err
	}

	if len(keys) == 0 {
		fmt.Println("  No secrets stored.")
		return nil
	}

	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "  KEY")
	for _, k := range keys {
		fmt.Fprintf(w, "  %s\n", k)
	}
	w.Flush()
	return nil
}

func runSecretsInit(cmd *cobra.Command, _ []string) error {
	keyFile, _ := cmd.Flags().GetString("key-file")

	// Check if file already exists.
	if _, err := os.Stat(keyFile); err == nil {
		return fmt.Errorf("key file %q already exists — refusing to overwrite", keyFile)
	}

	// Generate 32 random bytes.
	key := make([]byte, 32)
	if _, err := io.ReadFull(rand.Reader, key); err != nil {
		return fmt.Errorf("generating key: %w", err)
	}

	// Create parent directory if needed.
	if dir := filepath.Dir(keyFile); dir != "." {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return fmt.Errorf("creating directory: %w", err)
		}
	}

	// Write as hex (64 characters).
	hexKey := fmt.Sprintf("%x\n", key)
	if err := os.WriteFile(keyFile, []byte(hexKey), 0o600); err != nil {
		return fmt.Errorf("writing key file: %w", err)
	}

	fmt.Printf("  Master key written to %s (mode 0600).\n", keyFile)
	fmt.Println("  Keep this file safe — losing it means losing access to all encrypted secrets.")
	return nil
}
