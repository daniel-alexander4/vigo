// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"

	"git.alexander4.io/tessera/license"
	vigolic "git.alexander4.io/tessera/license/products/vigo"
	srvlicense "git.alexander4.io/vigo/server/license"
	"github.com/spf13/cobra"
)

var cliLicenseCmd = &cobra.Command{
	Use:   "license",
	Short: "View, install, and inspect the server license that governs envoy limits",
}

var licenseInstallFile string

var cliLicenseInstallCmd = &cobra.Command{
	Use:   "install",
	Short: "Copy a .lic file to the server's license directory and trigger a reload",
	RunE:  runLicenseInstall,
}

var cliLicenseStatusCmd = &cobra.Command{
	Use:   "status",
	Short: "Show current license tier, envoy usage, enforcement stage, and expiry",
	RunE:  runLicenseStatus,
}

var cliLicenseInspectCmd = &cobra.Command{
	Use:   "inspect [file]",
	Short: "Decode and display a .lic file without contacting the server",
	Args:  cobra.MaximumNArgs(1),
	RunE:  runLicenseInspect,
}

var licenseUpdateFile string

var cliLicenseUpdateCmd = &cobra.Command{
	Use:   "update",
	Short: "Atomically rotate the active .lic (validate → archive old → install new → SIGHUP → verify reload)",
	Long: `Atomic license rotation. Validates the new file (signature, machine
fingerprint, expiry) before touching anything on disk; archives the existing
.lic to /srv/vigo/license/.archive/ with a UTC timestamp suffix; installs the
new file; sends SIGHUP to vigosrv; polls the local REST API to confirm the
enforcer picked up the new license_id.

Refuses if the license directory has more than one .lic file (operator must
consolidate first — the loader picks first-alphabetically on multi-.lic dirs,
which would make rotation ambiguous).`,
	RunE: runLicenseUpdate,
}

var licenseVerifyFile string

var cliLicenseVerifyCmd = &cobra.Command{
	Use:   "verify",
	Short: "Dry-run validate a .lic file (signature + fingerprint + expiry) without installing it",
	RunE:  runLicenseVerify,
}

var cliLicenseRollbackCmd = &cobra.Command{
	Use:   "rollback",
	Short: "Restore the most recently archived .lic and reload",
	Long: `Restores the most recently archived .lic from /srv/vigo/license/.archive/,
validates it, archives the currently active .lic (if any), installs the
restored file, and SIGHUPs vigosrv. Use as the "undo" button if a new license
has a problem detected post-update.`,
	RunE: runLicenseRollback,
}

func init() {
	cliLicenseInstallCmd.Flags().StringVar(&licenseInstallFile, "file", "", "Path to the .lic file (required)")
	cliLicenseInstallCmd.MarkFlagRequired("file")

	cliLicenseFingerprintCmd.Flags().String("ca-file", "/srv/vigo/tls/ca", "Path to TLS CA certificate")

	cliLicenseUpdateCmd.Flags().StringVar(&licenseUpdateFile, "file", "", "Path to the new .lic file (required)")
	cliLicenseUpdateCmd.MarkFlagRequired("file")
	cliLicenseUpdateCmd.Flags().String("ca-file", "/srv/vigo/tls/ca", "Path to TLS CA certificate (for fingerprint check)")

	cliLicenseVerifyCmd.Flags().StringVar(&licenseVerifyFile, "file", "", "Path to the .lic file to validate (required)")
	cliLicenseVerifyCmd.MarkFlagRequired("file")
	cliLicenseVerifyCmd.Flags().String("ca-file", "/srv/vigo/tls/ca", "Path to TLS CA certificate (for fingerprint check)")

	cliLicenseCmd.AddCommand(cliLicenseInstallCmd)
	cliLicenseCmd.AddCommand(cliLicenseStatusCmd)
	cliLicenseCmd.AddCommand(cliLicenseInspectCmd)
	cliLicenseCmd.AddCommand(cliLicenseFingerprintCmd)
	cliLicenseCmd.AddCommand(cliLicenseUpdateCmd)
	cliLicenseCmd.AddCommand(cliLicenseVerifyCmd)
	cliLicenseCmd.AddCommand(cliLicenseRollbackCmd)
}

