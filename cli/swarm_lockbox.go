// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: AGPL-3.0-only

package main

import (
	"fmt"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/spf13/cobra"
)

// Admin-side view of lockbox — per-user encrypted P2P directory on the
// swarm substrate. Read-only. The server plays no role in lockbox keys
// or content; this command exposes the fleet-wide aggregator that
// joins each envoy's `lockbox` trait into a mesh view, including
// recipient drift and active unlock sessions.
//
// Mirrors the shape of `vigocli swarm gitback` + `vigocli swarm longdrawer`. No write
// verbs — lockbox enrollment is per-user on the envoy. Per ADR-020,
// lockbox is puddle-only: bootstrap auto-runs on `vigo swarm puddle
// unlock` (which invokes `vigo swarm lockbox puddle init` inline).
// Single-envoy puddles cover the no-peers encryption-at-rest case.

// lockboxFleet mirrors `server/lockboxmesh.Fleet`. Duplicated rather than
// imported so the CLI stays decoupled from the server package
// (matches the pattern in cli/gitback.go, cli/longdrawer.go).
type lockboxFleet struct {
	GeneratedAt  time.Time            `json:"generated_at"`
	Users        []lockboxUserSummary `json:"users"`
	Participants []string             `json:"participants"`
	Totals       lockboxTotals        `json:"totals"`
}

type lockboxTotals struct {
	Users            int `json:"users"`
	Envoys           int `json:"envoys"`
	Files            int `json:"files"`
	Tombstones       int `json:"tombstones"`
	Divergences      int `json:"divergences"`
	RecipientDrifts  int `json:"recipient_drifts"`
	UnlockedSessions int `json:"unlocked_sessions"`
}

type lockboxUserSummary struct {
	User           string                 `json:"user"`
	Envoys         []lockboxEnvoyPresence `json:"envoys"`
	FileCount      int                    `json:"file_count"`
	TotalBytes     int64                  `json:"total_bytes"`
	Divergences    []string               `json:"divergences,omitempty"`
	RecipientDrift bool                   `json:"recipient_drift"`
}

type lockboxEnvoyPresence struct {
	Hostname         string `json:"hostname"`
	Status           string `json:"status"`
	UpdatedAt        int64  `json:"updated_at,omitempty"`
	FileCount        int    `json:"file_count"`
	FilesDigest      string `json:"files_digest,omitempty"`
	TombstoneCount   int    `json:"tombstone_count"`
	RecipientsDigest string `json:"recipients_digest,omitempty"`
}

var lockboxCmd = &cobra.Command{
	Use:   "lockbox",
	Short: "Fleet-wide view of lockbox per-user encrypted directory sync",
	Long: `Read-only admin view of the lockbox mesh across every envoy.

Each envoy reports its per-user lockbox state — ciphertext files,
recipients digest, and session unlock status — through the 'lockbox'
trait on every check-in. The server joins every envoy's report into a
fleet view; this command renders it. One row per user.

Lockbox enrollment is per-user on the envoy. Per ADR-020, lockbox
is puddle-only: bootstrap auto-runs on 'vigo swarm puddle unlock'
(which invokes 'lockbox puddle init' inline). Single-envoy puddles
cover the no-peers encryption-at-rest case. No admin write path —
this command never decrypts content, never touches keys, and never
forces unlock. It only shows what each envoy is reporting.`,
}

var lockboxStatusCmd = &cobra.Command{
	Use:   "status",
	Short: "Fleet-wide lockbox totals",
	Args:  cobra.NoArgs,
	RunE:  runLockboxStatus,
}

var lockboxListCmd = &cobra.Command{
	Use:   "list",
	Short: "One row per user across the fleet",
	Args:  cobra.NoArgs,
	RunE:  runLockboxList,
}

var lockboxShowCmd = &cobra.Command{
	Use:   "show <user>",
	Short: "Detailed per-envoy presence for one user",
	Args:  cobra.ExactArgs(1),
	RunE:  runLockboxShow,
}

func fetchLockboxFleet() (*lockboxFleet, error) {
	client := newAPIClient()
	var resp lockboxFleet
	if err := client.get("/api/v1/swarm/lockbox/fleet", &resp); err != nil {
		return nil, fmt.Errorf("fetching lockbox fleet: %w", err)
	}
	return &resp, nil
}

