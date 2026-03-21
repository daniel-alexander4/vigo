// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

package main

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/spf13/cobra"
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
  vigogo risk
  vigogo risk web01.example.com
  vigogo risk --json`,
	RunE: runRisk,
}

var riskJSON bool

func init() {
	riskCmd.Flags().BoolVar(&riskJSON, "json", false, "Output raw JSON")
}

func runRisk(cmd *cobra.Command, args []string) error {
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
	var envoys struct {
		Envoys []struct {
			ID       string `json:"id"`
			Hostname string `json:"hostname"`
		} `json:"envoys"`
	}
	if err := client.get("/api/v1/envoys", &envoys); err != nil {
		return fmt.Errorf("listing envoys: %w", err)
	}

	var envoyID string
	for _, e := range envoys.Envoys {
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

// colorRiskScore formats a score with color based on severity.
func colorRiskScore(score int) string {
	s := fmt.Sprintf("%d", score)
	switch {
	case score >= 70:
		return red(s)
	case score >= 40:
		return yellow(s)
	case score >= 20:
		return cyan(s)
	default:
		return green(s)
	}
}

// colorRiskLevel formats a risk level with color.
func colorRiskLevel(level string) string {
	switch level {
	case "critical":
		return red(level)
	case "high":
		return yellow(level)
	case "medium":
		return cyan(level)
	default:
		return green(level)
	}
}
