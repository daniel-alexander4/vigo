// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

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
	Short: "Show a bolt's compliance breakdown, envoy count, and recent activity",
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

var spannerBoltAddCmd = &cobra.Command{
	Use:   "add <bolt-id>",
	Short: "Register a new bolt with the spanner and generate a sample server.yaml for it",
	Long: `Adds a bolt entry to the hub's server.yaml and generates a sample
server.yaml for the new bolt pre-filled with its ID, hub address,
and hostname patterns. The new bolt's Vigo server is deployed
separately (Docker Compose); this command only configures the hub
to recognize it.`,
	Args: cobra.ExactArgs(1),
	RunE: runSpannerBoltAdd,
}

var spannerBoltRemoveCmd = &cobra.Command{
	Use:   "remove <bolt-id>",
	Short: "Drain and remove a bolt from the spanner configuration",
	Long: `Drains all envoys from the bolt (reassigning to other bolts by
pattern), then removes the bolt entry from the hub's server.yaml.`,
	Args: cobra.ExactArgs(1),
	RunE: runSpannerBoltRemove,
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

	spannerBoltAddCmd.Flags().String("addr", "", "Bolt gRPC address (host:port)")
	spannerBoltAddCmd.Flags().StringSlice("patterns", nil, "Hostname glob patterns routed to this bolt")
	spannerBoltAddCmd.Flags().String("output", "", "Write sample bolt server.yaml to this path (default: stdout)")
	spannerBoltAddCmd.MarkFlagRequired("addr")
	spannerBoltAddCmd.MarkFlagRequired("patterns")

	spannerBoltCmd.AddCommand(spannerBoltListCmd)
	spannerBoltCmd.AddCommand(spannerBoltInviteCmd)
	spannerBoltCmd.AddCommand(spannerBoltJoinCmd)
	spannerBoltCmd.AddCommand(spannerBoltShowCmd)
	spannerBoltCmd.AddCommand(spannerBoltEnvoysCmd)
	spannerBoltCmd.AddCommand(spannerBoltRunsCmd)
	spannerBoltCmd.AddCommand(spannerBoltPushCmd)
	spannerBoltCmd.AddCommand(spannerBoltReassignCmd)
	spannerBoltCmd.AddCommand(spannerBoltDrainCmd)
	spannerBoltCmd.AddCommand(spannerBoltAddCmd)
	spannerBoltCmd.AddCommand(spannerBoltRemoveCmd)
}

func runSpannerBoltAdd(cmd *cobra.Command, args []string) error {
	boltID := args[0]
	addr, _ := cmd.Flags().GetString("addr")
	patterns, _ := cmd.Flags().GetStringSlice("patterns")
	output, _ := cmd.Flags().GetString("output")

	// Generate sample server.yaml for the new bolt.
	sample := fmt.Sprintf(`# Vigo Server Configuration — Bolt: %s
# Generated by: vigocli spanner bolt add
# Deploy this file as server.yaml on the new bolt's server.

server:
  grpc_listen: ":1530"
  api_listen: ":8443"
  tls:
    cert_file: "tls/cert"
    key_file: "tls/key"
    ca_file: "tls/ca"
    ca_key_file: "tls/ca_key"

database:
  dsn: "vigo.db"

spanner:
  role: spoke
  bolt_id: "%s"
  hub_addr: "%s"

checkin:
  interval: "15s"

secrets:
  backend: local
`, boltID, boltID, addr)

	client := newAPIClient()
	var status struct {
		Role string `json:"role"`
	}
	if err := client.get("/api/v1/spanner/status", &status); err != nil {
		return fmt.Errorf("cannot reach hub: %w", err)
	}
	if status.Role != "hub" {
		return fmt.Errorf("this server is %q, not a hub — run this command against the hub", status.Role)
	}

	if output != "" {
		if err := os.WriteFile(output, []byte(sample), 0644); err != nil {
			return fmt.Errorf("writing sample config: %w", err)
		}
		fmt.Printf("  Sample bolt config written to %s\n", output)
	} else {
		fmt.Println("  --- Sample bolt server.yaml ---")
		fmt.Print(sample)
		fmt.Println("  --------------------------------")
	}

	fmt.Println()
	fmt.Printf("  Add this bolt entry to your hub's server.yaml:\n\n")
	fmt.Printf("    spanner:\n")
	fmt.Printf("      bolts:\n")
	fmt.Printf("        - id: %q\n", boltID)
	fmt.Printf("          addr: %q\n", addr)
	fmt.Printf("          patterns: [%s]\n", quotePatterns(patterns))
	fmt.Println()
	fmt.Println("  Then restart the hub to apply the change.")

	return nil
}

func runSpannerBoltRemove(cmd *cobra.Command, args []string) error {
	boltID := args[0]

	if !confirmAction(fmt.Sprintf("Remove bolt %q? This will drain all envoys first.", boltID)) {
		fmt.Println("Cancelled.")
		return nil
	}

	client := newAPIClient()

	var drainResp struct {
		Moved  int32    `json:"Moved"`
		Failed int32    `json:"Failed"`
		Errors []string `json:"Errors"`
	}
	if err := client.post("/api/v1/spanner/bolts/"+boltID+"/drain", map[string]string{}, &drainResp); err != nil {
		return fmt.Errorf("drain failed: %w", err)
	}

	fmt.Printf("  Bolt %s drained: %d moved, %d failed.\n", boltID, drainResp.Moved, drainResp.Failed)
	for _, e := range drainResp.Errors {
		fmt.Printf("  %s %s\n", red("Error:"), e)
	}

	fmt.Println()
	fmt.Printf("  Remove the bolt entry from your hub's server.yaml:\n\n")
	fmt.Printf("    # Remove from spanner.bolts:\n")
	fmt.Printf("    #   - id: %q\n", boltID)
	fmt.Println()
	fmt.Println("  Then restart the hub to apply the change.")

	return nil
}

