// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

package main

import (
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"text/tabwriter"
	"time"

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
		// gitback project-revoke fan-out lands, this verb will also
		// dispatch the per-project revokes — see ADR-021 follow-up.
		return runLockboxEvict(host, user, allUsers, yes)
	},
}

// ---------- vigocli swarm puddle names ----------

var puddleNamesCmd = &cobra.Command{
	Use:   "names",
	Short: "Fleet view of friendly puddle names (ADR-022)",
	Long: `Friendly names map a human-meaningful label to a puddle pubkey, so
gitback://<name>/<repo> can be used alongside the self-certifying
gitback://<project_id>/<repo> form. Names are first-come and
cooperatively enforced; the server publishes the fleet name map and
arbitrates nothing — a name with competing claims is shown with all
of them, and resolvers refuse it. Set / clear a name on an envoy with
'vigo swarm puddle name set|clear'.`,
}

var puddleNamesListCmd = &cobra.Command{
	Use:   "list",
	Short: "Every claimed puddle name across the fleet, collisions flagged",
	Args:  cobra.NoArgs,
	RunE:  runPuddleNamesList,
}

// puddleNamesResponse mirrors server/swarm/gitbackmesh.FleetNames.
// Duplicated here so the CLI stays decoupled from the server package.
type puddleNamesResponse struct {
	UpdatedAt int64                        `json:"updated_at"`
	Names     map[string][]puddleNameClaim `json:"names"`
}

type puddleNameClaim struct {
	Body      puddleNameClaimBody `json:"body"`
	Signature string              `json:"signature"`
}

type puddleNameClaimBody struct {
	PuddlePubkey string          `json:"puddle_pubkey"`
	Name         string          `json:"name"`
	ClaimedAt    int64           `json:"claimed_at"`
	Supersedes   json.RawMessage `json:"supersedes,omitempty"`
}

func runPuddleNamesList(cmd *cobra.Command, _ []string) error {
	asJSON, _ := cmd.Flags().GetBool("json")
	client := newAPIClient()
	var resp puddleNamesResponse
	if err := client.get("/api/v1/swarm/puddle/names", &resp); err != nil {
		return fmt.Errorf("fetching puddle name map: %w", err)
	}
	if asJSON {
		return writeJSONPretty(resp)
	}
	if len(resp.Names) == 0 {
		fmt.Println("No puddle names claimed in the fleet yet.")
		fmt.Println("Claim one on an envoy with:  vigo swarm puddle name set <name>")
		return nil
	}
	when := "(unknown)"
	if resp.UpdatedAt > 0 {
		when = time.Unix(resp.UpdatedAt, 0).Local().Format("2006-01-02 15:04:05")
	}
	fmt.Printf("Puddle names — fleet map generated %s\n\n", when)

	names := make([]string, 0, len(resp.Names))
	for n := range resp.Names {
		names = append(names, n)
	}
	sort.Strings(names)

	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintf(w, "%s\t%s\t%s\t%s\n", bold("NAME"), bold("PUDDLE PUBKEY"), bold("CLAIMED"), bold("STATUS"))
	collisions := 0
	for _, n := range names {
		claims := resp.Names[n]
		collision := len(claims) > 1
		if collision {
			collisions++
		}
		for i, c := range claims {
			label := n
			status := "ok"
			if collision {
				status = fmt.Sprintf("COLLISION — %d competing claims", len(claims))
			}
			if i > 0 {
				label = ""
				status = ""
			}
			claimed := "(unknown)"
			if c.Body.ClaimedAt > 0 {
				claimed = time.Unix(c.Body.ClaimedAt, 0).Local().Format("2006-01-02")
			}
			sup := ""
			if len(c.Body.Supersedes) > 2 { // anything beyond "[]"
				sup = " (re-found)"
			}
			fmt.Fprintf(w, "%s\ted25519:%s%s\t%s\t%s\n", label, shortPubkey(c.Body.PuddlePubkey), sup, claimed, status)
		}
	}
	w.Flush()
	if collisions > 0 {
		fmt.Printf("\n%d name(s) have competing claims; resolvers refuse those and require gitback://<project_id>/<repo>.\n", collisions)
	}
	return nil
}

// shortPubkey trims a 64-char hex pubkey to a head…tail form for
// table display; leaves shorter strings untouched.
func shortPubkey(hex string) string {
	if len(hex) <= 20 {
		return hex
	}
	return hex[:12] + "…" + hex[len(hex)-8:]
}

func init() {
	puddleEvictCmd.Flags().String("user", "", "User whose puddle to evict from")
	puddleEvictCmd.Flags().Bool("all-users", false, "Evict the host from every user's puddle it belongs to")
	puddleEvictCmd.Flags().Bool("yes", false, "Skip the confirmation prompt")

	puddleNamesListCmd.Flags().Bool("json", false, "Emit the raw fleet name map as JSON")
	puddleNamesCmd.AddCommand(puddleNamesListCmd)

	puddleCmd.AddCommand(puddleEvictCmd)
	puddleCmd.AddCommand(puddleNamesCmd)
}
