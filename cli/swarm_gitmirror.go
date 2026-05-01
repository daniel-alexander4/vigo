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

// Admin-side view of gitmirror state across the fleet, sourced from
// every envoy's `gitmirror` trait through the server aggregator at
// `GET /api/v1/swarm/gitmirror/fleet`.
//
// Post-ADR-015 the model is project-id-keyed (every project is
// `vigo://<project_id>/<handle>` with `project_id =
// sha256(founder_fleet_pubkey)`). Membership is a stack of capability
// tokens; the legacy publisher/receiver distinction is gone.
//
// All subcommands are read-only. Recovery, push, and revoke
// operations live on the envoy side under
// `vigo swarm gitmirror project ...`.

// fleetResponse mirrors the shape of `server/swarm/gitmirrormesh.Fleet`
// exactly. Duplicated here so the CLI stays decoupled from the
// server package (matches the pattern in cli/swarm.go).
type fleetResponse struct {
	GeneratedAt  time.Time              `json:"generated_at"`
	Participants []string               `json:"participants"`
	Totals       fleetTotals            `json:"totals"`
	Projects     []fleetProjectSummary  `json:"projects"`
}

type fleetTotals struct {
	Projects      int `json:"projects"`
	Hosts         int `json:"hosts"`
	DivergentRefs int `json:"divergent_refs"`
	TotalRevokes  int `json:"total_revokes"`
	TotalLeaves   int `json:"total_leaves"`
}

type fleetProjectSummary struct {
	ProjectID          string                  `json:"project_id"`
	Handle             string                  `json:"handle"`
	FounderFleetPubkey string                  `json:"founder_fleet_pubkey"`
	Members            []string                `json:"members"`
	DrFanout           string                  `json:"dr_fanout"`
	Hosts              []fleetHostPresence     `json:"hosts"`
	HeadRefs           map[string]fleetRefStat `json:"head_refs"`
	MemberFleetCount   int                     `json:"member_fleet_count"`
	HostCount          int                     `json:"host_count"`
	LastUpdated        int64                   `json:"last_updated"`
	DivergentRefs      []string                `json:"divergent_refs,omitempty"`
	RevokesCount       int                     `json:"revokes_count"`
	LeavesCount        int                     `json:"leaves_count"`
	DigestUnanimous    bool                    `json:"digest_unanimous"`
}

type fleetHostPresence struct {
	Host            string            `json:"host"`
	UpdatedAt       int64             `json:"updated_at"`
	HasBareRepo     bool              `json:"has_bare_repo"`
	SizeBytes       uint64            `json:"size_bytes,omitempty"`
	HeadRefs        map[string]string `json:"head_refs,omitempty"`
	EnvelopesDigest string            `json:"envelopes_digest,omitempty"`
	UsersWithState  []string          `json:"users_with_state,omitempty"`
	LeafRole        string            `json:"leaf_role,omitempty"`
}

type fleetRefStat struct {
	Sha           string   `json:"sha,omitempty"`
	HostsAgreeing []string `json:"hosts_agreeing,omitempty"`
	HostsLagging  []string `json:"hosts_lagging,omitempty"`
}

var gitmirrorCmd = &cobra.Command{
	Use:   "gitmirror",
	Short: "Fleet-wide view of gitmirror projects",
	Long: `Read-only admin view of the unified-substrate gitmirror mesh
(ADR-015). Each project is identified by
project_id = sha256(founder_fleet_pubkey) and accessed via
vigo://<project_id>/<handle>. Membership is a stack of Ed25519
capability tokens.

Use the envoy-side 'vigo swarm gitmirror project ...' verbs to
init / invite / join / leave / revoke / reanchor projects. This
admin command is observability only.`,
}

var gitmirrorStatusCmd = &cobra.Command{
	Use:   "status",
	Short: "Fleet-wide gitmirror totals",
	Args:  cobra.NoArgs,
	RunE:  runGitmirrorStatus,
}

var gitmirrorListCmd = &cobra.Command{
	Use:   "list",
	Short: "One row per project across the fleet",
	Args:  cobra.NoArgs,
	RunE:  runGitmirrorList,
}

var gitmirrorShowCmd = &cobra.Command{
	Use:   "show <project_id|handle>",
	Short: "Detailed view of one project with per-host breakdown",
	Args:  cobra.ExactArgs(1),
	RunE:  runGitmirrorShow,
}

func fetchFleet() (*fleetResponse, error) {
	client := newAPIClient()
	var resp fleetResponse
	if err := client.get("/api/v1/swarm/gitmirror/fleet", &resp); err != nil {
		return nil, fmt.Errorf("fetching fleet: %w", err)
	}
	return &resp, nil
}

