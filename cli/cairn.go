// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

package main

import (
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"
)

// Admin-side view of cairn state across the fleet. Complements
// the envoy-local `vigo repo` CLI shipped with the agent: that one
// reads one envoy's mirrors; this one reads every envoy's publisher-
// and receiver-side cairn traits through the server's aggregator
// at `GET /api/v1/cairn/fleet`.
//
// All subcommands are read-only. Recovery, publish, and revoke
// operations live on the envoy side (`vigo repo recover ...`) or in
// `vigocli swarm` — there is deliberately no `vigocli cairn recover`
// in v1 because recovery runs as a user on the envoy holding the
// mirror, not as an admin over the REST API.

// fleetResponse mirrors the shape of
// `server/cairnmesh.Fleet` exactly. Duplicated here rather than
// imported so the CLI stays decoupled from the server package
// (matches the pattern in cli/swarm.go).
type fleetResponse struct {
	GeneratedAt  time.Time             `json:"generated_at"`
	Participants []string              `json:"participants"`
	Totals       fleetTotals           `json:"totals"`
	Labels       []fleetLabelSummary   `json:"labels"`
}

type fleetTotals struct {
	Sources       int `json:"sources"`
	Labels        int `json:"labels"`
	Mirrors       int `json:"mirrors"`
	OrphanMirrors int `json:"orphan_mirrors"`
	StaleMirrors  int `json:"stale_mirrors"`
}

type fleetLabelSummary struct {
	Label             string                 `json:"label"`
	Publisher         string                 `json:"publisher"`
	User              string                 `json:"user"`
	Project           string                 `json:"project"`
	Paused            bool                   `json:"paused"`
	MaxMirrors        int                    `json:"max_mirrors"`
	CrossSubnetMirrors      []string               `json:"cross_subnet_mirrors,omitempty"`
	Tags              []string               `json:"tags,omitempty"`
	Description       string                 `json:"description,omitempty"`
	PublisherSeen     int64                  `json:"publisher_seen"`
	BlobSha           string                 `json:"blob_sha,omitempty"`
	GitSha            string                 `json:"git_sha,omitempty"`
	LastPublishFull   *bool                  `json:"last_publish_full,omitempty"`
	LastBundleSize    int64                  `json:"last_bundle_size,omitempty"`
	Mirrors           []fleetMirrorPresence  `json:"mirrors"`
	CountSelected     int                    `json:"count_selected"`
	CountMaterialized int                    `json:"count_materialized"`
	CountOrphan       int                    `json:"count_orphan"`
	CountStale        int                    `json:"count_stale"`
}

type fleetMirrorPresence struct {
	Host          string `json:"host"`
	Status        string `json:"status"`
	Materialized  bool   `json:"materialized"`
	Selected      bool   `json:"selected"`
	Stale         bool   `json:"stale,omitempty"`
	GitSha        string `json:"git_sha,omitempty"`
	BlobSha       string `json:"blob_sha,omitempty"`
	LastRefreshed int64  `json:"last_refreshed,omitempty"`
}

var cairnCmd = &cobra.Command{
	Use:   "cairn",
	Short: "Fleet-wide view of cairn personal-DR mirrors",
	Long: `Read-only admin view of the cairn P2P mesh across every envoy.

Publishers report their source list, bundle metadata, and chosen LAN
receivers through the cairn trait. Receivers report the mirrors they
currently hold. The server joins both sides and this command shows that
join, one label per row.

Use the envoy-local 'vigo repo' CLI (shipped with the agent) to recover
a mirror on the envoy where it lives. This admin command never touches
bare repos or initiates recovery.`,
}

var cairnStatusCmd = &cobra.Command{
	Use:   "status",
	Short: "Fleet-wide cairn totals",
	Args:  cobra.NoArgs,
	RunE:  runRepoStatus,
}

var cairnListCmd = &cobra.Command{
	Use:   "list",
	Short: "One row per cairn label across the fleet",
	Args:  cobra.NoArgs,
	RunE:  runRepoList,
}

var cairnShowCmd = &cobra.Command{
	Use:   "show <label|project>",
	Short: "Detailed view of one cairn label with per-host mirror grid",
	Args:  cobra.ExactArgs(1),
	RunE:  runRepoShow,
}

