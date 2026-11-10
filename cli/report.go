// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: AGPL-3.0-only

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
  vigocli compliance report compliance
  vigocli compliance report compliance --format html --output report.html
  vigocli compliance report compliance --format json --output report.json`,
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
				f, err := os.OpenFile(output, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0600)
				if err != nil {
					return fmt.Errorf("creating output file %s: %w", output, err)
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
			if err := os.WriteFile(output, append(data, '\n'), 0600); err != nil {
				return fmt.Errorf("writing output to %s: %w", output, err)
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
  vigocli compliance report hitrust
  vigocli compliance report hitrust --format html --output hitrust-report.html`,
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
				f, err := os.OpenFile(output, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0600)
				if err != nil {
					return fmt.Errorf("creating output file %s: %w", output, err)
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
			if err := os.WriteFile(output, append(data, '\n'), 0600); err != nil {
				return fmt.Errorf("writing output to %s: %w", output, err)
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
  vigocli compliance report soc2
  vigocli compliance report soc2 --format html --output soc2-report.html`,
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
				f, err := os.OpenFile(output, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0600)
				if err != nil {
					return fmt.Errorf("creating output file %s: %w", output, err)
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
			if err := os.WriteFile(output, append(data, '\n'), 0600); err != nil {
				return fmt.Errorf("writing output to %s: %w", output, err)
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
  vigocli compliance report pci
  vigocli compliance report pci --format html --output pci-report.html`,
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
				f, err := os.OpenFile(output, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0600)
				if err != nil {
					return fmt.Errorf("creating output file %s: %w", output, err)
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
			if err := os.WriteFile(output, append(data, '\n'), 0600); err != nil {
				return fmt.Errorf("writing output to %s: %w", output, err)
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
					f, err := os.OpenFile(output, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0600)
					if err != nil {
						return fmt.Errorf("creating output file %s: %w", output, err)
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
				if err := os.WriteFile(output, append(data, '\n'), 0600); err != nil {
					return fmt.Errorf("writing output to %s: %w", output, err)
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
	reportNISTCmd            = makeReportCmd("nist", "Generate a NIST 800-53 compliance report", "nist")
	reportISO27001Cmd        = makeReportCmd("iso27001", "Generate an ISO 27001 compliance report", "iso27001")
	reportGDPRCmd            = makeReportCmd("gdpr", "Generate a GDPR compliance report", "gdpr")
	reportNERCCIPCmd         = makeReportCmd("nerccip", "Generate a NERC CIP compliance report", "nerccip")
	reportExecutiveCmd       = makeReportCmd("executive", "Generate an executive compliance summary", "executive")
	reportCyberEssentialsCmd = makeReportCmd("cyberessentials", "Generate a Cyber Essentials+ compliance report", "cyberessentials")
	reportIEC62443Cmd        = makeReportCmd("iec62443", "Generate an IEC 62443 industrial cybersecurity report", "iec62443")
	reportSOXCmd             = makeReportCmd("sox", "Generate a SOX IT General Controls report", "sox")
	reportFINRACmd           = makeReportCmd("finra", "Generate a FINRA compliance report", "finra")
	reportMiFID2Cmd          = makeReportCmd("mifid2", "Generate a MiFID II RTS 7 compliance report", "mifid2")
	reportCCSSCmd            = makeReportCmd("ccss", "Generate a CCSS cryptocurrency security report", "ccss")
	reportFDA21CFR11Cmd      = makeReportCmd("fda21cfr11", "Generate a FDA 21 CFR Part 11 compliance report", "fda21cfr11")
	reportGamingUKGCCmd      = makeReportCmd("gamingukgc", "Generate a UK Gambling Commission compliance report", "gamingukgc")
	reportGamingNevadaCmd    = makeReportCmd("gamingnevada", "Generate a Nevada Gaming Control Board compliance report", "gamingnevada")
	reportGamingMGACmd       = makeReportCmd("gamingmga", "Generate a Malta Gaming Authority compliance report", "gamingmga")
	reportCISWindowsCmd      = makeReportCmd("ciswindows", "Generate a CIS Windows Server benchmark report", "ciswindows")
	reportNYDFSCyberCmd      = makeReportCmd("nydfscyber", "Generate a NY DFS Cybersecurity (23 NYCRR 500) report", "nydfscyber")
)

var reportCISCmd = &cobra.Command{
	Use:   "cis",
	Short: "Generate a CIS Benchmark compliance report",
	Long: `Generate a compliance report mapped to CIS Benchmark controls
for Ubuntu, RHEL, and Windows Server.

Examples:
  vigocli compliance report cis
  vigocli compliance report cis --format html --output cis-report.html`,
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
				f, err := os.OpenFile(output, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0600)
				if err != nil {
					return fmt.Errorf("creating output file %s: %w", output, err)
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
			if err := os.WriteFile(output, append(data, '\n'), 0600); err != nil {
				return fmt.Errorf("writing output to %s: %w", output, err)
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
	Long: `Detect nodes with degraded, diverged, or error status and trigger force-push
reconvergence. Use --dry-run to preview without acting.

Examples:
  vigocli compliance remediation run --dry-run
  vigocli compliance remediation run`,
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

var reportAuditBundleCmd = &cobra.Command{
	Use:   "audit-bundle <framework>",
	Short: "Download a self-contained audit bundle (zip) for one framework",
	Long: `Stream a zip archive containing a complete, auditor-ready snapshot for
one compliance framework over a configurable period.

The bundle contains:
  manifest.json    sha256 of every other artifact in the bundle
  report.pdf       compliance report rendered via WeasyPrint
  report.html      same content as text/html (auditor fallback)
  evidence.json    per-control: scope, status, modules, attached docs
  docs/            decrypted copies of every uploaded compliance doc
                   linked to the framework's controls (requires
                   compliance.read; under any other backend or without
                   the permission, the docs/ directory is empty)
  audit-log.csv    audit-chain entries within the period
  convergence.csv  daily fleet convergence percentages within the period

Verify the bundle after extraction:

  unzip bundle.zip -d audit/
  cd audit && jq -r '.artifacts[] | "\(.sha256)  \(.path)"' manifest.json | sha256sum -c

Examples:
  vigocli compliance report audit-bundle hipaa
  vigocli compliance report audit-bundle pci --period 90d --output pci-q1-bundle.zip
  vigocli compliance report audit-bundle nist --period 12mo --output nist-annual.zip`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		slug := args[0]
		period, _ := cmd.Flags().GetString("period")
		output, _ := cmd.Flags().GetString("output")

		path := "/api/v1/report/audit-bundle/" + slug
		if period != "" {
			path += "?period=" + period
		}

		client := newAPIClient()
		resp, err := client.getRaw(path)
		if err != nil {
			return fmt.Errorf("downloading audit bundle: %w", err)
		}
		defer resp.Body.Close()
		if resp.StatusCode/100 != 2 {
			body, _ := io.ReadAll(resp.Body) // best-effort: the status code is the error; the body only adds detail
			return fmt.Errorf("server returned %d: %s", resp.StatusCode, string(body))
		}

		if output == "" {
			output = fmt.Sprintf("vigo-audit-bundle-%s.zip", slug)
		}
		f, err := os.OpenFile(output, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0600)
		if err != nil {
			return fmt.Errorf("creating output file %s: %w", output, err)
		}
		defer f.Close()
		n, err := io.Copy(f, resp.Body)
		if err != nil {
			return fmt.Errorf("writing bundle: %w", err)
		}
		fmt.Fprintf(os.Stderr, "Wrote %d bytes to %s\n", n, output)
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
	reportAuditBundleCmd.Flags().String("period", "365d", "Lookback period for audit-log + convergence history (e.g. 30d, 90d, 12mo, 3y)")
	reportAuditBundleCmd.Flags().String("output", "", "Write zip to file (default: vigo-audit-bundle-<framework>.zip)")
	reportCmd.AddCommand(reportComplianceCmd, reportHITRUSTCmd, reportSOC2Cmd, reportPCICmd, reportCISCmd,
		reportNISTCmd, reportISO27001Cmd, reportGDPRCmd, reportNERCCIPCmd, reportCyberEssentialsCmd,
		reportIEC62443Cmd, reportSOXCmd, reportFINRACmd, reportMiFID2Cmd, reportCCSSCmd, reportFDA21CFR11Cmd,
		reportGamingUKGCCmd, reportGamingNevadaCmd, reportGamingMGACmd, reportCISWindowsCmd,
		reportNYDFSCyberCmd, reportExecutiveCmd, reportAuditBundleCmd)

	remediationRunCmd.Flags().Bool("dry-run", false, "Preview targets without acting")
	remediationCmd.AddCommand(remediationRunCmd, remediationStatusCmd, remediationListCmd)

	complianceCmd.AddCommand(reportCmd, remediationCmd)
}