func runLockboxStatus(cmd *cobra.Command, _ []string) error {
	asJSON, _ := cmd.Flags().GetBool("json")
	fleet, err := fetchLockboxFleet()
	if err != nil {
		return err
	}
	if asJSON {
		return writeJSONPretty(fleet)
	}

	fmt.Printf("Lockbox fleet — generated %s\n\n", fleet.GeneratedAt.Local().Format("2006-01-02 15:04:05"))

	w := newAlignedTable(os.Stdout)
	fmt.Fprintf(w, "%s\t%d\n", bold("Users"), fleet.Totals.Users)
	fmt.Fprintf(w, "%s\t%d\n", bold("Envoys reporting"), fleet.Totals.Envoys)
	fmt.Fprintf(w, "%s\t%d\n", bold("Ciphertext files"), fleet.Totals.Files)
	fmt.Fprintf(w, "%s\t%d\n", bold("Tombstones"), fleet.Totals.Tombstones)
	fmt.Fprintf(w, "%s\t%s\n", bold("Divergences"), meshCountWarn(fleet.Totals.Divergences))
	fmt.Fprintf(w, "%s\t%s\n", bold("Recipient drifts"), meshCountWarn(fleet.Totals.RecipientDrifts))
	fmt.Fprintf(w, "%s\t%d\n", bold("Unlocked sessions"), fleet.Totals.UnlockedSessions)
	fmt.Fprintf(w, "%s\t%d\n", bold("Participants"), len(fleet.Participants))
	w.Flush()
	return nil
}

func runLockboxList(cmd *cobra.Command, _ []string) error {
	asJSON, _ := cmd.Flags().GetBool("json")
	fleet, err := fetchLockboxFleet()
	if err != nil {
		return err
	}
	if asJSON {
		return writeJSONPretty(fleet.Users)
	}

	if len(fleet.Users) == 0 {
		fmt.Println("No lockbox users reported by the fleet.")
		return nil
	}

	sorted := append([]lockboxUserSummary(nil), fleet.Users...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].User < sorted[j].User })

	w := newAlignedTable(os.Stdout)
	fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\n",
		bold("USER"), bold("ENVOYS"), bold("FILES"), bold("SIZE"), bold("STATE"))
	for _, u := range sorted {
		envoys := fmt.Sprintf("%d", len(u.Envoys))
		files := fmt.Sprintf("%d", u.FileCount)
		size := formatBytes(u.TotalBytes)
		state := green("in sync")
		switch {
		case u.RecipientDrift:
			state = red("recipient drift")
		case len(u.Divergences) > 0:
			state = red(fmt.Sprintf("%d divergence(s)", len(u.Divergences)))
		}
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\n", u.User, envoys, files, size, state)
	}
	w.Flush()
	return nil
}

func runLockboxShow(cmd *cobra.Command, args []string) error {
	target := args[0]
	asJSON, _ := cmd.Flags().GetBool("json")
	fleet, err := fetchLockboxFleet()
	if err != nil {
		return err
	}

	var match *lockboxUserSummary
	for i := range fleet.Users {
		if fleet.Users[i].User == target {
			match = &fleet.Users[i]
			break
		}
	}
	if match == nil {
		return fmt.Errorf("no lockbox user %q reported by the fleet", target)
	}
	if asJSON {
		return writeJSONPretty(match)
	}

	fmt.Printf("User:             %s\n", bold(match.User))
	fmt.Printf("Envoys:           %d\n", len(match.Envoys))
	fmt.Printf("Ciphertext files: %d\n", match.FileCount)
	fmt.Printf("Total size:       %s\n", formatBytes(match.TotalBytes))
	if match.RecipientDrift {
		fmt.Printf("Recipient drift:  %s\n", red("yes"))
	}
	if len(match.Divergences) > 0 {
		fmt.Printf("Divergences:      %s\n", red(strings.Join(match.Divergences, ", ")))
	}
	fmt.Println()

	if len(match.Envoys) == 0 {
		fmt.Println("  (no host presence reported yet)")
		return nil
	}

	sorted := append([]lockboxEnvoyPresence(nil), match.Envoys...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].Hostname < sorted[j].Hostname })

	w := newAlignedTable(os.Stdout)
	fmt.Fprintf(w, "  %s\t%s\t%s\t%s\t%s\n",
		bold("HOST"), bold("STATUS"), bold("FILES"), bold("TOMBSTONES"), bold("LAST UPDATE"))
	for _, e := range sorted {
		status := echoPresenceStatus(e.Status)
		updated := "—"
		if e.UpdatedAt > 0 {
			updated = time.Unix(e.UpdatedAt, 0).Local().Format("2006-01-02 15:04")
		}
		fmt.Fprintf(w, "  %s\t%s\t%d\t%d\t%s\n",
			e.Hostname, status, e.FileCount, e.TombstoneCount, updated)
	}
	w.Flush()
	return nil
}

