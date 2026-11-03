// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"golang.org/x/term"
)

// spannerBoltCmd is the per-bolt noun group under `vigocli spanner`.
// Hosts the listing, inspection, drain, reassign, and membership verbs.
var spannerBoltCmd = &cobra.Command{
	Use:   "bolt",
	Short: "Operate on individual bolts",
	Long: `Verbs that target a single bolt — listing, inspecting, draining,
reassigning envoys, and managing membership. The bare command (no verb)
prints this help, per ADR-Phase-0 design — it does NOT default to 'list'.`,
}

var spannerBoltListCmd = &cobra.Command{
	Use:   "list",
	Short: "List all bolts in the admissions roster",
	RunE:  runSpannerBoltList,
}

var spannerBoltInviteCmd = &cobra.Command{
	Use:   "invite",
	Short: "Mint a signed invite bundle authorizing another server to join this spanner",
	Long: `Mints a single-use, signed invite bundle on the local server. The
bundle carries a random token, this spanner's ID, the local peer
address, and an expiry. Hand the bundle (out of band) to the operator
of the joining server, who runs 'vigocli spanner bolt join --from
<this-server> --code <bundle>'.

The bundle is a secret — anyone holding it can join the spanner until
it expires or is consumed.`,
	RunE: runSpannerBoltInvite,
}

var spannerBoltJoinCmd = &cobra.Command{
	Use:   "join --from <url> --code <bundle>",
	Short: "Join an existing spanner using an invite bundle minted on a member",
	Long: `Joins an existing spanner. The local server forwards its identity +
the invite bundle to the inviting server's admit endpoint, receives a
signed admission row + the current roster, and stores the roster
locally.

Both --from (the inviting server's URL) and --code (the bundle string
printed by 'vigocli spanner bolt invite' on --from) are required.`,
	RunE: runSpannerBoltJoin,
}

var spannerBoltShowCmd = &cobra.Command{
	Use:   "show <bolt-id>",
	Short: "Show a bolt's compliance breakdown and envoy count from gossip-backed observability",
	Args:  cobra.ExactArgs(1),
	RunE:  runSpannerBoltShow,
}

var spannerBoltEnvoysCmd = &cobra.Command{
	Use:   "envoys <bolt-id>",
	Short: "List all envoys managed by a specific bolt",
	Args:  cobra.ExactArgs(1),
	RunE:  runSpannerBoltEnvoys,
}

var spannerBoltRunsCmd = &cobra.Command{
	Use:   "runs <bolt-id>",
	Short: "List recent convergence runs on a specific bolt",
	Args:  cobra.ExactArgs(1),
	RunE:  runSpannerBoltRuns,
}

var spannerBoltPushCmd = &cobra.Command{
	Use:   "push <bolt-id>",
	Short: "Trigger immediate check-in for all envoys on a specific bolt",
	Args:  cobra.ExactArgs(1),
	RunE:  runSpannerBoltPush,
}

var spannerBoltReassignCmd = &cobra.Command{
	Use:   "reassign <envoy-id>",
	Short: "Move an envoy to a different bolt without re-enrollment",
	Args:  cobra.ExactArgs(1),
	RunE:  runSpannerBoltReassign,
}

var spannerBoltDrainCmd = &cobra.Command{
	Use:   "drain <bolt-id>",
	Short: "Migrate all envoys off a bolt to prepare it for maintenance or decommission",
	Args:  cobra.ExactArgs(1),
	RunE:  runSpannerBoltDrain,
}

func init() {
	spannerBoltInviteCmd.Flags().Duration("ttl", 24*time.Hour, "How long the invite bundle stays valid")
	spannerBoltInviteCmd.Flags().String("out", "", "Write the bundle to this file (default: stdout)")

	spannerBoltJoinCmd.Flags().String("from", "", "HTTPS URL of an inviting spanner member (e.g. https://srv1.example.com:8443)")
	spannerBoltJoinCmd.Flags().String("code", "", "Invite bundle string printed by `vigocli spanner bolt invite` on --from")
	_ = spannerBoltJoinCmd.MarkFlagRequired("from")
	_ = spannerBoltJoinCmd.MarkFlagRequired("code")

	spannerBoltPushCmd.Flags().String("envoy-id", "", "Target a specific envoy by ID")

	spannerBoltReassignCmd.Flags().String("from", "", "Source bolt ID")
	spannerBoltReassignCmd.Flags().String("to", "", "Target bolt ID")
	spannerBoltReassignCmd.MarkFlagRequired("from")
	spannerBoltReassignCmd.MarkFlagRequired("to")

	spannerBoltDrainCmd.Flags().String("to", "", "Target bolt ID (optional, auto-route by pattern if empty)")

	spannerBoltCmd.AddCommand(spannerBoltListCmd)
	spannerBoltCmd.AddCommand(spannerBoltInviteCmd)
	spannerBoltCmd.AddCommand(spannerBoltJoinCmd)
	spannerBoltCmd.AddCommand(spannerBoltShowCmd)
	spannerBoltCmd.AddCommand(spannerBoltEnvoysCmd)
	spannerBoltCmd.AddCommand(spannerBoltRunsCmd)
	spannerBoltCmd.AddCommand(spannerBoltPushCmd)
	spannerBoltCmd.AddCommand(spannerBoltReassignCmd)
	spannerBoltCmd.AddCommand(spannerBoltDrainCmd)
}

