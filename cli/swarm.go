// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

package main

import (
	"fmt"
	"net/url"
	"os"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"
)

// Substrate-level view of the envoy P2P network. `vigocli swarm` is
// the substrate — the transport layer every content subsystem rides
// on. Admin-pushed file fan-out is `vigocli swarm swarmcast`; personal-DR
// mirrors are `vigocli swarm swarmgit`; per-user directory sync is
// `vigocli swarm longdrawer` and `vigocli swarm lockbox`. Muscle memory
// from the pre-rename era points here for content verbs — the
// command's long help leads with that disambiguation so a mistyped
// "swarm distribute" produces an obvious pointer at
// `swarmcast distribute`.

type swarmStatusFleet struct {
	GeneratedAt            time.Time             `json:"generated_at"`
	EnvoysTotal            int                   `json:"envoys_total"`
	EnvoysOnline           int                   `json:"envoys_online"`
	EnvoysSubstrateActive  int                   `json:"envoys_substrate_active"`
	EnvoysSeeding          int                   `json:"envoys_seeding"`
	ManifestEntriesActive  int                   `json:"manifest_entries_active"`
	ManifestEntriesRevoked int                   `json:"manifest_entries_revoked"`
	BlobsByKind            swarmBlobCounts       `json:"blobs_by_kind"`
	TotalFootprintBytes    int64                 `json:"total_footprint_bytes"`
}

type swarmBlobCounts struct {
	Swarmcast int `json:"swarmcast"`
	Swarmgit  int `json:"swarmgit"`
}

type swarmPeersFleet struct {
	GeneratedAt time.Time       `json:"generated_at"`
	Total       int             `json:"total"`
	Returned    int             `json:"returned"`
	Sort        string          `json:"sort"`
	Limit       int             `json:"limit"`
	Envoys      []swarmPeerRow  `json:"envoys"`
}

type swarmPeerRow struct {
	Hostname         string    `json:"hostname"`
	ID               string    `json:"id"`
	LastSeen         time.Time `json:"last_seen"`
	Online           bool      `json:"online"`
	SubstrateActive  bool      `json:"substrate_active"`
	SeedingCount     int       `json:"seeding_count"`
	CachedCount      int       `json:"cached_count"`
	DownloadingCount int       `json:"downloading_count"`
	FootprintBytes   int64     `json:"footprint_bytes"`
}

var swarmCmd = &cobra.Command{
	Use:   "swarm",
	Short: "Substrate-level view of the envoy P2P network",
	Long: `Read-only admin view of the swarm substrate — the envoy P2P network
four content subsystems ride on.

This command is the SUBSTRATE. For the content verbs that used to live
under 'vigocli swarm distribute' etc., see:

  vigocli swarm swarmcast      — admin-pushed swarmcasts (was 'swarm distribute')
  vigocli swarm swarmgit     — personal-DR git mirror fleet view
  vigocli swarm longdrawer      — per-user plaintext directory sync mesh
  vigocli swarm lockbox     — per-user encrypted directory sync mesh

Subcommands here report substrate health: who is reachable, who has a
blob cache, who is seeding, total footprint across the fleet.`,
}

var swarmStatusCmd = &cobra.Command{
	Use:   "status",
	Short: "Fleet-wide substrate totals (envoys, seeders, blobs, footprint)",
	Args:  cobra.NoArgs,
	RunE:  runSwarmStatus,
}

var swarmPeersCmd = &cobra.Command{
	Use:   "peers",
	Short: "One row per envoy with substrate-participation counts",
	Args:  cobra.NoArgs,
	RunE:  runSwarmPeers,
}

var swarmCleanupCmd = &cobra.Command{
	Use:   "cleanup",
	Short: "Fleet-wide swarm-cache hygiene sweep (dangling symlinks, stale .partial dirs)",
	Long: `Dispatch the hidden 'vigo swarm _apply-cleanup' verb to every targeted
envoy. The agent walks its blob cache and removes:

  - dangling files/<label> symlinks (target file gone)
  - .partial/ dirs whose final blob has already been assembled

Idempotent — safe to re-run; reports zero changes when nothing's
stale. Does NOT touch revoked-blob eviction (that's the manifest
gossip path) or retention-expired blobs (that's the RetentionPruner).

Defaults to fleet-wide; pass --target to scope to a hostname pattern.`,
	Args: cobra.NoArgs,
	RunE: runSwarmCleanup,
}

type swarmCleanupResp struct {
	Target     string `json:"target"`
	Dispatched int    `json:"dispatched"`
	TaskRunID  string `json:"task_run_id,omitempty"`
}

func runSwarmCleanup(cmd *cobra.Command, _ []string) error {
	target, _ := cmd.Flags().GetString("target")
	if target == "" {
		target = "*"
	}
	client := newAPIClient()
	var resp swarmCleanupResp
	path := fmt.Sprintf("/api/v1/swarm/cleanup?target=%s", url.QueryEscape(target))
	if err := client.post(path, nil, &resp); err != nil {
		return fmt.Errorf("dispatch swarm cleanup: %w", err)
	}
	fmt.Printf("Dispatched 'vigo swarm _apply-cleanup' to %d envoy(s) matching %q.\n", resp.Dispatched, resp.Target)
	if resp.TaskRunID != "" {
		fmt.Printf("Per-envoy results:\n  vigocli task status %s\n", resp.TaskRunID)
	} else {
		fmt.Println("Per-envoy results in the task-run log:")
		fmt.Println("  vigocli task list")
	}
	return nil
}

