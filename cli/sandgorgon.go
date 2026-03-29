// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"
)

var sandgorgonCmd = &cobra.Command{
	Use:   "sandgorgon",
	Short: "Bare metal lifecycle — commission, decommission, and NIST 800-88 certificates",
}

var sandgorgonStatusCmd = &cobra.Command{
	Use:   "status",
	Short: "Show all managed hardware assets and their lifecycle state",
	RunE:  runSandgorgonStatus,
}

var sandgorgonCommissionCmd = &cobra.Command{
	Use:   "commission",
	Short: "Register a BMC address for bare metal lifecycle management",
	Long: `Register hardware with its BMC (Redfish) address. If hostname matches
an enrolled envoy, the asset is linked automatically.

Example:
  vigogo sandgorgon commission --bmc 10.0.1.101 --hostname web01.example.com
  vigogo sandgorgon commission --bmc 10.0.1.102 --serial SN12345 --tag ASSET-002`,
	RunE: runSandgorgonCommission,
}

var sandgorgonDecommissionCmd = &cobra.Command{
	Use:   "decommission <asset-id-or-hostname>",
	Short: "Decommission hardware: revoke cert, sanitize disks, generate NIST 800-88 certificate",
	Long: `Full decommission workflow:
  1. Set lifecycle state to decommissioning
  2. Connect to BMC via Redfish
  3. Enumerate and sanitize all drives (method selected per media type)
  4. Generate NIST 800-88 Rev. 2 certificate
  5. Mark asset as retired

Example:
  vigogo sandgorgon decommission web01.example.com --technician "Jane Doe"
  vigogo sandgorgon decommission abc123-def456 --method purge`,
	Args: cobra.ExactArgs(1),
	RunE: runSandgorgonDecommission,
}

var sandgorgonCertificateCmd = &cobra.Command{
	Use:   "certificate <certificate-id>",
	Short: "View or export a NIST 800-88 sanitization certificate",
	Args:  cobra.ExactArgs(1),
	RunE:  runSandgorgonCertificate,
}

var sandgorgonCertificatesCmd = &cobra.Command{
	Use:   "certificates",
	Short: "List all NIST 800-88 certificates",
	RunE:  runSandgorgonCertificates,
}

var sandgorgonImportCmd = &cobra.Command{
	Use:   "import --source csv --file <path>",
	Short: "Import hardware assets from CSV, NetBox, or Snipe-IT",
	Long: `Import BMC addresses and asset metadata. CSV format:
  hostname,bmc_address,serial_number,asset_tag,rack_location

Optional columns: bmc_username, bmc_password, model

Hostnames matching enrolled envoys are linked automatically.
Unmatched rows create assets in "available" state.`,
	RunE: runSandgorgonImport,
}

var sandgorgonEventsCmd = &cobra.Command{
	Use:   "events <asset-id-or-hostname>",
	Short: "Show lifecycle events for an asset",
	Args:  cobra.ExactArgs(1),
	RunE:  runSandgorgonEvents,
}

// Flag variables.
var (
	sgBMC        string
	sgHostname   string
	sgSerial     string
	sgModel      string
	sgTag        string
	sgRack       string
	sgBMCUser    string
	sgBMCPass    string
	sgTechnician string
	sgMethod     string
	sgExport     string
	sgSource     string
	sgFile       string
	sgJSON       bool
)

