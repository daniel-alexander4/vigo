// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: BUSL-1.1

package main

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"
)

const defaultKeyDir = "/srv/hobnob/license-keys"

var keyDir string

var rootCmd = &cobra.Command{
	Use:   "hobnob-license-manager",
	Short: "Internal tool for managing Hobnob licenses",
	Long:  "hobnob-license-manager generates Ed25519 keypairs, creates and tracks signed license files, and manages customer license lifecycle.",
}

func main() {
	if err := rootCmd.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func init() {
	envKeyDir := os.Getenv("HOBNOB_KEY_DIR")
	if envKeyDir == "" {
		envKeyDir = defaultKeyDir
	}
	rootCmd.PersistentFlags().StringVar(&keyDir, "key-dir", envKeyDir,
		"Directory for keys and license database")

	rootCmd.AddCommand(keysCmd)
	rootCmd.AddCommand(licenseCmd)
	rootCmd.AddCommand(customerCmd)
}
