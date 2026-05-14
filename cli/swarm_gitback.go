// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"
)

// Admin-side view of gitback state across the fleet, sourced from
// every envoy's `gitback` trait through the server aggregator at
// `GET /api/v1/swarm/gitback/fleet`.
//
// Post-ADR-015 the model is project-id-keyed (every project is
// `gitback://<project_id>/<handle>` with `project_id =
// sha256(founder_puddle_pubkey ‖ handle)`, or `gitback://<puddle-name>/<handle>`
// when the founder puddle has claimed a name — ADR-022). Membership is
// a stack of capability tokens; the legacy publisher/receiver
// distinction is gone.
//
// All subcommands are read-only. Recovery, push, and revoke
// operations live on the envoy side under
// `vigo swarm gitback project ...`.

// fleetResponse mirrors the shape of `server/swarm/gitbackmesh.Fleet`
// exactly. Duplicated here so the CLI stays decoupled from the
// server package (matches the pattern in cli/swarm.go).
type fleetResponse struct {
	GeneratedAt  time.Time             `json:"generated_at"`
	Participants []string              `json:"participants"`
	Totals       fleetTotals           `json:"totals"`
	Projects     []fleetProjectSummary `json:"projects"`
}

type fleetTotals struct {
	Projects      int `json:"projects"`
	Hosts         int `json:"hosts"`
	DivergentRefs int `json:"divergent_refs"`
	TotalRevokes  int `json:"total_revokes"`
	TotalLeaves   int `json:"total_leaves"`
}

type fleetProjectSummary struct {
	ProjectID           string                  `json:"project_id"`
	Handle              string                  `json:"handle"`
	FounderPuddlePubkey string                  `json:"founder_puddle_pubkey"`
	FriendlyName        string                  `json:"friendly_name,omitempty"`
	Members             []string                `json:"members"`
	DrScope             string                  `json:"dr_scope"`
	Hosts               []fleetHostPresence     `json:"hosts"`
	HeadRefs            map[string]fleetRefStat `json:"head_refs"`
	MemberFleetCount    int                     `json:"member_fleet_count"`
	HostCount           int                     `json:"host_count"`
	LastUpdated         int64                   `json:"last_updated"`
	DivergentRefs       []string                `json:"divergent_refs,omitempty"`
	RevokesCount        int                     `json:"revokes_count"`
	LeavesCount         int                     `json:"leaves_count"`
	DigestUnanimous     bool                    `json:"digest_unanimous"`
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

var gitbackCmd = &cobra.Command{
	Use:   "gitback",
	Short: "Fleet-wide view of gitback projects",
	Long: `Read-only admin view of the unified-substrate gitback mesh
(ADR-015). Each project is identified by
project_id = sha256(founder_puddle_pubkey ‖ handle) and accessed via
gitback://<project_id>/<handle> — or gitback://<puddle-name>/<handle>
when the founder puddle has claimed a name (ADR-022). Membership is a
stack of Ed25519 capability tokens.

Use the envoy-side 'vigo swarm gitback project ...' verbs to
init / invite / join / leave / revoke / reanchor projects. This
admin command is observability only.`,
}

var gitbackStatusCmd = &cobra.Command{
	Use:   "status",
	Short: "Fleet-wide gitback totals",
	Args:  cobra.NoArgs,
	RunE:  runGitbackStatus,
}

var gitbackListCmd = &cobra.Command{
	Use:   "list",
	Short: "One row per project across the fleet",
	Args:  cobra.NoArgs,
	RunE:  runGitbackList,
}

var gitbackShowCmd = &cobra.Command{
	Use:   "show <project_id|handle>",
	Short: "Detailed view of one project with per-host breakdown",
	Args:  cobra.ExactArgs(1),
	RunE:  runGitbackShow,
}

var gitbackMembersCmd = &cobra.Command{
	Use:   "members [project_id|handle]",
	Short: "Print member fleets per gitback project, formatted as a table",
	Long: `Print the cryptographic membership of every gitback project
across the fleet, sourced from the 'gitback' trait projection.
Pass [project_id|handle] to scope to one project; omit to list every
project's members.

Read-only. Mirrors the shape of 'vigocli swarm lockbox members'.
For host-presence detail use 'vigocli swarm gitback show <project>'.`,
	Args: cobra.MaximumNArgs(1),
	RunE: runGitbackMembers,
}

func fetchFleet() (*fleetResponse, error) {
	client := newAPIClient()
	var resp fleetResponse
	if err := client.get("/api/v1/swarm/gitback/fleet", &resp); err != nil {
		return nil, fmt.Errorf("fetching fleet: %w", err)
	}
	return &resp, nil
}

func runGitbackStatus(cmd *cobra.Command, _ []string) error {
	asJSON, _ := cmd.Flags().GetBool("json")
	fleet, err := fetchFleet()
	if err != nil {
		return err
	}
	if asJSON {
		return writeJSONPretty(fleet)
	}

	fmt.Printf("Gitback fleet — generated %s\n\n",
		fleet.GeneratedAt.Local().Format("2006-01-02 15:04:05"))

	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintf(w, "%s\t%d\n", bold("Projects"), fleet.Totals.Projects)
	fmt.Fprintf(w, "%s\t%d\n", bold("Hosts"), fleet.Totals.Hosts)
	fmt.Fprintf(w, "%s\t%s\n", bold("Divergent refs"), gitbackCountWarn(fleet.Totals.DivergentRefs))
	fmt.Fprintf(w, "%s\t%d\n", bold("Revoke envelopes"), fleet.Totals.TotalRevokes)
	fmt.Fprintf(w, "%s\t%d\n", bold("Leave envelopes"), fleet.Totals.TotalLeaves)
	fmt.Fprintf(w, "%s\t%d\n", bold("Participants"), len(fleet.Participants))
	w.Flush()
	return nil
}

func runGitbackList(cmd *cobra.Command, _ []string) error {
	asJSON, _ := cmd.Flags().GetBool("json")
	fleet, err := fetchFleet()
	if err != nil {
		return err
	}
	if asJSON {
		return writeJSONPretty(fleet.Projects)
	}

	if len(fleet.Projects) == 0 {
		fmt.Println("No gitback projects reported by the fleet.")
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
		bold("HANDLE"), bold("SCOPE"), bold("MEMBERS"),
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
			p.Handle, p.DrScope, p.MemberFleetCount, p.HostCount,
			divergent, envelopes, shortSha(p.ProjectID))
	}
	w.Flush()
	return nil
}

