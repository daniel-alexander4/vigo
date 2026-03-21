// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"

	"github.com/spf13/cobra"
)

var reportCmd = &cobra.Command{
	Use:   "report",
	Short: "Generate HIPAA compliance reports",
}

var reportComplianceCmd = &cobra.Command{
	Use:   "compliance",
	Short: "Generate a HIPAA compliance report (JSON or HTML)",
	Long: `Generate a compliance report with HIPAA 164.312 control mappings,
fleet status, and audit chain verification.

Examples:
  vigogo report compliance
  vigogo report compliance --format html --output report.html
  vigogo report compliance --format json --output report.json`,
	RunE: func(cmd *cobra.Command, args []string) error {
		format, _ := cmd.Flags().GetString("format")
		output, _ := cmd.Flags().GetString("output")

		client := newAPIClient()

		var path string
		switch format {
		case "html":
			path = "/api/v1/report/compliance.html"
		default:
			path = "/api/v1/report/compliance"
		}

		// For HTML, we need raw response handling.
		if format == "html" {
			resp, err := client.getRaw(path)
			if err != nil {
				return fmt.Errorf("generating report: %w", err)
			}
			defer resp.Body.Close()

			var w io.Writer = os.Stdout
			if output != "" {
				f, err := os.Create(output)
				if err != nil {
					return fmt.Errorf("creating output file: %w", err)
				}
				defer f.Close()
				w = f
			}
			if _, err := io.Copy(w, resp.Body); err != nil {
				return fmt.Errorf("writing report: %w", err)
			}
			if output != "" {
				fmt.Fprintf(os.Stderr, "Report written to %s\n", output)
			}
			return nil
		}

		// JSON format.
		var report map[string]any
		if err := client.get(path, &report); err != nil {
			return fmt.Errorf("generating report: %w", err)
		}

		data, _ := json.MarshalIndent(report, "", "  ")
		if output != "" {
			if err := os.WriteFile(output, append(data, '\n'), 0644); err != nil {
				return fmt.Errorf("writing output: %w", err)
			}
			fmt.Fprintf(os.Stderr, "Report written to %s\n", output)
		} else {
			fmt.Println(string(data))
		}
		return nil
	},
}

var reportHITRUSTCmd = &cobra.Command{
	Use:   "hitrust",
	Short: "Generate a HITRUST CSF compliance report",
	Long: `Generate a compliance report mapped to HITRUST CSF control domains
(01 Access Control, 06 Compliance, 09 Operations, 10 Maintenance, etc.).

Examples:
  vigogo report hitrust
  vigogo report hitrust --format html --output hitrust-report.html`,
	RunE: func(cmd *cobra.Command, args []string) error {
		format, _ := cmd.Flags().GetString("format")
		output, _ := cmd.Flags().GetString("output")

		client := newAPIClient()

		var path string
		switch format {
		case "html":
			path = "/api/v1/report/hitrust.html"
		default:
			path = "/api/v1/report/hitrust"
		}

		if format == "html" {
			resp, err := client.getRaw(path)
			if err != nil {
				return fmt.Errorf("generating report: %w", err)
			}
			defer resp.Body.Close()

			var w io.Writer = os.Stdout
			if output != "" {
				f, err := os.Create(output)
				if err != nil {
					return fmt.Errorf("creating output file: %w", err)
				}
				defer f.Close()
				w = f
			}
			if _, err := io.Copy(w, resp.Body); err != nil {
				return fmt.Errorf("writing report: %w", err)
			}
			if output != "" {
				fmt.Fprintf(os.Stderr, "Report written to %s\n", output)
			}
			return nil
		}

		var report map[string]any
		if err := client.get(path, &report); err != nil {
			return fmt.Errorf("generating report: %w", err)
		}

		data, _ := json.MarshalIndent(report, "", "  ")
		if output != "" {
			if err := os.WriteFile(output, append(data, '\n'), 0644); err != nil {
				return fmt.Errorf("writing output: %w", err)
			}
			fmt.Fprintf(os.Stderr, "Report written to %s\n", output)
		} else {
			fmt.Println(string(data))
		}
		return nil
	},
}

