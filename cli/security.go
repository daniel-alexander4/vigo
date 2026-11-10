// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: AGPL-3.0-only

package main

import "github.com/spf13/cobra"

var securityCmd = &cobra.Command{
	Use:   "security",
	Short: "Security posture: CVE search, risk scoring, and audit trail",
}

// Subcommands are added in their respective files:
//   cve.go       → securityCmd.AddCommand(cveCmd)
//   risk.go      → securityCmd.AddCommand(riskCmd)
//   audit.go     → securityCmd.AddCommand(auditCmd)
