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
	Use:   "commission [hostname-or-id]",
	Short: "Register hardware for bare metal lifecycle management",
	Long: `Register hardware for lifecycle management. If hostname matches an enrolled
envoy, serial number, model, manufacturer, and MAC are auto-populated from traits.

BMC is optional — home systems (laptops, desktops) without BMC are supported.
Use --provision to commission and start provisioning in one step.

Examples:
  vigocli sandgorgon commission plex                                    # enrolled envoy, traits auto-populated
  vigocli sandgorgon commission plex --provision ubuntu-24.04-amd64     # commission + provision
  vigocli sandgorgon commission --hostname newbox --mac aa:bb:cc:dd:ee:ff  # unregistered machine
  vigocli sandgorgon commission --bmc 10.0.1.101 --hostname web01      # rack server with BMC`,
	Args: cobra.MaximumNArgs(1),
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
  vigocli sandgorgon decommission web01.example.com --technician "Jane Doe"
  vigocli sandgorgon decommission abc123-def456 --method purge`,
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

var sandgorgonReturnCmd = &cobra.Command{
	Use:   "return-to-available <asset-id-or-hostname>",
	Short: "Return a retired asset to the available pool for re-use",
	Args:  cobra.ExactArgs(1),
	RunE:  runSandgorgonReturn,
}

var sandgorgonProvisionCmd = &cobra.Command{
	Use:   "provision <asset-id-or-hostname>",
	Short: "PXE boot an asset with a registered OS image",
	Long: `Set one-time PXE boot via Redfish, power cycle the machine, and track
provisioning state. The OS installs automatically via preseed/kickstart/autoinstall
and enrolls the Vigo agent as the final step.

Example:
  vigocli sandgorgon provision web01.example.com --image ubuntu-24.04-amd64
  vigocli sandgorgon provision web01.example.com --image rhel-9.4-amd64 --token <enroll-token>`,
	Args: cobra.ExactArgs(1),
	RunE: runSandgorgonProvision,
}

var sandgorgonImagesCmd = &cobra.Command{
	Use:   "images",
	Short: "List registered OS images",
	RunE:  runSandgorgonImages,
}

var sandgorgonImagesRegisterCmd = &cobra.Command{
	Use:   "images-register",
	Short: "Register an OS image for provisioning",
	Long: `Register an OS image file for PXE boot provisioning.

For ISO images, kernel and initrd are automatically extracted from the ISO
(searched in casper/, live/, images/pxeboot/, isolinux/, boot/). Override
with --kernel and --initrd if the defaults don't match.

Example:
  vigocli sandgorgon images-register --name ubuntu-2404 --path /srv/vigo/sandgorgon/images/ubuntu-24.04-live-server-amd64.iso --os-family ubuntu --os-version 24.04`,
	RunE: runSandgorgonImagesRegister,
}

var sandgorgonImagesRemoveCmd = &cobra.Command{
	Use:   "images-remove <name>",
	Short: "Remove an image registration (does not delete the file)",
	Args:  cobra.ExactArgs(1),
	RunE:  runSandgorgonImagesRemove,
}

var sandgorgonReportCmd = &cobra.Command{
	Use:   "report <asset-id-or-hostname>",
	Short: "Generate a full lifecycle report for an asset",
	Long: `Produces a complete audit trail: provisioning, operational history
(compliance, risk, convergence from Vigo), decommission, and NIST 800-88
certificates. Export as JSON or print-friendly HTML.

Example:
  vigocli sandgorgon report web01.example.com
  vigocli sandgorgon report web01.example.com --export html > report.html`,
	Args: cobra.ExactArgs(1),
	RunE: runSandgorgonReport,
}

var sandgorgonDHCPConfigCmd = &cobra.Command{
	Use:   "dhcp-config",
	Short: "Generate DHCP server config snippet for PXE boot",
	Long: `Output a DHCP configuration snippet that points PXE clients to the
Sandgorgon iPXE boot endpoint.

