package main

import (
	"fmt"

	"github.com/spf13/cobra"
)

var pushCmd = &cobra.Command{
	Use:   "push [hostname-or-uuid]",
	Short: "Trigger an immediate check-in for a node or all nodes",
	RunE: func(cmd *cobra.Command, args []string) error {
		all, _ := cmd.Flags().GetBool("all")
		nodeID := ""
		if !all {
			if len(args) < 1 {
				return fmt.Errorf("specify a hostname or UUID, or use --all")
			}
			nodeID = args[0]
		}

		client := newAPIClient()
		var resp struct {
			NodesNotified int32 `json:"nodesNotified"`
		}
		err := client.post("/api/v1/push", map[string]string{
			"node_id": nodeID,
		}, &resp)
		if err != nil {
			return err
		}

		fmt.Printf("Force push sent — %d node(s) notified\n", resp.NodesNotified)
		return nil
	},
}

func init() {
	pushCmd.Flags().Bool("all", false, "Push to all nodes")
}