func quotePatterns(patterns []string) string {
	quoted := make([]string, len(patterns))
	for i, p := range patterns {
		quoted[i] = fmt.Sprintf("%q", p)
	}
	return strings.Join(quoted, ", ")
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

	var resp struct {
		ID             string   `json:"id"`
		Addr           string   `json:"addr"`
		Patterns       []string `json:"patterns"`
		Healthy        bool     `json:"healthy"`
		EnvoyCount     int32    `json:"envoy_count"`
		Converged      int32    `json:"converged"`
		Relapsed       int32    `json:"relapsed"`
		Diverged       int32    `json:"diverged"`
		Failed         int32    `json:"failed"`
		Offline        int32    `json:"offline"`
		NoData         int32    `json:"no_data"`
		ConvergencePct float32  `json:"compliance_pct"`
		LatencyMs      int64    `json:"latency_ms"`
		Error          string   `json:"error"`
	}

	if err := client.get("/api/v1/spanner/bolts/"+boltID, &resp); err != nil {
		return fmt.Errorf("bolt %q not found or unreachable: %w", boltID, err)
	}

	status := colorStatus("healthy")
	if !resp.Healthy {
		status = colorStatus("unreachable")
	}

	fmt.Printf("  Bolt: %s\n", bold(resp.ID))
	fmt.Printf("  Address: %s\n", resp.Addr)
	fmt.Printf("  Status: %s\n", status)
	fmt.Printf("  Latency: %dms\n", resp.LatencyMs)
	fmt.Printf("  Patterns: %s\n", strings.Join(resp.Patterns, ", "))
	fmt.Println()
	fmt.Printf("  Envoys: %d\n", resp.EnvoyCount)
	fmt.Printf("  Compliance: %.1f%%\n", resp.ConvergencePct)
	fmt.Printf("    Converged: %s\n", green(fmt.Sprintf("%d", resp.Converged)))
	fmt.Printf("    Relapsed:        %s\n", yellow(fmt.Sprintf("%d", resp.Relapsed)))
	fmt.Printf("    Diverged:        %s\n", red(fmt.Sprintf("%d", resp.Diverged)))
	fmt.Printf("    Failed:          %s\n", red(fmt.Sprintf("%d", resp.Failed)))
	fmt.Printf("    Offline:    %d\n", resp.Offline)
	fmt.Printf("    No Data:         %d\n", resp.NoData)

	if resp.Error != "" {
		fmt.Printf("\n  Error: %s\n", red(resp.Error))
	}

	return nil
}

func runSpannerBoltEnvoys(cmd *cobra.Command, args []string) error {
	client := newAPIClient()
	boltID := args[0]

	var resp struct {
		Envoys []struct {
			ID        string `json:"id"`
			Hostname  string `json:"hostname"`
			IPAddress string `json:"ip_address"`
			Revoked   bool   `json:"revoked"`
			LastSeen  string `json:"last_seen,omitempty"`
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
	fmt.Fprintf(w, "  %s\t%s\t%s\t%s\n", header("ID"), header("HOSTNAME"), header("IP ADDRESS"), header("REVOKED"))
	for _, t := range resp.Envoys {
		revoked := ""
		if t.Revoked {
			revoked = red("yes")
		}
		fmt.Fprintf(w, "  %s\t%s\t%s\t%s\n", t.ID, t.Hostname, t.IPAddress, revoked)
	}
	w.Flush()
	return nil
}

func runSpannerBoltRuns(cmd *cobra.Command, args []string) error {
	client := newAPIClient()
	boltID := args[0]

	var resp struct {
		Runs []struct {
			ID           string `json:"id"`
			EnvoyID      string `json:"envoy_id"`
			Status       string `json:"status"`
			ModuleCount  int32  `json:"module_count"`
			ChangedCount int32  `json:"changed_count"`
			FailedCount  int32  `json:"failed_count"`
			StartedAt    string `json:"started_at,omitempty"`
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
	fmt.Fprintf(w, "  %s\t%s\t%s\t%s\t%s\t%s\n", header("ID"), header("ENVOY"), header("STATUS"), header("MODULES"), header("CHANGED"), header("FAILED"))
	for _, r := range resp.Runs {
		fmt.Fprintf(w, "  %s\t%s\t%s\t%d\t%d\t%d\n",
			r.ID, r.EnvoyID, r.Status, r.ModuleCount, r.ChangedCount, r.FailedCount)
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
	if !confirmAction("Reassign this envoy to a different bolt?") {
		fmt.Println("Cancelled.")
		return nil
	}

	client := newAPIClient()
	envoyID := args[0]
	fromBolt, _ := cmd.Flags().GetString("from")
	toBolt, _ := cmd.Flags().GetString("to")

	var resp struct {
		NewEnvoyID string `json:"NewEnvoyID"`
		TargetAddr string `json:"TargetAddr"`
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
	if !confirmAction("Drain all envoys from this bolt? They will be migrated to other bolts.") {
		fmt.Println("Cancelled.")
		return nil
	}

	client := newAPIClient()
	boltID := args[0]
	toBolt, _ := cmd.Flags().GetString("to")

	var resp struct {
		Moved  int32    `json:"Moved"`
		Failed int32    `json:"Failed"`
		Errors []string `json:"Errors"`
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
