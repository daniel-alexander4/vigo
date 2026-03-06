package main

import (
	"fmt"
	"os"
	"text/tabwriter"

	"github.com/spf13/cobra"
)

var clientsCmd = &cobra.Command{
	Use:   "clients",
	Short: "Manage enrolled clients",
}

var clientsListCmd = &cobra.Command{
	Use:   "list",
	Short: "List all enrolled clients",
	RunE: func(cmd *cobra.Command, args []string) error {
		client := newAPIClient()
		var resp struct {
			Clients []struct {
				ID         string `json:"id"`
				Hostname   string `json:"hostname"`
				IPAddress  string `json:"ipAddress"`
				EnrolledAt string `json:"enrolledAt"`
				LastSeen   string `json:"lastSeen"`
				Revoked    bool   `json:"revoked"`
			} `json:"clients"`
		}
		if err := client.get("/api/v1/clients", &resp); err != nil {
			return err
		}

		w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
		fmt.Fprintln(w, "HOSTNAME\tIP\tENROLLED\tLAST SEEN\tSTATUS")
		for _, c := range resp.Clients {
			status := "active"
			if c.Revoked {
				status = "revoked"
			}
			fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\n",
				c.Hostname, c.IPAddress,
				formatTimestamp(c.EnrolledAt),
				formatTimestamp(c.LastSeen),
				status)
		}
		return w.Flush()
	},
}

var clientsShowCmd = &cobra.Command{
	Use:   "show <id>",
	Short: "Show details for a client",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		client := newAPIClient()
		var resp struct {
			Client struct {
				ID         string `json:"id"`
				Hostname   string `json:"hostname"`
				IPAddress  string `json:"ipAddress"`
				EnrolledAt string `json:"enrolledAt"`
				LastSeen   string `json:"lastSeen"`
				Revoked    bool   `json:"revoked"`
				RevokedAt  string `json:"revokedAt"`
			} `json:"client"`
		}
		if err := client.get("/api/v1/clients/"+args[0], &resp); err != nil {
			return err
		}

		c := resp.Client
		fmt.Printf("ID:         %s\n", c.ID)
		fmt.Printf("Hostname:   %s\n", c.Hostname)
		fmt.Printf("IP:         %s\n", c.IPAddress)
		fmt.Printf("Enrolled:   %s\n", formatTimestamp(c.EnrolledAt))
		fmt.Printf("Last Seen:  %s\n", formatTimestamp(c.LastSeen))
		status := "active"
		if c.Revoked {
			status = fmt.Sprintf("revoked (%s)", formatTimestamp(c.RevokedAt))
		}
		fmt.Printf("Status:     %s\n", status)

		// Also fetch compliance status
		var compResp struct {
			Status string `json:"status"`
		}
		if err := client.get("/api/v1/clients/"+args[0]+"/compliance", &compResp); err == nil {
			fmt.Printf("Compliance: %s\n", compResp.Status)
		}

		return nil
	},
}

var clientsRevokeCmd = &cobra.Command{
	Use:   "revoke <id>",
	Short: "Revoke an enrolled client",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		client := newAPIClient()
		if err := client.post("/api/v1/clients/"+args[0]+"/revoke", nil, nil); err != nil {
			return err
		}
		fmt.Printf("Revoked client %s\n", args[0])
		return nil
	},
}

func init() {
	clientsCmd.AddCommand(clientsListCmd, clientsShowCmd, clientsRevokeCmd)
}