const licenseArchiveSubdir = ".archive"

var cliLicenseFingerprintCmd = &cobra.Command{
	Use:   "fingerprint",
	Short: "Display this server's machine fingerprint for license binding",
	Long: `Compute and display the machine fingerprint used to bind paid licenses
to this server. Send this fingerprint when requesting a new license.

The fingerprint is SHA256(machine-id + primary-MAC + CA-cert-fingerprint).
It changes on OS reinstall, primary NIC replacement, or CA cert regeneration.

Required for every paid tier (Starter and above). Community-tier (free,
up to 100 nodes) licenses do not require a fingerprint and run on any
machine.`,
	RunE: func(cmd *cobra.Command, args []string) error {
		caFile, _ := cmd.Flags().GetString("ca-file")
		fp, err := vigolic.MachineFingerprint(caFile)
		if err != nil {
			return fmt.Errorf("computing fingerprint: %w", err)
		}
		fmt.Println(fp)
		return nil
	},
}

func runLicenseInstall(cmd *cobra.Command, args []string) error {
	// Validate the license file.
	lic, err := license.LoadFile(licenseInstallFile)
	if err != nil {
		return fmt.Errorf("invalid license file: %w", err)
	}

	p, err := vigolic.Decode(lic)
	if err != nil {
		return fmt.Errorf("license verification failed: %w", err)
	}

	// Check expiry.
	if exp, err := p.ExpiresTime(); err == nil && time.Now().After(exp) {
		return fmt.Errorf("license has already expired (%s)", p.ExpiresAt)
	}

	// Read the file content and write to the install path.
	data, err := os.ReadFile(licenseInstallFile)
	if err != nil {
		return fmt.Errorf("reading license file: %w", err)
	}

	destDir := vigolic.DefaultLicenseDir
	if err := os.MkdirAll(destDir, 0700); err != nil {
		return fmt.Errorf("creating license directory: %w", err)
	}

	destPath := filepath.Join(destDir, filepath.Base(licenseInstallFile))
	if err := os.WriteFile(destPath, data, 0600); err != nil {
		return fmt.Errorf("writing license file: %w", err)
	}

	fmt.Printf("License installed: %s\n", destPath)
	fmt.Printf("  License ID:  %s\n", p.LicenseID)
	fmt.Printf("  Customer:    %s\n", p.CustomerName)
	fmt.Printf("  Tier:        %s\n", p.TierDisplayName())
	fmt.Printf("  Max envoys: %d\n", p.MaxEnvoys)
	fmt.Printf("  Expires:     %s\n", p.ExpiresAt)

	// Send SIGHUP to the running vigosrv process for live reload.
	if err := signalVigosrv(); err != nil {
		fmt.Printf("\nNote: could not signal vigosrv for reload: %v\n", err)
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
			Tier         string `json:"tier"`
			MaxEnvoys   int    `json:"max_envoys"`
			IssuedAt     string `json:"issued_at"`
			ExpiresAt    string `json:"expires_at"`
			Version      int    `json:"version"`
		} `json:"license"`
		ActiveEnvoys  int    `json:"active_envoys"`
		MaxEnvoys     int    `json:"max_envoys"`
		ExpiresAt      string `json:"expires_at"`
		DaysRemaining  int    `json:"days_remaining"`
		GraceStartedAt string `json:"grace_started_at"`
		GraceDaysLeft  int    `json:"grace_days_left"`
		Violation      string `json:"violation"`
	}

	if err := client.get("/api/v1/license", &status); err != nil {
		return err
	}

	fmt.Printf("Enforcement Stage: %s\n", srvlicense.Stage(status.Stage).String())

	if status.License != nil {
		fmt.Printf("\nLicense:\n")
		fmt.Printf("  License ID:  %s\n", status.License.LicenseID)
		fmt.Printf("  Customer:    %s (%s)\n", status.License.CustomerName, status.License.CustomerID)
		fmt.Printf("  Tier:        %s\n", status.License.Tier)
		fmt.Printf("  Version:     %d\n", status.License.Version)
		fmt.Printf("  Issued:      %s\n", status.License.IssuedAt)
		fmt.Printf("  Expires:     %s\n", status.ExpiresAt)

		if status.DaysRemaining >= 0 {
			fmt.Printf("  Days left:   %d\n", status.DaysRemaining)
		} else {
			fmt.Printf("  Expired:     %d days ago\n", -status.DaysRemaining)
		}
	}

	fmt.Printf("\nEnvoys: %d / %d\n", status.ActiveEnvoys, status.MaxEnvoys)

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
	var p *vigolic.Payload
	var err error
	if len(args) > 0 {
		lic, err = license.LoadFile(args[0])
		if err == nil {
			p, err = vigolic.Decode(lic)
		}
	} else {
		lic, p, _, err = vigolic.LoadDir(vigolic.DefaultLicenseDir)
	}
	if err != nil {
		return err
	}

	// Verify is already done by Decode/LoadDir. Re-running it here gives
	// the operator an explicit signature line in the output.
	if verr := lic.Verify(vigolic.PublicKeyHex); verr != nil {
		fmt.Println("SIGNATURE: COULD NOT VERIFY (public key may not be configured)")
	} else {
		fmt.Println("SIGNATURE: VALID")
	}

	fmt.Println()
	fmt.Printf("License ID:    %s\n", p.LicenseID)
	fmt.Printf("Customer:      %s (%s)\n", p.CustomerName, p.CustomerID)
	fmt.Printf("Tier:          %s\n", p.TierDisplayName())
	fmt.Printf("Max envoys:   %d\n", p.MaxEnvoys)
	fmt.Printf("Issued:        %s\n", p.IssuedAt)
	fmt.Printf("Expires:       %s\n", p.ExpiresAt)
	fmt.Printf("Version:       %d\n", p.Version)

	if exp, err := p.ExpiresTime(); err == nil {
		days := int(time.Until(exp).Hours() / 24)
		if days < 0 {
			fmt.Printf("Status:        EXPIRED (%d days ago)\n", -days)
		} else {
			fmt.Printf("Status:        Valid (%d days remaining)\n", days)
		}
	}

	// Also print raw JSON of the typed payload.
	fmt.Println("\nRaw payload:")
	raw, _ := json.MarshalIndent(p, "  ", "  ")
	fmt.Printf("  %s\n", string(raw))

	return nil
}

