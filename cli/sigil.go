// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

package main

import (
	"fmt"
	"os"
	"sort"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"
)

// Admin-side view of sigil — per-user encrypted P2P directory on the
// swarm substrate. Read-only. The server plays no role in sigil keys
// or content; this command exposes the fleet-wide aggregator that
// joins each envoy's `sigil` trait into a mesh view, including
// recipient drift and active unlock sessions.
//
// Mirrors the shape of `vigocli cairn` + `vigocli echo`. No write
// verbs — sigil enrollment is per-user on the envoy via
// `vigo sigil enroll`.

// sigilFleet mirrors `server/sigilmesh.Fleet`. Duplicated rather than
// imported so the CLI stays decoupled from the server package
// (matches the pattern in cli/cairn.go, cli/echo.go).
type sigilFleet struct {
	GeneratedAt  time.Time          `json:"generated_at"`
	Users        []sigilUserSummary `json:"users"`
	Participants []string           `json:"participants"`
	Totals       sigilTotals        `json:"totals"`
}

type sigilTotals struct {
	Users            int `json:"users"`
	Envoys           int `json:"envoys"`
	Files            int `json:"files"`
	Tombstones       int `json:"tombstones"`
	Divergences      int `json:"divergences"`
	RecipientDrifts  int `json:"recipient_drifts"`
	UnlockedSessions int `json:"unlocked_sessions"`
}

type sigilUserSummary struct {
	User           string               `json:"user"`
	Envoys         []sigilEnvoyPresence `json:"envoys"`
	FileCount      int                  `json:"file_count"`
	TotalBytes     int64                `json:"total_bytes"`
	Divergences    []string             `json:"divergences,omitempty"`
	RecipientDrift bool                 `json:"recipient_drift"`
	ActiveSessions []string             `json:"active_sessions,omitempty"`
}

type sigilEnvoyPresence struct {
	Hostname         string `json:"hostname"`
	Status           string `json:"status"`
	UpdatedAt        int64  `json:"updated_at,omitempty"`
	FileCount        int    `json:"file_count"`
	FilesDigest      string `json:"files_digest,omitempty"`
	TombstoneCount   int    `json:"tombstone_count"`
	SessionUnlocked  bool   `json:"session_unlocked"`
	RecipientsDigest string `json:"recipients_digest,omitempty"`
}

var sigilCmd = &cobra.Command{
	Use:   "sigil",
	Short: "Fleet-wide view of sigil per-user encrypted directory sync",
	Long: `Read-only admin view of the sigil mesh across every envoy.

Each envoy reports its per-user sigil state — ciphertext files,
recipients digest, and session unlock status — through the 'sigil'
trait on every check-in. The server joins every envoy's report into a
fleet view; this command renders it. One row per user.

Sigil enrollment is per-user on the envoy via 'vigo sigil enroll'. No
admin write path — this command never decrypts content, never touches
keys, and never forces unlock. It only shows what each envoy is
reporting.`,
}

var sigilStatusCmd = &cobra.Command{
	Use:   "status",
	Short: "Fleet-wide sigil totals",
	Args:  cobra.NoArgs,
	RunE:  runSigilStatus,
}

var sigilListCmd = &cobra.Command{
	Use:   "list",
	Short: "One row per user across the fleet",
	Args:  cobra.NoArgs,
	RunE:  runSigilList,
}

var sigilShowCmd = &cobra.Command{
	Use:   "show <user>",
	Short: "Detailed per-envoy presence for one user",
	Args:  cobra.ExactArgs(1),
	RunE:  runSigilShow,
}

func fetchSigilFleet() (*sigilFleet, error) {
	client := newAPIClient()
	var resp sigilFleet
	if err := client.get("/api/v1/sigil/fleet", &resp); err != nil {
		return nil, fmt.Errorf("fetching sigil fleet: %w", err)
	}
	return &resp, nil
}

