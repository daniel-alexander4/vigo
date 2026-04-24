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

func init() {
	sigilStatusCmd.Flags().Bool("json", false, "Emit machine-readable JSON instead of a table")
	sigilListCmd.Flags().Bool("json", false, "Emit machine-readable JSON instead of a table")
	sigilShowCmd.Flags().Bool("json", false, "Emit machine-readable JSON instead of a table")
	sigilCmd.AddCommand(sigilStatusCmd, sigilListCmd, sigilShowCmd)
}
