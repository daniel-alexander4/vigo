package main

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/hobnob/hobnob/server/license"
	"github.com/spf13/cobra"
)

var cliLicenseCmd = &cobra.Command{
	Use:   "license",
	Short: "Manage the Hobnob license",
}

var licenseInstallFile string

var cliLicenseInstallCmd = &cobra.Command{
	Use:   "install",
	Short: "Install a license file on this server",
	RunE:  runLicenseInstall,
}

var cliLicenseStatusCmd = &cobra.Command{
	Use:   "status",
	Short: "Show license status from the running server",
	RunE:  runLicenseStatus,
}

var cliLicenseInspectCmd = &cobra.Command{
	Use:   "inspect [file]",
	Short: "Decode and display a license file (offline, no server needed)",
	Args:  cobra.MaximumNArgs(1),
	RunE:  runLicenseInspect,
}

func init() {
	cliLicenseInstallCmd.Flags().StringVar(&licenseInstallFile, "file", "", "Path to the .lic file (required)")
	cliLicenseInstallCmd.MarkFlagRequired("file")

	cliLicenseCmd.AddCommand(cliLicenseInstallCmd)
	cliLicenseCmd.AddCommand(cliLicenseStatusCmd)
	cliLicenseCmd.AddCommand(cliLicenseInspectCmd)
}

func runLicenseInstall(cmd *cobra.Command, args []string) error {
	// Validate the license file.
	lic, err := license.LoadFile(licenseInstallFile)
	if err != nil {
		return fmt.Errorf("invalid license file: %w", err)
	}

	if err := lic.Verify(); err != nil {
		return fmt.Errorf("license verification failed: %w", err)
	}

	// Check expiry.
	if exp, err := lic.ExpiresTime(); err == nil && time.Now().After(exp) {
		return fmt.Errorf("license has already expired (%s)", lic.Payload.ExpiresAt)
	}

	// Read the file content and write to the install path.
	data, err := os.ReadFile(licenseInstallFile)
	if err != nil {
		return fmt.Errorf("reading license file: %w", err)
	}

	destDir := license.DefaultLicenseDir
	if err := os.MkdirAll(destDir, 0700); err != nil {
		return fmt.Errorf("creating license directory: %w", err)
	}

	destPath := filepath.Join(destDir, filepath.Base(licenseInstallFile))
	if err := os.WriteFile(destPath, data, 0600); err != nil {
		return fmt.Errorf("writing license file: %w", err)
	}

	fmt.Printf("License installed: %s\n", destPath)
	fmt.Printf("  License ID:  %s\n", lic.Payload.LicenseID)
	fmt.Printf("  Customer:    %s\n", lic.Payload.CustomerName)
	fmt.Printf("  Max thralls: %d\n", lic.Payload.MaxThralls)
	fmt.Printf("  Expires:     %s\n", lic.Payload.ExpiresAt)

	// Send SIGHUP to the running hobd process for live reload.
	if err := signalHobd(); err != nil {
		fmt.Printf("\nNote: could not signal hobd for reload: %v\n", err)
		fmt.Println("Restart the server to apply the new license.")
	} else {
		fmt.Println("\nServer notified — license reloaded.")
	}

	return nil
}