// rosterRowView is the CLI projection of one admissions-roster row
// returned by GET /api/v1/spanner/roster.
type rosterRowView struct {
	Pubkey            string `json:"pubkey"`
	Hostname          string `json:"hostname"`
	AdmittedAt        int64  `json:"admitted_at"`
	AdmittedBy        string `json:"admitted_by"`
	ConsumedTokenHash string `json:"consumed_token_hash"`
	RevokedAt         int64  `json:"revoked_at"`
	PatternClaims     string `json:"pattern_claims"`
}

func runSpannerBoltList(cmd *cobra.Command, args []string) error {
	client := newAPIClient()

	var resp struct {
		Roster []rosterRowView `json:"roster"`
	}
	if err := client.get("/api/v1/spanner/roster", &resp); err != nil {
		return fmt.Errorf("cannot reach server: %w", err)
	}

	if len(resp.Roster) == 0 {
		fmt.Println("  No bolts in the roster — found a spanner with `vigocli spanner init`.")
		return nil
	}

	w := newAlignedTable(os.Stdout)
	fmt.Fprintf(w, "  %s\t%s\t%s\t%s\t%s\n",
		header("PUBKEY"), header("HOSTNAME"), header("ADMITTED"), header("ADMITTED BY"), header("STATUS"))
	for _, b := range resp.Roster {
		status := colorStatus("healthy")
		if b.RevokedAt != 0 {
			status = colorStatus("unreachable")
		}
		admitter := b.AdmittedBy
		if admitter == b.Pubkey {
			admitter = "(self / founder)"
		} else {
			admitter = shortPubkey(admitter)
		}
		admittedAt := time.UnixMilli(b.AdmittedAt).UTC().Format("2006-01-02 15:04 UTC")
		fmt.Fprintf(w, "  %s\t%s\t%s\t%s\t%s\n",
			shortPubkey(b.Pubkey), b.Hostname, admittedAt, admitter, status)
	}
	w.Flush()
	return nil
}

// runSpannerBoltInvite mints a signed invite bundle server-side and
// prints or writes it.
func runSpannerBoltInvite(cmd *cobra.Command, _ []string) error {
	ttl, _ := cmd.Flags().GetDuration("ttl")
	out, _ := cmd.Flags().GetString("out")

	client := newAPIClient()
	body := map[string]any{"ttl_seconds": int64(ttl.Seconds())}
	var resp struct {
		Bundle    string `json:"bundle"`
		SpannerID string `json:"spanner_id"`
		PeerAddr  string `json:"peer_addr"`
		ExpiresAt int64  `json:"expires_at"`
	}
	if err := client.post("/api/v1/spanner/bolt/invite", body, &resp); err != nil {
		return fmt.Errorf("minting invite: %w", err)
	}

	expires := time.UnixMilli(resp.ExpiresAt).Local().Format("2006-01-02 15:04:05 MST")

	if out != "" {
		if err := os.WriteFile(out, []byte(resp.Bundle+"\n"), 0600); err != nil {
			return fmt.Errorf("writing bundle: %w", err)
		}
		fmt.Printf("Invite bundle written to %s (mode 0600)\n", out)
		fmt.Printf("  Spanner ID: %s\n", resp.SpannerID)
		fmt.Printf("  Expires:    %s\n", expires)
		fmt.Printf("\n  On the joining server, run:\n")
		fmt.Printf("    vigocli spanner bolt join --from %s --code \"$(cat %s)\"\n", resp.PeerAddr, out)
		return nil
	}

	// stdout: warn (on stderr) that the bundle is a secret when
	// stdout is a TTY — a piped/redirected bundle is the expected
	// scripting path and shouldn't be cluttered.
	if term.IsTerminal(int(os.Stdout.Fd())) {
		fmt.Fprintf(os.Stderr, "warning: the invite bundle below is a secret — anyone holding it can join the spanner until %s\n\n", expires)
	}
	fmt.Println(resp.Bundle)
	if term.IsTerminal(int(os.Stdout.Fd())) {
		fmt.Fprintf(os.Stderr, "\nOn the joining server, run:\n")
		fmt.Fprintf(os.Stderr, "  vigocli spanner bolt join --from %s --code <bundle-above>\n", resp.PeerAddr)
	}
	return nil
}

