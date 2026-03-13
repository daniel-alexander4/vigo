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
)

var secretsCmd = &cobra.Command{
	Use:   "secrets",
	Short: "Manage local secrets",
	Long:  "Manage secrets stored as files. With --key-file, files are AES-256-GCM encrypted; without, plaintext.",
}

var secretsSetCmd = &cobra.Command{
	Use:   "set <key> <value>",
	Short: "Encrypt and store a secret",
	Args:  cobra.ExactArgs(2),
	RunE:  runSecretsSet,
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
	secretsCmd.PersistentFlags().String("key-file", "", "Path to master encryption key (omit for plaintext mode)")
	secretsCmd.PersistentFlags().String("secrets-dir", "/etc/hobnob-server/secrets", "Directory for secret files")

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

func runSecretsSet(cmd *cobra.Command, args []string) error {
	provider, err := newLocalProvider(cmd)
	if err != nil {
		return err
	}
	defer provider.Close()

	key, value := args[0], args[1]

	// Read from stdin if value is "-".
	if value == "-" {
		data, err := io.ReadAll(os.Stdin)
		if err != nil {
			return fmt.Errorf("reading stdin: %w", err)
		}
		value = strings.TrimRight(string(data), "\n")
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
