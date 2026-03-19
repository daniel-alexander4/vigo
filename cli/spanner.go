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

var spannerCmd = &cobra.Command{
	Use:   "spanner",
	Short: "Manage hub-spoke spanner topology, spoke health, and envoy routing",
}

var spannerStatusCmd = &cobra.Command{
	Use:   "status",
	Short: "Show this server's spanner role (standalone/hub/spoke) and spoke availability",
	RunE:  runSpannerStatus,
}

var spannerSpokesCmd = &cobra.Command{
	Use:   "spokes",
	Short: "List all registered spokes with status, latency, and envoy counts",
	RunE:  runSpannerSpokes,
}

var spannerSpokeDetailCmd = &cobra.Command{
	Use:   "spoke <spoke-id>",
	Short: "Show a spoke's compliance breakdown, envoy count, and recent activity",
	Args:  cobra.ExactArgs(1),
	RunE:  runSpannerSpokeDetail,
}

var spannerSpokeEnvoysCmd = &cobra.Command{
	Use:   "spoke-envoys <spoke-id>",
	Short: "List all envoys managed by a specific spoke server",
	Args:  cobra.ExactArgs(1),
	RunE:  runSpannerSpokeEnvoys,
}

var spannerSpokeRunsCmd = &cobra.Command{
	Use:   "spoke-runs <spoke-id>",
	Short: "List recent convergence runs on a specific spoke server",
	Args:  cobra.ExactArgs(1),
	RunE:  runSpannerSpokeRuns,
}

var spannerSpokePushCmd = &cobra.Command{
	Use:   "spoke-push <spoke-id>",
	Short: "Trigger immediate check-in for all envoys on a specific spoke",
	Args:  cobra.ExactArgs(1),
	RunE:  runSpannerSpokePush,
}

var spannerReassignCmd = &cobra.Command{
	Use:   "reassign <envoy-id>",
	Short: "Move a envoy to a different spoke server without re-enrollment",
	Args:  cobra.ExactArgs(1),
	RunE:  runSpannerReassign,
}

var spannerDrainCmd = &cobra.Command{
	Use:   "drain <spoke-id>",
	Short: "Migrate all envoys off a spoke to prepare it for maintenance or decommission",
	Args:  cobra.ExactArgs(1),
	RunE:  runSpannerDrain,
}

func init() {
	spannerSpokePushCmd.Flags().String("envoy-id", "", "Target a specific envoy by ID")

	spannerReassignCmd.Flags().String("from", "", "Source spoke ID")
	spannerReassignCmd.Flags().String("to", "", "Target spoke ID")
	spannerReassignCmd.MarkFlagRequired("from")
	spannerReassignCmd.MarkFlagRequired("to")

	spannerDrainCmd.Flags().String("to", "", "Target spoke ID (optional, auto-route by pattern if empty)")

	spannerCmd.AddCommand(spannerStatusCmd)
	spannerCmd.AddCommand(spannerSpokesCmd)
	spannerCmd.AddCommand(spannerSpokeDetailCmd)
	spannerCmd.AddCommand(spannerSpokeEnvoysCmd)
	spannerCmd.AddCommand(spannerSpokeRunsCmd)
	spannerCmd.AddCommand(spannerSpokePushCmd)
	spannerCmd.AddCommand(spannerReassignCmd)
	spannerCmd.AddCommand(spannerDrainCmd)
}