func init() {
	sandgorgonCommissionCmd.Flags().StringVar(&sgBMC, "bmc", "", "BMC/iDRAC/iLO address (required)")
	sandgorgonCommissionCmd.Flags().StringVar(&sgHostname, "hostname", "", "Machine hostname")
	sandgorgonCommissionCmd.Flags().StringVar(&sgSerial, "serial", "", "Serial number")
	sandgorgonCommissionCmd.Flags().StringVar(&sgModel, "model", "", "Hardware model")
	sandgorgonCommissionCmd.Flags().StringVar(&sgTag, "tag", "", "Asset tag")
	sandgorgonCommissionCmd.Flags().StringVar(&sgRack, "rack", "", "Rack location (e.g., DC1/R05/U12)")
	sandgorgonCommissionCmd.Flags().StringVar(&sgBMCUser, "bmc-user", "", "BMC username")
	sandgorgonCommissionCmd.Flags().StringVar(&sgBMCPass, "bmc-pass", "", "BMC password")
	sandgorgonCommissionCmd.MarkFlagRequired("bmc")

	sandgorgonDecommissionCmd.Flags().StringVar(&sgTechnician, "technician", "", "Technician performing the decommission")
	sandgorgonDecommissionCmd.Flags().StringVar(&sgMethod, "method", "auto", "Sanitization method: auto, clear, purge, destroy")

	sandgorgonCertificateCmd.Flags().StringVar(&sgExport, "export", "", "Export format: json or html")
	sandgorgonCertificateCmd.Flags().BoolVar(&sgJSON, "json", false, "Output raw JSON")

	sandgorgonImportCmd.Flags().StringVar(&sgSource, "source", "csv", "Import source: csv")
	sandgorgonImportCmd.Flags().StringVar(&sgFile, "file", "", "Path to CSV file")
	sandgorgonImportCmd.MarkFlagRequired("file")

	sandgorgonCmd.AddCommand(sandgorgonStatusCmd)
	sandgorgonCmd.AddCommand(sandgorgonCommissionCmd)
	sandgorgonCmd.AddCommand(sandgorgonDecommissionCmd)
	sandgorgonCmd.AddCommand(sandgorgonCertificateCmd)
	sandgorgonCmd.AddCommand(sandgorgonCertificatesCmd)
	sandgorgonCmd.AddCommand(sandgorgonImportCmd)
	sandgorgonCmd.AddCommand(sandgorgonEventsCmd)
}

func runSandgorgonStatus(cmd *cobra.Command, args []string) error {
	client := newAPIClient()
	var assets []struct {
		ID             string `json:"id"`
		Hostname       string `json:"hostname"`
		BMCAddress     string `json:"bmc_address"`
		SerialNumber   string `json:"serial_number"`
		LifecycleState string `json:"lifecycle_state"`
		RackLocation   string `json:"rack_location"`
	}
	if err := client.get("/api/v1/sandgorgon/assets", &assets); err != nil {
		return fmt.Errorf("listing assets: %w", err)
	}

	if len(assets) == 0 {
		fmt.Println("No managed hardware assets.")
		fmt.Println("Use 'vigogo sandgorgon commission' or 'vigogo sandgorgon import' to add assets.")
		return nil
	}

	fmt.Printf("  %s  %-25s %-16s %-14s %-15s %s\n",
		header("STATE"), header("HOSTNAME"), header("BMC"), header("SERIAL"), header("RACK"), header("ID"))
	for _, a := range assets {
		state := colorLifecycleState(a.LifecycleState)
		fmt.Printf("  %-25s %-25s %-16s %-14s %-15s %s\n",
			state, a.Hostname, a.BMCAddress, a.SerialNumber, a.RackLocation, dim(a.ID[:8]))
	}
	fmt.Printf("\n  %s assets\n", bold(fmt.Sprintf("%d", len(assets))))
	return nil
}

func runSandgorgonCommission(cmd *cobra.Command, args []string) error {
	client := newAPIClient()
	body := map[string]string{
		"bmc_address":   sgBMC,
		"hostname":      sgHostname,
		"serial_number": sgSerial,
		"model":         sgModel,
		"asset_tag":     sgTag,
		"rack_location": sgRack,
		"bmc_username":  sgBMCUser,
		"bmc_password":  sgBMCPass,
	}
	var resp struct {
		ID             string `json:"id"`
		Hostname       string `json:"hostname"`
		LifecycleState string `json:"lifecycle_state"`
	}
	if err := client.post("/api/v1/sandgorgon/commission", body, &resp); err != nil {
		return fmt.Errorf("commissioning: %w", err)
	}

	fmt.Printf("  %s asset commissioned\n", green("OK"))
	fmt.Printf("  ID:       %s\n", resp.ID)
	fmt.Printf("  Hostname: %s\n", resp.Hostname)
	fmt.Printf("  State:    %s\n", colorLifecycleState(resp.LifecycleState))
	return nil
}