var reportSOC2Cmd = &cobra.Command{
	Use:   "soc2",
	Short: "Generate a SOC 2 Trust Services Criteria compliance report",
	Long: `Generate a compliance report mapped to SOC 2 Trust Services Criteria
(CC1-CC9 Common Criteria, A1 Availability, C1 Confidentiality, PI1 Processing Integrity).

Examples:
  vigogo report soc2
  vigogo report soc2 --format html --output soc2-report.html`,
	RunE: func(cmd *cobra.Command, args []string) error {
		format, _ := cmd.Flags().GetString("format")
		output, _ := cmd.Flags().GetString("output")
		client := newAPIClient()

		var path string
		switch format {
		case "html":
			path = "/api/v1/report/soc2.html"
		default:
			path = "/api/v1/report/soc2"
		}

		if format == "html" {
			resp, err := client.getRaw(path)
			if err != nil {
				return fmt.Errorf("generating report: %w", err)
			}
			defer resp.Body.Close()
			var w io.Writer = os.Stdout
			if output != "" {
				f, err := os.Create(output)
				if err != nil {
					return fmt.Errorf("creating output file: %w", err)
				}
				defer f.Close()
				w = f
			}
			if _, err := io.Copy(w, resp.Body); err != nil {
				return fmt.Errorf("writing report: %w", err)
			}
			if output != "" {
				fmt.Fprintf(os.Stderr, "Report written to %s\n", output)
			}
			return nil
		}

		var report map[string]any
		if err := client.get(path, &report); err != nil {
			return fmt.Errorf("generating report: %w", err)
		}
		data, _ := json.MarshalIndent(report, "", "  ")
		if output != "" {
			if err := os.WriteFile(output, append(data, '\n'), 0644); err != nil {
				return fmt.Errorf("writing output: %w", err)
			}
			fmt.Fprintf(os.Stderr, "Report written to %s\n", output)
		} else {
			fmt.Println(string(data))
		}
		return nil
	},
}

var reportPCICmd = &cobra.Command{
	Use:   "pci",
	Short: "Generate a PCI DSS v4.0 compliance report",
	Long: `Generate a compliance report mapped to PCI DSS v4.0 requirements
(Req 1-12: network security, config hardening, access control, audit trails, etc.).

Examples:
  vigogo report pci
  vigogo report pci --format html --output pci-report.html`,
	RunE: func(cmd *cobra.Command, args []string) error {
		format, _ := cmd.Flags().GetString("format")
		output, _ := cmd.Flags().GetString("output")
		client := newAPIClient()

		var path string
		switch format {
		case "html":
			path = "/api/v1/report/pci.html"
		default:
			path = "/api/v1/report/pci"
		}

		if format == "html" {
			resp, err := client.getRaw(path)
			if err != nil {
				return fmt.Errorf("generating report: %w", err)
			}
			defer resp.Body.Close()
			var w io.Writer = os.Stdout
			if output != "" {
				f, err := os.Create(output)
				if err != nil {
					return fmt.Errorf("creating output file: %w", err)
				}
				defer f.Close()
				w = f
			}
			if _, err := io.Copy(w, resp.Body); err != nil {
				return fmt.Errorf("writing report: %w", err)
			}
			if output != "" {
				fmt.Fprintf(os.Stderr, "Report written to %s\n", output)
			}
			return nil
		}

		var report map[string]any
		if err := client.get(path, &report); err != nil {
			return fmt.Errorf("generating report: %w", err)
		}
		data, _ := json.MarshalIndent(report, "", "  ")
		if output != "" {
			if err := os.WriteFile(output, append(data, '\n'), 0644); err != nil {
				return fmt.Errorf("writing output: %w", err)
			}
			fmt.Fprintf(os.Stderr, "Report written to %s\n", output)
		} else {
			fmt.Println(string(data))
		}
		return nil
	},
}

