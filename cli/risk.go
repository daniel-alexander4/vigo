// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"git.alexander4.io/vigo/server/risk"
)

var riskCmd = &cobra.Command{
	Use:   "risk [hostname]",
	Short: "Show fleet-wide or per-envoy risk scores",
	Long: `Compute and display risk scores based on CVEs, hardening, rootkits,
integrity, open ports, convergence status, and connectivity.

Without arguments, shows the fleet-wide risk summary with top riskiest hosts.
With a hostname argument, shows the per-envoy risk breakdown with factor details.

Risk levels: low (0-19), medium (20-39), high (40-69), critical (70-100).

Examples:
  vigocli security risk
  vigocli security risk web01.example.com
  vigocli security risk --json
  vigocli security risk --history
  vigocli security risk --history --days 90
  vigocli security risk --export json
  vigocli security risk --export html --output report.html`,
	RunE: runRisk,
}

var (
	riskJSON    bool
	riskHistory bool
	riskDays    int
	riskExport  string
	riskOutput  string
)

func init() {
	riskCmd.Flags().BoolVar(&riskJSON, "json", false, "Output raw JSON")
	riskCmd.Flags().BoolVar(&riskHistory, "history", false, "Show risk trend over time")
	riskCmd.Flags().IntVar(&riskDays, "days", 30, "Number of days for --history")
	riskCmd.Flags().StringVar(&riskExport, "export", "", "Export insurance report (json or html)")
	riskCmd.Flags().StringVarP(&riskOutput, "output", "o", "", "Output file (default: stdout)")
	securityCmd.AddCommand(riskCmd)
}

func runRisk(cmd *cobra.Command, args []string) error {
	if riskExport != "" {
		return runRiskExport()
	}
	if riskHistory {
		return runRiskHistory()
	}
	if len(args) > 0 {
		return runEnvoyRisk(args[0])
	}
	return runFleetRisk()
}

func runFleetRisk() error {
	client := newAPIClient()

	var resp struct {
		TotalEnvoys  int     `json:"total_envoys"`
		ScoredEnvoys int     `json:"scored_envoys"`
		AvgScore     float64 `json:"avg_score"`
		MaxScore     int     `json:"max_score"`
		Distribution map[string]int `json:"distribution"`
		TopRisks     []struct {
			EnvoyID  string `json:"envoy_id"`
			Hostname string `json:"hostname"`
			Score    int    `json:"score"`
			Level    string `json:"level"`
		} `json:"top_risks"`
	}

	if err := client.get("/api/v1/risk", &resp); err != nil {
		return fmt.Errorf("fetching risk data: %w", err)
	}

	if riskJSON {
		raw, _ := json.MarshalIndent(resp, "", "  ")
		fmt.Println(string(raw))
		return nil
	}

	fmt.Println(bold("Fleet Risk Summary"))
	fmt.Println()
	fmt.Printf("  Scored Envoys:  %s\n", bold(fmt.Sprintf("%d / %d", resp.ScoredEnvoys, resp.TotalEnvoys)))
	fmt.Printf("  Average Score:  %s\n", colorRiskScore(int(resp.AvgScore)))
	fmt.Printf("  Highest Score:  %s\n", colorRiskScore(resp.MaxScore))
	fmt.Println()

	fmt.Println(bold("Distribution"))
	for _, level := range []string{"critical", "high", "medium", "low"} {
		count := resp.Distribution[level]
		bar := strings.Repeat("█", count)
		fmt.Printf("  %-10s %s %d\n", colorRiskLevel(level), bar, count)
	}
	fmt.Println()

	if len(resp.TopRisks) > 0 {
		fmt.Println(bold("Top Risks"))
		fmt.Printf("  %-30s %6s  %s\n", header("HOSTNAME"), header("SCORE"), header("LEVEL"))
		for _, r := range resp.TopRisks {
			fmt.Printf("  %-30s %6s  %s\n", r.Hostname, colorRiskScore(r.Score), colorRiskLevel(r.Level))
		}
	}

	return nil
}

