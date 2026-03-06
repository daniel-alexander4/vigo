package main

import (
	"fmt"

	"github.com/spf13/cobra"
)

var pushCmd = &cobra.Command{
	Use:   "push [hostname-or-uuid]",
	Short: "Trigger an immediate check-in for a client or all clients",
	RunE: func(cmd *cobra.Command, args []string) error {
		all, _ := cmd.Flags().GetBool("all")
		clientID := ""
		if !all {
			if len(args) < 1 {
				return fmt.Errorf("specify a hostname or UUID, or use --all")
			}
			clientID = args[0]
		}

		client := newAPIClient()
		var resp struct {
			ClientsNotified int32 `json:"clientsNotified"`
		}
		err := client.post("/api/v1/push", map[string]string{
			"client_id": clientID,
		}, &resp)
		if err != nil {
			return err
		}

		fmt.Printf("Force push sent — %d client(s) notified\n", resp.ClientsNotified)
		return nil
	},
}

func init() {
	pushCmd.Flags().Bool("all", false, "Push to all clients")
}
