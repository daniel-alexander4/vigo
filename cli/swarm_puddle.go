// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

package main

import (
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"
)

// Admin-side view of puddle — the per-user trust anchor every swarm
// content subsystem builds on (ADR-014, ADR-020, ADR-021). Most puddle
// lifecycle (`init`, `pair`, `join`, `unlock`, `lock`, `leave`,
// `rotate-passphrase`, `rekey-pair`, `rekey`, `name`) is per-user and
// lives on the agent CLI. The observability + cross-envoy admin verbs
// surface here.

var puddleCmd = &cobra.Command{
	Use:   "puddle",
	Short: "Fleet view + admin operations for the puddle primitive",
	Long: `Read-only fleet view and cross-envoy admin operations for the puddle
primitive (ADR-014) — the per-user identity that gitback, curator,
poolq, lockbox, and longdrawer all build on.

'list' / 'show' render the fleet puddle topology: which puddles exist,
who owns each (its friendly name, ADR-022), which envoys hold it, and
whether it's healthy. Most lifecycle verbs (init, pair, join, leave,
unlock, lock, rotate-passphrase, rekey-pair, rekey, name) are
per-user-per-envoy and live on the agent CLI ('vigo swarm puddle
<verb>'). The admin-authority verbs that span envoys (evict) live here.`,
}

// ---------- shared fleet shape (mirrors server/swarm/puddlemesh.Fleet) ----------

type puddleFleetResponse struct {
	GeneratedAt time.Time            `json:"generated_at"`
	Puddles     []puddleSummaryResp  `json:"puddles"`
	Totals      puddleTotalsResponse `json:"totals"`
}

type puddleTotalsResponse struct {
	Puddles   int `json:"puddles"`
	Envoys    int `json:"envoys"`
	Named     int `json:"named"`
	Unlocked  int `json:"unlocked"`
	Unhealthy int `json:"unhealthy"`
}

type puddleSummaryResp struct {
	PuddlePubkey    string                `json:"puddle_pubkey"`
	Owner           string                `json:"owner"`
	OwnerContested  bool                  `json:"owner_contested"`
	Envoys          []puddleEnvoyPresence `json:"envoys"`
	EnvoyCount      int                   `json:"envoy_count"`
	Users           []string              `json:"users"`
	SessionUnlocked bool                  `json:"session_unlocked"`
	RetiredCount    int                   `json:"retired_count"`
	RekeyPending    bool                  `json:"rekey_pending"`
	LastUpdated     int64                 `json:"last_updated"`
	Health          puddleHealthResp      `json:"health"`
}

type puddleHealthResp struct {
	Band    string   `json:"band"`
	Reasons []string `json:"reasons"`
}

type puddleEnvoyPresence struct {
	Hostname        string `json:"hostname"`
	User            string `json:"user"`
	Status          string `json:"status"`
	UpdatedAt       int64  `json:"updated_at"`
	Initialized     bool   `json:"initialized"`
	SessionUnlocked bool   `json:"session_unlocked"`
	PairPending     bool   `json:"pair_pending"`
	RekeyPending    bool   `json:"rekey_pending"`
}

func fetchPuddleFleet() (*puddleFleetResponse, error) {
	client := newAPIClient()
	var fleet puddleFleetResponse
	if err := client.get("/api/v1/swarm/puddle/fleet", &fleet); err != nil {
		return nil, fmt.Errorf("fetch puddle fleet: %w", err)
	}
	return &fleet, nil
}

// ---------- vigocli swarm puddle list ----------

var puddleListCmd = &cobra.Command{
	Use:   "list",
	Short: "One row per puddle across the fleet — owner, envoys, health",
	Args:  cobra.NoArgs,
	RunE:  runPuddleList,
}

func runPuddleList(cmd *cobra.Command, _ []string) error {
	asJSON, _ := cmd.Flags().GetBool("json")
	fleet, err := fetchPuddleFleet()
	if err != nil {
		return err
	}
	if asJSON {
		return writeJSONPretty(fleet.Puddles)
	}
	if len(fleet.Puddles) == 0 {
		fmt.Println("No puddles in the fleet yet.")
		fmt.Println("Create one on an envoy with:  vigo swarm puddle init")
		return nil
	}

	w := newAlignedTable(os.Stdout)
	fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\n",
		bold("OWNER"), bold("PUBKEY"), bold("ENVOYS"), bold("USERS"), bold("UNLOCKED"), bold("HEALTH"))
	for _, p := range fleet.Puddles {
		unlocked := "—"
		if p.SessionUnlocked {
			unlocked = green("yes")
		}
		fmt.Fprintf(w, "%s\ted25519:%s\t%d\t%s\t%s\t%s\n",
			puddleOwnerLabel(p), shortPubkey(p.PuddlePubkey), p.EnvoyCount,
			strings.Join(p.Users, ","), unlocked, healthBadge(p.Health.Band))
	}
	w.Flush()

	fmt.Printf("\n%d puddle(s) · %d envoy(s) · %d named · %d unlocked",
		fleet.Totals.Puddles, fleet.Totals.Envoys, fleet.Totals.Named, fleet.Totals.Unlocked)
	if fleet.Totals.Unhealthy > 0 {
		fmt.Printf(" · %s", yellow(fmt.Sprintf("%d need attention", fleet.Totals.Unhealthy)))
	}
	fmt.Println()
	return nil
}

// ---------- vigocli swarm puddle show ----------

var puddleShowCmd = &cobra.Command{
	Use:   "show <name|pubkey>",
	Short: "Detailed view of one puddle — member envoys + health reasons",
	Args:  cobra.ExactArgs(1),
	RunE:  runPuddleShow,
}

