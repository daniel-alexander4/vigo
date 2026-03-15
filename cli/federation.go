// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

package main

import (
	"fmt"
	"os"
	"strings"
	"text/tabwriter"

	"github.com/spf13/cobra"
)

var federationCmd = &cobra.Command{
	Use:   "federation",
	Short: "Manage hub-spoke federation topology, spoke health, and thrall routing",
}

var federationStatusCmd = &cobra.Command{
	Use:   "status",
	Short: "Show this server's federation role (standalone/hub/spoke) and spoke availability",
	RunE:  runFederationStatus,
}

var federationSpokesCmd = &cobra.Command{
	Use:   "spokes",
	Short: "List all registered spokes with status, latency, and thrall counts",
	RunE:  runFederationSpokes,
}

var federationSpokeDetailCmd = &cobra.Command{
	Use:   "spoke <spoke-id>",
	Short: "Show a spoke's compliance breakdown, thrall count, and recent activity",
	Args:  cobra.ExactArgs(1),
	RunE:  runFederationSpokeDetail,
}

var federationSpokeThrallsCmd = &cobra.Command{
	Use:   "spoke-thralls <spoke-id>",
	Short: "List all thralls managed by a specific spoke server",
	Args:  cobra.ExactArgs(1),
	RunE:  runFederationSpokeThralls,
}

var federationSpokeRunsCmd = &cobra.Command{
	Use:   "spoke-runs <spoke-id>",
	Short: "List recent convergence runs on a specific spoke server",
	Args:  cobra.ExactArgs(1),
	RunE:  runFederationSpokeRuns,
}

var federationSpokePushCmd = &cobra.Command{
	Use:   "spoke-push <spoke-id>",
	Short: "Trigger immediate check-in for all thralls on a specific spoke",
	Args:  cobra.ExactArgs(1),
	RunE:  runFederationSpokePush,
}

var federationReassignCmd = &cobra.Command{
	Use:   "reassign <thrall-id>",
	Short: "Move a thrall to a different spoke server without re-enrollment",
	Args:  cobra.ExactArgs(1),
	RunE:  runFederationReassign,
}

var federationDrainCmd = &cobra.Command{
	Use:   "drain <spoke-id>",
	Short: "Migrate all thralls off a spoke to prepare it for maintenance or decommission",
	Args:  cobra.ExactArgs(1),
	RunE:  runFederationDrain,
}

func init() {
	federationSpokePushCmd.Flags().String("thrall-id", "", "Target a specific thrall by ID")

	federationReassignCmd.Flags().String("from", "", "Source spoke ID")
	federationReassignCmd.Flags().String("to", "", "Target spoke ID")
	federationReassignCmd.MarkFlagRequired("from")
	federationReassignCmd.MarkFlagRequired("to")

	federationDrainCmd.Flags().String("to", "", "Target spoke ID (optional, auto-route by pattern if empty)")

	federationCmd.AddCommand(federationStatusCmd)
	federationCmd.AddCommand(federationSpokesCmd)
	federationCmd.AddCommand(federationSpokeDetailCmd)
	federationCmd.AddCommand(federationSpokeThrallsCmd)
	federationCmd.AddCommand(federationSpokeRunsCmd)
	federationCmd.AddCommand(federationSpokePushCmd)
	federationCmd.AddCommand(federationReassignCmd)
	federationCmd.AddCommand(federationDrainCmd)
}

func runFederationStatus(cmd *cobra.Command, args []string) error {
	client := newAPIClient()

	var resp struct {
		Role    string `json:"role"`
		SpokeID string `json:"spoke_id"`
		Spokes  []struct {
			ID            string   `json:"id"`
			Addr          string   `json:"addr"`
			Patterns      []string `json:"patterns"`
			Healthy       bool     `json:"healthy"`
			ThrallCount   int32    `json:"thrall_count"`
			Compliant     int32    `json:"compliant"`
			Drifted       int32    `json:"drifted"`
			Failing       int32    `json:"failing"`
			Stale         int32    `json:"stale"`
			Unknown       int32    `json:"unknown"`
			CompliancePct float32  `json:"compliance_pct"`
			LatencyMs     int64    `json:"latency_ms"`
			Error         string   `json:"error"`
		} `json:"spokes"`
	}

	if err := client.get("/api/v1/federation/status", &resp); err != nil {
		return fmt.Errorf("cannot reach server: %w", err)
	}

	fmt.Printf("  Role: %s%s%s\n", colorBold, resp.Role, colorReset)

	if resp.SpokeID != "" {
		fmt.Printf("  Spoke ID: %s\n", resp.SpokeID)
	}

	if resp.Role == "standalone" {
		fmt.Println("  No federation configured.")
		return nil
	}

	if len(resp.Spokes) == 0 {
		return nil
	}

	// Count healthy/total
	healthy := 0
	var totalThralls int32
	for _, s := range resp.Spokes {
		if s.Healthy {
			healthy++
		}
		totalThralls += s.ThrallCount
	}

	fmt.Printf("  Spokes: %d (%d healthy)\n", len(resp.Spokes), healthy)
	fmt.Printf("  Total Thralls: %d\n\n", totalThralls)

	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "  ID\tADDRESS\tTHRALLS\tCOMPLIANT\tCOMPLIANCE\tLATENCY\tPATTERNS\tSTATUS")
	for _, s := range resp.Spokes {
		status := colorGreen + "healthy" + colorReset
		if !s.Healthy {
			status = colorRed + "unreachable" + colorReset
		}
		patterns := strings.Join(s.Patterns, ", ")
		compliant := fmt.Sprintf("%d/%d", s.Compliant, s.ThrallCount)
		compPct := fmt.Sprintf("%.1f%%", s.CompliancePct)
		latency := fmt.Sprintf("%dms", s.LatencyMs)
		fmt.Fprintf(w, "  %s\t%s\t%d\t%s\t%s\t%s\t%s\t%s\n", s.ID, s.Addr, s.ThrallCount, compliant, compPct, latency, patterns, status)
	}
	w.Flush()

	return nil
}