func runGitbackShow(cmd *cobra.Command, args []string) error {
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
	if match.FriendlyName != "" {
		fmt.Printf("URL:            gitback://%s/%s\n", match.FriendlyName, match.Handle)
		fmt.Printf("Canonical URL:  gitback://%s/%s\n", match.ProjectID, match.Handle)
	} else {
		fmt.Printf("URL:            gitback://%s/%s\n", match.ProjectID, match.Handle)
	}
	fmt.Printf("Project ID:     %s\n", match.ProjectID)
	fmt.Printf("Founder puddle: ed25519:%s\n", match.FounderPuddlePubkey)
	fmt.Printf("Scope:          %s\n", match.DrScope)
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

func runGitbackMembers(cmd *cobra.Command, args []string) error {
	asJSON, _ := cmd.Flags().GetBool("json")
	fleet, err := fetchFleet()
	if err != nil {
		return err
	}

	var selected []fleetProjectSummary
	if len(args) == 1 {
		match, err := resolveFleetProject(fleet.Projects, args[0])
		if err != nil {
			return err
		}
		selected = []fleetProjectSummary{*match}
	} else {
		selected = append([]fleetProjectSummary(nil), fleet.Projects...)
	}
	return renderGitbackMembers(selected, asJSON, os.Stdout)
}

func renderGitbackMembers(selected []fleetProjectSummary, asJSON bool, out io.Writer) error {
	if len(selected) == 0 {
		fmt.Fprintln(out, "No gitback projects reported by the fleet.")
		return nil
	}

	sort.Slice(selected, func(i, j int) bool {
		if selected[i].Handle != selected[j].Handle {
			return selected[i].Handle < selected[j].Handle
		}
		return selected[i].ProjectID < selected[j].ProjectID
	})

	if asJSON {
		enc := json.NewEncoder(out)
		enc.SetIndent("", "  ")
		return enc.Encode(selected)
	}

	for _, p := range selected {
		fmt.Fprintf(out, "PROJECT: %s  (project_id %s)\n", p.Handle, shortSha(p.ProjectID))
		w := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
		fmt.Fprintf(w, "%s\t%s\n", bold("ROLE"), bold("FLEET-PUBKEY"))
		for i, fp := range p.Members {
			role := "member"
			if i == 0 || fp == p.FounderPuddlePubkey {
				role = "founder"
			}
			fmt.Fprintf(w, "%s\ted25519:%s\n", role, fp)
		}
		w.Flush()
		fmt.Fprintln(out)
	}
	return nil
}

// resolveFleetProject matches `target` against the fleet's projects:
// exact project_id, exact handle, then unique handle prefix. An
// ambiguous match is an error (caller must pass the full project_id).
func resolveFleetProject(projects []fleetProjectSummary, target string) (*fleetProjectSummary, error) {
	if len(projects) == 0 {
		return nil, fmt.Errorf("the fleet has no gitback projects")
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
	return nil, fmt.Errorf("no gitback project matches %q", target)
}

func gitbackCountWarn(n int) string {
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
	gitbackStatusCmd.Flags().Bool("json", false, "Emit machine-readable JSON instead of a table")
	gitbackListCmd.Flags().Bool("json", false, "Emit machine-readable JSON instead of a table")
	gitbackShowCmd.Flags().Bool("json", false, "Emit machine-readable JSON instead of a table")
	gitbackMembersCmd.Flags().Bool("json", false, "Emit machine-readable JSON instead of a table")

	gitbackCmd.AddCommand(gitbackStatusCmd, gitbackListCmd, gitbackShowCmd, gitbackMembersCmd)
	// Hidden redirect for the agent-side `project` verb tree (init / invite
	// / join / leave / revoke / reanchor / re-invite-all / rejoin /
	// re-found / push / status). All of them sign capability tokens or
	// envelopes with the user's puddle identity, so they live on
	// `vigo swarm gitback project <verb>`. Single redirect under `project`
	// catches every subverb because DisableFlagParsing forwards the rest.
	addAgentRedirects(gitbackCmd, "gitback", "gitback", []string{"project", "purge"})
}
