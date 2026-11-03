// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: Apache-2.0

package main

import "github.com/spf13/cobra"

var complianceCmd = &cobra.Command{
	Use:   "compliance",
	Short: "Compliance reporting, evidence export, and remediation workflows",
}

// Subcommands are added in their respective files:
//   report.go    → complianceCmd.AddCommand(reportCmd)
//   export.go    → complianceCmd.AddCommand(exportCmd)
//   report.go    → complianceCmd.AddCommand(remediationCmd)
