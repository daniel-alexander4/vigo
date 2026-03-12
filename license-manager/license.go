// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: BUSL-1.1

package main

import (
	"crypto/ed25519"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"text/tabwriter"
	"time"

	"github.com/hobnob/hobnob/server/license"
	"github.com/spf13/cobra"
)

var licenseCmd = &cobra.Command{
	Use:   "license",
	Short: "Manage license files",
}

// Flags for license create.
var (
	createCustomer   string
	createCustomerID string
	createMaxThralls int
	createExpires    string
	createOut        string
)

var licenseCreateCmd = &cobra.Command{
	Use:   "create",
	Short: "Generate and sign a new license file",
	RunE:  runLicenseCreate,
}

// Flags for license renew.
var (
	renewLicenseID string
	renewExpires   string
	renewOut       string
)

var licenseRenewCmd = &cobra.Command{
	Use:   "renew",
	Short: "Issue a renewed license with updated expiry",
	RunE:  runLicenseRenew,
}

// Flags for license update-thralls.
var (
	updateLicenseID  string
	updateMaxThralls int
	updateOut        string
)

var licenseUpdateThrallsCmd = &cobra.Command{
	Use:   "update-thralls",
	Short: "Issue a new license with updated thrall count",
	RunE:  runLicenseUpdateThralls,
}

var licenseInspectCmd = &cobra.Command{
	Use:   "inspect <file>",
	Short: "Decode and display a license file",
	Args:  cobra.ExactArgs(1),
	RunE:  runLicenseInspect,
}

var licenseVerifyCmd = &cobra.Command{
	Use:   "verify <file>",
	Short: "Verify the signature of a license file",
	Args:  cobra.ExactArgs(1),
	RunE:  runLicenseVerify,
}

var listExpiringWithin int

var licenseListCmd = &cobra.Command{
	Use:   "list",
	Short: "List all issued licenses",
	RunE:  runLicenseList,
}

func init() {
	licenseCreateCmd.Flags().StringVar(&createCustomer, "customer", "", "Customer name (required)")
	licenseCreateCmd.Flags().StringVar(&createCustomerID, "customer-id", "", "Customer ID (required)")
	licenseCreateCmd.Flags().IntVar(&createMaxThralls, "max-thralls", 0, "Maximum thrall count (required)")
	licenseCreateCmd.Flags().StringVar(&createExpires, "expires", "", "Expiry date YYYY-MM-DD (required)")
	licenseCreateCmd.Flags().StringVar(&createOut, "out", "", "Output file path (required)")
	licenseCreateCmd.MarkFlagRequired("customer")
	licenseCreateCmd.MarkFlagRequired("customer-id")
	licenseCreateCmd.MarkFlagRequired("max-thralls")
	licenseCreateCmd.MarkFlagRequired("expires")
	licenseCreateCmd.MarkFlagRequired("out")

	licenseRenewCmd.Flags().StringVar(&renewLicenseID, "license-id", "", "License ID to renew (required)")
	licenseRenewCmd.Flags().StringVar(&renewExpires, "expires", "", "New expiry date YYYY-MM-DD (required)")
	licenseRenewCmd.Flags().StringVar(&renewOut, "out", "", "Output file path (required)")
	licenseRenewCmd.MarkFlagRequired("license-id")
	licenseRenewCmd.MarkFlagRequired("expires")
	licenseRenewCmd.MarkFlagRequired("out")

	licenseUpdateThrallsCmd.Flags().StringVar(&updateLicenseID, "license-id", "", "License ID to update (required)")
	licenseUpdateThrallsCmd.Flags().IntVar(&updateMaxThralls, "max-thralls", 0, "New max thrall count (required)")
	licenseUpdateThrallsCmd.Flags().StringVar(&updateOut, "out", "", "Output file path (required)")
	licenseUpdateThrallsCmd.MarkFlagRequired("license-id")
	licenseUpdateThrallsCmd.MarkFlagRequired("max-thralls")
	licenseUpdateThrallsCmd.MarkFlagRequired("out")

	licenseListCmd.Flags().IntVar(&listExpiringWithin, "expiring-within", 0, "Only show licenses expiring within N days")

	licenseCmd.AddCommand(licenseCreateCmd)
	licenseCmd.AddCommand(licenseRenewCmd)
	licenseCmd.AddCommand(licenseUpdateThrallsCmd)
	licenseCmd.AddCommand(licenseInspectCmd)
	licenseCmd.AddCommand(licenseVerifyCmd)
	licenseCmd.AddCommand(licenseListCmd)
}

