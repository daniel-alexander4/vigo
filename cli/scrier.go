// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: Apache-2.0

package main

import "github.com/spf13/cobra"

// scrierCmd is the root for Scrier remote-access verbs. Subcommands attach in
// their own files (scrier_ssh.go etc.). There is no operator-managed
// web-to-OS-user mapping any more — the agent identifies the OS user by
// scanning every regular user's authorized_keys for the web user's pubkey.
var scrierCmd = &cobra.Command{
	Use:   "scrier",
	Short: "Manage Scrier remote access",
}