func runGitmirrorStatus(cmd *cobra.Command, _ []string) error {
	asJSON, _ := cmd.Flags().GetBool("json")
	fleet, err := fetchFleet()
	if err != nil {
		return err
	}
	if asJSON {
		return writeJSONPretty(fleet)
	}

	fmt.Printf("Gitmirror fleet — generated %s\n\n",
		fleet.GeneratedAt.Local().Format("2006-01-02 15:04:05"))

	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintf(w, "%s\t%d\n", bold("Projects"), fleet.Totals.Projects)
	fmt.Fprintf(w, "%s\t%d\n", bold("Hosts"), fleet.Totals.Hosts)
	fmt.Fprintf(w, "%s\t%s\n", bold("Divergent refs"), gitmirrorCountWarn(fleet.Totals.DivergentRefs))
	fmt.Fprintf(w, "%s\t%d\n", bold("Revoke envelopes"), fleet.Totals.TotalRevokes)
	fmt.Fprintf(w, "%s\t%d\n", bold("Leave envelopes"), fleet.Totals.TotalLeaves)
	fmt.Fprintf(w, "%s\t%d\n", bold("Participants"), len(fleet.Participants))
	w.Flush()
	return nil
}

func runGitmirrorList(cmd *cobra.Command, _ []string) error {
	asJSON, _ := cmd.Flags().GetBool("json")
	fleet, err := fetchFleet()
	if err != nil {
		return err
	}
	if asJSON {
		return writeJSONPretty(fleet.Projects)
	}

	if len(fleet.Projects) == 0 {
		fmt.Println("No gitmirror projects reported by the fleet.")
		return nil
	}

	sorted := append([]fleetProjectSummary(nil), fleet.Projects...)
	sort.Slice(sorted, func(i, j int) bool {
		if sorted[i].Handle != sorted[j].Handle {
			return sorted[i].Handle < sorted[j].Handle
		}
		return sorted[i].ProjectID < sorted[j].ProjectID
	})

	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\t%s\n",
		bold("HANDLE"), bold("FAN-OUT"), bold("MEMBERS"),
		bold("HOSTS"), bold("DIVERGENT"), bold("ENVELOPES"), bold("PROJECT_ID"))
	for _, p := range sorted {
		divergent := "—"
		if len(p.DivergentRefs) > 0 {
			divergent = yellow(fmt.Sprintf("%d ref(s)", len(p.DivergentRefs)))
		}
		envelopes := fmt.Sprintf("%d R / %d L", p.RevokesCount, p.LeavesCount)
		if !p.DigestUnanimous && p.HostCount > 1 {
			envelopes += " " + yellow("(skew)")
		}
		fmt.Fprintf(w, "%s\t%s\t%d\t%d\t%s\t%s\t%s\n",
			p.Handle, p.DrFanout, p.MemberFleetCount, p.HostCount,
			divergent, envelopes, shortSha(p.ProjectID))
	}
	w.Flush()
	return nil
}