// runSpannerBoltJoin asks the local server to join an existing
// spanner by forwarding the invite bundle to --from's admit endpoint.
func runSpannerBoltJoin(cmd *cobra.Command, _ []string) error {
	from, _ := cmd.Flags().GetString("from")
	code, _ := cmd.Flags().GetString("code")

	client := newAPIClient()
	body := map[string]string{"from_addr": from, "bundle": strings.TrimSpace(code)}
	var resp struct {
		Joined     bool            `json:"joined"`
		FromAddr   string          `json:"from_addr"`
		RosterSize int             `json:"roster_size"`
		Roster     []rosterRowView `json:"roster"`
	}
	if err := client.post("/api/v1/spanner/bolt/join", body, &resp); err != nil {
		return fmt.Errorf("joining spanner: %w", err)
	}

	fmt.Printf("Joined spanner via %s\n", resp.FromAddr)
	fmt.Printf("  Roster: %d bolt(s)\n", resp.RosterSize)
	for _, b := range resp.Roster {
		fmt.Printf("    - %s  %s\n", shortPubkey(b.Pubkey), b.Hostname)
	}
	fmt.Printf("\n  Inspect the roster anytime with `vigocli spanner bolt list`.\n")
	return nil
}

func runSpannerBoltShow(cmd *cobra.Command, args []string) error {
	client := newAPIClient()
	boltID := args[0]

	// The bolt-id is a peer bolt's pubkey-hex; the detail comes from
	// the local gossip-backed observability cache.
	var resp struct {
		BoltPubkey         string `json:"bolt_pubkey"`
		EnvoyCount         int    `json:"envoy_count"`
		Converged          int    `json:"converged"`
		Degraded           int    `json:"degraded"`
		Failed             int    `json:"failed"`
		NoData             int    `json:"no_data"`
		Offline            int    `json:"offline"`
		SnapshotAgeSeconds int64  `json:"snapshot_age_seconds"`
	}

	if err := client.get("/api/v1/spanner/bolts/"+boltID, &resp); err != nil {
		return fmt.Errorf("bolt %q not found in the gossiped roster: %w", boltID, err)
	}

	var convPct float64
	if active := resp.EnvoyCount - resp.Offline; active > 0 {
		convPct = float64(resp.Converged) / float64(active) * 100
	}

	fmt.Printf("  Bolt: %s\n", bold(resp.BoltPubkey))
	fmt.Printf("  Snapshot age: %ds\n", resp.SnapshotAgeSeconds)
	fmt.Println()
	fmt.Printf("  Envoys: %d\n", resp.EnvoyCount)
	fmt.Printf("  Compliance: %.1f%%\n", convPct)
	fmt.Printf("    Converged: %s\n", green(fmt.Sprintf("%d", resp.Converged)))
	fmt.Printf("    Degraded:  %s\n", yellow(fmt.Sprintf("%d", resp.Degraded)))
	fmt.Printf("    Failed:    %s\n", red(fmt.Sprintf("%d", resp.Failed)))
	fmt.Printf("    Offline:   %d\n", resp.Offline)
	fmt.Printf("    No Data:   %d\n", resp.NoData)

	return nil
}

func runSpannerBoltEnvoys(cmd *cobra.Command, args []string) error {
	client := newAPIClient()
	boltID := args[0]

	var resp struct {
		Envoys []struct {
			ID         string `json:"id"`
			Hostname   string `json:"hostname"`
			IP         string `json:"ip"`
			Revoked    bool   `json:"revoked"`
			LastSeenMs int64  `json:"last_seen_ms"`
		} `json:"envoys"`
	}

	if err := client.get("/api/v1/spanner/bolts/"+boltID+"/envoys", &resp); err != nil {
		return fmt.Errorf("cannot list envoys for bolt %q: %w", boltID, err)
	}

	if len(resp.Envoys) == 0 {
		fmt.Printf("  No envoys on bolt %s.\n", boltID)
		return nil
	}

	w := newAlignedTable(os.Stdout)
	fmt.Fprintf(w, "  %s\t%s\t%s\t%s\t%s\n", header("ID"), header("HOSTNAME"), header("IP ADDRESS"), header("LAST SEEN"), header("REVOKED"))
	for _, t := range resp.Envoys {
		revoked := ""
		if t.Revoked {
			revoked = red("yes")
		}
		lastSeen := "-"
		if t.LastSeenMs > 0 {
			lastSeen = time.UnixMilli(t.LastSeenMs).Local().Format("2006-01-02 15:04:05")
		}
		fmt.Fprintf(w, "  %s\t%s\t%s\t%s\t%s\n", t.ID, t.Hostname, t.IP, lastSeen, revoked)
	}
	w.Flush()
	return nil
}