func runFederationSpokes(cmd *cobra.Command, args []string) error {
	client := newAPIClient()

	var resp struct {
		Spokes []struct {
			ID            string   `json:"id"`
			Addr          string   `json:"addr"`
			Patterns      []string `json:"patterns"`
			Healthy       bool     `json:"healthy"`
			ThrallCount   int32    `json:"thrall_count"`
			CompliancePct float32  `json:"compliance_pct"`
			LatencyMs     int64    `json:"latency_ms"`
		} `json:"spokes"`
	}

	if err := client.get("/api/v1/federation/status", &resp); err != nil {
		return fmt.Errorf("cannot reach server: %w", err)
	}

	if len(resp.Spokes) == 0 {
		fmt.Println("  No spokes configured.")
		return nil
	}

	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "  ID\tADDRESS\tTHRALLS\tCOMPLIANCE\tLATENCY\tSTATUS")
	for _, s := range resp.Spokes {
		status := colorGreen + "healthy" + colorReset
		if !s.Healthy {
			status = colorRed + "unreachable" + colorReset
		}
		fmt.Fprintf(w, "  %s\t%s\t%d\t%.1f%%\t%dms\t%s\n",
			s.ID, s.Addr, s.ThrallCount, s.CompliancePct, s.LatencyMs, status)
	}
	w.Flush()
	return nil
}

func runFederationSpokeDetail(cmd *cobra.Command, args []string) error {
	client := newAPIClient()
	spokeID := args[0]

	var resp struct {
		ID            string   `json:"id"`
		Addr          string   `json:"addr"`
		Patterns      []string `json:"patterns"`
		Healthy       bool     `json:"healthy"`
		ThrallCount   int32    `json:"thrall_count"`
		Compliant     int32    `json:"compliant"`
		Drifted       int32    `json:"drifted"`
		Failing       int32    `json:"failing"`
		Stale         int32    `json:"stale"`
		Unknown       int32    `json:"unknown"`
		CompliancePct float32  `json:"compliance_pct"`
		LatencyMs     int64    `json:"latency_ms"`
		Error         string   `json:"error"`
	}

	if err := client.get("/api/v1/federation/spokes/"+spokeID, &resp); err != nil {
		return fmt.Errorf("spoke %q not found or unreachable: %w", spokeID, err)
	}

	status := colorGreen + "healthy" + colorReset
	if !resp.Healthy {
		status = colorRed + "unreachable" + colorReset
	}

	fmt.Printf("  Spoke: %s%s%s\n", colorBold, resp.ID, colorReset)
	fmt.Printf("  Address: %s\n", resp.Addr)
	fmt.Printf("  Status: %s\n", status)
	fmt.Printf("  Latency: %dms\n", resp.LatencyMs)
	fmt.Printf("  Patterns: %s\n", strings.Join(resp.Patterns, ", "))
	fmt.Println()
	fmt.Printf("  Thralls: %d\n", resp.ThrallCount)
	fmt.Printf("  Compliance: %.1f%%\n", resp.CompliancePct)
	fmt.Printf("    Compliant: %s%d%s\n", colorGreen, resp.Compliant, colorReset)
	fmt.Printf("    Drifted:   %s%d%s\n", colorYellow, resp.Drifted, colorReset)
	fmt.Printf("    Failing:   %s%d%s\n", colorRed, resp.Failing, colorReset)
	fmt.Printf("    Stale:     %d\n", resp.Stale)
	fmt.Printf("    Unknown:   %d\n", resp.Unknown)

	if resp.Error != "" {
		fmt.Printf("\n  Error: %s%s%s\n", colorRed, resp.Error, colorReset)
	}

	return nil
}