// validatedLicense is the result of a full pre-install check on a .lic file.
type validatedLicense struct {
	lic     *license.License
	payload *vigolic.Payload
	data    []byte
}

// validateLicenseFile loads, decodes, and checks a .lic file. When
// checkFingerprint is true, also verifies the machine fingerprint matches
// this server. Returns the parsed payload + raw bytes on success.
func validateLicenseFile(path string, checkFingerprint bool, caFile string) (*validatedLicense, error) {
	lic, err := license.LoadFile(path)
	if err != nil {
		return nil, fmt.Errorf("loading %s: %w", path, err)
	}
	p, err := vigolic.Decode(lic)
	if err != nil {
		return nil, fmt.Errorf("decoding %s: %w", path, err)
	}
	if exp, err := p.ExpiresTime(); err == nil && time.Now().After(exp) {
		return nil, fmt.Errorf("license has already expired (%s)", p.ExpiresAt)
	}
	if checkFingerprint && p.Fingerprint != "" {
		actual, err := vigolic.MachineFingerprint(caFile)
		if err != nil {
			return nil, fmt.Errorf("computing machine fingerprint: %w", err)
		}
		if actual != p.Fingerprint {
			return nil, fmt.Errorf("fingerprint mismatch — license is bound to a different machine\n  expected: %s\n  actual:   %s",
				p.Fingerprint, actual)
		}
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("reading %s: %w", path, err)
	}
	return &validatedLicense{lic: lic, payload: p, data: data}, nil
}

