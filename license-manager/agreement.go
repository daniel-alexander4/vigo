// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

package main

import (
	"bytes"
	"embed"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"text/tabwriter"
	"text/template"
	"time"

	"github.com/charmbracelet/huh"
	"github.com/go-pdf/fpdf"
	"github.com/spf13/cobra"
)

//go:embed templates/*.tmpl
var templateFS embed.FS

var agreementCmd = &cobra.Command{
	Use:   "agreement",
	Short: "Generate and manage license agreements",
}

var agreementGenerateCmd = &cobra.Command{
	Use:   "generate",
	Short: "Interactive wizard to generate a license agreement",
	RunE:  runAgreementGenerate,
}

var agreementListCmd = &cobra.Command{
	Use:   "list",
	Short: "List all generated agreements",
	RunE:  runAgreementList,
}

var agreementShowCmd = &cobra.Command{
	Use:   "show <agreement-id>",
	Short: "Show details of an agreement",
	Args:  cobra.ExactArgs(1),
	RunE:  runAgreementShow,
}

func init() {
	agreementCmd.AddCommand(agreementGenerateCmd)
	agreementCmd.AddCommand(agreementListCmd)
	agreementCmd.AddCommand(agreementShowCmd)
}

// agreementData holds all template variables for agreement rendering.
type agreementData struct {
	AgreementID   string
	Date          string
	CustomerName  string
	CustomerID    string
	ContactName   string
	ContactEmail  string
	Tier          string
	NodeCount     int
	TermMonths    int
	AnnualFee     string
	SupportLevel  string
	SpecialTerms  string
	AgreementType string
}

// tierPricing returns per-node annual price for a tier.
func tierPricing(tier string, nodeCount int) int {
	switch {
	case nodeCount <= 25:
		return 0
	case nodeCount <= 250:
		return 144
	case nodeCount <= 1000:
		return 132
	case nodeCount <= 5000:
		return 120
	case nodeCount <= 25000:
		return 108
	default:
		return 84
	}
}

// tierName returns the display name for a node count.
func tierName(nodeCount int) string {
	switch {
	case nodeCount <= 25:
		return "Community"
	case nodeCount <= 250:
		return "Starter"
	case nodeCount <= 1000:
		return "Growth"
	case nodeCount <= 5000:
		return "Business"
	case nodeCount <= 25000:
		return "Enterprise"
	default:
		return "Scale"
	}
}

// supportLevel returns the support level for a tier.
func supportLevel(nodeCount int) string {
	switch {
	case nodeCount <= 25:
		return "Community (best effort)"
	case nodeCount <= 1000:
		return "Standard (2 business days)"
	case nodeCount <= 5000:
		return "Priority (1 business day)"
	default:
		return "Dedicated (4 business hours)"
	}
}