func runGitmirrorShow(cmd *cobra.Command, args []string) error {
	target := args[0]
	asJSON, _ := cmd.Flags().GetBool("json")
	fleet, err := fetchFleet()
	if err != nil {
		return err
	}

	match, err := resolveFleetProject(fleet.Projects, target)
	if err != nil {
		return err
	}
	if asJSON {
		return writeJSONPretty(match)
	}

	fmt.Printf("Project:        %s\n", bold(match.Handle))
	fmt.Printf("URL:            vigo://%s/%s\n", match.ProjectID, match.Handle)
	fmt.Printf("Project ID:     %s\n", match.ProjectID)
	fmt.Printf("Founder fleet:  ed25519:%s\n", match.FounderFleetPubkey)
	fmt.Printf("Fan-out:        %s\n", match.DrFanout)
	fmt.Printf("Members (%d):\n", match.MemberFleetCount)
	for _, m := range match.Members {
		fmt.Printf("  ed25519:%s\n", m)
	}
	fmt.Printf("Hosts:          %d\n", match.HostCount)
	if match.LastUpdated > 0 {
		fmt.Printf("Last updated:   %s\n",
			time.Unix(match.LastUpdated, 0).Local().Format("2006-01-02 15:04:05"))
	}
	envState := green("unanimous")
	if !match.DigestUnanimous && match.HostCount > 1 {
		envState = yellow("divergent — gossip in flight")
	}
	fmt.Printf("Envelope state: %s · %d revoke(s), %d leave(s)\n",
		envState, match.RevokesCount, match.LeavesCount)
	fmt.Println()

	// Refs
	if len(match.HeadRefs) > 0 {
		refKeys := make([]string, 0, len(match.HeadRefs))
		for r := range match.HeadRefs {
			refKeys = append(refKeys, r)
		}
		sort.Strings(refKeys)
		fmt.Println("Refs:")
		w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
		fmt.Fprintf(w, "  %s\t%s\t%s\n", bold("REF"), bold("CONSENSUS"), bold("LAGGING"))
		for _, r := range refKeys {
			st := match.HeadRefs[r]
			consensus := "—"
			if st.Sha != "" {
				consensus = shortSha(st.Sha)
			} else {
				consensus = yellow("divergent")
			}
			lagging := "—"
			if len(st.HostsLagging) > 0 {
				lagging = strings.Join(st.HostsLagging, ", ")
			}
			fmt.Fprintf(w, "  %s\t%s\t%s\n", r, consensus, lagging)
		}
		w.Flush()
		fmt.Println()
	}

	// Hosts
	if len(match.Hosts) == 0 {
		fmt.Println("(no host has reported this project yet)")
		return nil
	}
	fmt.Println("Hosts:")
	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintf(w, "  %s\t%s\t%s\t%s\t%s\t%s\n",
		bold("HOST"), bold("BARE REPO"), bold("SIZE"),
		bold("USERS"), bold("LEAF ROLE"), bold("LAST SEEN"))
	for _, h := range match.Hosts {
		repo := "—"
		size := "—"
		if h.HasBareRepo {
			repo = green("yes")
			size = formatBytes(int64(h.SizeBytes))
		}
		users := "—"
		if len(h.UsersWithState) > 0 {
			users = strings.Join(h.UsersWithState, ", ")
		}
		role := "—"
		if h.LeafRole != "" {
			role = h.LeafRole
		}
		seen := "—"
		if h.UpdatedAt > 0 {
			seen = time.Unix(h.UpdatedAt, 0).Local().Format("2006-01-02 15:04")
		}
		fmt.Fprintf(w, "  %s\t%s\t%s\t%s\t%s\t%s\n",
			h.Host, repo, size, users, role, seen)
	}
	w.Flush()
	return nil
}

// resolveFleetProject matches `target` against the fleet's projects:
// exact project_id, exact handle, then unique handle prefix. An
// ambiguous match is an error (caller must pass the full project_id).
func resolveFleetProject(projects []fleetProjectSummary, target string) (*fleetProjectSummary, error) {
	if len(projects) == 0 {
		return nil, fmt.Errorf("the fleet has no gitmirror projects")
	}
	// Exact project_id (canonical) wins regardless of handle collisions.
	for i := range projects {
		if projects[i].ProjectID == target {
			return &projects[i], nil
		}
	}
	// Exact handle match.
	var exact []*fleetProjectSummary
	for i := range projects {
		if projects[i].Handle == target {
			exact = append(exact, &projects[i])
		}
	}
	if len(exact) == 1 {
		return exact[0], nil
	}
	if len(exact) > 1 {
		ids := make([]string, len(exact))
		for i, p := range exact {
			ids[i] = shortSha(p.ProjectID)
		}
		return nil, fmt.Errorf("handle %q is ambiguous across %d projects (project_ids %s); pass the full project_id",
			target, len(exact), strings.Join(ids, ", "))
	}
	// Unique handle prefix.
	var prefix []*fleetProjectSummary
	for i := range projects {
		if strings.HasPrefix(projects[i].Handle, target) {
			prefix = append(prefix, &projects[i])
		}
	}
	if len(prefix) == 1 {
		return prefix[0], nil
	}
	if len(prefix) > 1 {
		handles := make([]string, len(prefix))
		for i, p := range prefix {
			handles[i] = p.Handle
		}
		return nil, fmt.Errorf("prefix %q is ambiguous; candidates: %s",
			target, strings.Join(handles, ", "))
	}
	return nil, fmt.Errorf("no gitmirror project matches %q", target)
}

func gitmirrorCountWarn(n int) string {
	if n == 0 {
		return "0"
	}
	return yellow(fmt.Sprintf("%d", n))
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

func init() {
	gitmirrorStatusCmd.Flags().Bool("json", false, "Emit machine-readable JSON instead of a table")
	gitmirrorListCmd.Flags().Bool("json", false, "Emit machine-readable JSON instead of a table")
	gitmirrorShowCmd.Flags().Bool("json", false, "Emit machine-readable JSON instead of a table")

	gitmirrorCmd.AddCommand(gitmirrorStatusCmd, gitmirrorListCmd, gitmirrorShowCmd)
}