// makeReportCmd creates a report subcommand for a given framework.
func makeReportCmd(use, short, apiPath string) *cobra.Command {
	cmd := &cobra.Command{
		Use:   use,
		Short: short,
		RunE: func(cmd *cobra.Command, args []string) error {
			format, _ := cmd.Flags().GetString("format")
			output, _ := cmd.Flags().GetString("output")
			client := newAPIClient()

			path := "/api/v1/report/" + apiPath
			if format == "html" {
				path += ".html"
			}

			if format == "html" {
				resp, err := client.getRaw(path)
				if err != nil {
					return fmt.Errorf("generating report: %w", err)
				}
				defer resp.Body.Close()
				var w io.Writer = os.Stdout
				if output != "" {
					f, err := os.Create(output)
					if err != nil {
						return fmt.Errorf("creating output file: %w", err)
					}
					defer f.Close()
					w = f
				}
				if _, err := io.Copy(w, resp.Body); err != nil {
					return fmt.Errorf("writing report: %w", err)
				}
				if output != "" {
					fmt.Fprintf(os.Stderr, "Report written to %s\n", output)
				}
				return nil
			}

			var report map[string]any
			if err := client.get(path, &report); err != nil {
				return fmt.Errorf("generating report: %w", err)
			}
			data, _ := json.MarshalIndent(report, "", "  ")
			if output != "" {
				if err := os.WriteFile(output, append(data, '\n'), 0644); err != nil {
					return fmt.Errorf("writing output: %w", err)
				}
				fmt.Fprintf(os.Stderr, "Report written to %s\n", output)
			} else {
				fmt.Println(string(data))
			}
			return nil
		},
	}
	cmd.Flags().String("format", "json", "Output format: json or html")
	cmd.Flags().String("output", "", "Write output to file instead of stdout")
	return cmd
}

var (
	reportNISTCmd      = makeReportCmd("nist", "Generate a NIST 800-53 compliance report", "nist")
	reportISO27001Cmd  = makeReportCmd("iso27001", "Generate an ISO 27001 compliance report", "iso27001")
	reportGDPRCmd      = makeReportCmd("gdpr", "Generate a GDPR compliance report", "gdpr")
	reportNERCCIPCmd   = makeReportCmd("nerccip", "Generate a NERC CIP compliance report", "nerccip")
	reportExecutiveCmd         = makeReportCmd("executive", "Generate an executive compliance summary", "executive")
	reportCyberEssentialsCmd   = makeReportCmd("cyberessentials", "Generate a Cyber Essentials+ compliance report", "cyberessentials")
	reportIEC62443Cmd          = makeReportCmd("iec62443", "Generate an IEC 62443 industrial cybersecurity report", "iec62443")
	reportSOXCmd               = makeReportCmd("sox", "Generate a SOX IT General Controls report", "sox")
	reportFINRACmd             = makeReportCmd("finra", "Generate a FINRA compliance report", "finra")
	reportMiFID2Cmd            = makeReportCmd("mifid2", "Generate a MiFID II RTS 7 compliance report", "mifid2")
	reportCCSSCmd               = makeReportCmd("ccss", "Generate a CCSS cryptocurrency security report", "ccss")
	reportFDA21CFR11Cmd         = makeReportCmd("fda21cfr11", "Generate a FDA 21 CFR Part 11 compliance report", "fda21cfr11")
	reportGamingCmd             = makeReportCmd("gaming", "Generate a gaming commission compliance report", "gaming")
	reportCISWindowsCmd          = makeReportCmd("ciswindows", "Generate a CIS Windows Server benchmark report", "ciswindows")
)

var reportCISCmd = &cobra.Command{
	Use:   "cis",
	Short: "Generate a CIS Benchmark compliance report",
	Long: `Generate a compliance report mapped to CIS Benchmark controls
for Ubuntu, RHEL, and Windows Server.

Examples:
  vigogo report cis
  vigogo report cis --format html --output cis-report.html`,
	RunE: func(cmd *cobra.Command, args []string) error {
		format, _ := cmd.Flags().GetString("format")
		output, _ := cmd.Flags().GetString("output")
		client := newAPIClient()

		var path string
		switch format {
		case "html":
			path = "/api/v1/report/cis.html"
		default:
			path = "/api/v1/report/cis"
		}

		if format == "html" {
			resp, err := client.getRaw(path)
			if err != nil {
				return fmt.Errorf("generating report: %w", err)
			}
			defer resp.Body.Close()
			var w io.Writer = os.Stdout
			if output != "" {
				f, err := os.Create(output)
				if err != nil {
					return fmt.Errorf("creating output file: %w", err)
				}
				defer f.Close()
				w = f
			}
			if _, err := io.Copy(w, resp.Body); err != nil {
				return fmt.Errorf("writing report: %w", err)
			}
			if output != "" {
				fmt.Fprintf(os.Stderr, "Report written to %s\n", output)
			}
			return nil
		}

		var report map[string]any
		if err := client.get(path, &report); err != nil {
			return fmt.Errorf("generating report: %w", err)
		}
		data, _ := json.MarshalIndent(report, "", "  ")
		if output != "" {
			if err := os.WriteFile(output, append(data, '\n'), 0644); err != nil {
				return fmt.Errorf("writing output: %w", err)
			}
			fmt.Fprintf(os.Stderr, "Report written to %s\n", output)
		} else {
			fmt.Println(string(data))
		}
		return nil
	},
}

