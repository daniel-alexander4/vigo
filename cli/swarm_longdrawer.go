// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"fmt"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/spf13/cobra"
)

// Admin-side view of longdrawer — per-user LAN plaintext directory sync on
// the swarm substrate. Read-only. The server plays no role in longdrawer
// content or discovery; this command exposes the fleet-wide aggregator
// that joins each envoy's `longdrawer` trait into a mesh view.
//
// Mirrors the shape of `vigocli swarm gitback` (fleet status/list/show). No
// write verbs — longdrawer membership is per-user and content is driven by
// dropping files into `~<user>/longdrawer/` on any envoy.

// echoFleet mirrors `server/longdrawermesh.Fleet`. Duplicated rather than
// imported so the CLI stays decoupled from the server package
// (matches the pattern in cli/gitback.go).
type echoFleet struct {
	GeneratedAt  time.Time         `json:"generated_at"`
	Users        []echoUserSummary `json:"users"`
	Participants []string          `json:"participants"`
	Totals       echoTotals        `json:"totals"`
}

type echoTotals struct {
	Users       int `json:"users"`
	Envoys      int `json:"envoys"`
	Files       int `json:"files"`
	Tombstones  int `json:"tombstones"`
	Divergences int `json:"divergences"`
}

type echoUserSummary struct {
	User        string              `json:"user"`
	Envoys      []echoEnvoyPresence `json:"envoys"`
	FileCount   int                 `json:"file_count"`
	TotalBytes  int64               `json:"total_bytes"`
	Divergences []string            `json:"divergences,omitempty"`
}

type echoEnvoyPresence struct {
	Hostname       string `json:"hostname"`
	Status         string `json:"status"` // reporting | not_reporting | stale
	UpdatedAt      int64  `json:"updated_at,omitempty"`
	FileCount      int    `json:"file_count"`
	FilesDigest    string `json:"files_digest,omitempty"`
	TombstoneCount int    `json:"tombstone_count"`
}

var longdrawerCmd = &cobra.Command{
	Use:   "longdrawer",
	Short: "Fleet-wide view of longdrawer per-user LAN directory sync",
	Long: `Read-only admin view of the longdrawer mesh across every envoy.

Each envoy reports its per-user longdrawer state — files, revoked entries,
and peer digest — through the 'longdrawer' trait on every check-in. The
server joins every envoy's report into a fleet view; this command
renders it. One row per user.

Longdrawer membership is per-user: users opt in by dropping files into
~<user>/longdrawer/ on any envoy. There is no admin write path — this
command never touches files or revocation records.`,
}

var echoStatusCmd = &cobra.Command{
	Use:   "status",
	Short: "Fleet-wide longdrawer totals",
	Args:  cobra.NoArgs,
	RunE:  runEchoStatus,
}

var echoListCmd = &cobra.Command{
	Use:   "list",
	Short: "One row per user across the fleet",
	Args:  cobra.NoArgs,
	RunE:  runEchoList,
}

var echoShowCmd = &cobra.Command{
	Use:   "show <user>",
	Short: "Detailed per-envoy presence for one user",
	Args:  cobra.ExactArgs(1),
	RunE:  runEchoShow,
}

func fetchEchoFleet() (*echoFleet, error) {
	client := newAPIClient()
	var resp echoFleet
	if err := client.get("/api/v1/swarm/longdrawer/fleet", &resp); err != nil {
		return nil, fmt.Errorf("fetching longdrawer fleet: %w", err)
	}
	return &resp, nil
}

func runEchoStatus(cmd *cobra.Command, _ []string) error {
	asJSON, _ := cmd.Flags().GetBool("json")
	fleet, err := fetchEchoFleet()
	if err != nil {
		return err
	}
	if asJSON {
		return writeJSONPretty(fleet)
	}

	fmt.Printf("Longdrawer fleet — generated %s\n\n", fleet.GeneratedAt.Local().Format("2006-01-02 15:04:05"))

	w := newAlignedTable(os.Stdout)
	fmt.Fprintf(w, "%s\t%d\n", bold("Users"), fleet.Totals.Users)
	fmt.Fprintf(w, "%s\t%d\n", bold("Envoys reporting"), fleet.Totals.Envoys)
	fmt.Fprintf(w, "%s\t%d\n", bold("Files"), fleet.Totals.Files)
	fmt.Fprintf(w, "%s\t%d\n", bold("Revoked"), fleet.Totals.Tombstones)
	fmt.Fprintf(w, "%s\t%s\n", bold("Divergences"), meshCountWarn(fleet.Totals.Divergences))
	fmt.Fprintf(w, "%s\t%d\n", bold("Participants"), len(fleet.Participants))
	w.Flush()
	return nil
}