func runSandgorgonDecommission(cmd *cobra.Command, args []string) error {
	target := args[0]

	// Resolve asset ID — could be hostname or ID.
	client := newAPIClient()
	assetID := target
	if !strings.Contains(target, "-") {
		// Looks like a hostname, not a UUID.
		var assets []struct {
			ID       string `json:"id"`
			Hostname string `json:"hostname"`
		}
		if err := client.get("/api/v1/sandgorgon/assets", &assets); err != nil {
			return err
		}
		for _, a := range assets {
			if a.Hostname == target {
				assetID = a.ID
				break
			}
		}
	}

	body := map[string]string{
		"technician": sgTechnician,
		"method":     sgMethod,
	}
	var resp struct {
		AssetID       string `json:"asset_id"`
		Hostname      string `json:"hostname"`
		CertificateID string `json:"certificate_id"`
		Error         string `json:"error"`
	}
	if err := client.post("/api/v1/sandgorgon/decommission/"+assetID, body, &resp); err != nil {
		return fmt.Errorf("decommission failed: %w", err)
	}

	if resp.Error != "" {
		fmt.Printf("  %s %s\n", red("ERROR"), resp.Error)
		return nil
	}

	fmt.Printf("  %s decommission complete\n", green("OK"))
	fmt.Printf("  Hostname:       %s\n", resp.Hostname)
	fmt.Printf("  Certificate ID: %s\n", bold(resp.CertificateID))
	fmt.Println()
	fmt.Printf("  View: vigogo sandgorgon certificate %s\n", resp.CertificateID)
	return nil
}

func runSandgorgonCertificate(cmd *cobra.Command, args []string) error {
	client := newAPIClient()
	certID := args[0]

	if sgExport == "html" || sgExport == "HTML" {
		resp, err := client.getRaw("/api/v1/sandgorgon/certificates/" + certID + ".html")
		if err != nil {
			return err
		}
		defer resp.Body.Close()
		body, _ := io.ReadAll(resp.Body)
		fmt.Print(string(body))
		return nil
	}

	var cert struct {
		CertificateID           string `json:"certificate_id"`
		Hostname                string `json:"hostname"`
		SerialNumber            string `json:"serial_number"`
		Model                   string `json:"model"`
		AssetTag                string `json:"asset_tag"`
		SanitizationMethod      string `json:"sanitization_method"`
		MediaType               string `json:"media_type"`
		VerificationMethod      string `json:"verification_method"`
		VerificationResult      string `json:"verification_result"`
		VerificationLimitations string `json:"verification_limitations"`
		Technician              string `json:"technician"`
		StartedAt               string `json:"started_at"`
		CompletedAt             string `json:"completed_at"`
		IssuedAt                string `json:"issued_at"`
		Standard                string `json:"standard"`
	}
	if err := client.get("/api/v1/sandgorgon/certificates/"+certID, &cert); err != nil {
		return fmt.Errorf("certificate not found: %w", err)
	}

	if sgJSON || sgExport == "json" {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(cert)
	}

	// Pretty-print.
	fmt.Printf("\n  %s\n", bold("NIST SP 800-88 Rev. 2 — Media Sanitization Certificate"))
	fmt.Println()
	fmt.Printf("  %s\n", header("Asset Identification"))
	fmt.Printf("    Hostname:       %s\n", cert.Hostname)
	fmt.Printf("    Serial Number:  %s\n", cert.SerialNumber)
	fmt.Printf("    Model:          %s\n", cert.Model)
	fmt.Printf("    Asset Tag:      %s\n", cert.AssetTag)
	fmt.Printf("    Media Type:     %s\n", cert.MediaType)
	fmt.Println()
	fmt.Printf("  %s\n", header("Sanitization"))
	fmt.Printf("    Method:         %s\n", cert.SanitizationMethod)
	fmt.Printf("    Started:        %s\n", cert.StartedAt)
	fmt.Printf("    Completed:      %s\n", cert.CompletedAt)
	fmt.Printf("    Technician:     %s\n", cert.Technician)
	fmt.Println()
	fmt.Printf("  %s\n", header("Verification"))
	fmt.Printf("    Method:         %s\n", cert.VerificationMethod)
	fmt.Printf("    Result:         %s\n", colorVerificationResult(cert.VerificationResult))
	if cert.VerificationLimitations != "" {
		fmt.Printf("    Limitations:    %s\n", yellow(cert.VerificationLimitations))
	}
	fmt.Println()
	fmt.Printf("  Certificate ID: %s\n", dim(cert.CertificateID))
	fmt.Printf("  Issued: %s\n", cert.IssuedAt)
	return nil
}

