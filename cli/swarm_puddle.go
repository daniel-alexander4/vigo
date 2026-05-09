// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

package main

import (
	"fmt"

	"github.com/spf13/cobra"
)

// Admin-side view of puddle — per-user trust anchor (ADR-014, ADR-020,
// ADR-021). Most puddle lifecycle (`init`, `pair`, `join`, `unlock`,
// `lock`, `leave`, `rotate-passphrase`, `rekey-pair`, `rekey`) is
// per-user and lives on the agent CLI. The verbs that surface here
// are the ones that span envoys and require admin authority via the
// server's TaskDispatch path.

var puddleCmd = &cobra.Command{
	Use:   "puddle",
	Short: "Admin-side puddle operations across the fleet",
	Long: `Cross-envoy admin operations for the puddle primitive (ADR-014).

Most puddle lifecycle verbs (init, pair, join, leave, unlock, lock,
rotate-passphrase, rekey-pair, rekey) are per-user-per-envoy and live
on the agent CLI ('vigo swarm puddle <verb>'). The admin-authority
verbs that span envoys live here.

Per ADR-020 + ADR-021, lockbox is mandatory on puddle membership
and 'puddle evict' is the canonical fleet-wide eviction path —
covering lockbox recipient cleanup as a side effect. The legacy
'vigocli swarm lockbox evict' is retained as an alias during the
0.40.0 transition.`,
}

// ---------- vigocli swarm puddle evict ----------

var puddleEvictCmd = &cobra.Command{
	Use:   "evict <host>",
	Short: "Evict a host from a puddle (cross-envoy)",
	Long: `Evict a host from a user's puddle fleet-wide. Triggers lockbox
peer-table cleanup, recipients.txt regeneration, and a queued
reencrypt on every remaining puddle member so the evicted host
cannot decrypt future ciphertext. Authenticated end-to-end by mTLS
+ admin token via the server's TaskDispatch path; the evicted host
does not need to cooperate.

Offline envoys pick up the eviction on their next check-in via the
normal task-dispatch retry.

Per ADR-021, this is the canonical admin-side eviction verb. The
older 'vigocli swarm lockbox evict' delegates here; both are
accepted during the 0.40.0 transition.`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		host := args[0]
		user, _ := cmd.Flags().GetString("user")
		allUsers, _ := cmd.Flags().GetBool("all-users")
		yes, _ := cmd.Flags().GetBool("yes")
		if user == "" && !allUsers {
			return fmt.Errorf("either --user <u> or --all-users is required")
		}
		if user != "" && allUsers {
			return fmt.Errorf("--user and --all-users are mutually exclusive")
		}
		// Delegate to the existing lockbox-evict implementation:
		// today's puddle-eviction is structurally a lockbox eviction
		// (peer-table drop + recipients regen + reencrypt). When
		// gitmirror project-revoke fan-out lands, this verb will also
		// dispatch the per-project revokes — see ADR-021 follow-up.
		return runLockboxEvict(host, user, allUsers, yes)
	},
}

func init() {
	puddleEvictCmd.Flags().String("user", "", "User whose puddle to evict from")
	puddleEvictCmd.Flags().Bool("all-users", false, "Evict the host from every user's puddle it belongs to")
	puddleEvictCmd.Flags().Bool("yes", false, "Skip the confirmation prompt")

	puddleCmd.AddCommand(puddleEvictCmd)
}