func runSpannerStatus(cmd *cobra.Command, args []string) error {
	client := newAPIClient()

	var resp struct {
		Role    string `json:"role"`
		SpokeID string `json:"spoke_id"`
		Spokes  []struct {
			ID             string   `json:"id"`
			Addr           string   `json:"addr"`
			Patterns       []string `json:"patterns"`
			Healthy        bool     `json:"healthy"`
			EnvoyCount    int32    `json:"envoy_count"`
			Compliant      int32    `json:"compliant"`
			RecurringDrift int32    `json:"recurring_drift"`
			Errors         int32    `json:"errors"`
			LostContact    int32    `json:"lost_contact"`
			NoData         int32    `json:"no_data"`
			CompliancePct  float32  `json:"compliance_pct"`
			LatencyMs      int64    `json:"latency_ms"`
			Error          string   `json:"error"`
		} `json:"spokes"`
	}

	if err := client.get("/api/v1/spanner/status", &resp); err != nil {
		return fmt.Errorf("cannot reach server: %w", err)
	}

	fmt.Printf("  Role: %s\n", bold(resp.Role))

	if resp.SpokeID != "" {
		fmt.Printf("  Spoke ID: %s\n", resp.SpokeID)
	}

	if resp.Role == "standalone" {
		fmt.Println("  No spanner configured.")
		return nil
	}

	if len(resp.Spokes) == 0 {
		return nil
	}

	// Count healthy/total
	healthy := 0
	var totalEnvoys int32
	for _, s := range resp.Spokes {
		if s.Healthy {
			healthy++
		}
		totalEnvoys += s.EnvoyCount
	}

	fmt.Printf("  Spokes: %d (%d healthy)\n", len(resp.Spokes), healthy)
	fmt.Printf("  Total Envoys: %d\n\n", totalEnvoys)

	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintf(w, "  %s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\n", header("ID"), header("ADDRESS"), header("ENVOYS"), header("COMPLIANT"), header("COMPLIANCE"), header("LATENCY"), header("PATTERNS"), header("STATUS"))
	for _, s := range resp.Spokes {
		status := colorStatus("healthy")
		if !s.Healthy {
			status = colorStatus("unreachable")
		}
		patterns := strings.Join(s.Patterns, ", ")
		compliant := fmt.Sprintf("%d/%d", s.Compliant, s.EnvoyCount)
		compPct := fmt.Sprintf("%.1f%%", s.CompliancePct)
		latency := fmt.Sprintf("%dms", s.LatencyMs)
		fmt.Fprintf(w, "  %s\t%s\t%d\t%s\t%s\t%s\t%s\t%s\n", s.ID, s.Addr, s.EnvoyCount, compliant, compPct, latency, patterns, status)
	}
	w.Flush()

	return nil
}

func runSpannerSpokes(cmd *cobra.Command, args []string) error {
	client := newAPIClient()

	var resp struct {
		Spokes []struct {
			ID            string   `json:"id"`
			Addr          string   `json:"addr"`
			Patterns      []string `json:"patterns"`
			Healthy       bool     `json:"healthy"`
			EnvoyCount   int32    `json:"envoy_count"`
			CompliancePct float32  `json:"compliance_pct"`
			LatencyMs     int64    `json:"latency_ms"`
		} `json:"spokes"`
	}

	if err := client.get("/api/v1/spanner/status", &resp); err != nil {
		return fmt.Errorf("cannot reach server: %w", err)
	}

	if len(resp.Spokes) == 0 {
		fmt.Println("  No spokes configured.")
		return nil
	}

	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintf(w, "  %s\t%s\t%s\t%s\t%s\t%s\n", header("ID"), header("ADDRESS"), header("ENVOYS"), header("COMPLIANCE"), header("LATENCY"), header("STATUS"))
	for _, s := range resp.Spokes {
		status := colorStatus("healthy")
		if !s.Healthy {
			status = colorStatus("unreachable")
		}
		fmt.Fprintf(w, "  %s\t%s\t%d\t%.1f%%\t%dms\t%s\n",
			s.ID, s.Addr, s.EnvoyCount, s.CompliancePct, s.LatencyMs, status)
	}
	w.Flush()
	return nil
}

func runSpannerSpokeDetail(cmd *cobra.Command, args []string) error {
	client := newAPIClient()
	spokeID := args[0]

	var resp struct {
		ID             string   `json:"id"`
		Addr           string   `json:"addr"`
		Patterns       []string `json:"patterns"`
		Healthy        bool     `json:"healthy"`
		EnvoyCount    int32    `json:"envoy_count"`
		Compliant      int32    `json:"compliant"`
		RecurringDrift int32    `json:"recurring_drift"`
		Errors         int32    `json:"errors"`
		LostContact    int32    `json:"lost_contact"`
		NoData         int32    `json:"no_data"`
		CompliancePct  float32  `json:"compliance_pct"`
		LatencyMs      int64    `json:"latency_ms"`
		Error          string   `json:"error"`
	}

	if err := client.get("/api/v1/spanner/spokes/"+spokeID, &resp); err != nil {
		return fmt.Errorf("spoke %q not found or unreachable: %w", spokeID, err)
	}

	status := colorStatus("healthy")
	if !resp.Healthy {
		status = colorStatus("unreachable")
	}

	fmt.Printf("  Spoke: %s\n", bold(resp.ID))
	fmt.Printf("  Address: %s\n", resp.Addr)
	fmt.Printf("  Status: %s\n", status)
	fmt.Printf("  Latency: %dms\n", resp.LatencyMs)
	fmt.Printf("  Patterns: %s\n", strings.Join(resp.Patterns, ", "))
	fmt.Println()
	fmt.Printf("  Envoys: %d\n", resp.EnvoyCount)
	fmt.Printf("  Compliance: %.1f%%\n", resp.CompliancePct)
	fmt.Printf("    Compliant: %s\n", green(fmt.Sprintf("%d", resp.Compliant)))
	fmt.Printf("    Recurring Drift: %s\n", yellow(fmt.Sprintf("%d", resp.RecurringDrift)))
	fmt.Printf("    Errors:          %s\n", red(fmt.Sprintf("%d", resp.Errors)))
	fmt.Printf("    Lost Contact:    %d\n", resp.LostContact)
	fmt.Printf("    No Data:         %d\n", resp.NoData)

	if resp.Error != "" {
		fmt.Printf("\n  Error: %s\n", red(resp.Error))
	}

	return nil
}

