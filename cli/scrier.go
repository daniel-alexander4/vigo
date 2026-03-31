// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

package main

import (
	"fmt"
	"os"
	"text/tabwriter"

	"github.com/spf13/cobra"
)

var scrierCmd = &cobra.Command{
	Use:   "scrier",
	Short: "Manage Scrier remote access",
}

var scrierUsersCmd = &cobra.Command{
	Use:   "users",
	Short: "Manage Scrier user-to-OS-user mappings",
}

var scrierUsersListCmd = &cobra.Command{
	Use:   "list",
	Short: "List Scrier user mappings",
	RunE: func(cmd *cobra.Command, args []string) error {
		client := newAPIClient()
		var resp struct {
			Users []struct {
				Username string `json:"username"`
				OSUser   string `json:"os_user"`
			} `json:"users"`
		}
		if err := client.get("/api/v1/scrier/users", &resp); err != nil {
			return err
		}

		if len(resp.Users) == 0 {
			fmt.Println("No Scrier user mappings configured.")
			return nil
		}

		w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
		fmt.Fprintf(w, "%s\t%s\n", header("USERNAME"), header("OS USER"))
		for _, u := range resp.Users {
			osUser := u.OSUser
			if osUser == "" {
				osUser = "-"
			}
			fmt.Fprintf(w, "%s\t%s\n", u.Username, osUser)
		}
		return w.Flush()
	},
}

var scrierUsersMapCmd = &cobra.Command{
	Use:   "map --user <web_username> --os-user <unix_username>",
	Short: "Map a web UI user to an OS username for Scrier SSH connections",
	RunE: func(cmd *cobra.Command, args []string) error {
		username, _ := cmd.Flags().GetString("user")
		if username == "" {
			return fmt.Errorf("--user is required")
		}
		osUser, _ := cmd.Flags().GetString("os-user")
		if osUser == "" {
			return fmt.Errorf("--os-user is required")
		}

		client := newAPIClient()
		if err := client.put("/api/v1/scrier/users/"+username+"/osuser",
			map[string]string{"os_user": osUser}, nil); err != nil {
			return err
		}

		fmt.Printf("Mapped Scrier user %s → OS user %s\n", username, osUser)
		return nil
	},
}

func init() {
	scrierUsersMapCmd.Flags().String("user", "", "Web UI username")
	scrierUsersMapCmd.Flags().String("os-user", "", "Unix username on target envoy")

	scrierUsersCmd.AddCommand(scrierUsersListCmd, scrierUsersMapCmd)
	scrierCmd.AddCommand(scrierUsersCmd)
}