func fetchFleet() (*fleetResponse, error) {
	client := newAPIClient()
	var resp fleetResponse
	if err := client.get("/api/v1/cairn/fleet", &resp); err != nil {
		return nil, fmt.Errorf("fetching fleet: %w", err)
	}
	return &resp, nil
}

func runRepoStatus(cmd *cobra.Command, _ []string) error {
	asJSON, _ := cmd.Flags().GetBool("json")
	fleet, err := fetchFleet()
	if err != nil {
		return err
	}
	if asJSON {
		return writeJSONPretty(fleet)
	}

	fmt.Printf("Cairn fleet — generated %s\n\n", fleet.GeneratedAt.Local().Format("2006-01-02 15:04:05"))

	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintf(w, "%s\t%s\n", bold("Publishers (sources)"), fmt.Sprintf("%d", fleet.Totals.Sources))
	fmt.Fprintf(w, "%s\t%s\n", bold("Distinct labels"), fmt.Sprintf("%d", fleet.Totals.Labels))
	fmt.Fprintf(w, "%s\t%s\n", bold("Materialized mirrors"), fmt.Sprintf("%d", fleet.Totals.Mirrors))
	fmt.Fprintf(w, "%s\t%s\n", bold("Orphan mirrors"), repoCountWarn(fleet.Totals.OrphanMirrors))
	fmt.Fprintf(w, "%s\t%s\n", bold("Stale mirrors"), repoCountWarn(fleet.Totals.StaleMirrors))
	fmt.Fprintf(w, "%s\t%d\n", bold("Participants"), len(fleet.Participants))
	w.Flush()
	return nil
}

func runRepoList(cmd *cobra.Command, _ []string) error {
	asJSON, _ := cmd.Flags().GetBool("json")
	fleet, err := fetchFleet()
	if err != nil {
		return err
	}
	if asJSON {
		return writeJSONPretty(fleet.Labels)
	}

	if len(fleet.Labels) == 0 {
		fmt.Println("No cairn labels reported by the fleet.")
		return nil
	}

	sorted := append([]fleetLabelSummary(nil), fleet.Labels...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].Label < sorted[j].Label })

	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\n",
		bold("LABEL"), bold("PUBLISHER"), bold("MIRRORS"), bold("LAST BUNDLE"), bold("SIZE"), bold("STATE"))
	for _, s := range sorted {
		mirrors := fmt.Sprintf("%d/%d", s.CountMaterialized, s.CountSelected)
		if s.CountOrphan > 0 {
			mirrors += " " + yellow(fmt.Sprintf("(+%d orphan)", s.CountOrphan))
		}
		if s.CountStale > 0 {
			mirrors += " " + red(fmt.Sprintf("(%d stale)", s.CountStale))
		}
		bundle := "—"
		if s.LastPublishFull != nil {
			if *s.LastPublishFull {
				bundle = green("full")
			} else {
				bundle = slateBlue("incremental")
			}
		}
		size := "—"
		if s.LastBundleSize > 0 {
			size = formatBytes(s.LastBundleSize)
		}
		state := "publishing"
		switch {
		case s.Paused:
			state = yellow("paused")
		case s.Publisher == "":
			state = red("orphan (no publisher)")
		}
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\n",
			s.Label, displayOrDash(s.Publisher), mirrors, bundle, size, state)
	}
	w.Flush()
	return nil
}