func runEnvoyRisk(hostname string) error {
	client := newAPIClient()

	// First resolve hostname to envoy ID via envoys list.
	envoys, err := listAllEnvoys(client, false)
	if err != nil {
		return fmt.Errorf("listing envoys: %w", err)
	}

	var envoyID string
	for _, e := range envoys {
		if strings.EqualFold(e.Hostname, hostname) {
			envoyID = e.ID
			break
		}
	}
	if envoyID == "" {
		return fmt.Errorf("envoy %q not found", hostname)
	}

	var resp struct {
		EnvoyID  string `json:"envoy_id"`
		Hostname string `json:"hostname"`
		Score    int    `json:"score"`
		Level    string `json:"level"`
		Factors  []struct {
			Name   string `json:"name"`
			Points int    `json:"points"`
			Detail string `json:"detail"`
		} `json:"factors"`
	}

	if err := client.get("/api/v1/risk/"+envoyID, &resp); err != nil {
		return fmt.Errorf("fetching risk for %s: %w", hostname, err)
	}

	if riskJSON {
		raw, _ := json.MarshalIndent(resp, "", "  ")
		fmt.Println(string(raw))
		return nil
	}

	fmt.Printf("%s  %s  %s\n\n", bold(resp.Hostname), colorRiskScore(resp.Score), colorRiskLevel(resp.Level))

	if len(resp.Factors) > 0 {
		fmt.Printf("  %-28s %6s  %s\n", header("FACTOR"), header("PTS"), header("DETAIL"))
		for _, f := range resp.Factors {
			fmt.Printf("  %-28s %6s  %s\n", f.Name, colorRiskScore(f.Points), f.Detail)
		}
	} else {
		fmt.Println("  No risk factors detected.")
	}

	return nil
}

func runRiskHistory() error {
	client := newAPIClient()

	path := fmt.Sprintf("/api/v1/risk/history?days=%d", riskDays)

	var resp struct {
		History []struct {
			Date         string         `json:"date"`
			AvgScore     int            `json:"avg_score"`
			MaxScore     int            `json:"max_score"`
			Distribution map[string]int `json:"distribution"`
			ScoredEnvoys int            `json:"scored_envoys"`
		} `json:"history"`
	}

	if err := client.get(path, &resp); err != nil {
		return fmt.Errorf("fetching risk history: %w", err)
	}

	if riskJSON {
		raw, _ := json.MarshalIndent(resp, "", "  ")
		fmt.Println(string(raw))
		return nil
	}

	if len(resp.History) == 0 {
		fmt.Println("No risk history available yet.")
		return nil
	}

	fmt.Printf("%s (%d days)\n\n", bold("Risk Trend"), riskDays)
	fmt.Printf("  %-12s %5s  %5s  %4s %4s %4s %5s\n",
		header("DATE"), header("AVG"), header("MAX"),
		header("LOW"), header("MED"), header("HIGH"), header("CRIT"))

	for _, h := range resp.History {
		fmt.Printf("  %-12s %5s  %5s  %4d %4d %4d %5d\n",
			h.Date,
			colorRiskScore(h.AvgScore),
			colorRiskScore(h.MaxScore),
			h.Distribution["low"],
			h.Distribution["medium"],
			h.Distribution["high"],
			h.Distribution["critical"])
	}

	return nil
}

func runRiskExport() error {
	client := newAPIClient()

	var path string
	switch strings.ToLower(riskExport) {
	case "html":
		path = "/api/v1/risk/insurance.html"
	case "json":
		path = "/api/v1/risk/insurance"
	default:
		return fmt.Errorf("unsupported export format %q (use json or html)", riskExport)
	}

	resp, err := client.getRaw(path)
	if err != nil {
		return fmt.Errorf("generating insurance report: %w", err)
	}
	defer resp.Body.Close()

	var w io.Writer = os.Stdout
	if riskOutput != "" {
		f, err := os.OpenFile(riskOutput, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0600)
		if err != nil {
			return fmt.Errorf("creating output file: %w", err)
		}
		defer f.Close()
		w = f
	}

	if _, err := io.Copy(w, resp.Body); err != nil {
		return fmt.Errorf("writing report: %w", err)
	}

	if riskOutput != "" {
		fmt.Fprintf(os.Stderr, "Report saved to %s\n", riskOutput)
	}

	return nil
}

// colorRiskScore formats a score with color based on severity. Score → level
// classification is delegated to risk.Classify; this function owns only the
// per-surface ANSI rendering of the resulting Level.
func colorRiskScore(score int) string {
	s := fmt.Sprintf("%d", score)
	switch risk.Classify(score) {
	case risk.LevelCritical:
		return red(s)
	case risk.LevelHigh:
		return yellow(s)
	case risk.LevelMedium:
		return cyan(s)
	default:
		return green(s)
	}
}

// colorRiskLevel formats a risk level with color. The string vocabulary
// must match risk.Level constants ("critical", "high", "medium", "low").
func colorRiskLevel(level string) string {
	switch risk.Level(level) {
	case risk.LevelCritical:
		return red(level)
	case risk.LevelHigh:
		return yellow(level)
	case risk.LevelMedium:
		return cyan(level)
	default:
		return green(level)
	}
}