func runFederationSpokeThralls(cmd *cobra.Command, args []string) error {
	client := newAPIClient()
	spokeID := args[0]

	var resp struct {
		Thralls []struct {
			ID        string `json:"id"`
			Hostname  string `json:"hostname"`
			IPAddress string `json:"ip_address"`
			Revoked   bool   `json:"revoked"`
			LastSeen  string `json:"last_seen,omitempty"`
		} `json:"thralls"`
	}

	if err := client.get("/api/v1/federation/spokes/"+spokeID+"/thralls", &resp); err != nil {
		return fmt.Errorf("cannot list thralls for spoke %q: %w", spokeID, err)
	}

	if len(resp.Thralls) == 0 {
		fmt.Printf("  No thralls on spoke %s.\n", spokeID)
		return nil
	}

	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "  ID\tHOSTNAME\tIP ADDRESS\tREVOKED")
	for _, t := range resp.Thralls {
		revoked := ""
		if t.Revoked {
			revoked = colorRed + "yes" + colorReset
		}
		fmt.Fprintf(w, "  %s\t%s\t%s\t%s\n", t.ID, t.Hostname, t.IPAddress, revoked)
	}
	w.Flush()
	return nil
}

func runFederationSpokeRuns(cmd *cobra.Command, args []string) error {
	client := newAPIClient()
	spokeID := args[0]

	var resp struct {
		Runs []struct {
			ID           string `json:"id"`
			ThrallID     string `json:"thrall_id"`
			Status       string `json:"status"`
			ModuleCount  int32  `json:"module_count"`
			ChangedCount int32  `json:"changed_count"`
			FailedCount  int32  `json:"failed_count"`
			StartedAt    string `json:"started_at,omitempty"`
		} `json:"runs"`
	}

	if err := client.get("/api/v1/federation/spokes/"+spokeID+"/runs", &resp); err != nil {
		return fmt.Errorf("cannot list runs for spoke %q: %w", spokeID, err)
	}

	if len(resp.Runs) == 0 {
		fmt.Printf("  No runs on spoke %s.\n", spokeID)
		return nil
	}

	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "  ID\tTHRALL\tSTATUS\tMODULES\tCHANGED\tFAILED")
	for _, r := range resp.Runs {
		fmt.Fprintf(w, "  %s\t%s\t%s\t%d\t%d\t%d\n",
			r.ID, r.ThrallID, r.Status, r.ModuleCount, r.ChangedCount, r.FailedCount)
	}
	w.Flush()
	return nil
}

func runFederationSpokePush(cmd *cobra.Command, args []string) error {
	client := newAPIClient()
	spokeID := args[0]

	thrallID, _ := cmd.Flags().GetString("thrall-id")

	var resp struct {
		ThrallsNotified int32 `json:"thralls_notified"`
	}

	body := map[string]string{}
	if thrallID != "" {
		body["thrall_id"] = thrallID
	}

	if err := client.post("/api/v1/federation/spokes/"+spokeID+"/push", body, &resp); err != nil {
		return fmt.Errorf("cannot push to spoke %q: %w", spokeID, err)
	}

	fmt.Printf("  Force push sent to spoke %s: %d thralls notified.\n", spokeID, resp.ThrallsNotified)
	return nil
}

func runFederationReassign(cmd *cobra.Command, args []string) error {
	if !confirmAction("Reassign this thrall to a different spoke?") {
		fmt.Println("Cancelled.")
		return nil
	}

	client := newAPIClient()
	thrallID := args[0]
	fromSpoke, _ := cmd.Flags().GetString("from")
	toSpoke, _ := cmd.Flags().GetString("to")

	var resp struct {
		NewThrallID string `json:"NewThrallID"`
		TargetAddr  string `json:"TargetAddr"`
	}

	body := map[string]string{
		"thrall_id":     thrallID,
		"from_spoke_id": fromSpoke,
		"to_spoke_id":   toSpoke,
	}

	if err := client.post("/api/v1/federation/reassign", body, &resp); err != nil {
		return fmt.Errorf("reassign failed: %w", err)
	}

	fmt.Printf("  Thrall %s reassigned.\n", thrallID)
	fmt.Printf("  New ID:     %s\n", resp.NewThrallID)
	fmt.Printf("  Target:     %s\n", resp.TargetAddr)
	fmt.Println("  The agent will reconnect on its next check-in.")

	return nil
}

func runFederationDrain(cmd *cobra.Command, args []string) error {
	if !confirmAction("Drain all thralls from this spoke? They will be migrated to other spokes.") {
		fmt.Println("Cancelled.")
		return nil
	}

	client := newAPIClient()
	spokeID := args[0]
	toSpoke, _ := cmd.Flags().GetString("to")

	var resp struct {
		Moved  int32    `json:"Moved"`
		Failed int32    `json:"Failed"`
		Errors []string `json:"Errors"`
	}

	body := map[string]string{}
	if toSpoke != "" {
		body["target_spoke_id"] = toSpoke
	}

	if err := client.post("/api/v1/federation/spokes/"+spokeID+"/drain", body, &resp); err != nil {
		return fmt.Errorf("drain failed: %w", err)
	}

	fmt.Printf("  Spoke %s drained: %d moved, %d failed.\n", spokeID, resp.Moved, resp.Failed)

	for _, e := range resp.Errors {
		fmt.Printf("  %sError:%s %s\n", colorRed, colorReset, e)
	}

	return nil
}

const colorBold = "\033[1m"