func runRepoShow(cmd *cobra.Command, args []string) error {
	target := args[0]
	asJSON, _ := cmd.Flags().GetBool("json")
	fleet, err := fetchFleet()
	if err != nil {
		return err
	}

	match, err := resolveFleetLabel(fleet.Labels, target)
	if err != nil {
		return err
	}
	if asJSON {
		return writeJSONPretty(match)
	}

	fmt.Printf("Label:          %s\n", bold(match.Label))
	fmt.Printf("Publisher:      %s\n", displayOrDash(match.Publisher))
	fmt.Printf("User:           %s\n", displayOrDash(match.User))
	fmt.Printf("Project:        %s\n", displayOrDash(match.Project))
	if match.Description != "" {
		fmt.Printf("Description:    %s\n", match.Description)
	}
	if len(match.Tags) > 0 {
		fmt.Printf("Tags:           %s\n", strings.Join(match.Tags, ", "))
	}
	fmt.Printf("Max mirrors:    %d\n", match.MaxMirrors)
	if match.Paused {
		fmt.Printf("Paused:         %s\n", yellow("yes"))
	}
	if match.GitSha != "" {
		fmt.Printf("Git HEAD:       %s\n", match.GitSha)
	}
	if match.BlobSha != "" {
		fmt.Printf("Blob sha:       %s\n", match.BlobSha)
	}
	if match.LastPublishFull != nil {
		kind := slateBlue("incremental")
		if *match.LastPublishFull {
			kind = green("full")
		}
		sz := ""
		if match.LastBundleSize > 0 {
			sz = " · " + formatBytes(match.LastBundleSize)
		}
		fmt.Printf("Last bundle:    %s%s\n", kind, sz)
	}
	if len(match.CrossSubnetMirrors) > 0 {
		fmt.Printf("Extra mirrors:  %s\n", strings.Join(match.CrossSubnetMirrors, ", "))
	}
	if match.PublisherSeen > 0 {
		fmt.Printf("Publisher seen: %s\n", time.Unix(match.PublisherSeen, 0).Local().Format("2006-01-02 15:04:05"))
	}
	fmt.Println()
	fmt.Printf("Mirrors (%d selected, %d materialized, %d orphan, %d stale):\n",
		match.CountSelected, match.CountMaterialized, match.CountOrphan, match.CountStale)
	if len(match.Mirrors) == 0 {
		fmt.Println("  (no host presence reported yet)")
		return nil
	}
	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintf(w, "  %s\t%s\t%s\t%s\n", bold("HOST"), bold("STATUS"), bold("GIT SHA"), bold("LAST REFRESH"))
	for _, m := range match.Mirrors {
		status := m.Status
		switch m.Status {
		case "active":
			status = green(m.Status)
		case "pending":
			status = yellow(m.Status)
		case "orphan":
			status = slateBlue(m.Status)
		}
		if m.Stale {
			status += " " + red("(stale)")
		}
		refreshed := "—"
		if m.LastRefreshed > 0 {
			refreshed = time.Unix(m.LastRefreshed, 0).Local().Format("2006-01-02 15:04")
		}
		gitSha := "—"
		if m.GitSha != "" {
			gitSha = shortSha(m.GitSha)
		}
		fmt.Fprintf(w, "  %s\t%s\t%s\t%s\n", m.Host, status, gitSha, refreshed)
	}
	w.Flush()
	return nil
}

// resolveFleetLabel matches the target arg against the fleet's labels
// in the same order vigo repo show resolves on the envoy: exact label,
// exact project, unique project prefix. An ambiguous prefix match is
// an error (caller must disambiguate with the full label).
func resolveFleetLabel(labels []fleetLabelSummary, target string) (*fleetLabelSummary, error) {
	if len(labels) == 0 {
		return nil, fmt.Errorf("the fleet has no cairn labels")
	}
	for i := range labels {
		if labels[i].Label == target {
			return &labels[i], nil
		}
	}
	var exact []*fleetLabelSummary
	for i := range labels {
		if labels[i].Project == target {
			exact = append(exact, &labels[i])
		}
	}
	if len(exact) == 1 {
		return exact[0], nil
	}
	if len(exact) > 1 {
		names := make([]string, len(exact))
		for i, m := range exact {
			names[i] = m.Label
		}
		return nil, fmt.Errorf("project %q is ambiguous across publishers: %s", target, strings.Join(names, ", "))
	}
	var prefix []*fleetLabelSummary
	for i := range labels {
		if strings.HasPrefix(labels[i].Project, target) {
			prefix = append(prefix, &labels[i])
		}
	}
	if len(prefix) == 1 {
		return prefix[0], nil
	}
	if len(prefix) > 1 {
		names := make([]string, len(prefix))
		for i, m := range prefix {
			names[i] = m.Project
		}
		return nil, fmt.Errorf("prefix %q is ambiguous; candidates: %s", target, strings.Join(names, ", "))
	}
	return nil, fmt.Errorf("no cairn label matches %q", target)
}

func repoCountWarn(n int) string {
	if n == 0 {
		return "0"
	}
	return yellow(fmt.Sprintf("%d", n))
}

func displayOrDash(s string) string {
	if s == "" {
		return "—"
	}
	return s
}