func runAgreementGenerate(cmd *cobra.Command, args []string) error {
	db, err := openDB()
	if err != nil {
		return err
	}
	defer db.Close()

	var data agreementData
	data.Date = time.Now().Format("2006-01-02")

	// Step 1: Agreement type
	var agreementType string
	err = huh.NewForm(
		huh.NewGroup(
			huh.NewSelect[string]().
				Title("Agreement Type").
				Options(
					huh.NewOption("Commercial License Agreement", "commercial"),
					huh.NewOption("Design Partner Agreement", "design-partner"),
					huh.NewOption("Non-Disclosure Agreement (NDA)", "nda"),
				).
				Value(&agreementType),
		),
	).Run()
	if err != nil {
		return err
	}
	data.AgreementType = agreementType

	// Step 2: Check for existing customers
	var useExisting bool
	rows, err := db.Query(`SELECT customer_id, customer_name FROM customers ORDER BY customer_name ASC`)
	if err != nil {
		return fmt.Errorf("querying customers: %w", err)
	}

	type existingCustomer struct {
		ID   string
		Name string
	}
	var customers []existingCustomer
	for rows.Next() {
		var c existingCustomer
		if err := rows.Scan(&c.ID, &c.Name); err != nil {
			rows.Close()
			return err
		}
		customers = append(customers, c)
	}
	rows.Close()

	if len(customers) > 0 {
		err = huh.NewForm(
			huh.NewGroup(
				huh.NewConfirm().
					Title("Use an existing customer?").
					Value(&useExisting),
			),
		).Run()
		if err != nil {
			return err
		}
	}

	if useExisting && len(customers) > 0 {
		var opts []huh.Option[string]
		for _, c := range customers {
			opts = append(opts, huh.NewOption(fmt.Sprintf("%s (%s)", c.Name, c.ID), c.ID))
		}

		err = huh.NewForm(
			huh.NewGroup(
				huh.NewSelect[string]().
					Title("Select Customer").
					Options(opts...).
					Value(&data.CustomerID),
			),
		).Run()
		if err != nil {
			return err
		}

		// Look up the name.
		for _, c := range customers {
			if c.ID == data.CustomerID {
				data.CustomerName = c.Name
				break
			}
		}
	} else {
		err = huh.NewForm(
			huh.NewGroup(
				huh.NewInput().
					Title("Customer Name").
					Placeholder("Acme Corp").
					Value(&data.CustomerName),
				huh.NewInput().
					Title("Customer ID").
					Placeholder("acme").
					Value(&data.CustomerID),
			),
		).Run()
		if err != nil {
			return err
		}
	}

	// Step 3: Contact info
	err = huh.NewForm(
		huh.NewGroup(
			huh.NewInput().
				Title("Contact Name").
				Placeholder("Jane Smith").
				Value(&data.ContactName),
			huh.NewInput().
				Title("Contact Email").
				Placeholder("jane@acme.com").
				Value(&data.ContactEmail),
		),
	).Run()
	if err != nil {
		return err
	}

	// Step 4: Type-specific fields
	switch agreementType {
	case "commercial":
		nodeCountStr := ""
		termStr := ""
		err = huh.NewForm(
			huh.NewGroup(
				huh.NewInput().
					Title("Node Count").
					Placeholder("500").
					Value(&nodeCountStr),
				huh.NewInput().
					Title("Term (months)").
					Placeholder("12").
					Value(&termStr),
			),
		).Run()
		if err != nil {
			return err
		}

		if _, err := fmt.Sscanf(nodeCountStr, "%d", &data.NodeCount); err != nil {
			return fmt.Errorf("invalid node count: %w", err)
		}
		if _, err := fmt.Sscanf(termStr, "%d", &data.TermMonths); err != nil {
			return fmt.Errorf("invalid term: %w", err)
		}

		data.Tier = tierName(data.NodeCount)
		perNode := tierPricing(data.Tier, data.NodeCount)
		data.AnnualFee = fmt.Sprintf("$%s/year", formatNumber(data.NodeCount*perNode))
		data.SupportLevel = supportLevel(data.NodeCount)

	case "design-partner":
		data.Tier = "Design Partner (unlimited)"
		data.NodeCount = 0
		data.TermMonths = 0
		data.AnnualFee = "N/A (perpetual, no fee)"
		data.SupportLevel = "Priority (best-effort, comparable to Business tier)"

	case "nda":
		termStr := ""
		err = huh.NewForm(
			huh.NewGroup(
				huh.NewInput().
					Title("NDA Term (months)").
					Placeholder("24").
					Value(&termStr),
			),
		).Run()
		if err != nil {
			return err
		}
		if _, err := fmt.Sscanf(termStr, "%d", &data.TermMonths); err != nil {
			return fmt.Errorf("invalid term: %w", err)
		}
	}

	// Step 5: Special terms
	err = huh.NewForm(
		huh.NewGroup(
			huh.NewText().
				Title("Special Terms (optional)").
				Placeholder("Any additional terms or conditions...").
				Value(&data.SpecialTerms),
		),
	).Run()
	if err != nil {
		return err
	}

	// Generate agreement ID.
	data.AgreementID = fmt.Sprintf("agr-%s-%s-%s", agreementType, time.Now().Format("20060102"), data.CustomerID)

	// Step 6: Preview and confirm
	preview := renderPreview(data)
	fmt.Println(preview)

	var confirmed bool
	err = huh.NewForm(
		huh.NewGroup(
			huh.NewConfirm().
				Title("Generate this agreement?").
				Value(&confirmed),
		),
	).Run()
	if err != nil {
		return err
	}
	if !confirmed {
		fmt.Println("Cancelled.")
		return nil
	}

	// Render template.
	tmplFile := fmt.Sprintf("templates/%s.md.tmpl", agreementType)
	tmplData, err := templateFS.ReadFile(tmplFile)
	if err != nil {
		return fmt.Errorf("reading template %s: %w", tmplFile, err)
	}

	tmpl, err := template.New("agreement").Parse(string(tmplData))
	if err != nil {
		return fmt.Errorf("parsing template: %w", err)
	}

	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, data); err != nil {
		return fmt.Errorf("executing template: %w", err)
	}

	// Write markdown.
	agreementsDir := filepath.Join(keyDir, "agreements")
	if err := os.MkdirAll(agreementsDir, 0700); err != nil {
		return fmt.Errorf("creating agreements dir: %w", err)
	}

	mdFilename := fmt.Sprintf("%s.md", data.AgreementID)
	mdPath := filepath.Join(agreementsDir, mdFilename)
	if err := os.WriteFile(mdPath, buf.Bytes(), 0600); err != nil {
		return fmt.Errorf("writing markdown: %w", err)
	}

	// Write PDF.
	pdfFilename := fmt.Sprintf("%s.pdf", data.AgreementID)
	pdfPath := filepath.Join(agreementsDir, pdfFilename)
	if err := renderPDF(buf.String(), pdfPath); err != nil {
		return fmt.Errorf("writing PDF: %w", err)
	}

	// Ensure customer exists.
	_, err = db.Exec(
		`INSERT OR IGNORE INTO customers (customer_id, customer_name) VALUES (?, ?)`,
		data.CustomerID, data.CustomerName,
	)
	if err != nil {
		return fmt.Errorf("recording customer: %w", err)
	}

	// Record agreement.
	_, err = db.Exec(
		`INSERT INTO agreements (agreement_id, customer_id, agreement_type, tier, node_count, term_months, contact_name, contact_email, special_terms, md_path, pdf_path, status)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 'draft')`,
		data.AgreementID, data.CustomerID, agreementType, data.Tier, data.NodeCount, data.TermMonths,
		data.ContactName, data.ContactEmail, data.SpecialTerms, mdPath, pdfPath,
	)
	if err != nil {
		return fmt.Errorf("recording agreement: %w", err)
	}

	fmt.Printf("\nAgreement generated:\n")
	fmt.Printf("  Agreement ID: %s\n", data.AgreementID)
	fmt.Printf("  Markdown:     %s\n", mdPath)
	fmt.Printf("  PDF:          %s\n", pdfPath)
	fmt.Printf("  Status:       draft\n")
	fmt.Printf("\nReview the markdown, make edits, then move the final PDF to the finals/ directory.\n")
	return nil
}