func runSwarmStatus(cmd *cobra.Command, _ []string) error {
	asJSON, _ := cmd.Flags().GetBool("json")
	client := newAPIClient()
	var resp swarmStatusFleet
	if err := client.get("/api/v1/swarm/status", &resp); err != nil {
		return fmt.Errorf("fetching swarm status: %w", err)
	}
	if asJSON {
		return writeJSONPretty(resp)
	}

	fmt.Printf("Swarm substrate — generated %s\n\n", resp.GeneratedAt.Local().Format("2006-01-02 15:04:05"))

	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintf(w, "%s\t%d / %d\n", bold("Envoys online"), resp.EnvoysOnline, resp.EnvoysTotal)
	fmt.Fprintf(w, "%s\t%d\n", bold("Substrate active"), resp.EnvoysSubstrateActive)
	fmt.Fprintf(w, "%s\t%d\n", bold("Seeding envoys"), resp.EnvoysSeeding)
	fmt.Fprintf(w, "%s\t%s\n", bold("Fleet footprint"), formatBytes(resp.TotalFootprintBytes))
	fmt.Fprintf(w, "%s\t%d active · %d revoked\n", bold("Manifest entries"), resp.ManifestEntriesActive, resp.ManifestEntriesRevoked)
	fmt.Fprintf(w, "%s\t%d swarmcast · %d swarmgit\n", bold("Active blobs"), resp.BlobsByKind.Swarmcast, resp.BlobsByKind.Swarmgit)
	w.Flush()
	return nil
}

func runSwarmPeers(cmd *cobra.Command, _ []string) error {
	asJSON, _ := cmd.Flags().GetBool("json")
	sortKey, _ := cmd.Flags().GetString("sort")
	limit, _ := cmd.Flags().GetInt("limit")

	client := newAPIClient()
	path := fmt.Sprintf("/api/v1/swarm/peers?sort=%s&limit=%d", url.QueryEscape(sortKey), limit)
	var resp swarmPeersFleet
	if err := client.get(path, &resp); err != nil {
		return fmt.Errorf("fetching swarm peers: %w", err)
	}
	if asJSON {
		return writeJSONPretty(resp)
	}

	if resp.Total == 0 {
		fmt.Println("No envoys reporting on the substrate yet.")
		return nil
	}

	fmt.Printf("Swarm peers — generated %s · sorted by %s\n\n",
		resp.GeneratedAt.Local().Format("2006-01-02 15:04:05"),
		resp.Sort,
	)

	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\n",
		bold("HOSTNAME"),
		bold("STATUS"),
		bold("SUBSTRATE"),
		bold("SEEDING"),
		bold("CACHED"),
		bold("DOWNLOADING"),
		bold("FOOTPRINT"),
		bold("LAST SEEN"),
	)
	for _, p := range resp.Envoys {
		status := green("online")
		if !p.Online {
			status = red("offline")
		}
		substrate := dim("idle")
		if p.SubstrateActive {
			substrate = green("active")
		}
		last := "—"
		if !p.LastSeen.IsZero() {
			last = p.LastSeen.Local().Format("2006-01-02 15:04")
		}
		fmt.Fprintf(w, "%s\t%s\t%s\t%d\t%d\t%d\t%s\t%s\n",
			p.Hostname,
			status,
			substrate,
			p.SeedingCount,
			p.CachedCount,
			p.DownloadingCount,
			formatBytes(p.FootprintBytes),
			last,
		)
	}
	w.Flush()

	if resp.Total > resp.Returned {
		fmt.Printf("\n%s %d of %d envoys. Pass --limit 0 to show all.\n",
			dim("Showing"), resp.Returned, resp.Total)
	}
	return nil
}

// sortKeyIsValid screens --sort values on the client so the user gets
// a clear error here instead of the server silently falling back to
// the default.
func sortKeyIsValid(k string) bool {
	switch strings.ToLower(k) {
	case "footprint", "blobs", "hostname":
		return true
	}
	return false
}

func init() {
	swarmStatusCmd.Flags().Bool("json", false, "Emit machine-readable JSON instead of a table")
	swarmPeersCmd.Flags().Bool("json", false, "Emit machine-readable JSON instead of a table")
	swarmPeersCmd.Flags().String("sort", "footprint", "Sort key: footprint | blobs | hostname")
	swarmPeersCmd.Flags().Int("limit", 50, "Max rows to return; 0 for all")
	swarmPeersCmd.PreRunE = func(cmd *cobra.Command, _ []string) error {
		k, _ := cmd.Flags().GetString("sort")
		if !sortKeyIsValid(k) {
			return fmt.Errorf("invalid --sort %q; expected footprint | blobs | hostname", k)
		}
		return nil
	}
	swarmCleanupCmd.Flags().String("target", "*", "Hostname pattern (filepath.Match glob) to scope the sweep; default '*' = whole fleet")
	swarmCmd.AddCommand(swarmStatusCmd, swarmPeersCmd, swarmCleanupCmd)
	swarmCmd.AddCommand(swarmcastCmd, swarmgitCmd, longdrawerCmd, lockboxCmd, puddleCmd)
}