func runPuddleShow(cmd *cobra.Command, args []string) error {
	target := args[0]
	asJSON, _ := cmd.Flags().GetBool("json")
	fleet, err := fetchPuddleFleet()
	if err != nil {
		return err
	}
	match, err := resolvePuddle(fleet.Puddles, target)
	if err != nil {
		return err
	}
	if asJSON {
		return writeJSONPretty(match)
	}

	fmt.Printf("Owner:        %s\n", puddleOwnerLabel(*match))
	fmt.Printf("Puddle key:   ed25519:%s\n", match.PuddlePubkey)
	fmt.Printf("Users:        %s\n", strings.Join(match.Users, ", "))
	fmt.Printf("Health:       %s\n", healthBadge(match.Health.Band))
	for _, r := range match.Health.Reasons {
		fmt.Printf("              · %s\n", r)
	}
	if match.RetiredCount > 0 {
		fmt.Printf("Rotations:    %d retired key(s)\n", match.RetiredCount)
	}
	fmt.Println()

	if len(match.Envoys) == 0 {
		fmt.Println("  (no envoy presence reported yet)")
		return nil
	}
	w := newAlignedTable(os.Stdout)
	fmt.Fprintf(w, "  %s\t%s\t%s\t%s\t%s\t%s\n",
		bold("HOST"), bold("USER"), bold("STATUS"), bold("INIT"), bold("UNLOCKED"), bold("FLAGS"))
	for _, e := range match.Envoys {
		fmt.Fprintf(w, "  %s\t%s\t%s\t%s\t%s\t%s\n",
			e.Hostname, e.User, echoPresenceStatus(e.Status),
			yesNo(e.Initialized), yesNo(e.SessionUnlocked), puddleEnvoyFlags(e))
	}
	w.Flush()
	return nil
}

// resolvePuddle finds a puddle by exact owner name or by a pubkey
// prefix that uniquely identifies one puddle.
func resolvePuddle(puddles []puddleSummaryResp, target string) (*puddleSummaryResp, error) {
	for i := range puddles {
		if puddles[i].Owner != "" && puddles[i].Owner == target {
			return &puddles[i], nil
		}
	}
	t := strings.TrimPrefix(strings.ToLower(target), "ed25519:")
	var matches []*puddleSummaryResp
	for i := range puddles {
		if strings.HasPrefix(strings.ToLower(puddles[i].PuddlePubkey), t) {
			matches = append(matches, &puddles[i])
		}
	}
	switch len(matches) {
	case 1:
		return matches[0], nil
	case 0:
		return nil, fmt.Errorf("no puddle matches %q (try an owner name or a pubkey prefix; see `vigocli swarm puddle list`)", target)
	default:
		return nil, fmt.Errorf("%q is ambiguous — %d puddles share that pubkey prefix; use more characters", target, len(matches))
	}
}

func puddleOwnerLabel(p puddleSummaryResp) string {
	if p.OwnerContested {
		return yellow("(contested)")
	}
	if p.Owner == "" {
		return "(unnamed)"
	}
	return p.Owner
}

func puddleEnvoyFlags(e puddleEnvoyPresence) string {
	var flags []string
	if e.PairPending {
		flags = append(flags, "pair-pending")
	}
	if e.RekeyPending {
		flags = append(flags, "rekey-pending")
	}
	if len(flags) == 0 {
		return "—"
	}
	return strings.Join(flags, ",")
}

func healthBadge(band string) string {
	switch band {
	case "healthy":
		return green("healthy")
	case "warning":
		return yellow("warning")
	case "degraded":
		return red("degraded")
	default:
		return band
	}
}

func yesNo(b bool) string {
	if b {
		return "yes"
	}
	return "no"
}

// shortPubkey trims a 64-char hex pubkey to a head…tail form for table
// display; leaves shorter strings untouched.
func shortPubkey(hex string) string {
	if len(hex) <= 20 {
		return hex
	}
	return hex[:12] + "…" + hex[len(hex)-8:]
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
		if user == "" && !allUsers {
			return fmt.Errorf("either --user <u> or --all-users is required")
		}
		if user != "" && allUsers {
			return fmt.Errorf("--user and --all-users are mutually exclusive")
		}
		return runLockboxEvict(cmd, host, user, allUsers)
	},
}

func init() {
	puddleListCmd.Flags().Bool("json", false, "Emit the fleet puddle list as JSON")
	puddleShowCmd.Flags().Bool("json", false, "Emit the puddle detail as JSON")

	puddleEvictCmd.Flags().String("user", "", "User whose puddle to evict from")
	puddleEvictCmd.Flags().Bool("all-users", false, "Evict the host from every user's puddle it belongs to")

	puddleCmd.AddCommand(puddleListCmd)
	puddleCmd.AddCommand(puddleShowCmd)
	puddleCmd.AddCommand(puddleEvictCmd)
	// Hidden redirects for the per-user agent-side verbs. Puddle identity
	// is per-Unix-user; every lifecycle + signing verb lives on
	// `vigo swarm puddle` and signs with the user's own ~/.vigo-puddle/
	// identity. The operator-side `vigocli swarm puddle` is fleet
	// observability (list, show) + admin authority (evict). This is the
	// most common mis-invocation — `vigo swarm puddle status` run as root
	// was the original trigger for adding these redirects.
	addAgentRedirects(puddleCmd, "puddle", "puddle", []string{
		"init", "pubkey", "unlock", "lock", "pair", "join", "leave",
		"rekey-pair", "rekey", "members", "rotate-passphrase", "status", "name",
	})
}