func renderPreview(data agreementData) string {
	var sb strings.Builder
	sb.WriteString("\n--- Agreement Preview ---\n\n")
	sb.WriteString(fmt.Sprintf("  Type:          %s\n", data.AgreementType))
	sb.WriteString(fmt.Sprintf("  Agreement ID:  %s\n", data.AgreementID))
	sb.WriteString(fmt.Sprintf("  Date:          %s\n", data.Date))
	sb.WriteString(fmt.Sprintf("  Customer:      %s (%s)\n", data.CustomerName, data.CustomerID))
	sb.WriteString(fmt.Sprintf("  Contact:       %s <%s>\n", data.ContactName, data.ContactEmail))
	if data.AgreementType == "commercial" {
		sb.WriteString(fmt.Sprintf("  Tier:          %s\n", data.Tier))
		sb.WriteString(fmt.Sprintf("  Node Count:    %d\n", data.NodeCount))
		sb.WriteString(fmt.Sprintf("  Term:          %d months\n", data.TermMonths))
		sb.WriteString(fmt.Sprintf("  Annual Fee:    %s\n", data.AnnualFee))
		sb.WriteString(fmt.Sprintf("  Support:       %s\n", data.SupportLevel))
	} else if data.AgreementType == "nda" {
		sb.WriteString(fmt.Sprintf("  Term:          %d months\n", data.TermMonths))
	} else {
		sb.WriteString(fmt.Sprintf("  License:       Perpetual, unlimited nodes\n"))
	}
	if data.SpecialTerms != "" {
		sb.WriteString(fmt.Sprintf("  Special Terms: %s\n", data.SpecialTerms))
	}
	sb.WriteString("\n-------------------------\n")
	return sb.String()
}

// formatNumber adds comma separators to an integer.
func formatNumber(n int) string {
	s := fmt.Sprintf("%d", n)
	if len(s) <= 3 {
		return s
	}
	var result []byte
	for i, c := range s {
		if i > 0 && (len(s)-i)%3 == 0 {
			result = append(result, ',')
		}
		result = append(result, byte(c))
	}
	return string(result)
}