// archiveCurrentLicenses moves any .lic files in destDir into the .archive/
// subdirectory with a UTC timestamp suffix. Refuses when destDir has more
// than one .lic file — the loader picks alphabetically, so rotation against
// a multi-.lic directory would be ambiguous; operator must consolidate first.
// Returns the list of archived destination paths.
func archiveCurrentLicenses(destDir string) ([]string, error) {
	matches, _ := filepath.Glob(filepath.Join(destDir, "*.lic"))
	if len(matches) > 1 {
		sort.Strings(matches)
		return nil, fmt.Errorf("multiple .lic files in %s; consolidate to one file before update:\n  %s",
			destDir, strings.Join(matches, "\n  "))
	}
	if len(matches) == 0 {
		return nil, nil
	}
	archiveDir := filepath.Join(destDir, licenseArchiveSubdir)
	if err := os.MkdirAll(archiveDir, 0700); err != nil {
		return nil, fmt.Errorf("creating archive dir: %w", err)
	}
	ts := time.Now().UTC().Format("2006-01-02T15-04-05Z")
	src := matches[0]
	dst := filepath.Join(archiveDir, filepath.Base(src)+"."+ts)
	if err := os.Rename(src, dst); err != nil {
		return nil, fmt.Errorf("archiving %s: %w", src, err)
	}
	return []string{dst}, nil
}

// pollLicenseReload waits up to 5s for the local vigosrv's /api/v1/license
// endpoint to report the expected license_id. Returns nil on confirmation,
// or a timeout error.
func pollLicenseReload(expectedLicenseID string) error {
	client := newAPIClient()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		var status struct {
			License *struct {
				LicenseID string `json:"license_id"`
			} `json:"license"`
		}
		if err := client.get("/api/v1/license", &status); err == nil {
			if status.License != nil && status.License.LicenseID == expectedLicenseID {
				return nil
			}
		}
		time.Sleep(200 * time.Millisecond)
	}
	return fmt.Errorf("timed out (5s) waiting for license_id=%s to become active", expectedLicenseID)
}

func runLicenseUpdate(cmd *cobra.Command, args []string) error {
	caFile, _ := cmd.Flags().GetString("ca-file")

	fmt.Printf("Verifying %s\n", licenseUpdateFile)
	v, err := validateLicenseFile(licenseUpdateFile, true, caFile)
	if err != nil {
		return err
	}
	fmt.Printf("  signature valid\n")
	fmt.Printf("  fingerprint matches this machine\n")
	fmt.Printf("  license_id=%s, max_envoys=%d, expires=%s\n", v.payload.LicenseID, v.payload.MaxEnvoys, v.payload.ExpiresAt)

	destDir := vigolic.DefaultLicenseDir
	if err := os.MkdirAll(destDir, 0700); err != nil {
		return fmt.Errorf("creating license directory: %w", err)
	}

	archived, err := archiveCurrentLicenses(destDir)
	if err != nil {
		return err
	}
	for _, a := range archived {
		fmt.Printf("  archived prior license: %s\n", a)
	}

	destPath := filepath.Join(destDir, filepath.Base(licenseUpdateFile))
	if err := os.WriteFile(destPath, v.data, 0600); err != nil {
		return fmt.Errorf("writing license file: %w", err)
	}
	fmt.Printf("  installed: %s\n", destPath)

	if err := signalVigosrv(); err != nil {
		fmt.Printf("\nNote: could not signal vigosrv for reload: %v\n", err)
		fmt.Println("Restart the server to apply the new license.")
		return nil
	}
	fmt.Printf("  SIGHUP sent to vigosrv\n")

	if err := pollLicenseReload(v.payload.LicenseID); err != nil {
		fmt.Printf("\nWarning: %v\n", err)
		fmt.Println("File is installed; check `vigocli license status` to confirm.")
		return nil
	}
	fmt.Printf("  reload confirmed: license_id=%s active\n", v.payload.LicenseID)
	return nil
}