func runSandgorgonCertificates(cmd *cobra.Command, args []string) error {
	client := newAPIClient()
	var certs []struct {
		ID                 string `json:"id"`
		Hostname           string `json:"hostname"`
		SanitizationMethod string `json:"sanitization_method"`
		VerificationResult string `json:"verification_result"`
		CreatedAt          string `json:"created_at"`
	}
	if err := client.get("/api/v1/sandgorgon/certificates", &certs); err != nil {
		return err
	}
	if len(certs) == 0 {
		fmt.Println("No certificates.")
		return nil
	}

	fmt.Printf("  %-25s %-12s %-14s %-20s %s\n",
		header("HOSTNAME"), header("METHOD"), header("RESULT"), header("ISSUED"), header("ID"))
	for _, c := range certs {
		fmt.Printf("  %-25s %-12s %-14s %-20s %s\n",
			c.Hostname, c.SanitizationMethod,
			colorVerificationResult(c.VerificationResult),
			c.CreatedAt[:19], dim(c.ID[:8]))
	}
	return nil
}

func runSandgorgonImport(cmd *cobra.Command, args []string) error {
	if sgSource != "csv" {
		return fmt.Errorf("only 'csv' source is supported (got %q)", sgSource)
	}

	// Upload the file via multipart POST.
	f, err := os.Open(sgFile)
	if err != nil {
		return fmt.Errorf("opening file: %w", err)
	}
	defer f.Close()

	var buf bytes.Buffer
	writer := multipart.NewWriter(&buf)
	part, err := writer.CreateFormFile("file", filepath.Base(sgFile))
	if err != nil {
		return err
	}
	if _, err := io.Copy(part, f); err != nil {
		return err
	}
	writer.Close()

	client := newAPIClient()
	httpReq, err := http.NewRequest("POST", client.baseURL+"/api/v1/sandgorgon/import", &buf)
	if err != nil {
		return err
	}
	httpReq.Header.Set("Content-Type", writer.FormDataContentType())
	client.setAuthHeader(httpReq)

	resp, err := client.httpClient.Do(httpReq)
	if err != nil {
		return fmt.Errorf("upload: %w", err)
	}
	defer resp.Body.Close()

	var result struct {
		Created int      `json:"created"`
		Updated int      `json:"updated"`
		Skipped int      `json:"skipped"`
		Errors  []string `json:"errors"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return fmt.Errorf("decoding response: %w", err)
	}

	fmt.Printf("  %s import complete\n", green("OK"))
	fmt.Printf("  Created: %d\n", result.Created)
	fmt.Printf("  Skipped: %d (already exist)\n", result.Skipped)
	if len(result.Errors) > 0 {
		fmt.Printf("  Errors:  %d\n", len(result.Errors))
		for _, e := range result.Errors {
			fmt.Printf("    %s %s\n", red("!"), e)
		}
	}
	return nil
}

func runSandgorgonEvents(cmd *cobra.Command, args []string) error {
	target := args[0]
	client := newAPIClient()

	// Resolve asset ID from hostname if needed.
	assetID := target
	if !strings.Contains(target, "-") {
		var assets []struct {
			ID       string `json:"id"`
			Hostname string `json:"hostname"`
		}
		if err := client.get("/api/v1/sandgorgon/assets", &assets); err != nil {
			return err
		}
		for _, a := range assets {
			if a.Hostname == target {
				assetID = a.ID
				break
			}
		}
	}

	var events []struct {
		EventType string `json:"event_type"`
		Detail    string `json:"detail"`
		CreatedAt string `json:"created_at"`
	}
	if err := client.get("/api/v1/sandgorgon/events/"+assetID, &events); err != nil {
		return err
	}

	if len(events) == 0 {
		fmt.Println("No events.")
		return nil
	}

	for _, e := range events {
		fmt.Printf("  %-20s %-30s %s\n", e.CreatedAt[:19], bold(e.EventType), e.Detail)
	}
	return nil
}

func colorLifecycleState(state string) string {
	switch state {
	case "available":
		return cyan(state)
	case "commissioned", "active":
		return green(state)
	case "decommissioning":
		return yellow(state)
	case "retired":
		return dim(state)
	default:
		return state
	}
}

func colorVerificationResult(result string) string {
	switch result {
	case "verified":
		return green(result)
	case "unverifiable":
		return yellow(result)
	case "failed":
		return red(result)
	default:
		return result
	}
}