func runLicenseStatus(cmd *cobra.Command, args []string) error {
	client := newAPIClient()

	var status struct {
		Stage          int     `json:"stage"`
		License        *struct {
			CustomerName string `json:"customer_name"`
			CustomerID   string `json:"customer_id"`
			LicenseID    string `json:"license_id"`
			MaxThralls   int    `json:"max_thralls"`
			IssuedAt     string `json:"issued_at"`
			ExpiresAt    string `json:"expires_at"`
			Version      int    `json:"version"`
		} `json:"license"`
		ActiveThralls  int    `json:"active_thralls"`
		MaxThralls     int    `json:"max_thralls"`
		ExpiresAt      string `json:"expires_at"`
		DaysRemaining  int    `json:"days_remaining"`
		GraceStartedAt string `json:"grace_started_at"`
		GraceDaysLeft  int    `json:"grace_days_left"`
		Violation      string `json:"violation"`
	}

	if err := client.get("/api/v1/license", &status); err != nil {
		return err
	}

	stageName := "Unknown"
	switch status.Stage {
	case 0:
		stageName = "Compliant"
	case 1:
		stageName = "Grace Period"
	case 2:
		stageName = "Thrall Block"
	case 3:
		stageName = "Service Stop"
	}

	fmt.Printf("Enforcement Stage: %s\n", stageName)

	if status.License != nil {
		fmt.Printf("\nLicense:\n")
		fmt.Printf("  License ID:  %s\n", status.License.LicenseID)
		fmt.Printf("  Customer:    %s (%s)\n", status.License.CustomerName, status.License.CustomerID)
		fmt.Printf("  Version:     %d\n", status.License.Version)
		fmt.Printf("  Issued:      %s\n", status.License.IssuedAt)
		fmt.Printf("  Expires:     %s\n", status.ExpiresAt)

		if status.DaysRemaining >= 0 {
			fmt.Printf("  Days left:   %d\n", status.DaysRemaining)
		} else {
			fmt.Printf("  Expired:     %d days ago\n", -status.DaysRemaining)
		}
	}

	fmt.Printf("\nThralls: %d / %d\n", status.ActiveThralls, status.MaxThralls)

	if status.Violation != "" {
		fmt.Printf("\nViolation: %s\n", status.Violation)
		if status.GraceStartedAt != "" {
			fmt.Printf("Grace started: %s\n", status.GraceStartedAt)
			fmt.Printf("Days until service stop: %d\n", status.GraceDaysLeft)
		}
	}

	return nil
}

func runLicenseInspect(cmd *cobra.Command, args []string) error {
	var lic *license.License
	var err error
	if len(args) > 0 {
		lic, err = license.LoadFile(args[0])
	} else {
		lic, _, err = license.LoadDir(license.DefaultLicenseDir)
	}
	if err != nil {
		return err
	}

	// Try to verify, but don't fail — this is an offline inspect.
	if verr := lic.Verify(); verr != nil {
		fmt.Println("SIGNATURE: COULD NOT VERIFY (public key may not be configured)")
	} else {
		fmt.Println("SIGNATURE: VALID")
	}

	fmt.Println()
	p := lic.Payload
	fmt.Printf("License ID:    %s\n", p.LicenseID)
	fmt.Printf("Customer:      %s (%s)\n", p.CustomerName, p.CustomerID)
	fmt.Printf("Max thralls:   %d\n", p.MaxThralls)
	fmt.Printf("Issued:        %s\n", p.IssuedAt)
	fmt.Printf("Expires:       %s\n", p.ExpiresAt)
	fmt.Printf("Version:       %d\n", p.Version)

	if exp, err := lic.ExpiresTime(); err == nil {
		days := int(time.Until(exp).Hours() / 24)
		if days < 0 {
			fmt.Printf("Status:        EXPIRED (%d days ago)\n", -days)
		} else {
			fmt.Printf("Status:        Valid (%d days remaining)\n", days)
		}
	}

	// Also print raw JSON.
	fmt.Println("\nRaw payload:")
	raw, _ := json.MarshalIndent(lic.Payload, "  ", "  ")
	fmt.Printf("  %s\n", string(raw))

	return nil
}

// signalHobd finds the hobd process and sends SIGHUP.
func signalHobd() error {
	out, err := exec.Command("pgrep", "-x", "hobd").Output()
	if err != nil {
		return fmt.Errorf("hobd process not found")
	}

	pidStr := strings.TrimSpace(string(out))
	lines := strings.Split(pidStr, "\n")
	if len(lines) == 0 || lines[0] == "" {
		return fmt.Errorf("hobd process not found")
	}

	pid, err := strconv.Atoi(lines[0])
	if err != nil {
		return fmt.Errorf("invalid PID: %s", lines[0])
	}

	proc, err := os.FindProcess(pid)
	if err != nil {
		return fmt.Errorf("finding process %d: %w", pid, err)
	}

	return proc.Signal(syscall.SIGHUP)
}
