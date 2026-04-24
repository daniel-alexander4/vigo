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
// on. Admin-pushed payloads are `vigocli lode`; personal-DR mirrors
// are `vigocli cairn`; per-user directory sync is `vigocli echo` and
// `vigocli sigil`. Muscle memory from the pre-rename era points here
// for content verbs — the command's long help leads with that
// disambiguation so a mistyped "swarm distribute" produces an obvious
// pointer at `lode distribute`.

type swarmStatusFleet struct {
	GeneratedAt            time.Time             `json:"generated_at"`
	EnvoysTotal            int                   `json:"envoys_total"`
	EnvoysOnline           int                   `json:"envoys_online"`
	EnvoysSubstrateActive  int                   `json:"envoys_substrate_active"`
	EnvoysSeeding          int                   `json:"envoys_seeding"`
	SeedingAllowlist       []string              `json:"seeding_allowlist"`
	ServerEnvoyHost        string                `json:"server_envoy_host"`
	ManifestEntriesActive  int                   `json:"manifest_entries_active"`
	ManifestEntriesRevoked int                   `json:"manifest_entries_revoked"`
	Payloads               swarmStatusPayloads   `json:"payloads"`
	TotalFootprintBytes    int64                 `json:"total_footprint_bytes"`
}

type swarmStatusPayloads struct {
	Lode  int `json:"lode"`
	Cairn int `json:"cairn"`
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

  vigocli lode      — admin-pushed payloads (was 'swarm distribute')
  vigocli cairn     — personal-DR git mirror fleet view
  vigocli echo      — per-user plaintext directory sync mesh
  vigocli sigil     — per-user encrypted directory sync mesh

Subcommands here report substrate health: who is reachable, who has a
blob cache, who is seeding, total footprint across the fleet.`,
}

var swarmStatusCmd = &cobra.Command{
	Use:   "status",
	Short: "Fleet-wide substrate totals (envoys, seeders, payloads, footprint)",
	Args:  cobra.NoArgs,
	RunE:  runSwarmStatus,
}

var swarmPeersCmd = &cobra.Command{
	Use:   "peers",
	Short: "One row per envoy with substrate-participation counts",
	Args:  cobra.NoArgs,
	RunE:  runSwarmPeers,
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
	fmt.Fprintf(w, "%s\t%d lode · %d cairn\n", bold("Active payloads"), resp.Payloads.Lode, resp.Payloads.Cairn)
	w.Flush()

	fmt.Println()
	fmt.Println(bold("Seeding allowlist"))
	if resp.ServerEnvoyHost == "" && len(resp.SeedingAllowlist) == 0 {
		fmt.Println("  (none configured — set server.hostname and/or swarm.seeding_envoys in server.yaml)")
	} else {
		if resp.ServerEnvoyHost != "" {
			fmt.Printf("  %s  %s\n", resp.ServerEnvoyHost, dim("(server.hostname — implicit)"))
		}
		for _, p := range resp.SeedingAllowlist {
			fmt.Printf("  %s\n", p)
		}
	}
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
	swarmCmd.AddCommand(swarmStatusCmd, swarmPeersCmd)
}