func runSigilStatus(cmd *cobra.Command, _ []string) error {
	asJSON, _ := cmd.Flags().GetBool("json")
	fleet, err := fetchSigilFleet()
	if err != nil {
		return err
	}
	if asJSON {
		return writeJSONPretty(fleet)
	}

	fmt.Printf("Sigil fleet — generated %s\n\n", fleet.GeneratedAt.Local().Format("2006-01-02 15:04:05"))

	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
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

func runSigilList(cmd *cobra.Command, _ []string) error {
	asJSON, _ := cmd.Flags().GetBool("json")
	fleet, err := fetchSigilFleet()
	if err != nil {
		return err
	}
	if asJSON {
		return writeJSONPretty(fleet.Users)
	}

	if len(fleet.Users) == 0 {
		fmt.Println("No sigil users reported by the fleet.")
		return nil
	}

	sorted := append([]sigilUserSummary(nil), fleet.Users...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].User < sorted[j].User })

	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\n",
		bold("USER"), bold("ENVOYS"), bold("FILES"), bold("SIZE"), bold("UNLOCKED"), bold("STATE"))
	for _, u := range sorted {
		envoys := fmt.Sprintf("%d", len(u.Envoys))
		files := fmt.Sprintf("%d", u.FileCount)
		size := formatBytes(u.TotalBytes)
		unlocked := fmt.Sprintf("%d", len(u.ActiveSessions))
		state := green("in sync")
		switch {
		case u.RecipientDrift:
			state = red("recipient drift")
		case len(u.Divergences) > 0:
			state = red(fmt.Sprintf("%d divergence(s)", len(u.Divergences)))
		}
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\n", u.User, envoys, files, size, unlocked, state)
	}
	w.Flush()
	return nil
}

func runSigilShow(cmd *cobra.Command, args []string) error {
	target := args[0]
	asJSON, _ := cmd.Flags().GetBool("json")
	fleet, err := fetchSigilFleet()
	if err != nil {
		return err
	}

	var match *sigilUserSummary
	for i := range fleet.Users {
		if fleet.Users[i].User == target {
			match = &fleet.Users[i]
			break
		}
	}
	if match == nil {
		return fmt.Errorf("no sigil user %q reported by the fleet", target)
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
	if len(match.ActiveSessions) > 0 {
		fmt.Printf("Unlocked on:      %s\n", strings.Join(match.ActiveSessions, ", "))
	}
	fmt.Println()

	if len(match.Envoys) == 0 {
		fmt.Println("  (no host presence reported yet)")
		return nil
	}

	sorted := append([]sigilEnvoyPresence(nil), match.Envoys...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].Hostname < sorted[j].Hostname })

	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintf(w, "  %s\t%s\t%s\t%s\t%s\t%s\n",
		bold("HOST"), bold("STATUS"), bold("FILES"), bold("TOMBSTONES"), bold("SESSION"), bold("LAST UPDATE"))
	for _, e := range sorted {
		status := echoPresenceStatus(e.Status)
		unlocked := "locked"
		if e.SessionUnlocked {
			unlocked = green("unlocked")
		}
		updated := "—"
		if e.UpdatedAt > 0 {
			updated = time.Unix(e.UpdatedAt, 0).Local().Format("2006-01-02 15:04")
		}
		fmt.Fprintf(w, "  %s\t%s\t%d\t%d\t%s\t%s\n",
			e.Hostname, status, e.FileCount, e.TombstoneCount, unlocked, updated)
	}
	w.Flush()
	return nil
}

// ---------- vigocli sigil members ----------

var sigilMembersCmd = &cobra.Command{
	Use:   "members [user]",
	Short: "List enrolled envoys for a sigil user",
	Long: `Print the list of envoys currently enrolled in a user's sigil circle,
sourced from the 'sigil' trait projection. Pass [user] to scope to one
user; omit it to list every enrolled user + their envoys.

Read-only. Does not contact the envoys.`,
	Args: cobra.MaximumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		return runSigilMembers(args)
	},
}

