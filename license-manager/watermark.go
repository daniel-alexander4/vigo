package main

import (
	"fmt"
	"os"

	"github.com/daniel-alexander4/vigo/server/license"
	"github.com/spf13/cobra"
)

var licenseDecodeWatermarkCmd = &cobra.Command{
	Use:   "decode-watermark <file>",
	Short: "Decode a watermarked document to identify the source license",
	Long: `Scans a text file (copy-pasted documentation, saved HTML, raw text) for
an invisible watermark embedded by the Vigo documentation system. If found,
decodes the license ID and looks it up in the license database to identify
the customer.`,
	Args: cobra.ExactArgs(1),
	RunE: runDecodeWatermark,
}

func init() {
	licenseCmd.AddCommand(licenseDecodeWatermarkCmd)
}

func runDecodeWatermark(cmd *cobra.Command, args []string) error {
	data, err := os.ReadFile(args[0])
	if err != nil {
		return fmt.Errorf("reading file: %w", err)
	}

	licenseID := license.DecodeWatermark(string(data))
	if licenseID == "" {
		fmt.Println("No watermark detected in file.")
		return nil
	}

	fmt.Printf("Watermark found:\n")
	fmt.Printf("  License ID:  %s\n", licenseID)

	// Look up in license database.
	db, err := openDB()
	if err != nil {
		fmt.Printf("  (could not open license database: %v)\n", err)
		return nil
	}
	defer db.Close()

	var customerName, customerID, tier, issuedAt, expiresAt string
	var maxEnvoys int
	var supersededBy *string
	err = db.QueryRow(
		`SELECT c.customer_name, l.customer_id, l.tier, l.max_envoys, l.issued_at, l.expires_at, l.superseded_by
		 FROM licenses l JOIN customers c ON l.customer_id = c.customer_id
		 WHERE l.license_id = ?`, licenseID,
	).Scan(&customerName, &customerID, &tier, &maxEnvoys, &issuedAt, &expiresAt, &supersededBy)
	if err != nil {
		fmt.Printf("  (not found in license database)\n")
		return nil
	}

	status := "active"
	if supersededBy != nil {
		status = "superseded"
	}

	fmt.Printf("  Customer:    %s (%s)\n", customerName, customerID)
	fmt.Printf("  Tier:        %s\n", tier)
	fmt.Printf("  Max envoys:  %d\n", maxEnvoys)
	fmt.Printf("  Issued:      %s\n", issuedAt)
	fmt.Printf("  Expires:     %s\n", expiresAt)
	fmt.Printf("  Status:      %s\n", status)

	return nil
}