Example:
  vigocli sandgorgon dhcp-config --type isc
  vigocli sandgorgon dhcp-config --type kea
  vigocli sandgorgon dhcp-config --type dnsmasq`,
	RunE: runSandgorgonDHCPConfig,
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
	sgMAC        string
	sgProvision  string
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
	sgImageName  string
	sgImagePath  string
	sgOSFamily   string
	sgOSVersion  string
	sgArch       string
	sgImageType  string
	sgKernelPath string
	sgInitrdPath string
	sgBootParams string
	sgPreseedType string
	sgEnrollToken string
	sgDHCPType   string
)

func init() {
	sandgorgonCommissionCmd.Flags().StringVar(&sgBMC, "bmc", "", "BMC/iDRAC/iLO address (optional for home systems)")
	sandgorgonCommissionCmd.Flags().StringVar(&sgMAC, "mac", "", "MAC address (auto-populated from traits if envoy is enrolled)")
	sandgorgonCommissionCmd.Flags().StringVar(&sgProvision, "provision", "", "Also provision with this image name after commissioning")
	sandgorgonCommissionCmd.Flags().StringVar(&sgHostname, "hostname", "", "Machine hostname (use positional arg instead)")
	sandgorgonCommissionCmd.Flags().StringVar(&sgSerial, "serial", "", "Serial number")
	sandgorgonCommissionCmd.Flags().StringVar(&sgModel, "model", "", "Hardware model")
	sandgorgonCommissionCmd.Flags().StringVar(&sgTag, "tag", "", "Asset tag")
	sandgorgonCommissionCmd.Flags().StringVar(&sgRack, "rack", "", "Rack location (e.g., DC1/R05/U12)")
	sandgorgonCommissionCmd.Flags().StringVar(&sgBMCUser, "bmc-user", "", "BMC username")
	sandgorgonCommissionCmd.Flags().StringVar(&sgBMCPass, "bmc-pass", "", "BMC password")

	sandgorgonDecommissionCmd.Flags().StringVar(&sgTechnician, "technician", "", "Technician performing the decommission")
	sandgorgonDecommissionCmd.Flags().StringVar(&sgMethod, "method", "auto", "Sanitization method: auto, clear, purge, destroy")

	sandgorgonCertificateCmd.Flags().StringVar(&sgExport, "export", "", "Export format: json or html")
	sandgorgonCertificateCmd.Flags().BoolVar(&sgJSON, "json", false, "Output raw JSON")

	sandgorgonImportCmd.Flags().StringVar(&sgSource, "source", "csv", "Import source: csv")
	sandgorgonImportCmd.Flags().StringVar(&sgFile, "file", "", "Path to CSV file")
	sandgorgonImportCmd.MarkFlagRequired("file")

	sandgorgonProvisionCmd.Flags().StringVar(&sgImageName, "image", "", "Registered image name (required)")
	sandgorgonProvisionCmd.Flags().StringVar(&sgEnrollToken, "token", "", "Vigo enrollment token")
	sandgorgonProvisionCmd.Flags().StringVar(&sgHostname, "hostname", "", "Override hostname for provisioning")
	sandgorgonProvisionCmd.MarkFlagRequired("image")

	sandgorgonImagesRegisterCmd.Flags().StringVar(&sgImageName, "name", "", "Image name (required)")
	sandgorgonImagesRegisterCmd.Flags().StringVar(&sgImagePath, "path", "", "Path to image file (required)")
	sandgorgonImagesRegisterCmd.Flags().StringVar(&sgOSFamily, "os-family", "", "OS family: debian, ubuntu, rhel, rocky, windows")
	sandgorgonImagesRegisterCmd.Flags().StringVar(&sgOSVersion, "os-version", "", "OS version: 24.04, 9.4, 2022")
	sandgorgonImagesRegisterCmd.Flags().StringVar(&sgArch, "arch", "amd64", "Architecture: amd64, arm64")
	sandgorgonImagesRegisterCmd.Flags().StringVar(&sgImageType, "type", "iso", "Image type: iso, kernel, tarball")
	sandgorgonImagesRegisterCmd.Flags().StringVar(&sgKernelPath, "kernel", "", "Kernel path (for kernel type)")
	sandgorgonImagesRegisterCmd.Flags().StringVar(&sgInitrdPath, "initrd", "", "Initrd path (for kernel type)")
	sandgorgonImagesRegisterCmd.Flags().StringVar(&sgBootParams, "boot-params", "", "Extra kernel boot parameters")
	sandgorgonImagesRegisterCmd.Flags().StringVar(&sgPreseedType, "preseed-type", "", "Preseed type: preseed, autoinstall, kickstart, autounattend")
	sandgorgonImagesRegisterCmd.MarkFlagRequired("name")
	sandgorgonImagesRegisterCmd.MarkFlagRequired("path")

	sandgorgonReportCmd.Flags().StringVar(&sgExport, "export", "", "Export format: json or html")

	sandgorgonDHCPConfigCmd.Flags().StringVar(&sgDHCPType, "type", "isc", "DHCP server type: isc, kea, dnsmasq")

	sandgorgonCmd.AddCommand(sandgorgonStatusCmd)
	sandgorgonCmd.AddCommand(sandgorgonCommissionCmd)
	sandgorgonCmd.AddCommand(sandgorgonDecommissionCmd)
	sandgorgonCmd.AddCommand(sandgorgonProvisionCmd)
	sandgorgonCmd.AddCommand(sandgorgonCertificateCmd)
	sandgorgonCmd.AddCommand(sandgorgonCertificatesCmd)
	sandgorgonCmd.AddCommand(sandgorgonImagesCmd)
	sandgorgonCmd.AddCommand(sandgorgonImagesRegisterCmd)
	sandgorgonCmd.AddCommand(sandgorgonImagesRemoveCmd)
	sandgorgonCmd.AddCommand(sandgorgonImportCmd)
	sandgorgonCmd.AddCommand(sandgorgonReturnCmd)
	sandgorgonCmd.AddCommand(sandgorgonReportCmd)
	sandgorgonCmd.AddCommand(sandgorgonDHCPConfigCmd)
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
		fmt.Println("Use 'vigocli sandgorgon commission' or 'vigocli sandgorgon import' to add assets.")
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
	// Positional arg overrides --hostname flag.
	hostname := sgHostname
	if len(args) > 0 {
		hostname = args[0]
	}
	if hostname == "" && sgBMC == "" {
		return fmt.Errorf("provide a hostname or --bmc address")
	}

	client := newAPIClient()
	body := map[string]string{
		"bmc_address":   sgBMC,
		"mac_address":   sgMAC,
		"hostname":      hostname,
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
		SerialNumber   string `json:"serial_number"`
		Manufacturer   string `json:"manufacturer"`
		Model          string `json:"model"`
		MacAddress     string `json:"mac_address"`
		BmcAddress     string `json:"bmc_address"`
		LifecycleState string `json:"lifecycle_state"`
	}
	if err := client.post("/api/v1/sandgorgon/commission", body, &resp); err != nil {
		return fmt.Errorf("commissioning: %w", err)
	}

	fmt.Printf("Commissioned: %s\n", bold(resp.Hostname))
	if resp.SerialNumber != "" {
		fmt.Printf("  Serial:  %s\n", resp.SerialNumber)
	}
	if resp.MacAddress != "" {
		fmt.Printf("  MAC:     %s\n", resp.MacAddress)
	}
	if resp.Model != "" {
		label := resp.Model
		if resp.Manufacturer != "" {
			label = resp.Manufacturer + " " + resp.Model
		}
		fmt.Printf("  Model:   %s\n", label)
	}
	if resp.BmcAddress != "" {
		fmt.Printf("  BMC:     %s\n", resp.BmcAddress)
	} else {
		fmt.Printf("  BMC:     %s\n", dim("none"))
	}
	fmt.Printf("  State:   %s\n", colorLifecycleState(resp.LifecycleState))

	// Combined commission + provision.
	if sgProvision != "" {
		fmt.Println()
		return runProvisionByID(client, resp.ID, sgProvision)
	}

	return nil
}

// runProvisionByID provisions an already-commissioned asset by ID.
func runProvisionByID(client *apiClient, assetID, imageName string) error {
	provBody := map[string]string{
		"image_name": imageName,
	}
	if sgEnrollToken != "" {
		provBody["enroll_token"] = sgEnrollToken
	}
	var provResp struct {
		Hostname   string `json:"hostname"`
		ImageName  string `json:"image_name"`
		IPXEScript string `json:"ipxe_script"`
		Status     string `json:"status"`
		ManualBoot bool   `json:"manual_boot"`
		Error      string `json:"error"`
	}
	if err := client.post("/api/v1/sandgorgon/provision/"+assetID, provBody, &provResp); err != nil {
		return fmt.Errorf("provisioning: %w", err)
	}
	if provResp.Error != "" {
		return fmt.Errorf("provisioning: %s", provResp.Error)
	}

	fmt.Printf("Provisioning: %s\n", bold(provResp.ImageName))
	if provResp.ManualBoot {
		fmt.Println()
		fmt.Printf("  %s No BMC — boot manually into PXE:\n", yellow("⚠"))
		fmt.Println("    1. Restart the machine")
		fmt.Println("    2. Press F12 at the BIOS splash screen")
		fmt.Println("    3. Select \"Network Boot\" or \"PXE\"")
		fmt.Println("    4. Installation begins automatically")
	} else {
		fmt.Println("  PXE boot set via BMC, power cycling...")
	}
	fmt.Println()
	fmt.Println("  Monitoring for enrollment...")
	return nil
}

func runSandgorgonDecommission(cmd *cobra.Command, args []string) error {
	target := args[0]
	client := newAPIClient()
	assetID := resolveAssetID(client, target)

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
	fmt.Printf("  View: vigocli sandgorgon certificate %s\n", resp.CertificateID)
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
	assetID := resolveAssetID(client, target)

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

func runSandgorgonReturn(cmd *cobra.Command, args []string) error {
	target := args[0]
	client := newAPIClient()

	assetID := resolveAssetID(client, target)

	var resp struct {
		Status         string `json:"status"`
		LifecycleState string `json:"lifecycle_state"`
	}
	if err := client.post("/api/v1/sandgorgon/return-to-available/"+assetID, nil, &resp); err != nil {
		return fmt.Errorf("return to available: %w", err)
	}

	fmt.Printf("  %s asset returned to available pool\n", green("OK"))
	return nil
}

func runSandgorgonProvision(cmd *cobra.Command, args []string) error {
	target := args[0]
	client := newAPIClient()
	assetID := resolveAssetID(client, target)

	body := map[string]string{
		"image_name":   sgImageName,
		"hostname":     sgHostname,
		"enroll_token": sgEnrollToken,
	}
	var resp struct {
		AssetID   string `json:"asset_id"`
		Hostname  string `json:"hostname"`
		ImageName string `json:"image_name"`
		Status    string `json:"status"`
		Error     string `json:"error"`
	}
	if err := client.post("/api/v1/sandgorgon/provision/"+assetID, body, &resp); err != nil {
		return fmt.Errorf("provisioning: %w", err)
	}

	if resp.Error != "" {
		fmt.Printf("  %s %s\n", red("ERROR"), resp.Error)
		return nil
	}

	fmt.Printf("  %s provisioning initiated\n", green("OK"))
	fmt.Printf("  Hostname: %s\n", resp.Hostname)
	fmt.Printf("  Image:    %s\n", resp.ImageName)
	fmt.Printf("  Status:   %s\n", yellow(resp.Status))
	fmt.Println()
	fmt.Println("  The machine is PXE booting. Monitor via: vigocli sandgorgon events", target)
	return nil
}

func runSandgorgonImages(cmd *cobra.Command, args []string) error {
	client := newAPIClient()
	var images []struct {
		ID        string `json:"id"`
		Name      string `json:"name"`
		OsFamily  string `json:"os_family"`
		OsVersion string `json:"os_version"`
		Arch      string `json:"arch"`
		ImageType string `json:"image_type"`
		SizeBytes int64  `json:"size_bytes"`
		Sha256    string `json:"sha256"`
	}
	if err := client.get("/api/v1/sandgorgon/images", &images); err != nil {
		return err
	}

	if len(images) == 0 {
		fmt.Println("No registered images.")
		fmt.Printf("Place images in %s and register with:\n", bold("/srv/vigo/sandgorgon/images/"))
		fmt.Println("  vigocli sandgorgon images-register --name <name> --path <path> --os-family <family> --os-version <version>")
		return nil
	}

	fmt.Printf("  %-30s %-10s %-8s %-6s %-8s %s\n",
		header("NAME"), header("FAMILY"), header("VERSION"), header("ARCH"), header("TYPE"), header("SIZE"))
	for _, img := range images {
		sizeMB := img.SizeBytes / (1024 * 1024)
		fmt.Printf("  %-30s %-10s %-8s %-6s %-8s %d MB\n",
			img.Name, img.OsFamily, img.OsVersion, img.Arch, img.ImageType, sizeMB)
	}
	return nil
}

func runSandgorgonImagesRegister(cmd *cobra.Command, args []string) error {
	client := newAPIClient()
	body := map[string]string{
		"name":         sgImageName,
		"path":         sgImagePath,
		"os_family":    sgOSFamily,
		"os_version":   sgOSVersion,
		"arch":         sgArch,
		"image_type":   sgImageType,
		"kernel_path":  sgKernelPath,
		"initrd_path":  sgInitrdPath,
		"boot_params":  sgBootParams,
		"preseed_type": sgPreseedType,
	}
	var resp struct {
		ID     string `json:"id"`
		Name   string `json:"name"`
		Sha256 string `json:"sha256"`
	}
	if err := client.post("/api/v1/sandgorgon/images", body, &resp); err != nil {
		return fmt.Errorf("registering image: %w", err)
	}

	fmt.Printf("  %s image registered\n", green("OK"))
	fmt.Printf("  Name:   %s\n", resp.Name)
	fmt.Printf("  ID:     %s\n", resp.ID)
	fmt.Printf("  SHA256: %s\n", dim(resp.Sha256))
	return nil
}

func runSandgorgonImagesRemove(cmd *cobra.Command, args []string) error {
	name := args[0]
	client := newAPIClient()

	// Resolve name to ID.
	var images []struct {
		ID   string `json:"id"`
		Name string `json:"name"`
	}
	if err := client.get("/api/v1/sandgorgon/images", &images); err != nil {
		return err
	}
	var imageID string
	for _, img := range images {
		if img.Name == name || img.ID == name {
			imageID = img.ID
			break
		}
	}
	if imageID == "" {
		return fmt.Errorf("image %q not found", name)
	}

	if err := client.delete("/api/v1/sandgorgon/images/"+imageID, nil); err != nil {
		return err
	}
	fmt.Printf("  %s image registration removed (file not deleted)\n", green("OK"))
	return nil
}

func runSandgorgonReport(cmd *cobra.Command, args []string) error {
	target := args[0]
	client := newAPIClient()
	assetID := resolveAssetID(client, target)

	if sgExport == "html" {
		resp, err := client.getRaw("/api/v1/sandgorgon/report/" + assetID + "/html")
		if err != nil {
			return err
		}
		defer resp.Body.Close()
		body, _ := io.ReadAll(resp.Body)
		fmt.Print(string(body))
		return nil
	}

	if sgExport == "json" {
		var report json.RawMessage
		if err := client.get("/api/v1/sandgorgon/report/"+assetID, &report); err != nil {
			return err
		}
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(report)
	}

	// Pretty-print summary.
	var report struct {
		Hostname          string `json:"hostname"`
		SerialNumber      string `json:"serial_number"`
		Manufacturer      string `json:"manufacturer"`
		Model             string `json:"model"`
		LifecycleState    string `json:"lifecycle_state"`
		CommissionedAt    string `json:"commissioned_at"`
		DecommissionedAt  string `json:"decommissioned_at"`
		ConvergenceStatus string `json:"convergence_status"`
		RiskScore         int    `json:"risk_score"`
		RiskLevel         string `json:"risk_level"`
		ComplianceCoverage []struct {
			Framework       string  `json:"framework"`
			CoveragePercent float64 `json:"coverage_percent"`
		} `json:"compliance_coverage"`
		Certificates []struct {
			ID                 string `json:"id"`
			SanitizationMethod string `json:"sanitization_method"`
			VerificationResult string `json:"verification_result"`
		} `json:"certificates"`
		Events []struct {
			Timestamp string `json:"timestamp"`
			Type      string `json:"type"`
			Detail    string `json:"detail"`
		} `json:"events"`
		GeneratedAt string `json:"generated_at"`
	}
	if err := client.get("/api/v1/sandgorgon/report/"+assetID, &report); err != nil {
		return fmt.Errorf("report: %w", err)
	}

	fmt.Printf("\n  %s\n\n", bold("Bare Metal Lifecycle Report — "+report.Hostname))

	fmt.Printf("  %s\n", header("Asset"))
	fmt.Printf("    Hostname:     %s\n", report.Hostname)
	fmt.Printf("    Serial:       %s\n", report.SerialNumber)
	fmt.Printf("    Manufacturer: %s\n", report.Manufacturer)
	fmt.Printf("    Model:        %s\n", report.Model)
	fmt.Printf("    State:        %s\n", colorLifecycleState(report.LifecycleState))
	fmt.Println()

	if report.CommissionedAt != "" || report.DecommissionedAt != "" {
		fmt.Printf("  %s\n", header("Timeline"))
		if report.CommissionedAt != "" {
			fmt.Printf("    Commissioned:   %s\n", report.CommissionedAt)
		}
		if report.DecommissionedAt != "" {
			fmt.Printf("    Decommissioned: %s\n", report.DecommissionedAt)
		}
		fmt.Println()
	}

	if report.ConvergenceStatus != "" {
		fmt.Printf("  %s\n", header("Vigo Operational Data"))
		fmt.Printf("    Convergence: %s\n", colorStatus(report.ConvergenceStatus))
		fmt.Printf("    Risk Score:  %d (%s)\n", report.RiskScore, report.RiskLevel)
		if len(report.ComplianceCoverage) > 0 {
			fmt.Printf("    Compliance:\n")
			for _, c := range report.ComplianceCoverage {
				fmt.Printf("      %-20s %.0f%%\n", c.Framework, c.CoveragePercent)
			}
		}
		fmt.Println()
	}

	if len(report.Certificates) > 0 {
		fmt.Printf("  %s\n", header("Certificates"))
		for _, c := range report.Certificates {
			fmt.Printf("    %s  %s  %s\n", dim(c.ID[:8]), c.SanitizationMethod, colorVerificationResult(c.VerificationResult))
		}
		fmt.Println()
	}

	if len(report.Events) > 0 {
		fmt.Printf("  %s\n", header("Events (latest 10)"))
		limit := len(report.Events)
		if limit > 10 {
			limit = 10
		}
		for _, e := range report.Events[:limit] {
			ts := e.Timestamp
			if len(ts) > 19 {
				ts = ts[:19]
			}
			fmt.Printf("    %s  %-25s  %s\n", dim(ts), bold(e.Type), e.Detail)
		}
	}

	fmt.Printf("\n  Generated: %s\n", dim(report.GeneratedAt))
	fmt.Printf("  Export: vigocli sandgorgon report %s --export html\n", target)
	return nil
}

func runSandgorgonDHCPConfig(cmd *cobra.Command, args []string) error {
	client := newAPIClient()
	resp, err := client.getRaw("/api/v1/sandgorgon/dhcp-config?type=" + sgDHCPType)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	fmt.Print(string(body))
	return nil
}

// resolveAssetID resolves a hostname or UUID to an asset ID.
func resolveAssetID(client *apiClient, target string) string {
	if strings.Contains(target, "-") {
		return target // looks like a UUID
	}
	var assets []struct {
		ID       string `json:"id"`
		Hostname string `json:"hostname"`
	}
	if err := client.get("/api/v1/sandgorgon/assets", &assets); err != nil {
		return target
	}
	for _, a := range assets {
		if a.Hostname == target {
			return a.ID
		}
	}
	return target
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
