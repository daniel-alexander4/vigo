package main

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"text/tabwriter"

	"github.com/spf13/cobra"
)

var thrallsCmd = &cobra.Command{
	Use:   "thralls",
	Short: "Manage enrolled thralls",
}

var thrallsListCmd = &cobra.Command{
	Use:   "list",
	Short: "List all enrolled thralls",
	RunE: func(cmd *cobra.Command, args []string) error {
		client := newAPIClient()
		var resp struct {
			Thralls []struct {
				ID         string `json:"id"`
				Hostname   string `json:"hostname"`
				IPAddress  string `json:"ipAddress"`
				EnrolledAt string `json:"enrolledAt"`
				LastSeen   string `json:"lastSeen"`
				Revoked    bool   `json:"revoked"`
			} `json:"thralls"`
		}
		if err := client.get("/api/v1/thralls", &resp); err != nil {
			return err
		}

		if len(resp.Thralls) == 0 {
			fmt.Println("No thralls found.")
			return nil
		}

		w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
		fmt.Fprintln(w, "ID\tHOSTNAME\tIP\tENROLLED\tLAST SEEN\tSTATUS")
		for _, c := range resp.Thralls {
			status := "active"
			if c.Revoked {
				status = "revoked"
			}
			fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\n",
				c.ID, c.Hostname, c.IPAddress,
				formatTimestamp(c.EnrolledAt),
				formatTimestamp(c.LastSeen),
				status)
		}
		return w.Flush()
	},
}

var thrallsShowCmd = &cobra.Command{
	Use:   "show <id>",
	Short: "Show details for a thrall",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		client := newAPIClient()
		var resp struct {
			Thrall struct {
				ID         string `json:"id"`
				Hostname   string `json:"hostname"`
				IPAddress  string `json:"ipAddress"`
				EnrolledAt string `json:"enrolledAt"`
				LastSeen   string `json:"lastSeen"`
				Revoked    bool   `json:"revoked"`
				RevokedAt  string `json:"revokedAt"`
			} `json:"thrall"`
		}
		if err := client.get("/api/v1/thralls/"+args[0], &resp); err != nil {
			return err
		}

		c := resp.Thrall
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
		if err := client.get("/api/v1/thralls/"+args[0]+"/compliance", &compResp); err == nil {
			fmt.Printf("Compliance: %s\n", compResp.Status)
		}

		return nil
	},
}

var thrallsRevokeCmd = &cobra.Command{
	Use:   "revoke <id>",
	Short: "Revoke an enrolled thrall",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		client := newAPIClient()
		if err := client.post("/api/v1/thralls/"+args[0]+"/revoke", nil, nil); err != nil {
			return err
		}
		fmt.Printf("Revoked thrall %s\n", args[0])
		return nil
	},
}

var thrallsDeleteCmd = &cobra.Command{
	Use:   "delete <id>",
	Short: "Permanently delete a thrall and all its history",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		client := newAPIClient()
		if err := client.delete("/api/v1/thralls/"+args[0], nil); err != nil {
			return err
		}
		fmt.Printf("Deleted thrall %s\n", args[0])
		return nil
	},
}

var thrallsTraitsCmd = &cobra.Command{
	Use:   "traits <hostname> [dot.path]",
	Short: "View collected traits for a thrall",
	Args:  cobra.RangeArgs(1, 2),
	RunE: func(cmd *cobra.Command, args []string) error {
		hostname := args[0]
		client := newAPIClient()

		var resp struct {
			Hostname    string         `json:"hostname"`
			CollectedAt string         `json:"collected_at"`
			Traits       map[string]any `json:"traits"`
		}
		if err := client.get("/api/v1/thralls/"+hostname+"/traits", &resp); err != nil {
			return err
		}

		// If a dot-path is provided, extract just that value.
		if len(args) == 2 {
			val, ok := traverseDotPath(resp.Traits, args[1])
			if !ok {
				return fmt.Errorf("trait path '%s' not found", args[1])
			}
			fmt.Fprintln(os.Stdout, formatTraitValue(val))
			return nil
		}

		// No dot-path: print the full envelope as pretty JSON.
		data, err := json.MarshalIndent(resp, "", "  ")
		if err != nil {
			return fmt.Errorf("failed to format response: %w", err)
		}
		fmt.Fprintln(os.Stdout, string(data))
		return nil
	},
}

// traverseDotPath walks a nested map using a dot-separated path and returns
// the value at that path. Returns (nil, false) if any segment is missing.
func traverseDotPath(data map[string]any, path string) (any, bool) {
	parts := strings.Split(path, ".")
	var current any = data
	for _, part := range parts {
		m, ok := current.(map[string]any)
		if !ok {
			return nil, false
		}
		current, ok = m[part]
		if !ok {
			return nil, false
		}
	}
	return current, true
}

// formatTraitValue formats a trait value for display. Scalars (string, number,
// bool) are printed plainly; maps and slices are pretty-printed as JSON.
func formatTraitValue(v any) string {
	switch v.(type) {
	case map[string]any, []any:
		data, err := json.MarshalIndent(v, "", "  ")
		if err != nil {
			return fmt.Sprint(v)
		}
		return string(data)
	default:
		return fmt.Sprint(v)
	}
}

func init() {
	thrallsCmd.AddCommand(thrallsListCmd, thrallsShowCmd, thrallsRevokeCmd, thrallsDeleteCmd, thrallsTraitsCmd)
}