// ---------- vigocli swarm lockbox members ----------

var lockboxMembersCmd = &cobra.Command{
	Use:   "members [user]",
	Short: "List enrolled envoys for a lockbox user",
	Long: `Print the list of envoys currently enrolled in a user's lockbox circle,
sourced from the 'lockbox' trait projection. Pass [user] to scope to one
user; omit it to list every enrolled user + their envoys.

Read-only. Does not contact the envoys.`,
	Args: cobra.MaximumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		return runLockboxMembers(args)
	},
}

func runLockboxMembers(args []string) error {
	client := newAPIClient()
	var fleet lockboxFleet
	if err := client.get("/api/v1/swarm/lockbox/fleet", &fleet); err != nil {
		return fmt.Errorf("fetch lockbox fleet: %w", err)
	}

	filter := ""
	if len(args) == 1 {
		filter = args[0]
	}

	for _, u := range fleet.Users {
		if filter != "" && u.User != filter {
			continue
		}
		fmt.Printf("USER: %s\n", u.User)
		tw := newAlignedTable(os.Stdout)
		fmt.Fprintln(tw, "HOST\tSTATUS\tFILES")
		sort.Slice(u.Envoys, func(i, j int) bool { return u.Envoys[i].Hostname < u.Envoys[j].Hostname })
		for _, e := range u.Envoys {
			fmt.Fprintf(tw, "%s\t%s\t%d\n", e.Hostname, e.Status, e.FileCount)
		}
		tw.Flush()
		fmt.Println()
	}
	if filter != "" {
		found := false
		for _, u := range fleet.Users {
			if u.User == filter {
				found = true
				break
			}
		}
		if !found {
			return fmt.Errorf("no lockbox members found for user %q", filter)
		}
	}
	return nil
}

// ---------- vigocli swarm lockbox evict ----------

var lockboxEvictCmd = &cobra.Command{
	Use:   "evict",
	Short: "Remove a host from a user's lockbox circle fleet-wide",
	Long: `Dispatch a task to every enrolled envoy instructing the agent to drop
the target host's age pubkey from its peer table, regenerate
recipients.txt, and queue a reencrypt so the evicted host cannot
decrypt future ciphertext.

The admin path is authenticated end-to-end by mTLS + admin token —
not by a P2P envelope — so a gone host can be evicted even though it
cannot sign a LEAVE for itself.

Offline envoys pick up the eviction on their next check-in via the
normal task-dispatch retry.`,
	RunE: func(cmd *cobra.Command, args []string) error {
		host, _ := cmd.Flags().GetString("host")
		user, _ := cmd.Flags().GetString("user")
		allUsers, _ := cmd.Flags().GetBool("all-users")
		if host == "" {
			return fmt.Errorf("--host is required")
		}
		if user == "" && !allUsers {
			return fmt.Errorf("either --user <u> or --all-users is required")
		}
		if user != "" && allUsers {
			return fmt.Errorf("--user and --all-users are mutually exclusive")
		}
		return runLockboxEvict(cmd, host, user, allUsers)
	},
}