// renderPDF renders plain text content to a PDF file.
func renderPDF(content string, outPath string) error {
	pdf := fpdf.New("P", "mm", "Letter", "")
	pdf.SetMargins(25, 25, 25)
	pdf.SetAutoPageBreak(true, 25)
	pdf.AddPage()

	lines := strings.Split(content, "\n")
	for _, line := range lines {
		if line == "" {
			pdf.Ln(4)
			continue
		}

		// Detect heading-like lines (all caps, short).
		isHeading := len(line) > 3 && line == strings.ToUpper(line) && len(line) < 80

		if isHeading {
			pdf.SetFont("Helvetica", "B", 12)
			pdf.MultiCell(0, 5, line, "", "L", false)
			pdf.Ln(2)
		} else if strings.HasPrefix(line, "___") {
			// Signature line.
			pdf.Ln(6)
			pdf.SetFont("Helvetica", "", 10)
			pdf.Cell(0, 5, "________________________________________")
			pdf.Ln(5)
		} else {
			// Detect section numbers (e.g., "1.", "10.").
			trimmed := strings.TrimSpace(line)
			isSectionHead := false
			for i, c := range trimmed {
				if c == '.' && i > 0 {
					isSectionHead = true
					break
				}
				if c < '0' || c > '9' {
					break
				}
			}

			if isSectionHead && strings.ToUpper(trimmed) == trimmed {
				pdf.SetFont("Helvetica", "B", 11)
			} else {
				pdf.SetFont("Helvetica", "", 10)
			}
			pdf.MultiCell(0, 5, line, "", "L", false)
		}
	}

	return pdf.OutputFileAndClose(outPath)
}

func runAgreementList(cmd *cobra.Command, args []string) error {
	db, err := openDB()
	if err != nil {
		return err
	}
	defer db.Close()

	rows, err := db.Query(
		`SELECT a.agreement_id, c.customer_name, a.agreement_type, a.status, a.generated_at
		 FROM agreements a JOIN customers c ON a.customer_id = c.customer_id
		 ORDER BY a.generated_at DESC`,
	)
	if err != nil {
		return fmt.Errorf("querying agreements: %w", err)
	}
	defer rows.Close()

	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "AGREEMENT_ID\tCUSTOMER\tTYPE\tSTATUS\tGENERATED")

	for rows.Next() {
		var id, customer, aType, status, generated string
		if err := rows.Scan(&id, &customer, &aType, &status, &generated); err != nil {
			return err
		}
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\n", id, customer, aType, status, generated)
	}
	return w.Flush()
}

func runAgreementShow(cmd *cobra.Command, args []string) error {
	db, err := openDB()
	if err != nil {
		return err
	}
	defer db.Close()

	agreementID := args[0]

	var customerID, customerName, aType, tier, contactName, contactEmail, specialTerms, mdPath, pdfPath, status, generated string
	var nodeCount, termMonths int
	var licenseID *string

	err = db.QueryRow(
		`SELECT a.customer_id, c.customer_name, a.agreement_type, a.tier, a.node_count, a.term_months,
		        a.contact_name, a.contact_email, a.special_terms, a.md_path, a.pdf_path, a.status,
		        a.generated_at, a.license_id
		 FROM agreements a JOIN customers c ON a.customer_id = c.customer_id
		 WHERE a.agreement_id = ?`, agreementID,
	).Scan(&customerID, &customerName, &aType, &tier, &nodeCount, &termMonths,
		&contactName, &contactEmail, &specialTerms, &mdPath, &pdfPath, &status,
		&generated, &licenseID,
	)
	if err != nil {
		return fmt.Errorf("agreement %q not found", agreementID)
	}

	fmt.Printf("Agreement: %s\n", agreementID)
	fmt.Printf("Type:      %s\n", aType)
	fmt.Printf("Status:    %s\n", status)
	fmt.Printf("Generated: %s\n\n", generated)
	fmt.Printf("Customer:  %s (%s)\n", customerName, customerID)
	fmt.Printf("Contact:   %s <%s>\n", contactName, contactEmail)
	if aType == "commercial" {
		fmt.Printf("Tier:      %s\n", tier)
		fmt.Printf("Nodes:     %d\n", nodeCount)
		fmt.Printf("Term:      %d months\n", termMonths)
	} else if aType == "nda" {
		fmt.Printf("Term:      %d months\n", termMonths)
	}
	if specialTerms != "" {
		fmt.Printf("Special:   %s\n", specialTerms)
	}
	fmt.Printf("\nMarkdown:  %s\n", mdPath)
	fmt.Printf("PDF:       %s\n", pdfPath)
	if licenseID != nil {
		fmt.Printf("License:   %s\n", *licenseID)
	}

	return nil
}
