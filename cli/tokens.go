package main

import (
	"fmt"

	"github.com/spf13/cobra"
)

var tokensCmd = &cobra.Command{
	Use:   "tokens",
	Short: "Manage enrollment tokens",
}

var tokensGenerateCmd = &cobra.Command{
	Use:   "generate",
	Short: "Generate a new one-time enrollment token",
	RunE: func(cmd *cobra.Command, args []string) error {
		client := newAPIClient()
		var resp struct {
			Token string `json:"token"`
		}
		if err := client.post("/api/v1/tokens", map[string]any{}, &resp); err != nil {
			return err
		}

		fmt.Printf("Token: %s\n", resp.Token)
		fmt.Printf("\nBootstrap command:\n  curl -fsSL %s/bootstrap | HOBNOB_TOKEN=%s bash\n", serverURL, resp.Token)
		return nil
	},
}

func init() {
	tokensCmd.AddCommand(tokensGenerateCmd)
}
