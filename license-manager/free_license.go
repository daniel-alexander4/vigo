// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/daniel-alexander4/vigo/server/license"
	"github.com/spf13/cobra"
)

// Flags for license free-license.
var (
	freeMaxEnvoys int
	freeOut       string
	freeInject    bool
	freeRepoPath  string
)

var licenseFreeLicenseCmd = &cobra.Command{
	Use:   "free-license",
	Short: "Regenerate the built-in community license embedded in server/license/free.go",
	Long: `Regenerate the pre-signed community license that ships inside vigosrv.

Signs a new free-tier payload with --max-envoys using the private key at
$VIGO_KEY_DIR/vigo_private.key, then either prints the JSON blob to stdout
(--out) or injects it directly into server/license/free.go (--inject).

Run after any change to the Community tier node cap, then rebuild vigosrv.`,
	RunE: runLicenseFreeLicense,
}

func init() {
	licenseFreeLicenseCmd.Flags().IntVar(&freeMaxEnvoys, "max-envoys", 0, "Community tier node cap (required)")
	licenseFreeLicenseCmd.Flags().StringVar(&freeOut, "out", "", "Write signed JSON to this path (mutually exclusive with --inject)")
	licenseFreeLicenseCmd.Flags().BoolVar(&freeInject, "inject", false, "Inject directly into server/license/free.go")
	licenseFreeLicenseCmd.Flags().StringVar(&freeRepoPath, "repo", "", "Path to vigo repo for --inject (default: /home/dan/repos/vigo, then auto-detect)")
	licenseFreeLicenseCmd.MarkFlagRequired("max-envoys")

	licenseCmd.AddCommand(licenseFreeLicenseCmd)
}

func runLicenseFreeLicense(cmd *cobra.Command, args []string) error {
	if freeMaxEnvoys <= 0 {
		return fmt.Errorf("--max-envoys must be positive")
	}
	if freeOut == "" && !freeInject {
		return fmt.Errorf("must specify either --out <path> or --inject")
	}
	if freeOut != "" && freeInject {
		return fmt.Errorf("--out and --inject are mutually exclusive")
	}

	payload := license.Payload{
		CustomerName: "Community",
		CustomerID:   "community",
		LicenseID:    fmt.Sprintf("free-%d", freeMaxEnvoys),
		Tier:         "free",
		MaxEnvoys:   freeMaxEnvoys,
		IssuedAt:     "2026-03-12",
		ExpiresAt:    "2099-12-31",
		Version:      1,
	}

	lic, err := signPayload(payload)
	if err != nil {
		return err
	}

	blob, err := indentedBlob(lic)
	if err != nil {
		return err
	}

	if freeOut != "" {
		if err := os.WriteFile(freeOut, []byte(blob+"\n"), 0644); err != nil {
			return fmt.Errorf("writing %s: %w", freeOut, err)
		}
		fmt.Printf("Signed free-%d license written to %s\n", freeMaxEnvoys, freeOut)
		return nil
	}

	repoPath := freeRepoPath
	if repoPath == "" {
		repoPath = resolveRepoPath()
	}
	targetFile := filepath.Join(repoPath, "server", "license", "free.go")
	if err := injectFreeLicense(targetFile, blob); err != nil {
		return err
	}
	fmt.Printf("Injected free-%d license into %s\n", freeMaxEnvoys, targetFile)
	fmt.Println("Rebuild vigosrv to pick up the new embedded license.")
	return nil
}

// indentedBlob renders a License as a 2-space-indented JSON string suitable
// for embedding as a Go raw string literal.
func indentedBlob(lic *license.License) (string, error) {
	data, err := json.MarshalIndent(lic, "", "  ")
	if err != nil {
		return "", fmt.Errorf("marshaling license: %w", err)
	}
	return string(data), nil
}

// injectFreeLicense rewrites the freeLicenseJSON constant in server/license/free.go.
// It finds the existing backtick-delimited literal and replaces its contents.
func injectFreeLicense(path, blob string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("reading %s: %w", path, err)
	}
	src := string(data)

	marker := "const freeLicenseJSON = `"
	start := strings.Index(src, marker)
	if start < 0 {
		return fmt.Errorf("could not find freeLicenseJSON constant in %s", path)
	}
	literalStart := start + len(marker)
	end := strings.Index(src[literalStart:], "`")
	if end < 0 {
		return fmt.Errorf("could not find closing backtick for freeLicenseJSON in %s", path)
	}
	literalEnd := literalStart + end

	out := src[:literalStart] + blob + src[literalEnd:]
	if err := os.WriteFile(path, []byte(out), 0644); err != nil {
		return fmt.Errorf("writing %s: %w", path, err)
	}
	return nil
}