func runLicenseCreate(cmd *cobra.Command, args []string) error {
	if _, err := time.Parse("2006-01-02", createExpires); err != nil {
		return fmt.Errorf("invalid expiry date %q: use YYYY-MM-DD format", createExpires)
	}

	db, err := openDB()
	if err != nil {
		return err
	}
	defer db.Close()

	// Ensure customer exists.
	_, err = db.Exec(
		`INSERT OR IGNORE INTO customers (customer_id, customer_name) VALUES (?, ?)`,
		createCustomerID, createCustomer,
	)
	if err != nil {
		return fmt.Errorf("recording customer: %w", err)
	}

	issuedAt := time.Now().Format("2006-01-02")
	licenseID := fmt.Sprintf("lic-%s-%s", time.Now().Format("20060102"), createCustomerID)

	payload := license.Payload{
		CustomerName: createCustomer,
		CustomerID:   createCustomerID,
		LicenseID:    licenseID,
		MaxThralls:   createMaxThralls,
		IssuedAt:     issuedAt,
		ExpiresAt:    createExpires,
		Version:      1,
	}

	lic, err := signPayload(payload)
	if err != nil {
		return err
	}

	if err := writeLicenseFile(createOut, lic); err != nil {
		return err
	}

	// Record in database.
	_, err = db.Exec(
		`INSERT INTO licenses (license_id, customer_id, max_thralls, issued_at, expires_at, version) VALUES (?, ?, ?, ?, ?, ?)`,
		licenseID, createCustomerID, createMaxThralls, issuedAt, createExpires, 1,
	)
	if err != nil {
		return fmt.Errorf("recording license: %w", err)
	}

	fmt.Printf("License created: %s\n", createOut)
	fmt.Printf("  License ID:  %s\n", licenseID)
	fmt.Printf("  Customer:    %s (%s)\n", createCustomer, createCustomerID)
	fmt.Printf("  Max thralls: %d\n", createMaxThralls)
	fmt.Printf("  Expires:     %s\n", createExpires)
	return nil
}

func runLicenseRenew(cmd *cobra.Command, args []string) error {
	if _, err := time.Parse("2006-01-02", renewExpires); err != nil {
		return fmt.Errorf("invalid expiry date %q: use YYYY-MM-DD format", renewExpires)
	}

	db, err := openDB()
	if err != nil {
		return err
	}
	defer db.Close()

	// Look up existing license.
	var customerID, customerName string
	var maxThralls, version int
	err = db.QueryRow(
		`SELECT l.customer_id, c.customer_name, l.max_thralls, l.version
		 FROM licenses l JOIN customers c ON l.customer_id = c.customer_id
		 WHERE l.license_id = ?`, renewLicenseID,
	).Scan(&customerID, &customerName, &maxThralls, &version)
	if err != nil {
		return fmt.Errorf("license %q not found in database", renewLicenseID)
	}

	issuedAt := time.Now().Format("2006-01-02")
	newVersion := version + 1
	newLicenseID := fmt.Sprintf("lic-%s-%s", time.Now().Format("20060102"), customerID)

	payload := license.Payload{
		CustomerName: customerName,
		CustomerID:   customerID,
		LicenseID:    newLicenseID,
		MaxThralls:   maxThralls,
		IssuedAt:     issuedAt,
		ExpiresAt:    renewExpires,
		Version:      newVersion,
	}

	lic, err := signPayload(payload)
	if err != nil {
		return err
	}

	if err := writeLicenseFile(renewOut, lic); err != nil {
		return err
	}

	// Record new license and mark old as superseded.
	_, err = db.Exec(
		`INSERT INTO licenses (license_id, customer_id, max_thralls, issued_at, expires_at, version) VALUES (?, ?, ?, ?, ?, ?)`,
		newLicenseID, customerID, maxThralls, issuedAt, renewExpires, newVersion,
	)
	if err != nil {
		return fmt.Errorf("recording license: %w", err)
	}
	db.Exec(`UPDATE licenses SET superseded_by = ? WHERE license_id = ?`, newLicenseID, renewLicenseID)

	fmt.Printf("License renewed: %s\n", renewOut)
	fmt.Printf("  New license ID: %s (supersedes %s)\n", newLicenseID, renewLicenseID)
	fmt.Printf("  New expiry:     %s\n", renewExpires)
	return nil
}

func runLicenseUpdateThralls(cmd *cobra.Command, args []string) error {
	db, err := openDB()
	if err != nil {
		return err
	}
	defer db.Close()

	var customerID, customerName, expiresAt string
	var version int
	err = db.QueryRow(
		`SELECT l.customer_id, c.customer_name, l.expires_at, l.version
		 FROM licenses l JOIN customers c ON l.customer_id = c.customer_id
		 WHERE l.license_id = ?`, updateLicenseID,
	).Scan(&customerID, &customerName, &expiresAt, &version)
	if err != nil {
		return fmt.Errorf("license %q not found in database", updateLicenseID)
	}

	issuedAt := time.Now().Format("2006-01-02")
	newVersion := version + 1
	newLicenseID := fmt.Sprintf("lic-%s-%s", time.Now().Format("20060102"), customerID)

	payload := license.Payload{
		CustomerName: customerName,
		CustomerID:   customerID,
		LicenseID:    newLicenseID,
		MaxThralls:   updateMaxThralls,
		IssuedAt:     issuedAt,
		ExpiresAt:    expiresAt,
		Version:      newVersion,
	}

	lic, err := signPayload(payload)
	if err != nil {
		return err
	}

	if err := writeLicenseFile(updateOut, lic); err != nil {
		return err
	}

	_, err = db.Exec(
		`INSERT INTO licenses (license_id, customer_id, max_thralls, issued_at, expires_at, version) VALUES (?, ?, ?, ?, ?, ?)`,
		newLicenseID, customerID, updateMaxThralls, issuedAt, expiresAt, newVersion,
	)
	if err != nil {
		return fmt.Errorf("recording license: %w", err)
	}
	db.Exec(`UPDATE licenses SET superseded_by = ? WHERE license_id = ?`, newLicenseID, updateLicenseID)

	fmt.Printf("License updated: %s\n", updateOut)
	fmt.Printf("  New license ID: %s (supersedes %s)\n", newLicenseID, updateLicenseID)
	fmt.Printf("  Max thralls:    %d\n", updateMaxThralls)
	return nil
}