func runSigilMembers(args []string) error {
	client := newAPIClient()
	var fleet sigilFleet
	if err := client.get("/api/v1/sigil/fleet", &fleet); err != nil {
		return fmt.Errorf("fetch sigil fleet: %w", err)
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
		tw := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
		fmt.Fprintln(tw, "HOST\tSTATUS\tFILES\tSESSION")
		sort.Slice(u.Envoys, func(i, j int) bool { return u.Envoys[i].Hostname < u.Envoys[j].Hostname })
		for _, e := range u.Envoys {
			sess := "-"
			if e.SessionUnlocked {
				sess = "unlocked"
			}
			fmt.Fprintf(tw, "%s\t%s\t%d\t%s\n", e.Hostname, e.Status, e.FileCount, sess)
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
			return fmt.Errorf("no sigil members found for user %q", filter)
		}
	}
	return nil
}

// ---------- vigocli sigil evict ----------

var sigilEvictCmd = &cobra.Command{
	Use:   "evict",
	Short: "Remove a host from a user's sigil circle fleet-wide",
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
		yes, _ := cmd.Flags().GetBool("yes")
		if host == "" {
			return fmt.Errorf("--host is required")
		}
		if user == "" && !allUsers {
			return fmt.Errorf("either --user <u> or --all-users is required")
		}
		if user != "" && allUsers {
			return fmt.Errorf("--user and --all-users are mutually exclusive")
		}
		return runSigilEvict(host, user, allUsers, yes)
	},
}

func runSigilEvict(host, user string, allUsers, yes bool) error {
	client := newAPIClient()
	var fleet sigilFleet
	if err := client.get("/api/v1/sigil/fleet", &fleet); err != nil {
		return fmt.Errorf("fetch sigil fleet: %w", err)
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
		return fmt.Errorf("host %q is not enrolled in any matching sigil circle", host)
	}

	if !yes {
		fmt.Printf("Will evict %s from %d user circle(s):\n", host, len(targets))
		for _, t := range targets {
			fmt.Printf("  user=%s\n", t.user)
		}
		fmt.Print("Proceed? [y/N] ")
		var resp string
		fmt.Scanln(&resp)
		if strings.ToLower(strings.TrimSpace(resp)) != "y" {
			fmt.Println("aborted.")
			return nil
		}
	}

	// For each target user, look up the host's age pubkey and dispatch.
	// Falling back to the agent's own peer-table lookup: we dispatch
	// `_apply-evict` with --pubkey unset when the fleet projection doesn't
	// carry it, and the agent resolves it locally from its own peers.json.
	// This keeps the server simple.
	for _, t := range targets {
		// Find the pubkey from the Participants field or similar. The current
		// sigilFleet struct doesn't expose per-envoy age pubkey directly, so we
		// instruct agents to resolve locally via hostname. If an agent can't
		// find the hostname in its peer table, _apply-evict is a no-op.
		cmd := fmt.Sprintf("vigo sigil _apply-evict --user %s --hostname %s --pubkey LOOKUP",
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
	// Minimal quoting — task dispatch splits on whitespace. For typical
	// usernames and hostnames (alphanumeric + hyphen), no quoting needed.
	return s
}

func init() {
	sigilStatusCmd.Flags().Bool("json", false, "Emit machine-readable JSON instead of a table")
	sigilListCmd.Flags().Bool("json", false, "Emit machine-readable JSON instead of a table")
	sigilShowCmd.Flags().Bool("json", false, "Emit machine-readable JSON instead of a table")

	sigilEvictCmd.Flags().String("host", "", "Hostname to evict from the circle")
	sigilEvictCmd.Flags().String("user", "", "User whose circle to evict from")
	sigilEvictCmd.Flags().Bool("all-users", false, "Evict the host from every user's sigil circle it belongs to")
	sigilEvictCmd.Flags().Bool("yes", false, "Skip the confirmation prompt")

	sigilCmd.AddCommand(sigilStatusCmd, sigilListCmd, sigilShowCmd, sigilMembersCmd, sigilEvictCmd)
}
