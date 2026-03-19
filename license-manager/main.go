// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

package main

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"
)

const defaultKeyDir = "/srv/vigo/license/keys"

var keyDir string

var rootCmd = &cobra.Command{
	Use:   "vigo-license-manager",
	Short: "Internal tool for managing Vigo licenses",
	Long:  "vigo-license-manager generates Ed25519 keypairs, creates and tracks signed license files, and manages customer license lifecycle.",
}

func main() {
	if err := rootCmd.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func init() {
	envKeyDir := os.Getenv("VIGO_KEY_DIR")
	if envKeyDir == "" {
		envKeyDir = defaultKeyDir
	}
	rootCmd.PersistentFlags().StringVar(&keyDir, "key-dir", envKeyDir,
		"Directory for keys and license database")

	rootCmd.AddCommand(keysCmd)
	rootCmd.AddCommand(licenseCmd)
	rootCmd.AddCommand(customerCmd)
}