func runLicenseVerify(cmd *cobra.Command, args []string) error {
	caFile, _ := cmd.Flags().GetString("ca-file")

	fmt.Printf("Verifying %s\n", licenseVerifyFile)
	v, err := validateLicenseFile(licenseVerifyFile, true, caFile)
	if err != nil {
		return err
	}
	fmt.Printf("  signature valid\n")
	fmt.Printf("  fingerprint matches this machine\n")
	fmt.Printf("  license_id=%s, max_envoys=%d, expires=%s\n", v.payload.LicenseID, v.payload.MaxEnvoys, v.payload.ExpiresAt)
	fmt.Println("\nDry-run only — no changes made.")
	return nil
}

func runLicenseRollback(cmd *cobra.Command, args []string) error {
	archiveDir := filepath.Join(vigolic.DefaultLicenseDir, licenseArchiveSubdir)
	entries, err := os.ReadDir(archiveDir)
	if err != nil {
		if os.IsNotExist(err) {
			return fmt.Errorf("no archive directory found at %s — nothing to roll back to", archiveDir)
		}
		return fmt.Errorf("reading archive directory: %w", err)
	}

	var archives []string
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		// Archive format: <basename>.<UTC-timestamp>, where basename ends in .lic
		if !strings.Contains(e.Name(), ".lic.") {
			continue
		}
		archives = append(archives, e.Name())
	}
	if len(archives) == 0 {
		return fmt.Errorf("no archived licenses in %s", archiveDir)
	}
	sort.Strings(archives)
	mostRecent := archives[len(archives)-1]
	archivePath := filepath.Join(archiveDir, mostRecent)

	fmt.Printf("Rolling back to: %s\n", archivePath)

	v, err := validateLicenseFile(archivePath, true, "/srv/vigo/tls/ca")
	if err != nil {
		return fmt.Errorf("archived license is invalid: %w", err)
	}
	fmt.Printf("  archive valid: license_id=%s\n", v.payload.LicenseID)

	if archived, err := archiveCurrentLicenses(vigolic.DefaultLicenseDir); err != nil {
		return err
	} else {
		for _, a := range archived {
			fmt.Printf("  archived current: %s\n", a)
		}
	}

	// Strip the trailing .<timestamp> suffix to recover the original basename.
	// Archive format guarantees the timestamp is the final extension (UTC
	// format uses dashes, not dots), so filepath.Ext returns it cleanly.
	originalBase := strings.TrimSuffix(mostRecent, filepath.Ext(mostRecent))
	if !strings.HasSuffix(originalBase, ".lic") {
		return fmt.Errorf("unexpected archive filename format: %s", mostRecent)
	}
	destPath := filepath.Join(vigolic.DefaultLicenseDir, originalBase)
	if err := os.WriteFile(destPath, v.data, 0600); err != nil {
		return fmt.Errorf("restoring license: %w", err)
	}
	fmt.Printf("  restored: %s\n", destPath)

	if err := signalVigosrv(); err != nil {
		fmt.Printf("\nNote: could not signal vigosrv for reload: %v\n", err)
		return nil
	}
	fmt.Printf("  SIGHUP sent\n")

	if err := pollLicenseReload(v.payload.LicenseID); err != nil {
		fmt.Printf("\nWarning: %v\n", err)
		return nil
	}
	fmt.Printf("  reload confirmed: license_id=%s active\n", v.payload.LicenseID)
	return nil
}

// signalVigosrv finds the vigosrv process and sends SIGHUP.
func signalVigosrv() error {
	out, err := exec.Command("pgrep", "-x", "vigosrv").Output()
	if err != nil {
		return fmt.Errorf("vigosrv process not found")
	}

	pidStr := strings.TrimSpace(string(out))
	lines := strings.Split(pidStr, "\n")
	if len(lines) == 0 || lines[0] == "" {
		return fmt.Errorf("vigosrv process not found")
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