func runSpannerSpokeEnvoys(cmd *cobra.Command, args []string) error {
	client := newAPIClient()
	spokeID := args[0]

	var resp struct {
		Envoys []struct {
			ID        string `json:"id"`
			Hostname  string `json:"hostname"`
			IPAddress string `json:"ip_address"`
			Revoked   bool   `json:"revoked"`
			LastSeen  string `json:"last_seen,omitempty"`
		} `json:"envoys"`
	}

	if err := client.get("/api/v1/spanner/spokes/"+spokeID+"/envoys", &resp); err != nil {
		return fmt.Errorf("cannot list envoys for spoke %q: %w", spokeID, err)
	}

	if len(resp.Envoys) == 0 {
		fmt.Printf("  No envoys on spoke %s.\n", spokeID)
		return nil
	}

	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
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

func runSpannerSpokeRuns(cmd *cobra.Command, args []string) error {
	client := newAPIClient()
	spokeID := args[0]

	var resp struct {
		Runs []struct {
			ID           string `json:"id"`
			EnvoyID     string `json:"envoy_id"`
			Status       string `json:"status"`
			ModuleCount  int32  `json:"module_count"`
			ChangedCount int32  `json:"changed_count"`
			FailedCount  int32  `json:"failed_count"`
			StartedAt    string `json:"started_at,omitempty"`
		} `json:"runs"`
	}

	if err := client.get("/api/v1/spanner/spokes/"+spokeID+"/runs", &resp); err != nil {
		return fmt.Errorf("cannot list runs for spoke %q: %w", spokeID, err)
	}

	if len(resp.Runs) == 0 {
		fmt.Printf("  No runs on spoke %s.\n", spokeID)
		return nil
	}

	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintf(w, "  %s\t%s\t%s\t%s\t%s\t%s\n", header("ID"), header("ENVOY"), header("STATUS"), header("MODULES"), header("CHANGED"), header("FAILED"))
	for _, r := range resp.Runs {
		fmt.Fprintf(w, "  %s\t%s\t%s\t%d\t%d\t%d\n",
			r.ID, r.EnvoyID, r.Status, r.ModuleCount, r.ChangedCount, r.FailedCount)
	}
	w.Flush()
	return nil
}

func runSpannerSpokePush(cmd *cobra.Command, args []string) error {
	client := newAPIClient()
	spokeID := args[0]

	envoyID, _ := cmd.Flags().GetString("envoy-id")

	var resp struct {
		EnvoysNotified int32 `json:"envoys_notified"`
	}

	body := map[string]string{}
	if envoyID != "" {
		body["envoy_id"] = envoyID
	}

	if err := client.post("/api/v1/spanner/spokes/"+spokeID+"/push", body, &resp); err != nil {
		return fmt.Errorf("cannot push to spoke %q: %w", spokeID, err)
	}

	fmt.Printf("  Force push sent to spoke %s: %d envoys notified.\n", spokeID, resp.EnvoysNotified)
	return nil
}

func runSpannerReassign(cmd *cobra.Command, args []string) error {
	if !confirmAction("Reassign this envoy to a different spoke?") {
		fmt.Println("Cancelled.")
		return nil
	}

	client := newAPIClient()
	envoyID := args[0]
	fromSpoke, _ := cmd.Flags().GetString("from")
	toSpoke, _ := cmd.Flags().GetString("to")

	var resp struct {
		NewEnvoyID string `json:"NewEnvoyID"`
		TargetAddr  string `json:"TargetAddr"`
	}

	body := map[string]string{
		"envoy_id":     envoyID,
		"from_spoke_id": fromSpoke,
		"to_spoke_id":   toSpoke,
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

func runSpannerDrain(cmd *cobra.Command, args []string) error {
	if !confirmAction("Drain all envoys from this spoke? They will be migrated to other spokes.") {
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

	if err := client.post("/api/v1/spanner/spokes/"+spokeID+"/drain", body, &resp); err != nil {
		return fmt.Errorf("drain failed: %w", err)
	}

	fmt.Printf("  Spoke %s drained: %d moved, %d failed.\n", spokeID, resp.Moved, resp.Failed)

	for _, e := range resp.Errors {
		fmt.Printf("  %s %s\n", red("Error:"), e)
	}

	return nil
}