var remediationCmd = &cobra.Command{
	Use:   "remediation",
	Short: "Manage automated drift remediation",
}

var remediationRunCmd = &cobra.Command{
	Use:   "run",
	Short: "Trigger remediation for all non-compliant nodes",
	Long: `Detect nodes with recurring drift or errors and trigger force-push
reconvergence. Use --dry-run to preview without acting.

Examples:
  vigogo remediation run --dry-run
  vigogo remediation run`,
	RunE: func(cmd *cobra.Command, args []string) error {
		dryRun, _ := cmd.Flags().GetBool("dry-run")
		client := newAPIClient()

		var result map[string]any
		if err := client.post("/api/v1/remediation/run", map[string]bool{"dry_run": dryRun}, &result); err != nil {
			return fmt.Errorf("running remediation: %w", err)
		}

		data, _ := json.MarshalIndent(result, "", "  ")
		fmt.Println(string(data))
		return nil
	},
}

var remediationStatusCmd = &cobra.Command{
	Use:   "status",
	Short: "Show current fleet remediation posture",
	RunE: func(cmd *cobra.Command, args []string) error {
		client := newAPIClient()
		var result map[string]any
		if err := client.get("/api/v1/remediation/status", &result); err != nil {
			return err
		}
		data, _ := json.MarshalIndent(result, "", "  ")
		fmt.Println(string(data))
		return nil
	},
}

var remediationListCmd = &cobra.Command{
	Use:   "list",
	Short: "List recent remediation runs",
	RunE: func(cmd *cobra.Command, args []string) error {
		client := newAPIClient()
		var result map[string]any
		if err := client.get("/api/v1/remediation/runs", &result); err != nil {
			return err
		}
		data, _ := json.MarshalIndent(result, "", "  ")
		fmt.Println(string(data))
		return nil
	},
}

func init() {
	reportComplianceCmd.Flags().String("format", "json", "Output format: json or html")
	reportComplianceCmd.Flags().String("output", "", "Write output to file instead of stdout")
	reportHITRUSTCmd.Flags().String("format", "json", "Output format: json or html")
	reportHITRUSTCmd.Flags().String("output", "", "Write output to file instead of stdout")
	reportSOC2Cmd.Flags().String("format", "json", "Output format: json or html")
	reportSOC2Cmd.Flags().String("output", "", "Write output to file instead of stdout")
	reportPCICmd.Flags().String("format", "json", "Output format: json or html")
	reportPCICmd.Flags().String("output", "", "Write output to file instead of stdout")
	reportCISCmd.Flags().String("format", "json", "Output format: json or html")
	reportCISCmd.Flags().String("output", "", "Write output to file instead of stdout")
	reportCmd.AddCommand(reportComplianceCmd, reportHITRUSTCmd, reportSOC2Cmd, reportPCICmd, reportCISCmd,
		reportNISTCmd, reportISO27001Cmd, reportGDPRCmd, reportNERCCIPCmd, reportCyberEssentialsCmd,
		reportIEC62443Cmd, reportSOXCmd, reportFINRACmd, reportMiFID2Cmd, reportCCSSCmd, reportFDA21CFR11Cmd, reportGamingCmd, reportCISWindowsCmd, reportExecutiveCmd)

	remediationRunCmd.Flags().Bool("dry-run", false, "Preview targets without acting")
	remediationCmd.AddCommand(remediationRunCmd, remediationStatusCmd, remediationListCmd)
}