func runSpannerBoltRuns(cmd *cobra.Command, args []string) error {
	client := newAPIClient()
	boltID := args[0]

	var resp struct {
		Runs []struct {
			ID            string `json:"id"`
			EnvoyID       string `json:"envoy_id"`
			Status        string `json:"status"`
			ResourceCount int32  `json:"resource_count"`
			ChangedCount  int32  `json:"changed_count"`
			FailedCount   int32  `json:"failed_count"`
			StartedAt     string `json:"started_at,omitempty"`
		} `json:"runs"`
	}

	if err := client.get("/api/v1/spanner/bolts/"+boltID+"/runs", &resp); err != nil {
		return fmt.Errorf("cannot list runs for bolt %q: %w", boltID, err)
	}

	if len(resp.Runs) == 0 {
		fmt.Printf("  No runs on bolt %s.\n", boltID)
		return nil
	}

	w := newAlignedTable(os.Stdout)
	fmt.Fprintf(w, "  %s\t%s\t%s\t%s\t%s\t%s\n", header("ID"), header("ENVOY"), header("STATUS"), header("CONFIGCRATES"), header("CHANGED"), header("FAILED"))
	for _, r := range resp.Runs {
		fmt.Fprintf(w, "  %s\t%s\t%s\t%d\t%d\t%d\n",
			r.ID, r.EnvoyID, r.Status, r.ResourceCount, r.ChangedCount, r.FailedCount)
	}
	w.Flush()
	return nil
}

func runSpannerBoltPush(cmd *cobra.Command, args []string) error {
	client := newAPIClient()
	boltID := args[0]

	envoyID, _ := cmd.Flags().GetString("envoy-id")

	var resp struct {
		EnvoysNotified int32 `json:"envoys_notified"`
	}

	body := map[string]string{}
	if envoyID != "" {
		body["envoy_id"] = envoyID
	}

	if err := client.post("/api/v1/spanner/bolts/"+boltID+"/push", body, &resp); err != nil {
		return fmt.Errorf("cannot push to bolt %q: %w", boltID, err)
	}

	fmt.Printf("  Force push sent to bolt %s: %d envoys notified.\n", boltID, resp.EnvoysNotified)
	return nil
}

func runSpannerBoltReassign(cmd *cobra.Command, args []string) error {
	ok, err := confirmAction(cmd, "Reassign this envoy to a different bolt?")
	if err != nil {
		return err
	}
	if !ok {
		fmt.Println("Cancelled.")
		return nil
	}

	client := newAPIClient()
	envoyID := args[0]
	fromBolt, _ := cmd.Flags().GetString("from")
	toBolt, _ := cmd.Flags().GetString("to")

	var resp struct {
		NewEnvoyID string `json:"new_envoy_id"`
		TargetAddr string `json:"target_addr"`
	}

	body := map[string]string{
		"envoy_id":     envoyID,
		"from_bolt_id": fromBolt,
		"to_bolt_id":   toBolt,
	}

	if err := client.post("/api/v1/spanner/reassign", body, &resp); err != nil {
		return fmt.Errorf("reassign failed: %w", err)
	}

	fmt.Printf("  Envoy %s reassigned.\n", envoyID)
	fmt.Printf("  New ID:     %s\n", resp.NewEnvoyID)
	fmt.Printf("  Target:     %s\n", resp.TargetAddr)
	fmt.Println("  The agent will reconnect on its next check-in.")

	return nil
}

func runSpannerBoltDrain(cmd *cobra.Command, args []string) error {
	ok, err := confirmAction(cmd, "Drain all envoys from this bolt? They will be migrated to other bolts.")
	if err != nil {
		return err
	}
	if !ok {
		fmt.Println("Cancelled.")
		return nil
	}

	client := newAPIClient()
	boltID := args[0]
	toBolt, _ := cmd.Flags().GetString("to")

	var resp struct {
		Moved  int32    `json:"moved"`
		Failed int32    `json:"failed"`
		Errors []string `json:"errors"`
	}

	body := map[string]string{}
	if toBolt != "" {
		body["target_bolt_id"] = toBolt
	}

	if err := client.post("/api/v1/spanner/bolts/"+boltID+"/drain", body, &resp); err != nil {
		return fmt.Errorf("drain failed: %w", err)
	}

	fmt.Printf("  Bolt %s drained: %d moved, %d failed.\n", boltID, resp.Moved, resp.Failed)

	for _, e := range resp.Errors {
		fmt.Printf("  %s %s\n", red("Error:"), e)
	}

	return nil
}