func runEchoList(cmd *cobra.Command, _ []string) error {
	asJSON, _ := cmd.Flags().GetBool("json")
	fleet, err := fetchEchoFleet()
	if err != nil {
		return err
	}
	if asJSON {
		return writeJSONPretty(fleet.Users)
	}

	if len(fleet.Users) == 0 {
		fmt.Println("No longdrawer users reported by the fleet.")
		return nil
	}

	sorted := append([]echoUserSummary(nil), fleet.Users...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].User < sorted[j].User })

	w := newAlignedTable(os.Stdout)
	fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\n",
		bold("USER"), bold("ENVOYS"), bold("FILES"), bold("SIZE"), bold("STATE"))
	for _, u := range sorted {
		envoys := fmt.Sprintf("%d", len(u.Envoys))
		files := fmt.Sprintf("%d", u.FileCount)
		size := formatBytes(u.TotalBytes)
		state := green("in sync")
		if len(u.Divergences) > 0 {
			state = red(fmt.Sprintf("%d divergence(s)", len(u.Divergences)))
		}
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\n", u.User, envoys, files, size, state)
	}
	w.Flush()
	return nil
}

func runEchoShow(cmd *cobra.Command, args []string) error {
	target := args[0]
	asJSON, _ := cmd.Flags().GetBool("json")
	fleet, err := fetchEchoFleet()
	if err != nil {
		return err
	}

	var match *echoUserSummary
	for i := range fleet.Users {
		if fleet.Users[i].User == target {
			match = &fleet.Users[i]
			break
		}
	}
	if match == nil {
		return fmt.Errorf("no longdrawer user %q reported by the fleet", target)
	}
	if asJSON {
		return writeJSONPretty(match)
	}

	fmt.Printf("User:        %s\n", bold(match.User))
	fmt.Printf("Envoys:      %d\n", len(match.Envoys))
	fmt.Printf("Files:       %d\n", match.FileCount)
	fmt.Printf("Total size:  %s\n", formatBytes(match.TotalBytes))
	if len(match.Divergences) > 0 {
		fmt.Printf("Divergences: %s\n", red(strings.Join(match.Divergences, ", ")))
	}
	fmt.Println()

	if len(match.Envoys) == 0 {
		fmt.Println("  (no host presence reported yet)")
		return nil
	}

	sorted := append([]echoEnvoyPresence(nil), match.Envoys...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].Hostname < sorted[j].Hostname })

	w := newAlignedTable(os.Stdout)
	fmt.Fprintf(w, "  %s\t%s\t%s\t%s\t%s\n",
		bold("HOST"), bold("STATUS"), bold("FILES"), bold("REVOKED"), bold("LAST UPDATE"))
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

func echoPresenceStatus(s string) string {
	switch s {
	case "reporting":
		return green(s)
	case "stale":
		return yellow(s)
	case "not_reporting":
		return red(s)
	default:
		return s
	}
}

// meshCountWarn is shared with cli/lockbox.go.
func meshCountWarn(n int) string {
	if n == 0 {
		return "0"
	}
	return yellow(fmt.Sprintf("%d", n))
}

func init() {
	echoStatusCmd.Flags().Bool("json", false, "Emit machine-readable JSON instead of a table")
	echoListCmd.Flags().Bool("json", false, "Emit machine-readable JSON instead of a table")
	echoShowCmd.Flags().Bool("json", false, "Emit machine-readable JSON instead of a table")
	longdrawerCmd.AddCommand(echoStatusCmd, echoListCmd, echoShowCmd)
}
