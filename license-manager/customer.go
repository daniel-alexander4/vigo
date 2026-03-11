package main

import (
	"fmt"
	"os"
	"text/tabwriter"

	"github.com/spf13/cobra"
)

var customerCmd = &cobra.Command{
	Use:   "customer",
	Short: "Manage customers",
}

var customerListCmd = &cobra.Command{
	Use:   "list",
	Short: "List all customers",
	RunE:  runCustomerList,
}

var customerShowCmd = &cobra.Command{
	Use:   "show <customer-id>",
	Short: "Show a customer's license history",
	Args:  cobra.ExactArgs(1),
	RunE:  runCustomerShow,
}

func init() {
	customerCmd.AddCommand(customerListCmd)
	customerCmd.AddCommand(customerShowCmd)
}

func runCustomerList(cmd *cobra.Command, args []string) error {
	db, err := openDB()
	if err != nil {
		return err
	}
	defer db.Close()

	rows, err := db.Query(`SELECT customer_id, customer_name, created_at FROM customers ORDER BY created_at ASC`)
	if err != nil {
		return fmt.Errorf("querying customers: %w", err)
	}
	defer rows.Close()

	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "CUSTOMER_ID\tNAME\tCREATED")

	for rows.Next() {
		var id, name, created string
		if err := rows.Scan(&id, &name, &created); err != nil {
			return err
		}
		fmt.Fprintf(w, "%s\t%s\t%s\n", id, name, created)
	}
	return w.Flush()
}

func runCustomerShow(cmd *cobra.Command, args []string) error {
	db, err := openDB()
	if err != nil {
		return err
	}
	defer db.Close()

	customerID := args[0]

	var name, created string
	err = db.QueryRow(`SELECT customer_name, created_at FROM customers WHERE customer_id = ?`, customerID).Scan(&name, &created)
	if err != nil {
		return fmt.Errorf("customer %q not found", customerID)
	}

	fmt.Printf("Customer: %s (%s)\n", name, customerID)
	fmt.Printf("Created:  %s\n\n", created)

	rows, err := db.Query(
		`SELECT license_id, max_thralls, issued_at, expires_at, version, superseded_by
		 FROM licenses WHERE customer_id = ? ORDER BY created_at ASC`, customerID,
	)
	if err != nil {
		return fmt.Errorf("querying licenses: %w", err)
	}
	defer rows.Close()

	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "LICENSE_ID\tMAX_THRALLS\tISSUED\tEXPIRES\tVERSION\tSTATUS")

	for rows.Next() {
		var id, issued, expires string
		var maxThralls, version int
		var supersededBy *string
		if err := rows.Scan(&id, &maxThralls, &issued, &expires, &version, &supersededBy); err != nil {
			return err
		}

		status := "active"
		if supersededBy != nil {
			status = fmt.Sprintf("superseded by %s", *supersededBy)
		}

		fmt.Fprintf(w, "%s\t%d\t%s\t%s\t%d\t%s\n", id, maxThralls, issued, expires, version, status)
	}
	return w.Flush()
}