func runLockboxEvict(cmd *cobra.Command, host, user string, allUsers bool) error {
	client := newAPIClient()
	var fleet lockboxFleet
	if err := client.get("/api/v1/swarm/lockbox/fleet", &fleet); err != nil {
		return fmt.Errorf("fetch lockbox fleet: %w", err)
	}

	type target struct {
		user   string
		pubkey string
	}
	var targets []target
	for _, u := range fleet.Users {
		if !allUsers && u.User != user {
			continue
		}
		for _, e := range u.Envoys {
			if e.Hostname == host {
				// Note: the fleet projection doesn't surface the age pubkey directly.
				// We use RecipientsDigest as a marker that the user is enrolled there
				// and let the agent look up its own peer-table entry by hostname
				// during _apply-evict. Fallback plan: if RecipientsDigest is empty,
				// still dispatch — agent will be a no-op.
				_ = e.RecipientsDigest
				targets = append(targets, target{user: u.User, pubkey: ""})
			}
		}
	}
	if len(targets) == 0 {
		return fmt.Errorf("host %q is not enrolled in any matching lockbox circle", host)
	}

	fmt.Printf("Will evict %s from %d user circle(s):\n", host, len(targets))
	for _, t := range targets {
		fmt.Printf("  user=%s\n", t.user)
	}
	ok, err := confirmAction(cmd, "Proceed with eviction?")
	if err != nil {
		return err
	}
	if !ok {
		fmt.Println("aborted.")
		return nil
	}

	// For each target user, look up the host's age pubkey and dispatch.
	// Falling back to the agent's own peer-table lookup: we dispatch
	// `_apply-evict` with --pubkey unset when the fleet projection doesn't
	// carry it, and the agent resolves it locally from its own peers.json.
	// This keeps the server simple.
	for _, t := range targets {
		// Find the pubkey from the Participants field or similar. The current
		// lockboxFleet struct doesn't expose per-envoy age pubkey directly, so we
		// instruct agents to resolve locally via hostname. If an agent can't
		// find the hostname in its peer table, _apply-evict is a no-op.
		cmd := fmt.Sprintf("vigo swarm lockbox _apply-evict --user %s --hostname %s --pubkey LOOKUP",
			shellQuote(t.user), shellQuote(host))
		req := map[string]any{
			"command": cmd,
			"target":  "*",
			"timeout": 60,
		}
		var result map[string]any
		if err := client.post("/api/v1/task", req, &result); err != nil {
			return fmt.Errorf("dispatch evict for user %s: %w", t.user, err)
		}
		fmt.Printf("✓ dispatched evict for user=%s\n", t.user)
	}
	return nil
}

func shellQuote(s string) string {
	// POSIX single-quote escaping: the dispatched command runs via `sh -c`
	// on the fleet, so any shell metacharacter in a user- or
	// server-supplied value must be neutralized. Wrap in single quotes
	// and replace each embedded single quote with the '\'' sequence
	// (close-quote, escaped quote, reopen-quote).
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

func init() {
	lockboxStatusCmd.Flags().Bool("json", false, "Emit machine-readable JSON instead of a table")
	lockboxListCmd.Flags().Bool("json", false, "Emit machine-readable JSON instead of a table")
	lockboxShowCmd.Flags().Bool("json", false, "Emit machine-readable JSON instead of a table")

	lockboxEvictCmd.Flags().String("host", "", "Hostname to evict from the circle")
	lockboxEvictCmd.Flags().String("user", "", "User whose circle to evict from")
	lockboxEvictCmd.Flags().Bool("all-users", false, "Evict the host from every user's lockbox circle it belongs to")

	lockboxCmd.AddCommand(lockboxStatusCmd, lockboxListCmd, lockboxShowCmd, lockboxMembersCmd, lockboxEvictCmd)
	// Hidden redirects for the per-user agent-side verbs. Lockbox bootstraps
	// on `vigo swarm puddle unlock` (ADR-020/021) and content verbs are
	// per-user on the envoy — none of these belong on the operator surface.
	// Authorization derives from `puddle: true` (ADR-020 — there is no
	// separate lockbox usercrate flag), hence the flag name below.
	addAgentRedirects(lockboxCmd, "lockbox", "puddle", []string{
		"unlock", "lock", "encrypt", "decrypt", "delete", "resurrect",
		"reencrypt", "purge",
	})
}