func runLicenseInspect(cmd *cobra.Command, args []string) error {
	lic, err := license.LoadFile(args[0])
	if err != nil {
		return err
	}

	pubPath := filepath.Join(keyDir, publicKeyFile)
	pubHex, err := os.ReadFile(pubPath)
	if err != nil {
		fmt.Println("WARNING: Could not load public key for verification")
	} else {
		if err := lic.VerifyWithKey(string(pubHex)); err != nil {
			fmt.Printf("SIGNATURE: INVALID (%v)\n", err)
		} else {
			fmt.Println("SIGNATURE: VALID")
		}
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

	return nil
}

func runLicenseVerify(cmd *cobra.Command, args []string) error {
	lic, err := license.LoadFile(args[0])
	if err != nil {
		return err
	}

	pubPath := filepath.Join(keyDir, publicKeyFile)
	pubHex, err := os.ReadFile(pubPath)
	if err != nil {
		return fmt.Errorf("reading public key: %w\nRun 'keys generate' first.", err)
	}

	if err := lic.VerifyWithKey(string(pubHex)); err != nil {
		return fmt.Errorf("INVALID: %w", err)
	}

	fmt.Printf("VALID: %s (license_id: %s)\n", args[0], lic.Payload.LicenseID)
	return nil
}

func runLicenseList(cmd *cobra.Command, args []string) error {
	db, err := openDB()
	if err != nil {
		return err
	}
	defer db.Close()

	query := `SELECT l.license_id, c.customer_name, l.max_thralls, l.issued_at, l.expires_at, l.superseded_by
		FROM licenses l JOIN customers c ON l.customer_id = c.customer_id`
	queryArgs := []any{}

	if listExpiringWithin > 0 {
		query += ` WHERE l.superseded_by IS NULL AND l.expires_at <= date('now', '+' || ? || ' days')`
		queryArgs = append(queryArgs, listExpiringWithin)
	}
	query += ` ORDER BY l.expires_at ASC`

	rows, err := db.Query(query, queryArgs...)
	if err != nil {
		return fmt.Errorf("querying licenses: %w", err)
	}
	defer rows.Close()

	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "LICENSE_ID\tCUSTOMER\tMAX_THRALLS\tISSUED\tEXPIRES\tSTATUS")

	for rows.Next() {
		var id, customer, issued, expires string
		var maxThralls int
		var supersededBy *string
		if err := rows.Scan(&id, &customer, &maxThralls, &issued, &expires, &supersededBy); err != nil {
			return err
		}

		status := "active"
		if supersededBy != nil {
			status = "superseded"
		} else if exp, err := time.Parse("2006-01-02", expires); err == nil && time.Now().After(exp) {
			status = "expired"
		}

		fmt.Fprintf(w, "%s\t%s\t%d\t%s\t%s\t%s\n", id, customer, maxThralls, issued, expires, status)
	}
	return w.Flush()
}

// signPayload signs a license payload with the vendor's private key.
func signPayload(payload license.Payload) (*license.License, error) {
	privPath := filepath.Join(keyDir, privateKeyFile)
	privHex, err := os.ReadFile(privPath)
	if err != nil {
		return nil, fmt.Errorf("reading private key: %w\nRun 'keys generate' first.", err)
	}

	seed, err := hex.DecodeString(string(privHex))
	if err != nil {
		return nil, fmt.Errorf("decoding private key: %w", err)
	}
	privKey := ed25519.NewKeyFromSeed(seed)

	canonical, err := license.CanonicalJSON(payload)
	if err != nil {
		return nil, fmt.Errorf("canonicalizing payload: %w", err)
	}

	sig := ed25519.Sign(privKey, canonical)
	sigB64 := base64.StdEncoding.EncodeToString(sig)

	return &license.License{
		Payload:   payload,
		Signature: sigB64,
	}, nil
}

// writeLicenseFile writes a license to disk as pretty-printed JSON.
func writeLicenseFile(path string, lic *license.License) error {
	data, err := json.MarshalIndent(lic, "", "  ")
	if err != nil {
		return fmt.Errorf("marshaling license: %w", err)
	}
	data = append(data, '\n')

	if err := os.WriteFile(path, data, 0644); err != nil {
		return fmt.Errorf("writing license file: %w", err)
	}
	return nil
}