func shortSha(s string) string {
	if len(s) <= 12 {
		return s
	}
	return s[:12]
}

func writeJSONPretty(v any) error {
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}

// ---------- vigocli cairn evict ----------

var cairnEvictCmd = &cobra.Command{
	Use:   "evict",
	Short: "Prune all cairn mirrors sourced from a host fleet-wide",
	Long: `Dispatch a task to every envoy instructing the cairn reactor to prune
any receiver-side mirrors whose seeder hostname matches the target.
Useful for decommissioning a host: after eviction, no envoy in the
fleet will hold mirrors sourced from the evicted host.

Scope with --user <u> to limit the prune to a specific user's
mirrors (otherwise every user's mirrors sourced from the host are
pruned).

Cairn has no cryptographic membership — admin evict is just a
fleet-wide prune. Evicted hosts' own mirrors on themselves are
untouched (they can rebuild the local set from their sidecars).`,
	RunE: func(cmd *cobra.Command, args []string) error {
		host, _ := cmd.Flags().GetString("host")
		user, _ := cmd.Flags().GetString("user")
		yes, _ := cmd.Flags().GetBool("yes")
		if host == "" {
			return fmt.Errorf("--host is required")
		}
		return runCairnEvict(host, user, yes)
	},
}

func runCairnEvict(host, user string, yes bool) error {
	client := newAPIClient()
	if !yes {
		scope := "all users"
		if user != "" {
			scope = "user=" + user
		}
		fmt.Printf("Will prune every mirror on every envoy whose seeder is %s (%s).\n", host, scope)
		fmt.Print("Proceed? [y/N] ")
		var resp string
		fmt.Scanln(&resp)
		if strings.ToLower(strings.TrimSpace(resp)) != "y" {
			fmt.Println("aborted.")
			return nil
		}
	}

	cmdStr := fmt.Sprintf("vigo cairn prune --seeder %s", host)
	if user != "" {
		cmdStr += " --user " + user
	}
	cmdStr += " --yes"

	req := map[string]any{
		"command": cmdStr,
		"target":  "*",
		"timeout": 120,
	}
	var result map[string]any
	if err := client.post("/api/v1/task", req, &result); err != nil {
		return fmt.Errorf("dispatch evict: %w", err)
	}
	fmt.Printf("✓ dispatched: %s\n", cmdStr)
	fmt.Println("  watch `vigocli cairn status` to confirm mirrors drop fleet-wide.")
	return nil
}

// ---------- vigocli cairn members ----------

var cairnMembersCmd = &cobra.Command{
	Use:   "members",
	Short: "List envoys participating in cairn (publishing and/or receiving)",
	Long:  `Read-only view of envoys participating in the cairn mesh, sourced from the cairn trait projection.`,
	RunE: func(cmd *cobra.Command, args []string) error {
		return runCairnMembers()
	},
}

func runCairnMembers() error {
	client := newAPIClient()
	// /api/v1/cairn/fleet returns an aggregated projection; reuse
	// the existing status handler's output pattern. Minimal impl:
	// delegate to `vigocli cairn status` for now and let operators
	// filter server-side in future follow-ups.
	fmt.Println("cairn participating envoys (from fleet trait projection):")
	fmt.Println()
	var fleet map[string]any
	if err := client.get("/api/v1/cairn/fleet", &fleet); err != nil {
		return fmt.Errorf("fetch cairn fleet: %w", err)
	}
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	return enc.Encode(fleet)
}

func init() {
	cairnStatusCmd.Flags().Bool("json", false, "Emit machine-readable JSON instead of a table")
	cairnListCmd.Flags().Bool("json", false, "Emit machine-readable JSON instead of a table")
	cairnShowCmd.Flags().Bool("json", false, "Emit machine-readable JSON instead of a table")

	cairnEvictCmd.Flags().String("host", "", "Seeder hostname to evict fleet-wide")
	cairnEvictCmd.Flags().String("user", "", "Optionally scope to one user's mirrors")
	cairnEvictCmd.Flags().Bool("yes", false, "Skip confirmation prompt")

	cairnCmd.AddCommand(cairnStatusCmd, cairnListCmd, cairnShowCmd, cairnEvictCmd, cairnMembersCmd)
}
