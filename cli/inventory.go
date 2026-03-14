// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

package main

import (
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"strings"
	"text/tabwriter"

	"github.com/spf13/cobra"
)

var inventoryWhere []string
var inventoryShow []string

var inventoryCmd = &cobra.Command{
	Use:   "inventory",
	Short: "Query thralls by traits",
	Long: `Query enrolled thralls by their collected traits.

Examples:
  hobnobcli inventory --where os.distro=ubuntu
  hobnobcli inventory --where os.distro=ubuntu --show network.ip
  hobnobcli inventory --where os.family=debian --where virtual.type=kvm --show os.distro,os.version`,
	RunE: func(cmd *cobra.Command, args []string) error {
		client := newAPIClient()

		// Build query string.
		params := url.Values{}
		for _, w := range inventoryWhere {
			params.Add("where", w)
		}
		if len(inventoryShow) > 0 {
			params.Set("show", strings.Join(inventoryShow, ","))
		}

		path := "/api/v1/inventory"
		if encoded := params.Encode(); encoded != "" {
			path += "?" + encoded
		}

		var resp struct {
			Count   int `json:"count"`
			Thralls []struct {
				Hostname    string         `json:"hostname"`
				IPAddress   string         `json:"ip_address"`
				CollectedAt string         `json:"collected_at"`
				Traits      map[string]any `json:"traits"`
			} `json:"thralls"`
		}
		if err := client.get(path, &resp); err != nil {
			return err
		}

		if resp.Count == 0 {
			fmt.Println("No matching thralls found.")
			return nil
		}

		// If --show was provided, render a table with columns.
		if len(inventoryShow) > 0 {
			return renderTable(resp.Thralls, inventoryShow)
		}

		// Otherwise print JSON.
		data, err := json.MarshalIndent(resp, "", "  ")
		if err != nil {
			return fmt.Errorf("failed to format response: %w", err)
		}
		fmt.Fprintln(os.Stdout, string(data))
		return nil
	},
}

func renderTable(thralls []struct {
	Hostname    string         `json:"hostname"`
	IPAddress   string         `json:"ip_address"`
	CollectedAt string         `json:"collected_at"`
	Traits      map[string]any `json:"traits"`
}, showFields []string) error {
	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)

	// Header: HOSTNAME + each show field.
	header := "HOSTNAME"
	for _, f := range showFields {
		header += "\t" + strings.ToUpper(f)
	}
	fmt.Fprintln(w, header)

	// Rows.
	for _, t := range thralls {
		row := t.Hostname
		for _, f := range showFields {
			val := ""
			if v, ok := t.Traits[f]; ok {
				val = fmt.Sprint(v)
			}
			row += "\t" + val
		}
		fmt.Fprintln(w, row)
	}

	fmt.Fprintf(w, "\n%d thralls\n", len(thralls))
	return w.Flush()
}

func init() {
	inventoryCmd.Flags().StringArrayVar(&inventoryWhere, "where", nil, "Filter by trait (key=value, repeatable)")
	inventoryCmd.Flags().StringSliceVar(&inventoryShow, "show", nil, "Trait fields to display as columns (comma-separated)")
}
